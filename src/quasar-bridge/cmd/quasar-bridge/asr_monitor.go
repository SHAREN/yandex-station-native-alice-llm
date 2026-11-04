package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"quasarbridge/internal/bridge"
)

type asrSnapshot struct {
	Request   string `json:"request"`
	Text      string `json:"text"`
	Final     bool   `json:"final"`
	UpdatedAt string `json:"updated_at"`
}

const asrHistoryLimit = 64

type asrMonitor struct {
	mu      sync.RWMutex
	latest  asrSnapshot
	history []asrSnapshot
}

func (m *asrMonitor) publish(update bridge.ASRUpdate) {
	snapshot := asrSnapshot{
		Request:   update.Request,
		Text:      update.Text,
		Final:     update.Final,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	m.mu.Lock()
	m.latest = snapshot
	m.history = append(m.history, snapshot)
	if len(m.history) > asrHistoryLimit {
		m.history = append([]asrSnapshot(nil), m.history[len(m.history)-asrHistoryLimit:]...)
	}
	m.mu.Unlock()
}

func (m *asrMonitor) snapshot() asrSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest
}

func (m *asrMonitor) snapshots() []asrSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]asrSnapshot(nil), m.history...)
}

func (m *asrMonitor) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /__assistant/asr", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.snapshot())
	})
	mux.HandleFunc("GET /__assistant/asr/history", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.snapshots())
	})
}
