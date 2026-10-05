//go:build cgo && !nofyne

package gui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	_ "golang.org/x/image/webp"

	"github.com/DogeKingC/SWG/internal/app"
	"github.com/DogeKingC/SWG/internal/sources"
)

func urlQuery(s string) string { return url.QueryEscape(s) }

// ---------- Browse ----------

var siteOptions = []struct{ label, value string }{
	{"All sites", "all"}, {"Open Workshop", "ow"}, {"GameBanana", "gb"}, {"Nexus Mods", "nx"},
	{"True Workshop", "tw"}, {"01 STUDIO", "s01"}, {"Steam Workshop mirrors", "sky"},
}

var sortOptions = []struct{ label, value string }{
	{"Relevance", "relevance"}, {"Recently updated", "updated"}, {"Popular", "popular"},
}

var periodOptions = []struct{ label, value string }{
	{"today", "day"}, {"this week", "week"}, {"this month", "month"}, {"all time", "all"},
}

type browseCard struct {
	m         *app.SearchResult
	root      fyne.CanvasObject
	bg        *canvas.Rectangle
	installed fyne.CanvasObject
	pickBox   *fyne.Container
	meta      *widget.Label
	foot      *fyne.Container
	install   *widget.Button
	pick      *widget.Check
}

type browseView struct {
	u    *ui
	root fyne.CanvasObject

	kind                 *tabBar
	notesBox, errsBox    *fyne.Container
	site, sort, period   *widget.Select
	q, link              *widget.Entry
	notes, errs, waiting *widget.Label
	grid                 *fyne.Container
	scroll               *container.Scroll
	more                 *widget.Button
	selBar               *fyne.Container
	selCount             *widget.Label

	mu        sync.Mutex
	seq       int
	page      int
	lastQuery string
	earlier   []fyne.CanvasObject // cards of the pages before this one
	cards     map[string]*browseCard
	selected  map[string]string
	busy      bool
}

func label(opts []struct{ label, value string }, v string) string {
	for _, o := range opts {
		if o.value == v {
			return o.label
		}
	}
	return opts[0].label
}

func value(opts []struct{ label, value string }, l string) string {
	for _, o := range opts {
		if o.label == l {
			return o.value
		}
	}
	return opts[0].value
}

func labels(opts []struct{ label, value string }) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.label)
	}
	return out
}

func newBrowse(u *ui) *browseView {
	b := &browseView{u: u, cards: map[string]*browseCard{}, selected: map[string]string{}, page: 1}
	p := u.app.Preferences()
	sel := 0
	if p.StringWithFallback("browse.kind", "mod") == "contraption" {
		sel = 1
	}
	b.kind = newTabBar([]string{"Mods", "Contraptions"}, sel, nil)
	b.site = widget.NewSelect(labels(siteOptions), nil)
	b.site.SetSelected("All sites")
	b.sort = widget.NewSelect(labels(sortOptions), nil)
	b.sort.SetSelected(label(sortOptions, p.StringWithFallback("browse.sort", "relevance")))
	b.period = widget.NewSelect(labels(periodOptions), nil)
	b.period.SetSelected(label(periodOptions, p.StringWithFallback("browse.period", "week")))
	b.q = widget.NewEntry()
	b.q.OnSubmitted = func(s string) { go b.search(strings.TrimSpace(s), 1) }
	searchBtn := widget.NewButtonWithIcon("Search", theme.SearchIcon(), func() { go b.search(strings.TrimSpace(b.q.Text), 1) })
	searchBtn.Importance = widget.HighImportance
	b.notes, b.errs, b.waiting = small(""), widget.NewLabel(""), small("")
	b.errs.Wrapping = fyne.TextWrapWord
	b.errs.Importance = widget.WarningImportance
	b.waiting.Alignment = fyne.TextAlignCenter
	b.notesBox = tintBox(b.notes, pal().surface2)
	b.errsBox = tintBox(b.errs, pal().warnBg)
	b.notesBox.Hide()
	b.errsBox.Hide()
	b.grid = newCardGrid(230, 14)
	b.more = widget.NewButton("Load more", func() {
		b.mu.Lock()
		q, p := b.lastQuery, b.page
		b.mu.Unlock()
		go b.search(q, p+1)
	})
	b.more.Hide()
	b.selCount = widget.NewLabel("")
	instSel := widget.NewButton("Install selected", func() {
		b.mu.Lock()
		var refs []string
		for r := range b.selected {
			refs = append(refs, r)
		}
		b.selected = map[string]string{}
		b.mu.Unlock()
		for _, c := range b.cards {
			c.pick.SetChecked(false)
		}
		b.updateSelection()
		u.run(map[string]any{"action": "install", "refs": refs}, fmt.Sprintf("Installing %d mods", len(refs)))
	})
	instSel.Importance = widget.HighImportance
	clear := widget.NewButton("Clear", func() {
		b.mu.Lock()
		b.selected = map[string]string{}
		b.mu.Unlock()
		for _, c := range b.cards {
			c.pick.SetChecked(false)
		}
		b.updateSelection()
	})
	b.selCount.TextStyle = fyne.TextStyle{Bold: true}
	selBg := canvas.NewRectangle(pal().surface)
	selBg.CornerRadius = 10
	selBg.StrokeColor, selBg.StrokeWidth = pal().accent, 1.5
	b.selBar = container.NewStack(selBg, container.New(layout.NewCustomPaddedLayout(6, 6, 12, 8), container.NewHBox(b.selCount, instSel, clear)))
	b.selBar.Hide()
	b.link = widget.NewEntry()
	b.link.SetPlaceHolder("Have a link? Paste a GameBanana / Steam Workshop / Nexus link or ID (gb:123, sky:123)")
	installLink := func() {
		v := strings.TrimSpace(b.link.Text)
		if v == "" {
			return
		}
		ref := v
		if regexp.MustCompile(`^\d{6,12}$`).MatchString(v) {
			ref = "sky:" + v
		}
		b.link.SetText("")
		u.run(map[string]any{"action": "install", "refs": []string{ref}}, "Installing "+ref)
	}
	b.link.OnSubmitted = func(string) { installLink() }
	linkBtn := u.busyButton("Install link", installLink)

	refresh := func() {
		b.sync()
		b.mu.Lock()
		q := b.lastQuery
		b.mu.Unlock()
		go b.search(q, 1)
	}
	b.kind.onChange = func(int) { refresh() }
	b.site.OnChanged = func(string) { refresh() }
	b.sort.OnChanged = func(string) { refresh() }
	b.period.OnChanged = func(string) { refresh() }
	b.sync()

	lede := muted("Mods and contraptions from the Open Workshop, GameBanana, Nexus Mods, True Workshop, 01 STUDIO and the mirrors of the deleted Steam Workshop, in one list. Every mod is scanned before it is installed.")
	lede.SizeName = theme.SizeNameText
	searchRow := container.NewBorder(nil, nil, nil, searchBtn, b.q)
	label := func(s string) fyne.CanvasObject {
		l := widget.NewLabel(s)
		l.Importance = widget.LowImportance
		return l
	}
	filters := container.NewHBox(label("Site"), b.site, widget.NewLabel(" "), label("Sort"), b.sort, b.period)
	linkRow := container.NewBorder(nil, nil, nil, linkBtn, b.link)
	page := container.NewVBox(h1("Browse"), lede, b.kind.root, searchRow, filters, b.notesBox, b.errsBox,
		b.grid, b.waiting, container.NewCenter(b.more), widget.NewLabel(""), linkRow)
	b.scroll = container.NewVScroll(container.New(layout.NewCustomPaddedLayout(22, 30, 26, 26), page))
	b.root = container.NewStack(b.scroll, container.NewVBox(layout.NewSpacer(), container.NewCenter(b.selBar), widget.NewLabel("")))
	return b
}

func (b *browseView) kindValue() string {
	if b.kind.selected == 1 {
		return "contraption"
	}
	return "mod"
}

// sync applies the type, sort and period (and remembers them).
func (b *browseView) sync() {
	kind := b.kindValue()
	sortV := value(sortOptions, b.sort.Selected)
	if sortV == "popular" {
		b.period.Show()
	} else {
		b.period.Hide()
	}
	// 01 STUDIO only publishes mods.
	opts := labels(siteOptions)
	if kind == "contraption" {
		opts = nil
		for _, o := range siteOptions {
			if o.value != "s01" {
				opts = append(opts, o.label)
			}
		}
		if value(siteOptions, b.site.Selected) == "s01" {
			b.site.SetSelected("All sites")
		}
	}
	b.site.Options = opts
	b.site.Refresh()
	if kind == "contraption" {
		b.q.SetPlaceHolder("Search contraptions, e.g. tank, house, bridge")
	} else {
		b.q.SetPlaceHolder("Search mods, e.g. melee, tank, zombie")
	}
	p := b.u.app.Preferences()
	p.SetString("browse.kind", kind)
	p.SetString("browse.sort", sortV)
	p.SetString("browse.period", value(periodOptions, b.period.Selected))
}

var (
	reBrackets = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)
	reTitleVer = regexp.MustCompile(`\bv(er(sion)?)?\s*[:.]?\s*\d+(\.\d+)*\b`)
	reNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
)

// normTitle compares names across sites: "Jujutsu Playground [RELEASE]"
// and "Jujutsu Playground" are the same mod.
func normTitle(t string) string {
	t = reBrackets.ReplaceAllString(strings.ToLower(t), " ")
	t = reTitleVer.ReplaceAllString(t, " ")
	return strings.TrimSpace(reNonAlnum.ReplaceAllString(t, " "))
}

func hasPrefixAny(list []string, p string) bool {
	for _, x := range list {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

// search asks each source separately and shows results as they arrive.
func (b *browseView) search(q string, page int) {
	var kind, sortV, periodV, src string
	done := make(chan struct{})
	fyne.Do(func() {
		kind, sortV, periodV, src = b.kindValue(), value(sortOptions, b.sort.Selected), value(periodOptions, b.period.Selected), value(siteOptions, b.site.Selected)
		close(done)
	})
	<-done
	if sortV != "popular" {
		periodV = ""
	} else if periodV == "all" {
		periodV = ""
	}
	b.mu.Lock()
	b.seq++
	seq := b.seq
	b.lastQuery, b.page = q, page
	b.mu.Unlock()

	var parts []string
	switch {
	case src == "all" && kind == "contraption":
		parts = []string{"ow", "tw", "gb", "nx", "ws"}
	case src == "all":
		parts = []string{"ow", "tw", "gb", "ws", "s01", "nx"}
	case src == "sky":
		parts = []string{"ws"}
	default:
		parts = []string{src}
	}
	names := map[string]string{"gb": "GameBanana", "tw": "True Workshop", "ws": "Workshop mirrors (Skymods can be slow)", "s01": "01 STUDIO", "nx": "Nexus Mods", "ow": "Open Workshop"}
	var mu sync.Mutex
	pending := map[string]bool{}
	for _, p := range parts {
		pending[p] = true
	}
	lists := map[string][]app.SearchResult{}
	merged := map[string]bool{}
	var errs []string
	notes := map[string]bool{}

	fyne.Do(func() {
		if page == 1 {
			b.earlier = nil
			b.cards = map[string]*browseCard{}
			b.u.thumbs.forget()
			b.grid.Objects = nil
			b.grid.Refresh()
			b.scroll.ScrollToTop()
		} else {
			b.earlier = append([]fyne.CanvasObject{}, b.grid.Objects...)
		}
	})

	render := func() {
		mu.Lock()
		b.mu.Lock()
		stale := seq != b.seq
		b.mu.Unlock()
		if stale {
			mu.Unlock()
			return
		}
		ws := ptrs(lists["ws"])
		// A 01 STUDIO mod that the Workshop mirrors also have is one card.
		wsByRef := map[string]*app.SearchResult{}
		for _, w := range ws {
			wsByRef[w.Ref] = w
		}
		var s01 []*app.SearchResult
		for _, x := range ptrs(lists["s01"]) {
			w := wsByRef[x.Ref]
			if w == nil {
				s01 = append(s01, x)
				continue
			}
			if !hasPrefixAny(w.Mirrors, "01 STUDIO") {
				m := x.Mirrors
				if len(m) == 0 {
					m = []string{"01 STUDIO"}
				}
				w.Mirrors = append(w.Mirrors, m...)
			}
			w.Image = orStr(w.Image, x.Image)
			w.Author = orStr(w.Author, x.Author)
			if x.Downloads > w.Downloads {
				w.Downloads = x.Downloads
			}
		}
		// A Nexus Mods upload of a mod already shown (same name) joins its card.
		byTitle := map[string]*app.SearchResult{}
		for _, x := range append(append([]*app.SearchResult{}, ws...), s01...) {
			byTitle[normTitle(x.Name)] = x
		}
		var nx []*app.SearchResult
		for _, x := range ptrs(lists["nx"]) {
			w := byTitle[normTitle(x.Name)]
			if w == nil {
				nx = append(nx, x)
				continue
			}
			if !hasPrefixAny(w.Mirrors, "Nexus") {
				l := "Nexus Mods"
				if x.Version != "" {
					l += " v" + x.Version
				}
				w.Mirrors = append(w.Mirrors, l)
			}
			if x.Downloads > w.Downloads {
				w.Downloads = x.Downloads
			}
		}
		var tw []*app.SearchResult
		for _, x := range ptrs(lists["tw"]) {
			if !merged[x.Ref] {
				tw = append(tw, x)
			}
		}
		shown := mergeShown(sortV, ptrs(lists["ow"]), tw, ptrs(lists["gb"]), nx, ws, s01)
		var waiting []string
		for _, p := range parts {
			if pending[p] {
				waiting = append(waiting, names[p])
			}
		}
		errText := strings.Join(errs, " · ")
		var noteList []string
		for n := range notes {
			noteList = append(noteList, n)
		}
		mu.Unlock()
		fyne.Do(func() {
			b.mu.Lock()
			stale := seq != b.seq
			b.mu.Unlock()
			if stale {
				return
			}
			objs := append([]fyne.CanvasObject{}, b.earlier...)
			for _, m := range shown {
				c := b.card(m)
				objs = append(objs, fyne.CanvasObject(c.root))
			}
			b.grid.Objects = objs
			b.grid.Refresh()
			switch {
			case len(waiting) > 0:
				b.waiting.SetText("Still searching: " + strings.Join(waiting, ", ") + "…")
				b.waiting.Show()
			case len(shown) == 0 && page == 1:
				b.waiting.SetText("Nothing found.")
				b.waiting.Show()
			default:
				b.waiting.Hide()
			}
			setShownBox(b.errs, b.errsBox, errText)
			setShownBox(b.notes, b.notesBox, strings.Join(noteList, " "))
			if len(waiting) == 0 && len(shown) > 0 {
				b.more.Show()
			} else {
				b.more.Hide()
			}
		})
	}
	render()
	var wg sync.WaitGroup
	for _, part := range parts {
		wg.Add(1)
		go func(part string) {
			defer wg.Done()
			var r app.SearchResults
			err := b.u.s.call("GET", fmt.Sprintf("/api/search?q=%s&page=%d&part=%s&kind=%s&sort=%s&period=%s",
				urlQuery(q), page, part, kind, sortV, periodV), nil, &r)
			mu.Lock()
			if err != nil {
				errs = append(errs, err.Error())
			} else {
				for _, n := range r.Notes {
					notes[n] = true
				}
				switch part {
				case "gb":
					lists[part] = r.GameBanana
				case "tw":
					lists[part] = r.TrueWS
				case "ws":
					lists[part] = r.Workshop
					for _, x := range r.MergedTW {
						merged[x] = true
					}
				case "s01":
					lists[part] = r.Studio01
				case "nx":
					lists[part] = r.Nexus
				case "ow":
					lists[part] = r.OpenWS
				}
				errs = append(errs, r.Errors...)
			}
			delete(pending, part)
			mu.Unlock()
			render()
		}(part)
	}
	wg.Wait()
}

func setShownBox(l *widget.Label, box fyne.CanvasObject, s string) {
	l.SetText(s)
	if s == "" {
		box.Hide()
	} else {
		box.Show()
	}
}

func setShown(l *widget.Label, s string) {
	l.SetText(s)
	if s == "" {
		l.Hide()
	} else {
		l.Show()
	}
}

func ptrs(l []app.SearchResult) []*app.SearchResult {
	out := make([]*app.SearchResult, len(l))
	for i := range l {
		x := l[i]
		x.Mirrors = append([]string{}, l[i].Mirrors...)
		out[i] = &x
	}
	return out
}

func interleave(lists ...[]*app.SearchResult) []*app.SearchResult {
	n := 0
	for _, l := range lists {
		if len(l) > n {
			n = len(l)
		}
	}
	var out []*app.SearchResult
	for i := 0; i < n; i++ {
		for _, l := range lists {
			if i < len(l) {
				out = append(out, l[i])
			}
		}
	}
	return out
}

func resultDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || s == "unknown" {
		return time.Time{}
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return sources.ParseTMVersion(s)
}

func mergeShown(sortV string, lists ...[]*app.SearchResult) []*app.SearchResult {
	mixed := interleave(lists...)
	switch sortV {
	case "updated":
		sort.SliceStable(mixed, func(i, j int) bool {
			return resultDate(mixed[i].Date).After(resultDate(mixed[j].Date))
		})
	case "popular":
		sort.SliceStable(mixed, func(i, j int) bool {
			if mixed[i].Downloads != mixed[j].Downloads {
				return mixed[i].Downloads > mixed[j].Downloads
			}
			return resultDate(mixed[i].Date).After(resultDate(mixed[j].Date))
		})
	}
	return mixed
}

// sourceBadges labels where a result comes from.
func sourceBadges(m *app.SearchResult) []fyne.CanvasObject {
	switch {
	case strings.HasPrefix(m.Ref, "gb:"):
		return []fyne.CanvasObject{pill("GameBanana", pGB)}
	case strings.HasPrefix(m.Ref, "nx:"):
		return []fyne.CanvasObject{pill("Nexus Mods", pNX)}
	case strings.HasPrefix(m.Ref, "ow:"):
		if m.Reviewed {
			return []fyne.CanvasObject{pill("Open Workshop", pOW), pill("✓ reviewed", pOK)}
		}
		return []fyne.CanvasObject{pill("Open Workshop", pOW), pill("checked automatically", pNeutral)}
	case strings.HasPrefix(m.Ref, "tw:"):
		if m.Reviewed {
			return []fyne.CanvasObject{pill("True Workshop", pTW), pill("✓ reviewed", pOK)}
		}
		return []fyne.CanvasObject{pill("True Workshop", pTW), pill("not reviewed", pWarn)}
	}
	var out []fyne.CanvasObject
	if m.Source != "01 STUDIO" {
		l := "Workshop mirror"
		if len(m.Mirrors) > 1 {
			l = fmt.Sprintf("%d mirrors", len(m.Mirrors))
		}
		out = append(out, pill(l, pSky))
	}
	if m.Source == "01 STUDIO" || hasPrefixAny(m.Mirrors, "01 STUDIO") {
		out = append(out, pill("01 STUDIO", p01))
	}
	if hasPrefixAny(m.Mirrors, "Nexus") {
		out = append(out, pill("Nexus", pNX))
	}
	return out
}

func cardMeta(m *app.SearchResult) string {
	var b strings.Builder
	if m.Author != "" {
		b.WriteString("by " + m.Author + " · ")
	}
	if m.Version != "" {
		b.WriteString("v" + m.Version + " · ")
	}
	switch {
	case strings.HasPrefix(m.Ref, "gb:"), strings.HasPrefix(m.Ref, "nx:"), strings.HasPrefix(m.Ref, "ow:"):
		b.WriteString("updated ")
	case strings.HasPrefix(m.Ref, "tw:"):
		b.WriteString("uploaded ")
	case m.Source == "01 STUDIO":
		b.WriteString("published ")
	default:
		b.WriteString("copied ")
	}
	b.WriteString(m.Date)
	if m.Size != "" {
		b.WriteString(" · " + m.Size)
	}
	return b.String()
}

func (b *browseView) card(m *app.SearchResult) *browseCard {
	u := b.u
	p := pal()
	installed := u.installedKeys()[m.Ref]
	c := &browseCard{m: m}
	name := widget.NewLabelWithStyle(m.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	name.Wrapping = fyne.TextWrapWord
	c.meta = muted(cardMeta(m))
	body := container.New(&vlist{gap: -8}, name, c.meta)
	if m.Trend != "" {
		t := widget.NewLabelWithStyle(m.Trend, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		t.Importance = widget.SuccessImportance
		t.SizeName = sSmall
		t.Wrapping = fyne.TextWrapWord
		body.Add(t)
	}
	if !strings.HasPrefix(m.Ref, "gb:") && len(m.Mirrors) > 0 {
		t := muted(strings.Join(m.Mirrors, " · "))
		t.SizeName = sTiny
		body.Add(t)
	}
	pills := sourceBadges(m)
	if m.Kind == "contraption" {
		pills = append(pills, pill("contraption", pKind))
	}
	if m.Category != "" && m.Category != "Contraptions" {
		pills = append(pills, pill(m.Category, pNeutral))
	}
	if m.AfterCutoff {
		pills = append(pills, pill("after cutoff", pBad))
	}
	c.installed = pill("installed", pOK)
	pills = append(pills, c.installed)
	if !installed {
		c.installed.Hide()
	}
	c.install = widget.NewButton("Install", func() { u.install(m.Ref, m.Name, nil, "", "") })
	c.install.Importance = widget.HighImportance
	b.mu.Lock()
	busy := b.busy
	picked := b.selected[m.Ref] != ""
	b.mu.Unlock()
	if busy {
		c.install.Disable()
	}
	if installed || m.AfterCutoff {
		c.install.Hide()
	}
	c.foot = container.NewBorder(nil, nil, nil, container.NewVBox(layout.NewSpacer(), c.install), flowBox(6, pills...))

	bg := canvas.NewRectangle(p.surface)
	bg.CornerRadius = 10
	bg.StrokeColor, bg.StrokeWidth = p.border, 1
	c.bg = bg
	c.pick = widget.NewCheck("", func(on bool) {
		b.mu.Lock()
		if on {
			b.selected[m.Ref] = m.Name
		} else {
			delete(b.selected, m.Ref)
		}
		b.mu.Unlock()
		c.paint(false)
		b.updateSelection()
	})
	c.pick.Checked = picked
	if installed || m.AfterCutoff {
		c.pick.Hide()
	}
	thumb := u.thumbs.box(m.Ref, m.Name, m.Image, 0, 128, m, true)
	pbg := p.surface
	pbg.A = 225
	pickBg := canvas.NewRectangle(pbg)
	pickBg.CornerRadius = 6
	pickBox := container.NewStack(pickBg, c.pick)
	if !c.pick.Visible() {
		pickBox.Hide()
	}
	c.pickBox = pickBox
	media := container.NewStack(thumb, container.NewVBox(container.NewHBox(container.New(layout.NewCustomPaddedLayout(6, 0, 6, 0), pickBox))))
	inner := container.NewBorder(media, nil, nil, nil,
		container.New(layout.NewCustomPaddedLayout(4, 0, 4, 4), container.NewBorder(body, c.foot, nil, nil)))
	content := container.NewStack(bg, container.New(layout.NewCustomPaddedLayout(8, 10, 8, 8), inner))
	c.root = newTapArea(content, func() { u.openDetails(*m, "") }, func(in bool) { c.paint(in) })
	c.paint(false)
	b.cards[m.Ref] = c
	return c
}

// paint draws the card's border: accent when selected, stronger on hover.
func (c *browseCard) paint(hover bool) {
	p := pal()
	switch {
	case c.pick.Checked:
		c.bg.StrokeColor, c.bg.StrokeWidth = p.accent, 2
	case hover:
		c.bg.StrokeColor, c.bg.StrokeWidth = p.muted, 1
	default:
		c.bg.StrokeColor, c.bg.StrokeWidth = p.border, 1
	}
	c.bg.Refresh()
}

func (b *browseView) markInstalled() {
	keys := b.u.installedKeys()
	for ref, c := range b.cards {
		if !keys[ref] {
			continue
		}
		c.pick.SetChecked(false)
		c.pickBox.Hide()
		c.install.Hide()
		c.installed.Show()
		c.foot.Refresh()
	}
	b.updateSelection()
}

func (b *browseView) setBusy(on bool) {
	b.mu.Lock()
	b.busy = on
	b.mu.Unlock()
	for _, c := range b.cards {
		if on {
			c.install.Disable()
		} else {
			c.install.Enable()
		}
	}
}

func (b *browseView) updateSelection() {
	b.mu.Lock()
	n := len(b.selected)
	b.mu.Unlock()
	if n == 0 {
		b.selBar.Hide()
		return
	}
	b.selCount.SetText(fmt.Sprintf("%d selected", n))
	b.selBar.Show()
}

// fillAuthor adds an author learned from the mod's own mod.json to a card
// whose listing had none (Skymods and top-mods often omit it).
func (b *browseView) fillAuthor(ref, author string) {
	if author == "" {
		return
	}
	fyne.Do(func() {
		if c := b.cards[ref]; c != nil && c.m.Author == "" {
			c.m.Author = author
			c.meta.SetText(cardMeta(c.m))
		}
	})
}

// ---------- thumbnails ----------

// thumbLoader fetches preview images (https only, size-limited, decoded
// here, never rendered as anything but pixels) and the thumbnails ppgmods
// extracted from mod archives; for Workshop mirror mods whose Steam image is
// gone it can pre-scan small mods in the background to get one.
type thumbLoader struct {
	u      *ui
	mu     sync.Mutex
	cache  map[string]image.Image
	failed map[string]bool
	boxes  map[string][]*thumbBox
	sem    chan struct{}
	client *http.Client

	queue []*app.SearchResult
	tried map[string]bool
	busy  bool
}

type thumbBox struct {
	ref, img string
	stack    *fyne.Container
	w, h     float32
	cover    bool
}

func newThumbLoader(u *ui) *thumbLoader {
	return &thumbLoader{u: u, cache: map[string]image.Image{}, failed: map[string]bool{}, boxes: map[string][]*thumbBox{},
		sem: make(chan struct{}, 6), client: &http.Client{Timeout: 30 * time.Second}, tried: map[string]bool{}}
}

func (t *thumbLoader) forget() {
	t.mu.Lock()
	t.boxes = map[string][]*thumbBox{}
	t.queue = nil
	if len(t.cache) > 400 {
		t.cache = map[string]image.Image{}
	}
	t.mu.Unlock()
}

// box returns a placeholder that turns into the image once loaded. With m
// (a search card), a missing image may be fetched by pre-scanning the mod.
// cover crops the image to fill the box; otherwise it is shown whole.
func (t *thumbLoader) box(ref, name, img string, w, h float32, m *app.SearchResult, cover bool) fyne.CanvasObject {
	p := pal()
	bg := canvas.NewRectangle(p.surface2)
	bg.CornerRadius = 6
	bg.SetMinSize(fyne.NewSize(w, h))
	letter := canvas.NewText(strings.ToUpper(firstRune(strings.TrimSpace(name))), p.muted)
	letter.TextSize = h / 4
	letter.TextStyle = fyne.TextStyle{Bold: true}
	stack := container.NewStack(bg, container.NewCenter(letter))
	tb := &thumbBox{ref: ref, img: img, stack: stack, w: w, h: h, cover: cover}
	t.mu.Lock()
	t.boxes[ref] = append(t.boxes[ref], tb)
	t.mu.Unlock()
	go t.load(tb, m)
	return stack
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return "?"
}

func (t *thumbLoader) load(tb *thumbBox, m *app.SearchResult) {
	var tries []string
	if tb.img != "" {
		u := tb.img
		if strings.Contains(u, "steamusercontent.com") && !strings.Contains(u, "?") {
			u += "?imw=480&imh=270&ima=fit&impolicy=Letterbox&letterbox=false"
		}
		tries = append(tries, u)
	}
	tries = append(tries, "thumb:"+tb.ref)
	for _, src := range tries {
		if img := t.get(src); img != nil {
			t.show(tb, img)
			return
		}
	}
	if m != nil {
		t.enqueue(m)
	}
}

func (t *thumbLoader) show(tb *thumbBox, img image.Image) {
	fyne.Do(func() {
		ci := canvas.NewImageFromImage(img)
		ci.FillMode = canvas.ImageFillContain
		ci.CornerRadius = 6
		if tb.cover {
			ci.FillMode = canvas.ImageFillCover
		}
		ci.ScaleMode = canvas.ImageScaleSmooth
		ci.SetMinSize(fyne.NewSize(tb.w, tb.h))
		bg := tb.stack.Objects[0].(*canvas.Rectangle)
		if !tb.cover {
			bg.FillColor = color.Black
			bg.Refresh()
		}
		tb.stack.Objects = []fyne.CanvasObject{bg, ci}
		tb.stack.Refresh()
	})
}

// get returns a decoded image: "thumb:<ref>" is one ppgmods extracted.
func (t *thumbLoader) get(src string) image.Image {
	t.mu.Lock()
	if img, ok := t.cache[src]; ok {
		t.mu.Unlock()
		return img
	}
	if t.failed[src] {
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()
	t.sem <- struct{}{}
	defer func() { <-t.sem }()
	var data []byte
	if ref, ok := strings.CutPrefix(src, "thumb:"); ok {
		if p := app.ThumbPath(ref); p != "" {
			data, _ = os.ReadFile(p)
		}
	} else if strings.HasPrefix(src, "https://") {
		req, _ := http.NewRequest("GET", src, nil)
		req.Header.Set("User-Agent", sources.UserAgent)
		if resp, err := t.client.Do(req); err == nil {
			if resp.StatusCode == 200 {
				data, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			}
			resp.Body.Close()
		}
	}
	var img image.Image
	if len(data) > 0 {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width*cfg.Height <= 40e6 {
			img, _, _ = image.Decode(bytes.NewReader(data))
		}
	}
	t.mu.Lock()
	if img != nil {
		t.cache[src] = img
	} else if !strings.HasPrefix(src, "thumb:") {
		t.failed[src] = true
	}
	t.mu.Unlock()
	return img
}

// reload shows a thumbnail ppgmods just extracted for ref.
func (t *thumbLoader) reload(ref string) {
	t.mu.Lock()
	delete(t.cache, "thumb:"+ref)
	boxes := append([]*thumbBox{}, t.boxes[ref]...)
	t.mu.Unlock()
	for _, tb := range boxes {
		go func(tb *thumbBox) {
			if img := t.get("thumb:" + tb.ref); img != nil {
				t.show(tb, img)
			}
		}(tb)
	}
}

// enqueue pre-scans a small Workshop mirror mod to get its own thumbnail
// and author (Valve deleted the images of the removed mods).
func (t *thumbLoader) enqueue(m *app.SearchResult) {
	if !strings.HasPrefix(m.Ref, "sky:") || m.AfterCutoff || t.u.state().Settings.NoBgThumbs || sizeBytes(m.Size) > 3<<20 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tried[m.Ref] {
		return
	}
	t.tried[m.Ref] = true
	t.queue = append(t.queue, m)
	if !t.busy {
		t.busy = true
		go t.pump()
	}
}

func (t *thumbLoader) pump() {
	for {
		t.u.mu.Lock()
		running := t.u.running
		t.u.mu.Unlock()
		if running || t.u.state().Settings.NoBgThumbs {
			time.Sleep(3 * time.Second)
			continue
		}
		t.mu.Lock()
		if len(t.queue) == 0 {
			t.busy = false
			t.mu.Unlock()
			return
		}
		m := t.queue[0]
		t.queue = t.queue[1:]
		_, shown := t.boxes[m.Ref]
		t.mu.Unlock()
		if shown {
			var p app.Preview
			if t.u.s.call("GET", "/api/preview?ref="+urlQuery(m.Ref), nil, &p) == nil {
				t.u.browse.fillAuthor(m.Ref, p.Author)
				if p.Thumb {
					t.reload(m.Ref)
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

var reSize = regexp.MustCompile(`(?i)([\d.]+)\s*(KB|MB|GB|B)`)

func sizeBytes(s string) float64 {
	m := reSize.FindStringSubmatch(s)
	if m == nil {
		return 1e18
	}
	var n float64
	fmt.Sscan(m[1], &n)
	return n * map[string]float64{"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30}[strings.ToUpper(m[2])]
}
