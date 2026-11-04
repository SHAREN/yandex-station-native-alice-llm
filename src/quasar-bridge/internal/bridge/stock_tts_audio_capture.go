package bridge

// Temporary, opt-in capture of Yandex cloud TTS playback for exact splice
// analysis. NEVER records microphone or ASR. Audio/text remain private on
// station tmpfs and are not sent to the repository or any external service.
import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stockCaptureSentinel = "/data/opt/quasar-bridge/enable-stock-tts-multishot"
const stockCaptureDir = "/tmp"
const stockCaptureMaxResponses = 6
const stockCapturePerReplyLimit = int64(6 << 20)
const stockCaptureTotalLimit = int64(18 << 20)
const stockCaptureTextLimit = int64(128 << 10)

type stockCaptureSession struct {
	file    *os.File
	path    string
	bytes   int64
	request string
	index   int
}

var stockCapture = struct {
	sync.Mutex
	started    int
	totalBytes int64
	sessions   map[uint32]*stockCaptureSession
}{sessions: make(map[uint32]*stockCaptureSession)}

func stockCaptureEnabled() bool {
	if !stockTTSEnabled() {
		return false
	}
	_, err := os.Stat(stockCaptureSentinel)
	return err == nil
}

func stockCaptureBegin(stream uint32, format, requestID string) {
	if !stockCaptureEnabled() || stream == 0 || format != "audio/opus" {
		return
	}
	stockCapture.Lock()
	defer stockCapture.Unlock()
	if stockCapture.started >= stockCaptureMaxResponses || stockCapture.totalBytes >= stockCaptureTotalLimit {
		return
	}
	if _, exists := stockCapture.sessions[stream]; exists {
		return
	}
	next := stockCapture.started + 1
	path := filepath.Join(stockCaptureDir, fmt.Sprintf("stock-alice-splice-%02d-stream-%d.ogg", next, stream))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		slog.Warn("stock_tts_capture_unavailable", "error", err)
		return
	}
	stockCapture.started = next
	stockCapture.sessions[stream] = &stockCaptureSession{
		file: file, path: path, request: requestID, index: next,
	}
	stockTraceRecord(map[string]any{"kind": "capture_started", "stream": stream, "capture": next, "request": stockTraceID(requestID)})
}

func stockCaptureChunk(stream uint32, raw []byte) {
	if len(raw) == 0 {
		return
	}
	stockCapture.Lock()
	defer stockCapture.Unlock()
	part := stockCapture.sessions[stream]
	if part == nil || part.file == nil {
		return
	}
	if part.bytes+int64(len(raw)) > stockCapturePerReplyLimit ||
		stockCapture.totalBytes+int64(len(raw)) > stockCaptureTotalLimit {
		_ = part.file.Close()
		part.file = nil
		stockTraceRecord(map[string]any{"kind": "capture_truncated", "stream": stream, "capture": part.index, "bytes": part.bytes})
		return
	}
	n, err := part.file.Write(raw)
	part.bytes += int64(n)
	stockCapture.totalBytes += int64(n)
	if err != nil || n != len(raw) {
		slog.Warn("stock_tts_capture_write_failed", "error", err)
		_ = part.file.Close()
		part.file = nil
	}
}

// Only response text from Yandex's DeferredAliceResponse events is retained,
// never the user's spoken request. The pseudonym links a response to its audio
// capture. Text can arrive immediately before TtsSpeak.
func stockCaptureResponseText(requestID string, part uint32, text, voice string, last bool) {
	if !stockCaptureEnabled() || (text == "" && voice == "") {
		return
	}
	record := map[string]any{
		"at":      time.Now().UTC().Format(time.RFC3339Nano),
		"request": stockTraceID(requestID),
		"part":    part, "last": last, "text": text, "voice": voice,
	}
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > 16384 {
		return
	}
	stockCapture.Lock()
	defer stockCapture.Unlock()
	file, err := os.OpenFile(filepath.Join(stockCaptureDir, "stock-alice-splice-response-text.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size()+int64(len(encoded)+1) > stockCaptureTextLimit {
		return
	}
	_, _ = file.Write(append(encoded, '\n'))
}

func stockCaptureEnd(stream uint32) {
	stockCapture.Lock()
	defer stockCapture.Unlock()
	part := stockCapture.sessions[stream]
	if part == nil {
		return
	}
	if part.file != nil {
		if err := part.file.Sync(); err != nil {
			slog.Warn("stock_tts_capture_sync_failed", "error", err)
		}
		_ = part.file.Close()
		part.file = nil
	}
	delete(stockCapture.sessions, stream)
	stockTraceRecord(map[string]any{
		"kind": "capture_completed", "capture": part.index, "stream": stream,
		"request": stockTraceID(part.request), "bytes": part.bytes,
	})
}
