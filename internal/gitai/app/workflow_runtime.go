package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/workflow"
)

func (a *App) getWorkflowEngine() *workflow.Engine {
	if a == nil {
		return nil
	}
	a.workflowEngineMu.Do(func() {
		a.workflowEngine = workflow.NewEngine(&workflowDispatcher{app: a})
	})
	return a.workflowEngine
}

func (a *App) runWorkflowTurn(ctx context.Context, roomID, sender string, match *workflow.InboundProtocolMatch) (string, int, error) {
	if match == nil {
		return "", 0, fmt.Errorf("workflow match is required")
	}
	engine := a.getWorkflowEngine()
	if engine == nil {
		return "", 0, fmt.Errorf("workflow engine is not available")
	}

	execCtx := workflow.NewExecutionContext(trace.FromContext(ctx), roomID, sender, match)
	result, toolCalls, err := engine.ExecuteProtocol(ctx, match.Protocol, execCtx)
	if err != nil {
		return "", toolCalls, err
	}
	return strings.TrimSpace(result), toolCalls, nil
}

type workflowDispatcher struct {
	app *App
}

func (d *workflowDispatcher) DispatchTool(ctx context.Context, caller, sender, name string, args map[string]interface{}) (string, error) {
	if d == nil || d.app == nil {
		return "", fmt.Errorf("workflow dispatcher app is not configured")
	}
	return d.app.DispatchToolCall(ctx, ToolDispatchRequest{
		Caller: caller,
		Sender: sender,
		Name:   name,
		Args:   args,
	})
}

func (d *workflowDispatcher) Summarize(ctx context.Context, prompt string, _ *workflow.State) (string, error) {
	if d == nil || d.app == nil {
		return "", fmt.Errorf("workflow dispatcher app is not configured")
	}
	cfg := d.app.gosutoLdr.Config()
	if cfg == nil {
		return "", fmt.Errorf("no Gosuto config loaded")
	}
	prov := d.app.provider()
	if prov == nil {
		return "", fmt.Errorf("LLM provider not configured")
	}
	if err := d.app.enforceLLMCallHardLimit(); err != nil {
		return "", err
	}

	systemPrompt := buildSystemPrompt(cfg, buildMessagingTargets(cfg), "")
	resp, err := prov.Complete(ctx, llm.CompletionRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: prompt},
		},
		MaxTokens: cfg.Limits.MaxTokensPerRequest,
	})
	if err != nil {
		return "", fmt.Errorf("workflow summarize failed: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("workflow summarize returned no response")
	}
	return resp.Message.Content, nil
}
