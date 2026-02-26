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

const kairoDeterministicTestYAML = "apiVersion: gosuto/v1\n" +
	"metadata:\n" +
	"  name: kairo\n" +
	"  canonicalName: kairo\n" +
	"trust:\n" +
	"  allowedRooms:\n" +
	"    - \"!kairo-room:example.com\"\n" +
	"  allowedSenders:\n" +
	"    - \"*\"\n" +
	"  adminRoom: \"!kairo-room:example.com\"\n" +
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
	"    - roomId: \"!kumo-room:example.com\"\n" +
	"      alias: kumo\n" +
	"    - roomId: \"!user-room:example.com\"\n" +
	"      alias: user\n"

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
	if !strings.Contains(result, "Requested news context from Kumo") {
		t.Fatalf("result = %q, want Kumo request message", result)
	}

	var runCount int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kairo_analysis_runs WHERE status = 'awaiting_news'").Scan(&runCount); err != nil {
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
		t.Fatalf("sender calls = %d, want 1 Kumo request call", len(sender.calls))
	}
	if sender.calls[0].roomID != "!kumo-room:example.com" {
		t.Fatalf("request room = %q, want !kumo-room:example.com", sender.calls[0].roomID)
	}
	if !strings.Contains(sender.calls[0].text, kairoNewsRequestPrefix) {
		t.Fatalf("request text = %q, want KAIRO_NEWS_REQUEST payload", sender.calls[0].text)
	}

	respMsg, err := buildKumoNewsResponseMessage(kumoNewsResponse{
		RunID:     1,
		Summary:   "AAPL faces regulatory probe; MSFT announces cloud expansion.",
		Headlines: []string{"AAPL under regulatory probe", "MSFT cloud business grows"},
		Material:  true,
	})
	if err != nil {
		t.Fatalf("buildKumoNewsResponseMessage: %v", err)
	}

	handled, result, toolCalls, err = a.tryRunKairoDeterministicTurn(context.Background(), "!kairo-room:example.com", "@kumo:example.com", respMsg)
	if !handled {
		t.Fatal("expected deterministic Kairo path to handle Kumo response")
	}
	if err != nil {
		t.Fatalf("kumo response handling returned error: %v", err)
	}
	if toolCalls != 1 {
		t.Fatalf("toolCalls = %d, want 1 user notification call", toolCalls)
	}
	if !strings.Contains(result, "user notified") {
		t.Fatalf("result = %q, want notified message", result)
	}

	if len(sender.calls) != 2 {
		t.Fatalf("sender calls = %d, want 2 (kumo request + user report)", len(sender.calls))
	}
	if sender.calls[1].roomID != "!user-room:example.com" {
		t.Fatalf("final report room = %q, want !user-room:example.com", sender.calls[1].roomID)
	}
	if !strings.Contains(sender.calls[1].text, "Kairo final report") {
		t.Fatalf("final report text = %q, want final report", sender.calls[1].text)
	}

	var notifiedCount int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kairo_analysis_runs WHERE status = 'notified'").Scan(&notifiedCount); err != nil {
		t.Fatalf("count notified kairo_analysis_runs: %v", err)
	}
	if notifiedCount != 1 {
		t.Fatalf("notified run count = %d, want 1", notifiedCount)
	}
}

func TestKairoPipeline_KumoResponseSignificantButRateLimited_DoesNotNotifyUser(t *testing.T) {
	a, sender := newKairoDeterministicApp(t)
	a.kairoMarketDataFetcher = func(_ context.Context, ticker string) (kairoTickerMetrics, error) {
		return kairoTickerMetrics{Ticker: ticker, Price: 100, ChangePercent: 3.1, Raw: map[string]interface{}{"c": 100.0, "dp": 3.1}, Commentary: "material move"}, nil
	}

	if _, _, _, err := a.tryRunKairoDeterministicTurn(context.Background(), "!kairo-room:example.com", "@bogdan:example.com", "portfolio: AAPL=100"); err != nil {
		t.Fatalf("save portfolio: %v", err)
	}
	if _, _, _, err := a.tryRunKairoDeterministicTurn(context.Background(), "!kairo-room:example.com", "@saito:example.com", "Saito scheduled trigger: run cycle"); err != nil {
		t.Fatalf("run trigger: %v", err)
	}

	for i := 0; i < kairoMaxNotifyPerHour; i++ {
		if _, err := a.db.SaveKairoAnalysisRun(store.KairoAnalysisRun{
			TraceID:       "trace-rate-limit",
			TriggerSource: "saito",
			RoomID:        "!kairo-room:example.com",
			Status:        "notified",
			Summary:       "prior notification",
			Commentary:    "rate limit seed",
		}, nil); err != nil {
			t.Fatalf("seed notified run: %v", err)
		}
	}

	respMsg, err := buildKumoNewsResponseMessage(kumoNewsResponse{
		RunID:     1,
		Summary:   "Major legal development.",
		Headlines: []string{"Company hit with major lawsuit"},
		Material:  true,
	})
	if err != nil {
		t.Fatalf("buildKumoNewsResponseMessage: %v", err)
	}

	handled, result, toolCalls, err := a.tryRunKairoDeterministicTurn(context.Background(), "!kairo-room:example.com", "@kumo:example.com", respMsg)
	if !handled {
		t.Fatal("expected deterministic Kairo path to handle Kumo response")
	}
	if err != nil {
		t.Fatalf("handle Kumo response: %v", err)
	}
	if toolCalls != 0 {
		t.Fatalf("toolCalls = %d, want 0 due to rate limiting", toolCalls)
	}
	if !strings.Contains(strings.ToLower(result), "rate-limited") {
		t.Fatalf("result = %q, want rate-limited message", result)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d, want only initial Kumo request", len(sender.calls))
	}
	if sender.calls[0].roomID != "!kumo-room:example.com" {
		t.Fatalf("call room = %q, want !kumo-room:example.com", sender.calls[0].roomID)
	}

	var rateLimitedCount int
	if err := a.db.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kairo_analysis_runs WHERE status = 'rate_limited'").Scan(&rateLimitedCount); err != nil {
		t.Fatalf("count rate_limited runs: %v", err)
	}
	if rateLimitedCount != 1 {
		t.Fatalf("rate_limited run count = %d, want 1", rateLimitedCount)
	}
}
