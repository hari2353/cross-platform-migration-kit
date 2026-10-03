package migration

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
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
