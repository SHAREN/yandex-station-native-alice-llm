package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/coder/websocket"
	"quasarbridge/internal/wire"
)

// Native UniProxy sends one completed Opus/Ogg stream; its bytes must remain
// unchanged. The old concatenator was only for independent TtsGenerate calls.
func TestNativeStreamingForwardsOriginalAudioUnchanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	down := make(chan []byte, 10)
	n := NewNative(ctx, func(context.Context, websocket.MessageType, []byte) error { return nil },
		func(_ context.Context, _ websocket.MessageType, b []byte) error {
			down <- append([]byte(nil), b...)
			return nil
		}, nil)
	defer n.Close()
	const streamID uint32 = 501
	p := &nativeRequest{
		id:"native-raw-audio",
		ctx:ctx,
		cancel:cancel,
		streaming:true,
		nativeTextStream:true,
		ttsID:"native-raw-tts-id",
	}
	n.tts[p.ttsID] = p
	feedSpeech(t, n, p.ttsID, streamID, false)
	expected := append(binary.BigEndian.AppendUint32(nil, streamID), speechFixture(streamID)...)
	speaks, closes, audio := 0,0,0
	for len(down)>0 {
		b := <-down
		f := wire.Decode(b, wire.ServerToClient)
		switch {
		case f.Kind()=="TtsSpeak":
			speaks++
		case f.Server!=nil && f.Server.GetStreamControl()!=nil && f.Server.GetStreamControl().GetClose()!=nil:
			closes++
			if f.Server.GetStreamControl().GetStreamId()!=int64(streamID) {
				t.Fatalf("stream id changed on EOS")
			}
		case bytes.Equal(b, expected):
			audio++
		case len(b)>4 && binary.BigEndian.Uint32(b[:4])==streamID:
			t.Fatalf("Opus/Ogg source was transformed despite native streaming; bytes=%d, original=%d",len(b),len(expected))
		}
	}
	if speaks!=1 || closes!=1 || audio!=1 {
		t.Fatalf("expected one TtsSpeak, one byte-identical Opus payload and one Close, got: speaks=%d audio=%d closes=%d",speaks,audio,closes)
	}
}
