// Package compact is the compaction-row arithmetic from
// CompactionProgress.tla. The number on the row is read from this state
// alone. A wait earns at most half the room left when its request was sent,
// and a clock jump longer than LongestStep earns nothing.
package compact

// Constants from CompactionProgress.tla. Time is ticks of 20 seconds.
const (
	All         = 99
	Scale       = 10000
	WaitHalf    = 3
	LongestStep = 3
	SteadyWait  = 10
	Top         = All * 100
	WaitShare   = 5000
)

// Phase is the compaction on the row. none shows no number.
type Phase string

const (
	None        Phase = "none"
	Preparation Phase = "preparation"
	Summary     Phase = "summary"
	Publication Phase = "publication"
)

// Progress is the row's state. Shown is the number a person sees, or 0
// when the row is hidden.
type Progress struct {
	Phase   Phase
	Floor   int
	Base    int
	Notes   int
	Waiting bool
	Waited  int
	Seen    int
}

// Head is the share a wait of w ticks earns, in ten-thousandths.
func Head(w int) int {
	if w < 0 {
		w = 0
	}
	return (WaitShare * w) / (w + WaitHalf)
}

// ValueAfter is Progress.value for a wait of w ticks, in hundredths of a percent.
func (p Progress) ValueAfter(w int) int {
	room := Top - p.Floor
	span := All - p.Base
	fresh := p.Notes - p.Base
	rest := room * (Scale - Head(w))
	fill := 0
	if span != 0 {
		fill = (rest/span)*fresh + ((rest%span)*fresh)/span
	}
	return p.Floor + (room*Head(w)+fill)/Scale
}

// Value is the current hundredths of a percent.
func (p Progress) Value() int { return p.ValueAfter(p.Waited) }

// Count is Progress.shown.
func (p Progress) Count() int { return p.Value() / 100 }

// Shown is the number on the row. 100 is only the publication phase.
func (p Progress) Shown() int {
	switch p.Phase {
	case Summary:
		return p.Count()
	case Publication:
		return 100
	default:
		return 0
	}
}

func (p Progress) counting() bool {
	return p.Phase == Preparation || p.Phase == Summary || p.Phase == Publication
}

// WaitedAt is the wait after the row is handed time t.
// A step longer than LongestStep is a clock jump and adds nothing.
// A clock set backward does not take the wait back.
func (p Progress) WaitedAt(t int) int {
	if p.Waiting && t > p.Seen && t-p.Seen <= LongestStep {
		return p.Waited + (t - p.Seen)
	}
	return p.Waited
}

// Begin starts a new compaction with clean numbers.
func (p *Progress) Begin() {
	*p = Progress{Phase: Preparation}
}

// Summarize moves the row into the summary phase.
func (p *Progress) Summarize() {
	if p.Phase != Preparation {
		return
	}
	p.Phase = Summary
}

// Ask records that a summary request was sent at time now.
// The number on the row does not change.
func (p *Progress) Ask(now int) {
	if p.Phase != Summary {
		return
	}
	p.Floor = p.ValueAfter(p.WaitedAt(now))
	p.Base = p.Notes
	p.Waited = 0
	p.Waiting = true
	p.Seen = now
}

// Write records that the notes' count rose to n. A count that is not higher
// changes nothing. Once notes arrive, only notes move the row.
func (p *Progress) Write(now, n int) {
	if p.Phase != Summary || n <= p.Notes {
		return
	}
	if n > All {
		n = All
	}
	p.Notes = n
	p.Waited = p.WaitedAt(now)
	p.Waiting = false
	p.Seen = now
}

// Observe hands the row time t. Only forward steps of at most LongestStep
// count as waiting.
func (p *Progress) Observe(t int) {
	if !p.counting() {
		return
	}
	p.Waited = p.WaitedAt(t)
	p.Seen = t
}

// Save is the publication phase: the row shows 100.
func (p *Progress) Save() {
	if p.Phase != Summary {
		return
	}
	p.Phase = Publication
}

// Settle hides the row.
func (p *Progress) Settle() {
	if !p.counting() {
		return
	}
	p.Phase = None
}
