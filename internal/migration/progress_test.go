package migration

import (
	"path/filepath"
	"testing"
	"time"
)

// Steal: Zinstall sells transfer-duration honesty ("usually several
// hours"); this kit gave hours-long transfers zero feedback. Progress
// counters with a running ETA, bounded output (rate-limited by the
// ProgressFilter), no new dependencies.

func TestEstimateETATable(t *testing.T) {
	cases := []struct {
		done, total int64
		elapsed     time.Duration
		want        time.Duration
		ok          bool
	}{
		{0, 100, 10 * time.Second, 0, false},   // no rate yet
		{100, 100, 10 * time.Second, 0, false}, // finished
		{50, 100, 10 * time.Second, 10 * time.Second, true},
		{25, 100, 5 * time.Second, 15 * time.Second, true},
		{1, 1, 0, 0, false}, // no elapsed time
	}
	for _, tc := range cases {
		got, ok := EstimateETA(tc.done, tc.total, tc.elapsed)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("EstimateETA(%d,%d,%s) = (%s,%v), want (%s,%v)",
				tc.done, tc.total, tc.elapsed, got, ok, tc.want, tc.ok)
		}
	}
}

func TestProgressFilterBoundsOutput(t *testing.T) {
	filter := NewProgressFilter()
	// first item always reports
	if !filter.Report(10, 10_000, "a") {
		t.Fatal("first report must pass")
	}
	// small steps within the 5% band are suppressed
	if filter.Report(20, 10_000, "b") || filter.Report(30, 10_000, "c") {
		t.Fatal("sub-5% steps must be suppressed")
	}
	// crossing a 5% boundary reports
	if !filter.Report(600, 10_000, "d") {
		t.Fatal("crossing 5% must pass")
	}
	// 250-call boundary passes even without a 5% step
	filter2 := NewProgressFilter()
	filter2.Report(1, 0, "first") // total unknown (0)
	passed := 0
	for i := 0; i < 300; i++ {
		if filter2.Report(int64(i+2), 0, "x") {
			passed++
		}
	}
	if passed != 1 { // exactly one pass at the 250-call boundary
		t.Fatalf("expected exactly one boundary pass in 300 calls, got %d", passed)
	}
}

func TestCaptureWithProgressReportsEveryItem(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "Documents", "one.txt"), "one-content")
	writeFile(t, filepath.Join(source, "Pictures", "two.txt"), "two-content-longer")

	var paths []string
	var lastDone, lastTotal int64
	progress := func(done, total int64, currentPath string) {
		paths = append(paths, currentPath)
		lastDone, lastTotal = done, total
	}
	if _, err := CaptureWithProgress(source, repository, key, false, false, nil, progress); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("progress called %d times, want once per file (2): %v", len(paths), paths)
	}
	if lastDone != lastTotal || lastTotal == 0 {
		t.Fatalf("final progress done=%d total=%d — must converge", lastDone, lastTotal)
	}
}

func TestRestoreWithProgressReportsCopiedBytes(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "migration.key")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyForTest(keyPath, key); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "Documents", "one.txt"), "one")
	writeFile(t, filepath.Join(source, "Pictures", "two.txt"), "two")

	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	var calls int
	var lastDone, lastTotal int64
	progress := func(done, total int64, currentPath string) {
		calls++
		lastDone, lastTotal = done, total
	}
	if _, err := ManagedRestoreWithProgress(repository, destination, keyPath,
		"personal", false, false, progress); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if calls != 2 {
		t.Fatalf("progress called %d times, want 2 (one per file)", calls)
	}
	if lastDone != lastTotal || lastTotal == 0 {
		t.Fatalf("final progress done=%d total=%d — must converge", lastDone, lastTotal)
	}
}

func TestNilProgressIsSafe(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "Documents", "one.txt"), "one")
	if _, err := CaptureWithProgress(source, repository, key, false, false, nil, nil); err != nil {
		t.Fatalf("nil progress must not panic or fail: %v", err)
	}
}
