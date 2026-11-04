// Package webaddress recognizes written and spoken website addresses in requests.
package webaddress

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

const label = `[\p{L}\p{N}][\p{L}\p{N}-]{0,62}`
const spokenDot = `\s+(?:точка|dot)\s+`
const spokenSlash = `\s+(?:слэш|слеш|slash)\s+`
const pathToken = `[^\s<>"'«»]+`
const spokenPath = `(?:` + spokenSlash + pathToken + `)*`
const writtenDomain = `(?:` + label + `\.)+[\p{L}]{2,63}`
const spokenDomain = label + `(?:` + spokenDot + label + `)*` + spokenDot + `(?:с\s+ю|эс\s+ю|эр\s+ю|[\p{L}]{2,63})`
const addressTail = `(?::[0-9]{1,5})?(?:[/?#]` + pathToken + `)?` + spokenPath

var addresses = regexp.MustCompile(`(?i)https?://` + pathToken + spokenPath + `|(?:` + spokenDomain + `|` + writtenDomain + `)` + addressTail)
var dots = regexp.MustCompile(`(?i)` + spokenDot)
var slashes = regexp.MustCompile(`(?i)` + spokenSlash)
var absoluteAddress = regexp.MustCompile(`(?i)https?://`)
var playbackVerb = regexp.MustCompile(`(?i)(?:^|[\s,;:])(?:включи(?:те)?|проиграй(?:те)?|воспроизведи(?:те)?|поставь(?:те)?|запусти(?:те)?|сыграй(?:те)?)(?:\s|$)`)
var musicTerm = regexp.MustCompile(`(?i)(?:^|[\s,;:])(?:музык|аудио|песн|трек|радио|звук)`)

var suffixAliases = map[string]string{
	"су": "su", "с ю": "su", "эс ю": "su", "зу": "su", "zu": "su",
	"ру": "ru", "эр ю": "ru", "ком": "com", "нет": "net", "орг": "org", "ио": "io", "дев": "dev",
}

// Normalize changes only address spans, preserving the rest of the request.
// Speech aliases identify Latin labels; actual internationalized TLDs keep IDNA.
func Normalize(text string) string {
	var out strings.Builder
	previous := 0
	for _, span := range addresses.FindAllStringIndex(text, -1) {
		if span[1] < len(text) {
			after, _ := utf8.DecodeRuneInString(text[span[1]:])
			if unicode.IsLetter(after) || unicode.IsDigit(after) || strings.ContainsRune("_-", after) {
				continue
			}
		}
		if span[0] > 0 {
			before, _ := utf8.DecodeLastRuneInString(text[:span[0]])
			if unicode.IsLetter(before) || unicode.IsDigit(before) || strings.ContainsRune("@_", before) {
				continue
			}
		}
		raw := text[span[0]:span[1]]
		address := strings.TrimRight(raw, ".,!?;:")
		if strings.Count(address, ")") > strings.Count(address, "(") {
			address = strings.TrimRight(address, ")")
		}
		canonical, ok := normalizeAddress(address)
		if !ok {
			continue
		}
		out.WriteString(text[previous:span[0]])
		out.WriteString(canonical)
		out.WriteString(raw[len(address):])
		previous = span[1]
	}
	if previous == 0 {
		return text
	}
	out.WriteString(text[previous:])
	return out.String()
}

func normalizeAddress(raw string) (string, bool) {
	spoken := dots.MatchString(raw)
	raw = dots.ReplaceAllString(raw, ".")
	raw = slashes.ReplaceAllString(raw, "/")
	scheme, rest, explicit := strings.Cut(raw, "://")
	if !explicit {
		scheme, rest = "https", raw
	}
	head, tail := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		head, tail = rest[:i], rest[i:]
	}
	host, port := head, ""
	if !strings.HasPrefix(head, "[") {
		if name, suffix, ok := strings.Cut(head, ":"); ok {
			host, port = name, ":"+suffix
		}
	}
	canonicalHost, ok := normalizeHost(host, spoken, explicit)
	if !ok {
		return "", false
	}
	u, err := url.Parse(strings.ToLower(scheme) + "://" + canonicalHost + port + tail)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	return u.String(), true
}

func normalizeHost(host string, spoken, explicit bool) (string, bool) {
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return host, explicit
	}
	labels := strings.Split(strings.ToLower(host), ".")
	last := len(labels) - 1
	suffix := strings.Join(strings.Fields(labels[last]), " ")
	mapped, alias := suffixAliases[suffix]
	if alias {
		labels[last] = mapped
	}
	international := strings.ContainsFunc(suffix, func(r rune) bool { return r > unicode.MaxASCII }) && !alias
	if (spoken || alias) && !international {
		for i := range last {
			labels[i] = transliterate(labels[i])
		}
	}
	ascii, err := idna.Lookup.ToASCII(strings.Join(labels, "."))
	if err != nil {
		return "", false
	}
	if international && !explicit {
		if _, known := publicsuffix.PublicSuffix(ascii); !known {
			return "", false
		}
	}
	return ascii, true
}

func transliterate(label string) string {
	const letters = "абвгдеёжзийклмнопрстуфхцчшщъыьэюя"
	latin := [...]string{"a", "b", "v", "g", "d", "e", "e", "zh", "z", "i", "y", "k", "l", "m", "n", "o", "p", "r", "s", "t", "u", "f", "kh", "ts", "ch", "sh", "shch", "", "y", "", "e", "yu", "ya"}
	var out strings.Builder
	for _, r := range label {
		if i := strings.IndexRune(letters, r); i >= 0 {
			out.WriteString(latin[utf8.RuneCountInString(letters[:i])])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// MusicRequest excludes ordinary Yandex music and negated playback commands.
func MusicRequest(text string) bool {
	lower := strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	for _, negative := range []string{"не включ", "не проигр", "не воспроиз", "не став", "не постав", "не запус", "не сыгр", "не надо", "не нужно", "не хочу"} {
		if strings.Contains(lower, negative) {
			return false
		}
	}
	if !playbackVerb.MatchString(lower) || !musicTerm.MatchString(lower) {
		return false
	}
	return strings.Contains(lower, "сайт") || strings.Contains(lower, "страниц") || absoluteAddress.MatchString(Normalize(text))
}
