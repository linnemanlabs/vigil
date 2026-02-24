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
	EvidenceSigningKeyARN string
	APIPort               int
}

// RegisterFlags binds Config fields to the given FlagSet with defaults inline
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.IntVar(&c.DrainSeconds, "drain-seconds", 60, "seconds to wait for in-flight requests to drain before shutdown (1..300)")
	fs.IntVar(&c.ShutdownBudgetSeconds, "shutdown-budget-seconds", 90, "total seconds for component shutdown after drain (1..300)")
	fs.IntVar(&c.APIPort, "http-port", 8080, "API listen TCP port (1..65535)")
}

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

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
