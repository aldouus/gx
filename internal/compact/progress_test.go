package compact

import "testing"

func TestExactWithoutWaiting(t *testing.T) {
	p := Progress{Phase: Summary, Notes: 23}
	if p.Count() != 23 {
		t.Fatalf("count = %d, value = %d", p.Count(), p.Value())
	}
	if p.Shown() != 23 {
		t.Fatalf("shown = %d", p.Shown())
	}
}

func TestAllWrittenShows99(t *testing.T) {
	p := Progress{Phase: Summary, Notes: All}
	if p.Count() != 99 {
		t.Fatalf("count = %d", p.Count())
	}
}

func TestFullOnlyWhenSaving(t *testing.T) {
	p := Progress{Phase: Summary, Notes: All}
	if p.Shown() == 100 {
		t.Fatal("summary showed 100")
	}
	p.Save()
	if p.Shown() != 100 {
		t.Fatalf("publication = %d", p.Shown())
	}
}

func TestAskKeepsTheCount(t *testing.T) {
	p := Progress{Phase: Summary, Notes: 23}
	before := p.Value()
	p.Ask(1)
	if p.Value() != before {
		t.Fatalf("value %d -> %d", before, p.Value())
	}
}

func TestWaitIsBounded(t *testing.T) {
	p := Progress{Phase: Summary, Waiting: true, Seen: 1}
	p.Observe(4) // 3 ticks, the longest step
	if p.Notes != p.Base {
		t.Fatal("notes moved")
	}
	if 2*(p.Value()-p.Floor) > Top-p.Floor {
		t.Fatalf("wait ran past half the room: value %d floor %d", p.Value(), p.Floor)
	}
}

func TestJumpEarnsNothing(t *testing.T) {
	p := Progress{Phase: Summary, Waiting: true, Seen: 1, Waited: 1}
	before := p.Value()
	p.Observe(1 + LongestStep + 1)
	if p.Value() != before {
		t.Fatalf("jump moved value %d -> %d", before, p.Value())
	}
}

func TestClockBackDoesNotUnwind(t *testing.T) {
	p := Progress{Phase: Summary, Waiting: true, Seen: 5, Waited: 2}
	before := p.Waited
	p.Observe(2)
	if p.Waited != before {
		t.Fatalf("clock back changed wait %d -> %d", before, p.Waited)
	}
}

func TestStartsClean(t *testing.T) {
	p := Progress{Phase: Summary, Floor: 10, Base: 4, Notes: 9, Waiting: true, Waited: 3, Seen: 2}
	p.Settle()
	p.Begin()
	if p.Phase != Preparation || p.Floor != 0 || p.Base != 0 || p.Notes != 0 || p.Waiting || p.Waited != 0 || p.Seen != 0 {
		t.Fatalf("dirty start: %+v", p)
	}
}

func TestOnlyNotesMoveAfterNotes(t *testing.T) {
	p := Progress{Phase: Summary}
	p.Ask(1)
	p.Write(1, 10)
	before := p.Value()
	p.Observe(2)
	p.Observe(3)
	if p.Value() != before {
		t.Fatalf("wait after notes moved value %d -> %d", before, p.Value())
	}
	p.Write(4, 11)
	if p.Value() <= before {
		t.Fatalf("notes did not move the row: %d -> %d", before, p.Value())
	}
}

func TestFirstWaitRises(t *testing.T) {
	p := Progress{Phase: Summary, Waiting: true, Seen: 1}
	p.Ask(1)
	before := p.Shown()
	p.Observe(2)
	if p.Shown() <= before && p.Value() <= p.Floor {
		t.Fatalf("first wait did not earn: shown %d -> %d value %d", before, p.Shown(), p.Value())
	}
}
