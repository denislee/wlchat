// Package ui — pickers.go: modal popups for selecting models, providers, and skills.
package ui

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// scrollPickerIntoView updates a.popupList so the currently selected entry is
// visible. Shared by the model and generic popups.
func (a *App) scrollPickerIntoView(rowH, listH int) {
	if a.pickerIdx == a.lastPickerIdx {
		return
	}
	if a.lastPickerIdx == -1 {
		a.popupList.Position.First = 0
		a.popupList.Position.Offset = 0
	}
	visCount := listH / rowH
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

func (a *App) layoutModelPicker(gtx layout.Context) {
	if len(a.modelChoices) != len(a.allModels) {
		a.modelChoices = make([]*widget.Clickable, len(a.allModels))
		for i := range a.modelChoices {
			a.modelChoices[i] = &widget.Clickable{}
		}
	}

	paint.ColorOp{Color: color.NRGBA{A: 0x80}}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	w := gtx.Dp(520)
	h := gtx.Constraints.Max.Y - gtx.Dp(80)
	if h > gtx.Dp(600) {
		h = gtx.Dp(600)
	}

	x := (gtx.Constraints.Max.X - w) / 2
	y := (gtx.Constraints.Max.Y - h) / 2

	a.scrollPickerIntoView(gtx.Dp(40), h-gtx.Dp(56))

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
					layout.Flexed(1, a.layoutModelList),
				)
			})
		})
	})
}

func (a *App) layoutModelList(gtx layout.Context) layout.Dimensions {
	a.popupList.Axis = layout.Vertical
	return material.List(a.theme.Mat, &a.popupList).Layout(gtx, len(a.allModels), a.layoutModelRow)
}

func (a *App) layoutModelRow(gtx layout.Context, i int) layout.Dimensions {
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
			active := a.provIdx == entry.provIdx && a.providers[a.provIdx].GetModel() == entry.name

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
									if active {
										lbl.Font.Weight = font.Bold
										lbl.Color = a.theme.Pal.TextStrong
									}
									a.theme.applyFont(&lbl, a.theme.Fonts.Global)
									return lbl.Layout(gtx)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									if !active {
										return layout.Dimensions{}
									}
									lbl := material.Caption(a.theme.Mat, "ACTIVE")
									lbl.Color = a.theme.Pal.Accent
									a.theme.applyFont(&lbl, a.theme.Fonts.Global)
									return lbl.Layout(gtx)
								}),
							)
						})
				})
			})
		}),
	)
}

func (a *App) layoutProvPicker(gtx layout.Context) {
	if len(a.provChoices) != len(a.provOptions) {
		a.provChoices = make([]*widget.Clickable, len(a.provOptions))
		for i := range a.provChoices {
			a.provChoices[i] = &widget.Clickable{}
		}
	}
	a.popupList.Axis = layout.Vertical
	a.layoutPopup(gtx, "select provider", a.provOptions, a.provChoices)
}

func (a *App) layoutSkillPicker(gtx layout.Context) {
	if len(a.skillChoices) != len(a.skillOptions) {
		a.skillChoices = make([]*widget.Clickable, len(a.skillOptions))
		for i := range a.skillChoices {
			a.skillChoices[i] = &widget.Clickable{}
		}
	}
	a.popupList.Axis = layout.Vertical
	a.layoutPopup(gtx, "select skill", a.skillOptions, a.skillChoices)
}

func (a *App) layoutPopup(gtx layout.Context, title string, items []string, clicks []*widget.Clickable) {
	paint.ColorOp{Color: color.NRGBA{A: 0x80}}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	w := gtx.Dp(420)
	h := gtx.Dp(56) + gtx.Dp(40)*len(items)
	if maxH := gtx.Constraints.Max.Y - gtx.Dp(80); h > maxH {
		h = maxH
	}
	x := (gtx.Constraints.Max.X - w) / 2
	y := (gtx.Constraints.Max.Y - h) / 2

	a.scrollPickerIntoView(gtx.Dp(40), h-gtx.Dp(56))

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
							return a.layoutPopupRow(gtx, i, items[i], clicks)
						})
					}),
				)
			})
		})
	})
}

func (a *App) layoutPopupRow(gtx layout.Context, i int, label string, clicks []*widget.Clickable) layout.Dimensions {
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
					lbl := material.Body2(a.theme.Mat, label)
					lbl.Color = a.theme.Pal.Text
					a.theme.applyFont(&lbl, a.theme.Fonts.Global)
					return lbl.Layout(gtx)
				})
		})
	})
}
