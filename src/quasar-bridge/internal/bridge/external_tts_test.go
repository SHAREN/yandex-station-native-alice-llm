package bridge

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/coder/websocket"
	common "github.com/freequasar/quasar-go-genproto/proto/alice/megamind/protos/common"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"

	"quasarbridge/internal/wire"
)

func TestNativeVoiceTakeoverCompletesBootstrapAndKeepsGeminiActive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upCalls := 0
	up := func(context.Context, websocket.MessageType, []byte) error {
		upCalls++
		return nil
	}
	down := make(chan wire.Frame, 32)
	n := NewNative(ctx, up, func(_ context.Context, _ websocket.MessageType, data []byte) error {
		down <- wire.Decode(data, wire.ServerToClient)
		return nil
	}, resolverFunc(func(context.Context, string) (Resolution, error) {
		return Resolution{Route: "yandex"}, nil
	}))
	defer n.Close()
	n.OnVoiceInput = func(string) bool { return true }

	const inputStream = int64(55)
	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "full-duplex-takeover", StreamId: new(inputStream)},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  new("audio/opus"),
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}

	next := func() wire.Frame {
		t.Helper()
		select {
		case f := <-down:
			return f
		case <-time.After(time.Second):
			t.Fatal("missing takeover frame")
			return wire.Frame{}
		}
	}
	if f := next(); f.Kind() != "InputStartAck" {
		t.Fatalf("first frame=%s, want InputStartAck", f.Kind())
	}
	select {
	case f := <-down:
		t.Fatalf("unexpected stock response before vqe_0 readiness: %s", f.Kind())
	case <-time.After(30 * time.Millisecond):
	}

	if err := n.ExternalInputComplete(); err != nil {
		t.Fatal(err)
	}
	completed := next()
	if completed.Kind() != "AliceResponse" {
		t.Fatalf("bootstrap completion kind=%s, want AliceResponse", completed.Kind())
	}
	r := completed.Server.GetEvent().GetAliceResponse()
	if r.GetResponse().GetIsStreaming() {
		t.Fatal("bootstrap completion must be final/non-streaming")
	}
	if r.GetVoiceResponse().GetHasVoiceResponse() || r.GetVoiceResponse().GetShouldListen() {
		t.Fatalf("bootstrap completion must stay silent: voice=%t listen=%t", r.GetVoiceResponse().GetHasVoiceResponse(), r.GetVoiceResponse().GetShouldListen())
	}
	if !n.ExternalTTSActive() {
		t.Fatal("closing stock VoiceInput must not end Gemini takeover")
	}
	select {
	case f := <-down:
		t.Fatalf("unexpected keepalive after stock VoiceInput completion: %s", f.Kind())
	case <-time.After(40 * time.Millisecond):
	}

	chunk := binary.BigEndian.AppendUint32(nil, uint32(inputStream))
	chunk = append(chunk, []byte("stock microphone payload")...)
	if err := n.Client(ctx, websocket.MessageBinary, chunk); err != nil {
		t.Fatal(err)
	}
	if upCalls != 0 {
		t.Fatalf("takeover leaked %d frame(s) to Yandex upstream", upCalls)
	}

	if err := n.ExternalSessionEnd(); err != nil {
		t.Fatal(err)
	}
	if n.ExternalTTSActive() {
		t.Fatal("session end left takeover active")
	}
}
