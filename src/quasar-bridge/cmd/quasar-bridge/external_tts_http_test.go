package main

import (
	"context"
	"testing"

	"github.com/coder/websocket"
	common "github.com/freequasar/quasar-go-genproto/proto/alice/megamind/protos/common"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	"google.golang.org/protobuf/proto"

	"quasarbridge/internal/bridge"
)

type brokerResolver func(context.Context, string) (bridge.Resolution, error)

func (f brokerResolver) Resolve(ctx context.Context, s string) (bridge.Resolution, error) {
	return f(ctx, s)
}

func brokerFrame(m proto.Message) []byte {
	b, _ := proto.Marshal(m)
	return append([]byte("AAPI"), b...)
}

func TestExternalTTSBrokerFindsActiveTakeoverAcrossParallelNativeSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	noop := func(context.Context, websocket.MessageType, []byte) error { return nil }
	resolver := brokerResolver(func(context.Context, string) (bridge.Resolution, error) {
		return bridge.Resolution{Route: "yandex"}, nil
	})

	active := bridge.NewNative(ctx, noop, noop, resolver)
	defer active.Close()
	active.OnVoiceInput = func(string) bool { return true }
	voice := brokerFrame(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "active-takeover", StreamId: proto.Int64(7)},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  proto.String("audio/opus"),
		}},
	}}})
	if err := active.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	if err := active.ExternalInputComplete(); err != nil {
		t.Fatal(err)
	}
	if !active.ExternalTTSActive() {
		t.Fatal("bootstrap completion ended Gemini takeover")
	}

	aux := bridge.NewNative(ctx, noop, noop, resolver)
	defer aux.Close()

	var b externalTTSBroker
	b.attach(active)
	b.attach(aux) // later non-takeover session must not hide the active one
	if got := b.current(); got != active {
		t.Fatalf("broker selected %p, want active takeover %p", got, active)
	}
}

func TestExternalTTSBrokerSuppressesParallelConversationText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	noop := func(context.Context, websocket.MessageType, []byte) error { return nil }
	resolverCalls := 0
	resolver := brokerResolver(func(context.Context, string) (bridge.Resolution, error) {
		resolverCalls++
		return bridge.Resolution{Route: "llm", Answer: "legacy answer must not run"}, nil
	})

	active := bridge.NewNative(ctx, noop, noop, resolver)
	defer active.Close()
	active.OnVoiceInput = func(string) bool { return true }
	voice := brokerFrame(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "active-takeover", StreamId: proto.Int64(7)},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  proto.String("audio/opus"),
		}},
	}}})
	if err := active.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	if err := active.ExternalInputComplete(); err != nil {
		t.Fatal(err)
	}
	if !active.ExternalTTSActive() {
		t.Fatal("bootstrap completion ended Gemini takeover")
	}

	upCalls := 0
	aux := bridge.NewNative(ctx, func(context.Context, websocket.MessageType, []byte) error {
		upCalls++
		return nil
	}, noop, resolver)
	defer aux.Close()

	var b externalTTSBroker
	b.attach(active)
	b.attach(aux)
	aux.OnTextInput = b.handleTextInput

	text := brokerFrame(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "parallel-text"},
		Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Text: proto.String("Расскажи о себе")}},
		}},
	}}})
	if err := aux.Client(ctx, websocket.MessageBinary, text); err != nil {
		t.Fatal(err)
	}
	if upCalls != 0 {
		t.Fatalf("suppressed TextInput leaked %d frame(s) upstream", upCalls)
	}
	if resolverCalls != 0 {
		t.Fatalf("legacy resolver ran %d time(s) during Gemini takeover", resolverCalls)
	}
	if !active.ExternalTTSActive() {
		t.Fatal("ordinary suppressed text unexpectedly ended Gemini takeover")
	}
}

func TestExternalTTSBrokerRoutesParallelStopToActiveTakeover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	noop := func(context.Context, websocket.MessageType, []byte) error { return nil }
	resolver := brokerResolver(func(context.Context, string) (bridge.Resolution, error) {
		return bridge.Resolution{Route: "yandex"}, nil
	})

	active := bridge.NewNative(ctx, noop, noop, resolver)
	defer active.Close()
	active.OnVoiceInput = func(string) bool { return true }
	stopNotified := 0
	active.OnUserStop = func(string) { stopNotified++ }
	voice := brokerFrame(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "active-takeover", StreamId: proto.Int64(7)},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  proto.String("audio/opus"),
		}},
	}}})
	if err := active.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}

	aux := bridge.NewNative(ctx, noop, noop, resolver)
	defer aux.Close()
	var b externalTTSBroker
	b.attach(active)
	b.attach(aux)
	aux.OnTextInput = b.handleTextInput

	stop := brokerFrame(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "parallel-stop"},
		Event: &client.TClientMessage_TEvent_TextInput{TextInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Text: proto.String("Стоп")}},
		}},
	}}})
	if err := aux.Client(ctx, websocket.MessageBinary, stop); err != nil {
		t.Fatal(err)
	}
	if stopNotified != 1 {
		t.Fatalf("stop callback count=%d, want 1", stopNotified)
	}
	if active.ExternalTTSActive() {
		t.Fatal("parallel stop did not close active Gemini takeover")
	}
}
