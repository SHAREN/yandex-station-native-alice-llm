package live

import (
	"sync"
	"testing"
	"time"
)

func TestMatchCommandExactAndSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		phrase string
		want   Command
	}{
		{"Алиса, включи GPT-Live!", Start},
		{"Включи режим GPT Live", Start},
		{"Яндекс, включи джипити лайв", Start},
		{"Алиса, включи голосовой чат.", Start},
		{"Алиса, позови ChatGPT", Start},
		{"Алиса, позови чат джипити", Start},
		{"Алиса, выключи GPT-Live", Stop},
		{"Заверши разговор!", Stop},
		{"Алиса, закрой голосовой чат", Stop},
		{"Алиса, включи свет", NoCommand},
		{"Алиса, который час?", NoCommand},
		{"Расскажи, как включить GPT-Live", NoCommand},
		{"Не включи GPT Live", NoCommand},
		{"Алиса, не включай голосовой чат", NoCommand},
		{"Алиса, включи GPT Live завтра", NoCommand},
		{"Включи GPT-Live и выключи свет", NoCommand},
		{"Алиса, открой чат", NoCommand}, // Avoid stealing unrelated stock/skill commands.
		{"", NoCommand},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.phrase, func(t *testing.T) {
			t.Parallel()
			if got := MatchCommand(tt.phrase); got != tt.want {
				t.Fatalf("MatchCommand(%q) = %q, want %q", tt.phrase, got, tt.want)
			}
		})
	}
}

func TestDisabledNeverConsumesCommands(t *testing.T) {
	t.Parallel()
	c := NewController(Config{})
	now := time.Date(2026, time.November, 8, 0, 0, 0, 0, time.UTC)
	if action, _, handled := c.HandleFinalASR("Алиса, включи GPT Live", now); action != Start || handled {
		t.Fatalf("disabled controller must not consume command, action=%s handled=%v", action, handled)
	}
	if _, ok := c.Begin(now); ok {
		t.Fatal("disabled controller cannot connect")
	}
	if state := c.Snapshot().State; state != Idle {
		t.Fatalf("disabled state changed to %s", state)
	}
}

func TestLifecycleAndStaleCallbacks(t *testing.T) {
	t.Parallel()
	c := NewController(Config{Enabled: true})
	now := time.Date(2026, time.November, 8, 0, 0, 0, 0, time.UTC)
	action, first, ok := c.HandleFinalASR("Алиса, включи GPT-Live", now)
	if !ok || action != Start || first == 0 || c.Snapshot().State != Connecting {
		t.Fatalf("start failed action=%s id=%d ok=%v", action, first, ok)
	}
	if _, ok := c.Begin(now); ok {
		t.Fatal("duplicate connection accepted")
	}
	if !c.Ready(first, now.Add(time.Second)) || c.Snapshot().State != Active {
		t.Fatal("gateway ready did not activate")
	}
	if !c.Touch(first, now.Add(10*time.Second)) {
		t.Fatal("active heartbeat was rejected")
	}
	if c.Snapshot().Deadline != now.Add(2*time.Minute+10*time.Second) {
		t.Fatal("activity deadline not refreshed")
	}
	if _, _, handled := c.HandleFinalASR("Алиса, включи свет", now); handled {
		t.Fatal("regular command was stolen")
	}
	action, stopID, ok := c.HandleFinalASR("Заверши разговор", now.Add(11*time.Second))
	if !ok || action != Stop || stopID != first || c.Snapshot().State != Stopping {
		t.Fatalf("stop failed action=%s id=%d ok=%v", action, stopID, ok)
	}
	if _, ok := c.RequestStop(now); ok {
		t.Fatal("duplicate stop accepted")
	}
	if c.Ready(first, now) {
		t.Fatal("late Ready revived a stopping session")
	}
	if !c.Closed(first) || c.Snapshot().State != Idle {
		t.Fatal("close did not restore idle")
	}
	if c.Closed(first) {
		t.Fatal("duplicate close should not signal another closure")
	}
	if _, _, handled := c.HandleFinalASR("Заверши разговор", now); handled {
		t.Fatal("stock stop was intercepted in idle")
	}
	second, ok := c.Begin(now.Add(15 * time.Second))
	if !ok || second <= first {
		t.Fatal("new session generation not assigned")
	}
	if c.Ready(first, now) || c.Closed(first) || c.Touch(first, now) {
		t.Fatal("old session mutated the new session")
	}
	if c.Snapshot().State != Connecting {
		t.Fatal("stale callback changed state")
	}
	if !c.Closed(second) {
		t.Fatal("failed gateway did not close")
	}
}

func TestConnectionAndStopTimeouts(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, time.November, 8, 0, 0, 0, 0, time.UTC)
	c := NewController(Config{Enabled: true, ConnectTimeout: 3 * time.Second, StopTimeout: time.Second, IdleTimeout: 8 * time.Second})
	id, ok := c.Begin(t0)
	if !ok {
		t.Fatal("start rejected")
	}
	if _, expired := c.Expire(t0.Add(2999 * time.Millisecond)); expired {
		t.Fatal("timeout fired early")
	}
	if c.Ready(id, t0.Add(3*time.Second)) {
		t.Fatal("gateway ready at the timeout boundary")
	}
	if stale, expired := c.Expire(t0.Add(3 * time.Second)); !expired || stale != id {
		t.Fatal("connection timeout did not return the generation to cancel")
	}
	if c.Snapshot().State != Idle || c.Ready(id, t0) {
		t.Fatal("expired generation resurrected")
	}
	id2, _ := c.Begin(t0.Add(4 * time.Second))
	if !c.Ready(id2, t0.Add(5*time.Second)) {
		t.Fatal("second session did not become active")
	}
	if expired, ok := c.Expire(t0.Add(13 * time.Second)); !ok || expired != id2 {
		t.Fatal("idle timeout did not close inactive session")
	}
	id3, _ := c.Begin(t0.Add(14 * time.Second))
	stop, ok := c.RequestStop(t0.Add(15 * time.Second))
	if !ok || stop != id3 {
		t.Fatal("stop while connecting must be possible")
	}
	if expired, ok := c.Expire(t0.Add(16 * time.Second)); !ok || expired != id3 {
		t.Fatal("stop deadline did not expire")
	}
	if c.Snapshot().State != Idle {
		t.Fatal("stop timeout did not restore idle")
	}
}

func TestSimultaneousStartOnlyOneWins(t *testing.T) {
	t.Parallel()
	c := NewController(Config{Enabled: true})
	now := time.Now()
	const callers = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := c.Begin(now); ok {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("got %d concurrent session opens; want exactly one", success)
	}
}
