package cli

import "testing"

func TestHelpAndVersion(t *testing.T) {
	inv, err := Parse([]string{"help"})
	if err != nil || inv.Command != "help" {
		t.Fatalf("%+v %v", inv, err)
	}
	inv, err = Parse([]string{"--help"})
	if err != nil || inv.Command != "help" {
		t.Fatalf("help flag %+v %v", inv, err)
	}
	inv, err = Parse([]string{"-v"})
	if err != nil || inv.Command != "version" {
		t.Fatalf("version %+v %v", inv, err)
	}
	if !contains(HelpText(), "Usage:") || !contains(HelpText(), "ask") {
		t.Fatal(HelpText())
	}
}

func TestUnknownFlag(t *testing.T) {
	_, err := Parse([]string{"--not-a-flag"})
	u, ok := err.(*UsageError)
	if !ok || u.Code != 2 {
		t.Fatalf("%v", err)
	}
}

func TestAskPrompt(t *testing.T) {
	inv, err := Parse([]string{"ask", "--model", "m", "explain", "this"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Model != "m" || len(inv.Prompt) != 2 {
		t.Fatalf("%+v", inv)
	}
	inv, err = Parse([]string{"--full-access", "ask", "--json", "hi"})
	if err != nil || inv.Permission != "full-access" || !inv.JSON || inv.Prompt[0] != "hi" {
		t.Fatalf("%+v %v", inv, err)
	}
}

func TestResumeAndPrompt(t *testing.T) {
	inv, err := Parse([]string{"-c"})
	if err != nil || !inv.Continue || inv.Command != "interactive" {
		t.Fatalf("%+v %v", inv, err)
	}
	inv, err = Parse([]string{"Fix", "the", "test"})
	if err != nil || inv.Command != "interactive" || len(inv.Prompt) != 3 {
		t.Fatalf("%+v %v", inv, err)
	}
}

func TestSlashTable(t *testing.T) {
	name, rest, ok := MatchSlash("/permissions ask")
	if !ok || name != "permissions" || rest != "ask" {
		t.Fatalf("%s %s %v", name, rest, ok)
	}
	if _, _, ok := MatchSlash("/nope"); ok {
		t.Fatal("unknown slash matched")
	}
	name, _, ok = MatchSlash("/quit")
	if !ok || name != "quit" {
		t.Fatal("quit missing")
	}
	name, _, ok = MatchSlash("/image")
	if !ok || name != "image" {
		t.Fatal("image missing")
	}
	name, _, ok = MatchSlash("/login")
	if !ok || name != "login" {
		t.Fatal("login missing")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()))
}
