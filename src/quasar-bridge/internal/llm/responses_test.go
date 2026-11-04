package llm

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const responseAnswer = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Ответ"}]}]}`

func TestResponsesCompleteContract(t *testing.T) {
	for _, structured := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "structured"}[structured], func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
				}
				var body map[string]any
				if err := json.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
				}
				if body["model"] != "fixture-model" || body["stream"] != false || body["store"] != false || body["max_output_tokens"] != float64(8192) {
					t.Errorf("request body=%v", body)
				}
				for _, field := range []string{"messages", "max_tokens", "temperature", "chat_template_kwargs", "response_format", "previous_response_id"} {
					if _, ok := body[field]; ok {
						t.Errorf("unexpected %s", field)
					}
				}
				if !reflect.DeepEqual(body["reasoning"], map[string]any{"effort": "low"}) {
					t.Errorf("reasoning=%v", body["reasoning"])
				}
				wantInput := []any{map[string]any{"role": "system", "content": "JSON only"}, map[string]any{"role": "developer", "content": "short"}, map[string]any{"role": "user", "content": "Привет"}, map[string]any{"role": "assistant", "content": "Предыдущий ответ"}, map[string]any{"role": "user", "content": "Ещё"}}
				if !reflect.DeepEqual(body["input"], wantInput) {
					t.Errorf("input=%v", body["input"])
				}
				if structured {
					if !reflect.DeepEqual(body["text"], map[string]any{"format": map[string]any{"type": "json_object"}}) {
						t.Errorf("text=%v", body["text"])
					}
				} else if _, ok := body["text"]; ok {
					t.Error("JSON mode used for plain completion")
				}
				w.Write([]byte(responseAnswer))
			}))
			defer s.Close()
			c := Client{API: "responses", BaseURL: s.URL + "/v1///", Model: "fixture-model", APIKey: "fixture-key", ReasoningEffort: "low", MaxOutputTokens: 8192}
			answer, err := c.Complete(t.Context(), []Message{{Role: "system", Content: "JSON only"}, {Role: "developer", Content: "short"}, {Role: "user", Content: "Привет"}, {Role: "assistant", Content: "Предыдущий ответ"}, {Role: "user", Content: "Ещё"}}, structured)
			if err != nil || answer != "Ответ" {
				t.Fatalf("answer=%q error=%v", answer, err)
			}
		})
	}
}

func TestResponsesHostedToolsAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hosted     []string
		disabled   bool
		structured bool
		wantTools  bool
	}{
		{name: "disabled by default"},
		{name: "hosted tools", hosted: []string{"web_search", "code_interpreter"}, wantTools: true},
		{name: "privacy", hosted: []string{"web_search", "code_interpreter"}, disabled: true},
		{name: "structured", hosted: []string{"web_search", "code_interpreter"}, structured: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.UnmarshalRead(r.Body, &body); err != nil {
					t.Error(err)
				}
				if body["max_output_tokens"] != float64(4096) {
					t.Errorf("default token budget=%v", body["max_output_tokens"])
				}
				if _, ok := body["reasoning"]; ok {
					t.Error("unspecified reasoning sent")
				}
				if _, ok := body["parallel_tool_calls"]; ok {
					t.Error("parallel_tool_calls set without local functions")
				}
				if tc.wantTools {
					want := []any{map[string]any{"type": "web_search"}, map[string]any{"type": "code_interpreter", "container": map[string]any{"type": "auto"}}}
					if !reflect.DeepEqual(body["tools"], want) || body["tool_choice"] != "auto" {
						t.Errorf("hosted tools=%v choice=%v", body["tools"], body["tool_choice"])
					}
				} else if _, ok := body["tools"]; ok {
					t.Errorf("hosted tools unexpectedly present: %v", body["tools"])
				}
				w.Write([]byte(responseAnswer))
			}))
			defer s.Close()
			c := Client{API: "responses", BaseURL: s.URL, HostedTools: tc.hosted}
			ctx := t.Context()
			if tc.disabled {
				ctx = WithoutHostedTools(ctx)
			}
			if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "JSON response"}}, tc.structured); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponsesFunctionCycleReplaysAllOutput(t *testing.T) {
	const firstOutput = `[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"encrypted-fixture"},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"time"}},{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Проверяю."}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"current_time","arguments":"{}","status":"completed"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"lookup","arguments":"{\"value\":1}","status":"completed"}]`
	var wantedOutput []any
	if err := json.Unmarshal([]byte(firstOutput), &wantedOutput); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			t.Error(err)
		}
		tools, _ := body["tools"].([]any)
		if len(tools) != 2 || body["parallel_tool_calls"] != false {
			t.Errorf("tools=%v parallel=%v", tools, body["parallel_tool_calls"])
		} else {
			function := tools[0].(map[string]any)
			if function["type"] != "function" || function["name"] != "current_time" || function["description"] != "clock" || function["strict"] != false || function["parameters"] == nil || function["function"] != nil {
				t.Errorf("function definition=%v", function)
			}
		}
		if calls == 1 {
			w.Write([]byte(`{"status":"completed","output":` + firstOutput + `}`))
			return
		}
		want := append([]any{map[string]any{"role": "user", "content": "который час"}}, wantedOutput...)
		want = append(want, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": `{"hour":12}`}, map[string]any{"type": "function_call_output", "call_id": "call_2", "output": "found"})
		if !reflect.DeepEqual(body["input"], want) {
			t.Errorf("input=%v want=%v", body["input"], want)
		}
		w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Сейчас "},{"type":"output_text","text":"полдень"}]}]}`))
	}))
	defer s.Close()
	c := Client{API: "responses", BaseURL: s.URL, HostedTools: []string{"web_search"}}
	tools := []Tool{{Type: "function", Function: Function{Name: "current_time", Description: "clock", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}}
	messages := []Message{{Role: "user", Content: "который час"}}
	msg, err := c.Chat(t.Context(), messages, tools)
	if err != nil || msg.Role != "assistant" || msg.Content != "Проверяю." || len(msg.ToolCalls) != 2 || msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Type != "function" || msg.ToolCalls[1].Function.Arguments != `{"value":1}` {
		t.Fatalf("first message=%+v error=%v", msg, err)
	}
	encoded, err := json.Marshal(msg)
	if err != nil || strings.Contains(string(encoded), "encrypted-fixture") || strings.Contains(string(encoded), "responseOutput") {
		t.Fatalf("private output leaked: %s error=%v", encoded, err)
	}
	messages = append(messages, msg, Message{Role: "tool", ToolCallID: "call_1", Content: `{"hour":12}`}, Message{Role: "tool", ToolCallID: "call_2", Content: "found"})
	msg, err = c.Chat(t.Context(), messages, tools)
	if err != nil || msg.Content != "Сейчас полдень" || len(msg.ToolCalls) != 0 {
		t.Fatalf("final message=%+v error=%v", msg, err)
	}
}

func TestResponsesConvertsSyntheticFunctionContext(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			t.Error(err)
		}
		want := []map[string]any{{"role": "user", "content": "time"}, {"type": "function_call", "call_id": "context-1", "name": "current_time", "arguments": "{}"}, {"type": "function_call_output", "call_id": "context-1", "output": "12:00"}}
		if !reflect.DeepEqual(body.Input, want) {
			t.Errorf("input=%v", body.Input)
		}
		w.Write([]byte(responseAnswer))
	}))
	defer s.Close()
	c := Client{API: "responses", BaseURL: s.URL}
	messages := []Message{{Role: "user", Content: "time"}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "context-1", Type: "function", Function: FunctionCall{Name: "current_time", Arguments: "{}"}}}}, {Role: "tool", ToolCallID: "context-1", Content: "12:00"}}
	if _, err := c.Chat(t.Context(), messages, nil); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesRejectsUnusableOutput(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"malformed", `{`, "invalid LLM response"},
		{"no output", `{"status":"completed","output":[]}`, "no answer"},
		{"reasoning only", `{"status":"completed","output":[{"type":"reasoning","summary":[]}]}`, "no answer"},
		{"wrong role", `{"status":"completed","output":[{"type":"message","role":"user","content":[{"type":"output_text","text":"bad"}]}]}`, "no answer"},
		{"empty text", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":" "}]}]}`, "no answer"},
		{"refusal", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"secret refusal"}]}]}`, "refused"},
		{"failed", `{"status":"failed","error":{"message":"secret error"}}`, "failed"},
		{"error with completed status", `{"status":"completed","error":{"message":"secret error"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"bad"}]}]}`, "failed"},
		{"cancelled", `{"status":"cancelled"}`, "not completed"},
		{"missing status", `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"bad"}]}]}`, "not completed"},
		{"in progress", `{"status":"in_progress"}`, "not completed"},
		{"token budget", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","call_id":"call_1","name":"action","arguments":"{}"}]}`, "token budget"},
		{"filtered", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, "incomplete"},
		{"partial message", `{"status":"completed","output":[{"type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"bad"}]}]}`, "incomplete"},
		{"partial call", `{"status":"completed","output":[{"type":"function_call","status":"in_progress","call_id":"call_1","name":"action","arguments":"{}"}]}`, "incomplete"},
		{"missing call id", `{"status":"completed","output":[{"type":"function_call","id":"fc_1","name":"action","arguments":"{}"}]}`, "invalid LLM function call"},
		{"bad arguments", `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"action","arguments":"{"}]}`, "invalid LLM function call"},
		{"missing call name", `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","arguments":"{}"}]}`, "invalid LLM function call"},
		{"null output item", `{"status":"completed","output":[null]}`, "invalid LLM response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
			defer s.Close()
			c := Client{API: "responses", BaseURL: s.URL}
			msg, err := c.Chat(t.Context(), nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") || msg.Content != "" || len(msg.ToolCalls) > 0 {
				t.Fatalf("message=%+v error=%v want=%q", msg, err, tc.want)
			}
		})
	}
}

func TestResponsesCompleteRejectsFunctionCall(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"action","arguments":"{}"}]}`))
	}))
	defer s.Close()
	c := Client{API: "responses", BaseURL: s.URL}
	if _, err := c.Complete(t.Context(), nil, false); err == nil || !strings.Contains(err.Error(), "unexpected tool call") {
		t.Fatalf("error=%v", err)
	}
}

func TestResponsesTransportErrorsAndCancellation(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(250 * time.Millisecond):
			}
		}))
		defer s.Close()
		c := Client{API: "responses", BaseURL: s.URL}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		if _, err := c.Complete(ctx, nil, false); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("HTTP", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "secret upstream error", http.StatusBadRequest)
		}))
		defer s.Close()
		c := Client{API: "responses", BaseURL: s.URL}
		if _, err := c.Complete(t.Context(), nil, false); err == nil || err.Error() != "LLM returned HTTP 400" {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("redacted transport", func(t *testing.T) {
		endpoint := "https://example.invalid/private-endpoint"
		c := Client{API: "responses", BaseURL: endpoint, HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret transport") })}}
		if _, err := c.Complete(t.Context(), nil, false); err == nil || strings.Contains(err.Error(), endpoint) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestResponsesConcurrentCallsKeepOutputLocal(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct {
				Content string `json:"content"`
			} `json:"input"`
		}
		if err := json.UnmarshalRead(r.Body, &body); err != nil || len(body.Input) != 1 {
			t.Errorf("bad input: %+v error=%v", body.Input, err)
			http.Error(w, "bad input", 400)
			return
		}
		encoded, _ := json.Marshal(body.Input[0].Content)
		w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + string(encoded) + `}]}]}`))
	}))
	defer s.Close()
	c := Client{API: "responses", BaseURL: s.URL}
	var wg sync.WaitGroup
	for _, input := range []string{"first", "second"} {
		wg.Go(func() {
			msg, err := c.Chat(t.Context(), []Message{{Role: "user", Content: input}}, nil)
			if err != nil || msg.Content != input {
				t.Errorf("input=%s message=%+v error=%v", input, msg, err)
			}
		})
	}
	wg.Wait()
}

func TestClientRejectsUnknownAPIWithoutRequest(t *testing.T) {
	c := Client{API: "unknown", HTTP: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("request sent with unknown API")
		return nil, errors.New("unexpected request")
	})}}
	if _, err := c.Complete(t.Context(), nil, false); err == nil || !strings.Contains(err.Error(), "unsupported LLM API") {
		t.Fatalf("error=%v", err)
	}
}
