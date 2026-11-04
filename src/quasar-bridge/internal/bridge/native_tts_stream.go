package bridge

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/coder/websocket"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	"google.golang.org/protobuf/encoding/protowire"
	"quasarbridge/internal/wire"
)

const nativeStreamTTSSentinel = "/data/opt/quasar-bridge/enable-native-streaming-tts"

func nativeTTSFeatureEnabled() bool {
	_, err := os.Stat(nativeStreamTTSSentinel)
	return err == nil
}

// Both official event field IDs and payload numbers were verified on the
// actual Yandex UniProxy server, using an isolated live protocol session.
// The currently pinned Go protobuf package does not include this event.
func nativeDeferredTTSFrame(requestID, text string, chunk uint64, last bool) ([]byte, error) {
	payload := protowire.AppendTag(nil, 1, protowire.BytesType)
	payload = protowire.AppendString(payload, requestID)
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendString(payload, text)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	if last {
		payload = protowire.AppendVarint(payload, 1)
	} else {
		payload = protowire.AppendVarint(payload, 0)
	}
	payload = protowire.AppendTag(payload, 4, protowire.VarintType)
	payload = protowire.AppendVarint(payload, chunk)
	event := &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: newID()}}
	wireField := protowire.AppendTag(nil, 34, protowire.BytesType)
	wireField = protowire.AppendBytes(wireField, payload)
	event.ProtoReflect().SetUnknown(wireField)
	frame := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: event}}
	return (wire.Frame{Client: frame, Prefix: []byte("AAPI")}).Marshal()
}

func (n *Native) sendNativeTTSChunk(p *nativeRequest, text string, last bool) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	if p.canceled || !p.nativeTextStream {
		n.mu.Unlock()
		return context.Canceled
	}
	p.nativeTextChunk++
	index := p.nativeTextChunk
	id := p.ttsID
	n.mu.Unlock()
	// IMPORTANT: Preserve all characters, including spaces. Stream deltas
	// can start/end inside a word, and trimming would corrupt speech.
	b, err := nativeDeferredTTSFrame(id, text, index, last)
	if err != nil {
		return err
	}
	slog.Info("native_tts_incremental_text", "request", requestTag(p.id),
		"chunk", index, "last", last, "text_bytes", len(text))
	return n.up(p.ctx, websocket.MessageBinary, b)
}

func (n *Native) beginNativeStreamedReply(p *nativeRequest, text string) error {
	n.mu.Lock()
	p.nativeTextStream = true
	n.mu.Unlock()
	if err := n.beginStreamedReply(p, text); err != nil {
		return err
	}
	slog.Info("native_tts_one_session_started", "request", requestTag(p.id), "text_bytes", len(text))
	return nil
}

func (n *Native) appendNativeStreamedReply(p *nativeRequest, text string) error {
	if err := n.deferredPart(p, text, false); err != nil {
		return err
	}
	return n.sendNativeTTSChunk(p, text, false)
}

// The native streaming mode deliberately does NOT use streamedSentenceBuffer,
// runPrefetchWorker, or the legacy multi-synthesis audio stitcher. It streams
// *all* model text via DeferredTtsGenerate into ONE stock Yandex TTS session.
// Batching adjacent raw deltas here is only transport backpressure; no text
// is omitted and no separate native TtsGenerate is created for a sentence.
func (n *Native) streamSpeechNative(p *nativeRequest, prompt string, provider StreamingFallbackResolver) (Resolution, error) {
	const minBatchRunes = 16
	const maxBatchRunes = 128
	var pending, received string
	started := false
	chunks := 0
	ensureStarted := func() error {
		if started {
			return nil
		}
		if err := n.beginNativeStreamedReply(p, ""); err != nil {
			return err
		}
		started = true
		return nil
	}
	submit := func(text string) error {
		if text == "" {
			return nil
		}
		if err := ensureStarted(); err != nil {
			return err
		}
		if err := n.appendNativeStreamedReply(p, text); err != nil {
			return err
		}
		chunks++
		return nil
	}
	accept := func(delta string) error {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		if delta == "" {
			return nil
		}
		received += delta
		pending += delta
		for utf8.RuneCountInString(pending) >= minBatchRunes {
			// Batch only on an existing whitespace boundary. This does NOT
			// impose an artificial end of sentence or new synthesis.
			cut := strings.LastIndexAny(pending, " \r\n\t")
			if cut < 0 {
				if utf8.RuneCountInString(pending) < maxBatchRunes {
					break
				}
				// Never split a UTF-8 rune merely to satisfy a byte
				// limit: the protocol accepts the entire pending delta.
				cut = len(pending) - 1
			}
			part := pending[:cut+1]
			if err := submit(part); err != nil {
				return err
			}
			pending = pending[cut+1:]
		}
		return nil
	}
	result, err := provider.FallbackStream(p.ctx, prompt, accept)
	if err != nil {
		return result, err
	}
	// Some resolvers provide the complete answer only at return time;
	// others may have a final suffix not previously emitted as deltas.
	if received == "" {
		if err := accept(result.Answer); err != nil {
			return result, err
		}
	} else if strings.HasPrefix(result.Answer, received) {
		if err := accept(result.Answer[len(received):]); err != nil {
			return result, err
		}
	} else if result.Answer != "" && result.Answer != received {
		slog.Warn("native_stream_provider_text_mismatch", "received_bytes", len(received), "final_bytes", len(result.Answer))
	}
	if err := submit(pending); err != nil {
		return result, err
	}
	// A model producing no text still needs a deterministic final response.
	if !started {
		if err := ensureStarted(); err != nil {
			return result, err
		}
	}
	slog.Info("native_stream_complete", "request", requestTag(p.id),
		"native_chunks", chunks, "text_bytes", len(received))
	// All text was already sent via DeferredTtsGenerate. The ordinary
	// completion callback sends only IsLast=true with empty text.
	result.Answer = ""
	return result, nil
}

// The final streaming answer must NOT wait for the TTS stream to close:
// IsLast is precisely what tells Yandex to finish producing that stream.
func (n *Native) finishNativeStreamedReply(p *nativeRequest, tail string) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if err := n.deferredPart(p, tail, true); err != nil {
		return err
	}
	return n.sendNativeTTSChunk(p, tail, true)
}
