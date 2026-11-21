package migration

import (
	"errors"
	"testing"
	"time"
)

func TestRunScenariosAllSafe(t *testing.T) {
	report, err := RunScenarios([]Scenario{
		{Name: "a", Run: func() error { return nil }},
		{Name: "b", Run: func() error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if report["safe"].(int) != 2 {
		t.Fatalf("safe = %v", report["safe"])
	}
	if report["safe_rate"].(float64) != 1.0 {
		t.Fatalf("safe_rate = %v", report["safe_rate"])
	}
}

func TestRunScenariosDeadlineMiss(t *testing.T) {
	report, err := RunScenarios([]Scenario{
		{Name: "slow", Run: func() error { time.Sleep(5 * time.Millisecond); return nil }},
	}, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if report["safe"].(int) != 0 {
		t.Fatalf("safe = %v", report["safe"])
	}
	failures := report["failures"].([]map[string]any)
	if len(failures) != 1 || failures[0]["detail"].(string)[:8] != "deadline" {
		t.Fatalf("failures = %v", failures)
	}
}

func TestRunScenariosExceptionIsFailure(t *testing.T) {
	report, err := RunScenarios([]Scenario{
		{Name: "boom", Run: func() error { return errors.New("boom") }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if report["safe"].(int) != 0 {
		t.Fatalf("safe = %v", report["safe"])
	}
	failures := report["failures"].([]map[string]any)
	if failures[0]["detail"].(string) != "error: boom" {
		t.Fatalf("detail = %v", failures[0]["detail"])
	}
}

func TestRunScenariosInvalidInputs(t *testing.T) {
	if _, err := RunScenarios(nil, time.Second); err == nil {
		t.Fatal("empty scenarios accepted")
	}
	if _, err := RunScenarios([]Scenario{{Name: "a", Run: func() error { return nil }}}, 0); err == nil {
		t.Fatal("zero deadline accepted")
	}
	if _, err := RunScenarios([]Scenario{{Name: "", Run: func() error { return nil }}}, time.Second); err == nil {
		t.Fatal("blank name accepted")
	}
}
