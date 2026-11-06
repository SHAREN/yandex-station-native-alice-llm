package home

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"quasarbridge/internal/home/fastintent"
)

type fastFixture struct {
	state, entityID string
	exposed         bool
	colorModes      []string
	actionCalls     int
	noStateChange   bool
	lastService     string
	lastData        map[string]any
}

func (f *fastFixture) Call(ctx context.Context, name string, args map[string]any) (jsontext.Value, error) {
	var response any
	switch name {
	case "ha_get_entity_exposure":
		if args["entity_id"] != f.entityID {
			return nil, errors.New("wrong exposure target")
		}
		response = map[string]any{"exposed_to": map[string]bool{"conversation": f.exposed}}
	case "ha_get_state":
		if args["entity_id"] != f.entityID {
			return nil, errors.New("wrong state target")
		}
		response = map[string]any{"data": map[string]any{
			"entity_id": f.entityID, "state": f.state,
			"attributes": map[string]any{"supported_color_modes": f.colorModes},
		}}
	case "ha_call_service":
		if args["entity_id"] != f.entityID || args["wait"] != true {
			return nil, errors.New("unexpected service target")
		}
		f.actionCalls++
		f.lastService = args["service"].(string)
		f.lastData = args["data"].(map[string]any)
		if !f.noStateChange {
			if f.lastService == "turn_off" {
				f.state = "off"
			} else {
				f.state = "on"
			}
		}
		response = map[string]any{"success": true}
	default:
		return nil, errors.New("unexpected model/network call " + name)
	}
	return json.Marshal(response)
}

func TestFastHomeExecutesWithoutPlanner(t *testing.T) {
	cases := []struct {
		command, target, fromState string
		modes                      []string
		service                    string
		wantData                   map[string]any
	}{
		{"включи свет в зале", "light.living", "off", []string{"xy"}, "turn_on", map[string]any{}},
		{"выключи свет в зале", "light.living", "on", []string{"xy"}, "turn_off", map[string]any{}},
		{"включи оранжевый в зале", "light.living", "off", []string{"xy"}, "turn_on", map[string]any{"rgb_color": []int{255, 95, 0}}},
		{"включи мягкий белый в зале", "light.living", "off", []string{"color_temp"}, "turn_on", map[string]any{"color_temp_kelvin": 2700}},
		{"сделай яркость 50% в зале", "light.living", "off", []string{"xy"}, "turn_on", map[string]any{"brightness_pct": 50}},
		{"выключи свет в коридоре", "switch.hall", "on", nil, "turn_off", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			intent, ok := fastintent.Match(tc.command)
			if !ok {
				t.Fatal("unmatched prevalidated command")
			}
			f := &fastFixture{entityID: tc.target, state: tc.fromState, exposed: true, colorModes: tc.modes}
			c := &Controller{Tools: f, FastTargets: map[string]string{intent.Room: tc.target}}
			if !c.CanFastHandle(intent) {
				t.Fatal("wrong allowlist interpretation")
			}
			_, err := c.FastHandle(t.Context(), intent)
			if err != nil || f.actionCalls != 1 || f.lastService != tc.service || !reflect.DeepEqual(f.lastData, tc.wantData) {
				t.Fatalf("error=%v calls=%d service=%s data=%v want=%v", err, f.actionCalls, f.lastService, f.lastData, tc.wantData)
			}
		})
	}
}

func TestFastHomeFailsClosed(t *testing.T) {
	intent, _ := fastintent.Match("включи красный")
	for _, tc := range []struct {
		name, state, target string
		exposed             bool
		modes               []string
		noChange            bool
		wantCalls           int
	}{
		{"unexposed", "off", "light.room", false, []string{"xy"}, false, 0},
		{"unknown", "unknown", "light.room", true, []string{"xy"}, false, 0},
		{"unavailable", "unavailable", "light.room", true, []string{"xy"}, false, 0},
		{"no_color_mode", "off", "light.room", true, []string{"color_temp"}, false, 0},
		{"no_device_confirmation", "off", "light.room", true, []string{"xy"}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fastFixture{entityID: tc.target, state: tc.state, exposed: tc.exposed, colorModes: tc.modes, noStateChange: tc.noChange}
			c := &Controller{Tools: f, FastTargets: map[string]string{"local": tc.target}}
			_, err := c.FastHandle(t.Context(), intent)
			if err == nil || f.actionCalls != tc.wantCalls {
				t.Fatalf("expected fail-closed; err=%v calls=%d want=%d", err, f.actionCalls, tc.wantCalls)
			}
		})
	}
	switchTarget := &Controller{Tools: &fastFixture{}, FastTargets: map[string]string{"local": "switch.hall"}}
	if switchTarget.CanFastHandle(intent) {
		t.Fatal("cannot send color to a switch")
	}
	badTarget := &Controller{Tools: &fastFixture{}, FastTargets: map[string]string{"local": "light.room,light.other"}}
	if badTarget.CanFastHandle(intent) {
		t.Fatal("injected target accepted")
	}
}

func TestFastHomeEnvironmentOptIn(t *testing.T) {
	t.Setenv("QUASAR_FAST_HOME_ENABLED", "")
	t.Setenv("QUASAR_FAST_HOME_TARGETS_JSON", `{"local":"light.room"}`)
	f := &fastFixture{entityID: "light.room", exposed: true, state: "off", colorModes: []string{"xy"}}
	c := &Controller{Tools: f}
	intent, _ := fastintent.Match("включи свет")
	if c.CanFastHandle(intent) {
		t.Fatal("fast mode must be off by default")
	}
	t.Setenv("QUASAR_FAST_HOME_ENABLED", "1")
	if !c.CanFastHandle(intent) {
		t.Fatal("expected explicit valid opt-in to match")
	}
	t.Setenv("QUASAR_FAST_HOME_TARGETS_JSON", `{"local":"switch.kotel_pid_auto_tune","door":"lock.front"}`)
	if c.CanFastHandle(intent) {
		t.Fatal("unknown room/unsafe target accepted")
	}
	t.Setenv("QUASAR_FAST_HOME_TARGETS_JSON", `{"local":"switch.hall"}`)
	red, _ := fastintent.Match("включи красный")
	if c.CanFastHandle(red) {
		t.Fatal("color matched a switch")
	}
}
