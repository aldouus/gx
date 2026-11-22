package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"gx/internal/config"
	"gx/internal/mcp"
	"gx/internal/tool"
)

// attachMCP dials each configured server and adds its tools to the registry.
// A server that does not answer is reported and skipped. The caller closes
// the returned sessions after the turn.
func attachMCP(ctx context.Context, reg *tool.Registry, servers []config.MCPServer, errOut io.Writer) []*mcp.Session {
	var open []*mcp.Session
	for _, srv := range servers {
		dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		sess, err := mcp.Dial(dialCtx, mcp.Server{
			Name:    srv.Name,
			Command: srv.Command,
			Args:    srv.Args,
			URL:     srv.URL,
			Env:     srv.Env,
		})
		if err != nil {
			cancel()
			fmt.Fprintf(errOut, "mcp %s: %v\n", srv.Name, err)
			continue
		}
		listed, err := sess.List(dialCtx)
		cancel()
		if err != nil {
			_ = sess.Close()
			fmt.Fprintf(errOut, "mcp %s: %v\n", srv.Name, err)
			continue
		}
		open = append(open, sess)
		for _, item := range listed {
			name := mcpToolName(srv.Name, item.Name)
			reg.Add(tool.Remote{
				ToolName: name,
				Blurb:    item.Description,
				Input:    item.Schema,
				Call: func(ctx context.Context, args json.RawMessage) (string, error) {
					return sess.Call(ctx, item.Name, args)
				},
			})
		}
	}
	return open
}

func closeMCP(sessions []*mcp.Session) {
	for _, s := range sessions {
		_ = s.Close()
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
