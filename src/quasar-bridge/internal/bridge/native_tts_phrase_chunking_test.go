package bridge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The active native UniProxy mode must not turn raw 16-character LLM deltas
// into artificial Yandex acoustic phrases. Only existing punctuation is used.
func TestNativeSentenceBufferFirstClauseThenWholeSentences(t *testing.T) {
	first := "В далёком королевстве за широкой рекой,"
	rest := " где жила мудрая сова, считавшая звёзды каждую ночь."
	later := " Однажды она отправилась в путешествие, потому что увидела странный свет."
	tail := " Теперь история продолжается без точки"
	full := first + rest + later + tail
	var b streamedSentenceBuffer
	var got []string
	emit := func(part string) error { got = append(got, part); return nil }
	head := string([]rune(first)[:16])
	if err := b.feedNative(head, emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("native TTS started on a 16-rune boundary: %q", got)
	}
	if utf8.RuneCountInString(first) < firstClauseMinRunes {
		t.Fatal("test fixture has no qualifying opening clause")
	}
	for _, delta := range []string{strings.TrimPrefix(first, head), rest, later[:1], later[1:], tail} {
		if err := b.feedNative(delta, emit); err != nil {
			t.Fatal(err)
		}
	}
	if strings.TrimSpace(b.pending) != "" {
		got = append(got, b.pending) // flush residual on LLM completion
	}
	want := []string{first, rest, later, tail}
	if len(got) != len(want) {
		t.Fatalf("native text chunks = %q; want exactly %d", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
	if strings.Join(got, "") != full {
		t.Fatalf("TTS chunks lost or duplicated source bytes: %q", got)
	}
}

func TestNativeSentenceBufferNoSpuriousNumericOrQuoteSplit(t *testing.T) {
	var b streamedSentenceBuffer
	var got []string
	emit := func(s string) error { got = append(got, s); return nil }
	full := "Цена равна 3.14 рубля. Она ответила: «Привет!» И ушла."
	for _, delta := range []string{"Цена равна 3.", "14 рубля. Она ответила: «Привет!", "» И ушла."} {
		if err := b.feedNative(delta, emit); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Цена равна 3.14 рубля.", " Она ответила: «Привет!»", " И ушла."}
	if len(got) != len(want) {
		t.Fatalf("unexpected punctuation split: %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("part %d = %q, want %q", i, got[i], want[i])
		}
	}
	if strings.Join(got, "") != full {
		t.Fatalf("lost native text or punctuation: %q", got)
	}
}
