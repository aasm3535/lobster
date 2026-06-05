// Package mcp is a zero-dependency client for the Model Context Protocol. It speaks
// JSON-RPC 2.0 over a server's stdio (the standard transport for local MCP servers),
// performs the initialize handshake, lists the server's tools, and forwards tool calls.
// Discovered tools are surfaced to the model exactly like Lobster's native ones, which
// lets the whole MCP ecosystem (filesystem, github, …) plug straight in.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const protocolVersion = "2025-06-18"

// Tool is one tool advertised by an MCP server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// --- JSON-RPC wire types ---

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	Result json.RawMessage
	Err    *rpcError
}

// Client is a connection to one MCP server.
type Client struct {
	name   string
	w      io.Writer
	closer func() error

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResponse
	closed  bool
}

// DialStdio starts an MCP server as a subprocess and completes the handshake.
func DialStdio(ctx context.Context, name, command string, args []string, env map[string]string) (*Client, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = io.Discard

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}

	c := newClient(name, stdout, stdin, func() error {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return cmd.Wait()
	})

	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// newClient wires a client over an arbitrary reader/writer (so tests can use a pipe).
func newClient(name string, r io.Reader, w io.Writer, closer func() error) *Client {
	c := &Client{name: name, w: w, closer: closer, pending: map[int]chan rpcResponse{}}
	go c.readLoop(bufio.NewReaderSize(r, 64*1024))
	return c
}

func (c *Client) Name() string { return c.name }

// readLoop reads newline-delimited JSON-RPC messages and routes responses to callers.
func (c *Client) readLoop(r *bufio.Reader) {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	c.failAll(fmt.Errorf("connection to %s closed", c.name))
}

func (c *Client) dispatch(line []byte) {
	var msg struct {
		ID     *int            `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if json.Unmarshal(line, &msg) != nil || msg.ID == nil || msg.Method != "" {
		return // notification, server-initiated request, or junk — we don't need it
	}
	c.mu.Lock()
	ch := c.pending[*msg.ID]
	delete(c.pending, *msg.ID)
	c.mu.Unlock()
	if ch != nil {
		ch <- rpcResponse{Result: msg.Result, Err: msg.Error}
	}
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- rpcResponse{Err: &rpcError{Message: err.Error()}}
		delete(c.pending, id)
	}
}

// call sends a request and waits for its response.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: connection closed", c.name)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.write(req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Err != nil {
			return nil, resp.Err
		}
		return resp.Result, nil
	}
}

// notify sends a notification (a request with no id, expecting no response).
func (c *Client) notify(method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	return c.write(req)
}

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(b)
	return err
}

func (c *Client) initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "lobster", "version": "0.1.0"},
	})
	if err != nil {
		return fmt.Errorf("initialize %s: %w", c.name, err)
	}
	return c.notify("notifications/initialized", nil)
}

// ListTools returns every tool the server exposes (following cursor pagination).
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// CallTool invokes a tool and returns its text content. A tool-level error (isError)
// is returned as text prefixed with [tool error] so the model can read and recover.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage("{}")
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	var parts []string
	for _, b := range res.Content {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	out := strings.Join(parts, "\n")
	if out == "" {
		out = "[no content]"
	}
	if res.IsError {
		out = "[tool error] " + out
	}
	return out, nil
}

// Close shuts the server down.
func (c *Client) Close() {
	if c.closer != nil {
		_ = c.closer()
	}
}

// --- Manager: many servers, one flat list of tools ---

// ToolHandle is one discovered MCP tool, bound to the client that serves it.
type ToolHandle struct {
	QualifiedName string // server__tool, safe for function-calling APIs
	ServerName    string
	Description   string
	Schema        map[string]any

	client  *Client
	rawName string
}

// Call invokes the tool, bounded by a generous timeout.
func (t *ToolHandle) Call(ctx context.Context, args json.RawMessage) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return t.client.CallTool(cctx, t.rawName, args)
}

// Manager owns the connected MCP clients and their tool handles.
type Manager struct {
	mu      sync.Mutex
	clients []*Client
	handles []*ToolHandle
}

func NewManager() *Manager { return &Manager{} }

// AddStdio dials a stdio server, lists its tools, and registers their handles.
func (m *Manager) AddStdio(ctx context.Context, name, command string, args []string, env map[string]string) (int, error) {
	c, err := DialStdio(ctx, name, command, args, env)
	if err != nil {
		return 0, err
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		c.Close()
		return 0, fmt.Errorf("list tools from %s: %w", name, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients = append(m.clients, c)
	for _, t := range tools {
		m.handles = append(m.handles, &ToolHandle{
			QualifiedName: qualify(name, t.Name),
			ServerName:    name,
			Description:   t.Description,
			Schema:        t.InputSchema,
			client:        c,
			rawName:       t.Name,
		})
	}
	return len(tools), nil
}

// Tools returns all registered tool handles across servers.
func (m *Manager) Tools() []*ToolHandle {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ToolHandle, len(m.handles))
	copy(out, m.handles)
	return out
}

// Close shuts down every connected server.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Close()
	}
}

// qualify builds a function-calling-safe tool name: <server>__<tool>, sanitized to
// [A-Za-z0-9_-] and capped at 64 chars.
func qualify(server, tool string) string {
	name := sanitize(server) + "__" + sanitize(tool)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}
