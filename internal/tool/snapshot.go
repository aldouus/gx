package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// generation is one captured login-shell startup. Later commands replay it
// until the startup files change.
type generation struct {
	shell   string
	home    string
	fp      string
	env     []string
	prelude string
	funcs   string
	opts    string
}

var (
	snapMu sync.Mutex
	snap   *generation

	resolveShell = defaultResolveShell
)

func resetSnapshot() {
	snapMu.Lock()
	snap = nil
	snapMu.Unlock()
}

// ResetSnapshot drops the cached login-shell generation. The next
// user-profile command captures startup files again.
func ResetSnapshot() { resetSnapshot() }

func defaultResolveShell() string {
	if s := loginShell(); s != "" {
		return s
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if _, err := os.Stat("/bin/zsh"); err == nil {
		return "/bin/zsh"
	}
	return "/bin/sh"
}

func loginShell() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	user := filepath.Base(home)
	if _, err := exec.LookPath("dscl"); err == nil {
		out, err := exec.Command("dscl", ".", "-read", "/Users/"+user, "UserShell").Output()
		if err == nil {
			line := strings.TrimSpace(string(out))
			if _, rest, ok := strings.Cut(line, " "); ok {
				rest = strings.TrimSpace(rest)
				if strings.HasPrefix(rest, "/") {
					return rest
				}
			}
		}
	}
	if _, err := exec.LookPath("getent"); err == nil {
		out, err := exec.Command("getent", "passwd", user).Output()
		if err == nil {
			parts := strings.Split(strings.TrimSpace(string(out)), ":")
			if len(parts) >= 7 && strings.HasPrefix(parts[6], "/") {
				return parts[6]
			}
		}
	}
	return ""
}

func startupFiles(shell, home string) []string {
	base := strings.ToLower(filepath.Base(shell))
	var names []string
	switch {
	case strings.Contains(base, "zsh"):
		names = []string{".zshenv", ".zprofile", ".zshrc", ".zlogin"}
	case strings.Contains(base, "bash"):
		names = []string{".bashrc", ".bash_profile", ".profile"}
	default:
		names = []string{".profile"}
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = filepath.Join(home, name)
	}
	return out
}

func fingerprint(paths []string) string {
	var b strings.Builder
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s missing\n", path)
			continue
		}
		fmt.Fprintf(&b, "%s %d %d\n", path, info.ModTime().UnixNano(), info.Size())
	}
	return b.String()
}

func generationFor(ctx context.Context, shell, home string) (generation, error) {
	fp := fingerprint(startupFiles(shell, home))
	snapMu.Lock()
	defer snapMu.Unlock()
	if snap != nil && snap.shell == shell && snap.home == home && snap.fp == fp {
		return *snap, nil
	}
	g, err := capture(ctx, shell, home)
	if err != nil {
		return generation{}, err
	}
	g.shell = shell
	g.home = home
	g.fp = fp
	snap = &g
	return g, nil
}

func capture(ctx context.Context, shell, home string) (generation, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	const dump = "printf '%s\n' '---ENV---'\nenv\nprintf '%s\n' '---PRELUDE---'\nalias 2>/dev/null || true\nprintf '%s\n' '---FUNCS---'\nif [ -n \"$ZSH_VERSION\" ]; then whence -f 2>/dev/null || true; else declare -f 2>/dev/null || true; fi\nprintf '%s\n' '---OPTS---'\nif [ -n \"$ZSH_VERSION\" ]; then setopt 2>/dev/null || true; else shopt -p 2>/dev/null || true; fi\n"
	cmd := exec.CommandContext(ctx, shell, "-lic", dump)
	cmd.Dir = home
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return generation{}, fmt.Errorf("shell snapshot: %w", err)
	}
	env, prelude, funcs, opts := parseDump(stdout.String())
	if len(env) == 0 {
		return generation{}, fmt.Errorf("shell snapshot produced no environment")
	}
	return generation{env: env, prelude: prelude, funcs: funcs, opts: opts}, nil
}

func parseDump(text string) (env []string, prelude, funcs, opts string) {
	const envMark = "---ENV---"
	const preMark = "---PRELUDE---"
	const funcMark = "---FUNCS---"
	const optMark = "---OPTS---"
	envAt := strings.Index(text, envMark)
	preAt := strings.Index(text, preMark)
	funcAt := strings.Index(text, funcMark)
	optAt := strings.Index(text, optMark)
	if envAt < 0 {
		return nil, "", "", ""
	}
	envText := text[envAt+len(envMark):]
	preText := ""
	if preAt >= 0 {
		envText = text[envAt+len(envMark) : preAt]
		preText = text[preAt+len(preMark):]
		if funcAt >= 0 {
			preText = text[preAt+len(preMark) : funcAt]
		}
	}
	funcText := ""
	if funcAt >= 0 {
		funcText = text[funcAt+len(funcMark):]
		if optAt >= 0 {
			funcText = text[funcAt+len(funcMark) : optAt]
		}
	}
	optText := ""
	if optAt >= 0 {
		optText = text[optAt+len(optMark):]
	}
	skip := map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}
	for _, line := range strings.Split(envText, "\n") {
		line = strings.TrimRight(line, "\r")
		key, _, ok := strings.Cut(line, "=")
		if !ok || key == "" || skip[key] || strings.ContainsAny(key, " \t") {
			continue
		}
		env = append(env, line)
	}
	var lines []string
	for _, line := range strings.Split(preText, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return env, strings.Join(lines, "\n"), strings.TrimSpace(funcText), formatOpts(optText)
}

func formatOpts(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "set ") {
			continue
		}
		if strings.HasPrefix(line, "shopt ") || strings.HasPrefix(line, "setopt ") {
			lines = append(lines, line)
			continue
		}
		if strings.Contains(line, " ") {
			continue
		}
		lines = append(lines, "setopt "+line)
	}
	return strings.Join(lines, "\n")
}

func (g generation) command(userCmd string) (string, []string, []string) {
	var parts []string
	for _, part := range []string{g.opts, g.funcs, g.prelude, userCmd} {
		if strings.TrimSpace(part) == "" {
			continue
		}
		parts = append(parts, part)
	}
	script := strings.Join(parts, "\n")
	env := append([]string{}, g.env...)
	env = append(env, "AI_AGENT=fx")
	base := strings.ToLower(filepath.Base(g.shell))
	switch {
	case strings.Contains(base, "zsh"):
		return g.shell, []string{"-f", "-c", script}, env
	case strings.Contains(base, "bash"):
		return g.shell, []string{"--noprofile", "--norc", "-c", script}, env
	default:
		return g.shell, []string{"-c", script}, env
	}
}

func cleanArgs(shell, command string) []string {
	base := strings.ToLower(filepath.Base(shell))
	switch {
	case strings.Contains(base, "zsh"):
		return []string{"-f", "-c", command}
	case strings.Contains(base, "bash"):
		return []string{"--noprofile", "--norc", "-c", command}
	default:
		return []string{"-c", command}
	}
}
