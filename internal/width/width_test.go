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
