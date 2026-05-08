// gemini/client.go
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"wlchat/internal/conversation"
	"wlchat/internal/provider"
)

const apiURL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"

var Models = []string{
	"gemini-3.1-pro",
	"gemini-3.1-flash",
	"gemini-3.1-flash-lite",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-3.1-pro-thinking",
	"gemini-experimental",
	"gemma-3-27b-it",
	"gemma-4-31b-it",
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

func (c *Client) Name() string                         { return "gemini" }
func (c *Client) Icon() string                         { return "♊" }
func (c *Client) AvailableModels() []string            { return Models }
func (c *Client) GetModel() string                     { return c.model }
func (c *Client) SetModel(m string)                    { c.model = m }
func (c *Client) GetRateLimit() provider.RateLimitInfo { return c.rateLimit }

type chatRequest struct {
	Model     string                 `json:"model"`
	Messages  []conversation.Message `json:"messages"`
	Stream    bool                   `json:"stream"`
	MaxTokens int                    `json:"max_tokens,omitempty"`
}

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func parseSSELine(line string) (string, bool, bool) {
	if !strings.HasPrefix(line, "data: ") {
		return "", false, true
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return "", true, false
	}
	var sr streamResponse
	if err := json.Unmarshal([]byte(data), &sr); err != nil {
		return "", false, true
	}
	if len(sr.Choices) == 0 || sr.Choices[0].Delta.Content == "" {
		return "", false, true
	}
	return sr.Choices[0].Delta.Content, false, false
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

func (c *Client) FetchUsage(ctx context.Context) (provider.RateLimitInfo, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []conversation.Message{
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
		return c.rateLimit, fmt.Errorf("gemini API error: %s", resp.Status)
	}

	return c.rateLimit, nil
}

func (c *Client) StreamChat(ctx context.Context, messages []conversation.Message) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)

		reqBody := chatRequest{
			Model:    c.model,
			Messages: messages,
			Stream:   true,
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
			ch <- provider.StreamEvent{Err: fmt.Errorf("gemini API error: %s", resp.Status)}
			return
		}

		c.extractRateLimit(resp)

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			token, done, skip := parseSSELine(scanner.Text())
			if done {
				ch <- provider.StreamEvent{Done: true}
				return
			}
			if skip {
				continue
			}
			ch <- provider.StreamEvent{Token: token}
		}
		if err := scanner.Err(); err != nil {
			ch <- provider.StreamEvent{Err: err}
		}
	}()
	return ch
}
