package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
	response "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_response"
	cancellation "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/cancel_request"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	continuation "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/continue_request"
	ack "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/input_start_ack"
	tts "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/tts_generate"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/encoding/protowire"

	"quasarbridge/internal/dialogue"
	"quasarbridge/internal/sound"
	"quasarbridge/internal/wire"
)

type Resolution struct {
	Route  string
	Answer string
}
type Resolver interface {
	Resolve(context.Context, string) (Resolution, error)
}
type RoutedResolver interface {
	Decide(context.Context, string) RouteDecision
	ResolveDecision(context.Context, string, RouteDecision) (Resolution, error)
}

type ExplicitRuleResolver interface {
	MatchExplicitRule(string) (RouteDecision, bool)
}

type FallbackResolver interface {
	Fallback(context.Context, string) (Resolution, error)
}

type StreamingFallbackResolver interface {
	FallbackStream(context.Context, string, func(string) error) (Resolution, error)
}

type VoiceMode string

const (
	VoiceModeStock    VoiceMode = "stock"
	VoiceModePass     VoiceMode = "pass"
	VoiceModeTakeover VoiceMode = "takeover"
	VoiceModeQuasar   VoiceMode = "quasar"
)

type ASRUpdate struct {
	Request string
	Text    string
	Final   bool
}

type nativeRequest struct {
	streaming                       bool
	nativeTextStream                bool
	nativeTextChunk                 uint64
	listenOnStreamStart             bool
	partial                         uint32
	responseID                      string
	external                        bool
	externalFinished                bool
	externalStockCompleted          bool
	externalStream                  uint32
	externalMu                      sync.Mutex
	final                           bool
	header                          *header.TEventHeader
	isText                          bool
	ttsStarted                      bool
	gated                           bool
	input                           []byte
	id                              string
	request                         *request.TAliceRequestProto
	text                            string
	route                           string
	ctx                             context.Context
	cancel                          context.CancelFunc
	started, canceled, local, cloud bool
	continuation                    []byte
	ttsID                           string
	progressTTS                     string
	ttsSentAt                       time.Time
	ttsSpeakAt                      time.Time
	ttsTextRunes                    int
	ttsOrdinal                      uint32
	speechCharsPerSecond            float64
	audio                           oggSpeech
	audioIsProgress                 bool
	discardProgressAudio            bool
	audioStarted                    bool
	audioStream                     uint32
	audioWaiting                    bool
	progressAudioDone               chan struct{}
	soundRequest                    *sound.Request
	soundDirective                  *soundDirective
	soundSealed                     bool
	expires                         *time.Timer
	stockCapture                    *os.File
	stockCaptureBytes               int64
	forceLLM                        bool
}

const (
	stockAudioCaptureSentinel = "/tmp/enable-stock-audio-capture"
	stockAudioCaptureLimit    = int64(12 << 20)
	stockAudioTimingPath      = "/tmp/quasar-audio-capture-timing.log"
)

func stockAudioCaptureEnabled() bool {
	_, err := os.Stat(stockAudioCaptureSentinel)
	return err == nil
}

func appendStockAudioTiming(event, request string, streamID uint32, bytes int64) {
	if !stockAudioCaptureEnabled() {
		return
	}
	f, err := os.OpenFile(stockAudioTimingPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s|%s|%s|%d|%d\n", time.Now().UTC().Format(time.RFC3339Nano), event, request, streamID, bytes)
}

func closeStockCapture(p *nativeRequest) {
	if p != nil && p.stockCapture != nil {
		streamID := uint32(0)
		if p.header != nil {
			streamID = uint32(p.header.GetStreamId())
		}
		appendStockAudioTiming("input_close", requestTag(p.id), streamID, p.stockCaptureBytes)
		_ = p.stockCapture.Close()
		p.stockCapture = nil
	}
}

type Native struct {
	connectionScope  string
	ctx              context.Context
	cancel           context.CancelFunc
	up, down         Sender
	resolver         Resolver
	mu               sync.Mutex
	requests         map[string]*nativeRequest
	tts              map[string]*nativeRequest
	streams          map[uint32]*nativeRequest
	blocked          map[uint32]*nativeRequest
	clientStreams    map[uint32]*nativeRequest
	ttsCaptures      map[uint32]*os.File
	ttsCaptureBytes  map[uint32]int64
	external         *nativeRequest
	wg               sync.WaitGroup
	OnRoute          func(id, route string)
	OnVoiceMode      func(id string) VoiceMode
	OnVoiceInput     func(id string) bool
	OnASR            func(ASRUpdate)
	OnTextInput      func(text string) bool
	OnUserStop       func(id string)
	OnEndOfUtterance func(id string)
	GateText         bool
	ShouldListen     bool
	DecisionDelay    time.Duration
	ProgressAfter    time.Duration
	ProgressInterval time.Duration
	// Enable native single-session Yandex TTS; defaults off in tests.
	NativeStreamingTTS bool
}

func NewNative(ctx context.Context, up, down Sender, resolver Resolver) *Native {
	ctx, cancel := context.WithCancel(ctx)
	return &Native{connectionScope: newID(), ctx: ctx, cancel: cancel, up: up, down: down, resolver: resolver, ShouldListen: true, requests: map[string]*nativeRequest{}, tts: map[string]*nativeRequest{}, streams: map[uint32]*nativeRequest{}, blocked: map[uint32]*nativeRequest{}, clientStreams: map[uint32]*nativeRequest{}, ttsCaptures: map[uint32]*os.File{}, ttsCaptureBytes: map[uint32]int64{}}
}
func (n *Native) Close() {
	n.cancel()
	n.mu.Lock()
	for _, p := range n.requests {
		p.canceled = true
		p.cancel()
		closeStockCapture(p)
		if p.expires != nil {
			p.expires.Stop()
		}
	}
	for streamID, capture := range n.ttsCaptures {
		_ = capture.Close()
		delete(n.ttsCaptures, streamID)
		delete(n.ttsCaptureBytes, streamID)
	}
	n.mu.Unlock()
	n.wg.Wait()
}

func (n *Native) stopExternalSessionFromStockCommand(source string) bool {
	p, err := n.externalRequest()
	if err != nil {
		return false
	}
	requestID := p.id
	slog.Info("external_user_stop", "request", requestTag(requestID), "source", source)
	if err := n.ExternalTTSInterrupt(); err != nil {
		slog.Warn("external_user_stop_tts_interrupt_failed", "request", requestTag(requestID), "source", source, "error", err)
	}
	// Keep the Station's own short cancel cue, but consume the command locally
	// so stock Alice does not start a competing Vins.TextInput/UniProxy dialog.
	if err := n.queueSound(p.ctx, p, sound.Request{Sound: "spotter_cancel"}); err != nil {
		slog.Warn("external_user_stop_cue_failed", "request", requestTag(requestID), "source", source, "error", err)
	}
	if n.OnUserStop != nil {
		n.OnUserStop(requestID)
	}
	if err := n.ExternalSessionEnd(); err != nil {
		slog.Warn("external_user_stop_session_end_failed", "request", requestTag(requestID), "source", source, "error", err)
	}
	return true
}

// ExternalUserStop lets a sibling UniProxy session forward a locally detected
// stock command-spotter stop to the Native session that actually owns Gemini.
func (n *Native) ExternalUserStop(source string) bool {
	return n.stopExternalSessionFromStockCommand(source)
}
func (n *Native) Client(ctx context.Context, k websocket.MessageType, data []byte) error {
	f := wire.Decode(data, wire.ClientToServer)
	if f.Client == nil {
		if len(data) >= 4 {
			streamID := binary.BigEndian.Uint32(data)
			n.mu.Lock()
			p := n.clientStreams[streamID]
			drop := p != nil && p.external && !p.externalFinished
			if p != nil && p.stockCapture != nil && !p.external && p.stockCaptureBytes < stockAudioCaptureLimit {
				payload := data[4:]
				remaining := stockAudioCaptureLimit - p.stockCaptureBytes
				if int64(len(payload)) > remaining {
					payload = payload[:remaining]
				}
				if len(payload) > 0 {
					if p.stockCaptureBytes == 0 {
						appendStockAudioTiming("input_first_chunk", requestTag(p.id), streamID, int64(len(payload)))
						slog.Info("stock_input_capture_first_chunk", "request", requestTag(p.id), "stream", streamID, "bytes", len(payload))
					}
					if written, err := p.stockCapture.Write(payload); err != nil {
						slog.Warn("stock_input_capture_write_failed", "request", requestTag(p.id), "error", err)
						closeStockCapture(p)
					} else {
						p.stockCaptureBytes += int64(written)
					}
				}
			}
			n.mu.Unlock()
			if drop {
				return nil
			}
		}
		return n.up(ctx, k, data)
	}
	if sc := f.Client.GetStreamControl(); sc != nil {
		streamID := uint32(sc.GetStreamId())
		n.mu.Lock()
		p := n.clientStreams[streamID]
		if p != nil && sc.GetClose() != nil {
			delete(n.clientStreams, streamID)
			if p.external {
				slog.Info("external_input_stream_close", "request", requestTag(p.id), "stream", streamID, "reason", sc.GetClose().GetReason().String())
			} else if p.stockCapture != nil {
				slog.Info("stock_input_capture_closed", "request", requestTag(p.id), "stream", streamID, "bytes", p.stockCaptureBytes)
				closeStockCapture(p)
			}
		}
		if p != nil && p.external {
			if sc.GetClose() == nil {
				slog.Info("external_input_stream_control", "request", requestTag(p.id), "stream", streamID, "kind", fmt.Sprintf("%T", sc.GetAction()))
			}
			n.mu.Unlock()
			return nil
		}
		n.mu.Unlock()
		return n.up(ctx, k, data)
	}
	if f.Client.GetEvent() == nil {
		return n.up(ctx, k, data)
	}
	e := f.Client.GetEvent()
	if stat := e.GetRequestStat(); stat != nil {
		kind := "none"
		raw := strings.ToLower(stat.GetError())
		if raw != "" {
			kind = "other"
			if strings.Contains(raw, "timeout") || strings.Contains(raw, "timed out") || strings.Contains(raw, "deadline") {
				kind = "timeout"
			} else if strings.Contains(raw, "cancel") {
				kind = "canceled"
			}
		}
		timestamps := stat.GetTimestamps()
		reported := func(value string) bool { return value != "" && value != "0" }
		slog.Info("native_playback_status", "request", requestTag(stat.GetRequestMessageId()), "error_class", kind, "playback_reported", reported(timestamps.GetOnSoundPlayerBeginTime()) || reported(timestamps.GetOnPlayerBegin()), "finished_reported", reported(timestamps.GetOnSoundPlayerEndTime()) || reported(timestamps.GetOnPlayerEnd()))
	}
	var req *request.TAliceRequestProto
	switch f.Kind() {
	case "TextInput":
		req = e.GetTextInput()
	case "VoiceInput":
		req = e.GetVoiceInput()
	case "SpeechInput":
		req = e.GetSpeechInput()
	}
	if req != nil && f.Kind() == "TextInput" {
		text := strings.ToLower(strings.TrimSpace(req.GetRequest().GetEvent().GetText()))
		if n.OnTextInput != nil && n.OnTextInput(text) {
			return nil
		}
		if (text == "стоп" || text == "хватит") && n.stopExternalSessionFromStockCommand("command_spotter_text") {
			return nil
		}
		if text == "" {
			return n.up(ctx, k, data)
		}
	}
	if req != nil {
		id := e.GetHeader().GetMessageId()
		if id == "" {
			return n.up(ctx, k, data)
		}
		voiceMode := VoiceModePass
		if f.Kind() == "VoiceInput" || f.Kind() == "SpeechInput" {
			if n.OnVoiceMode != nil {
				voiceMode = n.OnVoiceMode(id)
				switch voiceMode {
				case VoiceModeStock, VoiceModePass, VoiceModeTakeover, VoiceModeQuasar:
				default:
					slog.Warn("native_voice_mode_unknown", "request", requestTag(id), "mode", voiceMode)
					voiceMode = VoiceModeStock
				}
			} else if n.OnVoiceInput != nil && n.OnVoiceInput(id) {
				voiceMode = VoiceModeTakeover
			}
		}
		if voiceMode == VoiceModeStock {
			// Strict stock mode is a transparent UniProxy pass-through. Do not add
			// WaitClientApproval, track the request, classify ASR, or synthesize a
			// local reply: the firmware and Yandex backend own the whole turn.
			slog.Info("native_voice_stock_passthrough", "request", requestTag(id), "kind", f.Kind())
			return n.up(ctx, k, data)
		}
		takeover := voiceMode == VoiceModeTakeover
		if takeover {
			slog.Info("native_voice_takeover", "request", requestTag(id), "kind", f.Kind())
		} else if voiceMode == VoiceModeQuasar {
			slog.Info("native_voice_quasar", "request", requestTag(id), "kind", f.Kind())
		}
		scope := "connection:" + n.connectionScope
		if id := req.GetApplication().GetDeviceId(); id != "" {
			scope = "device:" + id
		} else if id := req.GetApplication().GetUuid(); id != "" {
			scope = "uuid:" + id
		}
		baseCtx := dialogue.WithSpeaker(n.ctx, scope)
		var pctx context.Context
		var cancel context.CancelFunc
		if takeover {
			// A Gemini full-duplex takeover is a conversation container, not one
			// short cloud request. It must survive arbitrary user pauses and
			// repeated barge-ins until the agent explicitly ends it or the
			// UniProxy connection itself goes away.
			pctx, cancel = context.WithCancel(baseCtx)
		} else {
			pctx, cancel = context.WithTimeout(baseCtx, 90*time.Second)
		}
		playerState := nativePlaybackState(req)
		pctx = sound.WithPlaybackState(pctx, playerState)
		slog.Info("native_player_state", "request", requestTag(id), "present", playerState.Present, "player_state", playerState.PlayerState, "offset_known", playerState.OffsetKnown, "offset_ms", playerState.OffsetMS, "has_stream", playerState.StreamID != "", "stream_tag", requestTag(playerState.StreamID))
		p := &nativeRequest{id: id, request: proto.Clone(req).(*request.TAliceRequestProto), text: req.GetRequest().GetEvent().GetText(), ctx: pctx, cancel: cancel, forceLLM: voiceMode == VoiceModeQuasar}
		p.ctx = sound.WithPlayer(p.ctx, func(ctx context.Context, request sound.Request) error {
			return n.queueSound(ctx, p, request)
		})
		p.header = proto.Clone(e.GetHeader()).(*header.TEventHeader)
		p.isText = f.Kind() == "TextInput"
		p.input = append([]byte{}, data...)
		p.gated = !p.isText || n.GateText

		slog.Info("native_request_shape", "request", requestTag(id), "text_bytes", len(p.text), "event_type", req.GetRequest().GetEvent().GetType().String(), "stream", e.GetHeader().GetStreamId(), "unknown_request_bytes", len(req.ProtoReflect().GetUnknown()), "payload_bytes", len(req.GetRequest().GetEvent().GetPayloadRaw()))
		n.mu.Lock()
		if n.requests[id] != nil {
			n.mu.Unlock()
			cancel()
			return nil
		}
		if len(n.requests) >= 128 {
			n.mu.Unlock()
			cancel()
			return errors.New("too many pending native requests")
		}
		if preparer, ok := n.resolver.(interface {
			Prepare(context.Context) context.Context
		}); ok {
			p.ctx = preparer.Prepare(p.ctx)
		}
		if !takeover {
			p.expires = time.AfterFunc(90*time.Second, func() {
				n.mu.Lock()
				defer n.mu.Unlock()
				p.canceled = true
				p.cancel()
				closeStockCapture(p)
				delete(n.requests, p.id)
				delete(n.tts, p.ttsID)
				delete(n.tts, p.progressTTS)
				if n.external == p {
					n.external = nil
				}
				for stream, q := range n.blocked {
					if q == p {
						delete(n.blocked, stream)
					}
				}
				for stream, q := range n.streams {
					if q == p {
						delete(n.streams, stream)
					}
				}
				for stream, q := range n.clientStreams {
					if q == p {
						delete(n.clientStreams, stream)
					}
				}
			})
		}
		n.requests[id] = p
		if streamID := uint32(p.header.GetStreamId()); streamID != 0 && (f.Kind() == "VoiceInput" || f.Kind() == "SpeechInput") {
			n.clientStreams[streamID] = p
		}
		if !takeover && f.Kind() == "VoiceInput" && stockAudioCaptureEnabled() {
			capturePath := fmt.Sprintf("/tmp/quasar-stock-input-%s-%d.ogg", requestTag(id), p.header.GetStreamId())
			capture, err := os.OpenFile(capturePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
			if err != nil {
				slog.Warn("stock_input_capture_open_failed", "request", requestTag(id), "error", err)
			} else {
				p.stockCapture = capture
				slog.Info("stock_input_capture_started", "request", requestTag(id), "stream", p.header.GetStreamId(), "format", req.GetFormat(), "path", capturePath)
			}
		}
		if takeover {
			p.local = true
			p.streaming = true
			p.external = true
			p.responseID = newID()
			if previous := n.external; previous != nil && previous != p {
				previous.canceled = true
				previous.cancel()
			}
			n.external = p
		}
		n.mu.Unlock()
		if takeover {
			if err := n.beginExternalTakeover(p); err != nil {
				slog.Warn("native_voice_takeover_begin_failed", "request", requestTag(id), "error", err)
				p.cancel()
				return err
			}
			return nil
		}
		if p.gated {
			gated, err := wire.EnableApproval(f)
			if err != nil {
				return err
			}
			if err := n.up(ctx, k, gated); err != nil {
				return err
			}
		}
		// The original input stays local until routing has selected exactly one executor.
		n.maybeResolve(p)
		return nil
	}
	if c := e.GetContinueRequest(); c != nil {
		n.mu.Lock()
		p := n.requests[c.GetRequestMessageId()]
		if p != nil && p.external && !p.externalFinished {
			slog.Info("external_continue_request", "request", requestTag(p.id))
		}
		if p != nil && (p.canceled || p.local) {
			n.mu.Unlock()
			return nil
		}
		if p != nil && !p.cloud {
			if p.continuation != nil {
				n.mu.Unlock()
				return nil
			}
			p.continuation = append([]byte{}, data...)
			n.mu.Unlock()
			n.maybeResolve(p)
			return nil
		}
		n.mu.Unlock()
	}
	if finish := e.GetFinishRequest(); finish != nil {
		n.mu.Lock()
		p := n.requests[finish.GetRequestMessageId()]
		external := p != nil && p.external && !p.externalFinished
		requestID := ""
		callback := n.OnEndOfUtterance
		if p != nil {
			requestID = p.id
		}
		n.mu.Unlock()
		if external {
			ar := finish.GetAliceRequest()
			eventType := ""
			endOfUtterance := false
			textBytes := 0
			payloadBytes := 0
			if ar != nil && ar.GetRequest() != nil && ar.GetRequest().GetEvent() != nil {
				ev := ar.GetRequest().GetEvent()
				eventType = ev.GetType().String()
				endOfUtterance = ev.GetEndOfUtterance()
				textBytes = len(ev.GetText())
				payloadBytes = len(ev.GetPayloadRaw())
			}
			slog.Info("external_finish_request", "request", requestTag(requestID), "event_type", eventType, "end_of_utterance", endOfUtterance, "text_bytes", textBytes, "payload_bytes", payloadBytes)
			if endOfUtterance && callback != nil {
				callback(requestID)
			}
		}
	}
	if c := e.GetCancelRequest(); c != nil {
		n.mu.Lock()
		p := n.requests[c.GetRequestMessageId()]
		if p != nil && p.external && !p.externalFinished {
			userStop := c.GetUserStop() != nil
			requestID := p.id
			n.mu.Unlock()
			if userStop {
				n.stopExternalSessionFromStockCommand("cancel_request_user_stop")
				return nil
			}
			slog.Info("external_client_cancel_ignored", "request", requestTag(requestID), "reason", fmt.Sprintf("%T", c.GetReason()))
			return nil
		}
		ttsID := ""
		local := false
		if p != nil {
			p.canceled = true
			p.cancel()
			ttsID = p.ttsID
			local = p.local
		}
		n.mu.Unlock()
		if ttsID != "" && !local {
			_ = n.sendCancel(ctx, ttsID)
		}
		if local {
			return nil
		}
	}
	return n.up(ctx, k, data)
}
func (n *Native) findLocked(h *header.TEventHeader) (*nativeRequest, bool) {
	if h == nil {
		return nil, false
	}
	for _, id := range []string{h.GetRefMessageId(), h.GetSubRequestRef().GetMessageId(), h.GetSubRequestRef().GetRequestId()} {
		if p := n.tts[id]; p != nil {
			return p, true
		}
		if p := n.requests[id]; p != nil {
			return p, false
		}
		if id != "" {
			for _, p := range n.requests {
				if p.request.GetHeader().GetRequestId() == id {
					return p, false
				}
			}
		}
	}
	return nil, false
}
func (n *Native) Server(ctx context.Context, k websocket.MessageType, data []byte) error {
	f := wire.Decode(data, wire.ServerToClient)
	if f.Server == nil {
		if len(data) >= 4 {
			streamID := binary.BigEndian.Uint32(data)
			n.mu.Lock()
			p := n.streams[streamID]
			if capture := n.ttsCaptures[streamID]; capture != nil && n.ttsCaptureBytes[streamID] < stockAudioCaptureLimit {
				payload := data[4:]
				remaining := stockAudioCaptureLimit - n.ttsCaptureBytes[streamID]
				if int64(len(payload)) > remaining {
					payload = payload[:remaining]
				}
				if len(payload) > 0 {
					if n.ttsCaptureBytes[streamID] == 0 {
						request := ""
						if p != nil {
							request = requestTag(p.id)
						}
						appendStockAudioTiming("tts_first_chunk", request, streamID, int64(len(payload)))
						slog.Info("tts_output_capture_first_chunk", "stream", streamID, "bytes", len(payload))
					}
					if written, err := capture.Write(payload); err != nil {
						slog.Warn("tts_output_capture_write_failed", "stream", streamID, "error", err)
						_ = capture.Close()
						delete(n.ttsCaptures, streamID)
						delete(n.ttsCaptureBytes, streamID)
					} else {
						n.ttsCaptureBytes[streamID] += int64(written)
					}
				}
			}
			drop := n.blocked[streamID] != nil || (p != nil && p.canceled)
			if p != nil && p.nativeTextStream && !drop {
				// Native UniProxy has already generated a complete, valid
				// Opus/Ogg stream. No legacy Ogg remix, artificial segment
				// boundaries, retained packets, or guessed timing.
				n.mu.Unlock()
				return n.down(ctx, k, data)
			}
			if p != nil && p.streaming && !drop {
				out, err := p.audio.feed(data[4:])
				if err == nil && len(out) > 0 {
					err = n.down(ctx, k, append(binary.BigEndian.AppendUint32(nil, p.audioStream), out...))
				}
				if err != nil {
					p.cancel()
				}
				n.mu.Unlock()
				return err
			}
			n.mu.Unlock()
			if drop {
				return nil
			}
		}
		return n.down(ctx, k, data)
	}
	if sc := f.Server.GetStreamControl(); sc != nil {
		n.mu.Lock()
		if n.blocked[uint32(sc.GetStreamId())] != nil {
			delete(n.blocked, uint32(sc.GetStreamId()))
			n.mu.Unlock()
			return nil
		}
		p := n.tts[sc.GetMessageId()]
		if p == nil {
			p = n.streams[uint32(sc.GetStreamId())]
		}
		if sc.GetClose() != nil {
			streamID := uint32(sc.GetStreamId())
			delete(n.streams, streamID)
			if capture := n.ttsCaptures[streamID]; capture != nil {
				bytes := n.ttsCaptureBytes[streamID]
				request := ""
				if p != nil {
					request = requestTag(p.id)
				}
				appendStockAudioTiming("tts_close", request, streamID, bytes)
				_ = capture.Close()
				delete(n.ttsCaptures, streamID)
				delete(n.ttsCaptureBytes, streamID)
				slog.Info("tts_output_capture_closed", "stream", streamID, "bytes", bytes)
			}
		}
		original := n.requests[sc.GetMessageId()]
		if p != nil {
			if p.canceled {
				n.mu.Unlock()
				return nil
			}
			if p.nativeTextStream && sc.GetClose() != nil {
				// The native TTS server owns Ogg EOS and packet boundaries.
				// Forward exactly one close without touching oggSpeech.
				sc.MessageId = p.id
				n.mu.Unlock()
				out, err := f.Marshal()
				if err != nil {
					return err
				}
				return n.down(ctx, k, out)
			}
			if p.streaming && sc.GetClose() != nil {
				if err := p.audio.endSource(); err != nil {
					p.cancel()
					n.mu.Unlock()
					return err
				}
				recordSynthesizedSpeechLocked(p)
				if p.audioIsProgress && sc.GetClose().GetReason() == 0 {
					if tail := p.audio.flushPending(0, 0); len(tail) > 0 {
						if err := n.down(ctx, k, append(binary.BigEndian.AppendUint32(nil, p.audioStream), tail...)); err != nil {
							n.mu.Unlock()
							return err
						}
					}
					if !p.audioWaiting {
						p.audioWaiting = true
						n.waitAudio(p)
						close(p.progressAudioDone)
					}
					n.mu.Unlock()
					return nil
				}
				end := p.audio.finish()
				if err := n.down(ctx, k, append(binary.BigEndian.AppendUint32(nil, p.audioStream), end...)); err != nil {
					n.mu.Unlock()
					return err
				}
				sc.StreamId = int64(p.audioStream)
			}
			sc.MessageId = p.id
			n.mu.Unlock()
			out, err := f.Marshal()
			if err != nil {
				return err
			}
			return n.down(ctx, k, out)
		}
		drop := original != nil && original.local && (original.header.StreamId == nil || sc.GetStreamId() != original.header.GetStreamId())
		n.mu.Unlock()
		if drop {
			return nil
		}
		return n.down(ctx, k, data)
	}
	e := f.Server.GetEvent()
	if e == nil {
		return n.down(ctx, k, data)
	}

	n.mu.Lock()
	if f.Kind() == "TtsSpeak" {
		delete(n.streams, uint32(e.GetHeader().GetStreamId()))
		delete(n.blocked, uint32(e.GetHeader().GetStreamId()))
	}
	p, isTTS := n.findLocked(e.GetHeader())
	if r := e.GetAliceResponse(); r != nil {
		var ds []string
		for _, d := range r.GetResponse().GetDirectives() {
			ds = append(ds, d.GetType()+":"+d.GetName())
		}

		slog.Info("native_cloud_shape", "matched", p != nil, "directives", ds, "cards", len(r.GetResponse().GetCards()), "voice_text_bytes", len(r.GetVoiceResponse().GetOutputSpeech().GetText()))
	}
	if p == nil {
		n.mu.Unlock()
		return n.down(ctx, k, data)
	}
	if isTTS {
		if p.canceled {
			n.mu.Unlock()
			return nil
		}
		e.Header.RefMessageId = p.id
		if ref := e.Header.SubRequestRef; ref != nil {
			ref.MessageId = p.id
			ref.RequestId = p.request.GetHeader().GetRequestId()
		}
		if e.GetTtsSpeak() != nil {
			streamID := uint32(e.Header.GetStreamId())
			n.streams[streamID] = p
			if stockAudioCaptureEnabled() && e.GetTtsSpeak().GetFormat() == "audio/opus" && n.ttsCaptures[streamID] == nil {
				capturePath := fmt.Sprintf("/tmp/quasar-tts-output-%s-%d.ogg", requestTag(p.id), streamID)
				capture, err := os.OpenFile(capturePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
				if err != nil {
					slog.Warn("tts_output_capture_open_failed", "request", requestTag(p.id), "stream", streamID, "error", err)
				} else {
					n.ttsCaptures[streamID] = capture
					n.ttsCaptureBytes[streamID] = 0
					appendStockAudioTiming("tts_open", requestTag(p.id), streamID, 0)
					slog.Info("tts_output_capture_started", "request", requestTag(p.id), "stream", streamID, "path", capturePath)
				}
			}
			if p.streaming {
				p.ttsSpeakAt = time.Now()
				if e.GetTtsSpeak().GetFormat() != "audio/opus" {
					p.cancel()
					n.mu.Unlock()
					return errors.New("streaming progress requires Opus")
				}
				if p.nativeTextStream {
					if p.audioStarted {
						if uint32(e.Header.GetStreamId()) != p.audioStream {
							n.mu.Unlock()
							return errors.New("native incremental TTS unexpected additional audio stream")
						}
						// Repeated directive for the SAME native TTS stream
						// must never restart the station's audio player.
						n.mu.Unlock()
						return nil
					}
					p.audioStream = uint32(e.Header.GetStreamId())
					p.audioStarted = true
					slog.Info("native_tts_passthrough_started", "request", requestTag(p.id), "stream", p.audioStream)
					n.mu.Unlock()
					out, err := f.Marshal()
					if err != nil {
						return err
					}
					return n.down(ctx, k, out)
				}
				p.audioIsProgress = p.ttsID == p.progressTTS
				p.audio.next()
				p.audio.source.discardAudio = p.audioIsProgress && p.discardProgressAudio
				if p.audioStarted {
					p.audioWaiting = false
					n.mu.Unlock()
					return nil
				}
				p.audioStream = uint32(e.Header.GetStreamId())
				p.audioStarted = true
			}
			slog.Info("native_tts_ready", "request", requestTag(p.id), "format", e.GetTtsSpeak().GetFormat(), "stream", e.Header.GetStreamId())
		}
		n.mu.Unlock()
		out, err := f.Marshal()
		if err != nil {
			return err
		}
		return n.down(ctx, k, out)
	}

	if f.Kind() == "TtsSpeak" {
		p.ttsStarted = true
		if p.local || p.canceled {
			n.blocked[uint32(e.GetHeader().GetStreamId())] = p
		}
	}
	if p.local || p.canceled {
		n.mu.Unlock()
		return nil
	}
	if r := e.GetAliceResponse(); r != nil && p.cloud && !p.ttsStarted && canReplaceResponse(r) {
		text := ""
		for _, card := range r.GetResponse().GetCards() {
			text += card.GetText() + " "
		}
		if strings.TrimSpace(text) == "" {
			text = r.GetVoiceResponse().GetOutputSpeech().GetText()
		}

		if resolver, ok := n.resolver.(FallbackResolver); ok && n.isRefusal(text) {
			p.local = true
			if n.OnRoute != nil {
				n.OnRoute(p.id, "llm")
			}
			p.ttsID = newID()
			n.tts[p.ttsID] = p
			query := p.text
			n.mu.Unlock()
			n.wg.Go(func() {
				result, err := resolver.Fallback(p.ctx, query)
				if p.ctx.Err() != nil {
					return
				}
				if errors.Is(err, context.Canceled) {
					n.discardStale(p)
					return
				}
				if err != nil {
					n.discardSound(p)
					result.Answer = "Сейчас не получилось получить ответ локальной модели."
				}
				_ = n.sendCancel(p.ctx, p.id)
				if err := n.reply(p, result.Answer); err != nil {
					slog.Warn("native_reply_failed", "request", requestTag(p.id))
				}
				slog.Info("native_fallback", "request", requestTag(p.id), "route", "llm")
			})
			return nil
		}
	}
	var asrUpdate *ASRUpdate
	switch f.Kind() {
	case "AliceResponse":
		if !p.cloud {
			p.canceled = true
			p.cancel()
			slog.Warn("native_approval_not_honored", "request", requestTag(p.id))
		}
	case "ServerRequestReady":
		slog.Info("native_server_ready", "request", requestTag(p.id))
	case "AsrResult":
		asr := e.GetAsrResult()
		final := asr.GetEndOfUtt()
		if final {
			p.final = true
			slog.Info("native_asr_final", "request", requestTag(p.id), "hypotheses", len(asr.GetRecognition()), "code", asr.GetResponseCode().String())
		}
		if len(asr.GetRecognition()) > 0 {
			r := asr.GetRecognition()[0]
			text := r.GetNormalized()
			if text == "" {
				var words []string
				for _, w := range r.GetWords() {
					words = append(words, w.GetValue())
				}
				text = strings.Join(words, " ")
			}
			if text != "" {
				asrUpdate = &ASRUpdate{Request: requestTag(p.id), Text: text, Final: final}
				if final {
					p.text = text
				}
			}
		}
	}
	asrCallback := n.OnASR
	n.mu.Unlock()
	if asrUpdate != nil && asrCallback != nil {
		asrCallback(*asrUpdate)
	}
	n.maybeResolve(p)
	if err := n.down(ctx, k, data); err != nil {
		return err
	}
	n.captureCloud(p, e)
	return nil
}
func (n *Native) maybeResolve(p *nativeRequest) {
	n.mu.Lock()
	if p.started || p.canceled || (strings.TrimSpace(p.text) == "" && !p.final) {
		n.mu.Unlock()
		return
	}
	p.started = true
	text := p.text
	n.mu.Unlock()
	n.wg.Go(func() {
		if n.DecisionDelay > 0 {
			select {
			case <-time.After(n.DecisionDelay):
			case <-p.ctx.Done():
				return
			}
		}
		result := Resolution{Route: "yandex"}
		var err error
		if n.resolver != nil && strings.TrimSpace(text) != "" {
			if p.forceLLM {
				nativeHandled := false
				if matcher, matchOK := n.resolver.(ExplicitRuleResolver); matchOK {
					if decision, matched := matcher.MatchExplicitRule(text); matched && decision.Route == "yandex" {
						if routed, routeOK := n.resolver.(RoutedResolver); routeOK {
							slog.Info("native_route_decision", "request", requestTag(p.id), "route", "yandex", "source", "quasar_native_rule", "rule", decision.Rule)
							result, err = routed.ResolveDecision(p.ctx, text, decision)
							nativeHandled = true
						}
					}
				}
				if !nativeHandled {
					result.Route = "llm"
					// Lite 1 schedules post-reply listening from the initial streaming
					// envelope. Custom Assistant conversational turns arm follow-up
					// listening before local_progress can start. Explicit native Yandex
					// commands never enter this branch.
					n.mu.Lock()
					p.listenOnStreamStart = true
					n.mu.Unlock()
					fallback, ok := n.resolver.(FallbackResolver)
					if !ok {
						err = errors.New("forced LLM resolver is unavailable")
					} else {
						slog.Info("native_route_decision", "request", requestTag(p.id), "route", "llm", "source", "quasar_mode")
						if streaming, streamOK := n.resolver.(StreamingFallbackResolver); streamOK {
							result, err = n.streamSpeech(p, text, streaming)
						} else {
							stop := func() {}
							if n.ProgressAfter > 0 {
								stop = n.progress(p)
							}
							result, err = fallback.Fallback(p.ctx, text)
							stop()
						}
						result.Route = "llm"
					}
				}
			} else if routed, ok := n.resolver.(RoutedResolver); ok {
				decision := routed.Decide(p.ctx, text)
				n.mu.Lock()
				p.listenOnStreamStart = decision.Source == "dialogue_retry"
				n.mu.Unlock()
				slog.Info("native_route_decision", "request", requestTag(p.id), "route", decision.Route, "source", decision.Source)
				stop := func() {}
				if decision.Route != "yandex" && n.ProgressAfter > 0 {
					stop = n.progress(p)
				}
				result, err = routed.ResolveDecision(p.ctx, text, decision)
				stop()
			} else {
				result, err = n.resolver.Resolve(p.ctx, text)
			}
		}
		if p.ctx.Err() != nil {
			return
		}
		if errors.Is(err, context.Canceled) {
			n.discardStale(p)
			return
		}
		if err != nil {
			n.discardSound(p)
			if result.Route == "home" {
				result.Answer = "Не удалось подтвердить выполнение команды в умном доме."
			} else if result.Route == "music" {
				result.Answer = "Не удалось включить музыку с этой страницы."
			} else {
				result.Route = "llm"
				result.Answer = "Сейчас не получилось получить ответ локальной модели."
			}
		}
		n.mu.Lock()
		if p.canceled {
			n.mu.Unlock()
			return
		}
		if n.OnRoute != nil {
			n.OnRoute(p.id, result.Route)
		}
		p.route = result.Route
		if result.Route == "yandex" {
			p.cloud = true
			var err error
			if p.gated {
				control := p.continuation
				if control == nil {
					control, err = n.makeContinue(p)
				}
				if err == nil {
					err = n.up(p.ctx, websocket.MessageBinary, control)
				}
			} else {
				err = n.up(p.ctx, websocket.MessageBinary, p.input)
				if err == nil && p.continuation != nil {
					err = n.up(p.ctx, websocket.MessageBinary, p.continuation)
				}
			}
			n.mu.Unlock()
			slog.Info("native_route", "request", requestTag(p.id), "route", "yandex", "sent", err == nil)
			return
		}
		p.local = true
		if !p.streaming {
			p.ttsID = newID()
			n.tts[p.ttsID] = p
		}
		n.mu.Unlock()
		slog.Info("native_route", "request", requestTag(p.id), "route", result.Route)
		if p.gated && !p.streaming {
			if err := n.sendCancel(p.ctx, p.id); err != nil {
				return
			}
		}
		if err := n.reply(p, result.Answer); err != nil {
			slog.Warn("native_reply_failed", "request", requestTag(p.id))
		}
	})
}
func (n *Native) sendCancel(ctx context.Context, id string) error {
	m := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: newID()}, Event: &client.TClientMessage_TEvent_CancelRequest{CancelRequest: &cancellation.TCancelRequest{RequestMessageId: id, Reason: &cancellation.TCancelRequest_LocalExecution{LocalExecution: &cancellation.TCancelRequest_TReasonLocalExecution{}}}}}}}
	b, err := (wire.Frame{Client: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	return n.up(ctx, websocket.MessageBinary, b)
}
func (n *Native) reply(p *nativeRequest, text string) error {
	if p.nativeTextStream {
		return n.finishNativeStreamedReply(p, text)
	}
	if p.streaming {
		return n.finishDeferred(p, text)
	}
	requestID := p.request.GetHeader().GetRequestId()
	if requestID == "" {
		requestID = p.id
	}
	answer := &response.TAliceResponseProto{Header: &response.TAliceResponseProto_THeader{RequestId: new(requestID), ResponseId: new(newID()), DialogId: new(p.request.GetHeader().GetDialogId()), SequenceNumber: new(p.request.GetHeader().GetSequenceNumber())}, VoiceResponse: &response.TAliceResponseProto_TVoiceResponse{OutputSpeech: &response.TAliceResponseProto_TVoiceResponse_TOutputSpeech{Text: new(text)}, HasVoiceResponse: true}, Response: &response.TAliceResponseProto_TResponse{Cards: []*response.TAliceResponseProto_TResponse_TCard{{Type: "simple_text", Text: new(text), CardId: new(newID())}}}}
	silentPlayback := n.hasSilentPlayback(p)
	answer.VoiceResponse.ShouldListen = n.ShouldListen && !silentPlayback
	if silentPlayback {
		answer.VoiceResponse.HasVoiceResponse = false
		answer.VoiceResponse.OutputSpeech.Text = new("")
	}
	answer.Response.Directives = n.takeSound(p)
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id}, Event: &server.TServerMessage_TEvent_AliceResponse{AliceResponse: answer}}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if !p.gated && !p.cloud {
		if err := n.ack(p); err != nil {
			return err
		}
	}
	if err := n.down(p.ctx, websocket.MessageBinary, b); err != nil {
		return err
	}
	n.captureDelivered(p, text, true, silentPlayback || len(answer.Response.Directives) > 0, true)
	if silentPlayback {
		return nil
	}
	return n.synthesize(p, text)
}
func (n *Native) synthesize(p *nativeRequest, text string) error {
	n.mu.Lock()
	if p.streaming {
		p.ttsSentAt = time.Now()
		p.ttsSpeakAt = time.Time{}
		p.ttsTextRunes = utf8.RuneCountInString(text)
		p.ttsOrdinal++
	}
	n.mu.Unlock()
	if p.nativeTextStream {
		// The first text in an IsStreaming TtsGenerate is ignored by
		// Yandex UniProxy. All spoken text MUST be sent with
		// DeferredTtsGenerate, including the first character.
		text = ""
	}
	t := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: p.ttsID}, Event: &client.TClientMessage_TEvent_TtsGenerate{TtsGenerate: &tts.TTtsGenerate{Text: new(text), Lang: new("ru-RU"), Format: new("audio/opus")}}}}}
	if p.nativeTextStream {
		v := protowire.AppendTag(nil, 12, protowire.VarintType)
		v = protowire.AppendVarint(v, 1)
		t.GetEvent().GetTtsGenerate().ProtoReflect().SetUnknown(v)
	}
	b, err := (wire.Frame{Client: t, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	return n.up(p.ctx, websocket.MessageBinary, b)
}
func requestTag(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%x", sum[:4])
}

// Quasar uses Yandex TGUID spelling (8-8-8-8), not RFC UUID spelling.
func newID() string {
	return sound.NewStreamID()
}

func (n *Native) makeContinue(p *nativeRequest) ([]byte, error) {
	h := proto.Clone(p.header).(*header.TEventHeader)
	h.MessageId = newID()
	h.RefMessageId = ""
	h.StreamId = nil
	m := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: h, Event: &client.TClientMessage_TEvent_ContinueRequest{ContinueRequest: &continuation.TContinueRequest{RequestMessageId: p.id}}}}}
	return (wire.Frame{Client: m, Prefix: []byte("AAPI")}).Marshal()
}

func (n *Native) ack(p *nativeRequest) error {
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id}, Event: &server.TServerMessage_TEvent_InputStartAck{InputStartAck: &ack.TInputStartAck{RequestStartTime: uint64(time.Now().UnixMilli())}}}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	return n.down(p.ctx, websocket.MessageBinary, b)
}

func canReplaceResponse(r *response.TAliceResponseProto) bool {
	for _, d := range r.GetResponse().GetDirectives() {
		if d.GetType() != "client_action" || d.GetName() != "tts_play_placeholder" {
			return false
		}
	}
	return true
}

func (n *Native) isRefusal(text string) bool {
	if detector, ok := n.resolver.(interface{ IsRefusal(string) bool }); ok {
		return detector.IsRefusal(text)
	}
	return IsRefusal(text)
}

func (n *Native) discardStale(p *nativeRequest) {
	n.mu.Lock()
	p.canceled = true
	p.cancel()
	n.mu.Unlock()
	_ = n.sendCancel(n.ctx, p.id)
}
