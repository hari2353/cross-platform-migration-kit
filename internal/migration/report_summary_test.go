package migration

import (
	"strings"
	"testing"
	"time"
)

// Steal: PCmover's AI Assistant ("summarizes what will or will not
// transfer") as deterministic sentence generation from the evidence the
// Report already holds. No LLM — the doctrine forbids unearned authority.

func summaryReport() Report {
	report := NewReport("restore")
	report.Files = 4
	report.Applications = 3
	report.Results = []Result{
		{Kind: "file", Status: "copied"},
		{Kind: "file", Status: "copied"},
		{Kind: "file", Status: "skipped-existing"},
		{Kind: "file", Status: "failed", Detail: "disk full"},
		{Kind: "application", ID: "git", Status: "planned"},
		{Kind: "application", ID: "photoshop", Status: "unsupported", Detail: "no cross-platform installer"},
		{Kind: "application", ID: "vim", Status: "installed"},
	}
	return report
}

func TestSummarizeReportCountsWhatWillAndWillNotTransfer(t *testing.T) {
	lines := SummarizeReport(summaryReport())
	joined := strings.Join(lines, "\n")
	for _, needle := range []string{
		"2 of 4 files copied",
		"1 skipped (skipped-existing)",
		"1 failed",
		"1 installed, 1 planned, 1 unsupported",
	} {
		if !strings.Contains(joined, needle) {
			t.Errorf("summary missing %q in:\n%s", needle, joined)
		}
	}
}

func TestSummarizeReportNextStepsFollowTheState(t *testing.T) {
	failed := summaryReport()
	lines := strings.Join(SummarizeReport(failed), "\n")
	if !strings.Contains(lines, "migrate resume") {
		t.Errorf("failures must point at resume:\n%s", lines)
	}
	if !strings.Contains(lines, "install manually: photoshop") {
		t.Errorf("unsupported apps must be named:\n%s", lines)
	}

	clean := NewReport("restore")
	clean.Files = 2
	clean.Results = []Result{
		{Kind: "file", Status: "copied"},
		{Kind: "file", Status: "copied"},
	}
	cleanLines := strings.Join(SummarizeReport(clean), "\n")
	if !strings.Contains(cleanLines, "migrate verify") {
		t.Errorf("clean run must point at verify:\n%s", cleanLines)
	}
	if strings.Contains(cleanLines, "failed") {
		t.Errorf("clean run must not claim failures:\n%s", cleanLines)
	}
}

func TestSummarizeReportCapsManualInstallListAtFive(t *testing.T) {
	report := NewReport("restore")
	for i := 0; i < 8; i++ {
		report.Results = append(report.Results,
			Result{Kind: "application", ID: string(rune('a' + i)), Status: "unsupported"})
	}
	joined := strings.Join(SummarizeReport(report), "\n")
	if strings.Count(joined, "install manually:") != 1 {
		t.Fatalf("expected one manual-install line:\n%s", joined)
	}
	if !strings.Contains(joined, "and 3 more") {
		t.Errorf("cap must name the overflow:\n%s", joined)
	}
}

func TestWriteHumanReportLeadsWithSummary(t *testing.T) {
	report := summaryReport()
	report.FinishedAt = time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	var builder strings.Builder
	if err := WriteHumanReport(&builder, report); err != nil {
		t.Fatal(err)
	}
	out := builder.String()
	if !strings.Contains(out, "2 of 4 files copied") {
		t.Errorf("human report must open with the summary:\n%s", out)
	}
	if !strings.Contains(out, "restore: files=4 applications=3") {
		t.Errorf("existing header line must survive:\n%s", out)
	}
}

func TestNextStepsMatchTheCommand(t *testing.T) {
	dryRun := NewReport("restore-dry-run")
	dryRun.Results = []Result{{Kind: "file", Status: "copied"}}
	dryRunLines := strings.Join(SummarizeReport(dryRun), "\n")
	if !strings.Contains(dryRunLines, "without --dry-run") {
		t.Errorf("dry-run must point at the real restore:\n%s", dryRunLines)
	}
	if strings.Contains(dryRunLines, "migrate verify") {
		t.Errorf("dry-run must not send the user to verify (nothing restored yet):\n%s", dryRunLines)
	}

	undo := NewReport("undo")
	undo.Files = 1
	undo.Results = []Result{{Kind: "file", Status: "removed"}}
	undoLines := strings.Join(SummarizeReport(undo), "\n")
	if strings.Contains(undoLines, "migrate verify") {
		t.Errorf("undo must not point at verify (it compares against the manifest the undo removed):\n%s", undoLines)
	}
	if !strings.Contains(undoLines, "migrate doctor") {
		t.Errorf("undo should suggest doctor:\n%s", undoLines)
	}
}
