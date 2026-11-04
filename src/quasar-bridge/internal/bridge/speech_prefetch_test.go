package bridge

import (
	"strings"
	"testing"
	"time"
)

func TestSpeechPrefetchDelayAndEstimation(t *testing.T) {
	cases := []struct {
		duration, want time.Duration
	}{
		{2 * time.Second, 0},
		{3 * time.Second, 0},
		{5 * time.Second, 2 * time.Second},
		{10 * time.Second, 6 * time.Second},
		{20 * time.Second, 12 * time.Second},
	}
	for _, tc := range cases {
		if got := speechPrefetchDelay(tc.duration); got != tc.want {
			t.Fatalf("duration %s -> trigger %s, want %s", tc.duration, got, tc.want)
		}
	}
	if got := estimatedSpeechDuration(strings.Repeat("я", 140), 14); got != 10*time.Second {
		t.Fatalf("140 Russian runes / 14 runes/s = %s, want 10s", got)
	}
}

func TestSpeechBatchCountTwoSentencesWhenReady(t *testing.T) {
	testCases := []struct {
		ready []string
		want  int
	}{
		{[]string{}, 0},
		{[]string{"Коротко."}, 1},
		{[]string{"Первое.", "Второе."}, 2},
		{[]string{strings.Repeat("а", 190), strings.Repeat("б", 90)}, 1},
	}
	for _, tc := range testCases {
		if got := speechBatchCount(tc.ready); got != tc.want {
			t.Fatalf("batch count for %d sentences = %d, want %d", len(tc.ready), got, tc.want)
		}
	}
}

func TestPacingStartsNextImmediatelyForTinyFirstSentence(t *testing.T) {
	if got := speechPrefetchDelay(estimatedSpeechDuration("Да.", 14)); got != 0 {
		t.Fatalf("tiny first sentence should prefetch immediately, got %s", got)
	}
}

func TestSynthesizedOpusDurationCalibratesSpeechRate(t *testing.T) {
	p := &nativeRequest{
		id: "measurement-test",
		ttsTextRunes: 140,
		ttsOrdinal: 1,
		ttsSentAt: time.Now().Add(-3 * time.Second),
		ttsSpeakAt: time.Now().Add(-2 * time.Second),
		audio: oggSpeech{source: &oggSource{samples: 480000}},
	}
	recordSynthesizedSpeechLocked(p)
	if got := p.speechCharsPerSecond; got < 13.99 || got > 14.01 {
		t.Fatalf("480000 Opus samples at 48000 Hz = 10s; 140 runes should calibrate to 14 chars/s, got %f", got)
	}
}
