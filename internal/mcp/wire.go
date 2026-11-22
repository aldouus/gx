package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// protocolVersion is the version string initialize sends. The Zig client
// offers era.Config.legacy[0], "2025-11-25". It also accepts the older
// legacy versions a server may answer with.
const protocolVersion = "2025-11-25"

const (
	maxRequestID = (1 << 53) - 1
	maxPages     = 64
	maxMessage   = 8 << 20
)

func acceptedVersion(v string) bool {
	switch v {
	case "2025-11-25", "2025-06-18", "2025-03-26":
		return true
	default:
		return false
	}
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type progressMeta struct {
	ProgressToken int64 `json:"progressToken"`
}

type initParams struct {
	Meta            progressMeta    `json:"_meta"`
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ClientInfo      clientInfo      `json:"clientInfo"`
}

type listParams struct {
	Meta   progressMeta `json:"_meta"`
	Cursor *string      `json:"cursor,omitempty"`
}

type callParams struct {
	Meta      progressMeta    `json:"_meta"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e.Message == "" {
		return "mcp: error " + strconv.FormatInt(e.Code, 10)
	}
	return "mcp: " + e.Message + " (" + strconv.FormatInt(e.Code, 10) + ")"
}

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Method  string           `json:"method"`
	Result  *json.RawMessage `json:"result"`
	Err     *rpcError        `json:"error"`
	Params  json.RawMessage  `json:"params"`
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func encodeRequest(id int64, method string, params any) ([]byte, error) {
	raw, err := marshal(params)
	if err != nil {
		return nil, err
	}
	return marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: raw})
}

func encodeNotification(method string) ([]byte, error) {
	return marshal(rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  json.RawMessage(`{}`),
	})
}

func decode(line []byte) (rpcMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(line)))
	var m rpcMessage
	if err := dec.Decode(&m); err != nil {
		return rpcMessage{}, err
	}
	if dec.More() {
		return rpcMessage{}, errors.New("mcp: trailing json")
	}
	if m.JSONRPC != "2.0" {
		return rpcMessage{}, errors.New("mcp: not json-rpc 2.0")
	}
	return m, nil
}

func (m rpcMessage) idPresent() bool {
	raw := bytes.TrimSpace(m.ID)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

type msgKind int

const (
	msgBad msgKind = iota
	msgRequest
	msgNote
	msgResult
	msgError
)

func (m rpcMessage) kind() msgKind {
	hasMethod := m.Method != ""
	hasResult := m.Result != nil
	hasErr := m.Err != nil
	if hasMethod && !hasResult && !hasErr {
		if m.idPresent() {
			return msgRequest
		}
		return msgNote
	}
	if !hasMethod && hasErr != hasResult {
		if hasErr {
			return msgError
		}
		return msgResult
	}
	return msgBad
}

func (m rpcMessage) result() (json.RawMessage, error) {
	if m.Err != nil && m.Result != nil {
		return nil, errors.New("mcp: invalid response")
	}
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Result == nil {
		return nil, errors.New("mcp: invalid response")
	}
	return append(json.RawMessage(nil), (*m.Result)...), nil
}

func responseKey(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var n int64
	var err error
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		n, err = strconv.ParseInt(s, 10, 64)
	} else {
		n, err = strconv.ParseInt(string(raw), 10, 64)
	}
	if err != nil || n <= 0 {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}

func idMatch(raw json.RawMessage, id int64) bool {
	key, ok := responseKey(raw)
	return ok && key == strconv.FormatInt(id, 10)
}

func protocolOf(result json.RawMessage) (string, error) {
	var obj struct {
		ProtocolVersion *string `json:"protocolVersion"`
	}
	if json.Unmarshal(result, &obj) != nil || obj.ProtocolVersion == nil {
		return "", errors.New("mcp: initialize result has no protocol version")
	}
	return *obj.ProtocolVersion, nil
}

func validSessionID(v string) bool {
	if len(v) == 0 || len(v) > 1024 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x21 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

func isJSONType(ct string) bool {
	return mediaType(ct) == "application/json"
}

func isEventStream(ct string) bool {
	return mediaType(ct) == "text/event-stream"
}

func mediaType(ct string) string {
	media, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(media))
}

func compactObject(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("mcp: arguments are not json")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, errors.New("mcp: arguments are not json")
	}
	out := buf.Bytes()
	if len(out) == 0 || out[0] != '{' {
		return nil, errors.New("mcp: arguments must be a json object")
	}
	return append(json.RawMessage(nil), out...), nil
}

func isObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return false
	}
	return true
}

type listedTool struct {
	name, description string
	schema            json.RawMessage
}

func parseTool(raw json.RawMessage) (listedTool, bool) {
	var obj struct {
		Name        json.RawMessage `json:"name"`
		Description json.RawMessage `json:"description"`
		Schema      json.RawMessage `json:"inputSchema"`
	}
	if json.Unmarshal(raw, &obj) != nil || len(obj.Name) == 0 || len(obj.Schema) == 0 {
		return listedTool{}, false
	}
	var name string
	if json.Unmarshal(obj.Name, &name) != nil || name == "" || !isObject(obj.Schema) {
		return listedTool{}, false
	}
	var desc string
	if len(bytes.TrimSpace(obj.Description)) > 0 {
		if json.Unmarshal(obj.Description, &desc) != nil {
			return listedTool{}, false
		}
	}
	return listedTool{name: name, description: desc, schema: append(json.RawMessage(nil), obj.Schema...)}, true
}

func parsePage(result json.RawMessage) ([]json.RawMessage, *string, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(result, &obj) != nil {
		return nil, nil, errors.New("mcp: invalid tools/list result")
	}
	toolsRaw, ok := obj["tools"]
	if !ok || len(bytes.TrimSpace(toolsRaw)) == 0 || bytes.TrimSpace(toolsRaw)[0] != '[' {
		return nil, nil, errors.New("mcp: invalid tools/list result")
	}
	var tools []json.RawMessage
	if json.Unmarshal(toolsRaw, &tools) != nil {
		return nil, nil, errors.New("mcp: invalid tools/list result")
	}
	cursorRaw, ok := obj["nextCursor"]
	if !ok || bytes.Equal(bytes.TrimSpace(cursorRaw), []byte("null")) {
		return tools, nil, nil
	}
	var cursor string
	if json.Unmarshal(cursorRaw, &cursor) != nil {
		return nil, nil, errors.New("mcp: invalid tools/list cursor")
	}
	return tools, &cursor, nil
}

const (
	codeMethodNotFound = -32601
	acceptPost         = "application/json, text/event-stream"
)
