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

const kairoDeterministicTestYAML = `apiVersion: gosuto/v1
metadata:
  name: kairo
  canonicalName: kairo
trust:
  allowedRooms:
    - "!kairo-room:example.com"
  allowedSenders:
    - "*"
  adminRoom: "!kairo-room:example.com"
capabilities:
  - name: allow-matrix-send
    mcp: builtin
    tool: matrix.send_message
    allow: true
  - name: deny-all
    mcp: "*"
    tool: "*"
    allow: false
messaging:
  allowedTargets:
    - roomId: "!user-room:example.com"
      alias: user
`

type kairoRecordingSender struct {
	calls []matrixSend
}

func (s *kairoRecordingSender) SendText(roomID, text string) error {
	s.calls = append(s.calls, matrixSend{roomID: roomID, text: text})
	return nil
}

func newKairoDeterministicApp(t *testing.T) (*App, *kairoRecordingSender) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "gitai.db")
	db, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ldr := gosuto.New()
	if err := ldr.Apply([]byte(kairoDeterministicTestYAML)); err != nil {
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

func TestKairoPipeline_TriggerWithoutPortfolio_RequestsUserPortfolio(t *testing.T) {
	a, sender := newKairoDeterministicApp(t)

	handled, result, toolCalls, err := a.tryRunKairoDeterministicTurn(context.Background(), "!kairo-room:example.com", "@saito:example.com", "Saito scheduled trigger: run cycle")
	if !handled {
		t.Fatal("expected deterministic Kairo path to handle Saito trigger")
	}
	if err != nil {
		t.Fatalf("tryRunKairoDeterministicTurn returned error: %v", err)
	}
	if toolCalls != 1 {
		t.Fatalf("toolCalls = %d, want 1", toolCalls)
	}
	if !strings.Contains(result, "Portfolio is not configured") {
		t.Fatalf("result = %q, want missing-portfolio message", result)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d, want 1", len(sender.calls))
	}
	if sender.calls[0].roomID != "!user-room:example.com" {
		t.Fatalf("sent room = %q, want !user-room:example.com", sender.calls[0].roomID)
	}
	if !strings.Contains(strings.ToLower(sender.calls[0].text), "portfolio") {
		t.Fatalf("sent text = %q, want portfolio request", sender.calls[0].text)
	}
}

func TestKairoPipeline_PortfolioThenTrigger_PersistsAnalysisAndReports(t *testing.T) {
	a, sender := newKairoDeterministicApp(t)
	a.kairoMarketDataFetcher = func(_ context.Context, ticker string) (kairoTickerMetrics, error) {
		switch ticker {
		case "AAPL":
			return kairoTickerMetrics{Price: 200, ChangePercent: 1.5, Open: 197, High: 201, Low: 196, PreviousClose: 197, Raw: map[string]interface{}{"c": 200.0, "dp": 1.5}, Commentary: "notable move"}, nil
		case "MSFT":
			return kairoTickerMetrics{Price: 400, ChangePercent: -0.5, Open: 401, High: 405, Low: 398, PreviousClose: 402, Raw: map[string]interface{}{"c": 400.0, "dp": -0.5}, Commentary: "stable"}, nil
		default:
			return kairoTickerMetrics{}, nil
		}
	}

	handled, _, _, err := a.tryRunKairoDeterministicTurn(context.Background(), "!kairo-room:example.com", "@bogdan:example.com", "portfolio: AAPL=60,MSFT=40")
	if !handled {
		t.Fatal("expected deterministic path to handle portfolio message")
	}
	if err != nil {
		t.Fatalf("portfolio message handling returned error: %v", err)
	}

	handled, result, toolCalls, err := a.tryRunKairoDeterministicTurn(context.Background(), "!kairo-room:example.com", "@saito:example.com", "Saito scheduled trigger: run cycle")
	if !handled {
		t.Fatal("expected deterministic Kairo path to handle Saito trigger")
	}
	if err != nil {
		t.Fatalf("trigger handling returned error: %v", err)
	}
	if toolCalls != 1 {
		t.Fatalf("toolCalls = %d, want 1", toolCalls)
	}
	if !strings.Contains(result, "analysis completed") {
		t.Fatalf("result = %q, want completion message", result)
	}

	var runCount int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kairo_analysis_runs WHERE status = 'success'").Scan(&runCount); err != nil {
		t.Fatalf("count kairo_analysis_runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("successful run count = %d, want 1", runCount)
	}

	var tickerCount int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kairo_analysis_tickers").Scan(&tickerCount); err != nil {
		t.Fatalf("count kairo_analysis_tickers: %v", err)
	}
	if tickerCount != 2 {
		t.Fatalf("analysis ticker rows = %d, want 2", tickerCount)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d, want 1 summary report call", len(sender.calls))
	}
	if sender.calls[0].roomID != "!user-room:example.com" {
		t.Fatalf("summary room = %q, want !user-room:example.com", sender.calls[0].roomID)
	}
	if !strings.Contains(sender.calls[0].text, "Kairo portfolio snapshot") {
		t.Fatalf("summary text = %q, want snapshot report", sender.calls[0].text)
	}
}
