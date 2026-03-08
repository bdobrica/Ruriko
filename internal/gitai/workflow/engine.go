package workflow

import (
	"context"
	"fmt"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

// Engine executes one matched workflow protocol using a deterministic runner.
type Engine struct {
	runner *Runner
}

// NewEngine creates a workflow execution engine.
func NewEngine(dispatcher Dispatcher) *Engine {
	return &Engine{runner: NewRunner(dispatcher)}
}

// ExecuteProtocol runs the protocol steps from the provided execution context.
func (e *Engine) ExecuteProtocol(ctx context.Context, protocol gosutospec.WorkflowProtocol, execCtx *ExecutionContext) (string, int, error) {
	if e == nil || e.runner == nil {
		return "", 0, fmt.Errorf("workflow engine is not configured")
	}
	state := NewStateFromExecutionContext(execCtx)
	return e.runner.Run(ctx, protocol, state)
}
