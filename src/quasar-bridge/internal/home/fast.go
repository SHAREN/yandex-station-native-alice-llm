package home

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"quasarbridge/internal/home/fastintent"
)

// Safe, preapproved Home Assistant targets are supplied only by runtime config.
// This path never inventories all devices or invokes the LLM planner.
var fastEntityID = regexp.MustCompile(`^(?:light|switch)\.[a-z0-9_]+$`)

// Configured fast commands are disabled unless an operator explicitly opts in
// at process startup. The existing main.go keeps creating the same Controller;
// no additional on-speaker program is introduced.
func (c *Controller) configuredFastTargets() map[string]string {
	if c == nil {
		return nil
	}
	if c.FastTargets != nil { // Explicit injection for unit tests/in-process callers.
		return c.FastTargets
	}
	if os.Getenv("QUASAR_FAST_HOME_ENABLED") != "1" {
		return nil
	}
	var targets map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("QUASAR_FAST_HOME_TARGETS_JSON")), &targets); err != nil || len(targets) == 0 {
		return nil
	}
	for room, id := range targets {
		switch room {
		case "local", "bedroom", "living_room", "kitchen", "corridor":
		default:
			return nil
		}
		if !fastEntityID.MatchString(id) {
			return nil
		}
	}
	return targets
}

func (c *Controller) CanFastHandle(intent fastintent.Intent) bool {
	if c == nil || c.Tools == nil {
		return false
	}
	id, ok := c.configuredFastTargets()[intent.Room]
	if !ok || !fastEntityID.MatchString(id) {
		return false
	}
	if intent.Operation != "on" && intent.Operation != "off" &&
		intent.Operation != "color" && intent.Operation != "temperature" &&
		intent.Operation != "brightness" {
		return false
	}
	// Color, white temperature and brightness are never sent to switches.
	if intent.Operation != "on" && intent.Operation != "off" {
		return strings.HasPrefix(id, "light.")
	}
	return true
}

type fastLightState struct {
	Data struct {
		State      string `json:"state"`
		Attributes struct {
			SupportedColorModes []string `json:"supported_color_modes"`
		} `json:"attributes"`
	} `json:"data"`
}

func (c *Controller) fastState(ctx context.Context, id string) (fastLightState, error) {
	var result fastLightState
	raw, err := c.Tools.Call(ctx, "ha_get_state", map[string]any{
		"entity_id":      id,
		"fields":         []string{"entity_id", "state", "attributes"},
		"attribute_keys": []string{"supported_color_modes"},
	})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("invalid fast home state: %w", err)
	}
	if result.Data.State == "" || result.Data.State == "unknown" || result.Data.State == "unavailable" {
		return result, errors.New("fast home target has no reliable state")
	}
	return result, nil
}

func (c *Controller) FastHandle(ctx context.Context, intent fastintent.Intent) (string, error) {
	if !c.CanFastHandle(intent) {
		return "", errors.New("fast home command target is not allowlisted")
	}
	id := c.configuredFastTargets()[intent.Room]
	visible, err := c.exposed(ctx, id, false)
	if err != nil {
		return "", err
	}
	if !visible {
		return "", errors.New("fast home target is not exposed to Assist")
	}
	before, err := c.fastState(ctx, id)
	if err != nil {
		return "", err
	}
	domain := strings.SplitN(id, ".", 2)[0]
	service := "turn_on"
	data := map[string]any{}
	switch intent.Operation {
	case "off":
		service = "turn_off"
	case "color":
		if !slices.Contains(before.Data.Attributes.SupportedColorModes, "xy") &&
			!slices.Contains(before.Data.Attributes.SupportedColorModes, "rgb") &&
			!slices.Contains(before.Data.Attributes.SupportedColorModes, "hs") &&
			!slices.Contains(before.Data.Attributes.SupportedColorModes, "rgbw") &&
			!slices.Contains(before.Data.Attributes.SupportedColorModes, "rgbww") {
			return "", errors.New("target light does not support colors")
		}
		switch intent.Color {
		case "red":
			data["rgb_color"] = []int{255, 0, 0}
		case "orange":
			data["rgb_color"] = []int{255, 95, 0}
		default:
			return "", errors.New("color is not allowlisted")
		}
	case "temperature":
		if !slices.Contains(before.Data.Attributes.SupportedColorModes, "color_temp") ||
			intent.Kelvin != 2700 {
			return "", errors.New("target light does not support requested white temperature")
		}
		data["color_temp_kelvin"] = intent.Kelvin
	case "brightness":
		if intent.Percent < 1 || intent.Percent > 100 {
			return "", errors.New("brightness percentage is out of range")
		}
		data["brightness_pct"] = intent.Percent
	}
	if _, err := c.Tools.Call(ctx, "ha_call_service", map[string]any{
		"domain": domain, "service": service, "entity_id": id, "data": data, "wait": true,
	}); err != nil {
		return "", fmt.Errorf("fast home action failed (no retry to avoid duplicate): %w", err)
	}
	after, err := c.fastState(ctx, id)
	if err != nil {
		return "", err
	}
	expected := "on"
	if service == "turn_off" {
		expected = "off"
	}
	if after.Data.State != expected {
		return "", errors.New("fast home action did not reach expected device state")
	}
	if intent.Operation == "color" || intent.Operation == "temperature" || intent.Operation == "brightness" {
		// On/off is confirmed; exact color/brightness needs separate live attribute verification.
		return "Команда отправлена.", nil
	}
	return "Готово.", nil
}
