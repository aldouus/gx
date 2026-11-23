// Package app wires the fx command to the agent, the session, and the terminal.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"gx/internal/agent"
	"gx/internal/auth"
	"gx/internal/cli"
	"gx/internal/config"
	"gx/internal/perm"
	"gx/internal/provider"
	"gx/internal/session"
	"gx/internal/skills"
	"gx/internal/subagent"
	"gx/internal/tool"
	"gx/internal/tui"
)

var errQuit = errors.New("quit")

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
		for _, s := range settings.MCPServers {
			fmt.Fprintf(out, "%s\t%s\n", s.Name, firstNonEmpty(s.URL, s.Command))
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
	text, err := execute(ctx, inv, settings, home, cwd, prompt, errOut, false, nil, nil, nil, nil)
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

type subagents struct {
	home, cwd string
	inbox     *agent.Mailbox
	parent    *agent.Runner
	n         atomic.Int64
}

func (s *subagents) Message(ctx context.Context, message string, depth int) (string, error) {
	name := fmt.Sprintf("child-%d", s.n.Add(1))
	child, err := session.Create(s.home, s.cwd)
	if err != nil {
		return "", err
	}
	childCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.inbox.Start(name, cancel)
	childRunner := *s.parent
	childRunner.Session = child
	childRunner.Depth = depth + 1
	childRunner.Inbox = s.inbox
	mgr := subagent.Manager{Run: func(ctx context.Context, req subagent.Request, emit func(string)) (string, error) {
		defer child.Close()
		return childRunner.Turn(ctx, req.Message, nil)
	}}
	ch, err := mgr.Start(childCtx, subagent.Request{Name: name, Message: message, Depth: depth, Report: "final"})
	if err != nil {
		cancel()
		_ = child.Close()
		s.inbox.Finish(name, err.Error())
		return "", err
	}
	go func() {
		rep, ok := <-ch
		if !ok {
			s.inbox.Finish(name, "closed")
			return
		}
		text := rep.Text
		if rep.Err != nil && text == "" {
			text = rep.Err.Error()
		}
		s.inbox.Finish(name, text)
	}()
	return "subagent " + name + " started", nil
}

func (s *subagents) Wait(ctx context.Context, name string) (string, error) {
	return s.inbox.WaitReport(ctx, name)
}

func (s *subagents) Status(name string) string { return s.inbox.Status(name) }
func (s *subagents) Stop(name string) string   { return s.inbox.Stop(name) }
func (s *subagents) Close(name string) string  { return s.inbox.Close(name) }

func execute(ctx context.Context, inv cli.Invocation, settings config.File, home, cwd, prompt string, errOut io.Writer, remember bool, approve agent.Approval, on func(agent.Event), inbox *agent.Mailbox, grants *agent.Grants) (string, error) {
	model, origin, err := resolveModel(inv, settings)
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
	prov, model, err := pickProvider(inv, settings, home, model, origin)
	if err != nil {
		return "", err
	}
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
	if inbox == nil {
		inbox = &agent.Mailbox{}
	}
	r := &agent.Runner{
		Provider:  prov,
		Tools:     reg,
		Mode:      mode,
		Model:     model,
		Session:   sess,
		Inbox:     inbox,
		Images:    inv.Images,
		Workspace: cwd,
		Extra:     extra,
		Approve: func(ctx context.Context, name, detail string) (agent.Verdict, string, error) {
			if approve != nil {
				return approve(ctx, name, detail)
			}
			fmt.Fprintf(errOut, "Allow %s? %s\n", name, detail)
			return agent.Deny, "", nil
		},
		Grants: grants,
	}
	reg.Add(tool.Subagent(&subagents{home: home, cwd: cwd, inbox: inbox, parent: r}))
	sessions := attachMCP(ctx, reg, settings.MCPServers, errOut)
	defer closeMCP(sessions)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if inv.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(inv.TimeoutSec)*time.Second)
		defer cancel()
	}
	text, err := r.Turn(ctx, prompt, func(ev agent.Event) {
		if on != nil {
			on(ev)
			return
		}
		if ev.Kind == "tool_start" {
			fmt.Fprintf(errOut, "Running %s\n", ev.Name)
		}
	})
	if err != nil || on != nil {
		return text, err
	}
	for {
		notes := inbox.Take()
		if len(notes) == 0 {
			if inbox.Pending() == 0 {
				return text, nil
			}
			if err := inbox.Wait(ctx); err != nil {
				return text, err
			}
			continue
		}
		text, err = r.Turn(ctx, strings.Join(notes, "\n\n"), nil)
		if err != nil {
			return text, err
		}
	}
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
	tr.SetStatus(model, mode, signedIn(home))
	inbox := &agent.Mailbox{}
	grants := &agent.Grants{}
	var images []string
	continued := inv.Continue || inv.Resume != ""
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
			err := handleSubmit(inv, settings, home, cwd, tr, errOut, act.Submit, &continued, inbox, grants, &images)
			if err == errQuit {
				return 0
			}
			if err != nil {
				_ = tr.FinishText()
			}
			tr.SetInput("", 0)
		}
		if err := tr.Paint(); err != nil {
			return 1
		}
	}
}

func handleSubmit(inv cli.Invocation, settings config.File, home, cwd string, tr *tui.Transcript, errOut io.Writer, line string, continued *bool, inbox *agent.Mailbox, grants *agent.Grants, images *[]string) error {
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
				tr.SetStatus(inv.Model, rest, signedIn(home))
			}
			return tr.User("permission mode " + settings.PermissionMode)
		case "model":
			if rest != "" {
				settings.Model = rest
				_ = config.Save(home, settings)
				tr.SetStatus(rest, settings.PermissionMode, signedIn(home))
			}
			return tr.User("model " + settings.Model)
		case "clear", "new", "reset":
			*continued = false
			return tr.User("cleared")
		case "status":
			return tr.User(statusLine(inv, settings, cwd))
		case "shell":
			return tr.User(shellSlash(rest))
		case "allowlist":
			return tr.User(allowlistText(grants))
		case "version":
			return tr.User("fx " + cli.Version)
		case "quit", "exit":
			return errQuit
		case "login":
			return loginSlash(home, rest, tr, inv, settings)
		case "logout":
			return logoutSlash(home, rest, tr, inv, settings)
		case "image":
			return attachImage(cwd, rest, images, tr)
		case "images":
			return listImages(rest, images, tr)
		case "skills":
			return tr.User(skillList(cwd, home))
		case "mcp":
			return tr.User(mcpList(settings))
		case "agents":
			return tr.User(agentList(inbox))
		case "resume":
			text, err := resumeSession(home, rest)
			if err != nil {
				return tr.User(err.Error())
			}
			*continued = true
			return tr.User(text)
		case "fork":
			text, err := forkSession(home, continued)
			if err != nil {
				return tr.User(err.Error())
			}
			return tr.User(text)
		case "compact":
			return tr.User(compactSession(inv, settings, home, cwd, continued))
		default:
			return tr.User("/" + name + " is not available yet")
		}
	}
	next := inv
	if continued != nil && *continued {
		next.Continue = true
		next.Resume = ""
	}
	if images != nil && len(*images) > 0 {
		next.Images = append(append([]string{}, next.Images...), *images...)
		*images = nil
	}
	if err := tr.User(line); err != nil {
		return err
	}
	tr.SetBusy(true)
	defer tr.SetBusy(false)
	approve := func(ctx context.Context, name, detail string) (agent.Verdict, string, error) {
		if err := ctx.Err(); err != nil {
			return agent.Deny, "", err
		}
		if err := tr.Permission(name, detail); err != nil {
			return agent.Deny, "", err
		}
		choice, note, err := tui.DecidePermission(os.Stdin, tr.SetPermission)
		_ = tr.ClearPermission()
		if err != nil {
			return agent.Deny, note, err
		}
		switch choice {
		case 0:
			return agent.Once, note, nil
		case 1:
			return agent.Always, note, nil
		default:
			return agent.Deny, note, nil
		}
	}
	_, err := execute(context.Background(), next, settings, home, cwd, line, errOut, true, approve, func(ev agent.Event) {
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
	}, inbox, grants)
	if err == nil && continued != nil {
		*continued = true
	}
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

func signedIn(home string) bool {
	if os.Getenv("OPENAI_API_KEY") != "" {
		return true
	}
	if _, err := auth.Load(home, "codex"); err == nil {
		return true
	}
	_, err := auth.Load(home, "grok")
	return err == nil
}

func pickProvider(inv cli.Invocation, settings config.File, home, model, origin string) (provider.Provider, string, error) {
	name := providerName(inv, settings)
	if name == "codex" || name == "grok" {
		p, model, err := subscriptionProvider(home, name, model, origin)
		return p, model, err
	}
	key := os.Getenv("OPENAI_API_KEY")
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = settings.BaseURL
	}
	if key != "" {
		return provider.OpenAI{BaseURL: base, APIKey: key}, model, nil
	}
	for _, candidate := range []string{"grok", "codex"} {
		if _, err := auth.Load(home, candidate); err == nil {
			return subscriptionProvider(home, candidate, model, origin)
		}
	}
	return nil, model, fmt.Errorf("no API key. Set OPENAI_API_KEY or run fx login")
}

func subscriptionProvider(home, name, model, origin string) (provider.Provider, string, error) {
	cred, err := auth.Load(home, name)
	if err != nil {
		return nil, model, fmt.Errorf("no %s login. Run fx login %s", name, name)
	}
	if origin == "default" {
		if name == "grok" {
			model = "grok-4.7"
		} else {
			model = "gpt-5.1-codex"
		}
	}
	header := auth.Authorization(cred)
	token := strings.TrimPrefix(header, "Bearer ")
	return provider.Responses{Kind: name, APIKey: token, AccountID: cred.AccountID}, model, nil
}

func skillList(cwd, home string) string {
	found, err := skills.Discover(cwd, home)
	if err != nil {
		return err.Error()
	}
	if len(found) == 0 {
		return "no skills"
	}
	var b strings.Builder
	for _, sk := range found {
		fmt.Fprintf(&b, "%s  %s\n", sk.Name, sk.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

func mcpList(settings config.File) string {
	if len(settings.MCPServers) == 0 {
		return "no mcp servers"
	}
	var b strings.Builder
	for _, s := range settings.MCPServers {
		fmt.Fprintf(&b, "%s  %s\n", s.Name, firstNonEmpty(s.URL, s.Command))
	}
	return strings.TrimRight(b.String(), "\n")
}

func agentList(inbox *agent.Mailbox) string {
	kids := inbox.Agents()
	if len(kids) == 0 {
		return "no agents"
	}
	var b strings.Builder
	for _, k := range kids {
		state := "working"
		if k.Done {
			state = "done"
		}
		fmt.Fprintf(&b, "%s  %s\n", k.Name, state)
	}
	return strings.TrimRight(b.String(), "\n")
}

func loginSlash(home, rest string, tr *tui.Transcript, inv cli.Invocation, settings config.File) error {
	name := "codex"
	if rest != "" {
		name = rest
	}
	if name == "vercel" {
		return tr.User("vercel login is not part of this port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var shown strings.Builder
	_, err := auth.Login(ctx, home, name, writerFunc(func(p []byte) (int, error) {
		shown.Write(p)
		return len(p), tr.User(strings.TrimRight(shown.String(), "\n"))
	}))
	if err != nil {
		return tr.User(err.Error())
	}
	model, _, _ := resolveModel(inv, settings)
	mode := settings.PermissionMode
	if mode == "" {
		mode = string(perm.Default)
	}
	tr.SetStatus(model, mode, signedIn(home))
	return tr.User("signed in to " + name)
}

func logoutSlash(home, rest string, tr *tui.Transcript, inv cli.Invocation, settings config.File) error {
	name := "codex"
	if rest != "" {
		name = rest
	}
	if err := auth.Logout(context.Background(), home, name); err != nil {
		return tr.User(err.Error())
	}
	model, _, _ := resolveModel(inv, settings)
	mode := settings.PermissionMode
	if mode == "" {
		mode = string(perm.Default)
	}
	tr.SetStatus(model, mode, signedIn(home))
	return tr.User("signed out of " + name)
}

func attachImage(cwd, rest string, images *[]string, tr *tui.Transcript) error {
	if strings.TrimSpace(rest) == "" {
		return tr.User("usage: /image PATH")
	}
	path := rest
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	if _, err := os.Stat(path); err != nil {
		return tr.User(err.Error())
	}
	if images != nil {
		*images = append(*images, path)
	}
	return tr.User("attached " + path)
}

func listImages(rest string, images *[]string, tr *tui.Transcript) error {
	if rest == "clear" {
		if images != nil {
			*images = nil
		}
		return tr.User("images cleared")
	}
	if images == nil || len(*images) == 0 {
		return tr.User("no images attached")
	}
	return tr.User(strings.Join(*images, "\n"))
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func shellSlash(rest string) string {
	if rest != "reload" {
		return "usage: /shell reload"
	}
	tool.ResetSnapshot()
	return "shell snapshot cleared"
}

func statusLine(inv cli.Invocation, settings config.File, cwd string) string {
	model, _, _ := resolveModel(inv, settings)
	mode := settings.PermissionMode
	if inv.Permission != "" {
		mode = inv.Permission
	}
	if mode == "" {
		mode = string(perm.Default)
	}
	return "model " + model + "\npermission " + mode + "\nworkspace " + cwd
}

func allowlistText(grants *agent.Grants) string {
	list := grants.List()
	if len(list) == 0 {
		return "allowlist is empty"
	}
	var b strings.Builder
	for _, g := range list {
		fmt.Fprintf(&b, "%s  %s\n", g.Tool, g.Target)
	}
	return strings.TrimRight(b.String(), "\n")
}

func forkSession(home string, continued *bool) (string, error) {
	if continued == nil || !*continued {
		return "", fmt.Errorf("nothing to fork")
	}
	id, err := session.Remembered(home)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("nothing to fork")
	}
	src, err := session.Open(home, id)
	if err != nil {
		return "", err
	}
	dst, err := src.Copy()
	srcErr := src.Close()
	if err != nil {
		return "", err
	}
	if srcErr != nil {
		_ = dst.Close()
		return "", srcErr
	}
	newID := dst.ID()
	if err := dst.Close(); err != nil {
		return "", err
	}
	if err := session.Remember(home, newID); err != nil {
		return "", err
	}
	return "forked " + newID, nil
}

func resumeSession(home, rest string) (string, error) {
	id := rest
	if id == "" || id == "last" {
		ids, err := session.List(home)
		if err != nil {
			return "", err
		}
		if len(ids) == 0 {
			return "", fmt.Errorf("no session to resume")
		}
		id = ids[0]
	}
	sess, err := session.Open(home, id)
	if err != nil {
		return "", err
	}
	text := sess.TranscriptText()
	_ = sess.Close()
	if err := session.Remember(home, id); err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		text = "resumed " + id
	}
	return text, nil
}

func compactSession(inv cli.Invocation, settings config.File, home, cwd string, continued *bool) string {
	model, origin, err := resolveModel(inv, settings)
	if err != nil {
		return err.Error()
	}
	prov, model, err := pickProvider(inv, settings, home, model, origin)
	if err != nil {
		return err.Error()
	}
	var sess *session.Session
	if continued != nil && *continued {
		id, _ := session.Remembered(home)
		if id != "" {
			sess, err = session.Open(home, id)
		}
	}
	if sess == nil {
		return "nothing to compact"
	}
	defer sess.Close()
	r := &agent.Runner{Provider: prov, Model: model, Session: sess, Workspace: cwd}
	if err := r.CompactNow(context.Background(), nil); err != nil {
		return err.Error()
	}
	return "compacted"
}
