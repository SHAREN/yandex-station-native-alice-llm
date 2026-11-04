package bridge

import (
	"encoding/json"
	"log/slog"
	"os"
	"runtime"
	"time"
)

const nativePacingLog = "/data/opt/quasar-bridge/tts-pacing.jsonl"

// recordSynthesizedSpeechLocked is called under Native.mu after the stock
// TTS source's StreamControl.Close. Unlike a WPM guess, the Opus sample count
// measures the actual duration of the Yandex voice for this exact fragment.
// Only aggregate timing/length is retained. No spoken text/audio is logged.
func recordSynthesizedSpeechLocked(p *nativeRequest) {
	if p == nil || p.audio.source == nil || p.ttsTextRunes == 0 {
		return
	}
	samples := p.audio.source.samples
	if samples > p.audio.trim {
		samples -= p.audio.trim
	}
	if samples == 0 {
		return
	}
	speechMS := float64(samples) / 48.0
	seconds := speechMS / 1000.0
	rate := float64(p.ttsTextRunes) / seconds
	// Voice varies with sentence punctuation. Reject outliers (near-silent
	// audio, broken segment). EWMA makes future estimates less jumpy.
	if rate >= 6 && rate <= 30 {
		if p.speechCharsPerSecond == 0 {
			p.speechCharsPerSecond = rate
		} else {
			p.speechCharsPerSecond = .75*p.speechCharsPerSecond + .25*rate
		}
	}
	speakMS, totalMS := int64(-1), int64(-1)
	if !p.ttsSpeakAt.IsZero() && !p.ttsSentAt.IsZero() {
		speakMS = p.ttsSpeakAt.Sub(p.ttsSentAt).Milliseconds()
	}
	if !p.ttsSentAt.IsZero() {
		totalMS = time.Since(p.ttsSentAt).Milliseconds()
	}
	r := struct {
		At            string  `json:"at"`
		Request       string  `json:"request"`
		Segment       uint32  `json:"segment"`
		TextRunes     int     `json:"text_runes"`
		VoiceMS       int64   `json:"voice_ms"`
		FirstSpeakMS  int64   `json:"tts_start_ms"`
		SynthesisMS   int64   `json:"tts_complete_ms"`
		CharsPerSec   float64 `json:"chars_per_sec"`
		RunningRate   float64 `json:"running_rate"`
	}{
		At: time.Now().UTC().Format(time.RFC3339Nano),
		Request: requestTag(p.id), Segment: p.ttsOrdinal,
		TextRunes: p.ttsTextRunes, VoiceMS: int64(speechMS),
		FirstSpeakMS: speakMS, SynthesisMS: totalMS,
		CharsPerSec: rate, RunningRate: p.speechCharsPerSecond,
	}
	slog.Info("native_tts_pacing_measured", "request", r.Request, "segment", r.Segment,
		"text_runes", r.TextRunes, "voice_ms", r.VoiceMS,
		"tts_start_ms", r.FirstSpeakMS, "tts_complete_ms", r.SynthesisMS)
	if runtime.GOOS != "linux" {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := os.OpenFile(nativePacingLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}
