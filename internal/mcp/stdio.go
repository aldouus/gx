package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	stdioGrace = 1500 * time.Millisecond
	stdioTerm  = 1500 * time.Millisecond
)

type rpcResult struct {
	result json.RawMessage
	err    error
}

type stdioLink struct {
	wmu    sync.Mutex
	w      *bufio.Writer
	wc     io.Closer
	shut   atomic.Bool
	mu     sync.Mutex
	wait   map[string]chan rpcResult
	done   chan struct{}
	once   sync.Once
	cmd    *exec.Cmd
	pid    int
	exited chan struct{}
}

func newStdioLink(r io.Reader, w io.WriteCloser, cmd *exec.Cmd) *stdioLink {
	l := &stdioLink{
		w:    bufio.NewWriter(w),
		wc:   w,
		wait: map[string]chan rpcResult{},
		done: make(chan struct{}),
		cmd:  cmd,
	}
	if cmd != nil && cmd.Process != nil {
		l.pid = cmd.Process.Pid
		l.exited = make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(l.exited)
		}()
	}
	go l.read(r)
	return l
}

func (l *stdioLink) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxMessage)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		l.handle(append([]byte(nil), line...))
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	l.fail(err)
}

func (l *stdioLink) handle(line []byte) {
	m, err := decode(line)
	if err != nil {
		return
	}
	switch m.kind() {
	case msgRequest:
		l.reply(m)
	case msgResult, msgError, msgBad:
		key, ok := responseKey(m.ID)
		if !ok {
			return
		}
		res, err := m.result()
		if m.kind() == msgBad {
			err = errors.New("mcp: invalid response")
			res = nil
		}
		l.deliver(key, rpcResult{result: res, err: err})
	}
}

func (l *stdioLink) reply(m rpcMessage) {
	var buf bytes.Buffer
	buf.WriteString(`{"jsonrpc":"2.0","id":`)
	buf.Write(bytes.TrimSpace(m.ID))
	if m.Method == "ping" {
		buf.WriteString(`,"result":{}}`)
	} else {
		buf.WriteString(`,"error":{"code":`)
		buf.WriteString(strconv.FormatInt(codeMethodNotFound, 10))
		buf.WriteString(`,"message":"Method not found"}}`)
	}
	_ = l.writeLine(buf.Bytes())
}

func (l *stdioLink) deliver(key string, res rpcResult) {
	l.mu.Lock()
	ch := l.wait[key]
	if ch != nil {
		delete(l.wait, key)
	}
	l.mu.Unlock()
	if ch != nil {
		ch <- res
	}
}

func (l *stdioLink) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, ch := range l.wait {
		delete(l.wait, key)
		ch <- rpcResult{err: err}
	}
}

func (l *stdioLink) writeLine(p []byte) error {
	if l.shut.Load() {
		return errClosed
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.shut.Load() {
		return errClosed
	}
	if _, err := l.w.Write(p); err != nil {
		return err
	}
	if err := l.w.WriteByte('\n'); err != nil {
		return err
	}
	return l.w.Flush()
}

func (l *stdioLink) roundTrip(ctx context.Context, id int64, payload []byte) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := make(chan rpcResult, 1)
	key := strconv.FormatInt(id, 10)
	l.mu.Lock()
	l.wait[key] = ch
	l.mu.Unlock()
	if err := l.writeLine(payload); err != nil {
		l.mu.Lock()
		delete(l.wait, key)
		l.mu.Unlock()
		return nil, err
	}
	select {
	case res := <-ch:
		return res.result, res.err
	case <-ctx.Done():
		l.mu.Lock()
		delete(l.wait, key)
		l.mu.Unlock()
		return nil, ctx.Err()
	case <-l.done:
		l.mu.Lock()
		delete(l.wait, key)
		l.mu.Unlock()
		return nil, errClosed
	}
}

func (l *stdioLink) close() {
	l.once.Do(func() {
		// Close the write end without taking wmu so a blocked write unblocks.
		l.shut.Store(true)
		_ = l.wc.Close()
		if l.cmd != nil {
			l.stopProcess()
		}
		close(l.done)
	})
}

func openPipe(name string, r io.Reader, w io.WriteCloser) *Session {
	return &Session{name: name, nextID: 1, stdio: newStdioLink(r, w, nil)}
}

func (l *stdioLink) stopProcess() {
	if waitFor(l.exited, stdioGrace) {
		return
	}
	signalGroup(l.pid, syscall.SIGTERM)
	if waitFor(l.exited, stdioTerm) {
		return
	}
	signalGroup(l.pid, syscall.SIGKILL)
	<-l.exited
}

func waitFor(ch <-chan struct{}, d time.Duration) bool {
	if ch == nil {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

func signalGroup(pid int, sig syscall.Signal) {
	if pid <= 1 {
		return
	}
	if syscall.Kill(-pid, sig) == nil {
		return
	}
	_ = syscall.Kill(pid, sig)
}

func startStdio(server Server) (*Session, error) {
	if server.Command == "" {
		return nil, errors.New("mcp: server command is empty")
	}
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Env = commandEnv(server.Env)
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Session{
		name:   server.Name,
		nextID: 1,
		stdio:  newStdioLink(stdout, stdin, cmd),
	}, nil
}

func commandEnv(extra []string) []string {
	if len(extra) == 0 {
		return nil
	}
	merged := make([]string, 0, len(os.Environ())+len(extra))
	index := make(map[string]int, len(os.Environ()))
	add := func(kv string) {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return
		}
		if i, ok := index[key]; ok {
			merged[i] = kv
			return
		}
		index[key] = len(merged)
		merged = append(merged, kv)
	}
	for _, kv := range os.Environ() {
		add(kv)
	}
	for _, kv := range extra {
		add(kv)
	}
	return merged
}
