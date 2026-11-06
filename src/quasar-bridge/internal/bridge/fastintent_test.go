package bridge

import (
	"context"
	"testing"

	"quasarbridge/internal/home/fastintent"
	"quasarbridge/internal/llm"
)

type fastHomeStub struct {
	calls int
}

func (h *fastHomeStub) Handle(context.Context, string) (string, error) {
	panic("slow home planner should not be called for direct intent")
}
func (h *fastHomeStub) CanFastHandle(i fastintent.Intent) bool {
	return (i.Room == "local" || i.Room == "living_room") &&
		(i.Operation == "on" || i.Operation == "off" || i.Operation == "color")
}
func (h *fastHomeStub) FastHandle(context.Context, fastintent.Intent) (string, error) {
	h.calls++
	return "Готово.", nil
}

func TestFastHomeRoutingSkipsClassifierAndPlanner(t *testing.T) {
	home := &fastHomeStub{}
	llmCalls := 0
	e := &Engine{
		Home: home,
		LLM: completionFunc(func(context.Context, []llm.Message, bool) (string, error) {
			llmCalls++
			return "", nil
		}),
	}
	for _, command := range []string{
		"Алиса, включи свет", "выключи свет в зале", "включи красный в зале",
	} {
		decision := e.Decide(t.Context(), command)
		if decision.Route != "home" || decision.Source != "fast_intent" {
			t.Fatalf("decision for %q = %+v", command, decision)
		}
		result, err := e.ResolveDecision(t.Context(), command, decision)
		if err != nil || result.Route != "home" || result.Answer != "Готово." {
			t.Fatalf("response for %q: %+v %v", command, result, err)
		}
	}
	if llmCalls != 0 || home.calls != 3 {
		t.Fatalf("wrong backends: LLM=%d FastHome=%d", llmCalls, home.calls)
	}
}

func TestFastHomeFailClosedAndOnlyAllowedWholePhrases(t *testing.T) {
	e := &Engine{Home: &fastHomeStub{}}
	for _, phrase := range []string{
		"не включай свет", "открой дверь", "выключи свет в прихожей",
		"включи свет и открой дверь", "включи белый шум",
		"включи красный в коридоре",
	} {
		if decision, ok := e.MatchFastIntent(phrase); ok {
			t.Fatalf("unexpected fast execution for %q: %+v", phrase, decision)
		}
	}
	if _, ok := (&Engine{}).MatchFastIntent("включи свет"); ok {
		t.Fatal("nil home should not enable fast route")
	}
	var _ FastIntentResolver = e
}
