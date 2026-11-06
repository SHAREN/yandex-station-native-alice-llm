// Package searchrouter provides a zero-disk, provider-agnostic search failover.
//
// Preferred: Brave LLM Context. Secondary: Tavily basic Search.
// This package intentionally does not configure the station or add a second
// speech/LLM engine. A caller must explicitly opt in before using it.
// Search content exists only in memory for the current user request.
package searchrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	Brave            = "brave"
	Tavily           = "tavily"
	maxResponseBytes = 3 << 20
)

type Query struct {
	Text      string
	Freshness string // "", "day", "week", "month", "year"
}

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type Result struct {
	Provider  string   `json:"provider"`
	Sources   []Source `json:"sources"`
	Credits   int      `json:"credits,omitempty"`
	ElapsedMS int64    `json:"elapsed_ms"`
	Fallback  bool     `json:"fallback"`
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Router struct {
	BraveKey, TavilyKey string
	Client              HTTPDoer
	Now                 func() time.Time
	mu                  sync.Mutex
	blocked             map[string]time.Time
	attempts            map[string]int
	fallbacks           int
}

// Status contains only non-content metadata, so it can be logged safely.
type Status struct {
	Preferred string               `json:"preferred"`
	Blocked   map[string]time.Time `json:"blocked,omitempty"`
	Attempts  map[string]int       `json:"attempts"`
	Fallbacks int                  `json:"fallbacks"`
}

func (r *Router) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := make(map[string]time.Time, len(r.blocked))
	for k, v := range r.blocked {
		b[k] = v
	}
	a := make(map[string]int, len(r.attempts))
	for k, v := range r.attempts {
		a[k] = v
	}
	return Status{Preferred: Brave, Blocked: b, Attempts: a, Fallbacks: r.fallbacks}
}
func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *Router) httpClient() HTTPDoer {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (r *Router) ready(provider string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.blocked[provider]; ok {
		return !r.now().Before(b)
	}
	return true
}
func (r *Router) block(provider string, until time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blocked == nil {
		r.blocked = make(map[string]time.Time)
	}
	r.blocked[provider] = until
}
func (r *Router) count(provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempts == nil {
		r.attempts = make(map[string]int)
	}
	r.attempts[provider]++
}
func (r *Router) fallback() { r.mu.Lock(); defer r.mu.Unlock(); r.fallbacks++ }

func nextCalendarMonth(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}
func requestValid(q Query) bool {
	s := strings.TrimSpace(q.Text)
	if s == "" || utf8.RuneCountInString(s) > 550 || strings.Count(s, " ") > 74 {
		return false
	}
	switch q.Freshness {
	case "", "day", "week", "month", "year":
		return true
	}
	return false
}

var ErrNoSearchAvailable = errors.New("no configured search provider returned results")
var ErrInvalidQuery = errors.New("invalid search query")
var ErrQuota = errors.New("provider quota exhausted")
var ErrRate = errors.New("provider temporarily rate limited")
var ErrUnavailable = errors.New("provider temporarily unavailable")
var ErrAuth = errors.New("search provider credentials rejected")

// Search calls one provider at a time. A monthly quota event keeps the
// preferred provider blocked until reset. A short rate spike does not.
func (r *Router) Search(ctx context.Context, q Query) (Result, error) {
	if !requestValid(q) {
		return Result{}, ErrInvalidQuery
	}
	q.Text = strings.TrimSpace(q.Text)
	type provider struct {
		name, key string
		fn        func(context.Context, Query) (Result, error)
	}
	order := []provider{{Brave, r.BraveKey, r.brave}, {Tavily, r.TavilyKey, r.tavily}}
	var usedFallback bool
	var available bool
	for index, p := range order {
		if strings.TrimSpace(p.key) == "" || !r.ready(p.name) {
			continue
		}
		available = true
		// Bound the voice path: a slow primary must not block the fallback indefinitely.
		providerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := p.fn(providerCtx, q)
		cancel()
		if err == nil && len(result.Sources) > 0 {
			if index > 0 {
				usedFallback = true
			}
			if usedFallback {
				r.fallback()
			}
			result.Fallback = usedFallback
			return result, nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		// Do not leak provider API error bodies or user queries into returned logs.
	}
	if !available {
		return Result{}, ErrNoSearchAvailable
	}
	return Result{}, ErrNoSearchAvailable
}
func safeSnippet(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len(s) > 2000 {
		s = string([]rune(s)[:min(utf8.RuneCountInString(s), 650)])
	}
	return s
}
func validURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
func freshBrave(f string) string {
	switch f {
	case "day":
		return "pd"
	case "week":
		return "pw"
	case "month":
		return "pm"
	case "year":
		return "py"
	}
	return ""
}
func freshTavily(f string) string {
	switch f {
	case "day":
		return "day"
	case "week":
		return "week"
	case "month":
		return "month"
	case "year":
		return "year"
	}
	return ""
}
func waitShort(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (r *Router) post(ctx context.Context, endpoint string, payload any, headerName, key string) (*http.Response, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, ErrInvalidQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if headerName == "Authorization" {
		req.Header.Set(headerName, "Bearer "+key)
	} else {
		req.Header.Set(headerName, key)
	}
	resp, err := r.httpClient().Do(req)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return nil, nil, ErrUnavailable
	}
	return resp, body, nil
}
func (r *Router) brave(ctx context.Context, q Query) (Result, error) {
	r.count(Brave)
	start := r.now()
	payload := map[string]any{
		"q": q.Text, "country": "RU", "search_lang": "ru",
		"maximum_number_of_tokens": 3072, "maximum_number_of_urls": 6,
		"context_threshold_mode": "balanced", "safesearch": "moderate",
	}
	if f := freshBrave(q.Freshness); f != "" {
		payload["freshness"] = f
	}
	for attempt := 0; attempt < 2; attempt++ {
		resp, body, err := r.post(ctx, "https://api.search.brave.com/res/v1/llm/context", payload, "X-Subscription-Token", r.BraveKey)
		if err != nil {
			return Result{}, err
		}
		if resp.StatusCode != 200 {
			var fail struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(body, &fail)
			switch fail.Error.Code {
			case "QUOTA_LIMITED":
				until := nextCalendarMonth(r.now())
				// The server announces exact provider reset time via monthly rate header.
				if h := resp.Header.Get("X-RateLimit-Reset"); h != "" {
					parts := strings.Split(h, ",")
					if len(parts) > 1 {
						if seconds, e := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1])); e == nil && seconds > 0 && seconds <= 370*24*3600 {
							until = r.now().Add(time.Duration(seconds) * time.Second)
						}
					}
				}
				r.block(Brave, until)
				return Result{}, ErrQuota
			case "RATE_LIMITED":
				if attempt == 0 {
					if err := waitShort(ctx, 1100*time.Millisecond); err != nil {
						return Result{}, err
					}
					continue
				}
				return Result{}, ErrRate
			}
			if resp.StatusCode == 429 && attempt == 0 {
				if err := waitShort(ctx, 1100*time.Millisecond); err != nil {
					return Result{}, err
				}
				continue
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return Result{}, ErrAuth
			}
			return Result{}, ErrUnavailable
		}
		var data struct {
			Grounding struct {
				Generic []struct {
					Title, URL string
					Snippets   []string
				} `json:"generic"`
			} `json:"grounding"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return Result{}, ErrUnavailable
		}
		var sources []Source
		for _, item := range data.Grounding.Generic {
			if !validURL(item.URL) {
				continue
			}
			content := safeSnippet(strings.Join(item.Snippets, " "))
			if content == "" {
				continue
			}
			sources = append(sources, Source{Title: item.Title, URL: item.URL, Content: content})
			if len(sources) == 6 {
				break
			}
		}
		return Result{Provider: Brave, Sources: sources, Credits: 1, ElapsedMS: r.now().Sub(start).Milliseconds()}, nil
	}
	return Result{}, ErrRate
}
func (r *Router) tavily(ctx context.Context, q Query) (Result, error) {
	r.count(Tavily)
	start := r.now()
	payload := map[string]any{
		"query": q.Text, "search_depth": "basic", "max_results": 6,
		"chunks_per_source": 2, "country": "russia", "language": "ru",
		"filter_by_language": false, "include_usage": true,
		"include_answer": false, "include_raw_content": false,
		"auto_parameters": false, "safe_search": true,
	}
	if f := freshTavily(q.Freshness); f != "" {
		payload["time_range"] = f
	}
	for attempt := 0; attempt < 2; attempt++ {
		resp, body, err := r.post(ctx, "https://api.tavily.com/search", payload, "Authorization", r.TavilyKey)
		if err != nil {
			return Result{}, err
		}
		if resp.StatusCode != 200 {
			switch resp.StatusCode {
			case 432, 433:
				r.block(Tavily, nextCalendarMonth(r.now()))
				return Result{}, ErrQuota
			case 429:
				if attempt == 0 {
					if err := waitShort(ctx, 900*time.Millisecond); err != nil {
						return Result{}, err
					}
					continue
				}
				return Result{}, ErrRate
			case 401, 403:
				return Result{}, ErrAuth
			default:
				return Result{}, ErrUnavailable
			}
		}
		var data struct {
			Results []struct{ Title, URL, Content string } `json:"results"`
			Usage   struct {
				Credits int `json:"credits"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return Result{}, ErrUnavailable
		}
		var sources []Source
		for _, item := range data.Results {
			if !validURL(item.URL) {
				continue
			}
			if t := safeSnippet(item.Content); t != "" {
				sources = append(sources, Source{Title: item.Title, URL: item.URL, Content: t})
			}
		}
		credits := data.Usage.Credits
		if credits <= 0 {
			credits = 1
		}
		return Result{Provider: Tavily, Sources: sources, Credits: credits, ElapsedMS: r.now().Sub(start).Milliseconds()}, nil
	}
	return Result{}, ErrRate
}
