package ui

import (
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

func (a *App) messageRow(gtx layout.Context, idx int, role, content, model string, ts time.Time) layout.Dimensions {
	name := "User"
	nameColor := a.theme.Pal.Accent
	iconName := "user"
	if role == "assistant" {
		name = "Assistant"
		if model != "" {
			name = model
		}
		nameColor = a.theme.Pal.TextStrong
		iconName = modelIconName(model)
	}

	bg := color.NRGBA{}
	if a.focusOnMessages && a.msgIdx == idx {
		bg = a.theme.Pal.BgRowAlt
	}

	return paintedBg(gtx, bg, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 8, Bottom: 8, Left: 24, Right: 24}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
				// Avatar placeholder
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					sz := gtx.Dp(unit.Dp(36))
					return layout.Stack{}.Layout(gtx,
						layout.Expanded(func(gtx layout.Context) layout.Dimensions {
							rect := clip.RRect{
								Rect: image.Rectangle{Max: image.Pt(sz, sz)},
								SE:   4, SW: 4, NE: 4, NW: 4,
							}
							paint.FillShape(gtx.Ops, a.theme.Pal.BgRowAlt, rect.Op(gtx.Ops))
							return layout.Dimensions{Size: image.Pt(sz, sz)}
						}),
						layout.Stacked(func(gtx layout.Context) layout.Dimensions {
							if role == "user" {
								return layout.Dimensions{Size: image.Pt(sz, sz)}
							}
							gtx.Constraints.Min = image.Pt(sz, sz)
							if op, ok := a.icons.get(iconName); ok {
								return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									gtx.Constraints.Min = image.Pt(gtx.Dp(24), gtx.Dp(24))
									gtx.Constraints.Max = image.Pt(gtx.Dp(24), gtx.Dp(24))
									img := widget.Image{Src: op, Fit: widget.Contain}
									return img.Layout(gtx)
								})
							}
							// Fallback to emoji if icon not found
							return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								emoji := "🤖"
								lbl := material.Body1(a.theme.Mat, emoji)
								return lbl.Layout(gtx)
							})
						}),
					)
				}),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				// Content column
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						// Header: Name + Time
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Body1(a.theme.Mat, name)
									lbl.Color = nameColor
									lbl.Font.Weight = font.Bold
									a.theme.applyFont(&lbl, a.theme.Fonts.Messages)
									return lbl.Layout(gtx)
								}),
								layout.Rigid(layout.Spacer{Width: 8}.Layout),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									lbl := material.Caption(a.theme.Mat, ts.Local().Format("15:04"))
									lbl.Color = a.theme.Pal.TextMuted
									a.theme.applyFont(&lbl, a.theme.Fonts.Messages)
									return lbl.Layout(gtx)
								}),
							)
						}),
						layout.Rigid(layout.Spacer{Height: 2}.Layout),
						// Body
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body1(a.theme.Mat, content)
							lbl.Color = a.theme.Pal.Text
							a.theme.applyFont(&lbl, a.theme.Fonts.Messages)
							return lbl.Layout(gtx)
						}),
					)
				}),
			)
		})
	})
}

func modelIconName(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gemini"):
		return "gemini"
	case strings.Contains(m, "llama"):
		return "llama"
	case strings.Contains(m, "groq"):
		return "groq"
	default:
		return "gemini" // Default fallback
	}
}
