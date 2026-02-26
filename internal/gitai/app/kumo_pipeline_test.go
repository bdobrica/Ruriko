package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bdobrica/Ruriko/internal/gitai/builtin"
	"github.com/bdobrica/Ruriko/internal/gitai/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/policy"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
	"github.com/bdobrica/Ruriko/internal/gitai/supervisor"
)

const kumoDeterministicTestYAML = "apiVersion: gosuto/v1\n" +
	"metadata:\n" +
	"  name: kumo\n" +
	"  canonicalName: kumo\n" +
	"trust:\n" +
	"  allowedRooms:\n" +
	"    - \"!kumo-room:example.com\"\n" +
	"  allowedSenders:\n" +
	"    - \"*\"\n" +
	"  adminRoom: \"!kumo-room:example.com\"\n" +
	"capabilities:\n" +
	"  - name: allow-matrix-send\n" +
	"    mcp: builtin\n" +
	"    tool: matrix.send_message\n" +
	"    allow: true\n" +
	"  - name: deny-all\n" +
	"    mcp: \"*\"\n" +
	"    tool: \"*\"\n" +
	"    allow: false\n" +
	"messaging:\n" +
	"  allowedTargets:\n" +
	"    - roomId: \"!kairo-room:example.com\"\n" +
	"      alias: kairo\n"

func newKumoDeterministicApp(t *testing.T) (*App, *kairoRecordingSender) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "gitai.db")
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ldr := gosuto.New()
	if err := ldr.Apply([]byte(kumoDeterministicTestYAML)); err != nil {
		t.Fatalf("gosuto loader Apply: %v", err)
	}

	supv := supervisor.New()
	t.Cleanup(supv.Stop)

	sender := &kairoRecordingSender{}
	reg := builtin.New()
	reg.Register(builtin.NewMatrixSendTool(ldr, sender))

	return &App{
		db:         db,
		gosutoLdr:  ldr,
		supv:       supv,
		policyEng:  policy.New(ldr),
		cancelCh:   make(chan struct{}, 1),
		builtinReg: reg,
	}, sender
}

func TestKumoPipeline_ReceivesKairoRequest_SearchesAndRespondsToKairo(t *testing.T) {
	a, sender := newKumoDeterministicApp(t)
	a.kumoNewsFetcher = func(_ context.Context, req kairoNewsRequest) (kumoNewsResult, error) {
		if req.RunID != 42 {
			t.Fatalf("req.RunID = %d, want 42", req.RunID)
		}
		if len(req.Tickers) != 2 || req.Tickers[0] != "AAPL" || req.Tickers[1] != "MSFT" {
			t.Fatalf("req.Tickers = %+v, want [AAPL MSFT]", req.Tickers)
		}
		return kumoNewsResult{
			Summary:   "AAPL faces regulatory pressure; MSFT expands AI partnerships.",
			Headlines: []string{"AAPL under regulatory pressure", "MSFT expands AI partnerships"},
			Material:  true,
		}, nil
	}

	reqMsg, err := buildKairoNewsRequestMessage(kairoNewsRequest{
		RunID:         42,
		Tickers:       []string{"MSFT", "AAPL"},
		MarketSummary: "Kairo portfolio snapshot",
	})
	if err != nil {
		t.Fatalf("buildKairoNewsRequestMessage: %v", err)
	}

	handled, result, toolCalls, err := a.tryRunKumoDeterministicTurn(context.Background(), "@kairo:example.com", reqMsg)
	if !handled {
		t.Fatal("expected deterministic Kumo path to handle Kairo request")
	}
	if err != nil {
		t.Fatalf("tryRunKumoDeterministicTurn returned error: %v", err)
	}
	if toolCalls != 1 {
		t.Fatalf("toolCalls = %d, want 1", toolCalls)
	}
	if !strings.Contains(result, "run_id=42") {
		t.Fatalf("result = %q, want run_id reference", result)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d, want 1", len(sender.calls))
	}
	if sender.calls[0].roomID != "!kairo-room:example.com" {
		t.Fatalf("sent room = %q, want !kairo-room:example.com", sender.calls[0].roomID)
	}
	if !strings.HasPrefix(sender.calls[0].text, kumoNewsResponsePrefix+" ") {
		t.Fatalf("sent text = %q, want KUMO_NEWS_RESPONSE payload", sender.calls[0].text)
	}

	parsed, ok, err := parseKumoNewsResponseMessage(sender.calls[0].text)
	if !ok {
		t.Fatal("expected parseKumoNewsResponseMessage to match response payload")
	}
	if err != nil {
		t.Fatalf("parseKumoNewsResponseMessage error: %v", err)
	}
	if parsed.RunID != 42 {
		t.Fatalf("parsed.RunID = %d, want 42", parsed.RunID)
	}
	if !parsed.Material {
		t.Fatal("parsed.Material = false, want true")
	}
}
