package workflow

// State is the mutable deterministic execution state for one protocol run.
type State struct {
	TraceID     string
	RoomID      string
	SenderMXID  string
	ProtocolID  string
	Input       map[string]interface{}
	Values      map[string]interface{}
	StepOutputs map[string]interface{}
	FinalOutput string
}

// NewStateFromExecutionContext builds State from an ExecutionContext.
func NewStateFromExecutionContext(ctx *ExecutionContext) *State {
	state := &State{
		Values:      make(map[string]interface{}),
		StepOutputs: make(map[string]interface{}),
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
