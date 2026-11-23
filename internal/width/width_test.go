package width

import "testing"

func TestRuneWidths(t *testing.T) {
	if got := Visible("a"); got != 1 {
		t.Fatalf("a = %d", got)
	}
	if got := Visible("界"); got != 2 {
		t.Fatalf("界 = %d", got)
	}
	if got := Visible("Ａ"); got != 2 {
		t.Fatalf("fullwidth A = %d", got)
	}
	if got := Visible("a\u0301"); got != 1 {
		t.Fatalf("combining = %d", got)
	}
	if got := Visible("😀"); got != 2 {
		t.Fatalf("emoji = %d", got)
	}
}

func TestRGISequenceIsOnePair(t *testing.T) {
	// man + ZWJ + laptop, an RGI sequence. Without the trie this is 4 cells.
	seq := "\U0001F468\u200D\U0001F4BB"
	if got := Visible(seq); got != 2 {
		t.Fatalf("rgi sequence = %d", got)
	}
	invalid := "\U0001F469\u200DA"
	if got := Visible(invalid); got != 3 {
		t.Fatalf("invalid zwj = %d", got)
	}
	if got := Visible("a\u0301"); got != 1 {
		t.Fatalf("combining = %d", got)
	}
	if got := Rune(0x200D); got != 0 || Rune(0x1F3FB) != 0 || Rune(0xFE0F) != 0 {
		t.Fatal("continuation widths")
	}
	if got := Visible("\u2764\uFE0F"); got != 2 {
		t.Fatalf("fe0f = %d", got)
	}
}

func TestANSIDoesNotCount(t *testing.T) {
	s := "\x1b[38;5;245mauto\x1b[0m"
	if got := String(s); got != 4 {
		t.Fatalf("styled auto = %d", got)
	}
}

func TestPrefixStopsOnWideRune(t *testing.T) {
	if got := Prefix("界界", 2); got != "界" {
		t.Fatalf("prefix = %q", got)
	}
	if got := Clip("abcdef", 4); got != "abc…" {
		t.Fatalf("clip = %q", got)
	}
}
