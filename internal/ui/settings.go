package ui

import (
	"fmt"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type SettingsScreen struct {
	th       *Theme
	onChange func()
	onClose  func()

	rows []*settingsRow
	list widget.List

	close widget.Clickable
}

type settingsRow struct {
	label   string
	target  *FontStyle
	prevF   widget.Clickable
	nextF   widget.Clickable
	smaller widget.Clickable
	bigger  widget.Clickable
}

func newSettingsScreen(th *Theme, onChange, onClose func()) *SettingsScreen {
	s := &SettingsScreen{th: th, onChange: onChange, onClose: onClose}
	s.list.Axis = layout.Vertical
	s.rows = []*settingsRow{
		{label: "Global Font (Base)", target: &th.Fonts.Global},
		{label: "Sidebar", target: &th.Fonts.Sidebar},
		{label: "Header", target: &th.Fonts.Header},
		{label: "Messages", target: &th.Fonts.Messages},
		{label: "Input", target: &th.Fonts.Input},
	}
	return s
}

func (s *SettingsScreen) cycleFace(r *settingsRow, delta int) {
	options := append([]string{""}, s.th.Faces...)
	idx := 0
	for i, f := range options {
		if f == r.target.Face {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(options)) % len(options)
	r.target.Face = options[idx]
}

func (s *SettingsScreen) bumpSize(r *settingsRow, delta float32) {
	cur := r.target.Size
	if cur == 0 {
		cur = float32(s.th.Mat.TextSize)
	}
	cur += delta
	if cur < 8 {
		cur = 8
	}
	if cur > 32 {
		cur = 32
	}
	r.target.Size = cur
}

func (s *SettingsScreen) Layout(gtx layout.Context) layout.Dimensions {
	th := s.th
	dirty := false

	for _, r := range s.rows {
		if r.prevF.Clicked(gtx) {
			s.cycleFace(r, -1)
			dirty = true
		}
		if r.nextF.Clicked(gtx) {
			s.cycleFace(r, 1)
			dirty = true
		}
		if r.smaller.Clicked(gtx) {
			s.bumpSize(r, -1)
			dirty = true
		}
		if r.bigger.Clicked(gtx) {
			s.bumpSize(r, 1)
			dirty = true
		}
	}
	if s.close.Clicked(gtx) {
		if s.onClose != nil {
			s.onClose()
		}
	}
	if dirty && s.onChange != nil {
		s.onChange()
	}

	return paintedBg(gtx, th.Pal.Bg, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(20),
			Bottom: unit.Dp(20),
			Left:   unit.Dp(28),
			Right:  unit.Dp(28),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutHeader(gtx, th)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return material.List(th.Mat, &s.list).Layout(gtx, len(s.rows), func(gtx layout.Context, i int) layout.Dimensions {
						return s.layoutRow(gtx, th, s.rows[i])
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					hint := material.Caption(th.Mat, "Esc close · changes save automatically")
					hint.Color = th.Pal.TextMuted
					return hint.Layout(gtx)
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutHeader(gtx layout.Context, th *Theme) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.H6(th.Mat, "Settings · Fonts")
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.TextStrong
			lbl.Font.Weight = font.Bold
			return lbl.Layout(gtx)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return s.button(gtx, th, &s.close, "close")
		}),
	)
}

func (s *SettingsScreen) layoutRow(gtx layout.Context, th *Theme, r *settingsRow) layout.Dimensions {
	return withBorder(gtx, th.Pal.Border, borders{Bottom: true}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body1(th.Mat, r.label)
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.TextStrong
					lbl.Font.Weight = font.SemiBold
					return lbl.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutFaceControls(gtx, th, r)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutSizeControls(gtx, th, r)
				}),
			)
		})
	})
}

func (s *SettingsScreen) layoutFaceControls(gtx layout.Context, th *Theme, r *settingsRow) layout.Dimensions {
	face := r.target.Face
	if face == "" {
		face = "default"
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(th.Mat, "Face")
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.TextDim
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(48))
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.prevF, "<") }),
		layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body1(th.Mat, face)
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.Text
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.nextF, ">") }),
	)
}

func (s *SettingsScreen) layoutSizeControls(gtx layout.Context, th *Theme, r *settingsRow) layout.Dimensions {
	size := r.target.Size
	display := fmt.Sprintf("%.0f sp", size)
	if size == 0 {
		display = fmt.Sprintf("default (%d sp)", int(s.th.Mat.TextSize))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(th.Mat, "Size")
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.TextDim
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(48))
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.smaller, "-") }),
		layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body1(th.Mat, display)
			th.applyFont(&lbl, FontStyle{})
			lbl.Color = th.Pal.Text
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return s.button(gtx, th, &r.bigger, "+") }),
	)
}

func (s *SettingsScreen) button(gtx layout.Context, th *Theme, c *widget.Clickable, label string) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return withBorder(gtx, th.Pal.Border, borders{Top: true, Bottom: true, Left: true, Right: true}, func(gtx layout.Context) layout.Dimensions {
			return paintedBg(gtx, th.Pal.BgSidebar, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{
					Top:    unit.Dp(4),
					Bottom: unit.Dp(4),
					Left:   unit.Dp(10),
					Right:  unit.Dp(10),
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body2(th.Mat, label)
					th.applyFont(&lbl, FontStyle{})
					lbl.Color = th.Pal.Text
					return lbl.Layout(gtx)
				})
			})
		})
	})
}
