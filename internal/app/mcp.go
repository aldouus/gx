package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"gx/internal/config"
	"gx/internal/mcp"
	"gx/internal/tool"
)

// mcpPool dials a server the first time one of its tools is called and keeps
// that session until the turn ends.
type mcpPool struct {
	mu    sync.Mutex
	slots map[string]*mcpSlot
	open  []*mcp.Session
	reg   *tool.Registry
}

type mcpSlot struct {
	once sync.Once
	sess *mcp.Session
	err  error
}

// attachMCP registers one tool per server. Nothing is dialed here.
func attachMCP(_ context.Context, reg *tool.Registry, servers []config.MCPServer, _ io.Writer) *mcpPool {
	p := &mcpPool{slots: map[string]*mcpSlot{}, reg: reg}
	for _, srv := range servers {
		if srv.Name == "" || srv.Rejected {
			continue
		}
		if srv.Token != "" {
			srv.Env = append(append([]string{}, srv.Env...), "MCP_TOKEN="+srv.Token)
		}
		reg.Add(lazyMCP{pool: p, server: srv})
	}
	return p
}

func closeMCP(p *mcpPool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	open := p.open
	p.open = nil
	p.mu.Unlock()
	for _, s := range open {
		_ = s.Close()
	}
}

type lazyMCP struct {
	pool   *mcpPool
	server config.MCPServer
}

func (t lazyMCP) Name() string { return "mcp_" + ident(t.server.Name) }
func (t lazyMCP) Description() string {
	where := t.server.URL
	if where == "" {
		where = t.server.Command
	}
	return "Call a tool on MCP server " + t.server.Name + " (" + where + "). The server connects on the first call. Pass tool and arguments."
}
func (lazyMCP) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"tool":{"type":"string","description":"Tool name on the server"},"arguments":{"type":"object"}},"required":["tool"],"additionalProperties":false}`)
}
func (lazyMCP) ReadOnly() bool      { return false }
func (lazyMCP) NeedsApproval() bool { return true }

func (t lazyMCP) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Tool) == "" {
		return "", fmt.Errorf("mcp %s: missing tool", t.server.Name)
	}
	return t.pool.call(ctx, t.server, in.Tool, in.Args)
}

func (p *mcpPool) call(ctx context.Context, server config.MCPServer, name string, args json.RawMessage) (string, error) {
	sess, err := p.session(ctx, server)
	if err != nil {
		return "", err
	}
	return sess.Call(ctx, name, args)
}

func (p *mcpPool) session(ctx context.Context, server config.MCPServer) (*mcp.Session, error) {
	p.mu.Lock()
	sl := p.slots[server.Name]
	if sl == nil {
		sl = &mcpSlot{}
		p.slots[server.Name] = sl
	}
	p.mu.Unlock()
	sl.once.Do(func() {
		dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		sess, err := mcp.Dial(dialCtx, mcp.Server{
			Name:    server.Name,
			Command: server.Command,
			Args:    server.Args,
			URL:     server.URL,
			Env:     server.Env,
		})
		if err != nil {
			sl.err = fmt.Errorf("mcp %s: %w", server.Name, err)
			return
		}
		listed, err := sess.List(dialCtx)
		if err != nil {
			_ = sess.Close()
			sl.err = fmt.Errorf("mcp %s: %w", server.Name, err)
			return
		}
		sl.sess = sess
		p.mu.Lock()
		p.open = append(p.open, sess)
		p.mu.Unlock()
		p.advertise(server, listed, sess)
	})
	if sl.err != nil {
		return nil, sl.err
	}
	if sl.sess == nil {
		return nil, fmt.Errorf("mcp %s: not connected", server.Name)
	}
	return sl.sess, nil
}

func (p *mcpPool) advertise(server config.MCPServer, listed []mcp.Tool, sess *mcp.Session) {
	if p.reg == nil {
		return
	}
	for _, item := range listed {
		item := item
		name := mcpToolName(server.Name, item.Name)
		p.reg.Add(tool.Remote{
			ToolName: name,
			Blurb:    item.Description,
			Input:    item.Schema,
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				return sess.Call(ctx, item.Name, args)
			},
		})
	}
}

// mcpToolName is the advertised name: mcp_{server}_{tool}, with other
// characters folded to underscores so the model can call it.
func mcpToolName(server, name string) string {
	return "mcp_" + ident(server) + "_" + ident(name)
}

func ident(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else if r == '-' || r == ' ' || r == '.' {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}
