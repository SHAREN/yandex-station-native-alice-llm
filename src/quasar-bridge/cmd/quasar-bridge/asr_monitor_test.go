package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quasarbridge/internal/bridge"
)

func TestASRMonitorExposesLatestUpdateAndHistory(t *testing.T) {
	m := &asrMonitor{}
	m.publish(bridge.ASRUpdate{Request: "abcd1234", Text: "начало фразы", Final: false})
	m.publish(bridge.ASRUpdate{Request: "abcd1234", Text: "начало фразы целиком", Final: true})

	mux := http.NewServeMux()
	m.register(mux)

	latestReq := httptest.NewRequest("GET", "/__assistant/asr", nil)
	latestRes := httptest.NewRecorder()
	mux.ServeHTTP(latestRes, latestReq)
	if latestRes.Code != 200 {
		t.Fatalf("latest status=%d", latestRes.Code)
	}
	var latest asrSnapshot
	if err := json.Unmarshal(latestRes.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest.Request != "abcd1234" || latest.Text != "начало фразы целиком" || !latest.Final || latest.UpdatedAt == "" {
		t.Fatalf("latest=%+v", latest)
	}

	historyReq := httptest.NewRequest("GET", "/__assistant/asr/history", nil)
	historyRes := httptest.NewRecorder()
	mux.ServeHTTP(historyRes, historyReq)
	if historyRes.Code != 200 {
		t.Fatalf("history status=%d", historyRes.Code)
	}
	var history []asrSnapshot
	if err := json.Unmarshal(historyRes.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Text != "начало фразы" || history[0].Final || history[1].Text != "начало фразы целиком" || !history[1].Final {
		t.Fatalf("history=%+v", history)
	}
}
