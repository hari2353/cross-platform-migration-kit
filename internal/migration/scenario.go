package migration

import (
	"fmt"
	"time"
)

// Scenario is one named migration run for the benchmark.
type Scenario struct {
	Name string
	Run  func() error
}

// ScenarioResult records one scenario run.
type ScenarioResult struct {
	Name    string
	Latency time.Duration
	Success bool
	Safe    bool
	Detail  string
}

// RunScenarios executes each scenario once and reports the safe-completion
// rate (completed without error and inside the deadline).
// Cross-pollinated from harvard-edge/AVPerf.
func RunScenarios(scenarios []Scenario, deadline time.Duration) (map[string]any, error) {
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("scenarios must be non-empty")
	}
	if deadline <= 0 {
		return nil, fmt.Errorf("deadline must be positive")
	}

	results := make([]ScenarioResult, 0, len(scenarios))
	for _, s := range scenarios {
		if s.Name == "" {
			return nil, fmt.Errorf("scenario name must be non-empty")
		}
		start := time.Now()
		err := s.Run()
		latency := time.Since(start)
		success := err == nil
		safe := success && latency <= deadline
		detail := "ok"
		if err != nil {
			detail = "error: " + err.Error()
		} else if !safe {
			detail = fmt.Sprintf("deadline %s > %s", latency, deadline)
		}
		results = append(results, ScenarioResult{
			Name: s.Name, Latency: latency, Success: success, Safe: safe, Detail: detail,
		})
	}

	safeCount := 0
	for _, r := range results {
		if r.Safe {
			safeCount++
		}
	}
	failures := make([]map[string]any, 0)
	for _, r := range results {
		if !r.Safe {
			failures = append(failures, map[string]any{
				"name": r.Name, "latency": r.Latency.String(), "detail": r.Detail,
			})
		}
	}
	if len(failures) > 20 {
		failures = failures[:20]
	}
	scenarioMaps := make([]map[string]any, 0, len(results))
	for _, r := range results {
		scenarioMaps = append(scenarioMaps, map[string]any{
			"name": r.Name, "latency": r.Latency.String(),
			"success": r.Success, "safe": r.Safe, "detail": r.Detail,
		})
	}

	return map[string]any{
		"total":     len(results),
		"safe":      safeCount,
		"safe_rate": float64(safeCount) / float64(len(results)),
		"failures":  failures,
		"scenarios": scenarioMaps,
	}, nil
}
