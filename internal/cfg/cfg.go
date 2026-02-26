package cfg

import (
	"errors"
	"flag"
	"fmt"
)

// Config adds log-specific configuration fields to the
// common cfg.Registerable and cfg.Validatable interfaces
type Config struct {
	DrainSeconds          int
	ShutdownBudgetSeconds int
	APIPort               int
	PrometheusEndpoint    string
	PrometheusTenantID    string
	LokiEndpoint          string
	LokiTenantID          string
	ClaudeAPIKey          string
	ClaudeModel           string
	DatabaseURL           string
	SlackWebhookURL       string
}

// RegisterFlags binds Config fields to the given FlagSet with defaults inline
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.IntVar(&c.DrainSeconds, "drain-seconds", 60, "seconds to wait for in-flight requests to drain before shutdown (1..300)")
	fs.IntVar(&c.ShutdownBudgetSeconds, "shutdown-budget-seconds", 90, "total seconds for component shutdown after drain (1..300)")
	fs.IntVar(&c.APIPort, "http-port", 8080, "API listen TCP port (1..65535)")
	fs.StringVar(&c.PrometheusEndpoint, "prometheus-endpoint", "", "Prometheus endpoint for metrics collection by tool use")
	fs.StringVar(&c.PrometheusTenantID, "prometheus-tenant-id", "", "Prometheus tenant ID for multi-tenant setups")
	fs.StringVar(&c.ClaudeAPIKey, "claude-api-key", "", "API key for accessing the Claude LLM provider")
	fs.StringVar(&c.ClaudeModel, "claude-model", "claude-sonnet-4-20250514", "Claude model to use)")
	fs.StringVar(&c.DatabaseURL, "database-url", "", "PostgreSQL connection URL (empty = in-memory store)")
	fs.StringVar(&c.LokiEndpoint, "loki-endpoint", "", "Loki endpoint for log collection by tool use")
	fs.StringVar(&c.LokiTenantID, "loki-tenant-id", "", "Loki tenant ID for multi-tenant setups")
	fs.StringVar(&c.SlackWebhookURL, "slack-webhook-url", "", "Slack webhook URL for notifications")
}

// Validate checks all configuration fields for correctness.
// It returns an error if any field is invalid, or nil if all fields are valid.
func (c *Config) Validate() error {
	var errs []error

	// Drain and shutdown budgets
	if c.DrainSeconds <= 0 || c.DrainSeconds > 300 {
		errs = append(errs, fmt.Errorf("invalid DRAIN_SECONDS %d (must be 1..300)", c.DrainSeconds))
	}
	if c.ShutdownBudgetSeconds <= 0 || c.ShutdownBudgetSeconds > 300 {
		errs = append(errs, fmt.Errorf("invalid SHUTDOWN_BUDGET_SECONDS %d (must be 1..300)", c.ShutdownBudgetSeconds))
	}

	// Shutdown budget must be greater than drain time
	if c.ShutdownBudgetSeconds <= c.DrainSeconds {
		errs = append(errs, fmt.Errorf("SHUTDOWN_BUDGET_SECONDS %d must be greater than DRAIN_SECONDS %d", c.ShutdownBudgetSeconds, c.DrainSeconds))
	}

	// API port must be valid TCP port number
	if c.APIPort <= 0 || c.APIPort > 65535 {
		errs = append(errs, fmt.Errorf("invalid HTTP_PORT %d (must be 1..65535)", c.APIPort))
	}

	// Prometheus endpoint is required for metrics collection by tools
	if c.PrometheusEndpoint == "" {
		errs = append(errs, errors.New("PROMETHEUS_ENDPOINT is required"))
	}

	// Claude API key is required for LLM access
	if c.ClaudeAPIKey == "" {
		errs = append(errs, errors.New("CLAUDE_API_KEY is required"))
	}

	// Claude model is required for LLM access
	if c.ClaudeModel == "" {
		errs = append(errs, errors.New("CLAUDE_MODEL is required"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
