package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type Call struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Calls   []Call `json:"tool_calls,omitempty"`
	CallID  string `json:"tool_call_id,omitempty"`
}
type Usage struct {
	Input  int64 `json:"prompt_tokens"`
	Cached int64 `json:"prompt_cache_hit_tokens"`
	Output int64 `json:"completion_tokens"`
}
type Completion struct {
	Message Message
	Usage   *Usage
}
type Provider interface {
	Complete(context.Context, []Message, []any) (Completion, error)
}
type DeepSeek struct {
	Key, Model string
	Client     *http.Client
	Endpoint   string
}

func (p *DeepSeek) Complete(ctx context.Context, messages []Message, tools []any) (Completion, error) {
	var result Completion
	if p.Key == "" {
		return result, errors.New("AI provider is not configured")
	}
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/chat/completions"
	}
	raw, err := json.Marshal(map[string]any{"model": p.Model, "messages": messages, "tools": tools, "thinking": map[string]string{"type": "disabled"}, "max_tokens": 2500, "stream": false})
	if err != nil {
		return result, errors.New("invalid AI request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, errors.New("invalid AI endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return result, errors.New("AI request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, errors.New("AI provider rejected request")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(body) > 2*1024*1024 {
		return result, errors.New("invalid AI response")
	}
	var envelope struct {
		Choices []struct {
			Message Message `json:"message"`
			Finish  string  `json:"finish_reason"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err = json.Unmarshal(body, &envelope); err != nil || len(envelope.Choices) != 1 {
		return result, errors.New("invalid AI response")
	}
	result.Usage = envelope.Usage
	if envelope.Choices[0].Finish != "stop" && envelope.Choices[0].Finish != "tool_calls" {
		return result, errors.New("AI answer incomplete")
	}
	result.Message = envelope.Choices[0].Message
	result.Message.Role = "assistant"
	return result, nil
}
