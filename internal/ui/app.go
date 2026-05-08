// Package ui implements the Gio-based GUI for wlchat. Gio renders directly
// against Wayland on Linux (no GTK/X11 fallback) when GDK_BACKEND is unset
// or GIO_RENDERER is not overridden.
package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"wlchat/internal/conversation"
	"wlchat/internal/provider"
	"wlchat/internal/store"
)

// Run starts the Gio event loop on the current goroutine. It must be called
// from main (Gio requires the OS thread that owns the main loop).
func Run(s *store.Store, providers []provider.Provider) error {
	a := newApp(s, providers)
	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("wlchat"),
			app.Size(unit.Dp(1100), unit.Dp(720)),
			app.MinSize(unit.Dp(640), unit.Dp(420)),
		)
		if err := a.loop(w); err != nil {
			log.Println("wlchat:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
	return nil
}

// ---------- application state ----------

type App struct {
	store     *store.Store
	providers []provider.Provider
	provIdx   int

	skills []store.Skill

	// Conversations list (metadata only) for the sidebar.
	convs []conversation.Conversation

	// Currently open conversation (full messages). nil means no chat selected.
	current *conversation.Conversation

	// Streaming state.
	streaming    bool
	streamCancel context.CancelFunc
	streamBuf    strings.Builder // accumulates assistant tokens during stream
	pendingErr   string

	// Skill currently active for next send (empty = plain chat).
	activeSkill string

	mu sync.Mutex // protects current, convs, streaming, streamBuf, pendingErr

	// Widgets.
	theme       *Theme
	convList    widget.List
	chatList    widget.List
	popupList   widget.List
	input       widget.Editor
	sendBtn     widget.Clickable
	newBtn      widget.Clickable
	modelBtn    widget.Clickable
	skillBtn    widget.Clickable
	provBtn     widget.Clickable
	convClicks  map[string]*widget.Clickable
	convDelete  map[string]*widget.Clickable

	// Popups.
	showModelPicker bool
	showSkillPicker bool
	showProvPicker  bool
	settingsOpen    bool
	showDeleteConfirm bool
	convToDelete      string
	modelChoices    []*widget.Clickable
	skillChoices    []*widget.Clickable
	provChoices     []*widget.Clickable

	settings *SettingsScreen
	icons    *Icons

	allModels []modelEntry

	// Async UI invalidation.
	window *app.Window

	focused         bool // tracks if initial focus has been applied
	msgIdx          int  // selected message index for j/k navigation
	focusOnMessages bool // true when navigating messages, false when input is focused
	focusOnSidebar  bool // true when navigating conversation list
	pickerIdx       int  // selected index in popups
	lastPickerIdx   int  // used to detect picker navigation
	convIdx         int  // selected conversation index in sidebar
}

func newApp(s *store.Store, providers []provider.Provider) *App {
	th := newTheme()

	a := &App{
		store:           s,
		providers:       providers,
		theme:           th,
		convClicks:      map[string]*widget.Clickable{},
		convDelete:      map[string]*widget.Clickable{},
		msgIdx:          -1,
		focusOnMessages: false,
	}

	a.settings = newSettingsScreen(th, a.saveConfig, func() {
		a.settingsOpen = false
	})
	a.icons = newIcons()

	for i, p := range providers {
		for _, m := range p.AvailableModels() {
			a.allModels = append(a.allModels, modelEntry{provIdx: i, name: m})
		}
	}

	a.convList.Axis = layout.Vertical
	a.chatList.Axis = layout.Vertical
	a.input.Submit = true
	a.input.SingleLine = false

	if cfg, err := s.LoadConfig(); err == nil {
		a.skills = cfg.Skills

		// Restore fonts
		a.theme.Fonts.Global = FontStyle{Face: cfg.Fonts.Global.Face, Size: cfg.Fonts.Global.Size}
		a.theme.Fonts.Sidebar = FontStyle{Face: cfg.Fonts.Sidebar.Face, Size: cfg.Fonts.Sidebar.Size}
		a.theme.Fonts.Header = FontStyle{Face: cfg.Fonts.Header.Face, Size: cfg.Fonts.Header.Size}
		a.theme.Fonts.Messages = FontStyle{Face: cfg.Fonts.Messages.Face, Size: cfg.Fonts.Messages.Size}
		a.theme.Fonts.Input = FontStyle{Face: cfg.Fonts.Input.Face, Size: cfg.Fonts.Input.Size}

		// Fallback for global size if not set
		if a.theme.Fonts.Global.Size == 0 {
			a.theme.Fonts.Global.Size = 13
		}

		if cfg.Provider != "" {
			for i, p := range providers {
				if p.Name() == cfg.Provider {
					a.provIdx = i
					break
				}
			}
		}
		if cfg.Model != "" {
			for _, m := range a.providers[a.provIdx].AvailableModels() {
				if m == cfg.Model {
					a.providers[a.provIdx].SetModel(cfg.Model)
					break
				}
			}
		}
	}
	a.refreshConvs()
	return a
}

func (a *App) saveConfig() {
	cfg := store.Config{
		Provider: a.providers[a.provIdx].Name(),
		Model:    a.providers[a.provIdx].GetModel(),
		Skills:   a.skills,
		Fonts:    a.uiToStoreFonts(),
	}
	_ = a.store.SaveConfig(cfg)
}

func (a *App) uiToStoreFonts() store.SectionFonts {
	return store.SectionFonts{
		Global:   store.FontStyle{Face: a.theme.Fonts.Global.Face, Size: a.theme.Fonts.Global.Size},
		Sidebar:  store.FontStyle{Face: a.theme.Fonts.Sidebar.Face, Size: a.theme.Fonts.Sidebar.Size},
		Header:   store.FontStyle{Face: a.theme.Fonts.Header.Face, Size: a.theme.Fonts.Header.Size},
		Messages: store.FontStyle{Face: a.theme.Fonts.Messages.Face, Size: a.theme.Fonts.Messages.Size},
		Input:    store.FontStyle{Face: a.theme.Fonts.Input.Face, Size: a.theme.Fonts.Input.Size},
	}
}

func (a *App) refreshConvs() {
	convs, err := a.store.ListMeta()
	if err != nil {
		return
	}
	sort.Slice(convs, func(i, j int) bool { return convs[i].CreatedAt.After(convs[j].CreatedAt) })
	a.convs = convs
}

// ---------- event loop ----------

func (a *App) loop(w *app.Window) error {
	a.window = w
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			a.cancelStream()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.handleKeys(gtx)
			a.handleClicks(gtx)
			a.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func (a *App) handleKeys(gtx layout.Context) {
	// Process editor events only if no pickers or settings are open.
	if !a.showModelPicker && !a.showSkillPicker && !a.settingsOpen {
		for {
			ev, ok := a.input.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				a.send()
			}
		}
	}

	// Listen for shortcuts.
	for {
		ev, ok := gtx.Event(
			key.Filter{Name: key.NameReturn, Required: key.ModCtrl},
			key.Filter{Name: key.NameEscape},
			key.Filter{Name: "[", Required: key.ModCtrl},
			key.Filter{Name: "K", Required: key.ModCtrl},
			key.Filter{Name: "N", Required: key.ModCtrl},
			key.Filter{Name: "M", Required: key.ModCtrl},
			key.Filter{Name: "P", Required: key.ModCtrl},
			key.Filter{Name: "F", Required: key.ModCtrl},
			key.Filter{Name: "B", Required: key.ModCtrl},
			key.Filter{Name: "W", Required: key.ModCtrl},
			key.Filter{Name: key.NameDeleteBackward, Required: key.ModCtrl},
			key.Filter{Name: ","},
			key.Filter{Name: "J"},
			key.Filter{Name: "K"},
			key.Filter{Name: "I"},
			key.Filter{Name: "Y"},
			key.Filter{Name: "H"},
			key.Filter{Name: "L"},
			key.Filter{Name: key.NameTab},
			key.Filter{Name: key.NameReturn},
		)
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			switch {
			case ke.Name == key.NameTab:
				if a.focusOnSidebar {
					// sidebar -> chat log
					a.focusOnSidebar = false
					a.focusOnMessages = true
					a.mu.Lock()
					if a.current != nil {
						count := len(a.current.Messages)
						if a.streaming || a.streamBuf.Len() > 0 {
							count++
						}
						if a.pendingErr != "" {
							count++
						}
						if count > 0 {
							a.msgIdx = count - 1
							ensureVisible(&a.chatList, a.msgIdx, count)
						}
					}
					a.mu.Unlock()
				} else if a.focusOnMessages {
					// chat log -> message input
					a.focusOnMessages = false
					a.focusOnSidebar = false
					gtx.Execute(key.FocusCmd{Tag: &a.input})
				} else {
					// message input -> sidebar
					a.focusOnSidebar = true
					a.focusOnMessages = false
					// Find current conversation index if any
					a.mu.Lock()
					if a.current != nil {
						for i, c := range a.convs {
							if c.ID == a.current.ID {
								a.convIdx = i
								ensureVisible(&a.convList, i, len(a.convs))
								break
							}
						}
					}
					a.mu.Unlock()
				}
			case ke.Name == key.NameReturn && ke.Modifiers.Contain(key.ModCtrl):
				a.send()
			case ke.Name == key.NameEscape || (ke.Name == "[" && ke.Modifiers.Contain(key.ModCtrl)):
				if a.showModelPicker || a.showSkillPicker || a.showProvPicker || a.settingsOpen || a.showDeleteConfirm {
					a.showModelPicker = false
					a.showSkillPicker = false
					a.showProvPicker = false
					a.settingsOpen = false
					a.showDeleteConfirm = false
				} else if a.streaming {
					a.cancelStream()
				} else {
					a.mu.Lock()
					if a.current != nil {
						count := len(a.current.Messages)
						if a.streaming || a.streamBuf.Len() > 0 {
							count++
						}
						if a.pendingErr != "" {
							count++
						}
						if count > 0 {
							a.msgIdx = count - 1
							ensureVisible(&a.chatList, a.msgIdx, count)
							a.focusOnMessages = true
						}
					}
					a.mu.Unlock()
				}
			case ke.Name == ",":
				if !gtx.Source.Focused(&a.input) {
					a.settingsOpen = !a.settingsOpen
					a.showModelPicker = false
					a.showSkillPicker = false
					a.showProvPicker = false
					a.showDeleteConfirm = false
				}
			case ke.Name == "K" && ke.Modifiers.Contain(key.ModCtrl):
				a.showSkillPicker = !a.showSkillPicker
				a.showModelPicker = false
				a.showProvPicker = false
				a.showDeleteConfirm = false
				a.pickerIdx = 0
				a.lastPickerIdx = -1
			case ke.Name == "M" && ke.Modifiers.Contain(key.ModCtrl):
				a.showModelPicker = !a.showModelPicker
				a.showSkillPicker = false
				a.showProvPicker = false
				a.showDeleteConfirm = false
				a.pickerIdx = 0
				a.lastPickerIdx = -1
			case ke.Name == "P" && ke.Modifiers.Contain(key.ModCtrl):
				a.showProvPicker = !a.showProvPicker
				a.showModelPicker = false
				a.showSkillPicker = false
				a.showDeleteConfirm = false
				a.pickerIdx = 0
				a.lastPickerIdx = -1
			case a.showDeleteConfirm && ke.Name == key.NameReturn:
				a.deleteConv(a.convToDelete)
				a.showDeleteConfirm = false
			case a.focusOnSidebar && !gtx.Source.Focused(&a.input) && ke.Name == "D":
				a.mu.Lock()
				if a.convIdx >= 0 && a.convIdx < len(a.convs) {
					a.convToDelete = a.convs[a.convIdx].ID
					a.showDeleteConfirm = true
				}
				a.mu.Unlock()
			case (ke.Name == "W" || ke.Name == key.NameDeleteBackward) && ke.Modifiers.Contain(key.ModCtrl):
				if !a.showModelPicker && !a.showSkillPicker && !a.showProvPicker && !a.settingsOpen && !a.focusOnMessages {
					DeleteWord(&a.input)
				}
			case ke.Name == "N" && ke.Modifiers.Contain(key.ModCtrl):
				a.newChat()
			case (a.showModelPicker || a.showSkillPicker || a.showProvPicker) && ke.Name == "J":
				a.pickerIdx++
				max := 0
				switch {
				case a.showModelPicker:
					max = len(a.allModels)
				case a.showSkillPicker:
					max = len(a.skills) + 1 // +1 for (none)
				case a.showProvPicker:
					max = len(a.providers)
				}
				if a.pickerIdx >= max {
					a.pickerIdx = max - 1
				}
			case (a.showModelPicker || a.showSkillPicker || a.showProvPicker) && ke.Name == "K":
				a.pickerIdx--
				if a.pickerIdx < 0 {
					a.pickerIdx = 0
				}
			case (a.showModelPicker || a.showSkillPicker || a.showProvPicker) && ke.Name == "F" && ke.Modifiers.Contain(key.ModCtrl):
				a.pickerIdx += 5
				max := 0
				switch {
				case a.showModelPicker:
					max = len(a.allModels)
				case a.showSkillPicker:
					max = len(a.skills) + 1
				case a.showProvPicker:
					max = len(a.providers)
				}
				if a.pickerIdx >= max {
					maxVal := max - 1
					if maxVal < 0 {
						maxVal = 0
					}
					a.pickerIdx = maxVal
				}
			case (a.showModelPicker || a.showSkillPicker || a.showProvPicker) && ke.Name == "B" && ke.Modifiers.Contain(key.ModCtrl):
				a.pickerIdx -= 5
				if a.pickerIdx < 0 {
					a.pickerIdx = 0
				}
			case (a.showModelPicker || a.showSkillPicker || a.showProvPicker) && ke.Name == key.NameReturn:
				if a.showModelPicker {
					if a.pickerIdx < len(a.allModels) {
						entry := a.allModels[a.pickerIdx]
						fmt.Printf("info: selected model %s for provider %s\n", entry.name, a.providers[entry.provIdx].Name())
						a.provIdx = entry.provIdx
						a.providers[a.provIdx].SetModel(entry.name)
						a.saveConfig()
						a.showModelPicker = false
					}
				} else if a.showSkillPicker {
					if a.pickerIdx == 0 {
						fmt.Printf("info: skill cleared\n")
						a.activeSkill = ""
					} else if a.pickerIdx-1 < len(a.skills) {
						fmt.Printf("info: selected skill %s\n", a.skills[a.pickerIdx-1].Mode)
						a.activeSkill = a.skills[a.pickerIdx-1].Mode
					}
					a.showSkillPicker = false
				} else if a.showProvPicker {
					if a.pickerIdx < len(a.providers) {
						fmt.Printf("info: switched to provider %s\n", a.providers[a.pickerIdx].Name())
						a.provIdx = a.pickerIdx
						a.saveConfig()
						a.showProvPicker = false
					}
				}
			case a.focusOnMessages && !gtx.Source.Focused(&a.input) && ke.Name == "J":
				a.mu.Lock()
				count := 0
				if a.current != nil {
					count = len(a.current.Messages)
					if a.streaming || a.streamBuf.Len() > 0 {
						count++
					}
					if a.pendingErr != "" {
						count++
					}
				}
				if a.msgIdx < count-1 {
					a.msgIdx++
					ensureVisible(&a.chatList, a.msgIdx, count)
				}
				a.mu.Unlock()
			case a.focusOnMessages && !gtx.Source.Focused(&a.input) && ke.Name == "K":
				a.mu.Lock()
				count := 0
				if a.current != nil {
					count = len(a.current.Messages)
					if a.streaming || a.streamBuf.Len() > 0 {
						count++
					}
					if a.pendingErr != "" {
						count++
					}
				}
				if a.msgIdx > 0 {
					a.msgIdx--
					ensureVisible(&a.chatList, a.msgIdx, count)
				}
				a.mu.Unlock()
			case a.focusOnSidebar && !gtx.Source.Focused(&a.input) && ke.Name == "J":
				a.mu.Lock()
				if a.convIdx < len(a.convs)-1 {
					a.convIdx++
					ensureVisible(&a.convList, a.convIdx, len(a.convs))
					id := a.convs[a.convIdx].ID
					a.mu.Unlock()
					a.openConv(id)
				} else {
					a.mu.Unlock()
				}
			case a.focusOnSidebar && !gtx.Source.Focused(&a.input) && ke.Name == "K":
				a.mu.Lock()
				if a.convIdx > 0 {
					a.convIdx--
					ensureVisible(&a.convList, a.convIdx, len(a.convs))
					id := a.convs[a.convIdx].ID
					a.mu.Unlock()
					a.openConv(id)
				} else {
					a.mu.Unlock()
				}
			case a.focusOnMessages && !gtx.Source.Focused(&a.input) && ke.Name == "H":
				a.focusOnMessages = false
				a.focusOnSidebar = true
				// Find current conversation index
				a.mu.Lock()
				if a.current != nil {
					for i, c := range a.convs {
						if c.ID == a.current.ID {
							a.convIdx = i
							ensureVisible(&a.convList, i, len(a.convs))
							break
						}
					}
				}
				a.mu.Unlock()
			case a.focusOnSidebar && !gtx.Source.Focused(&a.input) && ke.Name == "L":
				a.focusOnSidebar = false
				a.focusOnMessages = true
				a.mu.Lock()
				if a.current != nil {
					count := len(a.current.Messages)
					if a.streaming || a.streamBuf.Len() > 0 {
						count++
					}
					if a.pendingErr != "" {
						count++
					}
					if count > 0 {
						a.msgIdx = count - 1
						ensureVisible(&a.chatList, a.msgIdx, count)
					}
				}
				a.mu.Unlock()
			case a.focusOnMessages && !gtx.Source.Focused(&a.input) && ke.Name == "F" && ke.Modifiers.Contain(key.ModCtrl):
				a.mu.Lock()
				count := 0
				if a.current != nil {
					count = len(a.current.Messages)
					if a.streaming || a.streamBuf.Len() > 0 {
						count++
					}
					if a.pendingErr != "" {
						count++
					}
				}
				if count > 0 {
					a.msgIdx += 5
					if a.msgIdx >= count {
						a.msgIdx = count - 1
					}
					ensureVisible(&a.chatList, a.msgIdx, count)
				}
				a.mu.Unlock()
			case a.focusOnMessages && !gtx.Source.Focused(&a.input) && ke.Name == "B" && ke.Modifiers.Contain(key.ModCtrl):
				a.mu.Lock()
				count := 0
				if a.current != nil {
					count = len(a.current.Messages)
					if a.streaming || a.streamBuf.Len() > 0 {
						count++
					}
					if a.pendingErr != "" {
						count++
					}
				}
				if count > 0 {
					a.msgIdx -= 5
					if a.msgIdx < 0 {
						a.msgIdx = 0
					}
					ensureVisible(&a.chatList, a.msgIdx, count)
				}
				a.mu.Unlock()
			case a.focusOnMessages && !gtx.Source.Focused(&a.input) && ke.Name == "Y":
				a.mu.Lock()
				if a.current != nil && a.msgIdx >= 0 && a.msgIdx < len(a.current.Messages) {
					content := a.current.Messages[a.msgIdx].Content
					gtx.Execute(clipboard.WriteCmd{Data: io.NopCloser(strings.NewReader(content))})
				}
				a.mu.Unlock()
			case !gtx.Source.Focused(&a.input) && ke.Name == "I":
				a.focusOnMessages = false
				a.focusOnSidebar = false
				gtx.Execute(key.FocusCmd{Tag: &a.input})
			}
		}
	}
	// Tell Gio we want focus on a stable area to receive shortcuts.
	event.Op(gtx.Ops, a)
}

func (a *App) handleClicks(gtx layout.Context) {
	if a.newBtn.Clicked(gtx) {
		a.newChat()
	}
	if a.modelBtn.Clicked(gtx) {
		a.showModelPicker = !a.showModelPicker
		a.showSkillPicker = false
		a.showProvPicker = false
		a.pickerIdx = 0
		a.lastPickerIdx = -1
	}
	if a.skillBtn.Clicked(gtx) {
		a.showSkillPicker = !a.showSkillPicker
		a.showModelPicker = false
		a.showProvPicker = false
		a.pickerIdx = 0
		a.lastPickerIdx = -1
	}
	if a.provBtn.Clicked(gtx) {
		a.showProvPicker = !a.showProvPicker
		a.showModelPicker = false
		a.showSkillPicker = false
		a.pickerIdx = 0
		a.lastPickerIdx = -1
	}
	for id, c := range a.convClicks {
		if c.Clicked(gtx) {
			a.openConv(id)
		}
	}
	for id, c := range a.convDelete {
		if c.Clicked(gtx) {
			a.convToDelete = id
			a.showDeleteConfirm = true
		}
	}
	for i, c := range a.modelChoices {
		if c == nil {
			continue
		}
		if c.Clicked(gtx) {
			if i < len(a.allModels) {
				entry := a.allModels[i]
				fmt.Printf("info: clicked model %s for provider %s\n", entry.name, a.providers[entry.provIdx].Name())
				a.provIdx = entry.provIdx
				a.providers[a.provIdx].SetModel(entry.name)
				a.saveConfig()
				a.showModelPicker = false
			}
		}
	}
	for i, c := range a.skillChoices {
		if c == nil {
			continue
		}
		if c.Clicked(gtx) {
			if i == 0 {
				a.activeSkill = ""
			} else if i-1 < len(a.skills) {
				a.activeSkill = a.skills[i-1].Mode
			}
			a.showSkillPicker = false
		}
	}
	for i, c := range a.provChoices {
		if c == nil {
			continue
		}
		if c.Clicked(gtx) {
			if i < len(a.providers) {
				a.provIdx = i
				a.saveConfig()
			}
			a.showProvPicker = false
		}
	}
}

// ---------- chat operations ----------

func (a *App) newChat() {
	a.cancelStream()
	a.mu.Lock()
	a.current = nil
	a.streamBuf.Reset()
	a.pendingErr = ""
	a.mu.Unlock()
	a.input.SetText("")
}

func (a *App) openConv(id string) {
	a.cancelStream()
	conv, err := a.store.Load(id)
	if err != nil {
		fmt.Printf("error: failed to load conversation %s: %v\n", id, err)
		a.mu.Lock()
		a.pendingErr = err.Error()
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	a.current = &conv
	a.streamBuf.Reset()
	a.pendingErr = ""
	a.mu.Unlock()
}

func (a *App) deleteConv(id string) {
	_ = a.store.Delete(id)
	a.mu.Lock()
	if a.current != nil && a.current.ID == id {
		a.current = nil
	}
	a.mu.Unlock()
	a.refreshConvs()
}

func (a *App) cancelStream() {
	a.mu.Lock()
	cancel := a.streamCancel
	a.streamCancel = nil
	a.streaming = false
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) send() {
	text := strings.TrimSpace(a.input.Text())
	if text == "" {
		return
	}
	a.mu.Lock()
	if a.streaming {
		a.mu.Unlock()
		return
	}
	a.input.SetText("")

	// Resolve effective prompt (skills wrap user input).
	prompt := text
	if a.activeSkill != "" {
		for _, sk := range a.skills {
			if sk.Mode == a.activeSkill && strings.Contains(sk.Prompt, "{{.Input}}") {
				prompt = strings.ReplaceAll(sk.Prompt, "{{.Input}}", text)
				break
			}
		}
	}

	if a.current == nil {
		title := text
		if len(title) > 60 {
			title = title[:60]
		}
		c := conversation.New(title)
		a.current = &c
	}
	a.current.Messages = append(a.current.Messages, conversation.Message{
		Role:    "user",
		Content: text,
	})
	if err := a.store.Save(*a.current); err != nil {
		fmt.Printf("error: failed to save conversation: %v\n", err)
		a.pendingErr = err.Error()
	}

	convCopy := *a.current
	if prompt != text {
		msgs := make([]conversation.Message, len(convCopy.Messages))
		copy(msgs, convCopy.Messages)
		msgs[len(msgs)-1].Content = prompt
		convCopy.Messages = msgs
	}
	a.streamBuf.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	a.streamCancel = cancel
	a.streaming = true

	// Set msgIdx to the upcoming assistant message so we follow it.
	a.msgIdx = len(a.current.Messages)
	a.focusOnMessages = true

	prov := a.providers[a.provIdx]
	model := prov.GetModel()
	a.mu.Unlock()
	a.refreshConvs()

	go a.streamLoop(ctx, prov, model, convCopy)
}

func (a *App) streamLoop(ctx context.Context, prov provider.Provider, model string, conv conversation.Conversation) {
	defer func() {
		a.mu.Lock()
		a.streaming = false
		a.streamCancel = nil
		a.mu.Unlock()
		if a.window != nil {
			a.window.Invalidate()
		}
	}()

	ch := prov.StreamChat(ctx, conv.Messages)
	ticker := time.NewTicker(33 * time.Millisecond) // ~30fps invalidation while streaming
	defer ticker.Stop()
	dirty := false

	flush := func() {
		if a.window != nil {
			a.window.Invalidate()
		}
	}

	for {
		select {
		case <-ticker.C:
			if dirty {
				flush()
				dirty = false
			}
		case ev, ok := <-ch:
			if !ok {
				a.finalizeStream(model, "")
				return
			}
			if ev.Err != nil {
				fmt.Printf("error: stream failure: %v\n", ev.Err)
				a.finalizeStream(model, ev.Err.Error())
				return
			}
			if ev.Done {
				a.finalizeStream(model, "")
				return
			}
			if ev.Reasoning {
				continue // hide reasoning in this minimal GUI
			}
			a.mu.Lock()
			a.streamBuf.WriteString(ev.Token)
			a.mu.Unlock()
			dirty = true
		case <-ctx.Done():
			a.finalizeStream(model, "")
			return
		}
	}
}

func (a *App) finalizeStream(model, errMsg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil {
		return
	}
	content := a.streamBuf.String()
	a.streamBuf.Reset()
	if content != "" {
		a.current.Messages = append(a.current.Messages, conversation.Message{
			Role:    "assistant",
			Content: content,
			Model:   model,
		})
		a.msgIdx = len(a.current.Messages) - 1
		_ = a.store.Save(*a.current)
	}
	if errMsg != "" {
		a.pendingErr = errMsg
	}
	a.refreshConvs()
}

func (a *App) layout(gtx layout.Context) {
	if !a.focused {
		gtx.Execute(key.FocusCmd{Tag: &a.input})
		a.focused = true
	}

	if a.focusOnMessages || a.focusOnSidebar || a.showModelPicker || a.showSkillPicker || a.settingsOpen {
		gtx.Execute(key.FocusCmd{Tag: a})
	} else if !gtx.Source.Focused(&a.input) {
		gtx.Execute(key.FocusCmd{Tag: &a.input})
	}

	a.theme.Mat.Palette.Bg = a.theme.Pal.Bg
	a.theme.Mat.Palette.Fg = a.theme.Pal.Text

	// Global background fill.
	paint.Fill(gtx.Ops, a.theme.Pal.Bg)

	if a.settingsOpen {
		a.settings.Layout(gtx)
		return
	}

	layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(280)
			gtx.Constraints.Max.X = gtx.Dp(280)
			return a.layoutSidebar(gtx)
		}),
		layout.Flexed(1, a.layoutChat),
	)

	if a.showModelPicker {
		a.layoutModelPicker(gtx)
	}
	if a.showSkillPicker {
		a.layoutSkillPicker(gtx)
	}
	if a.showProvPicker {
		a.layoutProvPicker(gtx)
	}
	if a.showDeleteConfirm {
		a.layoutDeleteConfirm(gtx)
	}
}

func (a *App) layoutDeleteConfirm(gtx layout.Context) {
	// Dim the background.
	paint.ColorOp{Color: color.NRGBA{A: 0x80}}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	w := gtx.Dp(320)
	h := gtx.Dp(140)
	x := (gtx.Constraints.Max.X - w) / 2
	y := (gtx.Constraints.Max.Y - h) / 2

	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()

	withBorder(gtx, a.theme.Pal.Border, borders{Top: true, Bottom: true, Left: true, Right: true}, func(gtx layout.Context) layout.Dimensions {
		return paintedBg(gtx, a.theme.Pal.BgHeader, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(w, h)
			gtx.Constraints.Max = image.Pt(w, h)
			return layout.Inset{Top: 24, Bottom: 24, Left: 24, Right: 24}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Body1(a.theme.Mat, "Delete this conversation?")
						lbl.Color = a.theme.Pal.TextStrong
						lbl.Font.Weight = font.Bold
						a.theme.applyFont(&lbl, a.theme.Fonts.Global)
						return lbl.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: 20}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(a.theme.Mat, "PRESS ENTER TO CONFIRM  ·  ESC TO CANCEL")
						lbl.Color = a.theme.Pal.TextMuted
						a.theme.applyFont(&lbl, a.theme.Fonts.Global)
						return lbl.Layout(gtx)
					}),
				)
			})
		})
	})
}

func (a *App) layoutTopBar(gtx layout.Context) layout.Dimensions {
	return withBorder(gtx, a.theme.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return paintedBg(gtx, a.theme.Pal.BgHeader, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = gtx.Dp(52)
			gtx.Constraints.Max.Y = gtx.Dp(52)
			prov := a.providers[a.provIdx]

			a.mu.Lock()
			streaming := a.streaming
			title, titleColor, titleStrong := "", a.theme.Pal.TextMuted, false
			if a.current != nil && strings.TrimSpace(a.current.Title) != "" {
				title = a.current.Title
				titleColor = a.theme.Pal.TextStrong
				titleStrong = true
			}
			a.mu.Unlock()

			return layout.Inset{Left: 20, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(topBarStatusDot(a.theme, streaming)),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(topBarLabel(a.theme, nil, title, titleColor, titleStrong)),
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(topBarLabel(a.theme, &a.provBtn, strings.ToLower(prov.Name()), a.theme.Pal.Accent, false)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(a.theme.Mat, " / ")
						lbl.Color = a.theme.Pal.Border
						return lbl.Layout(gtx)
					}),
					layout.Rigid(topBarLabel(a.theme, &a.modelBtn, prov.GetModel(), a.theme.Pal.Text, false)),
					layout.Rigid(topBarSeparator(a.theme)),
					layout.Rigid(topBarLabel(a.theme, &a.skillBtn, "skill: "+skillLabel(a), a.theme.Pal.TextMuted, false)),
				)
			})
		})
	})
}

func topBarStatusDot(th *Theme, on bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		d := gtx.Dp(8)
		sz := image.Pt(d, d)
		rr := d / 2
		defer clip.RRect{Rect: image.Rectangle{Max: sz}, NE: rr, NW: rr, SE: rr, SW: rr}.Push(gtx.Ops).Pop()
		col := th.Pal.Border
		if on {
			col = th.Pal.Accent
		}
		paint.ColorOp{Color: col}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		return layout.Dimensions{Size: sz}
	}
}

func topBarLabel(th *Theme, c *widget.Clickable, text string, col color.NRGBA, strong bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if c == nil {
			lbl := material.Body2(th.Mat, text)
			lbl.Color = col
			if strong {
				lbl.Font.Weight = font.Medium
			}
			th.applyFont(&lbl, th.Fonts.Header)
			return lbl.Layout(gtx)
		}
		return material.ButtonLayoutStyle{
			Background: color.NRGBA{},
			Button:     c,
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			fg := col
			if c.Hovered() {
				fg = th.Pal.TextStrong
			}
			lbl := material.Body2(th.Mat, text)
			lbl.Color = fg
			if strong {
				lbl.Font.Weight = font.Medium
			}
			th.applyFont(&lbl, th.Fonts.Header)
			return lbl.Layout(gtx)
		})
	}
}

func topBarSeparator(th *Theme) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(14)
			rect := clip.Rect{Max: image.Pt(gtx.Dp(1), h)}.Op()
			paint.FillShape(gtx.Ops, th.Pal.Border, rect)
			return layout.Dimensions{Size: image.Pt(gtx.Dp(1), h)}
		})
	}
}

func skillLabel(a *App) string {
	if a.activeSkill == "" {
		return "none"
	}
	for _, s := range a.skills {
		if s.Mode == a.activeSkill {
			return s.Title
		}
	}
	return a.activeSkill
}

func pillButton(th *Theme, c *widget.Clickable, label string, accent bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		bg, fg := th.Pal.BgRowAlt, th.Pal.Text
		if accent {
			bg, fg = th.Pal.Accent, th.Pal.AccentText
		}
		return material.ButtonLayoutStyle{
			Background:   bg,
			CornerRadius: 4,
			Button:       c,
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					lbl := material.Caption(th.Mat, label)
					lbl.Color = fg
					th.applyFont(&lbl, th.Fonts.Global)
					return lbl.Layout(gtx)
				})
		})
	}
}

func (a *App) layoutSidebar(gtx layout.Context) layout.Dimensions {
	return withBorder(gtx, a.theme.Pal.Border, borders{Right: true}, func(gtx layout.Context) layout.Dimensions {
		return paintedBg(gtx, a.theme.Pal.BgSidebar, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 16, Bottom: 8, Left: 16, Right: 12}.Layout(gtx,
						func(gtx layout.Context) layout.Dimensions {
							lbl := material.Caption(a.theme.Mat, "CONVERSATIONS")
							lbl.Color = a.theme.Pal.TextMuted
							lbl.Font.Weight = font.Bold
							a.theme.applyFont(&lbl, a.theme.Fonts.Sidebar)
							return lbl.Layout(gtx)
						})
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return material.List(a.theme.Mat, &a.convList).Layout(gtx, len(a.convs),
						func(gtx layout.Context, i int) layout.Dimensions {
							return a.layoutConvRow(gtx, a.convs[i])
						})
				}),
			)
		})
	})
}

func (a *App) layoutConvRow(gtx layout.Context, conv conversation.Conversation) layout.Dimensions {
	click, ok := a.convClicks[conv.ID]
	if !ok {
		click = &widget.Clickable{}
		a.convClicks[conv.ID] = click
	}
	del, ok := a.convDelete[conv.ID]
	if !ok {
		del = &widget.Clickable{}
		a.convDelete[conv.ID] = del
	}

	selected := a.current != nil && a.current.ID == conv.ID
	titleColor := a.theme.Pal.Text
	rowBg := color.NRGBA{}
	if selected {
		titleColor = a.theme.Pal.TextStrong
		rowBg = a.theme.Pal.BgRowSelected
	}

	return layout.Inset{Left: 6, Right: 6, Top: 1, Bottom: 1}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			// Determine the size of the content first
			m := op.Record(gtx.Ops)
			dims := material.ButtonLayoutStyle{
				Background:   rowBg,
				CornerRadius: 4,
				Button:       click,
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 8, Bottom: 8, Left: 14, Right: 6}.Layout(gtx,
					func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										title := conv.Title
										if title == "" {
											title = "(untitled)"
										}
										lbl := material.Body2(a.theme.Mat, truncate(title, 40))
										lbl.Color = titleColor
										a.theme.applyFont(&lbl, a.theme.Fonts.Sidebar)
										return lbl.Layout(gtx)
									}),
									layout.Rigid(layout.Spacer{Height: 2}.Layout),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										lbl := material.Caption(a.theme.Mat, conv.CreatedAt.Local().Format("Jan 2 15:04"))
										lbl.Color = a.theme.Pal.TextMuted
										a.theme.applyFont(&lbl, a.theme.Fonts.Sidebar)
										return lbl.Layout(gtx)
									}),
								)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return material.ButtonLayoutStyle{
									Background:   color.NRGBA{},
									CornerRadius: 4,
									Button:       del,
								}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}.Layout(gtx,
										func(gtx layout.Context) layout.Dimensions {
											lbl := material.Caption(a.theme.Mat, "✕")
											lbl.Color = a.theme.Pal.TextMuted
											a.theme.applyFont(&lbl, a.theme.Fonts.Sidebar)
											return lbl.Layout(gtx)
										})
								})
							}),
						)
					})
			})
			call := m.Stop()
			call.Add(gtx.Ops)

			// Draw the left-edge selection indicator on top of the row
			// background so it remains visible when the row is selected.
			if selected {
				r := image.Rect(0, 4, gtx.Dp(2), dims.Size.Y-4)
				paint.FillShape(gtx.Ops, a.theme.Pal.Accent, clip.Rect(r).Op())
			}

			return dims
		})
}

// ensureVisible scrolls list so item idx is fully on-screen, without snapping
// it to the top when it's already visible.
//
// Position.Count counts items that are at least partially visible, so the
// first/last visible rows can be clipped — Offset (top-clip, >0) and
// OffsetLast (bottom-clip, <0) tell us by how much. When scrolling forward,
// we bump Offset by `(idx - last)*avgItem - OffsetLast`, which (assuming
// near-uniform item heights) lands idx with its bottom flush against the
// viewport bottom on the next layout pass.
func ensureVisible(list *widget.List, idx, listLen int) {
	pos := list.Position
	if pos.Count == 0 || listLen == 0 {
		list.Position.First = idx
		list.Position.Offset = 0
		return
	}
	last := pos.First + pos.Count - 1
	switch {
	case idx < pos.First:
		list.Position.First = idx
		list.Position.Offset = 0
	case idx == pos.First && pos.Offset > 0:
		list.Position.Offset = 0
	case idx > last, idx == last && pos.OffsetLast < 0:
		itemH := 100 // Reasonable default for chat messages
		if pos.Length > 0 && listLen > 0 {
			itemH = pos.Length / listLen
			if itemH < 1 {
				itemH = 1
			}
		}
		list.Position.Offset += (idx-last)*itemH - pos.OffsetLast
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (a *App) layoutChat(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.layoutTopBar),
		layout.Flexed(1, a.layoutMessages),
		layout.Rigid(a.layoutInput),
	)
}

func (a *App) layoutMessages(gtx layout.Context) layout.Dimensions {
	// Fill background for the chat area.
	return paintedBg(gtx, a.theme.Pal.Bg, func(gtx layout.Context) layout.Dimensions {
		a.mu.Lock()
		var msgs []conversation.Message
		if a.current != nil {
			msgs = append(msgs, a.current.Messages...)
		}
		streaming := a.streaming
		streamText := a.streamBuf.String()
		errMsg := a.pendingErr
		a.mu.Unlock()

		count := len(msgs)
		if streaming || streamText != "" {
			count++
		}
		if errMsg != "" {
			count++
		}

		if count == 0 {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.H6(a.theme.Mat, "Start a conversation")
						lbl.Color = a.theme.Pal.Text
						a.theme.applyFont(&lbl, a.theme.Fonts.Global)
						return lbl.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: 10}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(a.theme.Mat, "Ctrl+Enter send  ·  Ctrl+M model  ·  Ctrl+K skill  ·  Ctrl+N new")
						lbl.Color = a.theme.Pal.TextMuted
						a.theme.applyFont(&lbl, a.theme.Fonts.Global)
						return lbl.Layout(gtx)
					}),
				)
			})
		} else {
			if a.focusOnMessages {
				ensureVisible(&a.chatList, a.msgIdx, count)
			}
			return material.List(a.theme.Mat, &a.chatList).Layout(gtx, count,
				func(gtx layout.Context, i int) layout.Dimensions {
					if i < len(msgs) {
						ts := a.current.CreatedAt // fallback
						return a.messageRow(gtx, i, msgs[i].Role, msgs[i].Content, msgs[i].Model, ts)
					}
					if i == len(msgs) && (streaming || streamText != "") {
						txt := streamText
						if txt == "" {
							txt = "…"
						}
						return a.messageRow(gtx, i, "assistant", txt, a.providers[a.provIdx].GetModel(), time.Now())
					}
					return layout.Inset{Top: 6, Bottom: 6, Left: 24, Right: 24}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Body2(a.theme.Mat, "error: "+errMsg)
						lbl.Color = a.theme.Pal.Danger
						a.theme.applyFont(&lbl, a.theme.Fonts.Global)
						return lbl.Layout(gtx)
					})
				})
		}
	})
}

func (a *App) layoutInput(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: 14, Bottom: 14, Left: 24, Right: 24}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		ed := material.Editor(a.theme.Mat, &a.input, "Type a message…")
		ed.Color = a.theme.Pal.TextStrong
		ed.HintColor = a.theme.Pal.TextMuted

		a.theme.applyFontToEditor(&ed, a.theme.Fonts.Input)

		return ed.Layout(gtx)
	})
}

func (a *App) layoutModelPicker(gtx layout.Context) {
	if len(a.modelChoices) != len(a.allModels) {
		a.modelChoices = make([]*widget.Clickable, len(a.allModels))
		for i := range a.modelChoices {
			a.modelChoices[i] = &widget.Clickable{}
		}
	}
	if a.showModelPicker {
		fmt.Printf("debug: rendering model picker with %d models\n", len(a.allModels))
	}

	// Dim the background to focus the modal.
	paint.ColorOp{Color: color.NRGBA{A: 0x80}}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	w := gtx.Dp(520) // Slightly wider for provider names
	h := gtx.Constraints.Max.Y - gtx.Dp(80)
	if h > gtx.Dp(600) {
		h = gtx.Dp(600)
	}

	x := (gtx.Constraints.Max.X - w) / 2
	y := (gtx.Constraints.Max.Y - h) / 2

	// Ensure the selected item is visible if we just navigated.
	if a.pickerIdx != a.lastPickerIdx {
		if a.lastPickerIdx == -1 {
			a.popupList.Position.First = 0
			a.popupList.Position.Offset = 0
		}
		listH := h - gtx.Dp(56)
		visCount := listH / gtx.Dp(40)
		if visCount < 1 {
			visCount = 1
		}

		if a.pickerIdx < a.popupList.Position.First {
			a.popupList.Position.First = a.pickerIdx
			a.popupList.Position.Offset = 0
		} else if a.pickerIdx >= a.popupList.Position.First+visCount {
			a.popupList.Position.First = a.pickerIdx - visCount + 1
			a.popupList.Position.Offset = 0
		}
		a.lastPickerIdx = a.pickerIdx
	}

	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()

	withBorder(gtx, a.theme.Pal.Border, borders{Top: true, Bottom: true, Left: true, Right: true}, func(gtx layout.Context) layout.Dimensions {
		return paintedBg(gtx, a.theme.Pal.BgHeader, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(w, h)
			gtx.Constraints.Max = image.Pt(w, h)
			return layout.Inset{Top: 14, Bottom: 12, Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Caption(a.theme.Mat, "SELECT MODEL")
							lbl.Color = a.theme.Pal.TextMuted
							lbl.Font.Weight = font.Bold
							a.theme.applyFont(&lbl, a.theme.Fonts.Global)
							return lbl.Layout(gtx)
						})
					}),
					layout.Rigid(layout.Spacer{Height: 6}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						a.popupList.Axis = layout.Vertical
						return material.List(a.theme.Mat, &a.popupList).Layout(gtx, len(a.allModels), func(gtx layout.Context, i int) layout.Dimensions {
							entry := a.allModels[i]
							isHeader := i == 0 || a.allModels[i-1].provIdx != entry.provIdx

							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									if !isHeader {
										return layout.Dimensions{}
									}
									return layout.Inset{Top: 12, Bottom: 4, Left: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										lbl := material.Caption(a.theme.Mat, strings.ToUpper(a.providers[entry.provIdx].Name()))
										lbl.Color = a.theme.Pal.Accent
										lbl.Font.Weight = font.Bold
										a.theme.applyFont(&lbl, a.theme.Fonts.Global)
										return lbl.Layout(gtx)
									})
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									bg := color.NRGBA{}
									if a.pickerIdx == i {
										bg = a.theme.Pal.BgRowSelected
									}

									return layout.Inset{Top: 1, Bottom: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
										return material.ButtonLayoutStyle{
											Background:   bg,
											CornerRadius: 4,
											Button:       a.modelChoices[i],
										}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
											return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}.Layout(gtx,
												func(gtx layout.Context) layout.Dimensions {
													return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
														layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
															lbl := material.Body2(a.theme.Mat, entry.name)
															lbl.Color = a.theme.Pal.Text
															if a.provIdx == entry.provIdx && a.providers[a.provIdx].GetModel() == entry.name {
																lbl.Font.Weight = font.Bold
																lbl.Color = a.theme.Pal.TextStrong
															}
															a.theme.applyFont(&lbl, a.theme.Fonts.Global)
															return lbl.Layout(gtx)
														}),
														layout.Rigid(func(gtx layout.Context) layout.Dimensions {
															if a.provIdx == entry.provIdx && a.providers[a.provIdx].GetModel() == entry.name {
																lbl := material.Caption(a.theme.Mat, "ACTIVE")
																lbl.Color = a.theme.Pal.Accent
																a.theme.applyFont(&lbl, a.theme.Fonts.Global)
																return lbl.Layout(gtx)
															}
															return layout.Dimensions{}
														}),
													)
												})
										})
									})
								}),
							)
						})
					}),
				)
			})
		})
	})
}

func (a *App) layoutProvPicker(gtx layout.Context) {
	var providers []string
	for _, p := range a.providers {
		providers = append(providers, p.Name())
	}
	if len(a.provChoices) != len(providers) {
		a.provChoices = make([]*widget.Clickable, len(providers))
		for i := range a.provChoices {
			a.provChoices[i] = &widget.Clickable{}
		}
	}
	if a.showProvPicker {
		fmt.Printf("debug: rendering provider picker with %d providers: %v\n", len(providers), providers)
	}
	a.popupList.Axis = layout.Vertical
	a.layoutPopup(gtx, "select provider", providers, a.provChoices)
}

func (a *App) layoutSkillPicker(gtx layout.Context) {
	options := []string{"(none)"}
	for _, s := range a.skills {
		options = append(options, s.Title+"  ["+s.Mode+"]")
	}
	if len(a.skills) == 0 {
		options = append(options, fmt.Sprintf("define skills in %s/config.json", a.store_dirHint()))
	}
	if len(a.skillChoices) != len(options) {
		a.skillChoices = make([]*widget.Clickable, len(options))
		for i := range a.skillChoices {
			a.skillChoices[i] = &widget.Clickable{}
		}
	}
	a.popupList.Axis = layout.Vertical
	a.layoutPopup(gtx, "select skill", options, a.skillChoices)
}

func (a *App) store_dirHint() string {
	return a.store.ConfigDir()
}

func (a *App) layoutPopup(gtx layout.Context, title string, items []string, clicks []*widget.Clickable) {
	// Dim the background to focus the modal.
	paint.ColorOp{Color: color.NRGBA{A: 0x80}}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	w := gtx.Dp(420)
	h := gtx.Dp(56) + gtx.Dp(40)*len(items)
	if maxH := gtx.Constraints.Max.Y - gtx.Dp(80); h > maxH {
		h = maxH
	}
	x := (gtx.Constraints.Max.X - w) / 2
	y := (gtx.Constraints.Max.Y - h) / 2

	// Ensure the selected item is visible if we just navigated.
	if a.pickerIdx != a.lastPickerIdx {
		if a.lastPickerIdx == -1 {
			a.popupList.Position.First = 0
			a.popupList.Position.Offset = 0
		}
		listH := h - gtx.Dp(56) // 56 is header + spacers
		visCount := listH / gtx.Dp(40)
		if visCount < 1 {
			visCount = 1
		}

		if a.pickerIdx < a.popupList.Position.First {
			a.popupList.Position.First = a.pickerIdx
			a.popupList.Position.Offset = 0
		} else if a.pickerIdx >= a.popupList.Position.First+visCount {
			a.popupList.Position.First = a.pickerIdx - visCount + 1
			a.popupList.Position.Offset = 0
		}
		a.lastPickerIdx = a.pickerIdx
	}

	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()

	withBorder(gtx, a.theme.Pal.Border, borders{Top: true, Bottom: true, Left: true, Right: true}, func(gtx layout.Context) layout.Dimensions {
		return paintedBg(gtx, a.theme.Pal.BgHeader, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(w, h)
			gtx.Constraints.Max = image.Pt(w, h)
			return layout.Inset{Top: 14, Bottom: 12, Left: 14, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: 4, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Caption(a.theme.Mat, strings.ToUpper(title))
							lbl.Color = a.theme.Pal.TextMuted
							lbl.Font.Weight = font.Bold
							a.theme.applyFont(&lbl, a.theme.Fonts.Global)
							return lbl.Layout(gtx)
						})
					}),
					layout.Rigid(layout.Spacer{Height: 6}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return material.List(a.theme.Mat, &a.popupList).Layout(gtx, len(items), func(gtx layout.Context, i int) layout.Dimensions {
							return layout.Inset{Top: 1, Bottom: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								var c *widget.Clickable
								if i < len(clicks) {
									c = clicks[i]
								} else {
									c = &widget.Clickable{}
								}

								bg := color.NRGBA{}
								if a.pickerIdx == i {
									bg = a.theme.Pal.BgRowSelected
								}

								return material.ButtonLayoutStyle{
									Background:   bg,
									CornerRadius: 4,
									Button:       c,
								}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}.Layout(gtx,
										func(gtx layout.Context) layout.Dimensions {
											gtx.Constraints.Min.X = gtx.Constraints.Max.X
											lbl := material.Body2(a.theme.Mat, items[i])
											lbl.Color = a.theme.Pal.Text
											a.theme.applyFont(&lbl, a.theme.Fonts.Global)
											return lbl.Layout(gtx)
										})
								})
							})
						})
					}),
				)
			})
		})
	})
}
