// Package session is an append-only JSONL log.
//
// One writer holds the lock. A committed turn survives a crash. An unfinished
// turn is not continued: the next open appends turn_interrupted and leaves
// the partial text in the log. The line format is this port's own. It is not
// the Zig session codec.
package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const version = 1

// ErrBusy is a second writer on a session that already has one.
var ErrBusy = errors.New("session is busy")

// ErrCorrupt is a log line that is not the torn tail.
var ErrCorrupt = errors.New("session log is corrupt")

// Event is one log line. Data is the JSON object stored between kind and crc.
type Event struct {
	Seq  int             `json:"seq"`
	Time time.Time       `json:"-"`
	TS   int64           `json:"ts"`
	Kind string          `json:"kind"`
	Turn int             `json:"turn,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Session is the single writer for one conversation.
type Session struct {
	id      string
	dir     string
	root    string
	file    *os.File
	lock    *os.File
	mu      sync.Mutex
	nextSeq int
	turn    int
	open    bool
	events  []Event
}

// Create starts a session under root/sessions/<id>.
func Create(root, workspace string) (*Session, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	s, err := open(root, id, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{"workspace": workspace})
	if err != nil {
		s.Close()
		return nil, err
	}
	if err := s.append("session_created", 0, body, true); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Open resumes a session. An unfinished turn receives turn_interrupted.
func Open(root, id string) (*Session, error) {
	return open(root, id, false)
}

func open(root, id string, create bool) (*Session, error) {
	dir := filepath.Join(root, "sessions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("session path is a symlink")
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, ErrBusy
	}
	path := filepath.Join(dir, "log.jsonl")
	flag := os.O_RDWR | os.O_CREATE
	file, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		return nil, err
	}
	s := &Session{id: id, dir: dir, root: root, file: file, lock: lock, nextSeq: 1}
	if err := s.replay(create); err != nil {
		s.Close()
		return nil, err
	}
	if s.open {
		if err := s.append("turn_interrupted", s.turn, nil, true); err != nil {
			s.Close()
			return nil, err
		}
		s.open = false
	}
	return s, nil
}

func (s *Session) ID() string { return s.id }

func (s *Session) replay(create bool) error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	body, err := io.ReadAll(s.file)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	torn := false
	if len(body) > 0 && body[len(body)-1] != '\n' {
		if i := bytes.LastIndexByte(body, '\n'); i >= 0 {
			body = body[:i+1]
		} else {
			body = nil
		}
		torn = true
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var lines [][]byte
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		return err
	}
	var good [][]byte
	for i, line := range lines {
		ev, err := parseLine(line)
		if err != nil {
			if i == len(lines)-1 {
				torn = true
				break
			}
			return fmt.Errorf("%w: line %d", ErrCorrupt, i+1)
		}
		if ev.Seq != len(good)+1 {
			return fmt.Errorf("%w: seq %d", ErrCorrupt, ev.Seq)
		}
		good = append(good, line)
		s.fold(ev)
	}
	if torn {
		var buf bytes.Buffer
		for _, line := range good {
			buf.Write(line)
			buf.WriteByte('\n')
		}
		if err := s.file.Truncate(int64(buf.Len())); err != nil {
			return err
		}
		if _, err := s.file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if _, err := s.file.Write(buf.Bytes()); err != nil {
			return err
		}
	}
	if _, err := s.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if create && len(s.events) > 0 {
		return fmt.Errorf("session %s already exists", s.id)
	}
	return nil
}

func (s *Session) fold(ev Event) {
	s.events = append(s.events, ev)
	s.nextSeq = ev.Seq + 1
	switch ev.Kind {
	case "user_message":
		s.turn = ev.Turn
		s.open = true
	case "turn_committed", "turn_interrupted":
		s.turn = ev.Turn
		s.open = false
	}
}

// User appends a user message and opens a turn.
func (s *Session) User(text string) (int, error) {
	if s.open {
		return 0, fmt.Errorf("turn %d is still open", s.turn)
	}
	turn := s.turn + 1
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return 0, err
	}
	if err := s.append("user_message", turn, body, false); err != nil {
		return 0, err
	}
	return turn, nil
}

// Steer appends a user message on the open turn. A subagent report uses this
// so the parent can keep the turn and still see the report.
func (s *Session) Steer(text string) error {
	if !s.open {
		return fmt.Errorf("no open turn")
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	return s.append("user_message", s.turn, body, false)
}

// Assistant appends streamed assistant text for the open turn.
func (s *Session) Assistant(text string) error {
	if !s.open {
		return fmt.Errorf("no open turn")
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	return s.append("assistant_delta", s.turn, body, false)
}

// ToolCall appends a tool call on the open turn.
func (s *Session) ToolCall(id, name, args string) error {
	if !s.open {
		return fmt.Errorf("no open turn")
	}
	body, err := json.Marshal(map[string]string{"id": id, "name": name, "arguments": args})
	if err != nil {
		return err
	}
	return s.append("tool_call", s.turn, body, false)
}

// ToolResult appends a tool result on the open turn.
func (s *Session) ToolResult(id, name, text string) error {
	if !s.open {
		return fmt.Errorf("no open turn")
	}
	body, err := json.Marshal(map[string]string{"id": id, "name": name, "text": text})
	if err != nil {
		return err
	}
	return s.append("tool_result", s.turn, body, false)
}

// Interrupt closes an unfinished turn. The partial text stays in the log
// and the next launch does not continue the model call.
func (s *Session) Interrupt() error {
	if !s.open {
		return nil
	}
	if err := s.append("turn_interrupted", s.turn, nil, true); err != nil {
		return err
	}
	s.open = false
	return nil
}

// Commit closes the open turn. The committed turn survives a later crash.
func (s *Session) Commit() error {
	if !s.open {
		return fmt.Errorf("no open turn")
	}
	if err := s.append("turn_committed", s.turn, nil, true); err != nil {
		return err
	}
	s.open = false
	return nil
}

// Compaction records a summary and does not by itself close a turn.
func (s *Session) Compaction(summary string, kept int) error {
	body, err := json.Marshal(map[string]any{"summary": summary, "kept": kept})
	if err != nil {
		return err
	}
	return s.append("compaction", s.turn, body, true)
}

func (s *Session) append(kind string, turn int, data json.RawMessage, sync bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := Event{
		Seq:  s.nextSeq,
		TS:   time.Now().UnixMilli(),
		Kind: kind,
		Turn: turn,
		Data: data,
	}
	line, err := encodeLine(ev)
	if err != nil {
		return err
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if sync {
		if err := s.file.Sync(); err != nil {
			return err
		}
	}
	ev.Time = time.UnixMilli(ev.TS)
	s.fold(ev)
	return nil
}

// Events is the log, including a crash's turn_interrupted.
func (s *Session) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

// Message is one model-facing turn piece. An interrupted turn is closed
// so the next launch does not continue it.
type Message struct {
	Role      string
	Text      string
	ToolCalls []ToolCall
	ToolID    string
	ToolName  string
}

// ToolCall is a function call the model already emitted.
type ToolCall struct {
	ID, Name, Args string
}

// History folds the log into model messages. The unfinished turn that was
// repaired stays visible and is not left open.
func (s *Session) History() []Message {
	var msgs []Message
	var cur *Message
	flush := func() {
		if cur == nil {
			return
		}
		msgs = append(msgs, *cur)
		cur = nil
	}
	for _, ev := range s.Events() {
		switch ev.Kind {
		case "user_message":
			flush()
			cur = &Message{Role: "user", Text: textField(ev.Data, "text")}
		case "assistant_delta":
			if cur == nil || cur.Role != "assistant" {
				flush()
				cur = &Message{Role: "assistant"}
			}
			cur.Text += textField(ev.Data, "text")
		case "tool_call":
			if cur == nil || cur.Role != "assistant" {
				flush()
				cur = &Message{Role: "assistant"}
			}
			cur.ToolCalls = append(cur.ToolCalls, ToolCall{
				ID:   textField(ev.Data, "id"),
				Name: textField(ev.Data, "name"),
				Args: textField(ev.Data, "arguments"),
			})
		case "tool_result":
			flush()
			cur = &Message{
				Role:     "tool",
				Text:     textField(ev.Data, "text"),
				ToolID:   textField(ev.Data, "id"),
				ToolName: textField(ev.Data, "name"),
			}
			flush()
		case "turn_interrupted":
			if cur != nil && cur.Role == "assistant" && cur.Text != "" {
				cur.Text += "\n[interrupted]"
			}
			flush()
		case "turn_committed":
			flush()
		case "compaction":
			flush()
			msgs = append(msgs, Message{Role: "user", Text: "Conversation summary:\n" + textField(ev.Data, "summary")})
		}
	}
	flush()
	return msgs
}

func textField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// TranscriptText is the text a person should see again on resume.
func (s *Session) TranscriptText() string {
	var b bytes.Buffer
	for _, m := range s.History() {
		switch m.Role {
		case "user":
			fmt.Fprintf(&b, "user: %s\n", m.Text)
		case "assistant":
			fmt.Fprintf(&b, "assistant: %s\n", m.Text)
		case "tool":
			fmt.Fprintf(&b, "tool %s: %s\n", m.ToolName, m.Text)
		}
	}
	return b.String()
}

// Close releases the writer lock. An open turn is interrupted first so a
// clean exit and a crash obey the same rule.
func (s *Session) Close() error {
	if s.file == nil {
		return nil
	}
	if s.open {
		if err := s.append("turn_interrupted", s.turn, nil, true); err != nil {
			return err
		}
		s.open = false
	}
	err := s.file.Close()
	s.file = nil
	if s.lock != nil {
		syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.lock.Close()
		s.lock = nil
	}
	return err
}

// Abandon drops the lock without closing the turn. Tests use it to simulate
// a crash. A person running fx does not.
func (s *Session) Abandon() error {
	s.open = false
	var err error
	if s.file != nil {
		err = s.file.Close()
		s.file = nil
	}
	if s.lock != nil {
		syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.lock.Close()
		s.lock = nil
	}
	return err
}

func encodeLine(ev Event) ([]byte, error) {
	payload := struct {
		V    int             `json:"v"`
		Seq  int             `json:"seq"`
		TS   int64           `json:"ts"`
		Kind string          `json:"kind"`
		Turn int             `json:"turn,omitempty"`
		Data json.RawMessage `json:"data,omitempty"`
	}{V: version, Seq: ev.Seq, TS: ev.TS, Kind: ev.Kind, Turn: ev.Turn, Data: ev.Data}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || b[len(b)-1] != '}' {
		return nil, fmt.Errorf("encoded event")
	}
	prefix := b[:len(b)-1]
	sum := crc32.Checksum(prefix, crc32.MakeTable(crc32.Castagnoli))
	var buf bytes.Buffer
	buf.Write(prefix)
	fmt.Fprintf(&buf, `,"crc":"%08x"}`, sum)
	return buf.Bytes(), nil
}

func parseLine(line []byte) (Event, error) {
	const mark = `,"crc":"`
	i := bytes.LastIndex(line, []byte(mark))
	if i < 0 || !bytes.HasSuffix(line, []byte("}")) {
		return Event{}, fmt.Errorf("shape")
	}
	prefix := line[:i]
	rest := line[i+len(mark):]
	if len(rest) < 8+2 || rest[8] != '"' || rest[9] != '}' {
		return Event{}, fmt.Errorf("crc field")
	}
	var got uint32
	if _, err := fmt.Sscanf(string(rest[:8]), "%08x", &got); err != nil {
		return Event{}, err
	}
	want := crc32.Checksum(prefix, crc32.MakeTable(crc32.Castagnoli))
	if got != want {
		return Event{}, fmt.Errorf("crc")
	}
	var payload struct {
		V    int             `json:"v"`
		Seq  int             `json:"seq"`
		TS   int64           `json:"ts"`
		Kind string          `json:"kind"`
		Turn int             `json:"turn"`
		Data json.RawMessage `json:"data"`
	}
	full := append(append([]byte{}, prefix...), '}')
	if err := json.Unmarshal(full, &payload); err != nil {
		return Event{}, err
	}
	if payload.V != version || payload.Seq < 1 || payload.Kind == "" {
		return Event{}, fmt.Errorf("envelope")
	}
	return Event{Seq: payload.Seq, TS: payload.TS, Time: time.UnixMilli(payload.TS), Kind: payload.Kind, Turn: payload.Turn, Data: payload.Data}, nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// List returns session ids under root, newest directory first.
func List(root string) ([]string, error) {
	dir := filepath.Join(root, "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type item struct {
		id  string
		mod time.Time
	}
	var items []item
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{id: e.Name(), mod: info.ModTime()})
	}
	for i := 1; i < len(items); i++ {
		j := i
		for j > 0 && items[j].mod.After(items[j-1].mod) {
			items[j], items[j-1] = items[j-1], items[j]
			j--
		}
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out, nil
}

// Remember stores the interactive session id for fx -c.
func Remember(root, id string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "last_interactive"), []byte(id+"\n"), 0o600)
}

// Remembered reads the id fx -c resumes.
func Remembered(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "last_interactive"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(b)), nil
}
