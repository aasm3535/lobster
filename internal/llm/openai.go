package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI talks to any OpenAI-compatible /chat/completions endpoint with native tools.
type OpenAI struct {
	BaseURL string
	APIKey  string
	Model   string
	client  *http.Client
}

func NewOpenAI(baseURL, apiKey, model string) *OpenAI {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAI{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}
}

func (o *OpenAI) Name() string { return "openai:" + o.Model }

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function oaFunc `json:"function"`
}

type oaFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaTool struct {
	Type     string        `json:"type"`
	Function oaToolFuncDef `json:"function"`
}

type oaToolFuncDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type oaRequest struct {
	Model      string      `json:"model"`
	Messages   []oaMessage `json:"messages"`
	Tools      []oaTool    `json:"tools,omitempty"`
	ToolChoice string      `json:"tool_choice,omitempty"`
}

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content   string       `json:"content"`
			ToolCalls []oaToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAI) Chat(ctx context.Context, system string, msgs []Message, tools []ToolDef) (*Response, error) {
	var om []oaMessage
	if system != "" {
		om = append(om, oaMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			om = append(om, oaMessage{Role: "user", Content: m.Content})
		case RoleAssistant:
			am := oaMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, oaToolCall{
					ID:       tc.ID,
					Type:     "function",
					Function: oaFunc{Name: tc.Name, Arguments: tc.Arguments},
				})
			}
			om = append(om, am)
		case RoleTool:
			om = append(om, oaMessage{Role: "tool", ToolCallID: m.ToolCallID, Content: m.Content})
		}
	}

	var ot []oaTool
	for _, t := range tools {
		ot = append(ot, oaTool{Type: "function", Function: oaToolFuncDef{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		}})
	}

	body := oaRequest{Model: o.Model, Messages: om, Tools: ot}
	if len(ot) > 0 {
		body.ToolChoice = "auto"
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("openai http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var or oaResponse
	if err := json.Unmarshal(raw, &or); err != nil {
		return nil, fmt.Errorf("decode: %w (body: %s)", err, truncate(string(raw), 500))
	}
	if or.Error != nil {
		return nil, fmt.Errorf("openai: %s", or.Error.Message)
	}
	if len(or.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty response (no choices)")
	}
	ch := or.Choices[0]
	out := &Response{Content: ch.Message.Content, Stop: ch.FinishReason}
	for _, tc := range ch.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
