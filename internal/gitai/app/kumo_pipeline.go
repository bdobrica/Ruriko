package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gosutospec "github.com/bdobrica/Ruriko/common/spec/gosuto"
	"github.com/bdobrica/Ruriko/internal/gitai/mcp"
)

type kumoNewsResult struct {
	Summary   string
	Headlines []string
	Material  bool
}

func (a *App) tryRunKumoDeterministicTurn(ctx context.Context, sender, text string) (bool, string, int, error) {
	cfg := a.gosutoLdr.Config()
	if !isCanonicalKumo(cfg) {
		return false, "", 0, nil
	}
	if matrixSenderLocalpart(sender) != "kairo" {
		return false, "", 0, nil
	}

	req, ok, err := parseKairoNewsRequestMessage(text)
	if !ok {
		return false, "", 0, nil
	}
	if err != nil {
		return true, "", 0, err
	}

	news, err := a.fetchKumoNews(ctx, req)
	if err != nil {
		return true, "", 0, fmt.Errorf("fetch Kumo news: %w", err)
	}

	response := kumoNewsResponse{
		RunID:     req.RunID,
		Summary:   news.Summary,
		Headlines: news.Headlines,
		Material:  news.Material,
	}
	msg, err := buildKumoNewsResponseMessage(response)
	if err != nil {
		return true, "", 0, fmt.Errorf("build Kumo response message: %w", err)
	}
	if err := a.sendKumoTargetMessage(ctx, "kairo", msg); err != nil {
		return true, "", 1, fmt.Errorf("send Kumo response to kairo: %w", err)
	}

	return true, fmt.Sprintf("✅ Kumo returned news context to Kairo (run_id=%d).", req.RunID), 1, nil
}

func isCanonicalKumo(cfg *gosutospec.Config) bool {
	if cfg == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cfg.Metadata.CanonicalName), "kumo")
}

func (a *App) sendKumoTargetMessage(ctx context.Context, target, message string) error {
	return a.sendKairoTargetMessage(ctx, target, message)
}

func (a *App) fetchKumoNews(ctx context.Context, req kairoNewsRequest) (kumoNewsResult, error) {
	if a.kumoNewsFetcher != nil {
		return a.kumoNewsFetcher(ctx, req)
	}

	client := a.supv.Get("brave-search")
	if client == nil {
		return kumoNewsResult{}, fmt.Errorf("MCP server \"brave-search\" is not running")
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		return kumoNewsResult{}, fmt.Errorf("list brave-search tools: %w", err)
	}

	searchTool, ok := chooseKumoSearchTool(tools)
	if !ok {
		return kumoNewsResult{}, fmt.Errorf("no candidate brave-search tool found")
	}

	headlines := make([]string, 0, len(req.Tickers)*2)
	summaries := make([]string, 0, len(req.Tickers))
	material := false

	for _, ticker := range req.Tickers {
		query := fmt.Sprintf("%s stock news latest", ticker)
		resultText, err := callKumoSearchTool(ctx, client, searchTool, query)
		if err != nil {
			return kumoNewsResult{}, fmt.Errorf("search %s news: %w", ticker, err)
		}

		extracted := extractHeadlines(resultText, 2)
		if len(extracted) == 0 {
			extracted = []string{fmt.Sprintf("%s: no clear headline extracted", ticker)}
		}
		headlines = append(headlines, extracted...)
		summaries = append(summaries, fmt.Sprintf("%s: %s", ticker, extracted[0]))
		if hasMaterialNews(strings.Join(extracted, " ") + " " + resultText) {
			material = true
		}
	}

	summary := "News lookup complete. " + strings.Join(summaries, " | ")
	return kumoNewsResult{
		Summary:   strings.TrimSpace(summary),
		Headlines: normalizeHeadlines(headlines),
		Material:  material,
	}, nil
}

func chooseKumoSearchTool(tools []mcp.Tool) (string, bool) {
	preferred := []string{"web_search", "search", "brave_web_search", "brave-search"}
	available := make(map[string]struct{}, len(tools))
	ordered := make([]string, 0, len(tools))
	for _, tool := range tools {
		available[tool.Name] = struct{}{}
		ordered = append(ordered, tool.Name)
	}
	for _, name := range preferred {
		if _, ok := available[name]; ok {
			return name, true
		}
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "search") {
			return name, true
		}
	}
	return "", false
}

func callKumoSearchTool(ctx context.Context, client *mcp.Client, toolName, query string) (string, error) {
	argVariants := []map[string]interface{}{
		{"query": query},
		{"q": query},
		{"search": query},
	}

	var lastErr error
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
		if text := flattenToolText(res); strings.TrimSpace(text) != "" {
			return text, nil
		}
		lastErr = fmt.Errorf("empty tool response")
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no successful search call")
	}
	return "", lastErr
}

func flattenToolText(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if t := strings.TrimSpace(c.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

func extractHeadlines(raw string, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, limit)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "-*•0123456789. ")
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func hasMaterialNews(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{
		"bankruptcy", "fraud", "probe", "investigation", "lawsuit", "downgrade",
		"guidance cut", "earnings miss", "merger", "acquisition", "sec",
		"regulatory", "plunge", "crash", "surge",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
