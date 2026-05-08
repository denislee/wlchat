package ui

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type Palette struct {
	Bg            color.NRGBA
	BgSidebar     color.NRGBA
	BgHeader      color.NRGBA
	BgComposer    color.NRGBA
	BgRowAlt      color.NRGBA
	BgRowSelected color.NRGBA
	Text          color.NRGBA
	TextStrong    color.NRGBA
	TextDim       color.NRGBA
	TextMuted     color.NRGBA
	Accent        color.NRGBA
	AccentText    color.NRGBA
	Border        color.NRGBA
	BorderStrong  color.NRGBA
	Danger        color.NRGBA
	UserBubble    color.NRGBA
}

func darkPalette() Palette {
	return Palette{
		Bg:            rgb(0x0f1115),
		BgSidebar:     rgb(0x0b0c10),
		BgHeader:      rgb(0x0f1115),
		BgComposer:    rgb(0x0f1115),
		BgRowAlt:      rgb(0x16191f),
		BgRowSelected: rgb(0x262b33),
		Text:          rgb(0xd7dae0),
		TextStrong:    rgb(0xeef0f3),
		TextDim:       rgb(0x8a93a0),
		TextMuted:     rgb(0x5e6571),
		Accent:        rgb(0x5294ff),
		AccentText:    rgb(0xffffff),
		Border:        rgb(0x1c2027),
		BorderStrong:  rgb(0x262b33),
		Danger:        rgb(0xf7768e),
		UserBubble:    rgb(0x262c47),
	}
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{
		R: uint8(c >> 16),
		G: uint8(c >> 8),
		B: uint8(c),
		A: 0xff,
	}
}

type FontStyle struct {
	Face string
	Size float32
}

type modelEntry struct {
	provIdx int
	name    string
}

type SectionFonts struct {
	Global   FontStyle
	Sidebar  FontStyle
	Header   FontStyle
	Messages FontStyle
	Input    FontStyle
}

type Theme struct {
	Mat       *material.Theme
	Pal       Palette
	Faces     []string
	MonoFaces []string
	Fonts     SectionFonts
}

func newTheme() *Theme {
	mat := material.NewTheme()
	mat.TextSize = unit.Sp(13)

	collection := gofont.Collection()
	// Simplified font loading: just use gofont for now, but allow selection
	mat.Shaper = text.NewShaper(text.WithCollection(collection))

	pal := darkPalette()
	mat.Palette.Bg = pal.Bg
	mat.Palette.Fg = pal.Text
	mat.Palette.ContrastBg = pal.Accent
	mat.Palette.ContrastFg = pal.AccentText

	var faces []string
	for _, f := range collection {
		found := false
		for _, existing := range faces {
			if existing == string(f.Font.Typeface) {
				found = true
				break
			}
		}
		if !found {
			faces = append(faces, string(f.Font.Typeface))
		}
	}

	return &Theme{
		Mat:   mat,
		Pal:   pal,
		Faces: faces,
		Fonts: SectionFonts{
			Global: FontStyle{Size: 13},
		},
	}
}

func (t *Theme) applyFont(lbl *material.LabelStyle, fs FontStyle) {
	face := fs.Face
	if face == "" {
		face = t.Fonts.Global.Face
	}
	if face != "" {
		lbl.Font.Typeface = font.Typeface(face)
	}

	size := fs.Size
	if size == 0 {
		size = t.Fonts.Global.Size
	}
	if size > 0 {
		lbl.TextSize = unit.Sp(size)
	}
}

func (t *Theme) applyFontToEditor(ed *material.EditorStyle, fs FontStyle) {
	face := fs.Face
	if face == "" {
		face = t.Fonts.Global.Face
	}
	if face != "" {
		ed.Font.Typeface = font.Typeface(face)
	}

	size := fs.Size
	if size == 0 {
		size = t.Fonts.Global.Size
	}
	if size > 0 {
		ed.TextSize = unit.Sp(size)
	}
}

// paintedBg fills the area occupied by w with bg, then renders w on top.
func paintedBg(gtx layout.Context, bg color.NRGBA, w layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()

	rect := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	paint.ColorOp{Color: bg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	rect.Pop()

	call.Add(gtx.Ops)
	return dims
}

// drawRect fills the given size with c.
func drawRect(gtx layout.Context, c color.NRGBA, sz image.Point) {
	defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

// borders bundles which sides of a rectangle to stroke. Zero value = no border.
type borders struct {
	Top, Right, Bottom, Left bool
}

// withBorder runs w, then draws 1dp lines on the requested edges of its
// dimensions in c.
func withBorder(gtx layout.Context, c color.NRGBA, b borders, w layout.Widget) layout.Dimensions {
	dims := w(gtx)
	px := gtx.Dp(unit.Dp(1))
	if px < 1 {
		px = 1
	}
	sz := dims.Size
	stroke := func(r image.Rectangle) {
		if r.Dx() <= 0 || r.Dy() <= 0 {
			return
		}
		defer clip.Rect{Min: r.Min, Max: r.Max}.Push(gtx.Ops).Pop()
		paint.ColorOp{Color: c}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
	}
	if b.Top {
		stroke(image.Rect(0, 0, sz.X, px))
	}
	if b.Bottom {
		stroke(image.Rect(0, sz.Y-px, sz.X, sz.Y))
	}
	if b.Left {
		stroke(image.Rect(0, 0, px, sz.Y))
	}
	if b.Right {
		stroke(image.Rect(sz.X-px, 0, sz.X, sz.Y))
	}
	return dims
}

// WithAlpha returns a copy of c with its alpha component set to a.
func WithAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// DeleteWord deletes the word before the caret in the editor.
func DeleteWord(ed *widget.Editor) {
	text := ed.Text()
	start, end := ed.Selection()
	if start != end {
		ed.Delete(1) // Just delete selection if there is one
		return
	}

	if start == 0 {
		return
	}

	runes := []rune(text)
	// caret position in runes
	pos := 0
	byteCount := 0
	for i, r := range runes {
		if byteCount >= start {
			pos = i
			break
		}
		byteCount += len(string(r))
		if i == len(runes)-1 {
			pos = len(runes)
		}
	}
	if byteCount < start {
		pos = len(runes)
	}

	isSpace := func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n'
	}

	i := pos - 1
	// Skip trailing spaces
	for i >= 0 && isSpace(runes[i]) {
		i--
	}
	// Skip non-spaces (the word)
	for i >= 0 && !isSpace(runes[i]) {
		i--
	}

	wordStartRunes := i + 1
	// Convert rune index back to byte offset
	wordStartBytes := 0
	for k := 0; k < wordStartRunes; k++ {
		wordStartBytes += len(string(runes[k]))
	}

	ed.SetCaret(wordStartBytes, start)
	ed.Delete(1)
}
