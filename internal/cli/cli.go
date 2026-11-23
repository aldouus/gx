// Package cli parses argv for the fx binary.
package cli

import (
	"fmt"
	"strings"
)

const Version = "0.0.1"

// UsageError is a bad argv. Code is the process exit code.
type UsageError struct {
	Code int
	Msg  string
}

func (e *UsageError) Error() string { return e.Msg }

// Invocation is a parsed command line.
type Invocation struct {
	Command     string
	Prompt      []string
	JSON        bool
	Quiet       bool
	Model       string
	Permission  string
	Resume      string
	Continue    bool
	Profile     string
	Provider    string
	System      string
	NoSave      bool
	TimeoutSec  int
	AddDirs     []string
	Images      []string
	Args        []string
	ShowHelp    bool
	ShowVersion bool
}

var commands = map[string]bool{
	"ask": true, "help": true, "status": true, "session": true, "sessions": true,
	"login": true, "logout": true, "models": true, "permissions": true, "mcp": true,
	"config": true, "doctor": true, "workspace": true, "provider": true,
	"completion": true, "version": true,
}

// Parse reads args, which do not include argv0.
func Parse(args []string) (Invocation, error) {
	var inv Invocation
	inv.Command = "interactive"
	var positionals []string
	seenCommand := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if !seenCommand && commands[a] {
				inv.Command = a
				seenCommand = true
				continue
			}
			positionals = append(positionals, a)
			continue
		}
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", &UsageError{Code: 2, Msg: "fx: missing value for " + a}
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "-h" || a == "--help":
			inv.ShowHelp = true
		case a == "-v" || a == "--version":
			inv.ShowVersion = true
		case a == "--json":
			inv.JSON = true
		case a == "--quiet":
			inv.Quiet = true
		case a == "--no-save":
			inv.NoSave = true
		case a == "-c" || a == "--continue":
			inv.Continue = true
		case a == "--ask":
			inv.Permission = "ask"
		case a == "--auto":
			inv.Permission = "auto"
		case a == "--full-access" || a == "--yolo":
			inv.Permission = "full-access"
		case a == "--model":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			inv.Model = s
		case strings.HasPrefix(a, "--model="):
			inv.Model = strings.TrimPrefix(a, "--model=")
		case a == "--permission-mode":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			inv.Permission = s
		case a == "--profile":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			inv.Profile = s
		case a == "--provider":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			inv.Provider = s
		case a == "--system":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			inv.System = s
		case a == "--resume" || a == "-r":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				inv.Resume = args[i]
			} else {
				inv.Resume = "pick"
			}
		case strings.HasPrefix(a, "--resume="):
			inv.Resume = strings.TrimPrefix(a, "--resume=")
		case a == "--add-dir":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			inv.AddDirs = append(inv.AddDirs, s)
		case a == "--image":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			inv.Images = append(inv.Images, s)
		case a == "--timeout":
			s, err := val()
			if err != nil {
				return Invocation{}, err
			}
			n, err := fmt.Sscanf(s, "%d", &inv.TimeoutSec)
			if err != nil || n != 1 {
				return Invocation{}, &UsageError{Code: 2, Msg: "fx: timeout wants seconds"}
			}
		default:
			return Invocation{}, &UsageError{Code: 2, Msg: "fx: unknown flag " + a + "\n" + shortUsage()}
		}
	}
	if inv.ShowVersion {
		inv.Command = "version"
	}
	if inv.ShowHelp && (inv.Command == "interactive" || inv.Command == "help") {
		inv.Command = "help"
	}
	if inv.ShowHelp && inv.Command != "help" {
		inv.Args = []string{"--help"}
		return inv, nil
	}
	switch inv.Command {
	case "ask":
		inv.Prompt = positionals
	case "interactive":
		inv.Prompt = positionals
	default:
		inv.Args = positionals
	}
	return inv, nil
}

func shortUsage() string {
	return "usage: fx [--model <id>] [--permission-mode <mode>] [--ask|--auto|--full-access] [--profile <name>] [-c|--continue] [-r|--resume [last|<id>]] <command>"
}

// HelpText is the top-level help a person reads.
func HelpText() string {
	return strings.TrimSpace(`
fx `+Version+`
Fast, native coding agent for the terminal.

fx starts an interactive session by default. Use `+"`fx ask`"+` to run one
noninteractive request.

Usage:
  fx [flags]
  fx [flags] "<prompt>"
  fx <command> [...flags] [...args]

Commands:
  ask <prompt>                  Run one noninteractive request
  sessions                      List saved sessions for the current workspace
  session resume [last|id]      Resume the latest session or a session by id
  login [codex|grok]            Sign in to Codex or Grok
  logout [codex|grok]           Sign out of Codex or Grok
  provider                      Show the active model provider
  models                        List the configured model
  status                        Show configuration and runtime information
  doctor                        Check that fx can work here
  mcp                           Manage MCP servers
  permissions                   Show the permission mode
  workspace                     Show workspace directories
  config                        Show and change fx settings
  help                          Show this help

Flags:
  --model <id>                  Override the model
  --provider <name>             openai, codex, or grok
  --permission-mode <mode>      ask, auto, or full-access
  --ask, --auto, --full-access  Shortcuts for each permission mode
  --profile <name>              Agent profile from ~/.gx/agents or .gx/agents
  --add-dir <path>              Add a workspace directory
  -c, --continue                Resume the remembered workspace session
  -r, --resume [last|<id>]      Resume the latest session or an exact id
  --image PATH                  Attach an image file
  -h, --help                    Display this help and exit
  -v, --version                 Print the fx version and exit

Examples:
  fx
      Start a fresh interactive session

  fx ask "Explain the changes in this repository"
      Run one request and exit

  fx session resume last
      Continue the latest session for this workspace

  fx status --json
      Inspect the current configuration as JSON

Run `+"`fx <command> --help`"+` for command-specific usage.
Run `+"`/help`"+` inside an interactive session for slash commands.
`) + "\n"
}

// AskHelp is fx ask --help.
func AskHelp() string {
	return strings.TrimSpace(`
fx ask

Run one noninteractive request

Usage:
  fx ask [--permission-mode <mode>|--ask|--auto|--full-access] [--provider <name>] [--model <id>] [--json] [--quiet] [--system TEXT] [--image PATH] [--resume <last|<id>>] [--] <prompt>

The prompt may be passed as arguments or piped on stdin when no prompt args are given.
Operational progress and diagnostics are written to stderr.
With --json, final_output is the completed assistant text.
`) + "\n"
}

// Slash is one slash command.
type Slash struct {
	Name string
	Help string
}

// SlashCommands is the interactive command table.
var SlashCommands = []Slash{
	{"help", "List slash commands"},
	{"model", "Show or set the model"},
	{"permissions", "Show or set ask, auto, or full-access"},
	{"compact", "Compact the conversation"},
	{"resume", "Resume a session"},
	{"fork", "Copy this session and continue in the copy"},
	{"clear", "Start a fresh conversation"},
	{"new", "Start a fresh session"},
	{"reset", "Reset the current session"},
	{"skills", "List, show, add, install, create, or remove skills"},
	{"rename", "Rename the current session"},
	{"workspace", "List or add workspace directories"},
	{"copy", "Copy the last assistant response"},
	{"undo", "Undo the latest file write"},
	{"effort", "Set the reasoning effort"},
	{"settings", "Show settings"},
	{"statusline", "Toggle footer segments"},
	{"launch", "Start another fx instance"},
	{"close", "Close this fx instance"},
	{"mcp", "List, show, add, remove, log in, or approve MCP servers"},
	{"agents", "List subagents"},
	{"status", "Show the model, permission mode, and workspace"},
	{"shell", "Reload shell startup files"},
	{"allowlist", "View, add, remove, or reset trusted tools"},
	{"login", "Sign in to codex or grok"},
	{"logout", "Sign out of codex or grok"},
	{"image", "Attach an image to the next prompt"},
	{"images", "List or clear pending images"},
	{"version", "Show the fx version"},
	{"quit", "Exit"},
	{"exit", "Exit"},
}

// MatchSlash returns the command name and the rest of the line.
func MatchSlash(line string) (name, rest string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "/")
	fields := strings.SplitN(line, " ", 2)
	name = fields[0]
	if len(fields) == 2 {
		rest = strings.TrimSpace(fields[1])
	}
	for _, c := range SlashCommands {
		if c.Name == name {
			return name, rest, true
		}
	}
	return name, rest, false
}
