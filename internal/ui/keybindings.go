// Package ui — keybindings.go: keyboard shortcut handling.
package ui

import (
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/widget"
)

// pickerMax returns the maximum valid index for whichever picker is open
// (or 0 when none is open). Caller may not need the lock — picker visibility
// flags are written only from the UI goroutine.
func (a *App) pickerMax() int {
	switch {
	case a.showModelPicker:
		return len(a.allModels)
	case a.showSkillPicker:
		return len(a.skills) + 1 // +1 for "(none)"
	case a.showProvPicker:
		return len(a.providers)
	}
	return 0
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
			key.Filter{Name: "Q"},
			key.Filter{Name: key.NameTab},
			key.Filter{Name: key.NameReturn},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		a.dispatchKey(gtx, ke)
	}

	// Tell Gio we want focus on a stable area to receive shortcuts.
	event.Op(gtx.Ops, a)
}

func (a *App) dispatchKey(gtx layout.Context, ke key.Event) {
	pickerOpen := a.showModelPicker || a.showSkillPicker || a.showProvPicker
	inputFocused := gtx.Source.Focused(&a.input)

	switch {
	case ke.Name == key.NameTab:
		a.handleTab(gtx)
	case ke.Name == key.NameReturn && ke.Modifiers.Contain(key.ModCtrl):
		a.send()
	case ke.Name == key.NameEscape || (ke.Name == "[" && ke.Modifiers.Contain(key.ModCtrl)):
		a.handleEscape()
	case ke.Name == ",":
		if !inputFocused {
			a.settingsOpen = !a.settingsOpen
			a.showModelPicker = false
			a.showSkillPicker = false
			a.showProvPicker = false
			a.showDeleteConfirm = false
		}
	case ke.Name == "K" && ke.Modifiers.Contain(key.ModCtrl):
		a.togglePicker(&a.showSkillPicker)
	case ke.Name == "M" && ke.Modifiers.Contain(key.ModCtrl):
		a.togglePicker(&a.showModelPicker)
	case ke.Name == "P" && ke.Modifiers.Contain(key.ModCtrl):
		a.togglePicker(&a.showProvPicker)
	case a.showDeleteConfirm && ke.Name == key.NameReturn:
		a.deleteConv(a.convToDelete)
		a.showDeleteConfirm = false
	case ke.Name == "Q" && !inputFocused && !pickerOpen && !a.settingsOpen && !a.showDeleteConfirm:
		a.window.Perform(system.ActionClose)
	case a.focusOnSidebar && !inputFocused && ke.Name == "D":
		a.mu.Lock()
		if a.convIdx >= 0 && a.convIdx < len(a.convs) {
			a.convToDelete = a.convs[a.convIdx].ID
			a.showDeleteConfirm = true
		}
		a.mu.Unlock()
	case (ke.Name == "W" || ke.Name == key.NameDeleteBackward) && ke.Modifiers.Contain(key.ModCtrl):
		if !pickerOpen && !a.settingsOpen && !a.focusOnMessages {
			DeleteWord(&a.input)
		}
	case ke.Name == "N" && ke.Modifiers.Contain(key.ModCtrl):
		a.newChat()
	case pickerOpen && ke.Name == "J":
		a.movePicker(1)
	case pickerOpen && ke.Name == "K":
		a.movePicker(-1)
	case pickerOpen && ke.Name == "F" && ke.Modifiers.Contain(key.ModCtrl):
		a.movePicker(5)
	case pickerOpen && ke.Name == "B" && ke.Modifiers.Contain(key.ModCtrl):
		a.movePicker(-5)
	case pickerOpen && ke.Name == key.NameReturn:
		a.pickerConfirm(gtx)
	case a.focusOnMessages && !inputFocused && ke.Name == "J":
		a.moveMsgCursor(1)
	case a.focusOnMessages && !inputFocused && ke.Name == "K":
		a.moveMsgCursor(-1)
	case a.focusOnSidebar && !inputFocused && ke.Name == "J":
		a.moveConvCursor(1)
	case a.focusOnSidebar && !inputFocused && ke.Name == "K":
		a.moveConvCursor(-1)
	case a.focusOnMessages && !inputFocused && ke.Name == "H":
		a.focusSidebarFromMessages()
	case a.focusOnSidebar && !inputFocused && ke.Name == "L":
		a.focusMessagesFromSidebar()
	case a.focusOnMessages && !inputFocused && ke.Name == "F" && ke.Modifiers.Contain(key.ModCtrl):
		a.moveMsgCursor(5)
	case a.focusOnMessages && !inputFocused && ke.Name == "B" && ke.Modifiers.Contain(key.ModCtrl):
		a.moveMsgCursor(-5)
	case a.focusOnMessages && !inputFocused && ke.Name == "Y":
		a.yankCurrentMessage(gtx)
	case !inputFocused && ke.Name == "I":
		a.focusOnMessages = false
		a.focusOnSidebar = false
		gtx.Execute(key.FocusCmd{Tag: &a.input})
	}
}

func (a *App) togglePicker(flag *bool) {
	*flag = !*flag
	if flag != &a.showModelPicker {
		a.showModelPicker = false
	}
	if flag != &a.showSkillPicker {
		a.showSkillPicker = false
	}
	if flag != &a.showProvPicker {
		a.showProvPicker = false
	}
	a.showDeleteConfirm = false
	a.pickerIdx = 0
	a.lastPickerIdx = -1
}

func (a *App) handleTab(gtx layout.Context) {
	switch {
	case a.focusOnSidebar:
		a.focusOnSidebar = false
		a.focusOnMessages = true
		a.mu.Lock()
		count := a.messageCount()
		if count > 0 {
			a.msgIdx = count - 1
			ensureVisible(&a.chatList, a.msgIdx, count)
		}
		a.mu.Unlock()
	case a.focusOnMessages:
		a.focusOnMessages = false
		a.focusOnSidebar = false
		gtx.Execute(key.FocusCmd{Tag: &a.input})
	default:
		a.focusOnSidebar = true
		a.focusOnMessages = false
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
}

func (a *App) handleEscape() {
	if a.showModelPicker || a.showSkillPicker || a.showProvPicker || a.settingsOpen || a.showDeleteConfirm {
		a.showModelPicker = false
		a.showSkillPicker = false
		a.showProvPicker = false
		a.settingsOpen = false
		a.showDeleteConfirm = false
		return
	}
	if a.streaming {
		a.cancelStream()
		return
	}
	a.mu.Lock()
	count := a.messageCount()
	if count > 0 {
		a.msgIdx = count - 1
		ensureVisible(&a.chatList, a.msgIdx, count)
		a.focusOnMessages = true
		a.focusOnSidebar = false
	} else {
		a.focusOnMessages = false
		a.focusOnSidebar = true
		if a.convIdx < 0 && len(a.convs) > 0 {
			a.convIdx = 0
		}
	}
	a.mu.Unlock()
}

func (a *App) movePicker(delta int) {
	a.pickerIdx += delta
	if a.pickerIdx < 0 {
		a.pickerIdx = 0
	}
	if max := a.pickerMax(); a.pickerIdx >= max {
		if max <= 0 {
			a.pickerIdx = 0
		} else {
			a.pickerIdx = max - 1
		}
	}
}

func (a *App) pickerConfirm(gtx layout.Context) {
	switch {
	case a.showModelPicker:
		if a.pickerIdx < len(a.allModels) {
			entry := a.allModels[a.pickerIdx]
			a.provIdx = entry.provIdx
			a.providers[a.provIdx].SetModel(entry.name)
			a.saveConfig()
			a.showModelPicker = false
			gtx.Execute(key.FocusCmd{Tag: &a.input})
		}
	case a.showSkillPicker:
		oldSkill := a.activeSkill
		if a.pickerIdx == 0 {
			a.activeSkill = ""
		} else if a.pickerIdx-1 < len(a.skills) {
			a.activeSkill = a.skills[a.pickerIdx-1].Mode
		}
		if a.activeSkill != oldSkill {
			a.newChat()
		}
		a.showSkillPicker = false
		gtx.Execute(key.FocusCmd{Tag: &a.input})
	case a.showProvPicker:
		if a.pickerIdx < len(a.providers) {
			a.provIdx = a.pickerIdx
			a.saveConfig()
			a.showProvPicker = false
			gtx.Execute(key.FocusCmd{Tag: &a.input})
		}
	}
}

func (a *App) moveMsgCursor(delta int) {
	a.mu.Lock()
	count := a.messageCount()
	if count == 0 {
		a.mu.Unlock()
		return
	}
	a.msgIdx += delta
	if a.msgIdx < 0 {
		a.msgIdx = 0
	}
	if a.msgIdx > count-1 {
		a.msgIdx = count - 1
	}
	ensureVisible(&a.chatList, a.msgIdx, count)
	a.mu.Unlock()
}

func (a *App) moveConvCursor(delta int) {
	a.mu.Lock()
	target := a.convIdx + delta
	if target < 0 || target >= len(a.convs) {
		a.mu.Unlock()
		return
	}
	a.convIdx = target
	ensureVisible(&a.convList, target, len(a.convs))
	id := a.convs[target].ID
	a.mu.Unlock()
	a.openConv(id)
}

func (a *App) focusSidebarFromMessages() {
	a.focusOnMessages = false
	a.focusOnSidebar = true
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

func (a *App) focusMessagesFromSidebar() {
	a.focusOnSidebar = false
	a.focusOnMessages = true
	a.mu.Lock()
	count := a.messageCount()
	if count > 0 {
		a.msgIdx = count - 1
		ensureVisible(&a.chatList, a.msgIdx, count)
	}
	a.mu.Unlock()
}

func (a *App) yankCurrentMessage(gtx layout.Context) {
	a.mu.Lock()
	if a.current != nil && a.msgIdx >= 0 && a.msgIdx < len(a.current.Messages) {
		content := a.current.Messages[a.msgIdx].Content
		gtx.Execute(clipboard.WriteCmd{Data: io.NopCloser(strings.NewReader(content))})
	}
	a.mu.Unlock()
}
