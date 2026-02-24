package cfg

import (
	"flag"
	"math"
	"strings"
	"testing"
)

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
}

func TestRegisterFlags_Override(t *testing.T) {
	t.Parallel()

	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c.RegisterFlags(fs)

	args := []string{"-drain-seconds", "30", "-shutdown-budget-seconds", "120", "-http-port", "9090"}
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
			cfg:     Config{DrainSeconds: 60, ShutdownBudgetSeconds: 90, APIPort: 8080},
			wantErr: false,
		},
		{
			name:    "minimum valid values",
			cfg:     Config{DrainSeconds: 1, ShutdownBudgetSeconds: 2, APIPort: 1},
			wantErr: false,
		},
		{
			name:    "maximum valid values",
			cfg:     Config{DrainSeconds: 299, ShutdownBudgetSeconds: 300, APIPort: 65535},
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
			name:    "drain at lower bound",
			cfg:     Config{DrainSeconds: 1, ShutdownBudgetSeconds: 90, APIPort: 8080},
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
			name:    "budget is drain plus one",
			cfg:     Config{DrainSeconds: 60, ShutdownBudgetSeconds: 61, APIPort: 8080},
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
		// Error accumulation: all fields invalid
		{
			name:      "all fields invalid",
			cfg:       Config{DrainSeconds: 0, ShutdownBudgetSeconds: 0, APIPort: 0},
			wantErr:   true,
			errSubstr: []string{"DRAIN_SECONDS", "SHUTDOWN_BUDGET_SECONDS", "HTTP_PORT"},
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
	seeds := []struct{ drain, budget, port int }{
		{60, 90, 8080},    // defaults
		{1, 2, 1},         // minimum valid
		{299, 300, 65535}, // maximum valid
		{0, 0, 0},         // all zero
		{-1, -1, -1},      // all negative
		{300, 300, 65535}, // drain == budget boundary
		{301, 302, 65536}, // just above max
		{150, 100, 8080},  // budget < drain
		{math.MinInt32, math.MinInt32, math.MinInt32},
		{math.MaxInt32, math.MaxInt32, math.MaxInt32},
	}
	for _, s := range seeds {
		f.Add(s.drain, s.budget, s.port)
	}

	f.Fuzz(func(t *testing.T, drain, budget, port int) {
		c := Config{
			DrainSeconds:          drain,
			ShutdownBudgetSeconds: budget,
			APIPort:               port,
		}
		err := c.Validate()

		drainOK := drain >= 1 && drain <= 300
		budgetOK := budget >= 1 && budget <= 300
		portOK := port >= 1 && port <= 65535
		crossOK := budget > drain

		allValid := drainOK && budgetOK && portOK && crossOK

		if allValid && err != nil {
			t.Errorf("expected no error for valid config %+v, got: %v", c, err)
		}
		if !allValid && err == nil {
			t.Errorf("expected error for invalid config %+v, got nil", c)
		}
	})
}
