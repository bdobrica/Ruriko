package workflow

import (
	"context"
	"testing"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

type fakeDispatcher struct {
	tools      []string
	args       []map[string]interface{}
	summarized []string
}

func (f *fakeDispatcher) DispatchTool(_ context.Context, _ string, _ string, name string, args map[string]interface{}) (string, error) {
	f.tools = append(f.tools, name)
	f.args = append(f.args, args)
	return "tool-result", nil
}

func (f *fakeDispatcher) Summarize(_ context.Context, prompt string, _ *State) (string, error) {
	f.summarized = append(f.summarized, prompt)
	return "summary-result", nil
}

func TestEngineExecuteProtocol_ExecutesStepsDeterministically(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{Type: "parse_input"},
			{
				Type: "tool",
				Tool: "brave__search",
				ArgsTemplate: map[string]interface{}{
					"query": "ticker={{input.ticker}}",
				},
			},
			{Type: "summarize", Prompt: "Summarize {{steps.step_1}}"},
			{Type: "send_message", TargetAlias: "user", PayloadTemplate: "{{steps.step_2}}"},
			{Type: "persist", PersistKey: "last_summary", PersistValue: "{{steps.step_2}}"},
		},
	}

	execCtx := NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"ticker": "AAPL",
		},
	})

	result, toolCalls, err := engine.ExecuteProtocol(context.Background(), protocol, execCtx)
	if err != nil {
		t.Fatalf("ExecuteProtocol() error = %v", err)
	}
	if result != "tool-result" {
		t.Fatalf("ExecuteProtocol() result = %q, want %q", result, "tool-result")
	}
	if toolCalls != 2 {
		t.Fatalf("ExecuteProtocol() toolCalls = %d, want 2", toolCalls)
	}
	if len(dispatcher.tools) != 2 {
		t.Fatalf("dispatch count = %d, want 2", len(dispatcher.tools))
	}
	if dispatcher.tools[0] != "brave__search" {
		t.Fatalf("first tool = %q, want brave__search", dispatcher.tools[0])
	}
	if dispatcher.tools[1] != "matrix.send_message" {
		t.Fatalf("second tool = %q, want matrix.send_message", dispatcher.tools[1])
	}
	if got := dispatcher.args[0]["query"]; got != "ticker=AAPL" {
		t.Fatalf("tool query arg = %#v, want ticker=AAPL", got)
	}
	if len(dispatcher.summarized) != 1 {
		t.Fatalf("summarize calls = %d, want 1", len(dispatcher.summarized))
	}
}

func TestEngineExecuteProtocol_BranchReturnsError(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{Type: "branch", BranchExpr: "state.foo == 'bar'"},
		},
	}

	_, _, err := engine.ExecuteProtocol(context.Background(), protocol, NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{Protocol: protocol, Payload: map[string]interface{}{}}))
	if err == nil {
		t.Fatal("ExecuteProtocol() expected error for unimplemented branch step")
	}
}
