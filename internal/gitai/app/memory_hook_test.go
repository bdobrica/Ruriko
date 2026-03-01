package app

import (
	"context"
	"strings"
	"testing"
	"time"

	commonmemory "github.com/bdobrica/Ruriko/common/memory"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
	"github.com/bdobrica/Ruriko/internal/gitai/supervisor"
)

func newRunTurnTestApp(t *testing.T, gosutoYAML string, prov llm.Provider) *App {
	t.Helper()

	ldr := gosuto.New()
	if err := ldr.Apply([]byte(gosutoYAML)); err != nil {
		t.Fatalf("gosuto loader Apply: %v", err)
	}

	supv := supervisor.New()
	t.Cleanup(supv.Stop)

	a := &App{
		gosutoLdr: ldr,
		supv:      supv,
		policyEng: policy.New(ldr),
		cancelCh:  make(chan struct{}, 1),
	}
	a.setProvider(prov)
	return a
}

func firstSystemPrompt(t *testing.T, req llm.CompletionRequest) string {
	t.Helper()
	if len(req.Messages) == 0 {
		t.Fatal("completion request has no messages")
	}
	msg := req.Messages[0]
	if msg.Role != llm.RoleSystem {
		t.Fatalf("first message role = %q, want system", msg.Role)
	}
	return msg.Content
}

func TestRunTurn_MemoryHookDisabled_DoesNotInjectMemoryContext(t *testing.T) {
	prov := newCapturingLLM("ok")
	a := newRunTurnTestApp(t, eventTestGosutoYAML, prov)

	result, _, err := a.runTurn(context.Background(), "!chat-room:example.com", "@user:example.com", "hello there", "$evt1")
	if err != nil {
		t.Fatalf("runTurn() error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("runTurn() result = %q, want %q", result, "ok")
	}

	req, ok := prov.waitForCall(500 * time.Millisecond)
	if !ok {
		t.Fatal("expected llm call, timed out")
	}

	prompt := firstSystemPrompt(t, req)
	if strings.Contains(prompt, "Memory Context") {
		t.Fatalf("unexpected memory context section in prompt:\n%s", prompt)
	}
}

func TestRunTurn_MemoryHookEnabled_InjectsContextFromPriorTurns(t *testing.T) {
	prov := newCapturingLLM("ok")
	a := newRunTurnTestApp(t, eventTestGosutoYAML, prov)
	a.memorySTM = newGitaiMemorySTM(50)
	a.memoryAssembler = &commonmemory.ContextAssembler{
		STM:       a.memorySTM,
		LTM:       gitaiNoopLTM{},
		Embedder:  gitaiNoopEmbedder{},
		MaxTokens: commonmemory.DefaultMaxTokens,
		LTMTopK:   commonmemory.DefaultLTMTopK,
	}

	if _, _, err := a.runTurn(context.Background(), "!chat-room:example.com", "@user:example.com", "first message", "$evt1"); err != nil {
		t.Fatalf("first runTurn() error: %v", err)
	}
	if _, ok := prov.waitForCall(500 * time.Millisecond); !ok {
		t.Fatal("expected first llm call, timed out")
	}

	if _, _, err := a.runTurn(context.Background(), "!chat-room:example.com", "@user:example.com", "second message", "$evt2"); err != nil {
		t.Fatalf("second runTurn() error: %v", err)
	}
	req2, ok := prov.waitForCall(500 * time.Millisecond)
	if !ok {
		t.Fatal("expected second llm call, timed out")
	}

	prompt := firstSystemPrompt(t, req2)
	for _, want := range []string{
		"Memory Context",
		"user: first message",
		"assistant: ok",
		"user: second message",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q\nPrompt:\n%s", want, prompt)
		}
	}
}
