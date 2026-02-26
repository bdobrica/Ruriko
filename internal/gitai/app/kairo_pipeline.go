package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/common/trace"
	"github.com/bdobrica/Ruriko/internal/gitai/builtin"
	"github.com/bdobrica/Ruriko/internal/gitai/llm"
	"github.com/bdobrica/Ruriko/internal/gitai/store"
)

const (
	kairoNewsRequestPrefix  = "KAIRO_NEWS_REQUEST"
	kumoNewsResponsePrefix  = "KUMO_NEWS_RESPONSE"
	kairoSignificantMovePct = 2.0
	kairoMaxNotifyPerHour   = 2
)

type kairoNewsRequest struct {
	RunID         int64    `json:"run_id"`
	Tickers       []string `json:"tickers"`
	MarketSummary string   `json:"market_summary"`
}

type kumoNewsResponse struct {
	RunID     int64    `json:"run_id"`
	Summary   string   `json:"summary"`
	Headlines []string `json:"headlines"`
	Material  bool     `json:"material"`
}

type kairoTickerMetrics struct {
	Ticker        string
	Price         float64
	ChangePercent float64
	Open          float64
	High          float64
	Low           float64
	PreviousClose float64
	Raw           map[string]interface{}
	Commentary    string
}

func (a *App) tryRunKairoDeterministicTurn(ctx context.Context, roomID, sender, text string) (bool, string, int, error) {
	cfg := a.gosutoLdr.Config()
	if !isCanonicalKairo(cfg) {
		return false, "", 0, nil
	}

	if entries, ok, err := parseKairoPortfolioUpdate(text); ok {
		if err != nil {
			return true, "", 0, err
		}
		if err := a.db.SaveKairoPortfolio(entries); err != nil {
			return true, "", 0, fmt.Errorf("save portfolio: %w", err)
		}
		return true, fmt.Sprintf("✅ Portfolio saved with %d tickers. Future Saito triggers will run analysis automatically.", len(entries)), 0, nil
	}

	if isKumoResponseMessage(sender, text) {
		resp, ok, err := parseKumoNewsResponseMessage(text)
		if !ok {
			return false, "", 0, nil
		}
		if err != nil {
			return true, "", 0, err
		}
		result, toolCalls, err := a.handleKumoNewsResponse(ctx, resp)
		return true, result, toolCalls, err
	}

	if !isSaitoTriggerMessage(sender, text) {
		return false, "", 0, nil
	}

	result, toolCalls, err := a.runKairoAnalysisPipeline(ctx, roomID)
	return true, result, toolCalls, err
}

func isCanonicalKairo(cfg *gosutospec.Config) bool {
	if cfg == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cfg.Metadata.CanonicalName), "kairo")
}

func isSaitoTriggerMessage(sender, text string) bool {
	if matrixSenderLocalpart(sender) == "saito" {
		return true
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, "saito scheduled trigger") || strings.Contains(lower, "portfolio analysis cycle")
}

func isKumoResponseMessage(sender, text string) bool {
	if matrixSenderLocalpart(sender) != "kumo" {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(text), kumoNewsResponsePrefix+" ")
}

func matrixSenderLocalpart(sender string) string {
	if !strings.HasPrefix(sender, "@") {
		return ""
	}
	i := strings.Index(sender, ":")
	if i <= 1 {
		return ""
	}
	return strings.ToLower(sender[1:i])
}

func parseKairoPortfolioUpdate(text string) ([]store.PortfolioPosition, bool, error) {
	raw := strings.TrimSpace(text)
	lower := strings.ToLower(raw)
	portfolioBody := ""

	switch {
	case strings.HasPrefix(lower, "portfolio:"):
		portfolioBody = strings.TrimSpace(raw[len("portfolio:"):])
	case strings.HasPrefix(lower, "portfolio "):
		portfolioBody = strings.TrimSpace(raw[len("portfolio "):])
	case strings.HasPrefix(lower, "set portfolio "):
		portfolioBody = strings.TrimSpace(raw[len("set portfolio "):])
	default:
		return nil, false, nil
	}

	if portfolioBody == "" {
		return nil, true, fmt.Errorf("portfolio update is empty; expected e.g. portfolio: AAPL=50,MSFT=50")
	}

	if strings.HasPrefix(portfolioBody, "[") {
		var positions []store.PortfolioPosition
		if err := json.Unmarshal([]byte(portfolioBody), &positions); err != nil {
			return nil, true, fmt.Errorf("invalid portfolio JSON: %w", err)
		}
		if err := validatePortfolio(positions); err != nil {
			return nil, true, err
		}
		normalisePortfolio(positions)
		return positions, true, nil
	}

	tokens := strings.Split(portfolioBody, ",")
	positions := make([]store.PortfolioPosition, 0, len(tokens))
	for _, token := range tokens {
		pair := strings.TrimSpace(token)
		if pair == "" {
			continue
		}
		sep := "="
		if strings.Contains(pair, ":") && !strings.Contains(pair, "=") {
			sep = ":"
		}
		parts := strings.SplitN(pair, sep, 2)
		if len(parts) != 2 {
			return nil, true, fmt.Errorf("invalid portfolio pair %q; expected TICKER=ALLOCATION", pair)
		}
		ticker := strings.ToUpper(strings.TrimSpace(parts[0]))
		allocationStr := strings.TrimSpace(parts[1])
		allocation, err := strconv.ParseFloat(allocationStr, 64)
		if err != nil {
			return nil, true, fmt.Errorf("invalid allocation for %s: %w", ticker, err)
		}
		positions = append(positions, store.PortfolioPosition{Ticker: ticker, Allocation: allocation})
	}

	if err := validatePortfolio(positions); err != nil {
		return nil, true, err
	}
	normalisePortfolio(positions)
	return positions, true, nil
}

func validatePortfolio(positions []store.PortfolioPosition) error {
	if len(positions) == 0 {
		return fmt.Errorf("portfolio has no positions")
	}
	seen := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		ticker := strings.TrimSpace(strings.ToUpper(p.Ticker))
		if ticker == "" {
			return fmt.Errorf("portfolio ticker cannot be empty")
		}
		if _, exists := seen[ticker]; exists {
			return fmt.Errorf("portfolio contains duplicate ticker %s", ticker)
		}
		seen[ticker] = struct{}{}
		if p.Allocation <= 0 {
			return fmt.Errorf("allocation for %s must be > 0", ticker)
		}
	}
	return nil
}

func normalisePortfolio(positions []store.PortfolioPosition) {
	for i := range positions {
		positions[i].Ticker = strings.ToUpper(strings.TrimSpace(positions[i].Ticker))
	}
	sort.Slice(positions, func(i, j int) bool {
		return positions[i].Ticker < positions[j].Ticker
	})
}

func (a *App) runKairoAnalysisPipeline(ctx context.Context, roomID string) (string, int, error) {
	portfolio, found, err := a.db.GetKairoPortfolio()
	if err != nil {
		return "", 0, fmt.Errorf("load portfolio: %w", err)
	}

	if !found {
		prompt := "I don't have your portfolio yet. Please DM me in this format: portfolio: AAPL=50,MSFT=50 (allocations as percentages)."
		if err := a.sendKairoTargetMessage(ctx, "user", prompt); err != nil {
			return "", 1, fmt.Errorf("request missing portfolio from user: %w", err)
		}
		_, _ = a.db.SaveKairoAnalysisRun(store.KairoAnalysisRun{
			TraceID:       trace.FromContext(ctx),
			TriggerSource: "saito",
			RoomID:        roomID,
			Status:        "waiting_portfolio",
			Summary:       "portfolio missing",
			Commentary:    "requested portfolio from user",
		}, nil)
		return "⏸️ Portfolio is not configured yet. I requested it from user via Matrix DM.", 1, nil
	}

	tickers := make([]store.KairoAnalysisTicker, 0, len(portfolio))
	parts := make([]string, 0, len(portfolio))
	for _, p := range portfolio {
		metrics, err := a.fetchKairoTickerMetrics(ctx, p.Ticker)
		if err != nil {
			return "", 0, fmt.Errorf("fetch market data for %s: %w", p.Ticker, err)
		}
		tickers = append(tickers, store.KairoAnalysisTicker{
			Ticker:        p.Ticker,
			Allocation:    p.Allocation,
			Price:         metrics.Price,
			ChangePercent: metrics.ChangePercent,
			Open:          metrics.Open,
			High:          metrics.High,
			Low:           metrics.Low,
			PreviousClose: metrics.PreviousClose,
			Metrics:       metrics.Raw,
			Commentary:    metrics.Commentary,
		})
		parts = append(parts, fmt.Sprintf("%s %.2f%% ($%.2f)", p.Ticker, metrics.ChangePercent, metrics.Price))
	}

	summary := "Kairo portfolio snapshot: " + strings.Join(parts, "; ")
	commentary := buildKairoRunCommentary(tickers)
	runID, err := a.db.SaveKairoAnalysisRun(store.KairoAnalysisRun{
		TraceID:       trace.FromContext(ctx),
		TriggerSource: "saito",
		RoomID:        roomID,
		Status:        "awaiting_news",
		Summary:       summary,
		Commentary:    commentary,
	}, tickers)
	if err != nil {
		return "", 0, fmt.Errorf("persist analysis run: %w", err)
	}

	requestTickers := selectKairoNewsRequestTickers(tickers)
	request := kairoNewsRequest{
		RunID:         runID,
		Tickers:       requestTickers,
		MarketSummary: summary,
	}
	requestMsg, err := buildKairoNewsRequestMessage(request)
	if err != nil {
		return "", 0, fmt.Errorf("build Kumo news request: %w", err)
	}

	if err := a.sendKairoTargetMessage(ctx, "kumo", requestMsg); err != nil {
		return "", 1, fmt.Errorf("send news request to kumo: %w", err)
	}

	return fmt.Sprintf("✅ Kairo analysis completed (run_id=%d). Requested news context from Kumo.", runID), 1, nil
}

func (a *App) handleKumoNewsResponse(ctx context.Context, resp kumoNewsResponse) (string, int, error) {
	run, found, err := a.db.GetKairoAnalysisRun(resp.RunID)
	if err != nil {
		return "", 0, fmt.Errorf("load analysis run %d: %w", resp.RunID, err)
	}
	if !found {
		return fmt.Sprintf("ℹ️ Received Kumo response for unknown run_id=%d; ignoring.", resp.RunID), 0, nil
	}

	maxAbs, err := a.db.GetKairoAnalysisMaxAbsChange(resp.RunID)
	if err != nil {
		return "", 0, fmt.Errorf("load analysis max move for run %d: %w", resp.RunID, err)
	}

	material := resp.Material || maxAbs >= kairoSignificantMovePct
	enrichedCommentary := buildKairoEnrichedCommentary(run.Commentary, resp, maxAbs, material)

	if !material {
		if err := a.db.UpdateKairoAnalysisRunStatus(resp.RunID, "logged_no_notify", enrichedCommentary); err != nil {
			return "", 0, fmt.Errorf("update non-significant run status: %w", err)
		}
		return fmt.Sprintf("✅ Kairo run_id=%d updated with Kumo news; not significant, logged without notifying user.", resp.RunID), 0, nil
	}

	notifiedLastHour, err := a.db.CountKairoNotifiedRunsLastHour()
	if err != nil {
		return "", 0, fmt.Errorf("count recent notifications: %w", err)
	}
	if notifiedLastHour >= kairoMaxNotifyPerHour {
		enrichedCommentary += fmt.Sprintf("\nNotification skipped: hourly rate limit reached (%d/%d).", notifiedLastHour, kairoMaxNotifyPerHour)
		if err := a.db.UpdateKairoAnalysisRunStatus(resp.RunID, "rate_limited", enrichedCommentary); err != nil {
			return "", 0, fmt.Errorf("update rate-limited run status: %w", err)
		}
		return fmt.Sprintf("ℹ️ Kairo run_id=%d is significant but user notification was rate-limited.", resp.RunID), 0, nil
	}

	finalReport := buildKairoFinalReport(resp.RunID, run.Summary, maxAbs, resp)
	if err := a.sendKairoTargetMessage(ctx, "user", finalReport); err != nil {
		return "", 1, fmt.Errorf("send final report to user: %w", err)
	}

	if err := a.db.UpdateKairoAnalysisRunStatus(resp.RunID, "notified", enrichedCommentary); err != nil {
		return "", 1, fmt.Errorf("mark run as notified: %w", err)
	}

	return fmt.Sprintf("✅ Kairo run_id=%d finalized with Kumo news and user notified.", resp.RunID), 1, nil
}

func selectKairoNewsRequestTickers(tickers []store.KairoAnalysisTicker) []string {
	if len(tickers) == 0 {
		return nil
	}
	selected := make([]string, 0, len(tickers))
	for _, t := range tickers {
		if math.Abs(t.ChangePercent) >= 1.0 {
			selected = append(selected, strings.ToUpper(strings.TrimSpace(t.Ticker)))
		}
	}
	if len(selected) == 0 {
		strongest := tickers[0]
		for i := 1; i < len(tickers); i++ {
			if math.Abs(tickers[i].ChangePercent) > math.Abs(strongest.ChangePercent) {
				strongest = tickers[i]
			}
		}
		selected = append(selected, strings.ToUpper(strings.TrimSpace(strongest.Ticker)))
	}
	sort.Strings(selected)
	return dedupeStrings(selected)
}

func buildKairoNewsRequestMessage(req kairoNewsRequest) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return kairoNewsRequestPrefix + " " + string(b), nil
}

func parseKairoNewsRequestMessage(text string) (kairoNewsRequest, bool, error) {
	raw := strings.TrimSpace(text)
	prefix := kairoNewsRequestPrefix + " "
	if !strings.HasPrefix(raw, prefix) {
		return kairoNewsRequest{}, false, nil
	}
	var req kairoNewsRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(raw, prefix))), &req); err != nil {
		return kairoNewsRequest{}, true, fmt.Errorf("invalid Kairo news request payload: %w", err)
	}
	if req.RunID <= 0 {
		return kairoNewsRequest{}, true, fmt.Errorf("invalid Kairo news request: run_id must be > 0")
	}
	req.Tickers = sanitizeTickers(req.Tickers)
	if len(req.Tickers) == 0 {
		return kairoNewsRequest{}, true, fmt.Errorf("invalid Kairo news request: no tickers")
	}
	return req, true, nil
}

func buildKumoNewsResponseMessage(resp kumoNewsResponse) (string, error) {
	b, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return kumoNewsResponsePrefix + " " + string(b), nil
}

func parseKumoNewsResponseMessage(text string) (kumoNewsResponse, bool, error) {
	raw := strings.TrimSpace(text)
	prefix := kumoNewsResponsePrefix + " "
	if !strings.HasPrefix(raw, prefix) {
		return kumoNewsResponse{}, false, nil
	}
	var resp kumoNewsResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(raw, prefix))), &resp); err != nil {
		return kumoNewsResponse{}, true, fmt.Errorf("invalid Kumo news response payload: %w", err)
	}
	if resp.RunID <= 0 {
		return kumoNewsResponse{}, true, fmt.Errorf("invalid Kumo news response: run_id must be > 0")
	}
	resp.Headlines = normalizeHeadlines(resp.Headlines)
	resp.Summary = strings.TrimSpace(resp.Summary)
	return resp, true, nil
}

func buildKairoEnrichedCommentary(existing string, resp kumoNewsResponse, maxAbs float64, material bool) string {
	commentary := strings.TrimSpace(existing)
	if commentary == "" {
		commentary = "Kairo analysis commentary unavailable."
	}
	if resp.Summary != "" {
		commentary += "\nNews summary: " + resp.Summary
	}
	if len(resp.Headlines) > 0 {
		commentary += "\nTop headlines: " + strings.Join(resp.Headlines, " | ")
	}
	commentary += fmt.Sprintf("\nSignificance: market_max_abs_change=%.2f%%, news_material=%t, final_material=%t.", maxAbs, resp.Material, material)
	return commentary
}

func buildKairoFinalReport(runID int64, marketSummary string, maxAbs float64, resp kumoNewsResponse) string {
	lines := []string{
		fmt.Sprintf("📊 Kairo final report (run_id=%d)", runID),
		marketSummary,
		fmt.Sprintf("Market significance: max absolute move %.2f%%.", maxAbs),
	}
	if resp.Summary != "" {
		lines = append(lines, "📰 Kumo news context: "+resp.Summary)
	}
	if len(resp.Headlines) > 0 {
		lines = append(lines, "Headlines: "+strings.Join(resp.Headlines, " | "))
	}
	return strings.Join(lines, "\n")
}

func sanitizeTickers(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, v := range values {
		ticker := strings.ToUpper(strings.TrimSpace(v))
		if ticker != "" {
			normalized = append(normalized, ticker)
		}
	}
	sort.Strings(normalized)
	return dedupeStrings(normalized)
}

func normalizeHeadlines(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, v := range values {
		h := strings.TrimSpace(v)
		if h != "" {
			trimmed = append(trimmed, h)
		}
	}
	return dedupeStrings(trimmed)
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func buildKairoRunCommentary(tickers []store.KairoAnalysisTicker) string {
	if len(tickers) == 0 {
		return "No tickers available for commentary."
	}

	strongestIdx := 0
	for i := 1; i < len(tickers); i++ {
		if math.Abs(tickers[i].ChangePercent) > math.Abs(tickers[strongestIdx].ChangePercent) {
			strongestIdx = i
		}
	}

	lead := tickers[strongestIdx]
	direction := "up"
	if lead.ChangePercent < 0 {
		direction = "down"
	}
	return fmt.Sprintf("Largest move: %s %s %.2f%%. Data source: finnhub MCP.", lead.Ticker, direction, math.Abs(lead.ChangePercent))
}

func (a *App) sendKairoTargetMessage(ctx context.Context, target, message string) error {
	raw, err := json.Marshal(map[string]string{
		"target":  target,
		"message": message,
	})
	if err != nil {
		return fmt.Errorf("marshal matrix.send_message args: %w", err)
	}

	_, err = a.executeBuiltinTool(ctx, "kairo-pipeline", llm.ToolCall{
		ID:   "kairo-pipeline-message",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      builtin.MatrixSendToolName,
			Arguments: string(raw),
		},
	})
	return err
}

func (a *App) fetchKairoTickerMetrics(ctx context.Context, ticker string) (kairoTickerMetrics, error) {
	if a.kairoMarketDataFetcher != nil {
		metrics, err := a.kairoMarketDataFetcher(ctx, ticker)
		if err != nil {
			return kairoTickerMetrics{}, err
		}
		metrics.Ticker = ticker
		return metrics, nil
	}

	client := a.supv.Get("finnhub")
	if client == nil {
		return kairoTickerMetrics{}, fmt.Errorf("MCP server \"finnhub\" is not running")
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		return kairoTickerMetrics{}, fmt.Errorf("list finnhub tools: %w", err)
	}

	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}

	ordered := selectFinnhubQuoteTools(toolNames)
	if len(ordered) == 0 {
		return kairoTickerMetrics{}, fmt.Errorf("no candidate finnhub quote tool found")
	}

	argVariants := []map[string]interface{}{
		{"symbol": ticker},
		{"ticker": ticker},
		{"code": ticker},
	}

	var lastErr error
	for _, toolName := range ordered {
		for _, args := range argVariants {
			res, err := client.CallTool(ctx, toolName, args)
			if err != nil {
				lastErr = err
				continue
			}
			if res.IsError {
				lastErr = fmt.Errorf("tool returned error")
				continue
			}
			metrics, ok := parseFinnhubMetrics(ticker, res)
			if ok {
				return metrics, nil
			}
			lastErr = fmt.Errorf("unable to parse metrics from tool result")
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no successful finnhub result")
	}
	return kairoTickerMetrics{}, lastErr
}

func selectFinnhubQuoteTools(toolNames []string) []string {
	preferred := []string{
		"get_quote",
		"quote",
		"stock_quote",
		"get_stock_quote",
		"price",
		"get_price",
	}

	set := make(map[string]struct{}, len(toolNames))
	for _, n := range toolNames {
		set[n] = struct{}{}
	}

	out := make([]string, 0, len(toolNames))
	for _, n := range preferred {
		if _, ok := set[n]; ok {
			out = append(out, n)
		}
	}

	for _, n := range toolNames {
		lower := strings.ToLower(n)
		if strings.Contains(lower, "quote") || strings.Contains(lower, "price") {
			if !containsString(out, n) {
				out = append(out, n)
			}
		}
	}

	return out
}

func parseFinnhubMetrics(ticker string, result interface { /* *mcp.CallToolResult */
}) (kairoTickerMetrics, bool) {
	callResult, ok := result.(interface {
		GetContent() []map[string]interface{}
	})
	if ok {
		_ = callResult
	}

	// We keep this parser decoupled from the concrete type to simplify tests.
	// The caller passes *mcp.CallToolResult and we unwrap it via JSON.
	b, err := json.Marshal(result)
	if err != nil {
		return kairoTickerMetrics{}, false
	}

	var payload struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return kairoTickerMetrics{}, false
	}

	for _, item := range payload.Content {
		raw := strings.TrimSpace(item.Text)
		if raw == "" {
			continue
		}
		metricsMap, ok := parseFirstJSONObject(raw)
		if !ok {
			continue
		}

		price, ok := extractFloat(metricsMap, "c", "current", "price", "last")
		if !ok {
			continue
		}
		open, _ := extractFloat(metricsMap, "o", "open")
		high, _ := extractFloat(metricsMap, "h", "high")
		low, _ := extractFloat(metricsMap, "l", "low")
		prevClose, _ := extractFloat(metricsMap, "pc", "previous_close", "previousClose")
		changePct, hasChange := extractFloat(metricsMap, "dp", "change_percent", "changePercent")
		if !hasChange && prevClose != 0 {
			changePct = ((price - prevClose) / prevClose) * 100
		}

		commentary := "stable"
		if math.Abs(changePct) >= 3 {
			commentary = "material move"
		} else if math.Abs(changePct) >= 1 {
			commentary = "notable move"
		}

		return kairoTickerMetrics{
			Ticker:        ticker,
			Price:         price,
			ChangePercent: changePct,
			Open:          open,
			High:          high,
			Low:           low,
			PreviousClose: prevClose,
			Raw:           metricsMap,
			Commentary:    commentary,
		}, true
	}

	return kairoTickerMetrics{}, false
}

func parseFirstJSONObject(raw string) (map[string]interface{}, bool) {
	if strings.HasPrefix(raw, "[") {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &arr); err == nil && len(arr) > 0 {
			return arr[0], true
		}
	}
	if strings.HasPrefix(raw, "{") {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &obj); err == nil {
			return obj, true
		}
	}
	return nil, false
}

func extractFloat(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			return n, true
		case float32:
			return float64(n), true
		case int:
			return float64(n), true
		case int64:
			return float64(n), true
		case json.Number:
			f, err := n.Float64()
			if err == nil {
				return f, true
			}
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
