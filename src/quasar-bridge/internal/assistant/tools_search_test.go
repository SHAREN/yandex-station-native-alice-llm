package assistant

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"quasarbridge/internal/assistant/searchrouter"
)

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestVoiceWebSearchUsesOneProvider(t *testing.T) {
	calls := 0
	t.Setenv("QUASAR_SEARCH_ENABLED", "0") // explicit injected router works for test only.
	mock := &searchrouter.Router{
		BraveKey:  "mock-brave-key",
		TavilyKey: "mock-tavily-key",
		Client: searchTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Host != "api.search.brave.com" || r.Header.Get("X-Subscription-Token") != "mock-brave-key" {
				t.Error("unexpected search request target or authentication")
			}
			return &http.Response{
				StatusCode: 200, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"grounding":{"generic":[{"title":"Тест","url":"https://site.example/news","snippets":["Новости дня"]}]}}`)),
			}, nil
		}),
	}
	tools := &Tools{Config: Config{Search: true}, SearchRouter: mock}
	defs := tools.Definitions()
	found := 0
	for _, d := range defs {
		if d.Function.Name == "web_search" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("expected one web_search, got %d", found)
	}
	got, err := tools.Call(context.Background(), "web_search", `{"query":"последние новости","freshness":"day"}`)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.(searchrouter.Result)
	if !ok || result.Provider != searchrouter.Brave || result.Fallback || len(result.Sources) != 1 || calls != 1 {
		t.Fatalf("result=%+v calls=%d", got, calls)
	}
	if _, err := tools.Call(context.Background(), "web_search", `{"query":"hello","extraneous":"bad"}`); err == nil {
		t.Fatal("unrecognized argument accepted")
	}
}
func TestVoiceSearchIsOffByDefault(t *testing.T) {
	t.Setenv("QUASAR_SEARCH_ENABLED", "1")
	t.Setenv("BRAVE_SEARCH_API_KEY", "mock-only")
	t.Setenv("TAVILY_API_KEY", "mock-only")
	tools := &Tools{Config: DefaultConfig()} // Search flag must be explicitly enabled in config.
	for _, def := range tools.Definitions() {
		if def.Function.Name == "web_search" {
			t.Fatal("search advertised by default")
		}
	}
	if _, err := tools.Call(context.Background(), "web_search", `{"query":"hello"}`); err == nil {
		t.Fatal("disabled search executed")
	}
}
func TestVoiceSearchNoSecretsOrProviderWhenUnconfigured(t *testing.T) {
	t.Setenv("QUASAR_SEARCH_ENABLED", "1")
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "")
	tools := &Tools{Config: Config{Search: true}}
	for _, def := range tools.Definitions() {
		if def.Function.Name == "web_search" {
			t.Fatal("missing keys still advertised")
		}
	}
	got, err := tools.Call(context.Background(), "web_search", `{"query":"anything"}`)
	if got != nil || err == nil {
		t.Fatalf("expected off/absent tool, got %v %v", got, err)
	}
}
func TestVoiceSearchBlockedAfterPrivateHistory(t *testing.T) {
	a := &Agent{}
	if a.allowedAfterHistory("web_search") {
		t.Fatal("search exposed after private history")
	}
	if a.allowedAfterHistory("read_webpage") {
		t.Fatal("web reader exposed after history")
	}
	if !a.allowedAfterHistory("current_time") {
		t.Fatal("clock should still work")
	}
}
