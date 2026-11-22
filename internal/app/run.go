// Package app wires the fx command to the agent, the session, and the terminal.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gx/internal/agent"
	"gx/internal/auth"
	"gx/internal/cli"
	"gx/internal/config"
	"gx/internal/mcp"
	"gx/internal/perm"
	"gx/internal/provider"
	"gx/internal/session"
	"gx/internal/skills"
	"gx/internal/subagent"
	"gx/internal/tool"
	"gx/internal/tui"
)

// Run executes one fx invocation. args do not include argv0.
func Run(args []string, in io.Reader, out, errOut io.Writer) int {
	inv, err := cli.Parse(args)
	if err != nil {
		if u, ok := err.(*cli.UsageError); ok {
			fmt.Fprintln(errOut, u.Msg)
			return u.Code
		}
		fmt.Fprintln(errOut, err)
		return 1
	}
	switch inv.Command {
	case "help":
		fmt.Fprint(out, cli.HelpText())
		return 0
	case "version":
		fmt.Fprintln(out, cli.Version)
		return 0
	}
	if inv.ShowHelp && inv.Command == "ask" {
		fmt.Fprint(out, cli.AskHelp())
		return 0
	}
	home, err := config.Home()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	settings, err := config.Load(home)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	cwd, _ := os.Getwd()
	switch inv.Command {
	case "status":
		return printStatus(out, home, cwd, settings, inv.JSON)
	case "doctor":
		return doctor(out, errOut, home)
	case "permissions":
		return printPermissions(out, settings, inv.Permission)
	case "models", "provider":
		model, _, _ := resolveModel(inv, settings)
		fmt.Fprintf(out, "%s\t%s\n", providerName(inv, settings), model)
		return 0
	case "workspace":
		fmt.Fprintln(out, cwd)
		for _, d := range inv.AddDirs {
			fmt.Fprintln(out, d)
		}
		return 0
	case "sessions":
		return listSessions(out, errOut, home)
	case "session":
		return sessionCommand(out, errOut, home, inv.Args)
	case "config":
		return configCommand(out, errOut, home, settings, inv.Args)
	case "mcp":
		if len(settings.MCPServers) == 0 {
			fmt.Fprintln(out, "no mcp servers")
			return 0
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for _, s := range settings.MCPServers {
			fmt.Fprintf(out, "%s\t%s\n", s.Name, firstNonEmpty(s.URL, s.Command))
			sess, err := mcp.Dial(ctx, mcp.Server{
				Name: s.Name, Command: s.Command, Args: s.Args, URL: s.URL, Env: s.Env,
			})
			if err != nil {
				fmt.Fprintf(errOut, "mcp %s: %v\n", s.Name, err)
				continue
			}
			listed, err := sess.List(ctx)
			_ = sess.Close()
			if err != nil {
				fmt.Fprintf(errOut, "mcp %s: %v\n", s.Name, err)
				continue
			}
			for _, item := range listed {
				fmt.Fprintf(out, "  %s\n", mcpToolName(s.Name, item.Name))
			}
		}
		return 0
	case "login":
		name := "codex"
		if len(inv.Args) > 0 {
			name = inv.Args[0]
		}
		if name == "vercel" {
			fmt.Fprintln(errOut, "fx: vercel login is not part of this port")
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := auth.Login(ctx, home, name, errOut); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		fmt.Fprintf(out, "signed in to %s\n", name)
		return 0
	case "logout":
		name := "codex"
		if len(inv.Args) > 0 {
			name = inv.Args[0]
		}
		if err := auth.Logout(context.Background(), home, name); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		fmt.Fprintf(out, "signed out of %s\n", name)
		return 0
	case "ask":
		return runAsk(context.Background(), inv, settings, home, cwd, in, out, errOut)
	case "interactive":
		return runInteractive(inv, settings, home, cwd, in, out, errOut)
	default:
		fmt.Fprintf(errOut, "fx: unknown command %s\n", inv.Command)
		return 2
	}
}

func printStatus(out io.Writer, home, cwd string, settings config.File, asJSON bool) int {
	model, origin, _ := resolveModel(cli.Invocation{}, settings)
	mode := settings.PermissionMode
	if mode == "" {
		mode = string(perm.Default)
	}
	if asJSON {
		enc := json.NewEncoder(out)
		_ = enc.Encode(map[string]any{
			"kind":            "status",
			"model":           model,
			"model_origin":    origin,
			"permission_mode": mode,
			"workspace":       cwd,
			"gx_home":         home,
			"provider":        providerName(cli.Invocation{}, settings),
		})
		return 0
	}
	fmt.Fprintf(out, "[status] model=%s\n", model)
	fmt.Fprintf(out, "[status] model_origin=%s\n", origin)
	fmt.Fprintf(out, "[status] permission_mode=%s\n", mode)
	fmt.Fprintf(out, "[status] provider=%s\n", providerName(cli.Invocation{}, settings))
	fmt.Fprintf(out, "[status] workspace=%s\n", cwd)
	fmt.Fprintf(out, "[status] gx_home=%s\n", home)
	if cred, err := auth.Load(home, "codex"); err == nil && cred.AccessToken != "" {
		fmt.Fprintf(out, "[status] auth=codex\n")
	}
	if cred, err := auth.Load(home, "grok"); err == nil && cred.AccessToken != "" {
		fmt.Fprintf(out, "[status] auth=grok\n")
	}
	return 0
}

func doctor(out, errOut io.Writer, home string) int {
	ok := true
	if err := os.MkdirAll(home, 0o700); err != nil {
		fmt.Fprintf(out, "home: %s\n", err)
		ok = false
	} else {
		fmt.Fprintf(out, "home: %s\n", home)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if _, err := os.Stat(shell); err != nil {
		fmt.Fprintf(out, "shell: %s missing\n", shell)
		ok = false
	} else {
		fmt.Fprintf(out, "shell: %s\n", shell)
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Fprintln(out, "openai: no OPENAI_API_KEY")
	} else {
		fmt.Fprintln(out, "openai: key is set")
	}
	if !ok {
		fmt.Fprintln(errOut, "fx doctor: problems found")
		return 1
	}
	return 0
}

func printPermissions(out io.Writer, settings config.File, override string) int {
	mode := override
	if mode == "" {
		mode = settings.PermissionMode
	}
	if mode == "" {
		mode = string(perm.Default)
	}
	fmt.Fprintf(out, "permission_mode=%s\n", mode)
	for _, m := range perm.Modes {
		fmt.Fprintf(out, "%s\t%s\t%s\n", m.ID, m.Name, m.Description)
	}
	return 0
}

func listSessions(out, errOut io.Writer, home string) int {
	ids, err := session.List(home)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if len(ids) == 0 {
		fmt.Fprintln(out, "no sessions")
		return 0
	}
	for _, id := range ids {
		fmt.Fprintln(out, id)
	}
	return 0
}

func sessionCommand(out, errOut io.Writer, home string, args []string) int {
	if len(args) == 0 {
		return listSessions(out, errOut, home)
	}
	if args[0] == "resume" {
		fmt.Fprintln(errOut, "fx session resume opens the interactive terminal. Run fx -r inside a terminal.")
		return 1
	}
	id := args[0]
	if id == "last" {
		ids, err := session.List(home)
		if err != nil || len(ids) == 0 {
			fmt.Fprintln(errOut, "fx: no session")
			return 1
		}
		id = ids[0]
	}
	s, err := session.Open(home, id)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	defer s.Close()
	fmt.Fprint(out, s.TranscriptText())
	return 0
}

func configCommand(out, errOut io.Writer, home string, settings config.File, args []string) int {
	if len(args) == 0 {
		b, _ := json.MarshalIndent(settings, "", "  ")
		fmt.Fprintf(out, "%s\n", b)
		return 0
	}
	switch args[0] {
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(errOut, "fx config get <key>")
			return 2
		}
		fmt.Fprintln(out, settingGet(settings, args[1]))
		return 0
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(errOut, "fx config set <key> <value>")
			return 2
		}
		if err := settingSet(&settings, args[1], strings.Join(args[2:], " ")); err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		if err := config.Save(home, settings); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(errOut, "fx config: get or set")
		return 2
	}
}

func settingGet(s config.File, key string) string {
	switch key {
	case "model":
		return s.Model
	case "permission_mode":
		return s.PermissionMode
	case "provider":
		return s.Provider
	case "base_url":
		return s.BaseURL
	default:
		return ""
	}
}

func settingSet(s *config.File, key, value string) error {
	switch key {
	case "model":
		s.Model = value
	case "permission_mode":
		if _, ok := perm.Parse(value); !ok {
			return fmt.Errorf("permission_mode is ask, auto, or full-access")
		}
		s.PermissionMode = value
	case "provider":
		s.Provider = value
	case "base_url":
		s.BaseURL = value
	default:
		return fmt.Errorf("unknown setting %s", key)
	}
	return nil
}

func runAsk(ctx context.Context, inv cli.Invocation, settings config.File, home, cwd string, in io.Reader, out, errOut io.Writer) int {
	prompt := strings.TrimSpace(strings.Join(inv.Prompt, " "))
	if prompt == "" && in != nil && !tui.IsTerminal(tui.StdinFD()) {
		b, _ := io.ReadAll(in)
		prompt = strings.TrimSpace(string(b))
	}
	if prompt == "" {
		fmt.Fprintln(errOut, "fx ask: missing prompt")
		fmt.Fprintln(errOut, strings.TrimSpace(cli.AskHelp()))
		return 2
	}
	for _, img := range inv.Images {
		prompt += "\n[image: " + img + "]"
	}
	text, err := execute(ctx, inv, settings, home, cwd, prompt, errOut, false, nil, nil)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if inv.JSON {
		enc := json.NewEncoder(out)
		_ = enc.Encode(map[string]string{"kind": "ask", "final_output": text, "output": text})
		return 0
	}
	if !inv.Quiet {
		fmt.Fprintln(out, text)
	}
	return 0
}

func execute(ctx context.Context, inv cli.Invocation, settings config.File, home, cwd, prompt string, errOut io.Writer, remember bool, approve agent.Approval, on func(agent.Event)) (string, error) {
	model, _, err := resolveModel(inv, settings)
	if err != nil {
		return "", err
	}
	mode := perm.Default
	if settings.PermissionMode != "" {
		if m, ok := perm.Parse(settings.PermissionMode); ok {
			mode = m
		}
	}
	if inv.Permission != "" {
		m, ok := perm.Parse(inv.Permission)
		if !ok {
			return "", fmt.Errorf("unknown permission mode %s", inv.Permission)
		}
		mode = m
	}
	roots := []string{cwd}
	roots = append(roots, inv.AddDirs...)
	key := os.Getenv("OPENAI_API_KEY")
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = settings.BaseURL
	}
	if key == "" {
		return "", fmt.Errorf("no API key. Set OPENAI_API_KEY or run fx login")
	}
	prov := provider.OpenAI{BaseURL: base, APIKey: key}
	root := home
	var sess *session.Session
	if inv.NoSave {
		root = filepath.Join(os.TempDir(), "gx-nosave")
	}
	if inv.Continue || inv.Resume != "" {
		id := ""
		if inv.Continue {
			id, _ = session.Remembered(home)
		}
		if inv.Resume == "last" || (inv.Resume == "" && id == "") {
			ids, _ := session.List(home)
			if len(ids) > 0 {
				id = ids[0]
			}
		} else if inv.Resume != "" && inv.Resume != "pick" {
			id = inv.Resume
		}
		if id == "" {
			return "", fmt.Errorf("no session to resume")
		}
		sess, err = session.Open(home, id)
		if err != nil {
			return "", err
		}
	} else {
		sess, err = session.Create(root, cwd)
		if err != nil {
			return "", err
		}
	}
	defer sess.Close()
	if remember {
		_ = session.Remember(home, sess.ID())
	}
	extra, _ := config.ProjectInstructions(home, cwd, cwd)
	if found, err := skills.Discover(cwd, home); err == nil && len(found) > 0 {
		var b strings.Builder
		b.WriteString("Skills available. Load a skill before following it.\n")
		for _, sk := range found {
			fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
		}
		extra = b.String() + "\n" + extra
	}
	if inv.System != "" {
		extra = inv.System + "\n\n" + extra
	}
	if inv.Profile != "" {
		p, err := config.LoadProfile(home, cwd, inv.Profile)
		if err != nil {
			return "", err
		}
		if p.Prompt != "" {
			extra = p.Prompt + "\n\n" + extra
		}
		if p.Model != "" && inv.Model == "" {
			model = p.Model
		}
		if p.PermissionMode != "" && inv.Permission == "" {
			if m, ok := perm.Parse(p.PermissionMode); ok {
				mode = m
			}
		}
	}
	reg := tool.Builtin(tool.Workspace{Roots: roots})
	if found, err := skills.Discover(cwd, home); err == nil {
		reg.Add(tool.Skill(found))
	}
	r := &agent.Runner{
		Provider:  prov,
		Tools:     reg,
		Mode:      mode,
		Model:     model,
		Session:   sess,
		Workspace: cwd,
		Extra:     extra,
		Approve: func(ctx context.Context, name, detail string) (bool, error) {
			if approve != nil {
				return approve(ctx, name, detail)
			}
			fmt.Fprintf(errOut, "Allow %s? %s\n", name, detail)
			return false, nil
		},
	}
	reg.Add(tool.Subagent(func(ctx context.Context, message string, depth int) (string, error) {
		child, err := session.Create(home, cwd)
		if err != nil {
			return "", err
		}
		defer child.Close()
		childRunner := *r
		childRunner.Session = child
		childRunner.Depth = depth + 1
		mgr := subagent.Manager{Run: func(ctx context.Context, req subagent.Request, emit func(string)) (string, error) {
			return childRunner.Turn(ctx, req.Message, nil)
		}}
		ch, err := mgr.Start(ctx, subagent.Request{Name: "child", Message: message, Depth: depth, Report: "final"})
		if err != nil {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case rep, ok := <-ch:
			if !ok {
				return "", fmt.Errorf("subagent closed")
			}
			if rep.Err != nil {
				return rep.Text, rep.Err
			}
			return rep.Text, nil
		}
	}))
	sessions := attachMCP(ctx, reg, settings.MCPServers, errOut)
	defer closeMCP(sessions)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if inv.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(inv.TimeoutSec)*time.Second)
		defer cancel()
	}
	return r.Turn(ctx, prompt, func(ev agent.Event) {
		if on != nil {
			on(ev)
			return
		}
		if ev.Kind == "tool_start" {
			fmt.Fprintf(errOut, "Running %s\n", ev.Name)
		}
	})
}

func runInteractive(inv cli.Invocation, settings config.File, home, cwd string, in io.Reader, out, errOut io.Writer) int {
	if !tui.IsTerminal(tui.StdinFD()) || !tui.IsTerminal(tui.StdoutFD()) {
		fmt.Fprintln(errOut, "fx requires an interactive terminal (TTY).")
		return 1
	}
	restore, err := tui.EnterRaw(tui.StdinFD())
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	defer restore()
	_, cols := tui.WindowSize(tui.StdoutFD())
	tr := tui.NewTranscript(os.Stdout, cols)
	model, _, _ := resolveModel(inv, settings)
	mode := settings.PermissionMode
	if inv.Permission != "" {
		mode = inv.Permission
	}
	if mode == "" {
		mode = string(perm.Default)
	}
	tr.SetStatus(model, mode, os.Getenv("OPENAI_API_KEY") != "")
	var comp tui.Composer
	tr.SetInput(comp.Line, comp.Pos)
	if err := tr.Paint(); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	buf := make([]byte, 256)
	pending := strings.TrimSpace(strings.Join(inv.Prompt, " "))
	for {
		var act tui.Action
		if pending != "" {
			act.Submit = pending
			pending = ""
		} else {
			n, err := in.Read(buf)
			if err != nil {
				return 0
			}
			act = comp.Feed(buf[:n])
		}
		if act.Exit {
			return 0
		}
		if act.Detail {
			tr.ToggleDetail()
		}
		tr.SetCtrlC(comp.Armed)
		tr.SetInput(comp.Line, comp.Pos)
		if act.Submit != "" {
			if err := handleSubmit(inv, settings, home, cwd, tr, errOut, act.Submit); err != nil {
				_ = tr.FinishText()
			}
			tr.SetInput("", 0)
		}
		if err := tr.Paint(); err != nil {
			return 1
		}
	}
}

func handleSubmit(inv cli.Invocation, settings config.File, home, cwd string, tr *tui.Transcript, errOut io.Writer, line string) error {
	if name, rest, ok := cli.MatchSlash(line); strings.HasPrefix(strings.TrimSpace(line), "/") {
		if !ok {
			return tr.User("unknown command /" + name)
		}
		switch name {
		case "help":
			var b strings.Builder
			for _, c := range cli.SlashCommands {
				fmt.Fprintf(&b, "/%s  %s\n", c.Name, c.Help)
			}
			return tr.User(strings.TrimRight(b.String(), "\n"))
		case "permissions":
			if rest != "" {
				if _, ok := perm.Parse(rest); !ok {
					return tr.User("permission mode is ask, auto, or full-access")
				}
				settings.PermissionMode = rest
				_ = config.Save(home, settings)
				tr.SetStatus(inv.Model, rest, os.Getenv("OPENAI_API_KEY") != "")
			}
			return tr.User("permission mode " + settings.PermissionMode)
		case "model":
			if rest != "" {
				settings.Model = rest
				_ = config.Save(home, settings)
				tr.SetStatus(rest, settings.PermissionMode, os.Getenv("OPENAI_API_KEY") != "")
			}
			return tr.User("model " + settings.Model)
		default:
			return tr.User("/" + name + " is not available yet")
		}
	}
	if err := tr.User(line); err != nil {
		return err
	}
	tr.SetBusy(true)
	defer tr.SetBusy(false)
	approve := func(ctx context.Context, name, detail string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := tr.Permission(name, detail); err != nil {
			return false, err
		}
		buf := make([]byte, 32)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return false, err
			}
			for _, b := range buf[:n] {
				switch b {
				case 'y', 'Y':
					_ = tr.ClearPermission()
					return true, nil
				case 'n', 'N', 3, 0x1b:
					_ = tr.ClearPermission()
					return false, nil
				}
			}
		}
	}
	_, err := execute(context.Background(), inv, settings, home, cwd, line, errOut, true, approve, func(ev agent.Event) {
		switch ev.Kind {
		case "text":
			_ = tr.Token(ev.Text)
		case "text_done":
			_ = tr.FinishText()
		case "tool_start":
			_ = tr.ToolStart(ev.Name, ev.Detail)
		case "tool_done":
			_ = tr.ToolDone(ev.Name, ev.Text)
		case "permission":
			_ = tr.Permission(ev.Name, ev.Detail)
		case "compact":
			_ = tr.Token(ev.Text + "\n")
		}
	})
	return err
}

func resolveModel(inv cli.Invocation, settings config.File) (model, origin string, err error) {
	if inv.Model != "" {
		return inv.Model, "flag", nil
	}
	if m := os.Getenv("GX_MODEL"); m != "" {
		return m, "env", nil
	}
	if m := os.Getenv("OPENAI_MODEL"); m != "" {
		return m, "env", nil
	}
	if settings.Model != "" {
		return settings.Model, "settings", nil
	}
	return "gpt-4o-mini", "default", nil
}

func providerName(inv cli.Invocation, settings config.File) string {
	if inv.Provider != "" {
		return inv.Provider
	}
	if settings.Provider != "" {
		return settings.Provider
	}
	return "openai"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
