package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"quasarbridge/internal/bridge"
)

const (
	defaultAssistantModeURL = "http://192.168.0.136:7872/healthz"
	assistantModePollEvery  = 250 * time.Millisecond
	assistantModeFreshFor   = 2 * time.Second
)

type assistantModeHealth struct {
	Mode string `json:"mode"`
}

type assistantModeCache struct {
	mu      sync.RWMutex
	mode    bridge.VoiceMode
	updated time.Time
}

func newAssistantModeCache() *assistantModeCache {
	return &assistantModeCache{mode: bridge.VoiceModeStock}
}

func normalizeAssistantMode(raw string) (bridge.VoiceMode, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "alice", "stock", "pass":
		return bridge.VoiceModeStock, true
	case "gemini", "quasar", "custom":
		// `gemini` is a migration-only alias. Full duplex is retired; the only
		// custom Station mode is the native turn-by-turn Quasar/LLM path.
		return bridge.VoiceModeQuasar, true
	default:
		return bridge.VoiceModeStock, false
	}
}

func (c *assistantModeCache) store(mode bridge.VoiceMode, now time.Time) {
	c.mu.Lock()
	c.mode = mode
	c.updated = now
	c.mu.Unlock()
}

func (c *assistantModeCache) load(now time.Time) bridge.VoiceMode {
	c.mu.RLock()
	mode, updated := c.mode, c.updated
	c.mu.RUnlock()
	if updated.IsZero() || now.Sub(updated) > assistantModeFreshFor {
		return bridge.VoiceModeStock
	}
	if mode != bridge.VoiceModeQuasar {
		return bridge.VoiceModeStock
	}
	return bridge.VoiceModeQuasar
}

func assistantModeURL() string {
	if value := strings.TrimSpace(os.Getenv("QUASAR_ASSISTANT_MODE_URL")); value != "" {
		return value
	}
	return defaultAssistantModeURL
}

func fetchAssistantMode(ctx context.Context, client *http.Client, url string) (bridge.VoiceMode, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return bridge.VoiceModeStock, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return bridge.VoiceModeStock, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return bridge.VoiceModeStock, false
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return bridge.VoiceModeStock, false
	}
	var health assistantModeHealth
	if err := json.Unmarshal(payload, &health); err != nil {
		return bridge.VoiceModeStock, false
	}
	return normalizeAssistantMode(health.Mode)
}

func monitorAssistantMode(ctx context.Context, client *http.Client, url string, cache *assistantModeCache) {
	poll := func() {
		requestCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		mode, ok := fetchAssistantMode(requestCtx, client, url)
		cancel()
		if !ok {
			return
		}
		cache.store(mode, time.Now())
	}
	poll()
	ticker := time.NewTicker(assistantModePollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

func logAssistantModeDecision(id string, mode bridge.VoiceMode, started time.Time) {
	sum := sha256.Sum256([]byte(id))
	tag := fmt.Sprintf("%x", sum[:4])
	slog.Info("wake_mode_decision", "request", tag, "mode", mode, "source", "cached_http", "latency_ms", time.Since(started).Milliseconds())
}
