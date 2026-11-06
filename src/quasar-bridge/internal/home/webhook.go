package home

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"quasarbridge/internal/home/fastintent"
)

// WebhookFast calls only a private LAN-only Home Assistant automation webhook.
// The automation must have a static allowlist for rooms/actions: this handler
// never forwards an arbitrary HA entity ID, microphone audio, or LLM output.
type WebhookFast struct {
	URL     string
	Targets map[string]string
	Client  *http.Client
}

var privateHAWebhook = regexp.MustCompile(`^/api/webhook/[0-9a-f]{64}$`)

func NewWebhookFastFromEnv() (*WebhookFast, error) {
	if os.Getenv("QUASAR_FAST_HOME_ENABLED") != "1" {
		return nil, nil
	}
	raw := strings.TrimSpace(os.Getenv("QUASAR_FAST_HOME_WEBHOOK_URL"))
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host != "192.168.0.136:8123" || !privateHAWebhook.MatchString(u.Path) || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("fast home requires a private local-only Home Assistant webhook")
	}
	var targets map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("QUASAR_FAST_HOME_TARGETS_JSON")), &targets); err != nil || len(targets) == 0 {
		return nil, errors.New("fast home requires allowed room light targets")
	}
	for room, id := range targets {
		switch room {
		case "local", "bedroom", "living_room":
		default:
			return nil, errors.New("fast home room is not permitted")
		}
		if !regexp.MustCompile(`^light\.[a-z0-9_]+$`).MatchString(id) {
			return nil, errors.New("fast home only allows light groups")
		}
	}
	return &WebhookFast{URL: raw, Targets: targets}, nil
}

// Handle has no planner. All commands not matched by a strict intent must
// follow the original stock Yandex/LLM routing.
func (h *WebhookFast) Handle(_ context.Context, _ string) (string, error) {
	return "", errors.New("fast home webhook does not process arbitrary speech")
}
func (h *WebhookFast) CanFastHandle(i fastintent.Intent) bool {
	if h == nil || h.URL == "" || h.Targets == nil {
		return false
	}
	id, ok := h.Targets[i.Room]
	if !ok || !strings.HasPrefix(id, "light.") {
		return false
	}
	switch i.Operation {
	case "on", "off", "color", "temperature", "brightness":
		return true
	}
	return false
}
func (h *WebhookFast) FastHandle(ctx context.Context, i fastintent.Intent) (string, error) {
	if !h.CanFastHandle(i) {
		return "", errors.New("fast home command not allowed")
	}
	// Build payload from a typed strict intent, not raw ASR text.
	payload := struct {
		Room      string `json:"room"`
		Operation string `json:"operation"`
		Color     string `json:"color,omitempty"`
		Percent   int    `json:"percent,omitempty"`
		Kelvin    int    `json:"kelvin,omitempty"`
	}{Room: i.Room, Operation: i.Operation}
	switch i.Operation {
	case "color":
		if i.Color != "red" && i.Color != "orange" {
			return "", errors.New("unsupported color")
		}
		payload.Color = i.Color
	case "temperature":
		if i.Kelvin != 2700 {
			return "", errors.New("unsupported color temperature")
		}
		payload.Kelvin = i.Kelvin
	case "brightness":
		if i.Percent < 1 || i.Percent > 100 {
			return "", errors.New("unsupported brightness")
		}
		payload.Percent = i.Percent
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(data))
	if err != nil {
		return "", errors.New("invalid home action endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // Never forward a private webhook secret to a redirected destination.
		}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("home action transport failure")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("home action HTTP %d", resp.StatusCode)
	}
	// HA webhook returns 200 when queued, not a post-action state guarantee.
	return "Команда отправлена.", nil
}
