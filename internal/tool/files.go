package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const (
	maxReadLines = 2000
	defaultLines = 400
	maxReadBytes = 256 * 1024
	maxLineRunes = 2000
)

type readFile struct{ ws Workspace }

func ReadFile(ws Workspace) Tool { return readFile{ws: ws} }

func (readFile) Name() string { return "read_file" }
func (readFile) Description() string {
	return "Read a text file as numbered lines. For part of a large file, pass start_line and line_count."
}
func (readFile) ReadOnly() bool      { return true }
func (readFile) NeedsApproval() bool { return false }
func (readFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative to the workspace, or absolute."},"start_line":{"type":"integer","description":"1-based first line."},"line_count":{"type":"integer"}},"required":["path"],"additionalProperties":false}`)
}

func (t readFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		LineCount int    `json:"line_count"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.StartLine < 0 {
		return "", fmt.Errorf("read_file field \"start_line\" must be a positive integer")
	}
	if in.LineCount < 0 {
		return "", fmt.Errorf("read_file field \"line_count\" must be a positive integer")
	}
	if in.StartLine == 0 {
		in.StartLine = 1
	}
	if in.LineCount == 0 {
		in.LineCount = defaultLines
	}
	if in.LineCount > maxReadLines {
		in.LineCount = maxReadLines
	}
	path, err := t.ws.ReadPath(in.Path)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(body[:min(len(body), 8192)], 0) >= 0 {
		return fmt.Sprintf("<path>%s</path>\n<content>\nbinary file (%d bytes)\n</content>", path, len(body)), nil
	}
	lines := bytes.Split(body, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if in.StartLine > total {
		return fmt.Sprintf("<path>%s</path>\n<content>\n... [start_line %d is beyond end of file; total lines %d]\n</content>", path, in.StartLine, total), nil
	}
	var picked [][2]string
	width := 1
	budget := 0
	for n := in.StartLine; n <= total && len(picked) < in.LineCount; n++ {
		text := string(lines[n-1])
		if utf8.RuneCountInString(text) > maxLineRunes {
			text = string([]rune(text)[:maxLineRunes]) + "... (line truncated)"
		}
		digits := len(fmt.Sprint(n))
		if digits > width {
			width = digits
		}
		rendered := width + 1 + len(text) + 1
		if budget+rendered > maxReadBytes {
			break
		}
		picked = append(picked, [2]string{fmt.Sprint(n), text})
		budget += rendered
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "<path>%s</path>\n<content>\n", path)
	for _, line := range picked {
		fmt.Fprintf(&b, "%s", line[0])
		pad := width - len(line[0])
		for pad > 0 {
			b.WriteByte(' ')
			pad--
		}
		b.WriteByte('\t')
		b.WriteString(line[1])
		b.WriteByte('\n')
	}
	if len(picked) < total-in.StartLine+1 {
		fmt.Fprintf(&b, "... [showing %d of %d lines; use start_line/line_count to read more.]\n", len(picked), total)
	}
	b.WriteString("</content>")
	return b.String(), nil
}

type writeFile struct{ ws Workspace }

func WriteFile(ws Workspace) Tool { return writeFile{ws: ws} }

func (writeFile) Name() string { return "write_file" }
func (writeFile) Description() string {
	return "Create a file, or overwrite one, with the complete content given."
}
func (writeFile) ReadOnly() bool      { return false }
func (writeFile) NeedsApproval() bool { return true }
func (writeFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative to the workspace, or absolute."},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)
}

func (t writeFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	path, err := t.ws.WritePath(in.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	return "wrote " + path, nil
}

type editFile struct{ ws Workspace }

func EditFile(ws Workspace) Tool { return editFile{ws: ws} }

func (editFile) Name() string { return "edit_file" }
func (editFile) Description() string {
	return "Replace old_string with new_string in an existing file. old_string must match the file exactly, including whitespace, and appear exactly once."
}
func (editFile) ReadOnly() bool      { return false }
func (editFile) NeedsApproval() bool { return true }
func (editFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative to the workspace, or absolute."},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["path","old_string","new_string"],"additionalProperties":false}`)
}

func (t editFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.OldString == in.NewString {
		return "", fmt.Errorf("edit_file old_string and new_string are the same")
	}
	path, err := t.ws.WritePath(in.Path)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(body)
	count := bytes.Count(body, []byte(in.OldString))
	if count == 0 {
		return "", fmt.Errorf("edit_file old_string not found in %s", path)
	}
	if count != 1 {
		return "", fmt.Errorf("edit_file old_string appears %d times in %s", count, path)
	}
	next := stringsReplaceOnce(text, in.OldString, in.NewString)
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return "", err
	}
	return "edited " + path, nil
}

func stringsReplaceOnce(s, old, new string) string {
	i := indexOf(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

func indexOf(s, sub string) int {
	return len(s[:]) - len(s[:]) + find(s, sub)
}

func find(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
