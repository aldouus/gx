package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotCapturedOnce(t *testing.T) {
	home := t.TempDir()
	shellPath := filepath.Join(home, "fake-sh")
	script := `#!/bin/sh
if [ "$1" = "-lic" ]; then
  printf '%s\n' '---ENV---'
  printf '%s\n' 'FOO=from-rc'
  printf '%s\n' 'PATH=/bin:/usr/bin'
  printf '%s\n' '---PRELUDE---'
  printf '%s\n' "alias hi='printf snapshot'"
  printf 'x\n' >> "$HOME/captures"
  exit 0
fi
if [ "$1" = "-c" ]; then
  sh -c "$2"
  exit $?
fi
exit 1
`
	if err := os.WriteFile(shellPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	old := resolveShell
	resolveShell = func() string { return shellPath }
	t.Cleanup(func() { resolveShell = old })
	resetSnapshot()
	t.Cleanup(resetSnapshot)

	ws := Workspace{Roots: []string{home}}
	out, err := Shell(ws).Run(t.Context(), []byte(`{"action":"run","command":"printf $FOO","profile":"user","yield_time_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-rc") {
		t.Fatalf("first run %s", out)
	}
	out, err = Shell(ws).Run(t.Context(), []byte(`{"action":"run","command":"printf $FOO","profile":"user","yield_time_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-rc") {
		t.Fatalf("second run %s", out)
	}
	got, err := os.ReadFile(filepath.Join(home, "captures"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "x") != 1 {
		t.Fatalf("captures %q", got)
	}
}

func TestSnapshotReloadsAfterReset(t *testing.T) {
	home := t.TempDir()
	shellPath := filepath.Join(home, "fake-sh")
	script := `#!/bin/sh
if [ "$1" = "-lic" ]; then
  printf '%s\n' '---ENV---'
  printf '%s\n' 'FOO=from-rc'
  printf '%s\n' 'PATH=/bin:/usr/bin'
  printf '%s\n' '---PRELUDE---'
  printf 'x\n' >> "$HOME/captures"
  exit 0
fi
if [ "$1" = "-c" ]; then
  sh -c "$2"
  exit $?
fi
exit 1
`
	if err := os.WriteFile(shellPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	old := resolveShell
	resolveShell = func() string { return shellPath }
	t.Cleanup(func() { resolveShell = old })
	ResetSnapshot()
	t.Cleanup(ResetSnapshot)

	ws := Workspace{Roots: []string{home}}
	run := func() {
		t.Helper()
		out, err := Shell(ws).Run(t.Context(), []byte(`{"action":"run","command":"printf $FOO","profile":"user","yield_time_ms":5000}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "from-rc") {
			t.Fatalf("run %s", out)
		}
	}
	run()
	run()
	ResetSnapshot()
	run()
	got, err := os.ReadFile(filepath.Join(home, "captures"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "x") != 2 {
		t.Fatalf("captures %q", got)
	}
}

func TestSnapshotReplaysFunctions(t *testing.T) {
	home := t.TempDir()
	shellPath := filepath.Join(home, "fake-sh")
	script := `#!/bin/sh
if [ "$1" = "-lic" ]; then
  printf '%s\n' '---ENV---'
  printf '%s\n' 'PATH=/bin:/usr/bin'
  printf '%s\n' '---PRELUDE---'
  printf '%s\n' '---FUNCS---'
  printf '%s\n' 'hello() { printf from-func; }'
  printf '%s\n' '---OPTS---'
  printf '%s\n' 'shopt -s histappend'
  exit 0
fi
if [ "$1" = "-c" ]; then
  sh -c "$2"
  exit $?
fi
exit 1
`
	if err := os.WriteFile(shellPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	old := resolveShell
	resolveShell = func() string { return shellPath }
	t.Cleanup(func() { resolveShell = old })
	ResetSnapshot()
	t.Cleanup(ResetSnapshot)
	out, err := Shell(Workspace{Roots: []string{home}}).Run(t.Context(), []byte(`{"action":"run","command":"hello","profile":"user","yield_time_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from-func") {
		t.Fatalf("function was not replayed: %s", out)
	}
}

func TestTTYIsATerminal(t *testing.T) {
	ws := Workspace{Roots: []string{t.TempDir()}}
	out, err := Shell(ws).Run(t.Context(), []byte(`{"action":"run","command":"tty","profile":"clean","tty":true,"yield_time_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/dev/") {
		t.Fatalf("tty was not a terminal: %s", out)
	}
}

func TestCleanShellSkipsSnapshot(t *testing.T) {
	home := t.TempDir()
	shellPath := filepath.Join(home, "fake-sh")
	script := "#!/bin/sh\nif [ \"$1\" = \"-lic\" ]; then printf 'x\\n' >> \"$HOME/captures\"; exit 0; fi\nif [ \"$1\" = \"-c\" ]; then sh -c \"$2\"; exit $?; fi\nexit 1\n"
	if err := os.WriteFile(shellPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	old := resolveShell
	resolveShell = func() string { return shellPath }
	t.Cleanup(func() { resolveShell = old })
	resetSnapshot()
	out, err := Shell(Workspace{Roots: []string{home}}).Run(t.Context(), []byte(`{"action":"run","command":"printf hi","profile":"clean","yield_time_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "<stdout>") || !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("out %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "captures")); !os.IsNotExist(err) {
		t.Fatal("clean profile captured startup")
	}
}
