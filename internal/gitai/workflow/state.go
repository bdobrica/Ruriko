package workflow

import "time"

// TraceMetadata captures deterministic per-run metadata for workflow audit and diagnostics.
type TraceMetadata struct {
	TraceID        string
	ProtocolID     string
	RoomID         string
	SenderMXID     string
	StartedAt      time.Time
	CompletedAt    time.Time
	DurationMS     int64
	Status         string
	Error          string
	ToolCalls      int
	StepsTotal     int
	StepsCompleted int
}

// FinalOutput captures the terminal workflow result contract.
type FinalOutput struct {
	StepKey   string
	StepIndex int
	StepType  string
	Value     interface{}
}

// State is the mutable deterministic execution state for one protocol run.
type State struct {
	TraceID     string
	RoomID      string
	SenderMXID  string
	ProtocolID  string
	Input       map[string]interface{}
	Values      map[string]interface{}
	Schemas     map[string]interface{}
	StepOutputs map[string]map[string]interface{}
	FinalOutput string
	Final       FinalOutput
	Trace       TraceMetadata
}

// NewStateFromExecutionContext builds State from an ExecutionContext.
func NewStateFromExecutionContext(ctx *ExecutionContext) *State {
	state := &State{
		Values:      make(map[string]interface{}),
		StepOutputs: make(map[string]map[string]interface{}),
	}
	if ctx == nil {
		return state
	}
	state.TraceID = ctx.TraceID
	state.RoomID = ctx.RoomID
	state.SenderMXID = ctx.SenderMXID
	state.ProtocolID = ctx.ProtocolID
	state.Input = cloneMap(ctx.Input)
	state.Values = cloneMap(ctx.State)
	state.Schemas = cloneSchemaDefinitions(ctx.SchemaDefinitions)
	state.Trace = TraceMetadata{
		TraceID:    ctx.TraceID,
		ProtocolID: ctx.ProtocolID,
		RoomID:     ctx.RoomID,
		SenderMXID: ctx.SenderMXID,
		StartedAt:  time.Now().UTC(),
		Status:     "running",
	}
	return state
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return make(map[string]interface{})
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
