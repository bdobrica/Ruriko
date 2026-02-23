package app

// Tests for R15.3: Policy engine integration with built-in tools,
// specifically the matrix.send_message gatherTools filtering.
//
// Coverage:
//   - gatherTools excludes matrix.send_message when no messaging targets are configured
//   - gatherTools includes matrix.send_message when messaging targets are configured

import (
	"context"
	"path/filepath"
	"testing"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/builtin"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
	"github.com/bdobrica/Ruriko/internal/gitai/supervisor"
)

// toolPolicyStubbedSender is a no-op Matrix sender used for building the
// built-in registry in policy tests. It never actually sends anything.
type toolPolicyStubbedSender struct{}

func (toolPolicyStubbedSender) SendText(_, _ string) error { return nil }

// toolPolicyConfigProvider wraps a gosuto.Loader so that MatrixSendTool has
// access to the same config as the policy engine.
type toolPolicyConfigProvider struct {
	ldr *gosuto.Loader
}

func (p *toolPolicyConfigProvider) Config() *gosutospec.Config { return p.ldr.Config() }

// newToolPolicyApp builds a minimal App wired for gatherTools tests. Unlike
// newEventApp, it also populates the builtinReg with matrix.send_message.
func newToolPolicyApp(t *testing.T, gosutoYAML string) *App {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "gitai.db")
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { db.Close() })

	ldr := gosuto.New()
	if err := ldr.Apply([]byte(gosutoYAML)); err != nil {
		t.Fatalf("gosuto loader Apply: %v", err)
	}

	supv := supervisor.New()
	t.Cleanup(supv.Stop)

	// Wire the built-in registry with matrix.send_message.
	reg := builtin.New()
	reg.Register(builtin.NewMatrixSendTool(&toolPolicyConfigProvider{ldr: ldr}, toolPolicyStubbedSender{}))

	a := &App{
		db:         db,
		gosutoLdr:  ldr,
		supv:       supv,
		policyEng:  policy.New(ldr),
		cancelCh:   make(chan struct{}, 1),
		builtinReg: reg,
	}
	return a
}

// hasToolDef reports whether name appears among the provided definitions.
func hasToolDef(defs []llm.ToolDefinition, name string) bool {
	for _, d := range defs {
		if d.Function.Name == name {
			return true
		}
	}
	return false
}

// toolsPolicyTestGosutoYAML_WithMessaging is a complete Gosuto config that
// includes a messaging section with one allowed target and the corresponding
// capability rule.
const toolsPolicyTestGosutoYAML_WithMessaging = `apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms:
    - "!chat-room:example.com"
  allowedSenders:
    - "@user:example.com"
  adminRoom: "!admin-room:example.com"
persona:
  llmProvider: openai
  model: gpt-4o-mini
  systemPrompt: "You are a helpful test agent."
capabilities:
  - name: allow-matrix-send
    mcp: builtin
    tool: matrix.send_message
    allow: true
messaging:
  allowedTargets:
    - roomId: "!kairo-admin:localhost"
      alias: "kairo"
  maxMessagesPerMinute: 30
`

// TestGatherTools_ExcludesMatrixSend_WhenNoMessagingConfigured verifies that
// gatherTools does not expose matrix.send_message to the LLM when the Gosuto
// config contains no AllowedTargets, keeping the tool genuinely unavailable
// rather than visible-but-always-denied.
func TestGatherTools_ExcludesMatrixSend_WhenNoMessagingConfigured(t *testing.T) {
	// eventTestGosutoYAML (defined in event_test.go) has no messaging section.
	a := newToolPolicyApp(t, eventTestGosutoYAML)

	defs, _ := a.gatherTools(context.Background())
	if hasToolDef(defs, builtin.MatrixSendToolName) {
		t.Errorf("gatherTools exposed %q when no messaging targets configured; want excluded",
			builtin.MatrixSendToolName)
	}
}

// TestGatherTools_IncludesMatrixSend_WhenMessagingConfigured verifies that
// gatherTools exposes matrix.send_message to the LLM when the Gosuto config
// contains at least one allowed target.
func TestGatherTools_IncludesMatrixSend_WhenMessagingConfigured(t *testing.T) {
	a := newToolPolicyApp(t, toolsPolicyTestGosutoYAML_WithMessaging)

	defs, _ := a.gatherTools(context.Background())
	if !hasToolDef(defs, builtin.MatrixSendToolName) {
		t.Errorf("gatherTools did not expose %q when messaging targets configured; want included",
			builtin.MatrixSendToolName)
	}
}
