package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"quasarbridge/internal/journal"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	common "github.com/freequasar/quasar-go-genproto/proto/alice/megamind/protos/common"
	directives "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/directives"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
	response "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_response"
	asr "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/asr_result"
	recognition "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/asr_result/common"
	cancellation "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/cancel_request"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	continuation "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/continue_request"
	finishrequest "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/finish_request"
	tts "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/tts_speak"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	fileplayer "github.com/freequasar/quasar-go-genproto/proto/alice/protos/endpoint/capabilities/audio_file_player"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"quasarbridge/internal/llm"
	"quasarbridge/internal/wire"
)

type resolverFunc func(context.Context, string) (Resolution, error)

func (f resolverFunc) Resolve(ctx context.Context, s string) (Resolution, error) { return f(ctx, s) }
func aa(m proto.Message) []byte {
	data, _ := proto.Marshal(m)
	return append([]byte("AAPI"), data...)
}

func TestNativeNotifiesVoiceInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	noop := func(context.Context, websocket.MessageType, []byte) error { return nil }
	n := NewNative(ctx, noop, noop, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "yandex"}, nil
	}))
	defer n.Close()

	got := ""
	n.OnVoiceInput = func(id string) bool { got = id; return false }
	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "wake-hook-test", StreamId: new(int64(1))},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  new("audio/opus"),
		}},
	}}})

	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	if got != "wake-hook-test" {
		t.Fatalf("voice-input callback=%q", got)
	}
}

func TestNativeVoiceTakeoverSuppressesUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upCalls := 0
	up := func(context.Context, websocket.MessageType, []byte) error { upCalls++; return nil }
	noop := func(context.Context, websocket.MessageType, []byte) error { return nil }
	n := NewNative(ctx, up, noop, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "yandex"}, nil
	}))
	defer n.Close()
	n.OnVoiceInput = func(string) bool { return true }

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "takeover-test", StreamId: new(int64(1))},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  new("audio/opus"),
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	chunk := binary.BigEndian.AppendUint32(nil, 1)
	chunk = append(chunk, []byte("stock-mic-audio-must-stay-local")...)
	if err := n.Client(ctx, websocket.MessageBinary, chunk); err != nil {
		t.Fatal(err)
	}
	if upCalls != 0 {
		t.Fatalf("takeover leaked %d upstream frame(s), including stock mic audio", upCalls)
	}
	n.mu.Lock()
	external := n.external
	n.mu.Unlock()
	if external == nil {
		t.Fatal("takeover did not retain external request")
	}
	if _, ok := external.ctx.Deadline(); ok {
		t.Fatal("full-duplex takeover unexpectedly has a request deadline")
	}
	if external.expires != nil {
		t.Fatal("full-duplex takeover unexpectedly has the 90-second expiry timer")
	}
}

func TestNativeRepeatedVoiceTakeoverReplacesLocalContainer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upCalls := 0
	up := func(context.Context, websocket.MessageType, []byte) error { upCalls++; return nil }
	noop := func(context.Context, websocket.MessageType, []byte) error { return nil }
	n := NewNative(ctx, up, noop, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "yandex"}, nil
	}))
	defer n.Close()
	n.OnVoiceInput = func(string) bool { return true }

	voice := func(id string, streamID int64) []byte {
		return aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
			Header: &header.TEventHeader{MessageId: id, StreamId: new(streamID)},
			Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
				Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
				Format:  new("audio/opus"),
			}},
		}}})
	}

	if err := n.Client(ctx, websocket.MessageBinary, voice("takeover-first", 11)); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	first := n.external
	n.mu.Unlock()
	if first == nil || first.id != "takeover-first" {
		t.Fatalf("first takeover not retained: %#v", first)
	}

	if err := n.Client(ctx, websocket.MessageBinary, voice("takeover-second", 13)); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	second := n.external
	n.mu.Unlock()
	if second == nil || second.id != "takeover-second" {
		t.Fatalf("second takeover not retained: %#v", second)
	}
	if first.ctx.Err() == nil {
		t.Fatal("previous takeover container was not canceled")
	}
	if upCalls != 0 {
		t.Fatalf("repeated takeover leaked %d upstream frame(s)", upCalls)
	}
}

func TestNativeExternalEndOfUtteranceNotifiesAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	noop := func(context.Context, websocket.MessageType, []byte) error { return nil }
	n := NewNative(ctx, noop, noop, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "yandex"}, nil
	}))
	defer n.Close()
	n.OnVoiceInput = func(string) bool { return true }
	gotEOU := ""
	n.OnEndOfUtterance = func(id string) { gotEOU = id }

	requestID := "eou-test"
	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: requestID, StreamId: new(int64(9))},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  new("audio/opus"),
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}

	finish := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "eou-finish"},
		Event: &client.TClientMessage_TEvent_FinishRequest{FinishRequest: &finishrequest.TFinishRequest{
			RequestMessageId: requestID,
			AliceRequest: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{
				Type:           common.EEventType_voice_input.Enum(),
				EndOfUtterance: new(true),
			}}},
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, finish); err != nil {
		t.Fatal(err)
	}
	if gotEOU != requestID {
		t.Fatalf("end-of-utterance callback=%q want=%q", gotEOU, requestID)
	}

	gotEOU = ""
	nonFinal := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "not-eou"},
		Event: &client.TClientMessage_TEvent_FinishRequest{FinishRequest: &finishrequest.TFinishRequest{
			RequestMessageId: requestID,
			AliceRequest: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{
				Type:           common.EEventType_voice_input.Enum(),
				EndOfUtterance: new(false),
			}}},
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, nonFinal); err != nil {
		t.Fatal(err)
	}
	if gotEOU != "" {
		t.Fatalf("non-final finish unexpectedly notified end-of-utterance: %q", gotEOU)
	}
}

func TestNativeExternalUserStopNotifiesAgentWithoutUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upCalls := 0
	up := func(context.Context, websocket.MessageType, []byte) error { upCalls++; return nil }
	var downFrames []wire.Frame
	down := func(_ context.Context, _ websocket.MessageType, data []byte) error {
		downFrames = append(downFrames, wire.Decode(data, wire.ServerToClient))
		return nil
	}
	n := NewNative(ctx, up, down, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "yandex"}, nil
	}))
	defer n.Close()
	n.OnVoiceInput = func(string) bool { return true }
	gotStop := ""
	n.OnUserStop = func(id string) { gotStop = id }

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "user-stop-test", StreamId: new(int64(9))},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  new("audio/opus"),
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	stop := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "stop-control"},
		Event: &client.TClientMessage_TEvent_CancelRequest{CancelRequest: &cancellation.TCancelRequest{
			RequestMessageId: "user-stop-test",
			Reason:           &cancellation.TCancelRequest_UserStop{UserStop: &cancellation.TCancelRequest_TReasonUserStop{}},
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, stop); err != nil {
		t.Fatal(err)
	}
	if gotStop != "user-stop-test" {
		t.Fatalf("user-stop callback=%q", gotStop)
	}
	if upCalls != 0 {
		t.Fatalf("user-stop leaked %d upstream frame(s)", upCalls)
	}
	if n.ExternalTTSActive() {
		t.Fatal("user stop left Gemini takeover active")
	}
	assertFinalSpotterCancel(t, downFrames)
}

func TestNativeExternalCommandSpotterStopEndsGeminiWithoutUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upCalls := 0
	up := func(context.Context, websocket.MessageType, []byte) error { upCalls++; return nil }
	var downFrames []wire.Frame
	down := func(_ context.Context, _ websocket.MessageType, data []byte) error {
		downFrames = append(downFrames, wire.Decode(data, wire.ServerToClient))
		return nil
	}
	n := NewNative(ctx, up, down, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "yandex"}, nil
	}))
	defer n.Close()
	n.OnVoiceInput = func(string) bool { return true }
	gotStop := ""
	n.OnUserStop = func(id string) { gotStop = id }

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "command-stop-session", StreamId: new(int64(11))},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  new("audio/opus"),
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}

	stopText := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "command-spotter-stop"},
		Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_text_input.Enum(), Text: new("Стоп")}},
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, stopText); err != nil {
		t.Fatal(err)
	}
	if gotStop != "command-stop-session" {
		t.Fatalf("command-spotter stop callback=%q", gotStop)
	}
	if upCalls != 0 {
		t.Fatalf("command-spotter stop leaked %d upstream frame(s)", upCalls)
	}
	n.mu.Lock()
	_, strayTextRequest := n.requests["command-spotter-stop"]
	n.mu.Unlock()
	if strayTextRequest {
		t.Fatal("stock stop TextInput became a competing Alice request")
	}
	if n.ExternalTTSActive() {
		t.Fatal("command-spotter stop left Gemini takeover active")
	}
	assertFinalSpotterCancel(t, downFrames)
}

func assertFinalSpotterCancel(t *testing.T, frames []wire.Frame) {
	t.Helper()
	for _, f := range frames {
		var directives []*directives.TDirective
		if deferred := f.Server.GetEvent().GetDeferredAliceResponse(); deferred != nil && deferred.GetIsLast() {
			directives = deferred.GetBaseResponse().GetDirectives()
		}
		if response := f.Server.GetEvent().GetAliceResponse(); response != nil && !response.GetResponse().GetIsStreaming() {
			directives = append(directives, response.GetResponse().GetDirectives()...)
		}
		for _, directive := range directives {
			if directive.GetName() != "local_audio_file_play" {
				continue
			}
			var playback fileplayer.TAudioFilePlayerCapability_TLocalAudioFilePlayDirective
			if err := proto.Unmarshal(directive.GetPayloadRaw(), &playback); err != nil {
				t.Fatal(err)
			}
			if playback.GetSoundToPlay() == fileplayer.TAudioFilePlayerCapability_SpotterCancel {
				return
			}
		}
	}
	t.Fatal("final stock response did not contain spotter_cancel cue")
}

func TestNativeListenAfterReply(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  *bool
		want bool
	}{
		{"default", nil, true},
		{"enabled", new(true), true},
		{"disabled", new(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _, down := soundSession(t, resolverFunc(func(context.Context, string) (Resolution, error) {
				return Resolution{Route: "llm", Answer: "Готово."}, nil
			}))
			if tc.set != nil {
				n.ShouldListen = *tc.set
			}
			input := wire.Decode(soundInput("follow-up"), wire.ClientToServer).Client
			e := input.GetEvent()
			e.Event = &client.TClientMessage_TEvent_VoiceInput{VoiceInput: e.GetTextInput()}
			if err := n.Client(t.Context(), websocket.MessageBinary, aa(input)); err != nil {
				t.Fatal(err)
			}
			for {
				f := nextSoundFrame(t, down)
				if r := f.Server.GetEvent().GetAliceResponse(); r != nil {
					if got := r.GetVoiceResponse().GetShouldListen(); got != tc.want {
						t.Fatalf("should_listen=%v, want %v", got, tc.want)
					}
					break
				}
			}
		})
	}
}

func TestNativeRoutesBeforeCloudExecutionAndCorrelatesTTS(t *testing.T) {
	for _, route := range []string{"yandex", "llm", "no_continue", "home_disabled"} {
		t.Run(route, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			requestID := "request-17"
			cont := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "continue-17"}, Event: &client.TClientMessage_TEvent_ContinueRequest{ContinueRequest: &continuation.TContinueRequest{RequestMessageId: requestID}}}}})
			cont = protowire.AppendTag(cont, 999, protowire.BytesType)
			cont = protowire.AppendString(cont, "future-control-field")
			errCh := make(chan string, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				for {
					_, data, err := ws.Read(ctx)
					if err != nil {
						return
					}
					f := wire.Decode(data, wire.ClientToServer)
					switch f.Kind() {
					case "TextInput":
						if route == "llm" {
							errCh <- "local request reached cloud"
							return
						}
						if f.Client.GetEvent().GetTextInput().GetRequest().GetUniproxyOptions().GetWaitClientApproval() {
							errCh <- "cloud input changed"
							return
						}
						msg := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: "cloud-answer", RefMessageId: requestID}, Event: &server.TServerMessage_TEvent_AliceResponse{AliceResponse: &response.TAliceResponseProto{VoiceResponse: &response.TAliceResponseProto_TVoiceResponse{OutputSpeech: &response.TAliceResponseProto_TVoiceResponse_TOutputSpeech{Text: new("Ответ Яндекса")}}}}}}}
						_ = ws.Write(ctx, websocket.MessageBinary, aa(msg))
					case "ContinueRequest":
						if !bytes.Equal(data, cont) {
							errCh <- "original continuation changed"
							return
						}

					case "TtsGenerate":
						if route != "llm" || f.Client.GetEvent().GetTtsGenerate().GetText() != "Ответ своей модели" {
							errCh <- "wrong TTS text"
							return
						}
						id := f.Client.GetEvent().GetHeader().GetMessageId()
						if len(id) != 35 || id[8] != '-' || id[17] != '-' || id[26] != '-' {
							errCh <- "invalid Yandex GUID"
							return
						}
						msg := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: "tts-answer", RefMessageId: id, StreamId: new(int64(72))}, Event: &server.TServerMessage_TEvent_TtsSpeak{TtsSpeak: &tts.TTtsSpeak{Format: "audio/opus"}}}}}
						_ = ws.Write(ctx, websocket.MessageBinary, aa(msg))
						audio := binary.BigEndian.AppendUint32(nil, 72)
						audio = append(audio, []byte("fixture-audio")...)
						_ = ws.Write(ctx, websocket.MessageBinary, audio)
					}
				}
			}))
			defer upstream.Close()
			var resolver Resolver = resolverFunc(func(ctx context.Context, text string) (Resolution, error) {
				if text != "Тестовый вопрос" {
					return Resolution{}, context.Canceled
				}
				if route == "no_continue" {
					return Resolution{Route: "yandex"}, nil
				}
				return Resolution{Route: route, Answer: "Ответ своей модели"}, nil
			})
			if route == "home_disabled" {
				engine := new(Engine)
				cfg := DefaultRoutingConfig()
				cfg.DefaultRoute = "home"
				if err := engine.SetRouting(cfg); err != nil {
					t.Fatal(err)
				}
				resolver = engine
			}
			log := new(journal.Writer)
			defer log.Close()
			logConfig := journal.DefaultConfig()
			logConfig.Enabled = true
			logConfig.Path = filepath.Join(t.TempDir(), "turns.jsonl")
			if err := log.Configure(logConfig); err != nil {
				t.Fatal(err)
			}
			proxy := &Proxy{Journal: log, Upstream: "ws" + strings.TrimPrefix(upstream.URL, "http"), NewSession: func(ctx context.Context, up, down Sender) Session {
				n := NewNative(ctx, up, down, resolver)

				return n
			}}
			s := httptest.NewServer(proxy)
			defer s.Close()
			ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.CloseNow()
			input := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: requestID}, Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_text_input.Enum(), Text: new("Тестовый вопрос")}}}}}}}
			_ = ws.Write(ctx, websocket.MessageBinary, aa(input))
			if route != "no_continue" {
				_ = ws.Write(ctx, websocket.MessageBinary, cont)
			}
			gotAnswer, gotTTS, gotAudio := false, false, false
			for !gotAnswer || (route == "llm" && (!gotTTS || !gotAudio)) {
				_, data, err := ws.Read(ctx)
				if err != nil {
					select {
					case msg := <-errCh:
						t.Fatal(msg)
					default:
						t.Fatal(err)
					}
				}
				f := wire.Decode(data, wire.ServerToClient)
				if f.Kind() == "AliceResponse" {
					gotAnswer = true
					if got := f.Server.GetEvent().GetAliceResponse().GetVoiceResponse().GetShouldListen(); got != (route == "llm") {
						t.Fatalf("should_listen=%v for route %s", got, route)
					}
					text := f.Server.GetEvent().GetAliceResponse().GetVoiceResponse().GetOutputSpeech().GetText()
					want := "Ответ своей модели"
					if route != "llm" {
						want = "Ответ Яндекса"
					}
					if text != want {
						t.Fatalf("answer=%q", text)
					}
				}
				if f.Kind() == "TtsSpeak" {
					gotTTS = true
					if f.Server.GetEvent().GetHeader().GetRefMessageId() != requestID {
						t.Fatal("TTS correlated to internal request instead of speaker request")
					}
				}
				if len(data) > 4 && binary.BigEndian.Uint32(data) == 72 {
					gotAudio = true
					if string(data[4:]) != "fixture-audio" {
						t.Fatal("audio altered")
					}
				}
			}
			// The actual downstream sender must record both cloud and generated replies.
			var logged string
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				raw, _ := os.ReadFile(logConfig.Path)
				logged = string(raw)
				if strings.Contains(logged, `"event":"response"`) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			wantRoute := route
			if route == "no_continue" || route == "home_disabled" {
				wantRoute = "yandex"
			}
			if !strings.Contains(logged, `"event":"request"`) || !strings.Contains(logged, `"route":"`+wantRoute+`"`) || !strings.Contains(logged, `"event":"response"`) || strings.Contains(logged, "fixture-audio") {
				t.Fatalf("missing conversation or audio leaked: %s", logged)
			}
			select {
			case msg := <-errCh:
				t.Fatal(msg)
			default:
			}
		})
	}
}

func TestNativeApprovalHoldsExecutionUntilDecision(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	sent := make(chan []byte, 4)
	release := make(chan struct{})
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { sent <- b; return nil }, func(context.Context, websocket.MessageType, []byte) error { return nil }, resolverFunc(func(context.Context, string) (Resolution, error) { <-release; return Resolution{Route: "yandex"}, nil }))
	defer n.Close()
	n.GateText = true
	input := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "gate-id"}, Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Text: new("погода")}}}}}}})
	if err := n.Client(ctx, websocket.MessageBinary, input); err != nil {
		t.Fatal(err)
	}
	first := wire.Decode(<-sent, wire.ClientToServer)
	if !first.Client.GetEvent().GetTextInput().GetRequest().GetUniproxyOptions().GetWaitClientApproval() {
		t.Fatal("approval was not requested")
	}
	select {
	case <-sent:
		t.Fatal("executed before routing")
	default:
	}
	close(release)
	select {
	case b := <-sent:
		if wire.Decode(b, wire.ClientToServer).Kind() != "ContinueRequest" {
			t.Fatal("missing approval")
		}
	case <-ctx.Done():
		t.Fatal("no approval")
	}
}

type fallbackResolver struct{}

func (fallbackResolver) Resolve(context.Context, string) (Resolution, error) {
	return Resolution{Route: "yandex"}, nil
}
func (fallbackResolver) Fallback(context.Context, string) (Resolution, error) {
	return Resolution{Route: "llm", Answer: "Ответ после отказа"}, nil
}

func TestNativeRefusalUsesLLMAndSuppressesOldCloudAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	up := make(chan []byte, 8)
	down := make(chan []byte, 8)
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, fallbackResolver{})
	defer n.Close()
	input := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "refusal"}, Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Text: new("запрос")}}}}}}})
	if err := n.Client(ctx, websocket.MessageBinary, input); err != nil {
		t.Fatal(err)
	}
	select {
	case <-up:
	case <-ctx.Done():
		t.Fatal("no cloud input")
	}
	refusal := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "refusal"}, Event: &server.TServerMessage_TEvent_AliceResponse{AliceResponse: &response.TAliceResponseProto{Response: &response.TAliceResponseProto_TResponse{Directives: []*directives.TDirective{{Type: "client_action", Name: "tts_play_placeholder"}}}, VoiceResponse: &response.TAliceResponseProto_TVoiceResponse{OutputSpeech: &response.TAliceResponseProto_TVoiceResponse_TOutputSpeech{Text: new("Я не могу ответить на этот вопрос.")}}}}}}})
	if err := n.Server(ctx, websocket.MessageBinary, refusal); err != nil {
		t.Fatal(err)
	}
	oldtts := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "refusal", StreamId: new(int64(22))}, Event: &server.TServerMessage_TEvent_TtsSpeak{TtsSpeak: &tts.TTtsSpeak{Format: "audio/opus"}}}}})
	_ = n.Server(ctx, websocket.MessageBinary, oldtts)
	_ = n.Server(ctx, websocket.MessageBinary, append(binary.BigEndian.AppendUint32(nil, 22), []byte("old audio")...))
	for {
		select {
		case b := <-down:
			f := wire.Decode(b, wire.ServerToClient)
			if f.Kind() == "opaque" || f.Kind() == "TtsSpeak" {
				t.Fatal("cloud refusal audio leaked")
			}
			if f.Kind() == "AliceResponse" {
				if f.Server.GetEvent().GetAliceResponse().GetVoiceResponse().GetOutputSpeech().GetText() != "Ответ после отказа" {
					t.Fatal("old refusal was delivered")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("no fallback answer")
		}
	}
}

func TestNativeCancellationDropsLateApprovalAndLocalReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	up := make(chan []byte, 8)
	down := make(chan []byte, 8)
	started := make(chan struct{})
	finished := make(chan struct{})
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, resolverFunc(func(ctx context.Context, _ string) (Resolution, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return Resolution{Route: "llm", Answer: "late"}, ctx.Err()
	}))
	defer n.Close()
	input := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "cancel-me"}, Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Text: new("вопрос")}}}}}}})
	_ = n.Client(ctx, websocket.MessageBinary, input)
	<-started
	c := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "cancel-control"}, Event: &client.TClientMessage_TEvent_CancelRequest{CancelRequest: &cancellation.TCancelRequest{RequestMessageId: "cancel-me"}}}}})
	_ = n.Client(ctx, websocket.MessageBinary, c)
	cont := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "late"}, Event: &client.TClientMessage_TEvent_ContinueRequest{ContinueRequest: &continuation.TContinueRequest{RequestMessageId: "cancel-me"}}}}})
	_ = n.Client(ctx, websocket.MessageBinary, cont)
	<-finished
	if len(up) != 1 || wire.Decode(<-up, wire.ClientToServer).Kind() != "CancelRequest" {
		t.Fatal("late continuation executed canceled request")
	}
	select {
	case b := <-down:
		if wire.Decode(b, wire.ServerToClient).Kind() != "InputStartAck" {
			t.Fatal("reply after cancellation")
		}
	default:
	}
}

func TestNativeStreamReuseDoesNotKeepCanceledLocalOwner(t *testing.T) {
	down := make(chan []byte, 4)
	n := NewNative(t.Context(), func(context.Context, websocket.MessageType, []byte) error { return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, nil)
	defer n.Close()
	n.streams[6] = &nativeRequest{canceled: true}
	msg := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "new-cloud-request", StreamId: new(int64(6))}, Event: &server.TServerMessage_TEvent_TtsSpeak{TtsSpeak: &tts.TTtsSpeak{Format: "audio/opus"}}}}})
	_ = n.Server(t.Context(), websocket.MessageBinary, msg)
	audio := append(binary.BigEndian.AppendUint32(nil, 6), []byte("new audio")...)
	_ = n.Server(t.Context(), websocket.MessageBinary, audio)
	if len(down) != 2 {
		t.Fatal("valid reused stream suppressed by old request")
	}
}

func TestVoiceRoutesOnlyAfterFinalASR(t *testing.T) {
	for _, route := range []string{"yandex", "home", "empty", "home_disabled"} {
		t.Run(route, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			up := make(chan []byte, 16)
			var resolver Resolver = resolverFunc(func(_ context.Context, text string) (Resolution, error) {
				if text != "включи свет" {
					t.Errorf("text=%q", text)
				}
				return Resolution{Route: route, Answer: "Готово"}, nil
			})
			if route == "home_disabled" {
				resolver = new(Engine)
			}
			n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(context.Context, websocket.MessageType, []byte) error { return nil }, resolver)
			defer n.Close()
			voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "voice", StreamId: new(int64(1))}, Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}}, Format: new("audio/opus")}}}}})
			_ = n.Client(ctx, websocket.MessageBinary, voice)
			first := wire.Decode(<-up, wire.ClientToServer)
			if !first.Client.GetEvent().GetVoiceInput().GetRequest().GetUniproxyOptions().GetWaitClientApproval() {
				t.Fatal("ungated voice")
			}
			audio := append(binary.BigEndian.AppendUint32(nil, 1), []byte("mic bytes")...)
			_ = n.Client(ctx, websocket.MessageBinary, audio)
			if !bytes.Equal(<-up, audio) {
				t.Fatal("microphone audio changed")
			}
			normalized := "включи свет"
			if route == "empty" {
				normalized = ""
			}
			result := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "voice"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new(normalized)}}}}}}})
			_ = n.Server(ctx, websocket.MessageBinary, result)
			select {
			case b := <-up:
				kind := wire.Decode(b, wire.ClientToServer).Kind()
				want := "ContinueRequest"
				if route == "home" {
					want = "CancelRequest"
				}
				if kind != want {
					t.Fatalf("kind=%s want=%s", kind, want)
				}
			case <-ctx.Done():
				t.Fatal("no decision")
			}
		})
	}
}

func TestStockVoiceModeIsTransparentPassThrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	up := make(chan []byte, 8)
	down := make(chan []byte, 8)
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, nil)
	defer n.Close()
	n.OnVoiceMode = func(string) VoiceMode { return VoiceModeStock }

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "stock-voice", StreamId: new(int64(21))}, Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}}, Format: new("audio/opus")}}}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	forwarded := wire.Decode(<-up, wire.ClientToServer)
	if forwarded.Client.GetEvent().GetVoiceInput().GetRequest().GetUniproxyOptions().GetWaitClientApproval() {
		t.Fatal("strict stock mode must not add WaitClientApproval")
	}

	final := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "stock-voice"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new("поставь таймер")}}}}}}})
	if err := n.Server(ctx, websocket.MessageBinary, final); err != nil {
		t.Fatal(err)
	}
	if kind := wire.Decode(<-down, wire.ServerToClient).Kind(); kind != "AsrResult" {
		t.Fatalf("stock server frame kind=%s, want AsrResult", kind)
	}
	select {
	case b := <-up:
		t.Fatalf("stock mode emitted local upstream frame %s", wire.Decode(b, wire.ClientToServer).Kind())
	case <-time.After(50 * time.Millisecond):
	}
}

func TestQuasarVoiceModeKeepsExplicitYandexRuleNative(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	up := make(chan []byte, 16)
	llmCalled := make(chan struct{}, 1)
	engine := &Engine{LLM: completionFunc(func(context.Context, []llm.Message, bool) (string, error) {
		llmCalled <- struct{}{}
		return "unexpected", nil
	})}
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(context.Context, websocket.MessageType, []byte) error { return nil }, engine)
	defer n.Close()
	n.OnVoiceMode = func(string) VoiceMode { return VoiceModeQuasar }

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "quasar-native", StreamId: new(int64(22))}, Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}}, Format: new("audio/opus")}}}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	<-up // stock VoiceInput with client approval

	final := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "quasar-native"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new("поставь таймер на пять минут")}}}}}}})
	if err := n.Server(ctx, websocket.MessageBinary, final); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-up:
		if kind := wire.Decode(b, wire.ClientToServer).Kind(); kind != "ContinueRequest" {
			t.Fatalf("native command upstream kind=%s, want ContinueRequest", kind)
		}
	case <-ctx.Done():
		t.Fatal("custom mode did not leave explicit Yandex command native")
	}
	select {
	case <-llmCalled:
		t.Fatal("explicit Yandex rule unexpectedly called LLM")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestQuasarVoiceModeStreamsPartialsAndForcesLLM(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	up := make(chan []byte, 16)
	updates := make(chan ASRUpdate, 4)
	gotPrompt := make(chan string, 1)
	engine := &Engine{LLM: completionFunc(func(_ context.Context, messages []llm.Message, _ bool) (string, error) {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				gotPrompt <- messages[i].Content
				break
			}
		}
		return "Ответ Quasar Assistant", nil
	})}
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(context.Context, websocket.MessageType, []byte) error { return nil }, engine)
	defer n.Close()
	n.OnVoiceMode = func(string) VoiceMode { return VoiceModeQuasar }
	n.OnASR = func(update ASRUpdate) { updates <- update }

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "quasar-voice", StreamId: new(int64(9))}, Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}}, Format: new("audio/opus")}}}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	first := wire.Decode(<-up, wire.ClientToServer)
	if !first.Client.GetEvent().GetVoiceInput().GetRequest().GetUniproxyOptions().GetWaitClientApproval() {
		t.Fatal("quasar voice must keep stock SpeechKit behind client approval")
	}

	audio := append(binary.BigEndian.AppendUint32(nil, 9), []byte("stock opus")...)
	if err := n.Client(ctx, websocket.MessageBinary, audio); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(<-up, audio) {
		t.Fatal("quasar mode changed or suppressed stock microphone audio")
	}

	partial := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "quasar-voice"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(false), Recognition: []*recognition.TAsrRecognition{{Normalized: new("расскажи мне")}}}}}}})
	if err := n.Server(ctx, websocket.MessageBinary, partial); err != nil {
		t.Fatal(err)
	}
	if got := <-updates; got.Text != "расскажи мне" || got.Final {
		t.Fatalf("partial update=%+v", got)
	}

	final := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "quasar-voice"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new("объясни мне теорию относительности")}}}}}}})
	if err := n.Server(ctx, websocket.MessageBinary, final); err != nil {
		t.Fatal(err)
	}
	if got := <-updates; got.Text != "объясни мне теорию относительности" || !got.Final {
		t.Fatalf("final update=%+v", got)
	}

	select {
	case prompt := <-gotPrompt:
		if prompt != "объясни мне теорию относительности" {
			t.Fatalf("LLM prompt=%q", prompt)
		}
	case <-ctx.Done():
		t.Fatal("Quasar Assistant did not call LLM")
	}
	select {
	case b := <-up:
		if kind := wire.Decode(b, wire.ClientToServer).Kind(); kind != "CancelRequest" {
			t.Fatalf("first post-ASR upstream kind=%s, want CancelRequest", kind)
		}
	case <-ctx.Done():
		t.Fatal("Quasar Assistant did not cancel stock execution")
	}
}

func TestQuasarStreamingStartsFirstSentenceBeforeLLMCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	up := make(chan []byte, 16)
	down := make(chan []byte, 16)
	release := make(chan struct{})
	finished := make(chan struct{})
	engine := &Engine{LLM: streamingCompletionFunc{
		complete: func(context.Context, []llm.Message, bool) (string, error) {
			return "Осенью листья меняют цвет из-за разрушения хлорофилла. Остальное позже.", nil
		},
		stream: func(ctx context.Context, _ []llm.Message, _ bool, onDelta func(string) error) (string, error) {
			if err := onDelta("Осенью листья меняют цвет из-за разрушения хлорофилла."); err != nil {
				return "", err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if err := onDelta(" Остальное позже."); err != nil {
				return "", err
			}
			close(finished)
			return "Осенью листья меняют цвет из-за разрушения хлорофилла. Остальное позже.", nil
		},
	}}
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, engine)
	defer func() {
		cancel()
		n.Close()
	}()
	n.OnVoiceMode = func(string) VoiceMode { return VoiceModeQuasar }

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "quasar-stream", StreamId: new(int64(12))}, Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}}, Format: new("audio/opus")}}}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	<-up // stock VoiceInput with WaitClientApproval
	final := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "quasar-stream"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new("проверка стриминга")}}}}}}})
	if err := n.Server(ctx, websocket.MessageBinary, final); err != nil {
		t.Fatal(err)
	}

	if kind := wire.Decode(<-up, wire.ClientToServer).Kind(); kind != "CancelRequest" {
		t.Fatalf("first streamed upstream kind=%s, want CancelRequest", kind)
	}
	firstTTS := wire.Decode(<-up, wire.ClientToServer)
	if firstTTS.Kind() != "TtsGenerate" || firstTTS.Client.GetEvent().GetTtsGenerate().GetText() != "Осенью листья меняют цвет из-за разрушения хлорофилла." {
		t.Fatalf("first streamed TTS=%s text=%q", firstTTS.Kind(), firstTTS.Client.GetEvent().GetTtsGenerate().GetText())
	}
	select {
	case <-finished:
		t.Fatal("LLM completed before first TTS was observable")
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("LLM did not resume after first TTS")
	}
}

func TestQuasarVoiceModeProgressArmsFollowUpListening(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	up := make(chan []byte, 16)
	down := make(chan []byte, 16)
	engine := &Engine{LLM: completionFunc(func(context.Context, []llm.Message, bool) (string, error) {
		time.Sleep(30 * time.Millisecond)
		return "Готово.", nil
	})}
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, engine)
	defer n.Close()
	n.OnVoiceMode = func(string) VoiceMode { return VoiceModeQuasar }
	n.ProgressAfter = 5 * time.Millisecond
	n.ProgressInterval = time.Second

	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: "quasar-progress", StreamId: new(int64(11))}, Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}}, Format: new("audio/opus")}}}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	<-up // stock VoiceInput with WaitClientApproval

	final := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: "quasar-progress"}, Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new("продолжи разговор")}}}}}}})
	if err := n.Server(ctx, websocket.MessageBinary, final); err != nil {
		t.Fatal(err)
	}

	for {
		select {
		case b := <-down:
			f := wire.Decode(b, wire.ServerToClient)
			if r := f.Server.GetEvent().GetAliceResponse(); r != nil {
				if !r.GetVoiceResponse().GetShouldListen() {
					t.Fatal("Quasar progress envelope must arm follow-up listening")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("no initial streaming AliceResponse")
		}
	}
}

func TestRefusalReplacementKeepsExecutorDirectives(t *testing.T) {
	for _, tc := range []struct {
		name      string
		directive *directives.TDirective
		want      bool
	}{
		{"speech", &directives.TDirective{Type: "client_action", Name: "tts_play_placeholder"}, true},
		{"home", &directives.TDirective{Type: "client_action", Name: "devices_action"}, false},
		{"unknown", &directives.TDirective{Type: "future", Name: "unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &response.TAliceResponseProto{Response: &response.TAliceResponseProto_TResponse{Directives: []*directives.TDirective{tc.directive}}}
			if canReplaceResponse(r) != tc.want {
				t.Fatal("incorrect directive substitution policy")
			}
		})
	}
}

func TestExistingNativeSessionUsesReloadedRefusalPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	up := make(chan []byte, 16)
	down := make(chan []byte, 16)
	engine := &Engine{LLM: completionFunc(func(context.Context, []llm.Message, bool) (string, error) {
		return "Локальный ответ", nil
	})}
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, engine)
	defer n.Close()
	for i, enabled := range []bool{false, true} {
		cfg := DefaultRoutingConfig()
		cfg.Rules = []RoutingRule{}
		cfg.DefaultRoute = "yandex"
		cfg.Refusal.Enabled = enabled
		if err := engine.SetRouting(cfg); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("policy-%d", i)
		input := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: id}, Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Text: new("вопрос")}}}}}}})
		if err := n.Client(ctx, websocket.MessageBinary, input); err != nil {
			t.Fatal(err)
		}
		select {
		case <-up:
		case <-ctx.Done():
			t.Fatal("no cloud request")
		}
		original := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{RefMessageId: id}, Event: &server.TServerMessage_TEvent_AliceResponse{AliceResponse: &response.TAliceResponseProto{VoiceResponse: &response.TAliceResponseProto_TVoiceResponse{OutputSpeech: &response.TAliceResponseProto_TVoiceResponse_TOutputSpeech{Text: new("Я не знаю")}}}}}}})
		if err := n.Server(ctx, websocket.MessageBinary, original); err != nil {
			t.Fatal(err)
		}
		select {
		case b := <-down:
			if !enabled {
				if !bytes.Equal(b, original) {
					t.Fatal("disabled fallback replaced answer")
				}
			} else if wire.Decode(b, wire.ServerToClient).Server.GetEvent().GetAliceResponse().GetVoiceResponse().GetOutputSpeech().GetText() != "Локальный ответ" {
				t.Fatal("existing session did not pick up policy")
			}
		case <-ctx.Done():
			t.Fatal("no response")
		}
	}
}

func TestExistingNativeSessionUsesReloadedRoute(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	up := make(chan []byte, 16)
	down := make(chan []byte, 16)
	engine := &Engine{LLM: completionFunc(func(context.Context, []llm.Message, bool) (string, error) { return "Новый маршрут", nil })}
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error { up <- b; return nil }, func(_ context.Context, _ websocket.MessageType, b []byte) error { down <- b; return nil }, engine)
	defer n.Close()
	cfg := DefaultRoutingConfig()
	cfg.Rules = []RoutingRule{}
	cfg.DefaultRoute = "yandex"
	if err := engine.SetRouting(cfg); err != nil {
		t.Fatal(err)
	}
	input := func(id string) []byte {
		return aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: id}, Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Text: new("один вопрос")}}}}}}})
	}
	first := input("before")
	if err := n.Client(ctx, websocket.MessageBinary, first); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-up:
		if !bytes.Equal(b, first) {
			t.Fatal("original cloud request changed")
		}
	case <-ctx.Done():
		t.Fatal("no cloud request")
	}
	cfg.DefaultRoute = "llm"
	if err := engine.SetRouting(cfg); err != nil {
		t.Fatal(err)
	}
	if err := n.Client(ctx, websocket.MessageBinary, input("after")); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case b := <-down:
			f := wire.Decode(b, wire.ServerToClient)
			if f.Kind() == "AliceResponse" {
				if f.Server.GetEvent().GetAliceResponse().GetVoiceResponse().GetOutputSpeech().GetText() != "Новый маршрут" {
					t.Fatal("old route reused")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("existing session did not change route")
		}
	}
}
