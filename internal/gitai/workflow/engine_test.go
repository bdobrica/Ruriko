package workflow

import (
	"context"
	"strings"
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
	if result != "summary-result" {
		t.Fatalf("ExecuteProtocol() result = %q, want %q", result, "summary-result")
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

func TestEngineExecuteProtocol_TracksStructuredContracts(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{Type: "parse_input"},
			{Type: "persist", PersistKey: "last_run_id", PersistValue: "{{input.run_id}}"},
		},
	}

	state := NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"run_id": float64(42),
		},
	}))

	result, toolCalls, err := runner.Run(context.Background(), protocol, state)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "42" {
		t.Fatalf("Run() result = %q, want %q", result, "42")
	}
	if toolCalls != 0 {
		t.Fatalf("Run() toolCalls = %d, want 0", toolCalls)
	}
	if state.Trace.Status != "success" {
		t.Fatalf("trace status = %q, want success", state.Trace.Status)
	}
	if state.Trace.TraceID != "trace-1" {
		t.Fatalf("trace id = %q, want trace-1", state.Trace.TraceID)
	}
	if state.Trace.StepsCompleted != 2 {
		t.Fatalf("steps completed = %d, want 2", state.Trace.StepsCompleted)
	}
	if len(state.StepOutputs) != 2 {
		t.Fatalf("step outputs = %d, want 2", len(state.StepOutputs))
	}
	if got := state.Final.StepType; got != "persist" {
		t.Fatalf("final step type = %q, want persist", got)
	}
	if got := state.Final.Value; got != float64(42) {
		t.Fatalf("final value = %#v, want 42", got)
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

func TestEngineExecuteProtocol_UnresolvedInterpolationFailsClosed(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type: "tool",
				Tool: "brave__search",
				ArgsTemplate: map[string]interface{}{
					"query": "ticker={{input.missing_field}}",
				},
			},
		},
	}

	_, _, err := engine.ExecuteProtocol(context.Background(), protocol, NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload:  map[string]interface{}{"ticker": "AAPL"},
	}))
	if err == nil {
		t.Fatal("ExecuteProtocol() expected interpolation error")
	}
	if !strings.Contains(err.Error(), "unresolved template token") {
		t.Fatalf("expected unresolved token error, got: %v", err)
	}
}

func TestEngineExecuteProtocol_OutputSchemaRefValidation_Succeeds(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:            "persist",
				PersistKey:      "last_run_id",
				PersistValue:    "{{input.run_id}}",
				OutputSchemaRef: "runIdNumber",
			},
		},
	}

	match := &InboundProtocolMatch{
		Protocol: protocol,
		Payload:  map[string]interface{}{"run_id": float64(42)},
		SchemaDefinitions: map[string]interface{}{
			"runIdNumber": map[string]interface{}{"type": "number"},
		},
	}

	result, _, err := engine.ExecuteProtocol(context.Background(), protocol, NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", match))
	if err != nil {
		t.Fatalf("ExecuteProtocol() unexpected error: %v", err)
	}
	if result != "42" {
		t.Fatalf("ExecuteProtocol() result = %q, want 42", result)
	}
}

func TestEngineExecuteProtocol_OutputSchemaRefValidation_Fails(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:            "persist",
				PersistKey:      "last_run_id",
				PersistValue:    "{{input.run_id}}",
				OutputSchemaRef: "runIdString",
			},
		},
	}

	match := &InboundProtocolMatch{
		Protocol: protocol,
		Payload:  map[string]interface{}{"run_id": float64(42)},
		SchemaDefinitions: map[string]interface{}{
			"runIdString": map[string]interface{}{"type": "string"},
		},
	}

	_, _, err := engine.ExecuteProtocol(context.Background(), protocol, NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", match))
	if err == nil {
		t.Fatal("ExecuteProtocol() expected schema validation error")
	}
	if !HasCode(err, CodeSchemaValidation) {
		t.Fatalf("expected schema validation error code, got: %v", err)
	}
}

func TestEngineExecuteProtocol_ForEachCollect_BoundedIteration(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{Type: "parse_input"},
			{
				Type:          "for_each",
				ItemsExpr:     "{{input.tickers}}",
				ItemVar:       "ticker",
				MaxIterations: 3,
				Steps: []gosutospec.WorkflowProtocolStep{
					{
						Type: "tool",
						Tool: "brave__search",
						ArgsTemplate: map[string]interface{}{
							"query": "ticker={{state.ticker}}",
						},
					},
				},
			},
			{
				Type:        "collect",
				CollectFrom: "{{steps.step_1}}",
			},
		},
	}

	state := NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"tickers": []interface{}{"AAPL", "MSFT"},
		},
	}))

	result, toolCalls, err := runner.Run(context.Background(), protocol, state)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "[tool-result tool-result]" {
		t.Fatalf("Run() result = %q, want [tool-result tool-result]", result)
	}
	if toolCalls != 2 {
		t.Fatalf("Run() toolCalls = %d, want 2", toolCalls)
	}
	if len(dispatcher.tools) != 2 {
		t.Fatalf("tool dispatch count = %d, want 2", len(dispatcher.tools))
	}
	if got := dispatcher.args[0]["query"]; got != "ticker=AAPL" {
		t.Fatalf("first query = %#v, want ticker=AAPL", got)
	}
	if got := dispatcher.args[1]["query"]; got != "ticker=MSFT" {
		t.Fatalf("second query = %#v, want ticker=MSFT", got)
	}
}

func TestEngineExecuteProtocol_ForEachCollect_MaxIterationsExceeded(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:          "for_each",
				ItemsExpr:     "{{input.tickers}}",
				ItemVar:       "ticker",
				MaxIterations: 1,
				Steps: []gosutospec.WorkflowProtocolStep{
					{Type: "persist", PersistKey: "last_ticker", PersistValue: "{{state.ticker}}"},
				},
			},
		},
	}

	_, _, err := runner.Run(context.Background(), protocol, NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"tickers": []interface{}{"AAPL", "MSFT"},
		},
	})))
	if err == nil {
		t.Fatal("Run() expected maxIterations error")
	}
	if !strings.Contains(err.Error(), "exceeds maxIterations") {
		t.Fatalf("expected maxIterations error, got: %v", err)
	}
}
