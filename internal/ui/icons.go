package ui

import (
	"bytes"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"sync"

	"gioui.org/op/paint"
	"wlchat/internal/ui/assets"
)

type Icons struct {
	mu    sync.Mutex
	cache map[string]paint.ImageOp
}

func newIcons() *Icons {
	return &Icons{
		cache: make(map[string]paint.ImageOp),
	}
}

func (ic *Icons) get(name string) (paint.ImageOp, bool) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	op, ok := ic.cache[name]
	if ok {
		return op, true
	}

	var data []byte
	switch name {
	case "gemini":
		data = assets.GeminiLogo
	case "llama":
		data = assets.LlamaLogo
	case "groq":
		data = assets.GroqLogo
	case "user":
		data = assets.UserLogo
	default:
		return paint.ImageOp{}, false
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return paint.ImageOp{}, false
	}

	op = paint.NewImageOp(img)
	ic.cache[name] = op
	return op, true
}
