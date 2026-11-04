// Package playback recognizes direct controls for the current music queue.
package playback

import (
	"regexp"
	"strings"
	"unicode"
)

var commands = []struct {
	action  string
	pattern *regexp.Regexp
}{
	{"next", regexp.MustCompile(`^(?:(?:включи|поставь|переключи|перейди|переключись)(?: на)? )?(?:следующий(?: трек)?|следующую (?:песню|композицию)|следующая (?:песня|композиция)|дальше|вперед)$`)},
	{"previous", regexp.MustCompile(`^(?:(?:включи|поставь|верни|переключи|перейди|переключись)(?: на)? )?(?:предыдущий(?: трек)?|предыдущую (?:песню|композицию)|предыдущая (?:песня|композиция)|назад)$`)},
	{"pause", regexp.MustCompile(`^(?:пауза|(?:поставь|поставьте)(?: (?:музыку|песню|трек|воспроизведение))? на паузу|приостанови(?: (?:музыку|песню|трек|воспроизведение))?)$`)},
	{"resume", regexp.MustCompile(`^(?:(?:продолжи|продолжай|возобнови)(?: (?:музыку|песню|трек|воспроизведение))?|сними(?: (?:музыку|песню|трек))? с паузы)$`)},
	{"stop", regexp.MustCompile(`^(?:хватит|стоп|останови(?: (?:музыку|песню|трек|воспроизведение))?|выключи (?:музыку|песню|трек|радио))$`)},
	{"restart", regexp.MustCompile(`^(?:сначала|начни(?: (?:песню|трек|музыку))? сначала|включи(?: текущий)? (?:трек|песню) сначала|повтори (?:трек|песню))$`)},
}

func Command(text string) string {
	text = strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, text))
	text = strings.ReplaceAll(text, "ё", "е")
	words := strings.Fields(text)
	if len(words) > 0 && words[0] == "алиса" {
		words = words[1:]
	}
	if len(words) > 0 && words[0] == "пожалуйста" {
		words = words[1:]
	}
	if len(words) > 0 && words[len(words)-1] == "пожалуйста" {
		words = words[:len(words)-1]
	}
	text = strings.Join(words, " ")
	for _, c := range commands {
		if c.pattern.MatchString(text) {
			return c.action
		}
	}
	return ""
}
