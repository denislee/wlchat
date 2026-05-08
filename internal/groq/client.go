// groq/client.go
package groq

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"wlchat/internal/conversation"
	"wlchat/internal/provider"
)

const apiURL = "https://api.groq.com/openai/v1/chat/completions"

var Models = []string{
	"llama-3.3-70b-versatile",
	"llama-3.1-8b-instant",
	"meta-llama/llama-4-scout-17b-16e-instruct",
	"openai/gpt-oss-120b",
	"openai/gpt-oss-20b",
	"qwen/qwen3-32b",
	"moonshotai/kimi-k2-instruct",
	"groq/compound",
	"groq/compound-mini",
}

// reasoningModels lists models that support reasoning_format: "parsed".
var reasoningModels = map[string]bool{
	"qwen/qwen3-32b":      true,
	"openai/gpt-oss-120b": true,
	"openai/gpt-oss-20b":  true,
}

type Client struct {
	apiKey     string
	httpClient *http.Client
	model      string
	rateLimit  provider.RateLimitInfo
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{},
		model:      Models[0],
	}
}

func (c *Client) Name() string                         { return "groq" }
func (c *Client) Icon() string                         { return "⚡" }
func (c *Client) AvailableModels() []string            { return Models }
func (c *Client) GetModel() string                     { return c.model }
func (c *Client) SetModel(m string)                    { c.model = m }
func (c *Client) GetRateLimit() provider.RateLimitInfo { return c.rateLimit }

type groqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []groqMessage `json:"messages"`
	Stream          bool          `json:"stream"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	ReasoningFormat string        `json:"reasoning_format,omitempty"`
}

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
}

// parseSSELine parses a single SSE line and returns a StreamEvent.
func parseSSELine(line string) (provider.StreamEvent, bool) {
	if !strings.HasPrefix(line, "data: ") {
		return provider.StreamEvent{}, true
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return provider.StreamEvent{Done: true}, false
	}
	var sr streamResponse
	if err := json.Unmarshal([]byte(data), &sr); err != nil {
		return provider.StreamEvent{}, true
	}
	if len(sr.Choices) == 0 {
		return provider.StreamEvent{}, true
	}
	delta := sr.Choices[0].Delta
	if delta.Reasoning != "" {
		return provider.StreamEvent{Token: delta.Reasoning, Reasoning: true}, false
	}
	if delta.Content != "" {
		return provider.StreamEvent{Token: delta.Content}, false
	}
	return provider.StreamEvent{}, true
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func readAPIError(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("groq API error: %s", resp.Status)
	}
	var ae apiError
	if err := json.Unmarshal(body, &ae); err == nil && ae.Error.Message != "" {
		return fmt.Errorf("groq: %s", ae.Error.Message)
	}
	return fmt.Errorf("groq API error: %s — %s", resp.Status, string(body))
}

func (c *Client) extractRateLimit(resp *http.Response) {
	c.rateLimit = provider.RateLimitInfo{
		LimitRequests:     resp.Header.Get("x-ratelimit-limit-requests"),
		LimitTokens:       resp.Header.Get("x-ratelimit-limit-tokens"),
		RemainingRequests: resp.Header.Get("x-ratelimit-remaining-requests"),
		RemainingTokens:   resp.Header.Get("x-ratelimit-remaining-tokens"),
		ResetRequests:     resp.Header.Get("x-ratelimit-reset-requests"),
		ResetTokens:       resp.Header.Get("x-ratelimit-reset-tokens"),
	}
}

// FetchUsage makes a minimal request to refresh rate limit info from response headers.
func (c *Client) FetchUsage(ctx context.Context) (provider.RateLimitInfo, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []groqMessage{
			{Role: "user", Content: "hi"},
		},
		Stream:    false,
		MaxTokens: 1,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return c.rateLimit, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return c.rateLimit, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.rateLimit, err
	}
	defer resp.Body.Close()

	c.extractRateLimit(resp)

	if resp.StatusCode != http.StatusOK {
		return c.rateLimit, readAPIError(resp)
	}

	return c.rateLimit, nil
}

// compound model tag parsing

const maxCompoundTagLen = 9 // len("</output>")

type compoundState int

const (
	cpContent compoundState = iota
	cpThink
	cpTool
	cpOutput
)

type compoundParser struct {
	state compoundState
	buf   strings.Builder
}

var compoundTags = []struct {
	text  string
	state compoundState
}{
	// Longer tags first to avoid partial matches.
	{"</output>", cpContent},
	{"</think>", cpContent},
	{"</tool>", cpContent},
	{"<output>", cpOutput},
	{"<think>", cpThink},
	{"<tool>", cpTool},
}

func (p *compoundParser) feed(token string) []provider.StreamEvent {
	var events []provider.StreamEvent
	text := p.buf.String() + token
	p.buf.Reset()

	for len(text) > 0 {
		idx := strings.Index(text, "<")
		if idx == -1 {
			if ev := p.makeEvent(text); ev.Token != "" {
				events = append(events, ev)
			}
			break
		}
		if idx > 0 {
			if ev := p.makeEvent(text[:idx]); ev.Token != "" {
				events = append(events, ev)
			}
			text = text[idx:]
		}

		matched := false
		for _, tag := range compoundTags {
			if strings.HasPrefix(text, tag.text) {
				p.state = tag.state
				text = text[len(tag.text):]
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		// Could be a partial tag — buffer and wait for more input.
		if len(text) < maxCompoundTagLen {
			p.buf.WriteString(text)
			break
		}

		// Not a known tag, emit the '<' as content.
		if ev := p.makeEvent("<"); ev.Token != "" {
			events = append(events, ev)
		}
		text = text[1:]
	}

	return events
}

func (p *compoundParser) flush() []provider.StreamEvent {
	if p.buf.Len() == 0 {
		return nil
	}
	text := p.buf.String()
	p.buf.Reset()
	if ev := p.makeEvent(text); ev.Token != "" {
		return []provider.StreamEvent{ev}
	}
	return nil
}

func (p *compoundParser) makeEvent(content string) provider.StreamEvent {
	return provider.StreamEvent{
		Token:     content,
		Reasoning: p.state == cpThink,
	}
}

func isCompoundModel(model string) bool {
	return model == "groq/compound" || model == "groq/compound-mini"
}

func wrapCompound(in <-chan provider.StreamEvent) <-chan provider.StreamEvent {
	out := make(chan provider.StreamEvent)
	go func() {
		defer close(out)
		var parser compoundParser
		for ev := range in {
			if ev.Err != nil || ev.Done {
				for _, flushed := range parser.flush() {
					out <- flushed
				}
				out <- ev
				continue
			}
			for _, parsed := range parser.feed(ev.Token) {
				out <- parsed
			}
		}
	}()
	return out
}

// StreamChat sends messages to Groq and returns a channel of streaming events.
func (c *Client) StreamChat(ctx context.Context, messages []conversation.Message) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)

		var gmsgs []groqMessage
		for _, m := range messages {
			gmsgs = append(gmsgs, groqMessage{
				Role:    m.Role,
				Content: m.Content,
			})
		}

		reqBody := chatRequest{
			Model:    c.model,
			Messages: gmsgs,
			Stream:   true,
		}
		if reasoningModels[c.model] {
			reqBody.ReasoningFormat = "parsed"
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			ch <- provider.StreamEvent{Err: err}
			return
		}

		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
		if err != nil {
			ch <- provider.StreamEvent{Err: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			ch <- provider.StreamEvent{Err: err}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			ch <- provider.StreamEvent{Err: readAPIError(resp)}
			return
		}

		c.extractRateLimit(resp)

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			event, skip := parseSSELine(scanner.Text())
			if skip {
				continue
			}
			ch <- event
			if event.Done {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- provider.StreamEvent{Err: err}
		}
	}()

	if isCompoundModel(c.model) {
		return wrapCompound(ch)
	}
	return ch
}
