package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/gitai/approvals"
	"github.com/bdobrica/Ruriko/internal/gitai/builtin"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
	"github.com/bdobrica/Ruriko/internal/gitai/supervisor"
)

type dispatcherRecordingSender struct {
	calls []matrixSend
}

func (s *dispatcherRecordingSender) SendText(roomID, text string) error {
	s.calls = append(s.calls, matrixSend{roomID: roomID, text: text})
	return nil
}

type dispatcherGateStub struct {
	calls      int
	err        error
	lastParams map[string]interface{}
}

func (g *dispatcherGateStub) Request(_ context.Context, _ string, _ string, _ string, _ string, params map[string]interface{}, _ time.Duration) error {
	g.calls++
	g.lastParams = params
	return g.err
}

func (g *dispatcherGateStub) RecordDecision(_ string, _ store.ApprovalStatus, _ string, _ string) error {
	return nil
}

func newDispatcherTestApp(t *testing.T, gosutoYAML string) (*App, *dispatcherRecordingSender) {
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

	sender := &dispatcherRecordingSender{}
	reg := builtin.New()
	reg.Register(builtin.NewMatrixSendTool(&toolPolicyConfigProvider{ldr: ldr}, sender))

	a := &App{
		db:         db,
		gosutoLdr:  ldr,
		supv:       supv,
		policyEng:  policy.New(ldr),
		cancelCh:   make(chan struct{}, 1),
		builtinReg: reg,
	}
	return a, sender
}

const dispatcherDenyYAML = `apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms:
    - "!room:example.com"
  allowedSenders:
    - "*"
  adminRoom: "!room:example.com"
capabilities:
  - name: deny-matrix-send
    mcp: builtin
    tool: matrix.send_message
    allow: false
messaging:
  allowedTargets:
    - roomId: "!kairo-room:example.com"
      alias: "kairo"
`

const dispatcherApprovalYAML = `apiVersion: gosuto/v1
metadata:
  name: test-agent
trust:
  allowedRooms:
    - "!room:example.com"
  allowedSenders:
    - "*"
  adminRoom: "!room:example.com"
approvals:
  enabled: true
  room: "!approvals-room:example.com"
  ttlSeconds: 30
capabilities:
  - name: approve-matrix-send
    mcp: builtin
    tool: matrix.send_message
    allow: true
    requireApproval: true
messaging:
  allowedTargets:
    - roomId: "!kairo-room:example.com"
      alias: "kairo"
`

func TestDispatchToolCall_DenyPolicy_IsSameForLLMAndWorkflow(t *testing.T) {
	a, sender := newDispatcherTestApp(t, dispatcherDenyYAML)

	args := map[string]interface{}{"target": "kairo", "message": "hello"}
	_, errLLM := a.DispatchToolCall(context.Background(), ToolDispatchRequest{
		Caller: dispatchCallerLLM,
		Sender: "@user:example.com",
		Name:   builtin.MatrixSendToolName,
		Args:   args,
	})
	_, errWorkflow := a.DispatchToolCall(context.Background(), ToolDispatchRequest{
		Caller: dispatchCallerWorkflow,
		Sender: "@kairo:example.com",
		Name:   builtin.MatrixSendToolName,
		Args:   args,
	})

	if errLLM == nil || errWorkflow == nil {
		t.Fatalf("expected policy deny errors for both paths, got llm=%v workflow=%v", errLLM, errWorkflow)
	}
	if !strings.Contains(errLLM.Error(), "policy denied") || !strings.Contains(errWorkflow.Error(), "policy denied") {
		t.Fatalf("expected policy denied errors, got llm=%v workflow=%v", errLLM, errWorkflow)
	}
	if len(sender.calls) != 0 {
		t.Fatalf("expected no outbound sends on denied dispatch, got %d", len(sender.calls))
	}
}

func TestDispatchToolCall_ApprovalPolicy_IsSameForLLMAndWorkflow(t *testing.T) {
	a, sender := newDispatcherTestApp(t, dispatcherApprovalYAML)
	gate := &dispatcherGateStub{}
	a.approvalGt = gate

	args := map[string]interface{}{"target": "kairo", "message": "approved message"}
	_, errLLM := a.DispatchToolCall(context.Background(), ToolDispatchRequest{
		Caller: dispatchCallerLLM,
		Sender: "@user:example.com",
		Name:   builtin.MatrixSendToolName,
		Args:   args,
	})
	_, errWorkflow := a.DispatchToolCall(context.Background(), ToolDispatchRequest{
		Caller: dispatchCallerWorkflow,
		Sender: "@kumo:example.com",
		Name:   builtin.MatrixSendToolName,
		Args:   args,
	})

	if errLLM != nil || errWorkflow != nil {
		t.Fatalf("expected approval path success for both callers, got llm=%v workflow=%v", errLLM, errWorkflow)
	}
	if gate.calls != 2 {
		t.Fatalf("approval gate calls = %d, want 2", gate.calls)
	}
	if gate.lastParams == nil {
		t.Fatal("expected approval payload params to be populated")
	}
	for _, key := range []string{"trace_id", "tool_ref", "normalized_arg_hash", "caller_context"} {
		if _, ok := gate.lastParams[key]; !ok {
			t.Fatalf("approval payload missing key %q: %+v", key, gate.lastParams)
		}
	}
	if len(sender.calls) != 2 {
		t.Fatalf("outbound sends = %d, want 2", len(sender.calls))
	}
	if sender.calls[0].roomID != "!kairo-room:example.com" || sender.calls[1].roomID != "!kairo-room:example.com" {
		t.Fatalf("unexpected room dispatches: %+v", sender.calls)
	}
}

func TestDispatchToolCall_WorkflowApproval_BlocksUntilDecision(t *testing.T) {
	a, sender := newDispatcherTestApp(t, dispatcherApprovalYAML)
	a.approvalGt = approvals.New(a.db, nil)

	ctx := trace.WithTraceID(context.Background(), "workflow-block-test")
	args := map[string]interface{}{"target": "kairo", "message": "approved message"}

	type result struct {
		value string
		err   error
	}
	resultCh := make(chan result, 1)

	go func() {
		res, err := a.DispatchToolCall(ctx, ToolDispatchRequest{
			Caller: dispatchCallerWorkflow,
			Sender: "@kumo:example.com",
			Name:   builtin.MatrixSendToolName,
			Args:   args,
		})
		resultCh <- result{value: res, err: err}
	}()

	select {
	case <-resultCh:
		t.Fatal("workflow dispatch returned before approval decision; expected blocking behavior")
	case <-time.After(250 * time.Millisecond):
	}

	if err := a.approvalGt.RecordDecision("appr_workflow-block-test", store.ApprovalApproved, "@approver:example.com", "ok"); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	select {
	case out := <-resultCh:
		if out.err != nil {
			t.Fatalf("DispatchToolCall returned error after approval: %v", out.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workflow dispatch to resume after approval decision")
	}

	if len(sender.calls) != 1 {
		t.Fatalf("outbound sends = %d, want 1 after approval", len(sender.calls))
	}
}
