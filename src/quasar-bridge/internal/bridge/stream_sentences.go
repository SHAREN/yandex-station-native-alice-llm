package bridge

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// streamedSentenceEnd finds a complete sentence, including punctuation
// arriving at the end of a streamed token. Decimal points are held until
// the following token disambiguates them.
func streamedSentenceEnd(text string) int {
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r != '.' && r != '!' && r != '?' && r != '…' {
			i += size
			continue
		}
		end := i + size
		for end < len(text) {
			r, size = utf8.DecodeRuneInString(text[end:])
			if r != '.' && r != '!' && r != '?' && r != '…' &&
				r != '"' && r != '»' && r != '”' && r != '’' {
				break
			}
			end += size
		}
		if end == len(text) {
			// A decimal like "3." may still be followed by "14".
			if r == '.' && i > 0 {
				prev, _ := utf8.DecodeLastRuneInString(text[:i])
				if unicode.IsDigit(prev) {
					return -1
				}
			}
			return end
		}
		next, _ := utf8.DecodeRuneInString(text[end:])
		if unicode.IsSpace(next) {
			return end
		}
		i = end
	}
	return -1
}

// A new TtsGenerate request resets utterance prosody. Never create an
// artificial full stop or split solely at a word count: use a grammatical
// boundary that is already in the streamed text.
//
// For the first fragment, a short sentence is preferred; if the opening
// sentence is long, the first natural comma/semicolon/colon after 55 runes
// starts speech earlier. Later fragments prefer sentence endings and only
// fall back to clause punctuation after 300 runes.
const (
	firstClauseMinRunes  = 55
	laterClauseMinRunes  = 115
	laterClauseThreshold = 300
)

func streamedClausePauseEnd(text string, minRunes int) int {
	runes := 0
	for i, r := range text {
		runes++
		if r != ',' && r != ';' && r != ':' {
			continue
		}
		if runes < minRunes {
			continue
		}
		end := i + utf8.RuneLen(r)
		// A decimal or thousands separator is not a breathing pause.
		if r == ',' && i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:i])
			if unicode.IsDigit(prev) {
				if end == len(text) {
					return -1 // wait for next token to disambiguate 3,14
				}
				next, _ := utf8.DecodeRuneInString(text[end:])
				if unicode.IsDigit(next) {
					continue
				}
			}
		}
		if end < len(text) {
			next, _ := utf8.DecodeRuneInString(text[end:])
			if !unicode.IsSpace(next) {
				continue
			}
		}
		return end
	}
	return -1
}

func (b *streamedSentenceBuffer) nextBoundary() int {
	endOfSentence := streamedSentenceEnd(b.pending)
	if b.emitted == "" {
		// The first sentence is the preferred boundary when it is short.
		// If a long opening clause has a comma first, start playback there.
		clauseEnd := streamedClausePauseEnd(b.pending, firstClauseMinRunes)
		if clauseEnd > 0 && (endOfSentence < 0 || clauseEnd < endOfSentence) {
			return clauseEnd
		}
		return endOfSentence
	}
	if endOfSentence > 0 && utf8.RuneCountInString(b.pending[:endOfSentence]) < laterClauseThreshold {
		return endOfSentence
	}
	// For subsequent very long sentences, prefer a real pause. This is
	// never a mid-word or arbitrary character-limit split.
	if utf8.RuneCountInString(b.pending) >= laterClauseThreshold {
		clauseEnd := streamedClausePauseEnd(b.pending, laterClauseMinRunes)
		if clauseEnd > 0 && (endOfSentence < 0 || clauseEnd < endOfSentence) {
			return clauseEnd
		}
	}
	return endOfSentence
}

type streamedSentenceBuffer struct {
	pending, emitted string
}

func (b *streamedSentenceBuffer) feed(delta string, emit func(string) error) error {
	b.pending += delta
	for {
		end := b.nextBoundary()
		if end <= 0 {
			return nil
		}
		raw := b.pending[:end]
		text := strings.TrimSpace(raw)
		if text != "" {
			if err := emit(text); err != nil {
				return err
			}
		}
		b.emitted += raw
		b.pending = b.pending[end:]
	}
}

func (b *streamedSentenceBuffer) remaining(full string) string {
	if b.emitted == "" {
		return full
	}
	if strings.HasPrefix(full, b.emitted) {
		return strings.TrimSpace(full[len(b.emitted):])
	}
	// Never replay already-synthesized audio if the provider normalizes its
	// final answer differently from its streamed deltas.
	slog.Warn("native_stream_prefix_mismatch", "emitted_bytes", len(b.emitted), "answer_bytes", len(full))
	return strings.TrimSpace(b.pending)
}

// streamSpeech uses Yandex's existing TtsGenerate + one continuous Opus stream.
// Text generation and audio synthesis proceed independently. Each new completed
// sentence is queued; the single audio worker plays segments in order, waiting
// for the preceding stock TTS segment before requesting the next one.
func (n *Native) streamSpeech(p *nativeRequest, prompt string, streaming StreamingFallbackResolver) (Resolution, error) {
	if n.NativeStreamingTTS || nativeTTSFeatureEnabled() {
		return n.streamSpeechNative(p, prompt, streaming)
	}
	segments := make(chan string, 64)
	workerDone := make(chan struct{})
	var workerErr error
	started := time.Now()
	go func() {
		defer close(workerDone)
		workerErr = n.runPrefetchWorker(p, segments, started)
	}()
	var buffer streamedSentenceBuffer
	result, err := streaming.FallbackStream(p.ctx, prompt, func(delta string) error {
		return buffer.feed(delta, func(sentence string) error {
			select {
			case <-p.ctx.Done():
				return p.ctx.Err()
			case <-workerDone:
				if workerErr != nil {
					return workerErr
				}
				return errors.New("streaming audio worker stopped unexpectedly")
			case segments <- sentence:
				return nil
			}
		})
	})
	close(segments)
	select {
	case <-workerDone:
		if workerErr != nil && err == nil {
			err = workerErr
		}
	case <-p.ctx.Done():
		if err == nil {
			err = context.Canceled
		}
		// The worker observes the same context and must exit before this
		// request can be discarded or its TTS state freed.
		<-workerDone
	}
	if err == nil {
		result.Answer = buffer.remaining(result.Answer)
	}
	return result, err
}
