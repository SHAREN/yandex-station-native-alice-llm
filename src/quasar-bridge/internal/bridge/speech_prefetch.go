package bridge

import (
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// This is a conservative planning estimate, not a claim about measured
	// Yandex Alice TTS speed. Real output Opus duration is logged separately.
	initialSpeechCharsPerSecond = 14.0
	speechPrefetchReserve       = 3 * time.Second
	speechBatchMaxRunes         = 220
	speechBatchMaxSentences     = 2
)

// estimatedSpeechDuration is a temporary estimate until a station supplies
// Opus sample-duration telemetry. 14 runes/s approximates natural Russian
// narration at around 140 words/min with ~6 runes/word including spaces.
func estimatedSpeechDuration(text string, charsPerSecond float64) time.Duration {
	if charsPerSecond < 7 || charsPerSecond > 28 {
		charsPerSecond = initialSpeechCharsPerSecond
	}
	runes := utf8.RuneCountInString(strings.TrimSpace(text))
	if runes == 0 {
		return 0
	}
	seconds := float64(runes) / charsPerSecond
	return time.Duration(math.Max(seconds, .45) * float64(time.Second))
}

// Start the next stock TTS request when approximately 60% of the previous
// segment's estimated speaking time has elapsed, or when only a 3-second
// reserve remains, whichever happens earlier. Tiny segments get 0 delay.
func speechPrefetchDelay(duration time.Duration) time.Duration {
	if duration <= speechPrefetchReserve {
		return 0
	}
	atSixty := time.Duration(float64(duration) * .6)
	atReserve := duration - speechPrefetchReserve
	if atReserve < atSixty {
		return atReserve
	}
	return atSixty
}

// Choose 1–2 finished sentences available *at the moment of prefetch*.
// Two short sentences are combined to reduce per-request TTS overhead.
func speechBatchCount(ready []string) int {
	if len(ready) == 0 {
		return 0
	}
	if len(ready) == 1 {
		return 1
	}
	count := utf8.RuneCountInString(ready[0]) + utf8.RuneCountInString(ready[1]) + 1
	if speechBatchMaxSentences >= 2 && count <= speechBatchMaxRunes {
		return 2
	}
	return 1
}

func speechJoin(ready []string) string {
	return strings.Join(ready, " ")
}

// runPrefetchWorker starts the first fully generated sentence immediately.
// While it is being spoken, additional sentences accumulate in the channel.
// At 60% estimated playback (or 3 seconds before its end, whichever is
// earlier), the next available one/two sentences are sent to the existing
// stock TtsGenerate API. appendStreamedReply keeps the native Opus stream
// serial and does not overlap voices, even if the backend TTS stream is slow.
func (n *Native) runPrefetchWorker(p *nativeRequest, segments <-chan string, started time.Time) error {
	ready := make([]string, 0, 8)
	input := segments
	first := true
	var nextTime time.Time
	segmentNumber := 0
	for {
		if !first {
			// Collect all completed sentences available before the prefetch
			// deadline; waiting here is harmless to the independent LLM.
			for time.Until(nextTime) > 0 {
				delay := time.Until(nextTime)
				timer := time.NewTimer(delay)
				select {
				case <-p.ctx.Done():
					timer.Stop()
					return p.ctx.Err()
				case text, ok := <-input:
					timer.Stop()
					if ok {
						ready = append(ready, text)
					} else {
						input = nil
					}
				case <-timer.C:
				}
			}
		}
		if len(ready) == 0 {
			if input == nil {
				return nil
			}
			select {
			case <-p.ctx.Done():
				return p.ctx.Err()
			case text, ok := <-input:
				if !ok {
					input = nil
					return nil
				}
				ready = append(ready, text)
			}
		}
		// Drain one more *already available* sentence before making a TTS
		// request. We never wait for unfinished punctuation and never
		// postpone a ready segment just to reach a target duration.
		if !first && len(ready) < speechBatchMaxSentences && input != nil {
			select {
			case text, ok := <-input:
				if ok {
					ready = append(ready, text)
				} else {
					input = nil
				}
			default:
			}
		}
		batchCount := 1
		if !first {
			batchCount = speechBatchCount(ready)
		}
		chunk := speechJoin(ready[:batchCount])
		ready = ready[batchCount:]
		if err := p.ctx.Err(); err != nil {
			return err
		}
		var err error
		if first {
			err = n.beginStreamedReply(p, chunk)
			first = false
		} else {
			err = n.appendStreamedReply(p, chunk)
		}
		if err != nil {
			return err
		}
		segmentNumber++
		n.mu.Lock()
		measuredRate := p.speechCharsPerSecond
		n.mu.Unlock()
		if measuredRate == 0 {
			measuredRate = initialSpeechCharsPerSecond
		}
		estimated := estimatedSpeechDuration(chunk, measuredRate)
		delay := speechPrefetchDelay(estimated)
		nextTime = time.Now().Add(delay)
		slog.Info("native_stream_prefetch", "request", requestTag(p.id),
			"segment", segmentNumber, "elapsed_ms", time.Since(started).Milliseconds(),
			"text_bytes", len(chunk), "speech_estimate_ms", estimated.Milliseconds(),
			"next_prefetch_ms", delay.Milliseconds(), "queued_sentences", len(ready))
	}
}

