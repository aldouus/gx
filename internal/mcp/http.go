package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const httpDeleteWait = 2 * time.Second

type httpLink struct {
	url    string
	client *http.Client
}

func startHTTP(server Server) (*Session, error) {
	u, err := url.Parse(server.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("mcp: invalid url")
	}
	return &Session{
		name:   server.Name,
		nextID: 1,
		http: &httpLink{
			url: server.URL,
			client: &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return errors.New("mcp: redirect")
				},
			},
		},
	}, nil
}

func (s *Session) post(ctx context.Context, method string, id int64, payload []byte, notify bool) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.http.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	s.decorate(req, true)
	resp, err := s.http.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	defer resp.Body.Close()
	if method == "initialize" && resp.StatusCode/100 == 2 {
		if raw := resp.Header.Get("Mcp-Session-Id"); raw != "" {
			if !validSessionID(raw) {
				return nil, errors.New("mcp: invalid session id")
			}
			s.mu.Lock()
			s.sessionID = raw
			s.mu.Unlock()
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxMessage {
		return nil, errors.New("mcp: response too large")
	}
	if notify {
		if resp.StatusCode/100 == 2 {
			return nil, nil
		}
		return nil, httpStatus(resp.StatusCode)
	}
	if resp.StatusCode == http.StatusAccepted {
		return nil, errors.New("mcp: http 202 without a result")
	}
	ct := resp.Header.Get("Content-Type")
	if isEventStream(ct) {
		if resp.StatusCode/100 != 2 {
			return nil, httpStatus(resp.StatusCode)
		}
		return readSSE(bytes.NewReader(body), id)
	}
	if isJSONType(ct) {
		out, err := finishJSON(body, id)
		if resp.StatusCode/100 != 2 {
			if err != nil {
				return nil, err
			}
			return nil, httpStatus(resp.StatusCode)
		}
		return out, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, httpStatus(resp.StatusCode)
	}
	return nil, errors.New("mcp: response is not json")
}

func (s *Session) decorate(req *http.Request, hasBody bool) {
	s.mu.Lock()
	version, sid := s.version, s.sessionID
	s.mu.Unlock()
	req.Header.Set("Accept", acceptPost)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
}

func httpStatus(code int) error {
	return errors.New("mcp: http " + strconv.Itoa(code))
}

func finishJSON(body []byte, id int64) (json.RawMessage, error) {
	m, err := decode(body)
	if err != nil {
		return nil, errors.New("mcp: invalid response")
	}
	if !idMatch(m.ID, id) {
		return nil, errors.New("mcp: response id mismatch")
	}
	return m.result()
}

func readSSE(r io.Reader, id int64) (json.RawMessage, error) {
	br := bufio.NewReader(r)
	var data bytes.Buffer
	has := false
	dispatch := func() (json.RawMessage, bool, error) {
		if !has {
			return nil, false, nil
		}
		raw := append([]byte(nil), data.Bytes()...)
		data.Reset()
		has = false
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, false, nil
		}
		m, err := decode(raw)
		if err != nil {
			return nil, true, errors.New("mcp: invalid response")
		}
		if m.Method != "" && m.Result == nil && m.Err == nil {
			return nil, false, nil
		}
		if !idMatch(m.ID, id) {
			return nil, false, nil
		}
		out, err := m.result()
		return out, true, err
	}
	for {
		line, err := br.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(line) > 0 && len(trimmed) == 0 {
			if out, ok, derr := dispatch(); ok || derr != nil {
				return out, derr
			}
		} else if len(trimmed) > 0 && trimmed[0] != ':' {
			field, value, found := bytes.Cut(trimmed, []byte(":"))
			if bytes.Equal(field, []byte("data")) {
				if found && len(value) > 0 && value[0] == ' ' {
					value = value[1:]
				}
				if has {
					data.WriteByte('\n')
				}
				data.Write(value)
				has = true
			}
		}
		if err == io.EOF {
			if out, ok, derr := dispatch(); ok || derr != nil {
				return out, derr
			}
			return nil, io.ErrUnexpectedEOF
		}
		if err != nil {
			return nil, err
		}
	}
}

func (h *httpLink) close(version, sessionID string) error {
	defer h.client.CloseIdleConnections()
	if sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpDeleteWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", acceptPost)
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
