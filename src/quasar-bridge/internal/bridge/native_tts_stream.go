package bridge

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

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
	// Nonfinal pieces include a space; otherwise the cloud might concatenate
	// the final word of one chunk and the first word of the next.
	text = strings.TrimSpace(text)
	if !last && text != "" {
		text += " "
	}
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

// The LLM returns first completed sentence/clause immediately, then
// progressively supplies more text while the Yandex server synthesizes the
// SAME TTS session. No synthetic 60%-timer or extra TtsGenerate is needed.
func (n *Native) streamSpeechNative(p *nativeRequest, prompt string, provider StreamingFallbackResolver) (Resolution, error) {
	segments := make(chan string, 64)
	workerDone := make(chan struct{})
	var workerErr error
	started := time.Now()
	go func() {
		defer close(workerDone)
		first := true
		for text := range segments {
			if err := p.ctx.Err(); err != nil {
				workerErr = err
				return
			}
			if first {
				workerErr = n.beginNativeStreamedReply(p, text)
				first = false
			} else {
				workerErr = n.appendNativeStreamedReply(p, text)
			}
			if workerErr != nil {
				return
			}
			slog.Info("native_stream_chunk_submitted", "request", requestTag(p.id), "elapsed_ms", time.Since(started).Milliseconds())
		}
	}()
	var buffer streamedSentenceBuffer
	result, err := provider.FallbackStream(p.ctx, prompt, func(delta string) error {
		return buffer.feed(delta, func(text string) error {
			select {
			case <-p.ctx.Done():
				return p.ctx.Err()
			case <-workerDone:
				if workerErr != nil {
					return workerErr
				}
				return context.Canceled
			case segments <- text:
				return nil
			}
		})
	})
	close(segments)
	<-workerDone
	if workerErr != nil && err == nil {
		err = workerErr
	}
	if err == nil {
		result.Answer = buffer.remaining(result.Answer)
	}
	return result, err
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
