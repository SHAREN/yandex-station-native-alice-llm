package main

import (
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"quasarbridge/internal/bridge"
)

type externalTTSBroker struct {
	mu      sync.Mutex
	natives []*bridge.Native
}

func (b *externalTTSBroker) attach(n *bridge.Native) {
	if n == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	alive := b.natives[:0]
	for _, existing := range b.natives {
		if existing != nil && !existing.Closed() {
			alive = append(alive, existing)
		}
	}
	b.natives = append(alive, n)
}

func (b *externalTTSBroker) current() *bridge.Native {
	b.mu.Lock()
	defer b.mu.Unlock()

	alive := b.natives[:0]
	var active *bridge.Native
	for _, n := range b.natives {
		if n == nil || n.Closed() {
			continue
		}
		alive = append(alive, n)
		if n.ExternalTTSActive() {
			active = n
		}
	}
	b.natives = alive
	return active
}

// handleTextInput globally fences the legacy Quasar classifier/LLM while a
// Gemini takeover is active, even when firmware emits the TextInput on another
// parallel UniProxy websocket. Stop remains a control command; all other text
// is consumed silently until Gemini ends.
func (b *externalTTSBroker) handleTextInput(text string) bool {
	n := b.current()
	if n == nil {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "стоп" || normalized == "хватит" {
		return n.ExternalUserStop("command_spotter_text_global")
	}
	slog.Info("external_text_input_suppressed", "text_bytes", len(text))
	return true
}

func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (b *externalTTSBroker) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /__gemini/status", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		n := b.current()
		active := n != nil && n.ExternalTTSActive()
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(map[string]any{"active": active})
		_, _ = w.Write(data)
	})

	mux.HandleFunc("POST /__gemini/input/complete", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		n := b.current()
		if n == nil || !n.ExternalTTSActive() {
			http.Error(w, "no active Gemini takeover", http.StatusConflict)
			return
		}
		if err := n.ExternalInputComplete(); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /__gemini/tts/stream", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		n := b.current()
		if n == nil || !n.ExternalTTSActive() {
			http.Error(w, "no active Gemini takeover", http.StatusConflict)
			return
		}
		format := r.Header.Get("Content-Type")
		if err := n.ExternalTTSBegin(format); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		buf := make([]byte, 16*1024)
		for {
			count, err := r.Body.Read(buf)
			if count > 0 {
				if writeErr := n.ExternalTTSWrite(buf[:count]); writeErr != nil {
					_ = n.ExternalTTSInterrupt()
					http.Error(w, writeErr.Error(), http.StatusBadGateway)
					return
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = n.ExternalTTSInterrupt()
				slog.Info("external_tts_http_stream_aborted", "error", err)
				return
			}
		}
		if err := n.ExternalTTSEnd(); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /__gemini/tts/interrupt", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if n := b.current(); n != nil && n.ExternalTTSActive() {
			if err := n.ExternalTTSInterrupt(); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /__gemini/session/end", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if n := b.current(); n != nil && n.ExternalTTSActive() {
			if err := n.ExternalSessionEnd(); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
