package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
)

const (
	workflowCaller              = "workflow"
	stepPrefix                  = "step_"
	defaultForEachMaxIterations = 10
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
			result, callCount, err := r.runStep(ctx, protocol.ID, step, state)
			if err != nil {
				return err
			}
			if err := validateStepOutputSchema(protocol.ID, step, result, state); err != nil {
				return err
			}
			if err := validateStepMaxOutputItems(protocol.ID, step, result); err != nil {
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

func (r *Runner) runStep(ctx context.Context, protocolID string, step gosutospec.WorkflowProtocolStep, state *State) (interface{}, int, error) {
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

	case "plan":
		if strings.TrimSpace(step.OutputSchemaRef) == "" {
			return nil, 0, fmt.Errorf("plan step requires outputSchemaRef")
		}
		promptAny, err := interpolateValue(step.Prompt, state)
		if err != nil {
			return nil, 0, fmt.Errorf("interpolate plan prompt: %w", err)
		}
		prompt, ok := promptAny.(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return nil, 0, fmt.Errorf("plan step requires non-empty prompt")
		}
		raw, err := r.dispatcher.Summarize(ctx, prompt, state)
		if err != nil {
			return nil, 0, err
		}
		structured, err := parseStructuredJSON(raw)
		if err != nil {
			return nil, 0, fmt.Errorf("plan step expected JSON output: %w", err)
		}
		return structured, 0, nil

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

	case "for_each":
		itemsExpr := strings.TrimSpace(step.ItemsExpr)
		if itemsExpr == "" {
			return nil, 0, fmt.Errorf("for_each step requires itemsExpr")
		}
		itemsAny, err := interpolateValue(itemsExpr, state)
		if err != nil {
			return nil, 0, fmt.Errorf("interpolate for_each itemsExpr: %w", err)
		}
		items, err := asInterfaceSlice(itemsAny)
		if err != nil {
			return nil, 0, fmt.Errorf("for_each itemsExpr must resolve to an array: %w", err)
		}

		maxIterations := step.MaxIterations
		if maxIterations <= 0 {
			maxIterations = defaultForEachMaxIterations
		}
		if len(items) > maxIterations {
			return nil, 0, fmt.Errorf("for_each item count %d exceeds maxIterations %d", len(items), maxIterations)
		}

		if len(step.Steps) == 0 {
			return nil, 0, fmt.Errorf("for_each step requires nested steps")
		}

		itemVar := strings.TrimSpace(step.ItemVar)
		if itemVar == "" {
			itemVar = "item"
		}
		indexVar := itemVar + "_index"

		oldItem, hadItem := state.Values[itemVar]
		oldIndex, hadIndex := state.Values[indexVar]
		defer func() {
			if hadItem {
				state.Values[itemVar] = oldItem
			} else {
				delete(state.Values, itemVar)
			}
			if hadIndex {
				state.Values[indexVar] = oldIndex
			} else {
				delete(state.Values, indexVar)
			}
		}()

		iterationResults := make([]interface{}, 0, len(items))
		totalNestedToolCalls := 0

		for i, item := range items {
			state.Values[itemVar] = item
			state.Values[indexVar] = float64(i)

			outputs := make(map[string]interface{}, len(step.Steps))
			var iterationFinal interface{}

			for nestedIndex, nestedStep := range step.Steps {
				nestedResult, nestedToolCalls, err := r.runStep(ctx, protocolID, nestedStep, state)
				totalNestedToolCalls += nestedToolCalls
				if err != nil {
					return nil, totalNestedToolCalls, fmt.Errorf("for_each iteration %d nested step %d (%s) failed: %w", i, nestedIndex, nestedStep.Type, err)
				}
				if err := validateStepOutputSchema(protocolID, nestedStep, nestedResult, state); err != nil {
					return nil, totalNestedToolCalls, fmt.Errorf("for_each iteration %d nested step %d (%s) output validation failed: %w", i, nestedIndex, nestedStep.Type, err)
				}
				if err := validateStepMaxOutputItems(protocolID, nestedStep, nestedResult); err != nil {
					return nil, totalNestedToolCalls, fmt.Errorf("for_each iteration %d nested step %d (%s) output cardinality failed: %w", i, nestedIndex, nestedStep.Type, err)
				}
				nestedKey := stepPrefix + strconv.Itoa(nestedIndex)
				outputs[nestedKey] = nestedResult
				if outputPresent(nestedResult) {
					iterationFinal = nestedResult
				}
			}

			iterationContract := map[string]interface{}{
				"index":   i,
				"item":    item,
				"outputs": outputs,
				"result":  iterationFinal,
			}

			if err := validateOutputAgainstSchemaRef(protocolID, step.ForEachResultSchemaRef, iterationFinal, state); err != nil {
				return nil, totalNestedToolCalls, fmt.Errorf("for_each iteration %d result schema validation failed: %w", i, err)
			}
			if err := validateOutputAgainstSchemaRef(protocolID, step.ForEachIterationSchemaRef, iterationContract, state); err != nil {
				return nil, totalNestedToolCalls, fmt.Errorf("for_each iteration %d contract schema validation failed: %w", i, err)
			}

			iterationResults = append(iterationResults, iterationContract)
		}

		return iterationResults, totalNestedToolCalls, nil

	case "collect":
		collectFrom := strings.TrimSpace(step.CollectFrom)
		if collectFrom == "" {
			return nil, 0, fmt.Errorf("collect step requires collectFrom")
		}
		collectMode := strings.TrimSpace(step.CollectMode)
		if collectMode == "" {
			collectMode = "result"
		}
		sourceAny, err := interpolateValue(collectFrom, state)
		if err != nil {
			return nil, 0, fmt.Errorf("interpolate collect source: %w", err)
		}
		source, err := asInterfaceSlice(sourceAny)
		if err != nil {
			return nil, 0, fmt.Errorf("collect source must resolve to an array: %w", err)
		}
		collected := make([]interface{}, 0, len(source))
		for _, entry := range source {
			selected, err := selectCollectValue(entry, collectMode)
			if err != nil {
				return nil, 0, err
			}
			if !outputPresent(selected) {
				continue
			}
			if step.CollectFlatten {
				if flat, ok := asOptionalInterfaceSlice(selected); ok {
					collected = append(collected, flat...)
					continue
				}
			}
			collected = append(collected, selected)
		}
		return collected, 0, nil

	case "branch":
		return nil, 0, fmt.Errorf("branch steps are not implemented yet")

	default:
		return nil, 0, fmt.Errorf("unsupported workflow step type %q", step.Type)
	}
}

func asInterfaceSlice(value interface{}) ([]interface{}, error) {
	if value == nil {
		return nil, fmt.Errorf("value is nil")
	}
	if typed, ok := value.([]interface{}); ok {
		return typed, nil
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("value type %T is not an array", value)
	}
	out := make([]interface{}, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out = append(out, rv.Index(i).Interface())
	}
	return out, nil
}

func asOptionalInterfaceSlice(value interface{}) ([]interface{}, bool) {
	if value == nil {
		return nil, false
	}
	if typed, ok := value.([]interface{}); ok {
		return typed, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	out := make([]interface{}, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out = append(out, rv.Index(i).Interface())
	}
	return out, true
}

func selectCollectValue(entry interface{}, mode string) (interface{}, error) {
	obj, isObj := entry.(map[string]interface{})
	switch mode {
	case "result":
		if isObj {
			if result, exists := obj["result"]; exists {
				return result, nil
			}
		}
		return entry, nil
	case "entry":
		return entry, nil
	case "outputs":
		if !isObj {
			return nil, fmt.Errorf("collect mode outputs requires object entries")
		}
		outputs, ok := obj["outputs"]
		if !ok {
			return nil, fmt.Errorf("collect mode outputs requires outputs field")
		}
		return outputs, nil
	case "item":
		if !isObj {
			return nil, fmt.Errorf("collect mode item requires object entries")
		}
		item, ok := obj["item"]
		if !ok {
			return nil, fmt.Errorf("collect mode item requires item field")
		}
		return item, nil
	default:
		return nil, fmt.Errorf("collect mode %q is not supported", mode)
	}
}

func parseStructuredJSON(raw string) (interface{}, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty response")
	}
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasPrefix(strings.ToLower(trimmed), "json") {
			trimmed = strings.TrimSpace(trimmed[4:])
		}
		if idx := strings.LastIndex(trimmed, "```"); idx >= 0 {
			trimmed = strings.TrimSpace(trimmed[:idx])
		}
	}

	var out interface{}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateStepOutputSchema(protocolID string, step gosutospec.WorkflowProtocolStep, output interface{}, state *State) error {
	return validateOutputAgainstSchemaRef(protocolID, step.OutputSchemaRef, output, state)
}

func validateStepMaxOutputItems(protocolID string, step gosutospec.WorkflowProtocolStep, output interface{}) error {
	if step.MaxOutputItems <= 0 {
		return nil
	}
	items, ok := asOptionalInterfaceSlice(output)
	if !ok {
		return nil
	}
	if len(items) <= step.MaxOutputItems {
		return nil
	}
	return &Error{
		Code:       CodeSchemaValidation,
		ProtocolID: protocolID,
		Message: fmt.Sprintf(
			"step output item count %d exceeds maxOutputItems %d",
			len(items),
			step.MaxOutputItems,
		),
	}
}

func validateOutputAgainstSchemaRef(protocolID, schemaRef string, output interface{}, state *State) error {
	ref := strings.TrimSpace(schemaRef)
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
	if parts[0] == "steps" && len(parts) > 2 {
		if contract, ok := state.StepOutputs[parts[1]]; ok {
			if parts[2] == "output" {
				resolved, ok := resolvePath(contract, parts[2:])
				if ok {
					return resolved, true
				}
			}
			if outputObj, ok := contract["output"].(map[string]interface{}); ok {
				resolved, ok := resolvePath(outputObj, parts[2:])
				if ok {
					return resolved, true
				}
			}
			resolved, ok := resolvePath(contract, parts[2:])
			if ok {
				return resolved, true
			}
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
