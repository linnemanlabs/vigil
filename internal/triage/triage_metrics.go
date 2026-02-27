package triage

import "github.com/prometheus/client_golang/prometheus"

// Metrics holds Prometheus metrics for the triage subsystem.
type Metrics struct {
	TriagesTotal    *prometheus.CounterVec
	TriageDuration  *prometheus.HistogramVec
	TriageLLMTime   *prometheus.HistogramVec
	TriageToolTime  prometheus.Histogram
	TriageTokensIn  prometheus.Histogram
	TriageTokensOut prometheus.Histogram
	TriageToolCalls prometheus.Histogram
	LLMCallsTotal   prometheus.Counter
	LLMTokensIn     prometheus.Counter
	LLMTokensOut    prometheus.Counter
	LLMDuration     prometheus.Histogram
	ToolCallsTotal  *prometheus.CounterVec
	ToolDuration    *prometheus.HistogramVec
	ToolInputBytes  *prometheus.HistogramVec
	ToolOutputBytes *prometheus.HistogramVec
	SubmitsTotal    *prometheus.CounterVec
}

// NewMetrics registers and returns triage metrics on the given registerer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		TriagesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigil_triages_total",
			Help: "Total triage runs by final status.",
		}, []string{"status"}),
		TriageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigil_triage_duration_seconds",
			Help:    "Duration of triage runs in seconds.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s .. ~512s
		}, []string{"status", "model"}),
		TriageLLMTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigil_triage_llm_time_seconds",
			Help:    "Total LLM time per triage run in seconds.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s .. ~512s
		}, []string{"model"}),
		TriageToolTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigil_triage_tool_time_seconds",
			Help:    "Total tool execution time per triage run in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.5, 2, 10), // 0.5s .. ~256s
		}),
		TriageTokensIn: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigil_triage_tokens_input",
			Help:    "Input tokens consumed per triage run.",
			Buckets: prometheus.ExponentialBuckets(100, 2, 12), // 100 .. ~409600
		}),
		TriageTokensOut: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigil_triage_tokens_output",
			Help:    "Output tokens consumed per triage run.",
			Buckets: prometheus.ExponentialBuckets(100, 2, 12), // 100 .. ~409600
		}),
		TriageToolCalls: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigil_triage_tool_calls",
			Help:    "Tool calls per triage run.",
			Buckets: prometheus.LinearBuckets(0, 1, 16), // 0 .. 15
		}),
		LLMCallsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vigil_llm_calls_total",
			Help: "Total LLM provider calls.",
		}),
		LLMTokensIn: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vigil_llm_tokens_input_total",
			Help: "Total LLM input tokens consumed.",
		}),
		LLMTokensOut: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vigil_llm_tokens_output_total",
			Help: "Total LLM output tokens consumed.",
		}),
		LLMDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "vigil_llm_call_duration_seconds",
			Help:    "Duration of individual LLM calls in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.5, 2, 8), // 0.5s .. ~64s
		}),
		ToolCallsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigil_tool_calls_total",
			Help: "Total tool executions by tool name and status.",
		}, []string{"tool", "status"}),
		ToolDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigil_tool_duration_seconds",
			Help:    "Duration of tool executions in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 8), // 0.1s .. ~12.8s
		}, []string{"tool"}),
		ToolInputBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigil_tool_input_bytes",
			Help:    "Size of tool input in bytes.",
			Buckets: prometheus.ExponentialBuckets(64, 4, 8), // 64B .. ~1MB
		}, []string{"tool"}),
		ToolOutputBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigil_tool_output_bytes",
			Help:    "Size of tool output in bytes.",
			Buckets: prometheus.ExponentialBuckets(64, 4, 8), // 64B .. ~1MB
		}, []string{"tool"}),
		SubmitsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigil_submits_total",
			Help: "Total alert submissions by result.",
		}, []string{"result"}),
	}

	reg.MustRegister(
		m.TriagesTotal,
		m.TriageDuration,
		m.TriageLLMTime,
		m.TriageToolTime,
		m.TriageTokensIn,
		m.TriageTokensOut,
		m.TriageToolCalls,
		m.LLMCallsTotal,
		m.LLMTokensIn,
		m.LLMTokensOut,
		m.LLMDuration,
		m.ToolCallsTotal,
		m.ToolDuration,
		m.ToolInputBytes,
		m.ToolOutputBytes,
		m.SubmitsTotal,
	)

	return m
}

// Hooks returns an EngineHooks that increments the corresponding metrics.
func (m *Metrics) Hooks() EngineHooks {
	return EngineHooks{
		OnLLMCall: func(inputTokens, outputTokens int, duration float64) {
			m.LLMCallsTotal.Inc()
			m.LLMTokensIn.Add(float64(inputTokens))
			m.LLMTokensOut.Add(float64(outputTokens))
			m.LLMDuration.Observe(duration)
		},
		OnToolCall: func(name string, duration float64, inputBytes, outputBytes int, isError bool) {
			status := "success"
			if isError {
				status = "error"
			}
			m.ToolCallsTotal.WithLabelValues(name, status).Inc()
			m.ToolDuration.WithLabelValues(name).Observe(duration)
			m.ToolInputBytes.WithLabelValues(name).Observe(float64(inputBytes))
			m.ToolOutputBytes.WithLabelValues(name).Observe(float64(outputBytes))
		},
		OnComplete: func(e *CompleteEvent) {
			m.TriagesTotal.WithLabelValues(string(e.Status)).Inc()
			m.TriageDuration.WithLabelValues(string(e.Status), e.Model).Observe(e.Duration)
			m.TriageLLMTime.WithLabelValues(e.Model).Observe(e.LLMTime)
			m.TriageToolTime.Observe(e.ToolTime)
			m.TriageTokensIn.Observe(float64(e.TokensIn))
			m.TriageTokensOut.Observe(float64(e.TokensOut))
			m.TriageToolCalls.Observe(float64(e.ToolCalls))
		},
	}
}
