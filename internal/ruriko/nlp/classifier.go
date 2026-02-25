package nlp

import (
	"context"
	"fmt"
	"strings"
)

// Confidence thresholds that govern how the Classifier interprets the
// raw LLM output.
//
//   - ≥ HighConfidenceThreshold:  proceed as-is (with confirmation for mutations).
//   - ≥ MidConfidenceThreshold:   present a clarifying question before acting.
//   - < MidConfidenceThreshold:   surface a friendly "I'm not sure" fallback.
const (
	HighConfidenceThreshold = 0.8
	MidConfidenceThreshold  = 0.5
)

// clarificationSuffix is appended to every low-confidence reply so the user
// immediately knows how to get help.
const clarificationSuffix = "\n\nYou can use `/ruriko help` to see all available commands."

// Classifier wraps a Provider with output validation and sanitisation.
//
// It adds three layers of enforcement on top of the raw LLM output:
//  1. Flag sanitisation: strips any flag keys starting with "_"
//     (defense-in-depth mirroring the same stripping in commands.Parse).
//  2. Action-key validation: rejects commands that reference an action key
//     not registered in the Router, preventing the LLM from producing
//     phantom commands.
//  3. Confidence thresholds: downgrades or rewrites the response for
//     low-confidence classifications so callers always get an explicit
//     signal about whether to proceed, ask for confirmation, or surface
//     a clarifying question.
//
// Classifier implements Provider and can be used as a drop-in replacement
// wherever a Provider is accepted.
type Classifier struct {
	provider  Provider
	knownKeys map[string]struct{}
}

// NewClassifier returns a Classifier backed by provider.
//
// knownActionKeys should be the complete set of action keys registered in the
// Router.  These are the only keys the LLM is allowed to produce; any other
// key is treated as malformed output and rejected with an IntentUnknown
// response.
func NewClassifier(provider Provider, knownActionKeys []string) *Classifier {
	keys := make(map[string]struct{}, len(knownActionKeys))
	for _, k := range knownActionKeys {
		keys[k] = struct{}{}
	}
	return &Classifier{
		provider:  provider,
		knownKeys: keys,
	}
}

// Classify calls the underlying Provider, then sanitises and validates the
// returned ClassifyResponse.
//
// The following post-processing steps are applied in order:
//  1. Strip flags whose keys start with "_" (internal-flag defense).
//  2. If intent == command: verify the action key is registered.  If not,
//     return IntentUnknown with an explanatory message.
//  3. Apply confidence-threshold policy (see applyConfidencePolicy).
//
// Classify implements the Provider interface so a Classifier can be stored
// wherever a Provider is expected.
func (c *Classifier) Classify(ctx context.Context, req ClassifyRequest) (*ClassifyResponse, error) {
	resp, err := c.provider.Classify(ctx, req)
	if err != nil {
		return nil, err
	}

	// --- 1. Sanitise flags --------------------------------------------------
	resp.Flags = sanitiseFlags(resp.Flags)
	for i := range resp.Steps {
		resp.Steps[i].Flags = sanitiseFlags(resp.Steps[i].Flags)
	}

	// --- 2. Validate action key(s) ------------------------------------------
	switch resp.Intent {
	case IntentCommand:
		if resp.Action != "" {
			if _, ok := c.knownKeys[resp.Action]; !ok {
				return unknownActionResponse(resp.Action), nil
			}
		}
		// Validate every step in a multi-step command response as well.
		for _, step := range resp.Steps {
			if _, ok := c.knownKeys[step.Action]; !ok {
				return unknownActionResponse(step.Action), nil
			}
		}

	case IntentPlan:
		// A plan must contain at least one step — an empty plan is treated as
		// malformed output and converted to IntentUnknown so the caller gets
		// a useful clarification prompt.
		if len(resp.Steps) == 0 {
			return &ClassifyResponse{
				Intent: IntentUnknown,
				Response: "I understood that as a multi-step plan but couldn't determine the individual steps. " +
					"Could you describe what you'd like to set up in more detail?" + clarificationSuffix,
				Explanation: "LLM returned intent=plan with an empty steps array",
			}, nil
		}
		// Validate every step action key.
		for _, step := range resp.Steps {
			if _, ok := c.knownKeys[step.Action]; !ok {
				return unknownActionResponse(step.Action), nil
			}
		}
		// Plans must not carry a top-level Action — clear it defensively.
		resp.Action = ""
	}

	// --- 3. Apply confidence-threshold policy --------------------------------
	resp = applyConfidencePolicy(resp)

	return resp, nil
}

// unknownActionResponse builds the IntentUnknown response returned when the
// LLM produces an action key that is not in the Router's registry.
func unknownActionResponse(action string) *ClassifyResponse {
	return &ClassifyResponse{
		Intent: IntentUnknown,
		Response: fmt.Sprintf(
			"I didn't quite understand that — I'm not familiar with the action %q.%s",
			action, clarificationSuffix,
		),
		Explanation: fmt.Sprintf("LLM produced unregistered action key: %q", action),
	}
}

// sanitiseFlags returns a copy of flags with any keys starting with "_"
// removed.  This mirrors the stripping performed by commands.Parse and acts
// as a defense-in-depth measure in case the LLM (or a prompt-injection
// payload) produces internal flags.
//
// Returns nil when flags is nil; returns an empty map when all keys are
// stripped.
func sanitiseFlags(flags map[string]string) map[string]string {
	if flags == nil {
		return nil
	}
	clean := make(map[string]string, len(flags))
	for k, v := range flags {
		if !strings.HasPrefix(k, "_") {
			clean[k] = v
		}
	}
	return clean
}

// applyConfidencePolicy rewrites resp according to the confidence thresholds.
//
// High confidence (≥ 0.8)
//
//	The response is returned unchanged.  Callers proceed with the normal
//	confirm-and-dispatch path.
//
// Mid confidence (≥ 0.5 and < 0.8)
//
//	The structured fields (Intent, Action, Args, Flags) are preserved so that
//	if the user confirms the interpretation, the caller can dispatch the
//	command directly.  However, the Response field is replaced with a
//	"I think you want to … — is that right?" message so the NL layer
//	always seeks human confirmation before acting on uncertain output.
//
// Low confidence (< 0.5)
//
//	Intent is downgraded to IntentUnknown and a friendly clarification
//	message is returned.  The original explanation (if any) is preserved
//	for traceability.
func applyConfidencePolicy(resp *ClassifyResponse) *ClassifyResponse {
	switch {
	case resp.Confidence >= HighConfidenceThreshold:
		// High confidence — pass through unchanged.
		return resp

	case resp.Confidence >= MidConfidenceThreshold:
		// Mid confidence — keep the proposed command but ask for confirmation.
		explanation := resp.Explanation
		if explanation == "" {
			explanation = fmt.Sprintf("run %s", resp.Action)
		}
		resp.Response = fmt.Sprintf(
			"I think you want to: %s — is that right? Reply **yes** to confirm or **no** to cancel.",
			explanation,
		)
		return resp

	default:
		// Low confidence — surface a clarification prompt.
		hint := "Here are some things I can help with: managing agents, secrets, Gosuto configs, approvals, and audit logs."
		if resp.Explanation != "" {
			hint = resp.Explanation
		}
		return &ClassifyResponse{
			Intent:      IntentUnknown,
			Explanation: resp.Explanation,
			Response: fmt.Sprintf(
				"I'm not sure what you'd like. %s%s",
				hint, clarificationSuffix,
			),
		}
	}
}
