package searchrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type redirectToFake struct {
	host string
	base http.RoundTripper
}

func (r redirectToFake) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.URL.Scheme = "http"
	copy.URL.Host = r.host
	copy.Host = r.host
	return r.base.RoundTrip(copy)
}
func fixture(t *testing.T, handler http.HandlerFunc) (*Router, *atomic.Int32, *httptest.Server) {
	t.Helper()
	counter := new(atomic.Int32)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		handler(w, r)
	}))
	r := &Router{BraveKey: "brave-test", TavilyKey: "tavily-test", Client: &http.Client{Timeout: 4 * time.Second, Transport: redirectToFake{host: strings.TrimPrefix(s.URL, "http://"), base: http.DefaultTransport}}}
	return r, counter, s
}
func braveOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"grounding":{"generic":[{"title":"Новости","url":"https://site.example/a","snippets":["Короткая сводка","Ещё факт"]}]},"sources":{}}`)
}
func tavilyOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"results":[{"title":"Другой источник","url":"https://another.example/b","content":"Сведения из поиска"}],"usage":{"credits":1}}`)
}
func TestBravePreferredSingleRequest(t *testing.T) {
	r, n, s := fixture(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/res/v1/llm/context" || req.Header.Get("X-Subscription-Token") != "brave-test" {
			t.Errorf("incorrect Brave endpoint/auth: %s", req.URL.Path)
		}
		if req.Header.Get("Authorization") != "" {
			t.Error("key leaked into wrong header")
		}
		braveOK(w)
	})
	defer s.Close()
	v, e := r.Search(context.Background(), Query{Text: "как работает свет", Freshness: "week"})
	if e != nil || v.Provider != Brave || v.Fallback || len(v.Sources) != 1 || n.Load() != 1 {
		t.Fatalf("unexpected search: %+v %v calls=%d", v, e, n.Load())
	}
	if r.Status().Fallbacks != 0 {
		t.Fatal("unexpected fallback")
	}
}
func TestQuotaSwitchAndStickyUntilReset(t *testing.T) {
	current := time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
	braveCalls := 0
	tavilyCalls := 0
	r, _, s := fixture(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/res/v1/llm/context" {
			braveCalls++
			w.Header().Set("X-RateLimit-Reset", "1,3600")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"code":"QUOTA_LIMITED"}}`)
		} else if req.URL.Path == "/search" {
			tavilyCalls++
			if req.Header.Get("Authorization") != "Bearer tavily-test" {
				t.Error("Tavily token absent")
			}
			tavilyOK(w)
		} else {
			t.Error("unexpected endpoint")
		}
	})
	defer s.Close()
	r.Now = func() time.Time { return current }
	for i := 0; i < 2; i++ {
		v, e := r.Search(context.Background(), Query{Text: "поиск в интернете"})
		if e != nil || v.Provider != Tavily || !v.Fallback {
			t.Fatalf("fallback result %+v %v", v, e)
		}
	}
	if braveCalls != 1 || tavilyCalls != 2 {
		t.Fatalf("quota was not cached: brave %d tavily %d", braveCalls, tavilyCalls)
	}
	if r.Status().Fallbacks != 2 {
		t.Fatal("fallback count failed")
	}
	current = current.Add(2 * time.Hour)
	_, _ = r.Search(context.Background(), Query{Text: "поиск третий"})
	if braveCalls != 2 {
		t.Fatal("brave quota did not automatically reset")
	}
}
func TestTavilyQuotaWhenBraveUnavailable(t *testing.T) {
	current := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tavilyCalls := 0
	r, _, s := fixture(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/search" {
			tavilyCalls++
			w.WriteHeader(432)
			io.WriteString(w, `{"detail":{"error":"Plan Limit Exceeded"}}`)
		}
	})
	defer s.Close()
	r.BraveKey = ""
	r.Now = func() time.Time { return current }
	for i := 0; i < 2; i++ {
		_, e := r.Search(context.Background(), Query{Text: "hello"})
		if !errors.Is(e, ErrNoSearchAvailable) {
			t.Fatalf("got %v", e)
		}
	}
	if tavilyCalls != 1 {
		t.Fatalf("monthly exhaustion should suppress second call, got %d", tavilyCalls)
	}
	current = current.AddDate(0, 1, 0)
	_, _ = r.Search(context.Background(), Query{Text: "next month"})
	if tavilyCalls != 2 {
		t.Fatal("reset of Tavily next month failed")
	}
}
func TestNoFallbackForBraveEmptyStillUsesTavily(t *testing.T) {
	r, n, s := fixture(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/res/v1/llm/context" {
			io.WriteString(w, `{"grounding":{"generic":[]}}`)
		} else {
			tavilyOK(w)
		}
	})
	defer s.Close()
	v, e := r.Search(context.Background(), Query{Text: "новости"})
	if e != nil || !v.Fallback || v.Provider != Tavily || n.Load() != 2 {
		t.Fatalf("fallback: %+v %v calls=%d", v, e, n.Load())
	}
}
func TestNoQueryLeakAndBadInputs(t *testing.T) {
	r, n, s := fixture(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("q") != "" {
			t.Error("query should not be in URL")
		}
		braveOK(w)
	})
	defer s.Close()
	for _, s := range []string{"", "   ", strings.Repeat("x", 600)} {
		if _, e := r.Search(context.Background(), Query{Text: s}); !errors.Is(e, ErrInvalidQuery) {
			t.Fatalf("bad input accepted %q", s)
		}
	}
	if n.Load() != 0 {
		t.Fatal("made request for invalid query")
	}
	v, e := r.Search(context.Background(), Query{Text: "русский поиск"})
	if e != nil || v.Provider != Brave {
		t.Fatal("valid query failed")
	}
}
func TestBraveDoesNotPersistContent(t *testing.T) {
	r, _, s := fixture(t, func(w http.ResponseWriter, req *http.Request) { braveOK(w) })
	defer s.Close()
	_, e := r.Search(context.Background(), Query{Text: "hello"})
	if e != nil {
		t.Fatal(e)
	}
	status := r.Status()
	if status.Attempts[Brave] != 1 || status.Fallbacks != 0 {
		t.Fatal("unexpected counters")
	}
	// Status struct deliberately has no SearchResult/Source/Query fields.
}
