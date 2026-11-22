// Package mcp is a client for one MCP server over stdio or HTTP.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
)

var errClosed = errors.New("mcp: session closed")

// Server is one MCP server. URL selects HTTP; otherwise Command is a stdio process.
type Server struct {
	Name    string
	Command string
	Args    []string
	URL     string // if set, HTTP; else stdio Command
	Env     []string
}

// Tool is one tool the server listed. Name is the server's own name.
type Tool struct {
	Server, Name, Description string
	Schema                    json.RawMessage
}

// Session is one initialized MCP connection.
type Session struct {
	name      string
	version   string
	sessionID string

	mu     sync.Mutex
	nextID int64
	closed bool

	stdio *stdioLink
	http  *httpLink
}

// Dial starts server and finishes initialize plus notifications/initialized.
func Dial(ctx context.Context, server Server) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		s   *Session
		err error
	)
	if server.URL != "" {
		s, err = startHTTP(server)
	} else {
		s, err = startStdio(server)
	}
	if err != nil {
		return nil, err
	}
	if err := s.handshake(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) handshake(ctx context.Context) error {
	raw, err := s.roundTrip(ctx, "initialize", initParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo:      clientInfo{Name: "gx", Version: "0.0.0"},
	}, false)
	if err != nil {
		return err
	}
	version, err := protocolOf(raw)
	if err != nil {
		return err
	}
	if !acceptedVersion(version) {
		return errors.New("mcp: unsupported protocol version " + strconv.Quote(version))
	}
	s.mu.Lock()
	s.version = version
	s.mu.Unlock()
	_, err = s.roundTrip(ctx, "notifications/initialized", nil, true)
	return err
}

// List fetches every tools/list page. Schema is the tool's inputSchema.
func (s *Session) List(ctx context.Context) ([]Tool, error) {
	var (
		cursor *string
		seen   map[string]struct{}
		names  map[string]struct{}
		tools  []Tool
	)
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cursor != nil {
			if seen == nil {
				seen = map[string]struct{}{}
			}
			if _, ok := seen[*cursor]; ok {
				return nil, errors.New("mcp: duplicate tools/list cursor")
			}
			seen[*cursor] = struct{}{}
		}
		raw, err := s.roundTrip(ctx, "tools/list", listParams{Cursor: cursor}, false)
		if err != nil {
			return nil, err
		}
		batch, next, err := parsePage(raw)
		if err != nil {
			return nil, err
		}
		for _, item := range batch {
			tool, ok := parseTool(item)
			if !ok {
				continue
			}
			if names == nil {
				names = map[string]struct{}{}
			}
			if _, dup := names[tool.name]; dup {
				continue
			}
			names[tool.name] = struct{}{}
			tools = append(tools, Tool{
				Server:      s.name,
				Name:        tool.name,
				Description: tool.description,
				Schema:      tool.schema,
			})
		}
		if next == nil {
			if tools == nil {
				tools = []Tool{}
			}
			return tools, nil
		}
		cursor = next
	}
	return nil, errors.New("mcp: too many tools/list pages")
}

// Call runs tools/call and returns the server's result object as JSON.
// A tool-level isError stays in that JSON. A JSON-RPC error is a Go error.
func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	obj, err := compactObject(args)
	if err != nil {
		return "", err
	}
	raw, err := s.roundTrip(ctx, "tools/call", callParams{Name: name, Arguments: obj}, false)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Close releases the session. Stdio closes stdin, then SIGTERM, then SIGKILL.
// HTTP DELETEs the session when the server assigned one.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	version, sid := s.version, s.sessionID
	s.mu.Unlock()
	if s.stdio != nil {
		s.stdio.close()
		return nil
	}
	if s.http != nil {
		return s.http.close(version, sid)
	}
	return nil
}

func (s *Session) allocID() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errClosed
	}
	id := s.nextID
	if id <= 0 || id > maxRequestID {
		return 0, errors.New("mcp: request id exhausted")
	}
	s.nextID++
	return id, nil
}

func (s *Session) roundTrip(ctx context.Context, method string, params any, notify bool) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if notify {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil, errClosed
		}
		payload, err := encodeNotification(method)
		if err != nil {
			return nil, err
		}
		if s.stdio != nil {
			return nil, s.stdio.writeLine(payload)
		}
		return s.post(ctx, method, 0, payload, true)
	}
	id, err := s.allocID()
	if err != nil {
		return nil, err
	}
	payload, err := encodeRequest(id, method, withToken(params, id))
	if err != nil {
		return nil, err
	}
	if s.stdio != nil {
		return s.stdio.roundTrip(ctx, id, payload)
	}
	return s.post(ctx, method, id, payload, false)
}

func withToken(params any, id int64) any {
	switch p := params.(type) {
	case initParams:
		p.Meta.ProgressToken = id
		return p
	case listParams:
		p.Meta.ProgressToken = id
		return p
	case callParams:
		p.Meta.ProgressToken = id
		return p
	default:
		return params
	}
}
