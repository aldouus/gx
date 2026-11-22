package perm

import "testing"

func TestDecide(t *testing.T) {
	read := Spec{Name: "read_file"}
	edit := Spec{Name: "edit_file", NeedsApproval: true, WorkspaceEdit: true, Routine: true}
	shell := Spec{Name: "shell", NeedsApproval: true, ShellCommand: "curl https://example.com"}
	testCmd := Spec{Name: "shell", NeedsApproval: true, ShellCommand: "go test ./...", Routine: true}

	cases := []struct {
		mode Mode
		spec Spec
		want Decision
	}{
		{Ask, read, Allow},
		{Ask, edit, Prompt},
		{Ask, shell, Prompt},
		{Auto, read, Allow},
		{Auto, edit, Allow},
		{Auto, testCmd, Allow},
		{Auto, shell, Review},
		{FullAccess, shell, Allow},
	}
	for _, tc := range cases {
		if got := Decide(tc.mode, tc.spec); got != tc.want {
			t.Errorf("Decide(%s, %+v) = %s, want %s", tc.mode, tc.spec, got, tc.want)
		}
	}
}

func TestRoutine(t *testing.T) {
	if !Routine(Spec{Name: "edit_file", WorkspaceEdit: true}) {
		t.Fatal("workspace edit")
	}
	if Routine(Spec{Name: "edit_file"}) {
		t.Fatal("edit outside workspace")
	}
	if !Routine(Spec{Name: "shell", ShellCommand: "go test ./internal/perm"}) {
		t.Fatal("go test")
	}
	if Routine(Spec{Name: "shell", ShellCommand: "go testifier"}) {
		t.Fatal("prefix was not a word")
	}
	if Routine(Spec{Name: "shell", ShellCommand: "rm -rf /"}) {
		t.Fatal("rm is not routine")
	}
}

func TestParse(t *testing.T) {
	if m, ok := Parse("yolo"); !ok || m != FullAccess {
		t.Fatalf("yolo: %s %v", m, ok)
	}
	if _, ok := Parse("code"); ok {
		t.Fatal("code is not a mode")
	}
}
