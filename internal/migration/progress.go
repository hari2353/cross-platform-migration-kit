package migration

import "time"

// ProgressFunc reports cumulative byte progress after each item. Nil-safe:
// call sites may pass nil. currentPath is the item's relative path.
type ProgressFunc func(done, total int64, currentPath string)

// EstimateETA returns the remaining time at the observed rate. false when
// no honest estimate exists yet (nothing done, no elapsed time, or done).
func EstimateETA(done, total int64, elapsed time.Duration) (time.Duration, bool) {
	if done <= 0 || elapsed <= 0 || total <= done {
		return 0, false
	}
	rate := float64(done) / elapsed.Seconds()
	remaining := float64(total-done) / rate
	return time.Duration(remaining * float64(time.Second)), true
}

// ProgressFilter bounds progress output: the first item always reports,
// then a report passes only on a >=5% byte boundary or every 250 calls —
// whichever comes first. A 100k-file tree yields ~20+400 lines, not 100k.
type ProgressFilter struct {
	started    bool
	lastPrint  int64
	lastCall   int
	calls      int
}

func NewProgressFilter() *ProgressFilter {
	return &ProgressFilter{}
}

// Report records one progress event and reports whether it should be
// printed now.
func (filter *ProgressFilter) Report(done, total int64, _ string) bool {
	filter.calls++
	if !filter.started {
		filter.started = true
		filter.lastPrint, filter.lastCall = done, filter.calls
		return true
	}
	if filter.calls-filter.lastCall >= 250 {
		filter.lastPrint, filter.lastCall = done, filter.calls
		return true
	}
	if total > 0 {
		lastFraction := float64(filter.lastPrint) / float64(total)
		fraction := float64(done) / float64(total)
		if fraction-lastFraction >= 0.05 {
			filter.lastPrint, filter.lastCall = done, filter.calls
			return true
		}
	}
	return false
}
