//go:build cgo && !nofyne

package gui

import (
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The look of the web page (web/style.css), as a Fyne theme and a few
// small widgets: the same palette in light and dark, cards with borders,
// tinted badge pills, chips, a sidebar with an accent bar.

type palette struct {
	bg, surface, surface2, border, text, muted, accent, accentStrong, accentInk,
	ok, okBg, warn, warnBg, bad, badBg, link color.NRGBA
}

func hexc(s string) color.NRGBA {
	var c color.NRGBA
	c.A = 0xff
	v := func(i int) uint8 {
		h := func(b byte) uint8 {
			switch {
			case b >= '0' && b <= '9':
				return b - '0'
			case b >= 'a' && b <= 'f':
				return b - 'a' + 10
			case b >= 'A' && b <= 'F':
				return b - 'A' + 10
			}
			return 0
		}
		return h(s[i])<<4 | h(s[i+1])
	}
	c.R, c.G, c.B = v(1), v(3), v(5)
	if len(s) == 9 {
		c.A = v(7)
	}
	return c
}

var lightPal = palette{
	bg: hexc("#f4f5f7"), surface: hexc("#ffffff"), surface2: hexc("#eef0f3"), border: hexc("#dde1e6"),
	text: hexc("#1d1f24"), muted: hexc("#5f6672"), accent: hexc("#e08a1e"), accentStrong: hexc("#c2700b"), accentInk: hexc("#1d1f24"),
	ok: hexc("#1f8a4c"), okBg: hexc("#e3f4ea"), warn: hexc("#9a6200"), warnBg: hexc("#fdf1dc"), bad: hexc("#c0362c"), badBg: hexc("#fbe6e4"),
	link: hexc("#1f6fd1"),
}

var darkPal = palette{
	bg: hexc("#15171b"), surface: hexc("#1d2026"), surface2: hexc("#252931"), border: hexc("#323741"),
	text: hexc("#e8eaee"), muted: hexc("#9aa2ae"), accent: hexc("#f2a33a"), accentStrong: hexc("#f7b75c"), accentInk: hexc("#1d1f24"),
	ok: hexc("#4cc283"), okBg: hexc("#173325"), warn: hexc("#f0b552"), warnBg: hexc("#3a2c12"), bad: hexc("#f07067"), badBg: hexc("#3d1d1b"),
	link: hexc("#6aa8f5"),
}

func isDark() bool {
	return fyne.CurrentApp().Settings().ThemeVariant() == theme.VariantDark
}

func pal() *palette {
	if isDark() {
		return &darkPal
	}
	return &lightPal
}

// Extra colour and size names of the theme.
const (
	cSurface  fyne.ThemeColorName = "ppg.surface"
	cSurface2 fyne.ThemeColorName = "ppg.surface2"
	cBorder   fyne.ThemeColorName = "ppg.border"

	sH1    fyne.ThemeSizeName = "ppg.h1"
	sH2    fyne.ThemeSizeName = "ppg.h2"
	sSmall fyne.ThemeSizeName = "ppg.small"
	sTiny  fyne.ThemeSizeName = "ppg.tiny"
)

type ppgTheme struct {
	regular, bold, mono fyne.Resource
}

func newTheme() *ppgTheme {
	t := &ppgTheme{}
	// Use the system's UI font where the page would (Segoe UI on Windows);
	// elsewhere Fyne's own font. Read at run time from the PC's font folder.
	if runtime.GOOS == "windows" {
		dir := filepath.Join(os.Getenv("WINDIR"), "Fonts")
		load := func(name string) fyne.Resource {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return nil
			}
			return fyne.NewStaticResource(name, b)
		}
		t.regular, t.bold, t.mono = load("segoeui.ttf"), load("segoeuib.ttf"), load("consola.ttf")
		if t.bold == nil {
			t.bold = load("seguisb.ttf")
		}
	}
	return t
}

func (t *ppgTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	p := &lightPal
	if v == theme.VariantDark {
		p = &darkPal
	}
	alpha := func(c color.NRGBA, a uint8) color.NRGBA { c.A = a; return c }
	switch n {
	case cSurface:
		return p.surface
	case cSurface2:
		return p.surface2
	case cBorder:
		return p.border
	case theme.ColorNameBackground:
		return p.bg
	case theme.ColorNameButton:
		return p.surface2
	case theme.ColorNameDisabledButton:
		return p.surface
	case theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
		return p.muted
	case theme.ColorNameError:
		return p.bad
	case theme.ColorNameSuccess:
		return p.ok
	case theme.ColorNameWarning:
		return p.warn
	case theme.ColorNameForeground:
		return p.text
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnWarning:
		return p.accentInk
	case theme.ColorNameForegroundOnError, theme.ColorNameForegroundOnSuccess:
		return color.White
	case theme.ColorNameHover:
		if v == theme.VariantDark {
			return color.NRGBA{255, 255, 255, 18}
		}
		return color.NRGBA{0, 0, 0, 14}
	case theme.ColorNamePressed:
		return alpha(p.accent, 60)
	case theme.ColorNameFocus:
		return alpha(p.accent, 120)
	case theme.ColorNameSelection:
		return alpha(p.accent, 70)
	case theme.ColorNameInputBackground:
		return p.surface
	case theme.ColorNameInputBorder:
		return p.border
	case theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground:
		return p.surface
	case theme.ColorNameHeaderBackground:
		return p.surface2
	case theme.ColorNamePrimary:
		return p.accent
	case theme.ColorNameHyperlink:
		return p.link
	case theme.ColorNameScrollBar:
		return alpha(p.muted, 110)
	case theme.ColorNameScrollBarBackground:
		return color.Transparent
	case theme.ColorNameSeparator:
		return p.border
	case theme.ColorNameShadow:
		return color.NRGBA{0, 0, 0, 70}
	}
	return theme.DefaultTheme().Color(n, v)
}

func (t *ppgTheme) Font(s fyne.TextStyle) fyne.Resource {
	switch {
	case s.Monospace && t.mono != nil:
		return t.mono
	case s.Bold && t.bold != nil:
		return t.bold
	case !s.Bold && !s.Monospace && !s.Italic && t.regular != nil:
		return t.regular
	}
	return theme.DefaultTheme().Font(s)
}

func (t *ppgTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return theme.DefaultTheme().Icon(n) }

func (t *ppgTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameText:
		return 14
	case theme.SizeNameCaptionText, sSmall:
		return 12.5
	case sTiny:
		return 11
	case sH1:
		return 22
	case sH2:
		return 16
	case theme.SizeNameSubHeadingText:
		return 16
	case theme.SizeNameHeadingText:
		return 22
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 8
	case theme.SizeNameInputBorder:
		return 1
	case theme.SizeNameScrollBar:
		return 8
	case theme.SizeNameScrollBarSmall:
		return 4
	}
	return theme.DefaultTheme().Size(n)
}

// ---------- building blocks ----------

// panelBox is a bordered, rounded surface (the page's .panel / .card).
func panelBox(content fyne.CanvasObject) *fyne.Container {
	return panelWith(content, pal().surface, 12, 14)
}

func panelWith(content fyne.CanvasObject, fill color.Color, padV, padH float32) *fyne.Container {
	bg := canvas.NewRectangle(fill)
	bg.CornerRadius = 10
	bg.StrokeColor = pal().border
	bg.StrokeWidth = 1
	return container.NewStack(bg, container.New(layout.NewCustomPaddedLayout(padV, padV, padH, padH), content))
}

// tintBox is a borderless tinted box (notices, verdicts, warn boxes).
func tintBox(content fyne.CanvasObject, fill color.Color) *fyne.Container {
	bg := canvas.NewRectangle(fill)
	bg.CornerRadius = 8
	return container.NewStack(bg, container.New(layout.NewCustomPaddedLayout(8, 8, 12, 12), content))
}

// tinted is wrapped text in a colour on a tinted box.
func tinted(s string, fg, bg color.NRGBA, bold bool) fyne.CanvasObject {
	rt := widget.NewRichText(&widget.TextSegment{Text: s, Style: widget.RichTextStyle{
		ColorName: colorName(fg), TextStyle: fyne.TextStyle{Bold: bold}, Inline: true}})
	rt.Wrapping = fyne.TextWrapWord
	return tintBox(rt, bg)
}

// colorName maps a palette colour to the theme name that produces it, so
// rich text can use it.
func colorName(c color.NRGBA) fyne.ThemeColorName {
	p := pal()
	switch c {
	case p.ok:
		return theme.ColorNameSuccess
	case p.warn:
		return theme.ColorNameWarning
	case p.bad:
		return theme.ColorNameError
	case p.muted:
		return theme.ColorNameDisabled
	case p.accent:
		return theme.ColorNamePrimary
	}
	return theme.ColorNameForeground
}

type pillKind int

const (
	pNeutral pillKind = iota
	pOK
	pBad
	pWarn
	pKind
	pGB
	pSky
	pTW
	pOW
	pNX
	p01
)

func pillColors(k pillKind) (bg, fg color.NRGBA) {
	p := pal()
	d := isDark()
	pick := func(light, dark [2]string) (color.NRGBA, color.NRGBA) {
		if d {
			return hexc(dark[0]), hexc(dark[1])
		}
		return hexc(light[0]), hexc(light[1])
	}
	switch k {
	case pOK:
		return p.okBg, p.ok
	case pBad:
		return p.badBg, p.bad
	case pWarn:
		return p.warnBg, p.warn
	case pKind:
		return p.surface2, p.text
	case pGB:
		return pick([2]string{"#fff2cc", "#8a6100"}, [2]string{"#3b3115", "#f0c860"})
	case pSky:
		return pick([2]string{"#dfeaff", "#2457a6"}, [2]string{"#1b2a44", "#8ab4f8"})
	case pTW:
		return pick([2]string{"#e6f4f1", "#136a5a"}, [2]string{"#123a33", "#6fd6bf"})
	case pOW:
		return pick([2]string{"#0e7a4a1f", "#0b6b40"}, [2]string{"#12402c", "#7fd8a9"})
	case pNX:
		return pick([2]string{"#d9870020", "#a85d00"}, [2]string{"#4a3210", "#f0b25a"})
	case p01:
		return pick([2]string{"#1b02ff1a", "#3b2bff"}, [2]string{"#2a2470", "#b9b2ff"})
	}
	return p.surface2, p.muted
}

// pill is a small rounded badge.
func pill(s string, k pillKind) fyne.CanvasObject {
	bg, fg := pillColors(k)
	r := canvas.NewRectangle(bg)
	r.CornerRadius = 9
	if k == pKind {
		r.StrokeColor, r.StrokeWidth = pal().border, 1
	}
	t := canvas.NewText(s, fg)
	t.TextSize = 11
	t.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewStack(r, container.New(layout.NewCustomPaddedLayout(2, 2, 7, 7), t))
}

// chip is a larger pill, for the top bar.
func chip(s string, bg, fg color.NRGBA, border bool) (*fyne.Container, *canvas.Text, *canvas.Rectangle) {
	r := canvas.NewRectangle(bg)
	r.CornerRadius = 12
	if border {
		r.StrokeColor, r.StrokeWidth = pal().border, 1
	}
	t := canvas.NewText(s, fg)
	t.TextSize = 12
	t.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewStack(r, container.New(layout.NewCustomPaddedLayout(4, 4, 10, 10), t)), t, r
}

// muted is secondary text.
func muted(s string) *widget.Label {
	l := widget.NewLabel(s)
	l.Importance = widget.LowImportance
	l.Wrapping = fyne.TextWrapWord
	l.SizeName = sSmall
	return l
}

func h1(s string) *widget.Label {
	l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	l.SizeName = sH1
	return l
}

func h2(s string) *widget.Label {
	l := widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	l.SizeName = sH2
	return l
}

// sectionTitle is the details window's small uppercase heading.
func sectionTitle(s string) *widget.Label {
	l := widget.NewLabelWithStyle(upper(s), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	l.Importance = widget.LowImportance
	l.SizeName = sSmall
	return l
}

func upper(s string) string {
	b := []rune(s)
	for i, r := range b {
		if r >= 'a' && r <= 'z' {
			b[i] = r - 32
		}
	}
	return string(b)
}

// tight removes the default padding around a column of objects.
func tight(objs ...fyne.CanvasObject) *fyne.Container {
	return container.New(&vlist{gap: 2}, objs...)
}

// vlist stacks objects with a fixed gap (VBox uses the theme padding).
type vlist struct{ gap float32 }

func (l *vlist) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var w, h float32
	n := 0
	for _, o := range objs {
		if !o.Visible() || o.MinSize().Height == 0 {
			continue
		}
		m := o.MinSize()
		if m.Width > w {
			w = m.Width
		}
		h += m.Height
		n++
	}
	if n > 1 {
		h += l.gap * float32(n-1)
	}
	return fyne.NewSize(w, h)
}

func (l *vlist) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		if o.MinSize().Height == 0 {
			o.Resize(fyne.NewSize(size.Width, 0))
			o.Move(fyne.NewPos(0, y))
			continue
		}
		o.Resize(fyne.NewSize(size.Width, o.MinSize().Height))
		o.Move(fyne.NewPos(0, y))
		y += o.Size().Height
		// a wrapping label knows its height only once it has its width
		if m := o.MinSize().Height; m != o.Size().Height {
			o.Resize(fyne.NewSize(size.Width, m))
			y += m - o.Size().Height
		}
		y += l.gap
	}
}

// flow lays objects out in rows, wrapping like inline elements.
type flow struct {
	gap   float32
	width float32 // from the last layout
}

func flowBox(gap float32, objs ...fyne.CanvasObject) *fyne.Container {
	return container.New(&flow{gap: gap}, objs...)
}

func (f *flow) rows(objs []fyne.CanvasObject, width float32) float32 {
	x, y, rowH := float32(0), float32(0), float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		if x > 0 && width > 0 && x+m.Width > width {
			x, y, rowH = 0, y+rowH+f.gap, 0
		}
		x += m.Width + f.gap
		if m.Height > rowH {
			rowH = m.Height
		}
	}
	return y + rowH
}

func (f *flow) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var w float32
	for _, o := range objs {
		if o.Visible() && o.MinSize().Width > w {
			w = o.MinSize().Width
		}
	}
	return fyne.NewSize(w, f.rows(objs, f.width))
}

func (f *flow) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	f.width = size.Width
	x, y, rowH := float32(0), float32(0), float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		m := o.MinSize()
		if x > 0 && x+m.Width > size.Width {
			x, y, rowH = 0, y+rowH+f.gap, 0
		}
		o.Resize(m)
		o.Move(fyne.NewPos(x, y))
		x += m.Width + f.gap
		if m.Height > rowH {
			rowH = m.Height
		}
	}
}

// cardGrid fills the width with as many columns as fit (like CSS
// repeat(auto-fill, minmax(min, 1fr))); each row is as tall as its tallest
// card.
type cardGrid struct {
	min, gap float32
	width    float32
	height   float32
	parent   *fyne.Container
}

func (g *cardGrid) cols(w float32) int {
	n := int((w + g.gap) / (g.min + g.gap))
	if n < 1 {
		n = 1
	}
	return n
}

func (g *cardGrid) MinSize(objs []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(g.min, g.height)
}

func (g *cardGrid) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	g.width = size.Width
	cols := g.cols(size.Width)
	cw := (size.Width - g.gap*float32(cols-1)) / float32(cols)
	y := float32(0)
	for i := 0; i < len(objs); i += cols {
		row := objs[i:min(i+cols, len(objs))]
		var h float32
		for _, o := range row {
			o.Resize(fyne.NewSize(cw, o.MinSize().Height))
			if m := o.MinSize().Height; m > h {
				h = m
			}
		}
		for j, o := range row {
			o.Resize(fyne.NewSize(cw, h))
			o.Move(fyne.NewPos(float32(j)*(cw+g.gap), y))
		}
		y += h + g.gap
	}
	if len(objs) > 0 {
		y -= g.gap
	}
	if y != g.height {
		g.height = y
		if g.parent != nil {
			p := g.parent
			go fyne.Do(p.Refresh) // the scroll area takes the new height
		}
	}
}

func newCardGrid(min, gap float32) *fyne.Container {
	g := &cardGrid{min: min, gap: gap}
	c := container.New(g)
	g.parent = c
	return c
}

// tapArea makes anything clickable, with a hover callback (cards, sidebar
// items, mirror rows). Buttons inside keep their own clicks.
type tapArea struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onTap   func()
	onHover func(bool)
}

func newTapArea(content fyne.CanvasObject, onTap func(), onHover func(bool)) *tapArea {
	t := &tapArea{content: content, onTap: onTap, onHover: onHover}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapArea) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(t.content) }
func (t *tapArea) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}
func (t *tapArea) Cursor() desktop.Cursor { return desktop.PointerCursor }
func (t *tapArea) MouseIn(*desktop.MouseEvent) {
	if t.onHover != nil {
		t.onHover(true)
	}
}
func (t *tapArea) MouseMoved(*desktop.MouseEvent) {}
func (t *tapArea) MouseOut() {
	if t.onHover != nil {
		t.onHover(false)
	}
}

// navItem is a sidebar entry: icon, name, optional count; the active one
// has a tinted background and an accent bar on the left.
type navItem struct {
	root  *tapArea
	bg    *canvas.Rectangle
	bar   *canvas.Rectangle
	label *widget.Label
	count *canvas.Text
	cbox  *fyne.Container
	on    bool
}

func newNavItem(icon fyne.Resource, name string, tap func()) *navItem {
	n := &navItem{}
	n.bg = canvas.NewRectangle(color.Transparent)
	n.bg.CornerRadius = 8
	n.bar = canvas.NewRectangle(pal().accent)
	n.bar.CornerRadius = 2
	n.bar.Hide()
	n.label = widget.NewLabel(name)
	ic := widget.NewIcon(theme.NewDisabledResource(icon))
	n.count = canvas.NewText("", pal().muted)
	n.count.TextSize = 11
	cbg := canvas.NewRectangle(pal().bg)
	cbg.CornerRadius = 8
	n.cbox = container.NewStack(cbg, container.New(layout.NewCustomPaddedLayout(1, 1, 7, 7), n.count))
	n.cbox.Hide()
	row := container.NewBorder(nil, nil, container.NewHBox(ic, n.label), container.NewCenter(n.cbox))
	barBox := container.NewBorder(nil, nil, container.New(layout.NewCustomPaddedLayout(6, 6, 0, 0), n.bar), nil)
	n.bar.SetMinSize(fyne.NewSize(3, 0))
	content := container.NewStack(n.bg, barBox, container.New(layout.NewCustomPaddedLayout(0, 0, 6, 8), row))
	n.root = newTapArea(content, tap, func(in bool) {
		if n.on {
			return
		}
		if in {
			n.bg.FillColor = pal().surface2
		} else {
			n.bg.FillColor = color.Transparent
		}
		n.bg.Refresh()
	})
	return n
}

func (n *navItem) setActive(on bool) {
	n.on = on
	n.label.TextStyle = fyne.TextStyle{Bold: on}
	n.label.Refresh()
	if on {
		n.bg.FillColor = pal().surface2
		n.bar.Show()
	} else {
		n.bg.FillColor = color.Transparent
		n.bar.Hide()
	}
	n.bg.Refresh()
}

func (n *navItem) setCount(s string) {
	n.count.Text = s
	n.count.Refresh()
	if s == "" {
		n.cbox.Hide()
	} else {
		n.cbox.Show()
	}
}

// tabBar is the page's underlined tabs (Mods / Contraptions).
type tabBar struct {
	root     *fyne.Container
	labels   []*widget.Label
	lines    []*canvas.Rectangle
	selected int
	onChange func(int)
}

func newTabBar(names []string, selected int, onChange func(int)) *tabBar {
	t := &tabBar{selected: selected, onChange: onChange}
	row := container.NewHBox()
	for i, n := range names {
		i := i
		l := widget.NewLabelWithStyle(n, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
		line := canvas.NewRectangle(pal().accent)
		line.SetMinSize(fyne.NewSize(0, 2))
		t.labels = append(t.labels, l)
		t.lines = append(t.lines, line)
		item := container.NewBorder(nil, line, nil, nil, container.New(layout.NewCustomPaddedLayout(0, 0, 8, 8), l))
		row.Add(newTapArea(item, func() { t.Select(i) }, nil))
	}
	under := canvas.NewRectangle(pal().border)
	under.SetMinSize(fyne.NewSize(0, 1))
	t.root = container.NewBorder(nil, under, nil, nil, row)
	t.paint()
	return t
}

func (t *tabBar) Select(i int) {
	if i == t.selected {
		return
	}
	t.selected = i
	t.paint()
	if t.onChange != nil {
		t.onChange(i)
	}
}

func (t *tabBar) SetText(i int, s string) {
	t.labels[i].SetText(s)
}

func (t *tabBar) paint() {
	for i, l := range t.labels {
		if i == t.selected {
			l.Importance = widget.MediumImportance
			t.lines[i].FillColor = pal().accent
		} else {
			l.Importance = widget.LowImportance
			t.lines[i].FillColor = color.Transparent
		}
		l.Refresh()
		t.lines[i].Refresh()
	}
}

// pulseDot is the running-task indicator.
func pulseDot() (*canvas.Circle, *fyne.Animation) {
	c := canvas.NewCircle(pal().accent)
	c.Resize(fyne.NewSize(9, 9))
	a := fyne.NewAnimation(time.Second, func(f float32) {
		col := pal().accent
		col.A = uint8(80 + 175*f)
		c.FillColor = col
		c.Refresh()
	})
	a.AutoReverse = true
	a.RepeatCount = fyne.AnimationRepeatForever
	return c, a
}

// fixed gives an object a fixed size.
func fixed(o fyne.CanvasObject, w, h float32) fyne.CanvasObject {
	return container.New(&fixedLayout{fyne.NewSize(w, h)}, o)
}

type fixedLayout struct{ s fyne.Size }

func (f *fixedLayout) MinSize([]fyne.CanvasObject) fyne.Size { return f.s }
func (f *fixedLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Resize(size)
		o.Move(fyne.NewPos(0, 0))
	}
}

// hline is a 1px border line.
func hline() fyne.CanvasObject {
	r := canvas.NewRectangle(pal().border)
	r.SetMinSize(fyne.NewSize(1, 1))
	return r
}
