package bridge

// Stock UniProxy TTS telemetry. This is deliberately metadata-only: no ASR
// transcript, generated speech text, raw audio or account identifiers are
// ever written. It is passively invoked before the normal message handling,
// including when strict stock Alice mode bypasses the request engine.
import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"quasarbridge/internal/wire"
)

const stockTTSTraceSentinel = "/data/opt/quasar-bridge/enable-stock-tts-trace"
const stockTTSTracePath = "/data/opt/quasar-bridge/stock-tts-telemetry.jsonl"

var stockTTSRecorder = struct {
	sync.Mutex
	once  sync.Once
	on    bool
	audio map[uint32]*stockAudioPages
	first map[uint32]bool
}{}

type stockAudioPages struct {
	unread []byte
	pages  uint64
	bos    uint64
	eos    uint64
}

func stockTTSEnabled() bool {
	stockTTSRecorder.once.Do(func() {
		_, err := os.Stat(stockTTSTraceSentinel)
		stockTTSRecorder.on = err == nil
		if stockTTSRecorder.on {
			stockTTSRecorder.audio = make(map[uint32]*stockAudioPages)
			stockTTSRecorder.first = make(map[uint32]bool)
		}
	})
	return stockTTSRecorder.on
}

func stockTraceRecord(fields map[string]any) {
	if !stockTTSEnabled() {
		return
	}
	stockTTSRecorder.Lock()
	defer stockTTSRecorder.Unlock()
	stockTraceWriteLocked(fields)
}

func stockTraceWriteLocked(fields map[string]any) {
	fields["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	f, err := os.OpenFile(stockTTSTracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 16<<20 {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

func stockTraceID(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:5])
}

func stockProtoVarint(raw []byte, wanted protowire.Number) (uint64, bool) {
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			break
		}
		raw = raw[n:]
		if num == wanted && typ == protowire.VarintType {
			v, m := protowire.ConsumeVarint(raw)
			return v, m >= 0
		}
		n = protowire.ConsumeFieldValue(num, typ, raw)
		if n < 0 {
			break
		}
		raw = raw[n:]
	}
	return 0, false
}
func stockProtoBytes(raw []byte, wanted protowire.Number) ([]byte, bool) {
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			break
		}
		raw = raw[n:]
		if num == wanted && typ == protowire.BytesType {
			b, m := protowire.ConsumeBytes(raw)
			return b, m >= 0
		}
		n = protowire.ConsumeFieldValue(num, typ, raw)
		if n < 0 {
			break
		}
		raw = raw[n:]
	}
	return nil, false
}

func stockTraceClient(k websocket.MessageType, data []byte, f wire.Frame) {
	if !stockTTSEnabled() {
		return
	}
	if f.Client != nil {
		if e := f.Client.GetEvent(); e != nil {
			switch f.Kind() {
			case "VoiceInput", "SpeechInput":
				stockTraceRecord(map[string]any{"direction": "up", "kind": f.Kind(), "request": stockTraceID(e.GetHeader().GetMessageId())})
			case "TtsGenerate":
				v := e.GetTtsGenerate()
				stream, _ := stockProtoVarint(v.ProtoReflect().GetUnknown(), 12)
				stockTraceRecord(map[string]any{"direction": "up", "kind": "TtsGenerate", "request": stockTraceID(e.GetHeader().GetMessageId()), "text_bytes": len(v.GetText()), "is_streaming": stream == 1})
			}
			return
		}
		if sc := f.Client.GetStreamControl(); sc != nil && sc.GetClose() != nil {
			stockTraceRecord(map[string]any{"direction": "up", "kind": "StreamClose", "stream": sc.GetStreamId(), "request": stockTraceID(sc.GetMessageId()), "reason": sc.GetClose().GetReason().String()})
		}
		return
	}
	if k != websocket.MessageBinary {
		return
	}
	raw := data
	if len(raw) > 4 && string(raw[:4]) == "AAPI" {
		raw = raw[4:]
	}
	ev, ok := stockProtoBytes(raw, 1)
	if !ok {
		return
	}
	// Client event has Header (field 1) and a oneof. For latest protocols
	// DeferredTtsGenerate is event field 34, not in pinned generated protobuf.
	body, ok := stockProtoBytes(ev, 34)
	if !ok {
		return
	}
	textBytes, _ := stockProtoBytes(body, 2)
	reqID, _ := stockProtoBytes(body, 1)
	last, _ := stockProtoVarint(body, 3)
	chunk, _ := stockProtoVarint(body, 4)
	stockTraceRecord(map[string]any{"direction": "up", "kind": "DeferredTtsGenerate", "request": stockTraceID(string(reqID)), "chunk": chunk, "is_last": last == 1, "text_bytes": len(textBytes)})
}

func stockTraceServer(k websocket.MessageType, data []byte, f wire.Frame) {
	if !stockTTSEnabled() {
		return
	}
	if f.Server != nil {
		if e := f.Server.GetEvent(); e != nil {
			switch f.Kind() {
			case "TtsSpeak":
				msg := e.GetTtsSpeak()
				stockCaptureBegin(uint32(e.GetHeader().GetStreamId()), msg.GetFormat())
				raw := msg.ProtoReflect().GetUnknown()
				lazy, _ := stockProtoVarint(raw, 2)
				chunk, _ := stockProtoVarint(raw, 6)
				sub, _ := stockProtoVarint(raw, 7)
				stockTraceRecord(map[string]any{"direction": "down", "kind": "TtsSpeak", "request": stockTraceID(e.GetHeader().GetRefMessageId()), "stream": e.GetHeader().GetStreamId(), "format": msg.GetFormat(), "lazy_tts_streaming": lazy == 1, "sound_chunk_type": chunk, "substream_id": sub})
			case "AliceResponse":
				stockTraceRecord(map[string]any{"direction": "down", "kind": f.Kind(), "request": stockTraceID(e.GetHeader().GetRefMessageId())})
			case "DeferredAliceResponse":
				d := e.GetDeferredAliceResponse()
				if b := d.GetBaseResponse(); b != nil {
					voice, _ := stockProtoBytes(b.ProtoReflect().GetUnknown(), 2)
					stockCaptureResponseText(d.GetResponsePartialNum(), b.GetText(), string(voice), d.GetIsLast())
				}
				stockTraceRecord(map[string]any{"direction": "down", "kind": f.Kind(), "request": stockTraceID(e.GetHeader().GetRefMessageId())})
			}
			return
		}
		if sc := f.Server.GetStreamControl(); sc != nil && sc.GetClose() != nil {
			stockCaptureEnd(uint32(sc.GetStreamId()))
			stockTraceRecord(map[string]any{"direction": "down", "kind": "StreamClose", "stream": sc.GetStreamId(), "request": stockTraceID(sc.GetMessageId()), "reason": sc.GetClose().GetReason().String()})
			return
		}
	}
	if k == websocket.MessageText {
		// Some deployed UniProxy sessions send ordinary JSON directives.
		var m struct {
			Directive struct {
				Header struct {
					Name         string `json:"name"`
					Namespace    string `json:"namespace"`
					StreamID     int64  `json:"streamId"`
					RefMessageID string `json:"refMessageId"`
				} `json:"header"`
				Payload struct {
					Lazy           bool   `json:"lazy_tts_streaming"`
					Format         string `json:"format"`
					SubStreamID    uint64 `json:"sub_stream_id"`
					SoundChunkType uint64 `json:"sound_chunk_type"`
				} `json:"payload"`
			} `json:"directive"`
			Streamcontrol json.RawMessage `json:"streamcontrol"`
		}
		if json.Unmarshal(data, &m) == nil {
			h := m.Directive.Header
			if h.Namespace == "TTS" && h.Name == "Speak" {
				stockTraceRecord(map[string]any{"direction": "down", "kind": "TtsSpeakJSON", "request": stockTraceID(h.RefMessageID), "stream": h.StreamID, "format": m.Directive.Payload.Format, "lazy_tts_streaming": m.Directive.Payload.Lazy, "substream_id": m.Directive.Payload.SubStreamID, "sound_chunk_type": m.Directive.Payload.SoundChunkType})
			} else if len(m.Streamcontrol) > 0 {
				stockTraceRecord(map[string]any{"direction": "down", "kind": "StreamControlJSON"})
			}
		}
		return
	}
	if k != websocket.MessageBinary || f.Server != nil || f.Legacy != nil || len(data) < 8 || string(data[:4]) == "AAPI" {
		return
	}
	stockTraceOggAudio(data)
}

func stockTraceOggAudio(data []byte) {
	stream := binary.BigEndian.Uint32(data[:4])
	raw := data[4:]
	if len(raw) == 0 {
		return
	}
	stockCaptureChunk(stream, raw)
	stockTTSRecorder.Lock()
	defer stockTTSRecorder.Unlock()
	info := stockTTSRecorder.audio[stream]
	if info == nil {
		if len(raw) < 4 || string(raw[:4]) != "OggS" {
			return
		}
		info = &stockAudioPages{}
		stockTTSRecorder.audio[stream] = info
	}
	if !stockTTSRecorder.first[stream] {
		stockTTSRecorder.first[stream] = true
		stockTraceWriteLocked(map[string]any{"direction": "down", "kind": "first_audio", "stream": stream, "bytes": len(raw)})
	}
	// Parse Ogg page headers only, discard bytes immediately. Keep at most
	// 1 MiB of an incomplete page. No decoded audio or speech text retained.
	if len(info.unread)+len(raw) > 1<<20 {
		info.unread = nil
		return
	}
	info.unread = append(info.unread, raw...)
	for len(info.unread) >= 27 {
		if string(info.unread[:4]) != "OggS" {
			info.unread = nil
			return
		}
		segs := int(info.unread[26])
		if len(info.unread) < 27+segs {
			return
		}
		total := 27 + segs
		for _, v := range info.unread[27:total] {
			total += int(v)
		}
		if len(info.unread) < total {
			return
		}
		flags := info.unread[5]
		serial := binary.LittleEndian.Uint32(info.unread[14:18])
		seq := binary.LittleEndian.Uint32(info.unread[18:22])
		info.pages++
		if flags&2 != 0 {
			info.bos++
		}
		if flags&4 != 0 {
			info.eos++
		}
		if flags&6 != 0 {
			stockTraceWriteLocked(map[string]any{"direction": "down", "kind": "ogg_boundary", "stream": stream, "serial": serial, "seq": seq, "bos": flags&2 != 0, "eos": flags&4 != 0, "pages_seen": info.pages, "bos_seen": info.bos, "eos_seen": info.eos})
		}
		info.unread = info.unread[total:]
	}
}

// compile-time guard: proto supports unknown fields on the pinned old schema.
var _ proto.Message
