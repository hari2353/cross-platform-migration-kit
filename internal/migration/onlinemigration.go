package migration

// OnlineMigrationPhase is one step of an online (zero-downtime) migration.
type OnlineMigrationPhase struct {
	Name        string
	Description string
	Done        bool
}

// OnlineMigrationPlan is a Stripe-style online migration plan with shadow
// traffic, dual-write, cutover, and cleanup phases.
type OnlineMigrationPlan struct {
	Name    string
	Phases  []OnlineMigrationPhase
	Current int
}

// NewOnlineMigrationPlan builds a standard online migration plan.
func NewOnlineMigrationPlan(name string) *OnlineMigrationPlan {
	return &OnlineMigrationPlan{
		Name: name,
		Phases: []OnlineMigrationPhase{
			{Name: "shadow", Description: "Shadow-write to new store, compare results"},
			{Name: "dual-write", Description: "Write to both old and new stores"},
			{Name: "backfill", Description: "Backfill historical data into new store"},
			{Name: "cutover", Description: "Switch reads to new store"},
			{Name: "cleanup", Description: "Remove old store writes"},
		},
		Current: 0,
	}
}

// Advance moves to the next phase if the current is done.
func (p *OnlineMigrationPlan) Advance() bool {
	if p.Current >= len(p.Phases) {
		return false
	}
	p.Phases[p.Current].Done = true
	p.Current++
	return p.Current < len(p.Phases)
}

// IsComplete returns true when all phases are done.
func (p *OnlineMigrationPlan) IsComplete() bool {
	return p.Current >= len(p.Phases)
}

// CurrentPhase returns the current phase name.
func (p *OnlineMigrationPlan) CurrentPhase() string {
	if p.Current >= len(p.Phases) {
		return "done"
	}
	return p.Phases[p.Current].Name
}
