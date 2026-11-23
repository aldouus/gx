// Package app wires the fx command to the agent, the session, and the terminal.
package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
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
		if len(inv.Args) > 0 && inv.Args[0] == "resume" && tui.IsTerminal(tui.StdinFD()) && tui.IsTerminal(tui.StdoutFD()) {
			next := inv
			next.Command = "interactive"
			next.Resume = "last"
			if len(inv.Args) > 1 {
				next.Resume = inv.Args[1]
			}
			return runInteractive(next, settings, home, cwd, in, out, errOut)
		}
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
		fmt.Fprintf(out, "%s\n", signedInText(name))
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
	for _, name := range []string{"codex", "grok"} {
		cred, err := auth.Load(home, name)
		if err != nil || cred.AccessToken == "" {
			fmt.Fprintf(out, "%s: not signed in\n", name)
			continue
		}
		fmt.Fprintf(out, "%s: signed in\n", name)
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

func signedInText(name string) string {
	if name == "" {
		name = "codex"
	}
	return "Signed in with " + strings.ToUpper(name[:1]) + name[1:] + "."
}

func sessionCommand(out, errOut io.Writer, home string, args []string) int {
	if len(args) == 0 {
		return listSessions(out, errOut, home)
	}
	if args[0] == "resume" {
		id := "last"
		if len(args) > 1 {
			id = args[1]
		}
		return openSessionTranscript(out, errOut, home, id)
	}
	return openSessionTranscript(out, errOut, home, args[0])
}

func openSessionTranscript(out, errOut io.Writer, home, id string) int {
	if id == "" || id == "last" {
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
	text := s.TranscriptText()
	_ = s.Close()
	_ = session.Remember(home, id)
	fmt.Fprint(out, text)
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
	case "service_tier":
		return s.ServiceTier
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
	case "service_tier":
		s.ServiceTier = value
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
	emit      func(agent.Event)
	n         atomic.Int64
	mu        sync.Mutex
	persist   map[string]*session.Session
}

func (s *subagents) Message(ctx context.Context, message string, depth int) (string, error) {
	return s.Send(ctx, tool.AgentCall{Message: message, Depth: depth})
}

func (s *subagents) Send(ctx context.Context, call tool.AgentCall) (string, error) {
	if call.To == "parent" {
		if s.parent != nil && s.parent.Session != nil {
			_ = s.parent.Session.Steer("subagent parent:\n" + call.Message)
		}
		if s.inbox != nil {
			s.inbox.Finish("parent", call.Message)
		}
		return "sent to parent", nil
	}
	name := call.Agent
	persistent := name != ""
	if name == "" {
		name = fmt.Sprintf("child-%d", s.n.Add(1))
	}
	var child *session.Session
	var err error
	if persistent {
		s.mu.Lock()
		if s.persist == nil {
			s.persist = map[string]*session.Session{}
		}
		child = s.persist[name]
		s.mu.Unlock()
	}
	if child == nil {
		child, err = session.Create(s.home, s.cwd)
		if err != nil {
			return "", err
		}
		if persistent {
			s.mu.Lock()
			s.persist[name] = child
			s.mu.Unlock()
		}
	}
	childCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.inbox.Start(name, cancel)
	childRunner := *s.parent
	childRunner.Session = child
	childRunner.Depth = call.Depth + 1
	childRunner.Inbox = s.inbox
	if call.Model != "" {
		childRunner.Model = call.Model
	}
	if call.Profile != "" {
		if p, err := config.LoadProfile(s.home, s.cwd, call.Profile); err == nil {
			if p.Prompt != "" {
				childRunner.Extra = p.Prompt + "\n\n" + childRunner.Extra
			}
			if p.Model != "" && call.Model == "" {
				childRunner.Model = p.Model
			}
		}
	}
	mgr := subagent.Manager{Run: func(ctx context.Context, req subagent.Request, emit func(string)) (string, error) {
		if !persistent {
			defer child.Close()
		}
		return childRunner.Turn(ctx, req.Message, func(ev agent.Event) {
			if emit != nil && ev.Text != "" {
				emit(ev.Text)
			}
		})
	}}
	ch, err := mgr.Start(childCtx, subagent.Request{Name: name, Message: call.Message, Depth: call.Depth, Report: "every_turn"})
	if err != nil {
		cancel()
		if !persistent {
			_ = child.Close()
		}
		s.inbox.Finish(name, err.Error())
		return "", err
	}
	go func() {
		var text string
		for rep := range ch {
			if rep.Text != "" {
				text = rep.Text
				if s.emit != nil {
					s.emit(agent.Event{Kind: "text", Name: name, Text: rep.Text})
				}
			}
			if rep.Err != nil && text == "" {
				text = rep.Err.Error()
			}
			if rep.Final {
				break
			}
		}
		if text == "" {
			text = "closed"
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
func (s *subagents) Close(name string) string {
	msg := s.inbox.Close(name)
	s.mu.Lock()
	if child := s.persist[name]; child != nil {
		_ = child.Close()
		delete(s.persist, name)
	}
	s.mu.Unlock()
	return msg
}

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
	roots = append(roots, settings.ExtraDirs...)
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
		extra = skills.Catalog(found) + "\n" + extra
	}
	project, _ := config.LoadProject(cwd)
	if project.Context != "" {
		extra = project.Context + "\n\n" + extra
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
		Provider:    prov,
		Tools:       reg,
		Mode:        mode,
		Model:       model,
		ServiceTier: settings.ServiceTier,
		Session:     sess,
		Inbox:       inbox,
		Images:      inv.Images,
		Extra:       extra,
		Approve: func(ctx context.Context, name, detail string) (agent.Verdict, string, error) {
			if approve != nil {
				return approve(ctx, name, detail)
			}
			fmt.Fprintf(errOut, "Allow %s? %s\n", name, detail)
			return agent.Deny, "", nil
		},
		Grants:    grants,
		Home:      home,
		MaxRounds: project.MaxAgentSteps,
		Workspace: cwd,
	}
	reg.Add(tool.Subagent(&subagents{home: home, cwd: cwd, inbox: inbox, parent: r, emit: on}))
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
	defer tr.End()
	model, _, _ := resolveModel(inv, settings)
	mode := settings.PermissionMode
	if inv.Permission != "" {
		mode = inv.Permission
	}
	if mode == "" {
		mode = string(perm.Default)
	}
	tr.SetStatus(model, mode, signedIn(home))
	tr.SetPlace(cwd, "medium")
	inbox := &agent.Mailbox{}
	grants := &agent.Grants{}
	grants.Bind(home, cwd)
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
			err := handleSubmit(inv, &settings, home, cwd, tr, errOut, act.Submit, &continued, inbox, grants, &images)
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

func handleSubmit(inv cli.Invocation, settings *config.File, home, cwd string, tr *tui.Transcript, errOut io.Writer, line string, continued *bool, inbox *agent.Mailbox, grants *agent.Grants, images *[]string) error {
	if settings == nil {
		settings = &config.File{}
	}
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
				_ = config.Save(home, *settings)
				tr.SetStatus(inv.Model, rest, signedIn(home))
			}
			return tr.User("permission mode " + settings.PermissionMode)
		case "model":
			if rest != "" {
				settings.Model = rest
				_ = config.Save(home, *settings)
				tr.SetStatus(rest, settings.PermissionMode, signedIn(home))
			}
			return tr.User("model " + settings.Model)
		case "clear", "new", "reset":
			*continued = false
			return tr.User("cleared")
		case "status":
			return tr.User(statusLine(inv, *settings, cwd))
		case "shell":
			return tr.User(shellSlash(rest))
		case "allowlist":
			return tr.User(allowlistCommand(home, cwd, rest, grants))
		case "version":
			return tr.User("fx " + cli.Version)
		case "quit", "exit":
			return errQuit
		case "login":
			return loginSlash(home, rest, tr, inv, *settings)
		case "logout":
			return logoutSlash(home, rest, tr, inv, *settings)
		case "image":
			return attachImage(cwd, rest, images, tr)
		case "images":
			return listImages(rest, images, tr)
		case "skills":
			return tr.User(skillsSlash(cwd, home, rest))
		case "rename":
			return tr.User(renameSession(home, rest))
		case "workspace":
			return tr.User(workspaceSlash(cwd, settings, rest))
		case "copy":
			return tr.User(copyLast(tr))
		case "undo":
			return tr.User(undoLast())
		case "effort":
			return tr.User(effortSlash(settings, tr, rest))
		case "settings":
			return tr.User(settingsSlash(settings))
		case "statusline":
			return tr.User(tr.ToggleStatus(rest))
		case "launch":
			return tr.User(launchInstance(rest))
		case "close":
			return errQuit
		case "mcp":
			return tr.User(mcpSlash(home, settings, rest))
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
			return tr.User(compactSession(inv, *settings, home, cwd, continued))
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
	shown := line
	if len(next.Images) > 0 {
		var b strings.Builder
		b.WriteString(line)
		for i, path := range next.Images {
			b.WriteByte('\n')
			b.WriteString(tui.ImageBadge(i+1, path))
		}
		shown = b.String()
	}
	if err := tr.User(shown); err != nil {
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
		choice, note, err := tui.DecidePermission(os.Stdin, tr.SetPermissionAmend)
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
	_, err := execute(context.Background(), next, *settings, home, cwd, line, errOut, true, approve, func(ev agent.Event) {
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

func skillsSlash(cwd, home, rest string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 || fields[0] == "list" {
		return skillList(cwd, home)
	}
	found, err := skills.Discover(cwd, home)
	if err != nil {
		return err.Error()
	}
	switch fields[0] {
	case "show":
		if len(fields) != 2 {
			return "usage: /skills show NAME"
		}
		text, err := skills.Show(found, fields[1])
		if err != nil {
			return err.Error()
		}
		return text
	case "create":
		if len(fields) < 2 {
			return "usage: /skills create NAME"
		}
		path, err := skills.Create(filepath.Join(home, "skills"), fields[1], strings.Join(fields[2:], " "))
		if err != nil {
			return err.Error()
		}
		return "created " + path
	case "add", "install":
		if len(fields) != 2 {
			return "usage: /skills install PATH"
		}
		dst, err := skills.Install(fields[1], filepath.Join(home, "skills"))
		if err != nil {
			return err.Error()
		}
		return "installed " + dst
	case "remove":
		if len(fields) != 2 {
			return "usage: /skills remove NAME"
		}
		if err := skills.Remove(filepath.Join(home, "skills"), fields[1]); err != nil {
			return err.Error()
		}
		return "removed " + fields[1]
	default:
		return "usage: /skills [list|show|add|install|create|remove]"
	}
}

func renameSession(home, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "usage: /rename TITLE"
	}
	id, err := session.Remembered(home)
	if err != nil || id == "" {
		return "no session to rename"
	}
	path := filepath.Join(home, "sessions", id, "title")
	if err := os.WriteFile(path, []byte(title+"\n"), 0o600); err != nil {
		return err.Error()
	}
	return "renamed " + title
}

func workspaceSlash(cwd string, settings *config.File, rest string) string {
	fields := strings.Fields(rest)
	if settings == nil {
		settings = &config.File{}
	}
	if len(fields) == 0 || fields[0] == "list" {
		var b strings.Builder
		b.WriteString(cwd)
		for _, dir := range settings.ExtraDirs {
			b.WriteByte('\n')
			b.WriteString(dir)
		}
		return b.String()
	}
	switch fields[0] {
	case "add":
		if len(fields) != 2 {
			return "usage: /workspace add PATH"
		}
		settings.ExtraDirs = append(settings.ExtraDirs, fields[1])
		return "added " + fields[1]
	case "remove":
		if len(fields) != 2 {
			return "usage: /workspace remove PATH"
		}
		var next []string
		for _, dir := range settings.ExtraDirs {
			if dir != fields[1] {
				next = append(next, dir)
			}
		}
		settings.ExtraDirs = next
		return "removed " + fields[1]
	case "clear":
		settings.ExtraDirs = nil
		return "cleared workspace directories"
	default:
		return "usage: /workspace [list|add PATH|remove PATH|clear]"
	}
}

func copyLast(tr *tui.Transcript) string {
	text := ""
	if tr != nil {
		text = tr.LastAssistant()
	}
	if text == "" {
		return "nothing to copy"
	}
	return "\x1b]52;c;" + b64(text) + "\x1b\\copied"
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func undoLast() string {
	text, err := tool.Undo()
	if err != nil {
		return "nothing to undo"
	}
	return text
}

func effortSlash(settings *config.File, tr *tui.Transcript, rest string) string {
	level := strings.TrimSpace(rest)
	if level == "" {
		if settings != nil && settings.Effort != "" {
			return "effort " + settings.Effort
		}
		return "effort medium"
	}
	switch level {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return "effort is low, medium, high, xhigh, or max"
	}
	if settings != nil {
		settings.Effort = level
		if level == "high" || level == "xhigh" || level == "max" {
			settings.ServiceTier = "priority"
		}
	}
	if tr != nil {
		tr.SetPlace("", level)
	}
	return "effort " + level
}

func settingsSlash(settings *config.File) string {
	if settings == nil {
		return "{}"
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	return string(b)
}

func launchInstance(profile string) string {
	args := []string{}
	if strings.TrimSpace(profile) != "" {
		args = append(args, "--profile", profile)
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err.Error()
	}
	return fmt.Sprintf("launched %d", cmd.Process.Pid)
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
		fmt.Fprintf(&b, "%s  %s  %s\n", sk.Name, sk.Description, sk.Location)
	}
	return strings.TrimRight(b.String(), "\n")
}

func mcpSlash(home string, settings *config.File, rest string) string {
	if settings == nil {
		settings = &config.File{}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 || fields[0] == "list" {
		return mcpList(*settings)
	}
	switch fields[0] {
	case "show":
		if len(fields) != 2 {
			return "usage: /mcp show NAME"
		}
		return mcpShow(*settings, fields[1])
	case "add":
		srv, err := parseMCPAdd(fields[1:])
		if err != nil {
			return err.Error()
		}
		settings.MCPServers = upsertMCP(settings.MCPServers, srv)
		if err := config.Save(home, *settings); err != nil {
			return err.Error()
		}
		return "added " + srv.Name
	case "remove":
		if len(fields) != 2 {
			return "usage: /mcp remove NAME"
		}
		next, ok := removeMCP(settings.MCPServers, fields[1])
		if !ok {
			return "no mcp server " + fields[1]
		}
		settings.MCPServers = next
		if err := config.Save(home, *settings); err != nil {
			return err.Error()
		}
		return "removed " + fields[1]
	default:
		return "usage: /mcp [list|show|add|remove]"
	}
}

func mcpShow(settings config.File, name string) string {
	for _, s := range settings.MCPServers {
		if s.Name != name {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "name  %s\n", s.Name)
		if s.URL != "" {
			fmt.Fprintf(&b, "url  %s\n", s.URL)
		}
		if s.Command != "" {
			fmt.Fprintf(&b, "command  %s\n", s.Command)
		}
		if len(s.Args) > 0 {
			fmt.Fprintf(&b, "args  %s\n", strings.Join(s.Args, " "))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return "no mcp server " + name
}

func parseMCPAdd(args []string) (config.MCPServer, error) {
	if len(args) < 2 {
		return config.MCPServer{}, fmt.Errorf("usage: /mcp add NAME URL | /mcp add NAME -- COMMAND [args...]")
	}
	name := args[0]
	if args[1] == "--" {
		if len(args) < 3 {
			return config.MCPServer{}, fmt.Errorf("usage: /mcp add NAME -- COMMAND [args...]")
		}
		return config.MCPServer{Name: name, Command: args[2], Args: append([]string{}, args[3:]...)}, nil
	}
	if strings.Contains(args[1], "://") {
		return config.MCPServer{Name: name, URL: args[1]}, nil
	}
	return config.MCPServer{Name: name, Command: args[1], Args: append([]string{}, args[2:]...)}, nil
}

func upsertMCP(list []config.MCPServer, srv config.MCPServer) []config.MCPServer {
	for i, s := range list {
		if s.Name == srv.Name {
			list[i] = srv
			return list
		}
	}
	return append(list, srv)
}

func removeMCP(list []config.MCPServer, name string) ([]config.MCPServer, bool) {
	var next []config.MCPServer
	found := false
	for _, s := range list {
		if s.Name == name {
			found = true
			continue
		}
		next = append(next, s)
	}
	return next, found
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
	return tr.User(signedInText(name))
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
	n := 1
	if images != nil {
		*images = append(*images, path)
		n = len(*images)
	}
	return tr.User("attached " + path + "\n" + tui.ImageBadge(n, path))
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

func allowlistCommand(home, cwd, rest string, grants *agent.Grants) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 || fields[0] == "view" {
		scope := agent.ScopeEffective
		if len(fields) > 1 {
			scope = fields[1]
		}
		if len(fields) > 2 {
			return allowlistUsage()
		}
		return formatAllowlist(home, cwd, scope)
	}
	scope := agent.ScopeWorkspace
	cmd := fields[0]
	args := fields[1:]
	if fields[0] == agent.ScopeWorkspace || fields[0] == agent.ScopeGlobal {
		scope = fields[0]
		if len(fields) < 2 {
			return allowlistUsage()
		}
		cmd = fields[1]
		args = fields[2:]
	}
	switch cmd {
	case "add":
		if len(args) < 2 {
			return "usage: /allowlist add <tool> <target>"
		}
		toolName, target := args[0], strings.Join(args[1:], " ")
		if err := agent.AddGrant(scope, home, cwd, toolName, target); err != nil {
			return err.Error()
		}
		if grants != nil {
			grants.Add(toolName, target)
		}
		return "added " + scope + " " + toolName + " " + target
	case "remove":
		if len(args) < 2 {
			return "usage: /allowlist remove <tool> <target>"
		}
		toolName, target := args[0], strings.Join(args[1:], " ")
		if err := agent.RemoveGrant(scope, home, cwd, toolName, target); err != nil {
			return err.Error()
		}
		if grants != nil {
			grants.Drop(toolName, target)
		}
		return "removed " + scope + " " + toolName + " " + target
	case "reset":
		if len(args) != 0 {
			return allowlistUsage()
		}
		if err := agent.ResetGrants(scope, home, cwd); err != nil {
			return err.Error()
		}
		if grants != nil {
			grants.Reset()
		}
		return "reset " + scope + " allowlist"
	default:
		return allowlistUsage()
	}
}

func allowlistUsage() string {
	return "usage: /allowlist [view [effective|workspace|global]|[workspace|global] add|remove|reset ...]"
}

func formatAllowlist(home, cwd, scope string) string {
	list, err := agent.ListGrants(scope, home, cwd)
	if err != nil {
		return err.Error()
	}
	if len(list) == 0 {
		if scope == agent.ScopeEffective || scope == "" {
			return "allowlist is empty"
		}
		return scope + " allowlist is empty"
	}
	var b strings.Builder
	for _, g := range list {
		fmt.Fprintf(&b, "%s  %s  %s\n", g.Scope, g.Tool, g.Target)
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
	r := &agent.Runner{Provider: prov, Model: model, ServiceTier: settings.ServiceTier, Session: sess, Workspace: cwd}
	if err := r.CompactNow(context.Background(), nil); err != nil {
		return err.Error()
	}
	return "compacted"
}
