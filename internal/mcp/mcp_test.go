package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// mockServer speaks just enough MCP over conn to satisfy the client: initialize,
// tools/list (one "echo" tool), and tools/call (echoes the arguments back).
func mockServer(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var req struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(line, &req)
			if req.ID != nil { // requests need a response; notifications don't
				var result any
				switch req.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}
				case "tools/list":
					result = map[string]any{"tools": []map[string]any{{
						"name":        "echo",
						"description": "Echo the input back",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}}},
					}}}
				case "tools/call":
					var p struct {
						Arguments json.RawMessage `json:"arguments"`
					}
					_ = json.Unmarshal(req.Params, &p)
					result = map[string]any{"content": []map[string]any{{"type": "text", "text": "echoed: " + string(p.Arguments)}}}
				}
				resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
				_, _ = conn.Write(append(resp, '\n'))
			}
		}
		if err != nil {
			return
		}
	}
}

func TestClient_HandshakeListCall(t *testing.T) {
	cConn, sConn := net.Pipe()
	go mockServer(sConn)

	c := newClient("mock", cConn, cConn, func() error { return cConn.Close() })
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}

	out, err := c.CallTool(ctx, "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(out, `echoed: {"msg":"hi"}`) {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestQualify(t *testing.T) {
	if got := qualify("my server", "read.file"); got != "my_server__read_file" {
		t.Fatalf("qualify = %q", got)
	}
}
