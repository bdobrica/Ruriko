package workflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

const (
	workflowCaller = "workflow"
	stepPrefix     = "step_"
)

// Dispatcher is the deterministic boundary used by the workflow runtime.
// Implementations must enforce policy/approval checks for tool execution.
type Dispatcher interface {
	DispatchTool(ctx context.Context, caller, sender, name string, args map[string]interface{}) (string, error)
	Summarize(ctx context.Context, prompt string, state *State) (string, error)
}

// Runner executes protocol steps against workflow state.
type Runner struct {
	dispatcher Dispatcher
}

// NewRunner constructs a step runner.
func NewRunner(dispatcher Dispatcher) *Runner {
	return &Runner{dispatcher: dispatcher}
}

// Run executes all protocol steps deterministically.
func (r *Runner) Run(ctx context.Context, protocol gosutospec.WorkflowProtocol, state *State) (string, int, error) {
	if state == nil {
		state = &State{Values: make(map[string]interface{}), StepOutputs: make(map[string]interface{})}
	}
	if r == nil || r.dispatcher == nil {
		return "", 0, fmt.Errorf("workflow runner dispatcher is not configured")
	}

	toolCalls := 0
	for i, step := range protocol.Steps {
		retries := protocol.Retries
		if step.Retries > 0 {
			retries = step.Retries
		}

		stepResult := ""
		err := RunStepWithRetryThenRefuse(protocol.ID, step.Type, retries, func() error {
			result, callCount, err := r.runStep(ctx, step, state)
			if err != nil {
				return err
			}
			stepResult = result
			toolCalls += callCount
			return nil
		})
		if err != nil {
			return "", toolCalls, fmt.Errorf("workflow step %d (%s) failed: %w", i, step.Type, err)
		}

		stepKey := stepPrefix + strconv.Itoa(i)
		if stepResult != "" {
			state.StepOutputs[stepKey] = stepResult
			state.Values[stepKey] = stepResult
			state.FinalOutput = stepResult
		}
	}

	return state.FinalOutput, toolCalls, nil
}

func (r *Runner) runStep(ctx context.Context, step gosutospec.WorkflowProtocolStep, state *State) (string, int, error) {
	switch strings.TrimSpace(step.Type) {
	case "parse_input":
		state.Values["parsed_input"] = cloneMap(state.Input)
		return "", 0, nil

	case "tool":
		toolName := strings.TrimSpace(step.Tool)
		if toolName == "" {
			return "", 0, fmt.Errorf("workflow tool step requires tool")
		}
		argsAny, err := interpolateValue(step.ArgsTemplate, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate tool args: %w", err)
		}
		args, ok := argsAny.(map[string]interface{})
		if !ok {
			return "", 0, fmt.Errorf("tool args template must resolve to an object")
		}
		result, err := r.dispatcher.DispatchTool(ctx, workflowCaller, state.SenderMXID, toolName, args)
		if err != nil {
			return "", 1, err
		}
		return result, 1, nil

	case "summarize":
		promptAny, err := interpolateValue(step.Prompt, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate summarize prompt: %w", err)
		}
		prompt, ok := promptAny.(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return "", 0, fmt.Errorf("summarize step requires non-empty prompt")
		}
		result, err := r.dispatcher.Summarize(ctx, prompt, state)
		if err != nil {
			return "", 0, err
		}
		return result, 0, nil

	case "send_message":
		targetAny, err := interpolateValue(step.TargetAlias, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate send_message target: %w", err)
		}
		target, ok := targetAny.(string)
		if !ok || strings.TrimSpace(target) == "" {
			return "", 0, fmt.Errorf("send_message step requires targetAlias")
		}
		msgAny, err := interpolateValue(step.PayloadTemplate, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate send_message payload: %w", err)
		}
		message, ok := msgAny.(string)
		if !ok || strings.TrimSpace(message) == "" {
			return "", 0, fmt.Errorf("send_message step requires payloadTemplate")
		}
		result, err := r.dispatcher.DispatchTool(ctx, workflowCaller, state.SenderMXID, "matrix.send_message", map[string]interface{}{
			"target":  target,
			"message": message,
		})
		if err != nil {
			return "", 1, err
		}
		return result, 1, nil

	case "persist":
		if strings.TrimSpace(step.PersistKey) == "" {
			return "", 0, fmt.Errorf("persist step requires persistKey")
		}
		valueAny, err := interpolateValue(step.PersistValue, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate persist value: %w", err)
		}
		state.Values[step.PersistKey] = valueAny
		return "", 0, nil

	case "branch":
		return "", 0, fmt.Errorf("branch steps are not implemented yet")

	default:
		return "", 0, fmt.Errorf("unsupported workflow step type %q", step.Type)
	}
}

func interpolateValue(value interface{}, state *State) (interface{}, error) {
	switch v := value.(type) {
	case string:
		return interpolateString(v, state), nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			rendered, err := interpolateValue(item, state)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			rendered, err := interpolateValue(item, state)
			if err != nil {
				return nil, err
			}
			out = append(out, rendered)
		}
		return out, nil
	default:
		return value, nil
	}
}

func interpolateString(raw string, state *State) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.Contains(trimmed, "{{") || !strings.Contains(trimmed, "}}") {
		return raw
	}

	out := raw
	for {
		start := strings.Index(out, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(out[start+2:], "}}")
		if end < 0 {
			break
		}
		end += start + 2

		token := strings.TrimSpace(out[start+2 : end])
		resolved := resolvePathToken(token, state)
		replacement := ""
		if resolved != nil {
			replacement = fmt.Sprintf("%v", resolved)
		}
		out = out[:start] + replacement + out[end+2:]
	}
	return out
}

func resolvePathToken(token string, state *State) interface{} {
	if state == nil || token == "" {
		return nil
	}
	parts := strings.Split(token, ".")
	if len(parts) == 0 {
		return nil
	}

	switch parts[0] {
	case "input":
		return resolvePath(state.Input, parts[1:])
	case "state":
		return resolvePath(state.Values, parts[1:])
	case "steps":
		return resolvePath(state.StepOutputs, parts[1:])
	default:
		return nil
	}
}

func resolvePath(root interface{}, path []string) interface{} {
	current := root
	if len(path) == 0 {
		return current
	}
	for _, p := range path {
		obj, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = obj[p]
		if !ok {
			return nil
		}
	}
	return current
}
