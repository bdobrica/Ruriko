package workflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

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
		state = &State{Values: make(map[string]interface{}), StepOutputs: make(map[string]map[string]interface{})}
	}
	if r == nil || r.dispatcher == nil {
		return "", 0, fmt.Errorf("workflow runner dispatcher is not configured")
	}

	if state.Trace.StartedAt.IsZero() {
		state.Trace.StartedAt = time.Now().UTC()
	}
	state.Trace.Status = "running"
	state.Trace.ProtocolID = protocol.ID
	state.Trace.StepsTotal = len(protocol.Steps)
	state.Trace.StepsCompleted = 0

	toolCalls := 0
	for i, step := range protocol.Steps {
		startedAt := time.Now().UTC()
		retries := protocol.Retries
		if step.Retries > 0 {
			retries = step.Retries
		}

		var stepResult interface{}
		stepToolCalls := 0
		err := RunStepWithRetryThenRefuse(protocol.ID, step.Type, retries, func() error {
			result, callCount, err := r.runStep(ctx, step, state)
			if err != nil {
				return err
			}
			if err := validateStepOutputSchema(protocol.ID, step, result, state); err != nil {
				return err
			}
			stepResult = result
			stepToolCalls += callCount
			toolCalls += callCount
			return nil
		})

		stepKey := stepPrefix + strconv.Itoa(i)
		completedAt := time.Now().UTC()
		stepContract := map[string]interface{}{
			"stepKey":       stepKey,
			"stepIndex":     i,
			"stepType":      step.Type,
			"startedAt":     startedAt.Format(time.RFC3339Nano),
			"completedAt":   completedAt.Format(time.RFC3339Nano),
			"durationMS":    completedAt.Sub(startedAt).Milliseconds(),
			"toolCalls":     stepToolCalls,
			"retryBudget":   retries,
			"status":        "success",
			"output":        stepResult,
			"outputPresent": outputPresent(stepResult),
		}

		if err != nil {
			stepContract["status"] = "error"
			stepContract["error"] = err.Error()
			state.StepOutputs[stepKey] = stepContract
			state.Trace.CompletedAt = completedAt
			state.Trace.DurationMS = completedAt.Sub(state.Trace.StartedAt).Milliseconds()
			state.Trace.Status = "error"
			state.Trace.Error = err.Error()
			state.Trace.ToolCalls = toolCalls
			state.Trace.StepsCompleted = i
			return "", toolCalls, fmt.Errorf("workflow step %d (%s) failed: %w", i, step.Type, err)
		}
		state.StepOutputs[stepKey] = stepContract

		if outputPresent(stepResult) {
			state.Values[stepKey] = stepResult
			state.FinalOutput = fmt.Sprintf("%v", stepResult)
			state.Final = FinalOutput{
				StepKey:   stepKey,
				StepIndex: i,
				StepType:  step.Type,
				Value:     stepResult,
			}
		}
		state.Trace.StepsCompleted = i + 1
	}

	completedAt := time.Now().UTC()
	state.Trace.CompletedAt = completedAt
	state.Trace.DurationMS = completedAt.Sub(state.Trace.StartedAt).Milliseconds()
	state.Trace.Status = "success"
	state.Trace.ToolCalls = toolCalls

	return state.FinalOutput, toolCalls, nil
}

func (r *Runner) runStep(ctx context.Context, step gosutospec.WorkflowProtocolStep, state *State) (interface{}, int, error) {
	switch strings.TrimSpace(step.Type) {
	case "parse_input":
		state.Values["parsed_input"] = cloneMap(state.Input)
		return cloneMap(state.Input), 0, nil

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
			return nil, 1, err
		}
		return result, 1, nil

	case "summarize":
		promptAny, err := interpolateValue(step.Prompt, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate summarize prompt: %w", err)
		}
		prompt, ok := promptAny.(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return nil, 0, fmt.Errorf("summarize step requires non-empty prompt")
		}
		result, err := r.dispatcher.Summarize(ctx, prompt, state)
		if err != nil {
			return nil, 0, err
		}
		return result, 0, nil

	case "send_message":
		targetAny, err := interpolateValue(step.TargetAlias, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate send_message target: %w", err)
		}
		target, ok := targetAny.(string)
		if !ok || strings.TrimSpace(target) == "" {
			return nil, 0, fmt.Errorf("send_message step requires targetAlias")
		}
		msgAny, err := interpolateValue(step.PayloadTemplate, state)
		if err != nil {
			return "", 0, fmt.Errorf("interpolate send_message payload: %w", err)
		}
		message, ok := msgAny.(string)
		if !ok || strings.TrimSpace(message) == "" {
			return nil, 0, fmt.Errorf("send_message step requires payloadTemplate")
		}
		result, err := r.dispatcher.DispatchTool(ctx, workflowCaller, state.SenderMXID, "matrix.send_message", map[string]interface{}{
			"target":  target,
			"message": message,
		})
		if err != nil {
			return nil, 1, err
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
		return valueAny, 0, nil

	case "branch":
		return nil, 0, fmt.Errorf("branch steps are not implemented yet")

	default:
		return nil, 0, fmt.Errorf("unsupported workflow step type %q", step.Type)
	}
}

func validateStepOutputSchema(protocolID string, step gosutospec.WorkflowProtocolStep, output interface{}, state *State) error {
	ref := strings.TrimSpace(step.OutputSchemaRef)
	if ref == "" {
		return nil
	}
	if state == nil || len(state.Schemas) == 0 {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocolID,
			Message:    fmt.Sprintf("output schema ref %s not available in execution context", ref),
		}
	}

	schemaAny, ok := state.Schemas[ref]
	if !ok {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocolID,
			Message:    fmt.Sprintf("output schema ref %s not found", ref),
		}
	}
	schema, ok := schemaAny.(map[string]interface{})
	if !ok {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocolID,
			Message:    fmt.Sprintf("output schema %s is not an object", ref),
		}
	}

	typeName, _ := schema["type"].(string)
	if typeName != "" && !matchesJSONSchemaType(typeName, output) {
		return &Error{
			Code:       CodeSchemaValidation,
			ProtocolID: protocolID,
			Message:    fmt.Sprintf("step output has wrong type; expected %s", typeName),
		}
	}

	_, hasRequired := schema["required"]
	_, hasProperties := schema["properties"]
	if typeName == "object" || (typeName == "" && (hasRequired || hasProperties)) {
		payload, ok := output.(map[string]interface{})
		if !ok {
			return &Error{
				Code:       CodeSchemaValidation,
				ProtocolID: protocolID,
				Message:    "step output must be an object",
			}
		}
		if err := validatePayloadAgainstSchema(schema, payload); err != nil {
			return &Error{
				Code:       CodeSchemaValidation,
				ProtocolID: protocolID,
				Message:    err.Error(),
			}
		}
	}

	return nil
}

func outputPresent(value interface{}) bool {
	if value == nil {
		return false
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return true
}

func interpolateValue(value interface{}, state *State) (interface{}, error) {
	switch v := value.(type) {
	case string:
		if token, ok := singleTemplateToken(v); ok {
			resolved, found := resolvePathToken(token, state)
			if !found {
				return nil, fmt.Errorf("unresolved template token %q", token)
			}
			return resolved, nil
		}
		return interpolateString(v, state)
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

func interpolateString(raw string, state *State) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.Contains(trimmed, "{{") || !strings.Contains(trimmed, "}}") {
		return raw, nil
	}

	out := raw
	for {
		start := strings.Index(out, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(out[start+2:], "}}")
		if end < 0 {
			return "", fmt.Errorf("malformed template %q: missing closing braces", raw)
		}
		end += start + 2

		token := strings.TrimSpace(out[start+2 : end])
		if token == "" {
			return "", fmt.Errorf("malformed template %q: empty token", raw)
		}
		resolved, found := resolvePathToken(token, state)
		if !found {
			return "", fmt.Errorf("unresolved template token %q", token)
		}
		replacement := ""
		if resolved != nil {
			replacement = fmt.Sprintf("%v", resolved)
		}
		out = out[:start] + replacement + out[end+2:]
	}

	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		return "", fmt.Errorf("malformed template %q: unmatched braces", raw)
	}

	return out, nil
}

func resolvePathToken(token string, state *State) (interface{}, bool) {
	if state == nil || token == "" {
		return nil, false
	}
	parts := strings.Split(token, ".")
	if len(parts) == 0 {
		return nil, false
	}

	if parts[0] == "steps" && len(parts) == 2 {
		if contract, ok := state.StepOutputs[parts[1]]; ok {
			if output, exists := contract["output"]; exists {
				return output, true
			}
			return contract, true
		}
	}

	var root interface{}
	switch parts[0] {
	case "input":
		root = state.Input
	case "state":
		root = state.Values
	case "steps":
		root = state.StepOutputs
	default:
		return nil, false
	}

	resolved, ok := resolvePath(root, parts[1:])
	if !ok {
		return nil, false
	}
	return resolved, true
}

func resolvePath(root interface{}, path []string) (interface{}, bool) {
	current := root
	if len(path) == 0 {
		return current, true
	}
	for _, p := range path {
		if typed, ok := current.(map[string]map[string]interface{}); ok {
			next, exists := typed[p]
			if !exists {
				return nil, false
			}
			current = next
			continue
		}
		obj, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = obj[p]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func singleTemplateToken(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return "", false
	}
	inner := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
	if inner == "" {
		return "", false
	}
	if strings.Contains(inner, "{{") || strings.Contains(inner, "}}") {
		return "", false
	}
	return inner, true
}
