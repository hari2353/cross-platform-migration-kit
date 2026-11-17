package migration

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type Result struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Version      int       `json:"version"`
	Command      string    `json:"command"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	SourceOS     string    `json:"source_os,omitempty"`
	Destination  string    `json:"destination,omitempty"`
	Files        int       `json:"files"`
	Applications int       `json:"applications"`
	Results      []Result  `json:"results"`
}

func NewReport(command string) Report {
	return Report{Version: 1, Command: command, StartedAt: time.Now().UTC(), Results: []Result{}}
}

func (report *Report) Finish() {
	report.FinishedAt = time.Now().UTC()
}

func (report *Report) Add(result Result) {
	result.Path = safeReportPath(result.Path)
	report.Results = append(report.Results, result)
}

func WriteJSONReport(writer io.Writer, report Report) error {
	return json.NewEncoder(writer).Encode(report)
}

func WriteHumanReport(writer io.Writer, report Report) error {
	for _, line := range SummarizeReport(report) {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "%s: files=%d applications=%d\n", report.Command, report.Files, report.Applications); err != nil {
		return err
	}
	for _, result := range report.Results {
		if _, err := fmt.Fprintf(writer, "%s %s %s %s\n", result.Kind, result.Status, result.ID, result.Path); err != nil {
			return err
		}
	}
	return nil
}

// SummarizeReport turns the evidence a Report already holds into a fixed
// number of plain-language lines: what transferred, what did not and why,
// and what to do next. Deterministic sentence generation — the PCmover AI
// assistant's job with no AI in it.
func SummarizeReport(report Report) []string {
	fileStatus := map[string]int{}
	appStatus := map[string]int{}
	var unsupportedApps []string
	var failureExamples []string
	for _, result := range report.Results {
		if result.Kind == "file" {
			fileStatus[result.Status]++
			if result.Status == "failed" && len(failureExamples) < 3 {
				failureExamples = append(failureExamples, result.Detail)
			}
		} else if result.Kind == "application" {
			appStatus[result.Status]++
			if result.Status == "unsupported" {
				unsupportedApps = append(unsupportedApps, result.ID)
			}
		}
	}
	copied := fileStatus["copied"]
	firstLine := fmt.Sprintf("%d of %d files copied",
		copied, maxInt(report.Files, copied))
	if report.Command == "undo" {
		// an undo report's action is removal/reversion, not copying
		firstLine = fmt.Sprintf("%d file(s) removed or reverted",
			report.Files)
	}
	lines := []string{firstLine}
	skipped := 0
	var skipReasons []string
	for _, status := range []string{"skipped-existing", "skipped", "unsupported", "excluded"} {
		if fileStatus[status] > 0 {
			skipped += fileStatus[status]
			skipReasons = append(skipReasons, status)
		}
	}
	if skipped > 0 {
		lines = append(lines, fmt.Sprintf("%d skipped (%s)", skipped,
			strings.Join(skipReasons, ", ")))
	}
	if fileStatus["failed"] > 0 {
		line := fmt.Sprintf("%d failed", fileStatus["failed"])
		if len(failureExamples) > 0 {
			line += " (e.g. " + failureExamples[0] + ")"
		}
		lines = append(lines, line)
	}
	if len(appStatus) > 0 {
		bits := []string{}
		for _, status := range []string{"installed", "planned", "unsupported", "failed"} {
			if appStatus[status] > 0 {
				bits = append(bits, fmt.Sprintf("%d %s", appStatus[status], status))
			}
		}
		lines = append(lines, "applications: "+strings.Join(bits, ", "))
	}
	// next steps, keyed off the command and the actual state — conditions
	// stack: a run can have failures AND untranslatable apps
	if report.Command == "restore-dry-run" {
		lines = append(lines, "next: review the plan above, then run migrate restore without --dry-run")
	} else if fileStatus["failed"] > 0 {
		lines = append(lines, "next: fix the failed items above, then run migrate resume")
	}
	if len(unsupportedApps) > 0 {
		names := unsupportedApps
		overflow := 0
		if len(names) > 5 {
			overflow = len(names) - 5
			names = names[:5]
		}
		line := "next: install manually: " + strings.Join(names, ", ")
		if overflow > 0 {
			line += fmt.Sprintf(", and %d more", overflow)
		}
		lines = append(lines, line)
	}
	if report.Command == "undo" && fileStatus["failed"] == 0 {
		// verify compares against the manifest the undo just unwound —
		// doctor is the right post-undo check
		lines = append(lines, "next: the destination is back to its pre-restore state — run migrate doctor to check it")
	} else if report.Command != "restore-dry-run" && fileStatus["failed"] == 0 && len(unsupportedApps) == 0 {
		lines = append(lines, "next: verify the destination with migrate verify")
	}
	return lines
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func safeReportPath(path string) string {
	if path == "" {
		return ""
	}
	clean := filepath.ToSlash(path)
	if filepath.IsAbs(path) {
		return filepath.Base(clean)
	}
	return clean
}
