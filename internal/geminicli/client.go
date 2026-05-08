// geminicli/client.go
package geminicli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"wlchat/internal/conversation"
	"wlchat/internal/provider"
)

var Models = []string{
	"auto-gemini-3",
	"gemini-3.1-pro",
	"gemini-3.1-flash",
	"gemini-3.1-flash-lite",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-3.1-pro-thinking",
	"gemini-experimental",
}

type Client struct {
	model string
}

func NewClient() *Client {
	return &Client{
		model: Models[0],
	}
}

// Available returns true if the gemini CLI is installed and in PATH.
func Available() bool {
	_, err := exec.LookPath("gemini")
	return err == nil
}

func (c *Client) Name() string                         { return "gemini-cli" }
func (c *Client) Icon() string                         { return "🐚" }
func (c *Client) AvailableModels() []string            { return Models }
func (c *Client) GetModel() string                     { return c.model }
func (c *Client) SetModel(m string)                    { c.model = m }
func (c *Client) GetRateLimit() provider.RateLimitInfo { return provider.RateLimitInfo{} }

func (c *Client) FetchUsage(ctx context.Context) (provider.RateLimitInfo, error) {
	return provider.RateLimitInfo{}, fmt.Errorf("usage info not available for gemini CLI")
}

type streamJSON struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	Delta   bool   `json:"delta,omitempty"`
	Status  string `json:"status,omitempty"`
}

func formatPrompt(messages []conversation.Message) string {
	if len(messages) == 1 {
		return messages[0].Content
	}

	var b strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "user":
			b.WriteString("User: ")
		case "assistant":
			b.WriteString("Assistant: ")
		}
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}

func (c *Client) StreamChat(ctx context.Context, messages []conversation.Message) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)

		prompt := formatPrompt(messages)

		args := []string{"-p", prompt, "-o", "stream-json"}
		if c.model != "auto-gemini-3" {
			args = append(args, "-m", c.model)
		}

		cmd := exec.CommandContext(ctx, "gemini", args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- provider.StreamEvent{Err: fmt.Errorf("gemini CLI: %w", err)}
			return
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		if err := cmd.Start(); err != nil {
			ch <- provider.StreamEvent{Err: fmt.Errorf("gemini CLI: %w", err)}
			return
		}

		// Ensure the process is always reaped to avoid leaking descriptors.
		defer func() { _ = cmd.Wait() }()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		gotOutput := false
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var ev streamJSON
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}

			switch ev.Type {
			case "message":
				if ev.Role == "assistant" && ev.Delta && ev.Content != "" {
					gotOutput = true
					ch <- provider.StreamEvent{Token: ev.Content}
				}
			case "result":
				ch <- provider.StreamEvent{Done: true}
				return
			}
		}

		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			ch <- provider.StreamEvent{Err: fmt.Errorf("gemini CLI: %w", err)}
			return
		}

		// Scanner closed without a "result" event: capture stderr for diagnostics.
		if !gotOutput && ctx.Err() == nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "no output"
			}
			ch <- provider.StreamEvent{Err: fmt.Errorf("gemini CLI: %s", msg)}
		}
	}()
	return ch
}
