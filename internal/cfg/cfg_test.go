package cfg

import (
	"flag"
	"math"
	"strings"
	"testing"
)

// validBase returns a Config with all required fields set to valid values.
func validBase() Config {
	return Config{
		DrainSeconds:          60,
		ShutdownBudgetSeconds: 90,
		APIPort:               8080,
		PrometheusEndpoint:    "http://localhost:9090",
		ClaudeAPIKey:          "sk-test-key",
		ClaudeModel:           "claude-sonnet-4-20250514",
		APIToken:              "test-token-123",
	}
}

func TestRegisterFlags_Defaults(t *testing.T) {
	t.Parallel()

	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c.RegisterFlags(fs)

	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse empty args: %v", err)
	}

	if c.DrainSeconds != 60 {
		t.Errorf("DrainSeconds = %d, want 60", c.DrainSeconds)
	}
	if c.ShutdownBudgetSeconds != 90 {
		t.Errorf("ShutdownBudgetSeconds = %d, want 90", c.ShutdownBudgetSeconds)
	}
	if c.APIPort != 8080 {
		t.Errorf("APIPort = %d, want 8080", c.APIPort)
	}
	if c.ClaudeModel != "claude-sonnet-4-20250514" {
		t.Errorf("ClaudeModel = %q, want %q", c.ClaudeModel, "claude-sonnet-4-20250514")
	}
}

func TestRegisterFlags_Override(t *testing.T) {
	t.Parallel()

	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c.RegisterFlags(fs)

	args := []string{
		"-drain-seconds", "30",
		"-shutdown-budget-seconds", "120",
		"-http-port", "9090",
		"-prometheus-endpoint", "http://prom:9090",
		"-claude-api-key", "sk-override",
		"-claude-model", "claude-opus-4-20250514",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args: %v", err)
	}

	if c.DrainSeconds != 30 {
		t.Errorf("DrainSeconds = %d, want 30", c.DrainSeconds)
	}
	if c.ShutdownBudgetSeconds != 120 {
		t.Errorf("ShutdownBudgetSeconds = %d, want 120", c.ShutdownBudgetSeconds)
	}
	if c.APIPort != 9090 {
		t.Errorf("APIPort = %d, want 9090", c.APIPort)
	}
	if c.PrometheusEndpoint != "http://prom:9090" {
		t.Errorf("PrometheusEndpoint = %q, want %q", c.PrometheusEndpoint, "http://prom:9090")
	}
	if c.ClaudeAPIKey != "sk-override" {
		t.Errorf("ClaudeAPIKey = %q, want %q", c.ClaudeAPIKey, "sk-override")
	}
	if c.ClaudeModel != "claude-opus-4-20250514" {
		t.Errorf("ClaudeModel = %q, want %q", c.ClaudeModel, "claude-opus-4-20250514")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       Config
		wantErr   bool
		errSubstr []string // substrings that must appear in error message
	}{
		{
			name:    "defaults are valid",
			cfg:     validBase(),
			wantErr: false,
		},
		{
			name: "minimum valid values",
			cfg: Config{
				DrainSeconds: 1, ShutdownBudgetSeconds: 2, APIPort: 1,
				PrometheusEndpoint: "http://p", ClaudeAPIKey: "k", ClaudeModel: "m", APIToken: "t",
			},
			wantErr: false,
		},
		{
			name: "maximum valid values",
			cfg: Config{
				DrainSeconds: 299, ShutdownBudgetSeconds: 300, APIPort: 65535,
				PrometheusEndpoint: "http://p", ClaudeAPIKey: "k", ClaudeModel: "m", APIToken: "t",
			},
			wantErr: false,
		},
		// DrainSeconds boundaries
		{
			name:      "drain zero",
			cfg:       Config{DrainSeconds: 0, ShutdownBudgetSeconds: 90, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"DRAIN_SECONDS"},
		},
		{
			name:      "drain negative",
			cfg:       Config{DrainSeconds: -1, ShutdownBudgetSeconds: 90, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"DRAIN_SECONDS"},
		},
		{
			name:      "drain above max",
			cfg:       Config{DrainSeconds: 301, ShutdownBudgetSeconds: 302, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"DRAIN_SECONDS"},
		},
		{
			name: "drain at lower bound",
			cfg: Config{
				DrainSeconds: 1, ShutdownBudgetSeconds: 90, APIPort: 8080,
				PrometheusEndpoint: "http://p", ClaudeAPIKey: "k", ClaudeModel: "m", APIToken: "t",
			},
			wantErr: false,
		},
		{
			name:    "drain at upper bound",
			cfg:     Config{DrainSeconds: 300, ShutdownBudgetSeconds: 300, APIPort: 8080},
			wantErr: true, // budget must be greater than drain
		},
		// ShutdownBudgetSeconds boundaries
		{
			name:      "budget zero",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: 0, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"SHUTDOWN_BUDGET_SECONDS"},
		},
		{
			name:      "budget negative",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: -1, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"SHUTDOWN_BUDGET_SECONDS"},
		},
		{
			name:      "budget above max",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: 301, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"SHUTDOWN_BUDGET_SECONDS"},
		},
		// Cross-field: budget vs drain
		{
			name:      "budget equals drain",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: 60, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"must be greater than"},
		},
		{
			name:      "budget less than drain",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: 30, APIPort: 8080},
			wantErr:   true,
			errSubstr: []string{"must be greater than"},
		},
		{
			name: "budget is drain plus one",
			cfg: Config{
				DrainSeconds: 60, ShutdownBudgetSeconds: 61, APIPort: 8080,
				PrometheusEndpoint: "http://p", ClaudeAPIKey: "k", ClaudeModel: "m", APIToken: "t",
			},
			wantErr: false,
		},
		// APIPort boundaries
		{
			name:      "port zero",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: 0},
			wantErr:   true,
			errSubstr: []string{"HTTP_PORT"},
		},
		{
			name:      "port negative",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: -1},
			wantErr:   true,
			errSubstr: []string{"HTTP_PORT"},
		},
		{
			name:      "port above max",
			cfg:       Config{DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: 65536},
			wantErr:   true,
			errSubstr: []string{"HTTP_PORT"},
		},
		// New string field validation
		{
			name: "empty prometheus endpoint",
			cfg: Config{
				DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: 8080,
				PrometheusEndpoint: "", ClaudeAPIKey: "k", ClaudeModel: "m", APIToken: "t",
			},
			wantErr:   true,
			errSubstr: []string{"PROMETHEUS_ENDPOINT"},
		},
		{
			name: "empty api token",
			cfg: Config{
				DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: 8080,
				PrometheusEndpoint: "http://p", ClaudeAPIKey: "k", ClaudeModel: "m", APIToken: "",
			},
			wantErr:   true,
			errSubstr: []string{"API_TOKEN"},
		},
		{
			name: "empty claude api key",
			cfg: Config{
				DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: 8080,
				PrometheusEndpoint: "http://p", ClaudeAPIKey: "", ClaudeModel: "m", APIToken: "t",
			},
			wantErr:   true,
			errSubstr: []string{"CLAUDE_API_KEY"},
		},
		{
			name: "empty claude model",
			cfg: Config{
				DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: 8080,
				PrometheusEndpoint: "http://p", ClaudeAPIKey: "k", ClaudeModel: "", APIToken: "t",
			},
			wantErr:   true,
			errSubstr: []string{"CLAUDE_MODEL"},
		},
		// Error accumulation: all fields invalid
		{
			name:      "all fields invalid",
			cfg:       Config{DrainSeconds: 0, ShutdownBudgetSeconds: 0, APIPort: 0},
			wantErr:   true,
			errSubstr: []string{"DRAIN_SECONDS", "SHUTDOWN_BUDGET_SECONDS", "HTTP_PORT", "PROMETHEUS_ENDPOINT", "API_TOKEN", "CLAUDE_API_KEY", "CLAUDE_MODEL"},
		},
		// Extreme values
		{
			name:      "extreme negative values",
			cfg:       Config{DrainSeconds: math.MinInt32, ShutdownBudgetSeconds: math.MinInt32, APIPort: math.MinInt32},
			wantErr:   true,
			errSubstr: []string{"DRAIN_SECONDS", "SHUTDOWN_BUDGET_SECONDS", "HTTP_PORT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				errMsg := err.Error()
				for _, sub := range tt.errSubstr {
					if !strings.Contains(errMsg, sub) {
						t.Errorf("error %q does not contain %q", errMsg, sub)
					}
				}
			}
		})
	}
}

func FuzzValidate(f *testing.F) {
	// Seeds: defaults, boundaries, extremes
	seeds := []struct {
		drain, budget, port             int
		promEndpoint, key, model, token string
	}{
		{60, 90, 8080, "http://localhost:9090", "sk-test", "claude-sonnet", "tok"},
		{1, 2, 1, "http://p", "k", "m", "t"},
		{299, 300, 65535, "http://p", "k", "m", "t"},
		{0, 0, 0, "", "", "", ""},
		{-1, -1, -1, "", "", "", ""},
		{300, 300, 65535, "http://p", "k", "m", "t"},
		{301, 302, 65536, "", "", "", ""},
		{150, 100, 8080, "http://p", "k", "m", "t"},
		{math.MinInt32, math.MinInt32, math.MinInt32, "", "", "", ""},
		{math.MaxInt32, math.MaxInt32, math.MaxInt32, "", "", "", ""},
	}
	for _, s := range seeds {
		f.Add(s.drain, s.budget, s.port, s.promEndpoint, s.key, s.model, s.token)
	}

	f.Fuzz(func(t *testing.T, drain, budget, port int, promEndpoint, key, model, token string) {
		c := Config{
			DrainSeconds:          drain,
			ShutdownBudgetSeconds: budget,
			APIPort:               port,
			PrometheusEndpoint:    promEndpoint,
			ClaudeAPIKey:          key,
			ClaudeModel:           model,
			APIToken:              token,
		}
		err := c.Validate()

		drainOK := drain >= 1 && drain <= 300
		budgetOK := budget >= 1 && budget <= 300
		portOK := port >= 1 && port <= 65535
		crossOK := budget > drain
		promOK := promEndpoint != ""
		keyOK := key != ""
		modelOK := model != ""
		tokenOK := token != ""

		allValid := drainOK && budgetOK && portOK && crossOK && promOK && keyOK && modelOK && tokenOK

		if allValid && err != nil {
			t.Errorf("expected no error for valid config %+v, got: %v", c, err)
		}
		if !allValid && err == nil {
			t.Errorf("expected error for invalid config %+v, got nil", c)
		}
	})
}
