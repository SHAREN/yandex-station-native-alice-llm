package bridge

import (
	"encoding/binary"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/coder/websocket"
	response "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_response"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	stream "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/stream_control"
	tts "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/tts_speak"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"quasarbridge/internal/wire"
)

const externalTTSFormat = "audio/opus"

var externalStreamSequence atomic.Uint32

func nextExternalStreamID() uint32 {
	// Stock UniProxy allocates server-originated output streams as small even
	// identifiers (2, 4, 6, ...). Mirror that convention so firmware media
	// routing treats Gemini TTS exactly like ordinary Alice cloud TTS.
	return externalStreamSequence.Add(1) << 1
}

func (n *Native) beginExternalTakeover(p *nativeRequest) error {
	p.externalMu.Lock()
	defer p.externalMu.Unlock()
	if p.externalFinished || p.ctx.Err() != nil {
		return errors.New("external takeover is no longer active")
	}
	// The stock VoiceInput is only a bootstrap trigger. Acknowledge input start
	// but do not emit an AliceResponse yet: an early response can make firmware
	// stop its stock AudioStream before the direct vqe_0 subscriber is ready.
	if err := n.ack(p); err != nil {
		return err
	}
	if inputStream := p.header.GetStreamId(); inputStream != 0 {
		slog.Info("external_input_stream_kept_local", "request", requestTag(p.id), "stream", inputStream)
	}
	slog.Info("external_tts_takeover_started", "request", requestTag(p.id), "stock_dialog_held", false, "bootstrap_pending", true)
	return nil
}

func (n *Native) completeExternalStockInputLocked(p *nativeRequest) error {
	if p.externalStockCompleted {
		return nil
	}
	r := &response.TAliceResponseProto{
		Header: &response.TAliceResponseProto_THeader{
			RequestId:      new(p.applicationID()),
			ResponseId:     new(p.responseID),
			DialogId:       new(p.request.GetHeader().GetDialogId()),
			SequenceNumber: new(p.request.GetHeader().GetSequenceNumber()),
		},
		VoiceResponse: &response.TAliceResponseProto_TVoiceResponse{
			HasVoiceResponse: false,
			ShouldListen:     false,
			OutputSpeech:     &response.TAliceResponseProto_TVoiceResponse_TOutputSpeech{Text: new("")},
		},
		Response: &response.TAliceResponseProto_TResponse{IsStreaming: false, Directives: n.takeSound(p)},
	}
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{
		Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id},
		Event:  &server.TServerMessage_TEvent_AliceResponse{AliceResponse: r},
	}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if err := n.down(p.ctx, websocket.MessageBinary, b); err != nil {
		return err
	}
	p.externalStockCompleted = true
	slog.Info("external_stock_voiceinput_completed", "request", requestTag(p.id))
	return nil
}

// ExternalInputComplete releases only the bootstrap stock VoiceInput after
// Gemini is connected and the direct vqe_0 subscriber has delivered audio.
// The external takeover itself remains active, so the cross-session fence and
// stock stop routing continue until Gemini explicitly ends.
func (n *Native) ExternalInputComplete() error {
	p, err := n.externalRequest()
	if err != nil {
		return err
	}
	p.externalMu.Lock()
	defer p.externalMu.Unlock()
	return n.completeExternalStockInputLocked(p)
}

func (n *Native) externalRequest() (*nativeRequest, error) {
	n.mu.Lock()
	p := n.external
	n.mu.Unlock()
	if p == nil || p.ctx.Err() != nil || p.externalFinished {
		return nil, errors.New("no active external Gemini takeover")
	}
	return p, nil
}

// ExternalTTSActive reports whether this UniProxy session currently owns a
// Gemini takeover request. It is intended for the loopback playback broker.
func (n *Native) ExternalTTSActive() bool {
	_, err := n.externalRequest()
	return err == nil
}

// Closed reports whether this Native session's websocket/proxy lifecycle has
// ended. The loopback broker uses it to discard stale parallel UniProxy
// sessions without confusing them with an inactive-but-still-live session.
func (n *Native) Closed() bool {
	return n.ctx.Err() != nil
}

// ExternalTTSBegin starts one stock AudioClientTtsPlayer stream. Gemini audio
// is encoded as ordinary Ogg/Opus and announced with the same audio/opus format
// used by stock Yandex cloud TTS.
func (n *Native) ExternalTTSBegin(format string) error {
	p, err := n.externalRequest()
	if err != nil {
		return err
	}
	if format == "" {
		format = externalTTSFormat
	}
	p.externalMu.Lock()
	defer p.externalMu.Unlock()
	if p.externalFinished || p.ctx.Err() != nil {
		return errors.New("external takeover ended")
	}
	if p.externalStream != 0 {
		return nil
	}
	streamID := nextExternalStreamID()
	msg := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{
		Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id, StreamId: new(int64(streamID))},
		Event: &server.TServerMessage_TEvent_TtsSpeak{TtsSpeak: &tts.TTtsSpeak{
			Format:           format,
			LazyTtsStreaming: false,
		}},
	}}}
	b, err := (wire.Frame{Server: msg, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if err := n.down(p.ctx, websocket.MessageBinary, b); err != nil {
		return err
	}
	p.externalStream = streamID
	slog.Info("external_tts_stream_started", "request", requestTag(p.id), "stream", streamID, "format", format)
	return nil
}

func (n *Native) ExternalTTSWrite(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	p, err := n.externalRequest()
	if err != nil {
		return err
	}
	p.externalMu.Lock()
	defer p.externalMu.Unlock()
	if p.externalStream == 0 {
		return errors.New("external TTS stream is not started")
	}
	frame := make([]byte, 4, 4+len(pcm))
	binary.BigEndian.PutUint32(frame, p.externalStream)
	frame = append(frame, pcm...)
	return n.down(p.ctx, websocket.MessageBinary, frame)
}

func (n *Native) closeExternalStream(p *nativeRequest, reason stream.TStreamControl_TActionClose_EReason) error {
	if p.externalStream == 0 {
		return nil
	}
	streamID := p.externalStream
	p.externalStream = 0
	msg := &server.TServerMessage{Message: &server.TServerMessage_StreamControl{StreamControl: &stream.TStreamControl{
		MessageId: p.id,
		StreamId:  int64(streamID),
		Action: &stream.TStreamControl_Close{Close: &stream.TStreamControl_TActionClose{
			Reason: reason,
		}},
	}}}
	b, err := (wire.Frame{Server: msg, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if err := n.down(p.ctx, websocket.MessageBinary, b); err != nil {
		return err
	}
	slog.Info("external_tts_stream_closed", "request", requestTag(p.id), "stream", streamID, "reason", reason.String())
	return nil
}

func (n *Native) ExternalTTSEnd() error {
	p, err := n.externalRequest()
	if err != nil {
		return err
	}
	p.externalMu.Lock()
	defer p.externalMu.Unlock()
	return n.closeExternalStream(p, stream.TStreamControl_TActionClose_OK)
}

func (n *Native) ExternalTTSInterrupt() error {
	p, err := n.externalRequest()
	if err != nil {
		return err
	}
	p.externalMu.Lock()
	defer p.externalMu.Unlock()
	return n.closeExternalStream(p, stream.TStreamControl_TActionClose_CANCEL)
}

// ExternalSessionEnd cleanly closes the stock local dialog. This is what
// prevents the firmware from reaching its 12-second recognition timeout and
// playing Alice's built-in error phrase in the background.
func (n *Native) ExternalSessionEnd() error {
	p, err := n.externalRequest()
	if err != nil {
		return err
	}
	p.externalMu.Lock()
	if p.externalFinished {
		p.externalMu.Unlock()
		return nil
	}
	if err := n.closeExternalStream(p, stream.TStreamControl_TActionClose_CANCEL); err != nil {
		p.externalMu.Unlock()
		return err
	}
	if !p.externalStockCompleted {
		if err := n.completeExternalStockInputLocked(p); err != nil {
			p.externalMu.Unlock()
			return err
		}
	}
	p.externalFinished = true
	p.externalMu.Unlock()

	n.mu.Lock()
	if n.external == p {
		n.external = nil
	}
	delete(n.requests, p.id)
	delete(n.tts, p.ttsID)
	delete(n.tts, p.progressTTS)
	for streamID, owner := range n.clientStreams {
		if owner == p {
			delete(n.clientStreams, streamID)
		}
	}
	n.mu.Unlock()
	if p.expires != nil {
		p.expires.Stop()
	}
	p.cancel()
	slog.Info("external_tts_takeover_finished", "request", requestTag(p.id))
	return nil
}
