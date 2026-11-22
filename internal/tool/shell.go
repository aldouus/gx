package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type shellTool struct {
	ws Workspace
}

func Shell(ws Workspace) Tool { return shellTool{ws: ws} }

func (shellTool) Name() string { return "shell" }
func (shellTool) Description() string {
	return "Run a shell command. Each call starts a new shell with the user's startup files, so cd and export do not carry over. action is run, interact, or stop. If the command is still running after yield_time_ms, you get a session_id."
}
func (shellTool) ReadOnly() bool      { return false }
func (shellTool) NeedsApproval() bool { return true }
func (shellTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["run","interact","stop"]},"command":{"type":"string","description":"For run."},"cwd":{"type":"string","description":"For run. Defaults to the workspace."},"profile":{"type":"string","enum":["clean","user"],"description":"For run: clean skips the user's startup files."},"yield_time_ms":{"type":"integer"},"timeout_ms":{"type":"integer"},"session_id":{"type":"string"},"chars":{"type":"string"},"force":{"type":"boolean"}},"required":["action"],"additionalProperties":false}`)
}

type liveProc struct {
	cmd    *exec.Cmd
	buf    bytes.Buffer
	mu     sync.Mutex
	sent   int
	done   chan struct{}
	stdin  io.WriteCloser
	exited bool
	code   int
}

var (
	procsMu sync.Mutex
	procs   = map[string]*liveProc{}
	procSeq int
)

func (t shellTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Action  string `json:"action"`
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		Profile string `json:"profile"`
		YieldMS int    `json:"yield_time_ms"`
		Timeout int    `json:"timeout_ms"`
		Session string `json:"session_id"`
		Chars   string `json:"chars"`
		Force   bool   `json:"force"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.Action == "" && in.Command != "" {
		in.Action = "run"
	}
	switch in.Action {
	case "run":
		return t.run(ctx, in.Command, in.Cwd, in.Profile, in.YieldMS, in.Timeout)
	case "interact":
		return interact(ctx, in.Session, in.Chars, in.YieldMS)
	case "stop":
		return stop(in.Session, in.Force)
	default:
		return "", fmt.Errorf("shell action %q is not run, interact, or stop", in.Action)
	}
}

func (t shellTool) run(ctx context.Context, command, cwd, profile string, yieldMS, timeoutMS int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("shell command is empty")
	}
	if yieldMS <= 0 {
		yieldMS = 30000
	}
	if cwd == "" && len(t.ws.Roots) > 0 {
		cwd = t.ws.Roots[0]
	}
	if cwd != "" {
		resolved, err := t.ws.WritePath(cwd)
		if err != nil {
			resolved, err = t.ws.ReadPath(cwd)
			if err != nil {
				return "", err
			}
		}
		cwd = resolved
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	args := []string{"-c", command}
	if profile != "clean" {
		args = []string{"-lc", command}
	}
	cmd := exec.Command(shell, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "AI_AGENT=fx")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	lp := &liveProc{cmd: cmd, done: make(chan struct{}), stdin: stdin}
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { io.Copy(&lockedWriter{lp: lp}, stdout); wg.Done() }()
		go func() { io.Copy(&lockedWriter{lp: lp}, stderr); wg.Done() }()
		wg.Wait()
		err := cmd.Wait()
		lp.mu.Lock()
		lp.exited = true
		lp.code = exitCode(err)
		lp.mu.Unlock()
		close(lp.done)
	}()
	quiet := time.NewTimer(5 * time.Second)
	defer quiet.Stop()
	timer := time.NewTimer(time.Duration(yieldMS) * time.Millisecond)
	defer timer.Stop()
	var timeout <-chan time.Time
	if timeoutMS > 0 {
		t := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
		defer t.Stop()
		timeout = t.C
	}
	quietNoted := false
	for {
		select {
		case <-ctx.Done():
			kill(cmd)
			<-lp.done
			return lp.output() + "\ninterrupted", ctx.Err()
		case <-timeout:
			kill(cmd)
			<-lp.done
			return lp.output() + fmt.Sprintf("\nexit_code: timeout"), nil
		case <-quiet.C:
			lp.mu.Lock()
			n := lp.buf.Len()
			lp.mu.Unlock()
			if n == 0 && !quietNoted {
				Progress(ctx, "quiet: command has produced no output")
				quietNoted = true
			}
		case <-lp.done:
			return fmt.Sprintf("%s\nexit_code: %d", strings.TrimRight(lp.output(), "\n"), lp.code), nil
		case <-timer.C:
			id := store(lp)
			return fmt.Sprintf("%s\n[session %s still running]", strings.TrimRight(lp.output(), "\n"), id), nil
		}
	}
}

type lockedWriter struct{ lp *liveProc }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.lp.mu.Lock()
	defer w.lp.mu.Unlock()
	return w.lp.buf.Write(p)
}

func (lp *liveProc) output() string {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	text := lp.buf.String()
	if lp.sent > len(text) {
		return ""
	}
	out := text[lp.sent:]
	lp.sent = len(text)
	return out
}

func store(lp *liveProc) string {
	procsMu.Lock()
	defer procsMu.Unlock()
	procSeq++
	id := fmt.Sprintf("sh-%d", procSeq)
	procs[id] = lp
	return id
}

func interact(ctx context.Context, id, chars string, yieldMS int) (string, error) {
	procsMu.Lock()
	lp := procs[id]
	procsMu.Unlock()
	if lp == nil {
		return "", fmt.Errorf("shell session %s not found", id)
	}
	if chars != "" {
		_, _ = io.WriteString(lp.stdin, chars)
	}
	if yieldMS <= 0 {
		yieldMS = 5000
	}
	timer := time.NewTimer(time.Duration(yieldMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		kill(lp.cmd)
		return lp.output() + "\ninterrupted", ctx.Err()
	case <-lp.done:
		procsMu.Lock()
		delete(procs, id)
		procsMu.Unlock()
		return fmt.Sprintf("%s\nexit_code: %d", strings.TrimRight(lp.output(), "\n"), lp.code), nil
	case <-timer.C:
		return fmt.Sprintf("%s\n[session %s still running]", strings.TrimRight(lp.output(), "\n"), id), nil
	}
}

func stop(id string, force bool) (string, error) {
	procsMu.Lock()
	lp := procs[id]
	delete(procs, id)
	procsMu.Unlock()
	if lp == nil {
		return "", fmt.Errorf("shell session %s not found", id)
	}
	if force {
		kill(lp.cmd)
	} else if lp.cmd.Process != nil {
		_ = syscall.Kill(-lp.cmd.Process.Pid, syscall.SIGINT)
		time.Sleep(50 * time.Millisecond)
		kill(lp.cmd)
	}
	<-lp.done
	return fmt.Sprintf("%s\nexit_code: %d", strings.TrimRight(lp.output(), "\n"), lp.code), nil
}

func kill(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errorsAs(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func errorsAs(err error, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
		return true
	}
	return false
}

type fetchTool struct{}

func WebFetch() Tool { return fetchTool{} }

func (fetchTool) Name() string { return "web_fetch" }
func (fetchTool) Description() string {
	return "Fetch a public http(s) URL the user named and return its text, which is untrusted."
}
func (fetchTool) ReadOnly() bool      { return true }
func (fetchTool) NeedsApproval() bool { return false }
func (fetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`)
}

func (fetchTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if !strings.HasPrefix(in.URL, "https://") && !strings.HasPrefix(in.URL, "http://") {
		return "", fmt.Errorf("web_fetch only fetches http and https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "fx")
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	text := stripTags(string(body))
	if res.StatusCode >= 300 {
		return fmt.Sprintf("HTTP %d\n%s", res.StatusCode, text), nil
	}
	return text, nil
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// Builtin is the tool set a turn starts with.
func Builtin(ws Workspace) *Registry {
	return NewRegistry(
		ReadFile(ws),
		WriteFile(ws),
		EditFile(ws),
		GrepFiles(ws),
		GlobFiles(ws),
		Shell(ws),
		WebFetch(),
	)
}
