package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quasarbridge/internal/bridge"
)

func TestNormalizeAssistantModeRetiresFullDuplex(t *testing.T) {
	tests := []struct {
		raw  string
		want bridge.VoiceMode
		ok   bool
	}{
		{raw: "alice", want: bridge.VoiceModeStock, ok: true},
		{raw: "stock", want: bridge.VoiceModeStock, ok: true},
		{raw: "pass", want: bridge.VoiceModeStock, ok: true},
		{raw: "quasar", want: bridge.VoiceModeQuasar, ok: true},
		{raw: "custom", want: bridge.VoiceModeQuasar, ok: true},
		{raw: "gemini", want: bridge.VoiceModeQuasar, ok: true},
		{raw: " GEMINI ", want: bridge.VoiceModeQuasar, ok: true},
		{raw: "takeover", want: bridge.VoiceModeStock, ok: false},
		{raw: "unknown", want: bridge.VoiceModeStock, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ok := normalizeAssistantMode(tt.raw)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("normalizeAssistantMode(%q)=(%q,%v), want (%q,%v)", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAssistantModeCacheFailsSafeWhenStale(t *testing.T) {
	cache := newAssistantModeCache()
	now := time.Unix(100, 0)
	if got := cache.load(now); got != bridge.VoiceModeStock {
		t.Fatalf("empty cache=%q, want pass", got)
	}
	cache.store(bridge.VoiceModeQuasar, now)
	if got := cache.load(now.Add(assistantModeFreshFor / 2)); got != bridge.VoiceModeQuasar {
		t.Fatalf("fresh cache=%q, want quasar", got)
	}
	if got := cache.load(now.Add(assistantModeFreshFor + time.Millisecond)); got != bridge.VoiceModeStock {
		t.Fatalf("stale cache=%q, want fail-safe pass", got)
	}
}

func TestFetchAssistantModeUsesLegacyGeminiAsQuasarAlias(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"gemini"}`))
	}))
	defer server.Close()

	mode, ok := fetchAssistantMode(context.Background(), server.Client(), server.URL)
	if !ok || mode != bridge.VoiceModeQuasar {
		t.Fatalf("mode=(%q,%v), want (quasar,true)", mode, ok)
	}
}
