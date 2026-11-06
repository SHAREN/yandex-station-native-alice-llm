package home

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quasarbridge/internal/home/fastintent"
)

func TestPrivateFastWebhookSendsOnlyStrictIntent(t *testing.T) {
	seen := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("invalid request %s", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		if body["room"] != "living_room" || body["operation"] != "color" || body["color"] != "orange" {
			t.Errorf("unexpected payload %v", body)
		}
		if strings.Contains(string(raw), "light.") {
			t.Error("entity IDs must not be sent to webhook")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	h := &WebhookFast{URL: srv.URL + "/api/webhook/test", Targets: map[string]string{"living_room": "light.hall"}, Client: srv.Client()}
	command, ok := fastintent.Match("Алиса включи оранжевый свет в зале")
	if !ok {
		t.Fatal("intent didn't match")
	}
	answer, err := h.FastHandle(context.Background(), command)
	if err != nil || answer != "Команда отправлена." || seen != 1 {
		t.Fatalf("unexpected: %q %v calls=%d", answer, err, seen)
	}
}
func TestWebhookFailClosed(t *testing.T) {
	t.Setenv("QUASAR_FAST_HOME_ENABLED", "1")
	t.Setenv("QUASAR_FAST_HOME_TARGETS_JSON", `{"local":"light.room"}`)
	t.Setenv("QUASAR_FAST_HOME_WEBHOOK_URL", "http://example.com/api/webhook/"+strings.Repeat("a", 64))
	if _, err := NewWebhookFastFromEnv(); err == nil {
		t.Fatal("non-LAN endpoint accepted")
	}
	t.Setenv("QUASAR_FAST_HOME_WEBHOOK_URL", "http://192.168.0.136:8123/api/webhook/"+strings.Repeat("b", 64))
	h, err := NewWebhookFastFromEnv()
	if err != nil || h == nil {
		t.Fatalf("valid gated webhook rejected: %v", err)
	}
	if _, ok := fastintent.Match("не включай свет"); ok {
		t.Fatal("negation matched")
	}
	if h.CanFastHandle(fastintent.Intent{Room: "garage", Operation: "on"}) {
		t.Fatal("unknown room accepted")
	}
	if h.CanFastHandle(fastintent.Intent{Room: "local", Operation: "unlock"}) {
		t.Fatal("unsafe action accepted")
	}
	t.Setenv("QUASAR_FAST_HOME_ENABLED", "0")
	if disabled, err := NewWebhookFastFromEnv(); err != nil || disabled != nil {
		t.Fatal("default off rule failed")
	}
}
func TestWebhookFailureNoRetry(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { count++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	h := &WebhookFast{URL: srv.URL, Targets: map[string]string{"local": "light.room"}, Client: srv.Client()}
	i, ok := fastintent.Match("включи свет")
	if !ok {
		t.Fatal("intent mismatch")
	}
	_, err := h.FastHandle(context.Background(), i)
	if err == nil || count != 1 {
		t.Fatalf("expected single failed call, got err=%v calls=%d", err, count)
	}
}
