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
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/io/transfer"
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

	// Cached picker option labels (built once; skills/providers don't change
	// at runtime, so we don't rebuild per-frame).
	skillOptions []string
	provOptions  []string

	// Async UI invalidation.
	window *app.Window

	// saveCh feeds a background goroutine that persists conversations off the
	// UI goroutine. The channel has buffer 1 and is overwritten under
	// saveMu — older pending saves are dropped (only the latest state of a
	// conversation matters).
	saveCh   chan conversation.Conversation
	saveMu   sync.Mutex
	saveDone chan struct{}

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

		a.theme.Fonts.Global = FontStyle{Face: cfg.Fonts.Global.Face, Size: cfg.Fonts.Global.Size}
		a.theme.Fonts.Sidebar = FontStyle{Face: cfg.Fonts.Sidebar.Face, Size: cfg.Fonts.Sidebar.Size}
		a.theme.Fonts.Header = FontStyle{Face: cfg.Fonts.Header.Face, Size: cfg.Fonts.Header.Size}
		a.theme.Fonts.Messages = FontStyle{Face: cfg.Fonts.Messages.Face, Size: cfg.Fonts.Messages.Size}
		a.theme.Fonts.Input = FontStyle{Face: cfg.Fonts.Input.Face, Size: cfg.Fonts.Input.Size}

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
	a.rebuildSkillOptions()
	a.rebuildProvOptions()
	a.refreshConvs()

	a.saveCh = make(chan conversation.Conversation, 1)
	a.saveDone = make(chan struct{})
	go a.saveLoop()

	return a
}

func (a *App) shutdown() {
	a.saveMu.Lock()
	if a.saveCh != nil {
		close(a.saveCh)
		a.saveCh = nil
	}
	a.saveMu.Unlock()
	<-a.saveDone
}

// queueSave enqueues a conversation snapshot for async persistence,
// coalescing with any pending save (only the most recent state is kept).
func (a *App) queueSave(conv conversation.Conversation) {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	if a.saveCh == nil {
		return // shutdown in progress
	}
	select {
	case <-a.saveCh:
	default:
	}
	a.saveCh <- conv
}

func (a *App) saveLoop() {
	defer close(a.saveDone)
	for conv := range a.saveCh {
		if err := a.store.Save(conv); err != nil {
			fmt.Printf("error: failed to save conversation: %v\n", err)
		}
	}
}

func (a *App) rebuildSkillOptions() {
	opts := make([]string, 0, len(a.skills)+1)
	opts = append(opts, "(none)")
	for _, s := range a.skills {
		opts = append(opts, s.Title+"  ["+s.Mode+"]")
	}
	if len(a.skills) == 0 {
		opts = append(opts, fmt.Sprintf("define skills in %s/config.json", a.store.ConfigDir()))
	}
	a.skillOptions = opts
}

func (a *App) rebuildProvOptions() {
	opts := make([]string, len(a.providers))
	for i, p := range a.providers {
		opts[i] = p.Name()
	}
	a.provOptions = opts
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

// messageCount returns the number of rows that would be rendered in the chat
// list (real messages + an in-flight streaming row + a pending-error row).
// Caller must hold a.mu.
func (a *App) messageCount() int {
	if a.current == nil {
		return 0
	}
	count := len(a.current.Messages)
	if a.streaming || a.streamBuf.Len() > 0 {
		count++
	}
	if a.pendingErr != "" {
		count++
	}
	return count
}

func (a *App) refreshConvs() {
	convs, err := a.store.ListMeta()
	if err != nil {
		return
	}
	// SQLite ListMeta already returns ORDER BY created_at DESC.
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
			a.shutdown()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.handleKeys(gtx)
			a.handleClicks(gtx)
			a.layout(gtx)
			e.Frame(gtx.Ops)
		case transfer.DataEvent:
			if rc := e.Open(); rc != nil {
				data, _ := io.ReadAll(rc)
				rc.Close()
				a.input.Insert(string(data))
				w.Invalidate()
			}
		}
	}
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
			a.focusOnMessages = false
			a.focusOnSidebar = false
			a.msgIdx = -1
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
			oldSkill := a.activeSkill
			if i == 0 {
				a.activeSkill = ""
			} else if i-1 < len(a.skills) {
				a.activeSkill = a.skills[i-1].Mode
			}
			if a.activeSkill != oldSkill {
				a.newChat()
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
	a.focusOnMessages = false
	a.focusOnSidebar = false
	a.msgIdx = -1
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
	delete(a.convClicks, id)
	delete(a.convDelete, id)
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

	newConv := false
	if a.current == nil {
		title := text
		if len(title) > 60 {
			title = title[:60]
		}
		c := conversation.New(title)
		a.current = &c
		newConv = true
	}
	a.current.Messages = append(a.current.Messages, conversation.Message{
		Role:    "user",
		Content: text,
	})
	saveSnapshot := *a.current

	convCopy := *a.current
	convCopy.Messages = truncateHistory(convCopy.Messages, maxProviderMessages)
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

	a.msgIdx = len(a.current.Messages)
	a.focusOnMessages = true

	prov := a.providers[a.provIdx]
	model := prov.GetModel()
	a.mu.Unlock()

	a.queueSave(saveSnapshot)
	if newConv {
		a.refreshConvs()
	}

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
	if a.current == nil {
		a.mu.Unlock()
		return
	}
	content := a.streamBuf.String()
	a.streamBuf.Reset()
	var snapshot conversation.Conversation
	save := false
	if content != "" {
		a.current.Messages = append(a.current.Messages, conversation.Message{
			Role:    "assistant",
			Content: content,
			Model:   model,
		})
		a.msgIdx = len(a.current.Messages) - 1
		snapshot = *a.current
		save = true
	}
	if errMsg != "" {
		a.pendingErr = errMsg
	}
	a.mu.Unlock()
	if save {
		a.queueSave(snapshot)
	}
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
		itemH := 100
		if pos.Length > 0 && listLen > 0 {
			itemH = pos.Length / listLen
			if itemH < 1 {
				itemH = 1
			}
		}
		list.Position.Offset += (idx-last)*itemH - pos.OffsetLast
	}
}

// maxProviderMessages caps how many trailing messages are sent to the
// provider on each turn. The full transcript is still persisted and
// rendered; only the API payload is bounded so token cost stays predictable
// on long conversations.
const maxProviderMessages = 40

// truncateHistory returns the last n messages of msgs, snapped so the slice
// begins with a "user" message (an orphaned "assistant" at the head confuses
// most chat models).
func truncateHistory(msgs []conversation.Message, n int) []conversation.Message {
	if len(msgs) <= n {
		return msgs
	}
	start := len(msgs) - n
	for start < len(msgs) && msgs[start].Role != "user" {
		start++
	}
	if start >= len(msgs) {
		return msgs[len(msgs)-1:]
	}
	return msgs[start:]
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
	return paintedBg(gtx, a.theme.Pal.Bg, func(gtx layout.Context) layout.Dimensions {
		// We can hold the slice header without copying the backing array:
		// existing entries are append-only (never mutated in place), so
		// readers see a stable snapshot even if a concurrent append in
		// finalizeStream reallocates the backing array.
		a.mu.Lock()
		var msgs []conversation.Message
		if a.current != nil {
			msgs = a.current.Messages
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
		}
		if a.focusOnMessages {
			ensureVisible(&a.chatList, a.msgIdx, count)
		}
		return material.List(a.theme.Mat, &a.chatList).Layout(gtx, count,
			func(gtx layout.Context, i int) layout.Dimensions {
				if i < len(msgs) {
					ts := a.current.CreatedAt
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
