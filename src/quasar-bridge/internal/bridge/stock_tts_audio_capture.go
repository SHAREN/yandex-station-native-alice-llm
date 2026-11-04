package bridge

// One-shot capture of ONLY server-side stock TTS audio (never microphone
// audio). Activated explicitly for a physical junction investigation.
// The file lives on tmpfs, is private, and is capped at 10 MiB.
import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

const stockOneShotSentinel = "/data/opt/quasar-bridge/enable-stock-tts-one-shot"
const stockOneShotPath = "/tmp/stock-alice-tts-one-shot.ogg"
const stockOneShotLimit = int64(10 << 20)

var stockOneShot = struct {
	sync.Mutex
	active   bool
	finished bool
	stream   uint32
	bytes    int64
	file     *os.File
}{}

func stockCaptureBegin(stream uint32, format string) {
	if !stockTTSEnabled() || stream == 0 || format != "audio/opus" {
		return
	}
	if _, err := os.Stat(stockOneShotSentinel); err != nil {
		return
	}
	stockOneShot.Lock()
	defer stockOneShot.Unlock()
	if stockOneShot.active || stockOneShot.finished {
		return
	}
	f, err := os.OpenFile(stockOneShotPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		slog.Warn("stock_tts_capture_unavailable", "error", err)
		return
	}
	stockOneShot.active = true
	stockOneShot.stream = stream
	stockOneShot.file = f
	stockTraceRecord(map[string]any{"kind": "capture_started", "stream": stream})
}

func stockCaptureChunk(stream uint32, raw []byte) {
	if len(raw) == 0 {
		return
	}
	stockOneShot.Lock()
	defer stockOneShot.Unlock()
	if !stockOneShot.active || stockOneShot.stream != stream || stockOneShot.file == nil {
		return
	}
	if stockOneShot.bytes+int64(len(raw)) > stockOneShotLimit {
		_ = stockOneShot.file.Close()
		stockOneShot.file = nil
		stockOneShot.active = false
		stockOneShot.finished = true
		stockTraceRecord(map[string]any{"kind": "capture_truncated", "stream": stream, "bytes": stockOneShot.bytes})
		return
	}
	n, err := stockOneShot.file.Write(raw)
	stockOneShot.bytes += int64(n)
	if err != nil {
		slog.Warn("stock_tts_capture_write_failed", "error", err)
		_ = stockOneShot.file.Close()
		stockOneShot.file = nil
		stockOneShot.active = false
		stockOneShot.finished = true
	}
}

// The spoken text of this one controlled test is stored separately so an
// extracted audio segment can be matched to exact words. No ASR input is stored.
func stockCaptureResponseText(part uint32, text, voice string, last bool) {
	if _, err := os.Stat(stockOneShotSentinel); err != nil {
		return
	}
	if text == "" && voice == "" {
		return
	}
	stockOneShot.Lock()
	defer stockOneShot.Unlock()
	if stockOneShot.finished {
		return
	}
	record := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "part": part, "last": last, "text": text, "voice": voice}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	if len(encoded) > 16384 {
		return
	}
	path := "/tmp/stock-alice-tts-one-shot-text.jsonl"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > 131072 {
		return
	}
	_, _ = file.Write(append(encoded, '\n'))
}

func stockCaptureEnd(stream uint32) {
	stockOneShot.Lock()
	defer stockOneShot.Unlock()
	if !stockOneShot.active || stockOneShot.stream != stream {
		return
	}
	if stockOneShot.file != nil {
		if err := stockOneShot.file.Sync(); err != nil {
			slog.Warn("stock_tts_capture_sync_failed", "error", err)
		}
		_ = stockOneShot.file.Close()
		stockOneShot.file = nil
	}
	stockOneShot.active = false
	stockOneShot.finished = true
	stockTraceRecord(map[string]any{"kind": "capture_completed", "stream": stream, "bytes": stockOneShot.bytes})
}
