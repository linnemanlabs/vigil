// Package slack sends triage notifications to Slack via incoming webhooks.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/linnemanlabs/vigil/internal/triage"
)

const (
	maxAnalysisLen = 3000
	httpTimeout    = 10 * time.Second
)

// Notifier sends triage results to a Slack webhook.
type Notifier struct {
	webhookURL string
	client     *http.Client
}

// New creates a new Slack notifier. If webhookURL is empty, Send is a no-op.
func New(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: httpTimeout},
	}
}

// Send posts a triage result to the configured Slack webhook.
// If no webhook URL is configured, it returns nil immediately.
func (n *Notifier) Send(ctx context.Context, result *triage.Result) error {
	if n.webhookURL == "" {
		return nil
	}

	msg := buildMessage(result)

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("slack: marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req) //nolint:gosec // G704: webhookURL is from trusted config, not user input
	if err != nil {
		return fmt.Errorf("slack: post webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack: webhook returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func buildMessage(r *triage.Result) map[string]any {
	return map[string]any{
		"blocks": []map[string]any{
			headerBlock(r),
			{"type": "divider"},
			fieldsBlock(r),
			{"type": "divider"},
			analysisBlock(r),
			{"type": "divider"},
			contextBlock(r),
		},
	}
}

func headerBlock(r *triage.Result) map[string]any {
	emoji := severityEmoji(r.Status, r.Severity)
	title := "Triage Complete"
	if r.Status == triage.StatusFailed {
		title = "Triage Failed"
	}
	text := fmt.Sprintf("%s %s: %s", emoji, title, r.Alert)

	return map[string]any{
		"type": "header",
		"text": map[string]any{
			"type": "plain_text",
			"text": text,
		},
	}
}

func fieldsBlock(r *triage.Result) map[string]any {
	fields := []map[string]any{
		{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Status:* %s", r.Status),
		},
		{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Severity:* %s", r.Severity),
		},
		{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Duration:* %.1fs", r.Duration),
		},
		{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Model:* %s", shortModel(r.Model)),
		},
		{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Tokens:* %d", r.TokensUsed),
		},
		{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Tool calls:* %d", r.ToolCalls),
		},
	}

	return map[string]any{
		"type":   "section",
		"fields": fields,
	}
}

func analysisBlock(r *triage.Result) map[string]any {
	text := truncate(r.Analysis, maxAnalysisLen)
	if text == "" {
		text = "_No analysis available._"
	}

	return map[string]any{
		"type": "section",
		"text": map[string]any{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Analysis*\n\n%s", text),
		},
	}
}

func contextBlock(r *triage.Result) map[string]any {
	ts := r.CompletedAt
	if ts.IsZero() {
		ts = r.CreatedAt
	}

	elements := []map[string]any{
		{
			"type": "mrkdwn",
			"text": fmt.Sprintf("vigil • triage %s • %s", r.ID, ts.UTC().Format("2006-01-02 15:04 UTC")),
		},
	}

	return map[string]any{
		"type":     "context",
		"elements": elements,
	}
}

func severityEmoji(status triage.Status, severity string) string {
	if status == triage.StatusFailed {
		return "\U0001f534" // red circle
	}
	switch strings.ToLower(severity) {
	case "critical":
		return "\U0001f534" // red circle
	case "warning":
		return "\U0001f7e1" // yellow circle
	default:
		return "\U0001f7e2" // green circle
	}
}

// dateModelRe matches model names ending with a YYYYMMDD date suffix.
var dateModelRe = regexp.MustCompile(`-\d{8}$`)

func shortModel(model string) string {
	return dateModelRe.ReplaceAllString(model, "")
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}
