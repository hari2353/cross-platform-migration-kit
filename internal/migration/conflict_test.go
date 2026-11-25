package migration

import "testing"

func TestResolveConflictLocalNewer(t *testing.T) {
	r := ResolveConflict(1, 1, 100, 50)
	if r.Winner != "local" {
		t.Fatalf("winner = %s", r.Winner)
	}
}

func TestResolveConflictRemoteNewer(t *testing.T) {
	r := ResolveConflict(1, 1, 50, 100)
	if r.Winner != "remote" {
		t.Fatalf("winner = %s", r.Winner)
	}
}

func TestResolveConflictTieByVersion(t *testing.T) {
	r := ResolveConflict(2, 1, 100, 100)
	if r.Winner != "local" {
		t.Fatalf("winner = %s", r.Winner)
	}
	r2 := ResolveConflict(1, 2, 100, 100)
	if r2.Winner != "remote" {
		t.Fatalf("winner = %s", r2.Winner)
	}
}

func TestResolveConflictExactTie(t *testing.T) {
	r := ResolveConflict(1, 1, 100, 100)
	if r.Winner != "manual" {
		t.Fatalf("winner = %s", r.Winner)
	}
}

func TestMergeThreeWay(t *testing.T) {
	if v, ok := MergeThreeWay("base", "local", "remote"); ok || v != "" {
		// divergent change -> conflict
	}
	v, ok := MergeThreeWay("base", "local", "local")
	if !ok || v != "local" {
		t.Fatalf("same change: %s %v", v, ok)
	}
	v2, ok2 := MergeThreeWay("base", "base", "remote")
	if !ok2 || v2 != "remote" {
		t.Fatalf("remote only: %s %v", v2, ok2)
	}
	v3, ok3 := MergeThreeWay("base", "local", "remote")
	if ok3 || v3 != "" {
		t.Fatalf("conflict should fail: %s %v", v3, ok3)
	}
}
