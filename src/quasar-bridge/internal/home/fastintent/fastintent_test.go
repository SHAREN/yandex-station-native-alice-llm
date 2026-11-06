package fastintent

import (
	"reflect"
	"testing"
)

func TestMatchPositive(t *testing.T) {
	tests := []struct {
		text string
		want Intent
	}{
		{"включи свет", Intent{Operation: "on", Room: "local"}},
		{"Выключи свет!", Intent{Operation: "off", Room: "local"}},
		{"Алиса, включи свет в комнате", Intent{Operation: "on", Room: "bedroom"}},
		{"Алиса, выключи свет в спальне.", Intent{Operation: "off", Room: "bedroom"}},
		{"Пожалуйста, зажги свет в зале", Intent{Operation: "on", Room: "living_room"}},
		{"Алиса, выключи свет на кухне пожалуйста", Intent{Operation: "off", Room: "kitchen"}},
		{"Погаси свет в коридоре", Intent{Operation: "off", Room: "corridor"}},
		{"включи красный", Intent{Operation: "color", Room: "local", Color: "red"}},
		{"включи оранжевый свет в зале", Intent{Operation: "color", Room: "living_room", Color: "orange"}},
		{"Алиса, сделай свет красным в комнате", Intent{Operation: "color", Room: "bedroom", Color: "red"}},
		{"Поставь свет оранжевым в зале", Intent{Operation: "color", Room: "living_room", Color: "orange"}},
		{"включи мягкий белый", Intent{Operation: "temperature", Room: "local", Kelvin: 2700}},
		{"сделай свет тёплым белым в комнате", Intent{Operation: "temperature", Room: "bedroom", Kelvin: 2700}},
		{"установи яркость 50 процентов", Intent{Operation: "brightness", Room: "local", Percent: 50}},
		{"сделай яркость 50%", Intent{Operation: "brightness", Room: "local", Percent: 50}},
		{"Поставь свет на 25% в зале", Intent{Operation: "brightness", Room: "living_room", Percent: 25}},
		{"сделай яркость 100 процентов в комнате", Intent{Operation: "brightness", Room: "bedroom", Percent: 100}},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok := Match(tt.text)
			if !ok || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Match(%q) = %+v %v; want %+v true", tt.text, got, ok, tt.want)
			}
		})
	}
}

func TestMatchNegative(t *testing.T) {
	negative := []string{
		"", "алиса", "свет", "свет в зале", "включи",
		"не включай свет", "не включи свет", "никогда не включай свет в зале",
		"выключи все в доме", "выключи центральный выключатель в комнате",
		"включи свет и открой дверь", "включи свет, а потом заведи машину",
		"включи свет везде", "включи свет в прихожей", "включи свет в ванной",
		"включи песню красный", "почему свет красный",
		"включи красное", "включи красный фонарь в комнате",
		"сделай яркость ноль", "сделай яркость 0%", "сделай яркость 101%",
		"сделай яркость 10 процентов и включи музыку",
		"включи белый шум", "включи таймер на 5 минут",
		"выключи светлый", "алиса пожалуйста не включай свет",
		"выключи свет справа в комнате",
	}
	for _, input := range negative {
		t.Run(input, func(t *testing.T) {
			got, ok := Match(input)
			if ok {
				t.Errorf("Match(%q) unexpectedly matched %+v", input, got)
			}
		})
	}
}
