// conversation/conversation.go
package conversation

import (
	"regexp"
	"strings"
	"time"
)

type Message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Model     string `json:"model,omitempty"`
	Reasoning bool   `json:"reasoning,omitempty"`
}

type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Messages  []Message `json:"messages"`
	Mode      string    `json:"mode,omitempty"`
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9-]`)

func New(title string) Conversation {
	now := time.Now()
	slug := strings.ToLower(title)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = nonAlphanumeric.ReplaceAllString(slug, "")
	id := now.Format("2006-01-02T15-04-05") + "_" + slug
	return Conversation{
		ID:        id,
		Title:     title,
		CreatedAt: now,
		Messages:  []Message{},
	}
}
