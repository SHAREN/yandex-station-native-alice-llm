// Package fastintent recognizes a deliberately small, deterministic subset of
// short stock-Alice ASR commands. It never calls a model, network, or speaker.
// The caller MUST separately resolve the room through an explicit HA allowlist,
// check current exposure/availability, and perform the confirmed action.
package fastintent

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Intent contains only a parsed user request, never a Home Assistant entity ID.
type Intent struct {
	Operation string // on, off, color, temperature, brightness
	Room      string // local, bedroom, living_room, kitchen, corridor
	Color     string // red, orange
	Percent   int    // 1..100 for brightness
	Kelvin    int    // only populated for soft_white
}

var (
	percentPattern = regexp.MustCompile(`^(?:сделай|установи|поставь) (?:яркость|свет на) (\d{1,3})(?: ?%| процент| процента| процентов)?$`)
	areaSuffixes   = []struct{ phrase, room string }{
		{" в комнате", "bedroom"},
		{" в спальне", "bedroom"},
		{" в зале", "living_room"},
		{" на кухне", "kitchen"},
		{" в коридоре", "corridor"},
	}
)

// normalize handles non-semantic ASR variations only. It never repairs truncated
// words, guesses intent, or strips negation.
func normalize(text string) string {
	text = strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	text = strings.Map(func(r rune) rune {
		if r == '%' {
			return r
		}
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func stripVocative(text string) string {
	if strings.HasPrefix(text, "алиса ") {
		text = strings.TrimPrefix(text, "алиса ")
	}
	if strings.HasPrefix(text, "пожалуйста ") {
		text = strings.TrimPrefix(text, "пожалуйста ")
	}
	if strings.HasSuffix(text, " пожалуйста") {
		text = strings.TrimSuffix(text, " пожалуйста")
	}
	return text
}

// Match returns true only for an exact full-phrase match after safe normalization.
// Execute ONLY for a final (non-partial) ASR result, never for speculative audio.
func Match(text string) (Intent, bool) {
	s := stripVocative(normalize(text))
	if s == "" {
		return Intent{}, false
	}

	intent := Intent{Room: "local"}
	for _, area := range areaSuffixes {
		if strings.HasSuffix(s, area.phrase) {
			s = strings.TrimSuffix(s, area.phrase)
			intent.Room = area.room
			break
		}
	}

	switch s {
	case "включи свет", "зажги свет":
		intent.Operation = "on"
	case "выключи свет", "погаси свет":
		intent.Operation = "off"
	case "включи красный", "включи красный свет", "сделай свет красным", "поставь свет красным":
		intent.Operation, intent.Color = "color", "red"
	case "включи оранжевый", "включи оранжевый свет", "сделай свет оранжевым", "поставь свет оранжевым":
		intent.Operation, intent.Color = "color", "orange"
	case "включи мягкий белый", "включи мягкий белый свет", "сделай свет мягким белым",
		"включи теплый белый", "включи теплый белый свет", "сделай свет теплым белым":
		intent.Operation, intent.Kelvin = "temperature", 2700
	default:
		parts := percentPattern.FindStringSubmatch(s)
		if len(parts) != 2 {
			return Intent{}, false
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 1 || n > 100 {
			return Intent{}, false
		}
		intent.Operation, intent.Percent = "brightness", n
	}
	return intent, true
}
