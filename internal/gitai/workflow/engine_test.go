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
	planResult string
}

func (f *fakeDispatcher) DispatchTool(_ context.Context, _ string, _ string, name string, args map[string]interface{}) (string, error) {
	f.tools = append(f.tools, name)
	f.args = append(f.args, args)
	return "tool-result", nil
}

func (f *fakeDispatcher) Summarize(_ context.Context, prompt string, _ *State) (string, error) {
	f.summarized = append(f.summarized, prompt)
	if f.planResult != "" {
		return f.planResult, nil
	}
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

func TestEngineExecuteProtocol_SummarizeStep_StructuredSchemaParsesJSON(t *testing.T) {
	dispatcher := &fakeDispatcher{
		planResult: `{"run_id":1,"summary":"ok","headlines":["h1"],"material":true}`,
	}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:            "summarize",
				Prompt:          "Summarize {{input.topic}}",
				OutputSchemaRef: "kumoNewsResponse",
			},
		},
	}

	match := &InboundProtocolMatch{
		Protocol: protocol,
		Payload:  map[string]interface{}{"topic": "OpenAI"},
		SchemaDefinitions: map[string]interface{}{
			"kumoNewsResponse": map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"run_id", "summary", "headlines", "material"},
				"properties": map[string]interface{}{
					"run_id":    map[string]interface{}{"type": "integer"},
					"summary":   map[string]interface{}{"type": "string"},
					"headlines": map[string]interface{}{"type": "array"},
					"material":  map[string]interface{}{"type": "boolean"},
				},
			},
		},
	}

	result, _, err := engine.ExecuteProtocol(context.Background(), protocol, NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", match))
	if err != nil {
		t.Fatalf("ExecuteProtocol() unexpected error: %v", err)
	}
	if !strings.Contains(result, "map[") {
		t.Fatalf("expected structured summarize output stringification, got: %q", result)
	}
}

func TestEngineExecuteProtocol_SummarizeStep_StructuredSchemaRejectsNonJSON(t *testing.T) {
	dispatcher := &fakeDispatcher{planResult: "not json"}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:            "summarize",
				Prompt:          "Summarize {{input.topic}}",
				OutputSchemaRef: "kumoNewsResponse",
				Retries:         1,
			},
		},
	}

	match := &InboundProtocolMatch{
		Protocol: protocol,
		Payload:  map[string]interface{}{"topic": "OpenAI"},
		SchemaDefinitions: map[string]interface{}{
			"kumoNewsResponse": map[string]interface{}{
				"type": "object",
			},
		},
	}

	_, _, err := engine.ExecuteProtocol(context.Background(), protocol, NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", match))
	if err == nil {
		t.Fatal("ExecuteProtocol() expected summarize parse error")
	}
	if !strings.Contains(err.Error(), "step summarize failed after") {
		t.Fatalf("unexpected error: %v", err)
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

func TestEngineExecuteProtocol_ForEach_IterationSchemaHooks_Succeed(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:                      "for_each",
				ItemsExpr:                 "{{input.tickers}}",
				ItemVar:                   "ticker",
				MaxIterations:             3,
				ForEachResultSchemaRef:    "iterResult",
				ForEachIterationSchemaRef: "iterContract",
				Steps: []gosutospec.WorkflowProtocolStep{
					{Type: "persist", PersistKey: "last_ticker", PersistValue: "{{state.ticker}}"},
				},
			},
		},
	}

	state := NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"tickers": []interface{}{"AAPL", "MSFT"},
		},
		SchemaDefinitions: map[string]interface{}{
			"iterResult": map[string]interface{}{"type": "string"},
			"iterContract": map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"index", "item", "outputs", "result"},
				"properties": map[string]interface{}{
					"item":    map[string]interface{}{"type": "string"},
					"outputs": map[string]interface{}{"type": "object"},
					"result":  map[string]interface{}{"type": "string"},
				},
			},
		},
	}))

	_, _, err := runner.Run(context.Background(), protocol, state)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestEngineExecuteProtocol_ForEach_IterationResultSchemaHook_Fails(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:                   "for_each",
				ItemsExpr:              "{{input.tickers}}",
				ItemVar:                "ticker",
				MaxIterations:          3,
				ForEachResultSchemaRef: "iterResultObject",
				Steps: []gosutospec.WorkflowProtocolStep{
					{Type: "persist", PersistKey: "last_ticker", PersistValue: "{{state.ticker}}"},
				},
			},
		},
	}

	state := NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"tickers": []interface{}{"AAPL", "MSFT"},
		},
		SchemaDefinitions: map[string]interface{}{
			"iterResultObject": map[string]interface{}{"type": "object"},
		},
	}))

	_, _, err := runner.Run(context.Background(), protocol, state)
	if err == nil {
		t.Fatal("Run() expected for_each iteration result schema validation error")
	}
	if !HasCode(err, CodeSchemaValidation) {
		t.Fatalf("expected schema validation error code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "result schema validation failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEngineExecuteProtocol_PlanStep_SchemaBoundStructuredOutput(t *testing.T) {
	dispatcher := &fakeDispatcher{planResult: `{"items":[{"query":"AAPL earnings"},{"query":"MSFT guidance"}]}`}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:            "plan",
				Prompt:          "Create a search plan for {{input.topic}}",
				OutputSchemaRef: "searchPlan",
			},
			{
				Type:          "for_each",
				ItemsExpr:     "{{steps.step_0.items}}",
				ItemVar:       "plan_item",
				MaxIterations: 5,
				Steps: []gosutospec.WorkflowProtocolStep{
					{Type: "persist", PersistKey: "last_query", PersistValue: "{{state.plan_item.query}}"},
				},
			},
		},
	}

	state := NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"topic": "earnings season",
		},
		SchemaDefinitions: map[string]interface{}{
			"searchPlan": map[string]interface{}{
				"type":     "object",
				"required": []interface{}{"items"},
				"properties": map[string]interface{}{
					"items": map[string]interface{}{"type": "array"},
				},
			},
		},
	}))

	_, toolCalls, err := runner.Run(context.Background(), protocol, state)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if toolCalls != 0 {
		t.Fatalf("Run() toolCalls = %d, want 0", toolCalls)
	}
	planOutput, ok := state.StepOutputs["step_0"]["output"].(map[string]interface{})
	if !ok {
		t.Fatalf("step_0 output type = %T, want map[string]interface{}", state.StepOutputs["step_0"]["output"])
	}
	items, ok := planOutput["items"].([]interface{})
	if !ok || len(items) != 2 {
		t.Fatalf("plan items = %#v, want 2 entries", planOutput["items"])
	}
}

func TestEngineExecuteProtocol_PlanStep_RejectsNonJSONOutput(t *testing.T) {
	dispatcher := &fakeDispatcher{planResult: "this is not json"}
	engine := NewEngine(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:            "plan",
				Prompt:          "Create plan",
				OutputSchemaRef: "searchPlan",
			},
		},
	}

	_, _, err := engine.ExecuteProtocol(context.Background(), protocol, NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload:  map[string]interface{}{"topic": "news"},
		SchemaDefinitions: map[string]interface{}{
			"searchPlan": map[string]interface{}{"type": "object"},
		},
	}))
	if err == nil {
		t.Fatal("ExecuteProtocol() expected JSON parsing error for plan step")
	}
	if !HasCode(err, CodeSchemaValidation) {
		t.Fatalf("expected schema validation error code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "step plan failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEngineExecuteProtocol_Collect_ModeItemAndFlatten(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:          "for_each",
				ItemsExpr:     "{{input.buckets}}",
				ItemVar:       "bucket",
				MaxIterations: 5,
				Steps: []gosutospec.WorkflowProtocolStep{
					{Type: "persist", PersistKey: "last_bucket", PersistValue: "{{state.bucket}}"},
				},
			},
			{
				Type:           "collect",
				CollectFrom:    "{{steps.step_0}}",
				CollectMode:    "item",
				CollectFlatten: true,
			},
		},
	}

	state := NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"buckets": []interface{}{
				[]interface{}{"a", "b"},
				[]interface{}{"c"},
			},
		},
	}))

	result, _, err := runner.Run(context.Background(), protocol, state)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "[a b c]" {
		t.Fatalf("Run() result = %q, want [a b c]", result)
	}
}

func TestEngineExecuteProtocol_MaxOutputItems_ArrayOutputRejectedWhenExceeded(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:           "collect",
				CollectFrom:    "{{input.items}}",
				CollectMode:    "entry",
				MaxOutputItems: 1,
			},
		},
	}

	_, _, err := runner.Run(context.Background(), protocol, NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"items": []interface{}{"a", "b"},
		},
	})))
	if err == nil {
		t.Fatal("Run() expected maxOutputItems error")
	}
	if !HasCode(err, CodeSchemaValidation) {
		t.Fatalf("expected schema validation error code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "exceeds maxOutputItems") {
		t.Fatalf("expected maxOutputItems error, got: %v", err)
	}
}

func TestEngineExecuteProtocol_MaxOutputItems_NonArrayOutputIgnored(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:           "persist",
				PersistKey:     "last_topic",
				PersistValue:   "{{input.topic}}",
				MaxOutputItems: 1,
			},
		},
	}

	result, _, err := runner.Run(context.Background(), protocol, NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"topic": "earnings",
		},
	})))
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if result != "earnings" {
		t.Fatalf("Run() result = %q, want earnings", result)
	}
}

func TestEngineExecuteProtocol_Collect_ModeOutputs(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	runner := NewRunner(dispatcher)

	protocol := gosutospec.WorkflowProtocol{
		ID: "kumo.news.request.v1",
		Steps: []gosutospec.WorkflowProtocolStep{
			{
				Type:          "for_each",
				ItemsExpr:     "{{input.tickers}}",
				ItemVar:       "ticker",
				MaxIterations: 5,
				Steps: []gosutospec.WorkflowProtocolStep{
					{Type: "persist", PersistKey: "last_ticker", PersistValue: "{{state.ticker}}"},
				},
			},
			{
				Type:        "collect",
				CollectFrom: "{{steps.step_0}}",
				CollectMode: "outputs",
			},
		},
	}

	state := NewStateFromExecutionContext(NewExecutionContext("trace-1", "!room:example.com", "@peer:example.com", &InboundProtocolMatch{
		Protocol: protocol,
		Payload: map[string]interface{}{
			"tickers": []interface{}{"AAPL", "MSFT"},
		},
	}))

	_, _, err := runner.Run(context.Background(), protocol, state)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output, ok := state.StepOutputs["step_1"]["output"].([]interface{})
	if !ok || len(output) != 2 {
		t.Fatalf("collect output = %#v, want 2 entries", state.StepOutputs["step_1"]["output"])
	}
}
