package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	common "github.com/freequasar/quasar-go-genproto/proto/alice/megamind/protos/common"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
	asr "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/asr_result"
	recognition "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/asr_result/common"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"quasarbridge/internal/llm"
	"quasarbridge/internal/wire"
)

func TestStreamedSentenceBufferPreservesSpeechAndRemainder(t *testing.T) {
	tests := []struct {
		deltas   []string
		segments []string
		tail     string
	}{
		{[]string{"Григорий жил в городе. Он любил ", "часы. Однажды он нашёл ключ."},
			[]string{"Григорий жил в городе.", "Он любил часы.", "Однажды он нашёл ключ."}, ""},
		{[]string{"Герой спросил: «Куда идём?» Потом", " он улыбнулся. Всё."},
			[]string{"Герой спросил: «Куда идём?»", "Потом он улыбнулся.", "Всё."}, ""},
		{[]string{"Число равно 3.", "14, а не 3. "},
			[]string{"Число равно 3.14, а не 3."}, ""},
		{[]string{"Один. Два. Три. Четыре."},
			[]string{"Один.", "Два.", "Три.", "Четыре."}, ""},
	}
	for _, tc := range tests {
		var b streamedSentenceBuffer
		var got []string
		for _, delta := range tc.deltas {
			if err := b.feed(delta, func(s string) error { got = append(got, s); return nil }); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Join(got, "|") != strings.Join(tc.segments, "|") {
			t.Fatalf("segments=%q, want=%q", got, tc.segments)
		}
		if full := strings.Join(tc.deltas, ""); b.remaining(full) != tc.tail {
			t.Fatalf("tail=%q, want=%q", b.remaining(full), tc.tail)
		}
	}
}

func TestQuasarStreamedStoryBatchesFourSentencesWithoutClosingBetween(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	up := make(chan wire.Frame, 64)
	down := make(chan wire.Frame, 256)
	release := make(chan struct{})
	complete := make(chan struct{})
	full := "Жил-был часовщик Григорий. Он чинил часы. Однажды он нашёл ключ. Ключ открывал башню."
	engine := &Engine{LLM: streamingCompletionFunc{
		complete: func(context.Context, []llm.Message, bool) (string, error) { return full, nil },
		stream: func(ctx context.Context, _ []llm.Message, _ bool, onDelta func(string) error) (string, error) {
			if err := onDelta("Жил-был часовщик Григорий. "); err != nil {
				return "", err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			for _, chunk := range []string{
				"Он чинил часы. ",
				"Однажды он нашёл ключ. ",
				"Ключ открывал башню.",
			} {
				if err := onDelta(chunk); err != nil {
					return "", err
				}
			}
			close(complete)
			return full, nil
		},
	}}
	n := NewNative(ctx, func(_ context.Context, _ websocket.MessageType, b []byte) error {
		up <- wire.Decode(b, wire.ClientToServer)
		return nil
	}, func(_ context.Context, _ websocket.MessageType, b []byte) error {
		down <- wire.Decode(b, wire.ServerToClient)
		return nil
	}, engine)
	defer func() {
		cancel()
		n.Close()
	}()
	n.OnVoiceMode = func(string) VoiceMode { return VoiceModeQuasar }
	voice := aa(&client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{
		Header: &header.TEventHeader{MessageId: "four-segments", StreamId: new(int64(12))},
		Event: &client.TClientMessage_TEvent_VoiceInput{VoiceInput: &request.TAliceRequestProto{
			Request: &request.TAliceRequestProto_TRequest{Event: &common.TEvent{Type: common.EEventType_voice_input.Enum()}},
			Format:  new("audio/opus"),
		}},
	}}})
	if err := n.Client(ctx, websocket.MessageBinary, voice); err != nil {
		t.Fatal(err)
	}
	<-up // forwarded stock VoiceInput
	final := aa(&server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{
		Header: &header.TEventHeader{RefMessageId: "four-segments"},
		Event: &server.TServerMessage_TEvent_AsrResult{AsrResult: &asr.TAsrResult{
			EndOfUtt: new(true), Recognition: []*recognition.TAsrRecognition{{Normalized: new("расскажи историю")}},
		}},
	}}})
	if err := n.Server(ctx, websocket.MessageBinary, final); err != nil {
		t.Fatal(err)
	}
	expectTTS := func(want string) wire.Frame {
		t.Helper()
		for {
			select {
			case f := <-up:
				if f.Kind() != "TtsGenerate" {
					continue
				}
				text := f.Client.GetEvent().GetTtsGenerate().GetText()
				if text != want {
					t.Fatalf("TTS=%q, want=%q", text, want)
				}
				return f
			case <-ctx.Done():
				t.Fatalf("timed out waiting for native TTS %q", want)
				return wire.Frame{}
			}
		}
	}
	first := expectTTS("Жил-был часовщик Григорий.")
	select {
	case <-complete:
		t.Fatal("LLM completed before first TTS")
	default:
	}
	feedSpeech(t, n, first.Client.GetEvent().GetHeader().GetMessageId(), 501, false)
	close(release)
	wantRemaining := "Он чинил часы. Однажды он нашёл ключ. Ключ открывал башню."
	gotRemaining := ""
	requestCount := 1
	for gotRemaining != wantRemaining {
		var next wire.Frame
		for {
			select {
			case f := <-up:
				if f.Kind() == "TtsGenerate" {
					next = f
				}
			case <-ctx.Done():
				t.Fatal("missing next stock TTS segment")
			}
			if next.Kind() == "TtsGenerate" {
				break
			}
		}
		chunk := next.Client.GetEvent().GetTtsGenerate().GetText()
		if gotRemaining != "" {
			gotRemaining += " "
		}
		gotRemaining += chunk
		if !strings.HasPrefix(wantRemaining, gotRemaining) {
			t.Fatalf("streamed TTS text=%q not prefix of expected=%q", gotRemaining, wantRemaining)
		}
		requestCount++
		feedSpeech(t, n, next.Client.GetEvent().GetHeader().GetMessageId(), uint32(500+requestCount), false)
	}
	if requestCount != 4 {
		t.Fatalf("four sentences must use exactly four TTS requests, got %d", requestCount)
	}
	select {
	case <-complete:
	case <-ctx.Done():
		t.Fatal("LLM never completed")
	}
	ttsSpeaks, closes, finalParts := 0, 0, 0
	for {
		select {
		case f := <-down:
			if f.Kind() == "TtsSpeak" {
				ttsSpeaks++
			}
			if close := f.Server.GetStreamControl(); close != nil && close.GetClose() != nil {
				closes++
				if close.GetStreamId() != 501 {
					t.Fatalf("audio closed stream %d, want first stream 501", close.GetStreamId())
				}
			}
			if e := f.Server.GetEvent(); e != nil {
				if deferred := e.GetDeferredAliceResponse(); deferred != nil && deferred.GetIsLast() {
					finalParts++
				}
			}
			if closes > 0 && finalParts > 0 {
				if ttsSpeaks != 1 || closes != 1 || finalParts != 1 {
					t.Fatalf("TtsSpeak=%d, closes=%d, finalParts=%d", ttsSpeaks, closes, finalParts)
				}
				n.mu.Lock()
				ttsEntries := len(n.tts)
				n.mu.Unlock()
				if ttsEntries != 1 {
					t.Fatalf("stale intermediate TTS IDs retained: %d (want last only)", ttsEntries)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("stream never finished: TtsSpeak=%d, closes=%d, finalParts=%d", ttsSpeaks, closes, finalParts)
		}
	}
}
