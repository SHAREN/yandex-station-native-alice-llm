// Package live contains the opt-in, transport-independent control plane for
// a future full-duplex voice session. It does not touch maind, the audio path,
// network credentials, or the existing stock Yandex/LLM routing.
package live

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// Command is a complete, final ASR utterance action; partial ASR must not
// be passed to MatchCommand or HandleFinalASR.
type Command string

const (
	NoCommand Command = ""
	Start     Command = "start"
	Stop      Command = "stop"
)

// MatchCommand deliberately uses exact utterances. Substrings such as
// "как включить GPT Live" must never activate the microphone.
func MatchCommand(text string) Command {
	phrase := normalize(text)
	phrase = strings.TrimPrefix(phrase, "алиса ")
	phrase = strings.TrimPrefix(phrase, "яндекс ")
	switch phrase {
	case "включи gpt live", "включи режим gpt live",
		"включи джипити лайв", "включи режим джипити лайв",
		"включи голосовой чат", "открой голосовой чат",
		"позови chatgpt", "позови чат гпт", "позови чат джипити":
		return Start
	case "выключи gpt live", "выключи режим gpt live",
		"выключи джипити лайв", "выключи режим джипити лайв",
		"закрой голосовой чат", "заверши разговор":
		return Stop
	default:
		return NoCommand
	}
}

func normalize(text string) string {
	text = strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	text = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

type State string

const (
	Idle       State = "idle"
	Connecting State = "connecting"
	Active     State = "active"
	Stopping   State = "stopping"
)

type Config struct {
	// Disabled by default. A future caller must explicitly enable this.
	Enabled        bool
	ConnectTimeout time.Duration
	StopTimeout    time.Duration
	IdleTimeout    time.Duration
}

type Snapshot struct {
	State      State
	Generation uint64
	Deadline   time.Time
	Enabled    bool
}

// Controller is the synchronized control plane. It never creates sockets or
// acquires audio focus: the future gateway adapter owns those side effects.
// Every asynchronous callback must carry the returned Generation to prevent
// a late connection from resurrecting an already closed session.
type Controller struct {
	mu             sync.Mutex
	enabled        bool
	state          State
	generation     uint64
	deadline       time.Time
	connectTimeout time.Duration
	stopTimeout    time.Duration
	idleTimeout    time.Duration
}

func NewController(cfg Config) *Controller {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 12 * time.Second
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = 5 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 2 * time.Minute
	}
	return &Controller{
		enabled: cfg.Enabled, state: Idle,
		connectTimeout: cfg.ConnectTimeout, stopTimeout: cfg.StopTimeout,
		idleTimeout: cfg.IdleTimeout,
	}
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{State: c.state, Generation: c.generation, Deadline: c.deadline, Enabled: c.enabled}
}

// Begin reserves a generation and a deadline. Only one Live session may
// connect at a time; the disabled controller does not intercept stock ASR.
func (c *Controller) Begin(now time.Time) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled || c.state != Idle {
		return 0, false
	}
	c.generation++
	c.state = Connecting
	c.deadline = now.Add(c.connectTimeout)
	return c.generation, true
}

// Ready is called only after the gateway confirms an actual audio session.
func (c *Controller) Ready(generation uint64, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.state != Connecting || !now.Before(c.deadline) {
		return false
	}
	c.state = Active
	c.deadline = now.Add(c.idleTimeout)
	return true
}

// Touch refreshes the inactivity deadline after activity on an active
// gateway session. A stale/closed session cannot keep the next one alive.
func (c *Controller) Touch(generation uint64, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.state != Active || !now.Before(c.deadline) {
		return false
	}
	c.deadline = now.Add(c.idleTimeout)
	return true
}

// RequestStop is idempotent. The gateway must terminate its audio ownership
// before calling Closed; a repeated voice command does not restart stopping.
func (c *Controller) RequestStop(now time.Time) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != Connecting && c.state != Active {
		return 0, false
	}
	c.state = Stopping
	c.deadline = now.Add(c.stopTimeout)
	return c.generation, true
}

// Closed acknowledges a stopped/failed or canceled gateway session.
func (c *Controller) Closed(generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.state == Idle {
		return false
	}
	c.state = Idle
	c.deadline = time.Time{}
	return true
}

// Expire transitions the logical state to idle and reports which gateway
// generation the owner MUST cancel/close. This alone does not release ALSA
// or cancel network I/O; the integration must perform that cleanup.
func (c *Controller) Expire(now time.Time) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == Idle || now.Before(c.deadline) {
		return 0, false
	}
	c.state = Idle
	c.deadline = time.Time{}
	return c.generation, true
}

// HandleFinalASR is for *final* ASR only. When Live is disabled or no command
// is recognized the caller must preserve the existing Quasar/Yandex path.
// Stop works in Connecting/Active, but does not swallow stock "stop" in Idle.
func (c *Controller) HandleFinalASR(text string, now time.Time) (Command, uint64, bool) {
	switch MatchCommand(text) {
	case Start:
		generation, ok := c.Begin(now)
		return Start, generation, ok
	case Stop:
		generation, ok := c.RequestStop(now)
		return Stop, generation, ok
	default:
		return NoCommand, 0, false
	}
}
