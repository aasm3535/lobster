package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Anthropic talks to any Anthropic-compatible /v1/messages endpoint with native tools.
type Anthropic struct {
	BaseURL    string
	APIKey     string
	Model      string
	MaxTokens  int
	authScheme string
	headers    map[string]string
	client     *http.Client

	label string // provider name shown in logs (e.g. "anthropic", "minimax")
}

func NewAnthropic(baseURL, apiKey, model string, maxTokens int, authScheme string, headers map[string]string) *Anthropic {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	if authScheme == "" {
		authScheme = AuthAPIKey // Anthropic's canonical x-api-key
	}
	return &Anthropic{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		Model:      model,
		MaxTokens:  maxTokens,
		authScheme: authScheme,
		headers:    headers,
		client:     &http.Client{Timeout: 5 * time.Minute},
		label:      "anthropic",
	}
}

// NewMiniMax is a preset: the Anthropic protocol pointed at MiniMax's compatible
// endpoint, which authenticates with a Bearer token rather than x-api-key.
// See https://platform.minimax.io/docs/token-plan/claude-code
func NewMiniMax(baseURL, apiKey, model string, maxTokens int, authScheme string, headers map[string]string) *Anthropic {
	if baseURL == "" {
		baseURL = "https://api.minimax.io/anthropic"
	}
	if authScheme == "" {
		authScheme = AuthBearer
	}
	a := NewAnthropic(baseURL, apiKey, model, maxTokens, authScheme, headers)
	a.label = "minimax"
	return a
}

func (a *Anthropic) Name() string { return a.label + ":" + a.Model }

type anBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	// image
	Source *anImageSource `json:"source,omitempty"`
}

type anImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/jpeg"
	Data      string `json:"data"`       // base64-encoded bytes
}

type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

type anTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anRequest struct {
	Model     string      `json:"model"`
	System    string      `json:"system,omitempty"`
	Messages  []anMessage `json:"messages"`
	Tools     []anTool    `json:"tools,omitempty"`
	MaxTokens int         `json:"max_tokens"`
	Stream    bool        `json:"stream,omitempty"`
}

type anResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// encodeMessages maps Lobster's messages onto Anthropic's block format.
func encodeMessages(msgs []Message) []anMessage {
	var am []anMessage
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			// Images go before the text so the model reads the picture, then the ask.
			for _, img := range m.Images {
				am = appendUserBlock(am, anBlock{Type: "image", Source: &anImageSource{
					Type: "base64", MediaType: img.MediaType, Data: base64.StdEncoding.EncodeToString(img.Data),
				}})
			}
			if strings.TrimSpace(m.Content) != "" || len(m.Images) == 0 {
				am = appendUserBlock(am, anBlock{Type: "text", Text: m.Content})
			}
		case RoleAssistant:
			var blocks []anBlock
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				// SanitizeArgs also heals histories already persisted with truncated
				// JSON, which would otherwise fail to marshal forever.
				blocks = append(blocks, anBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: json.RawMessage(SanitizeArgs(tc.Arguments))})
			}
			am = append(am, anMessage{Role: "assistant", Content: blocks})
		case RoleTool:
			// Tool results live in a user turn; consecutive ones group into one message.
			am = appendUserBlock(am, anBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content})
		}
	}
	// Defensive repair: providers reject the whole history if any tool_use lacks a
	// following tool_result (2013), or if a user turn leads with text before its
	// tool_results. Self-heal both so a once-corrupted history isn't stuck forever.
	am = repairToolPairing(am)
	for i := range am {
		if am[i].Role == "user" {
			am[i].Content = toolResultsFirst(am[i].Content)
		}
	}
	return am
}

// repairToolPairing injects a stub tool_result for any tool_use that isn't answered
// by the immediately following user turn — preventing a dangling tool_call (e.g. one
// left by a mid-tool shutdown) from poisoning the entire request.
func repairToolPairing(am []anMessage) []anMessage {
	var out []anMessage
	for i := 0; i < len(am); i++ {
		out = append(out, am[i])
		if am[i].Role != "assistant" {
			continue
		}
		var ids []string
		for _, b := range am[i].Content {
			if b.Type == "tool_use" {
				ids = append(ids, b.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		have := map[string]bool{}
		if i+1 < len(am) && am[i+1].Role == "user" {
			for _, b := range am[i+1].Content {
				if b.Type == "tool_result" {
					have[b.ToolUseID] = true
				}
			}
		}
		var stubs []anBlock
		for _, id := range ids {
			if !have[id] {
				stubs = append(stubs, anBlock{Type: "tool_result", ToolUseID: id, Content: "[no result — interrupted]"})
			}
		}
		if len(stubs) == 0 {
			continue
		}
		if i+1 < len(am) && am[i+1].Role == "user" {
			am[i+1].Content = append(stubs, am[i+1].Content...) // lead the existing turn
		} else {
			out = append(out, anMessage{Role: "user", Content: stubs}) // fresh turn
		}
	}
	return out
}

// toolResultsFirst stable-partitions a user turn's blocks so every tool_result comes
// before any text/image — the order Anthropic requires within a turn.
func toolResultsFirst(blocks []anBlock) []anBlock {
	hasResult := false
	for _, b := range blocks {
		if b.Type == "tool_result" {
			hasResult = true
			break
		}
	}
	if !hasResult {
		return blocks
	}
	var results, rest []anBlock
	for _, b := range blocks {
		if b.Type == "tool_result" {
			results = append(results, b)
		} else {
			rest = append(rest, b)
		}
	}
	return append(results, rest...)
}

func encodeTools(tools []ToolDef) []anTool {
	var at []anTool
	for _, t := range tools {
		at = append(at, anTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}
	return at
}

// newRequest builds a /v1/messages POST with the right auth headers.
func (a *Anthropic) newRequest(ctx context.Context, body anRequest) (*http.Request, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	applyAuth(req, a.authScheme, a.APIKey, a.headers)
	return req, nil
}

func (a *Anthropic) Chat(ctx context.Context, system string, msgs []Message, tools []ToolDef) (*Response, error) {
	body := anRequest{Model: a.Model, System: system, Messages: encodeMessages(msgs), Tools: encodeTools(tools), MaxTokens: a.MaxTokens}
	req, err := a.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("anthropic http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var ar anResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return nil, fmt.Errorf("decode: %w (body: %s)", err, truncate(string(raw), 500))
	}
	if ar.Error != nil {
		return nil, fmt.Errorf("anthropic: %s", ar.Error.Message)
	}

	out := &Response{Stop: ar.StopReason}
	var text []string
	for _, b := range ar.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: SanitizeArgs(string(b.Input))})
		}
	}
	out.Content = strings.Join(text, "")
	return out, nil
}

// ChatStream is the streaming variant: it parses Anthropic's SSE, forwarding each text
// chunk to onText as it lands, and assembles the same Response (text + tool calls) at
// the end. This is what makes the reply "type out" live in Telegram.
func (a *Anthropic) ChatStream(ctx context.Context, system string, msgs []Message, tools []ToolDef, onText func(string)) (*Response, error) {
	body := anRequest{Model: a.Model, System: system, Messages: encodeMessages(msgs), Tools: encodeTools(tools), MaxTokens: a.MaxTokens, Stream: true}
	req, err := a.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	// One streamed content block: text accumulates, or tool_use input JSON accumulates.
	type blk struct {
		typ, id, name string
		input         strings.Builder
	}
	blocks := map[int]*blk{}
	var textBuf strings.Builder
	out := &Response{}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "content_block_start":
			blocks[ev.Index] = &blk{typ: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
		case "content_block_delta":
			b := blocks[ev.Index]
			if b == nil {
				b = &blk{}
				blocks[ev.Index] = b
			}
			switch ev.Delta.Type {
			case "text_delta":
				textBuf.WriteString(ev.Delta.Text)
				if onText != nil {
					onText(ev.Delta.Text)
				}
			case "input_json_delta":
				b.input.WriteString(ev.Delta.PartialJSON)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.Stop = ev.Delta.StopReason
			}
		case "error":
			if ev.Error != nil {
				return nil, fmt.Errorf("anthropic: %s", ev.Error.Message)
			}
			return nil, fmt.Errorf("anthropic stream error")
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stream read: %w", err)
	}

	out.Content = textBuf.String()
	for i := 0; i < len(blocks); i++ {
		b := blocks[i]
		if b == nil || b.typ != "tool_use" {
			continue
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.id, Name: b.name, Arguments: SanitizeArgs(b.input.String())})
	}
	return out, nil
}

// appendUserBlock appends a block to the trailing user message, or starts a new one.
func appendUserBlock(am []anMessage, b anBlock) []anMessage {
	if n := len(am); n > 0 && am[n-1].Role == "user" {
		am[n-1].Content = append(am[n-1].Content, b)
		return am
	}
	return append(am, anMessage{Role: "user", Content: []anBlock{b}})
}
