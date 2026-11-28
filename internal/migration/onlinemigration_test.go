package migration

import "testing"

func TestOnlineMigrationPlanPhases(t *testing.T) {
	p := NewOnlineMigrationPlan("users")
	if p.CurrentPhase() != "shadow" {
		t.Fatalf("first phase = %s", p.CurrentPhase())
	}
	for i := 0; i < 5; i++ {
		if !p.Advance() && i < 4 {
			t.Fatalf("advance %d failed", i)
		}
	}
	if !p.IsComplete() {
		t.Fatal("not complete")
	}
	if p.CurrentPhase() != "done" {
		t.Fatalf("phase = %s", p.CurrentPhase())
	}
}

func TestOnlineMigrationPlanNoAdvancePastEnd(t *testing.T) {
	p := NewOnlineMigrationPlan("x")
	for i := 0; i < 5; i++ {
		p.Advance()
	}
	if p.Advance() {
		t.Fatal("advanced past end")
	}
}
