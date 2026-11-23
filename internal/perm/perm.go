// Package perm decides whether a tool call runs, is asked, or is reviewed.
// The three modes are ask, auto, and full-access.
package perm

// Mode is the permission mode a person selects.
type Mode string

const (
	Ask        Mode = "ask"
	Auto       Mode = "auto"
	FullAccess Mode = "full-access"
)

// Default is the mode gx uses when nothing is saved.
const Default = Auto

// Decision is what the runner does before a tool call.
type Decision string

const (
	Allow  Decision = "allow"
	Prompt Decision = "ask"
	Review Decision = "review"
)

// Spec is the part of a tool the decision needs.
type Spec struct {
	Name          string
	NeedsApproval bool
	Routine       bool
	WorkspaceEdit bool
	ShellCommand  string
}

// Parse accepts the spellings the CLI and slash command accept.
func Parse(s string) (Mode, bool) {
	switch Mode(s) {
	case Ask, Auto, FullAccess:
		return Mode(s), true
	case "yolo":
		return FullAccess, true
	default:
		return "", false
	}
}

// Decide is the mode table.
//
//	full-access  everything runs
//	ask          approval tools prompt
//	auto         routine workspace edits and test commands run; other
//	             approval tools go to review, which holds them for a
//	             reviewer and then for the person
func Decide(mode Mode, spec Spec) Decision {
	switch mode {
	case FullAccess:
		return Allow
	case Ask:
		if spec.NeedsApproval {
			return Prompt
		}
		return Allow
	case Auto:
		if !spec.NeedsApproval || spec.Routine {
			return Allow
		}
		return Review
	default:
		if spec.NeedsApproval {
			return Prompt
		}
		return Allow
	}
}

// Routine reports whether auto mode may run this call without a reviewer.
// Workspace edits are routine. A shell command is routine when it is a
// test or build the person would expect an agent to run.
func Routine(spec Spec) bool {
	if spec.WorkspaceEdit && (spec.Name == "edit_file" || spec.Name == "write_file") {
		return true
	}
	if spec.Name != "shell" {
		return false
	}
	return routineCommand(spec.ShellCommand)
}

func routineCommand(command string) bool {
	for _, prefix := range []string{
		"go test",
		"go fmt",
		"gofmt",
		"zig build test",
		"npm test",
		"pnpm test",
		"yarn test",
		"bun test",
		"pytest",
		"cargo test",
		"make test",
	} {
		if command == prefix || hasPrefixWord(command, prefix) {
			return true
		}
	}
	return false
}

func hasPrefixWord(command, prefix string) bool {
	if len(command) <= len(prefix) || command[:len(prefix)] != prefix {
		return false
	}
	switch command[len(prefix)] {
	case ' ', '\t':
		return true
	default:
		return false
	}
}

// Modes is the catalog /permissions prints, in the order gx lists them.
var Modes = []struct {
	ID, Name, Description string
}{
	{"auto", "Auto", "Check actions with an automatic safety review instead of asking"},
	{"ask", "Ask", "Request permission before making any changes"},
	{"full-access", "Full access", "Run every action without gx permission checks"},
}
