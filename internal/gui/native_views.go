//go:build cgo && !nofyne

package gui

import (
	"fmt"
	"image/color"
	"regexp"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Trlydev/SWG/internal/app"
	"github.com/Trlydev/SWG/internal/manager"
)

// ---------- details ----------

type detailsWin struct {
	pop    *widget.PopUp
	ref    string
	mirror string
	m      app.SearchResult

	sub      *fyne.Container
	facts    *fyne.Container
	mirrors  *fyne.Container
	check    *fyne.Container
	required *fyne.Container
	desc     *widget.Label
	warn     fyne.CanvasObject
	install  *widget.Button
	page     string
	scroll   *container.Scroll
	body     *fyne.Container
}

// relayout lays the sheet out again after a part of it filled in (facts,
// safety check, mirrors, required mods): a list positions each part by the
// height it had at the last layout, so without this a part that grew is
// drawn over by the parts below it.
func (d *detailsWin) relayout() {
	if d.body != nil {
		d.body.Refresh()
	}
	if d.scroll != nil {
		d.scroll.Refresh()
	}
}

func (d *detailsWin) close() {
	d.pop.Hide()
}

// openDetails shows a mod's overview and runs the safety check on it (or
// on the chosen mirror copy) before anything is installed. Like the page,
// it is a sheet over the window.
func (u *ui) openDetails(m app.SearchResult, mirror string) {
	d := u.details
	same := d != nil && d.ref == m.Ref && d.pop.Visible()
	if !same {
		if d != nil {
			d.close()
		}
		d = &detailsWin{}
		u.details = d
	}
	d.ref, d.mirror, d.m = m.Ref, mirror, m
	if !same {
		p := pal()
		d.sub = flowBox(6, sourceBadges(&m)...)
		if m.Author != "" {
			d.sub.Add(muted("by " + m.Author))
		}
		d.facts = flowBox(8)
		d.mirrors = tight()
		d.required = tight()
		d.desc = text("Loading description…")
		d.check = tight()
		d.warn = warnBox("Only continue if you have read the findings above and trust this mod's author. Mods run with full access to your PC.")
		d.install = widget.NewButton("Install", nil)
		d.install.Importance = widget.HighImportance
		pageBtn := widget.NewButton("View original page", func() { u.openURL(orStr(d.page, m.URL)) })
		pageBtn.Importance = widget.LowImportance
		closeBtn := widget.NewButton("Close", func() { d.close() })
		title := widget.NewLabelWithStyle(m.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		title.SizeName = sH1
		title.Wrapping = fyne.TextWrapWord
		hero := u.thumbs.box(m.Ref, m.Name, m.Image, 0, 300, nil, false)
		section := func(name string, c fyne.CanvasObject) fyne.CanvasObject { return tight(sectionTitle(name), c) }
		d.body = container.New(&vlist{gap: 14},
			tight(title, d.sub), d.facts, d.mirrors,
			section("Safety check", d.check), d.required,
			section("Description", d.desc))
		d.scroll = container.NewVScroll(container.NewBorder(hero, nil, nil, nil,
			container.New(layout.NewCustomPaddedLayout(16, 16, 22, 22), d.body)))
		actBg := canvas.NewRectangle(p.surface)
		actions := container.NewStack(actBg, container.NewBorder(hline(), nil, nil, nil,
			container.New(layout.NewCustomPaddedLayout(10, 10, 22, 22),
				tight(d.warn, container.NewHBox(pageBtn, layout.NewSpacer(), closeBtn, d.install)))))
		sheetBg := canvas.NewRectangle(p.surface)
		sheetBg.CornerRadius = 12
		sheetBg.StrokeColor, sheetBg.StrokeWidth = p.border, 1
		sheet := container.NewStack(sheetBg, container.NewBorder(nil, actions, nil, nil, d.scroll))
		d.pop = widget.NewModalPopUp(sheet, u.win.Canvas())
		cs := u.win.Canvas().Size()
		d.pop.Resize(fyne.NewSize(minF(860, cs.Width-24), cs.Height-24))
		d.pop.Show()
		go u.loadDetails(d, m)
	}
	d.check.Objects = []fyne.CanvasObject{container.NewHBox(widget.NewActivity(), muted("Downloading and scanning before install…"))}
	if a, ok := d.check.Objects[0].(*fyne.Container).Objects[0].(*widget.Activity); ok {
		a.Start()
	}
	d.check.Refresh()
	d.relayout()
	u.setDetailActions(d, nil)
	go u.previewDetails(d, m, mirror)
}

func fact(k, v string) fyne.CanvasObject {
	l := widget.NewLabel(k)
	l.Importance = widget.LowImportance
	l.SizeName = sTiny
	val := widget.NewLabelWithStyle(v, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	val.SizeName = sSmall
	bg := canvas.NewRectangle(pal().surface2)
	bg.CornerRadius = 8
	return container.NewStack(bg, container.New(layout.NewCustomPaddedLayout(2, 2, 4, 10), container.New(&vlist{gap: -10}, l, val)))
}

func (u *ui) loadDetails(d *detailsWin, m app.SearchResult) {
	var v detailsView
	err := u.s.call("GET", "/api/details?ref="+urlQuery(m.Ref)+"&name="+urlQuery(m.Name), nil, &v)
	fyne.Do(func() {
		if u.details != d || d.ref != m.Ref {
			return
		}
		if err != nil {
			d.desc.SetText("Could not load the description: " + err.Error())
			return
		}
		d.page = v.Page
		when := "Version"
		switch {
		case strings.HasPrefix(m.Ref, "gb:"), strings.HasPrefix(m.Ref, "nx:"):
			when = "Updated"
		case strings.HasPrefix(m.Ref, "tw:"):
			when = "Uploaded"
		}
		add := func(k, val string) {
			if val != "" && val != "0" {
				d.facts.Add(fact(k, val))
			}
		}
		num := func(n int) string {
			if n == 0 {
				return ""
			}
			return thousands(n)
		}
		add(when, orStr(v.Revision, v.Date))
		add("Likes", num(v.Likes))
		add("Size", v.Size)
		add("Category", v.Category)
		add("Downloads", num(v.Details.Downloads))
		add("Views", num(v.Views))
		add("ID", m.Ref)
		d.facts.Refresh()
		d.desc.SetText(orStr(v.Description, "No description."))
		if v.AfterCutoff {
			d.sub.Add(pill("revised after the worm cutoff", pBad))
		}
		d.relayout()
		if len(v.Required) > 0 {
			reqs := flowBox(6)
			var refs []string
			for _, r := range v.Required {
				r := r
				refs = append(refs, "sky:"+r.WorkshopID)
				reqs.Add(widget.NewButton(r.Title, func() {
					u.openDetails(app.SearchResult{Ref: "sky:" + r.WorkshopID, Name: r.Title}, "")
				}))
			}
			all := u.busyButton("Install all required mods", func() {
				u.run(map[string]any{"action": "install", "refs": refs}, "Installing required mods")
			})
			d.required.Objects = []fyne.CanvasObject{sectionTitle("Needs these mods too"), reqs, container.NewHBox(all)}
			d.required.Refresh()
			d.relayout()
		}
		if len(v.Mirrors) > 0 && len(d.mirrors.Objects) == 0 {
			u.renderMirrors(d, v.Mirrors, "")
		}
	})
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (u *ui) previewDetails(d *detailsWin, m app.SearchResult, mirror string) {
	q := "/api/preview?ref=" + urlQuery(m.Ref)
	if mirror != "" {
		q += "&mirror=" + urlQuery(mirror)
	}
	var p app.Preview
	err := u.s.call("GET", q, nil, &p)
	fyne.Do(func() {
		if u.details != d || d.ref != m.Ref || d.mirror != mirror {
			return
		}
		if err != nil {
			pl := pal()
			d.check.Objects = []fyne.CanvasObject{tinted("Could not check this mod: "+err.Error(), pl.bad, pl.badBg, true)}
			d.check.Refresh()
			d.relayout()
			u.setDetailActions(d, &app.Preview{Verdict: "error"})
			return
		}
		if len(p.Mirrors) > 0 {
			u.renderMirrors(d, p.Mirrors, p.Chosen)
		}
		u.showCheck(d, m, &p)
	})
}

// mirrorRow is one copy in the mirror list: a bordered row, accented when
// it is the one checked and installed.
func (u *ui) mirrorRow(d *detailsWin, id string, main fyne.CanvasObject, tags []fyne.CanvasObject, page string, off bool) fyne.CanvasObject {
	p := pal()
	on := d.mirror == id
	bg := canvas.NewRectangle(color.Transparent)
	bg.CornerRadius = 8
	bg.StrokeColor, bg.StrokeWidth = p.border, 1
	if on {
		bg.FillColor, bg.StrokeColor = p.surface2, p.accent
	}
	icon := theme.RadioButtonIcon()
	if on {
		icon = theme.RadioButtonCheckedIcon()
	}
	ic := widget.NewIcon(icon)
	if on {
		ic = widget.NewIcon(theme.NewPrimaryThemedResource(icon))
	}
	right := container.NewHBox()
	for _, t := range tags {
		right.Add(container.NewCenter(t))
	}
	if page != "" {
		b := widget.NewButton("page", func() { u.openURL(page) })
		b.Importance = widget.LowImportance
		right.Add(b)
	}
	row := container.NewBorder(nil, nil, container.NewCenter(ic), container.NewCenter(right), main)
	box := container.NewStack(bg, container.New(layout.NewCustomPaddedLayout(2, 2, 8, 6), row))
	if off {
		return container.NewStack(box, canvas.NewRectangle(color.NRGBA{p.surface.R, p.surface.G, p.surface.B, 120}))
	}
	return newTapArea(box, func() {
		if id != d.mirror {
			u.openDetails(d.m, id)
		}
	}, func(in bool) {
		if !on {
			if in {
				bg.FillColor = p.surface2
			} else {
				bg.FillColor = color.Transparent
			}
			bg.Refresh()
		}
	})
}

// renderMirrors lists every mirror copy with its version; picking one
// re-runs the safety check on that copy.
func (u *ui) renderMirrors(d *detailsWin, mirrors []app.Mirror, chosen string) {
	var best *app.Mirror
	for i := range mirrors {
		x := &mirrors[i]
		if !x.AfterCutoff && x.ModVersion != "" && (best == nil || app.CompareVersions(x.ModVersion, best.ModVersion) > 0) {
			best = x
		}
	}
	newestOK := best
	if newestOK == nil {
		for i := range mirrors {
			if !mirrors[i].AfterCutoff {
				newestOK = &mirrors[i]
				break
			}
		}
	}
	rows := []fyne.CanvasObject{u.mirrorRow(d, "", container.NewHBox(widget.NewLabelWithStyle("Automatic", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		muted("highest mod.json version from before the worm, skipping any the scanner flags")), nil, "", false)}
	for _, mr := range mirrors {
		var tags []fyne.CanvasObject
		if newestOK != nil && mr.ID == newestOK.ID {
			tags = append(tags, pill(map[bool]string{true: "highest version", false: "newest safe date"}[best != nil], pOK))
		}
		if mr.Reviewed {
			tags = append(tags, pill("reviewed", pTW))
		}
		switch mr.Archived {
		case "verified":
			tags = append(tags, pill("✓ pre-worm archive", pOK))
		case "recorded":
			tags = append(tags, pill("in pre-worm archive", pNeutral))
		}
		if mr.Browser {
			tags = append(tags, pill("in your browser", pNeutral))
		}
		if mr.Gone {
			tags = append(tags, pill("file gone", pBad))
		}
		if mr.AfterCutoff {
			tags = append(tags, pill("after worm cutoff", pBad))
		}
		if chosen != "" && mr.ID == chosen {
			tags = append(tags, pill("checked below", pNeutral))
		}
		line := " · " + orStr(mr.Version, "date unknown")
		if mr.ModVersion != "" {
			line += " · mod v" + mr.ModVersion
		} else if mr.TitleVer != "" {
			line += " · title v" + mr.TitleVer
		}
		if mr.Size != "" {
			line += " · " + mr.Size
		}
		main := container.NewHBox(widget.NewLabelWithStyle(mr.Source, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), muted(strings.TrimPrefix(line, " ")))
		rows = append(rows, u.mirrorRow(d, mr.ID, main, tags, mr.Page, mr.AfterCutoff || mr.Gone))
	}
	title := "Mirror"
	if len(mirrors) > 1 {
		title = fmt.Sprintf("Mirrors (%d copies)", len(mirrors))
	}
	d.mirrors.Objects = []fyne.CanvasObject{sectionTitle(title), container.New(&vlist{gap: 6}, rows...)}
	d.mirrors.Refresh()
	d.relayout()
}

func (u *ui) showCheck(d *detailsWin, m app.SearchResult, p *app.Preview) {
	u.browse.fillAuthor(m.Ref, p.Author)
	if p.Thumb {
		u.thumbs.reload(m.Ref)
	}
	pl := pal()
	verdicts := map[string]struct {
		fg, bg color.NRGBA
		msg    string
	}{
		"ok":          {pl.ok, pl.okBg, "✓ Passed every check. Ready to install."},
		"review":      {pl.warn, pl.warnBg, "⚠ Needs your review before installing."},
		"blocked":     {pl.bad, pl.badBg, "✕ ppgmods will not install this mod."},
		"risk":        {pl.bad, pl.badBg, "✕ CRITICAL findings. ppgmods won't install this unless you read them and accept the risk."},
		"browser":     {pl.warn, pl.warnBg, "This copy has to be downloaded in your browser."},
		"unavailable": {pl.bad, pl.badBg, "✕ This mod's mirror copy is gone."},
	}
	v, ok := verdicts[p.Verdict]
	if !ok {
		v.fg, v.bg, v.msg = pl.bad, pl.badBg, p.Verdict
	}
	box := []fyne.CanvasObject{tinted(v.msg, v.fg, v.bg, true)}
	if p.Verdict == "browser" && p.Browser != nil && p.Browser.NXM {
		box = append(box, muted("On the Files tab, click Mod Manager Download: your linked Nexus account lets ppgmods download, scan and install it."))
	} else if p.Verdict == "browser" && p.Browser != nil {
		box = append(box, muted("Reason: "+p.Browser.Reason+". Open the download page in your browser and click download; ppgmods watches your Downloads folder and installs the file automatically."))
		if strings.HasPrefix(p.Browser.Mirror, "01studio:") {
			box = append(box, muted("No 01studio.dev account? Pick the Nexus Mods or a mirror copy in the list above instead."))
		}
		if strings.HasPrefix(p.Browser.Mirror, "nexus:") {
			box = append(box, nexusAccountBox(u))
		}
	}
	if len(p.Reasons) > 0 && p.Verdict != "browser" {
		var rs []string
		for _, r := range p.Reasons {
			rs = append(rs, "•  "+stripOverride(r))
		}
		box = append(box, text(strings.Join(rs, "\n")))
	}
	var info []string
	if p.Chosen != "" {
		for _, c := range p.Mirrors {
			if c.ID == p.Chosen {
				info = append(info, "checked the "+c.Source+" copy (version "+c.Version+")")
			}
		}
	}
	if p.Kind == "contraption" {
		info = append(info, "contraption: "+strings.Join(p.Contraptions, ", ")+" (goes in your Contraptions folder)")
	} else if p.Files > 0 {
		info = append(info, fmt.Sprintf("%d files, %d C# scripts", p.Files, p.Scripts))
	}
	if p.Version != "" {
		info = append(info, "version "+p.Version)
	}
	if p.Author != "" {
		info = append(info, "mod.json author: "+p.Author)
	}
	if p.ScanMax != "" {
		if p.ScanMax == "none" {
			info = append(info, "scanner: nothing found")
		} else {
			info = append(info, "scanner: highest "+p.ScanMax)
		}
	}
	if len(info) > 0 {
		box = append(box, muted(strings.Join(info, " · ")))
	}
	if len(p.Findings) > 0 {
		box = append(box, mono(strings.Join(p.Findings, "\n")))
	}
	if p.Description != "" && strings.HasPrefix(d.desc.Text, "No description") {
		d.desc.SetText(p.Description)
	}
	d.check.Objects = []fyne.CanvasObject{container.New(&vlist{gap: 6}, box...)}
	d.check.Refresh()
	d.relayout()
	u.setDetailActions(d, p)
}

func (u *ui) setDetailActions(d *detailsWin, p *app.Preview) {
	m := d.m
	btn := d.install
	d.warn.Hide()
	btn.Importance = widget.HighImportance
	btn.Enable()
	if u.installedKeys()[m.Ref] {
		btn.SetText("Reinstall")
	} else {
		btn.SetText("Install")
	}
	btn.OnTapped = func() { d.close(); u.install(m.Ref, m.Name, nil, d.mirror, "") }
	defer btn.Refresh()
	if p == nil {
		return
	}
	switch p.Verdict {
	case "blocked", "error", "unavailable":
		btn.Disable()
	case "browser":
		if p.Browser != nil && p.Browser.NXM {
			btn.SetText("Open Files tab")
			url := p.Browser.URL
			btn.OnTapped = func() { u.openURL(url) }
			return
		}
		btn.SetText("Open download page")
		btn.OnTapped = func() { d.close(); u.install(m.Ref, m.Name, map[string]bool{"browser": true}, d.mirror, "") }
	case "risk":
		over := map[string]bool{"accept_risk": true}
		for _, r := range p.Reasons {
			if strings.Contains(r, "--cooldown") {
				over["skip_cooldown"] = true
			}
		}
		btn.Importance = widget.DangerImportance
		btn.SetText("Accept the risk…")
		d.warn.Show()
		findings, mirror := p.Findings, d.mirror
		btn.OnTapped = func() {
			d.close()
			u.riskDialog(m.Name, findings, func(phrase string) { u.install(m.Ref, m.Name, over, mirror, phrase) })
		}
	case "review":
		over := map[string]bool{}
		for _, r := range p.Reasons {
			if strings.Contains(r, "--allow-high") {
				over["allow_high"] = true
			}
			if strings.Contains(r, "--cooldown") {
				over["skip_cooldown"] = true
			}
			if strings.Contains(r, "--allow-new-findings") {
				over["allow_new_findings"] = true
			}
		}
		btn.Importance = widget.DangerImportance
		btn.SetText("Install anyway")
		d.warn.Show()
		btn.OnTapped = func() { d.close(); u.install(m.Ref, m.Name, over, d.mirror, "") }
	}
}

// ---------- Installed ----------

type installedPane struct {
	u         *ui
	compiler  *fyne.Container
	root      fyne.CanvasObject
	kind      *tabBar
	list      *fyne.Container
	missing   *fyne.Container
	verify    *fyne.Container
	openMods  *widget.Button
	openContr *widget.Button
	lastApply bool
}

func newInstalled(u *ui) *installedPane {
	p := &installedPane{u: u, list: container.New(&vlist{gap: 8}), missing: tight(), verify: tight(), compiler: tight()}
	sel := 0
	if u.app.Preferences().String("installed.kind") == "contraption" {
		sel = 1
	}
	p.kind = newTabBar([]string{"Mods", "Contraptions"}, sel, func(int) {
		u.app.Preferences().SetString("installed.kind", p.kindValue())
		p.render()
	})
	check := u.busyButton("Check for updates", func() {
		p.lastApply = false
		u.run(map[string]any{"action": "update", "apply": false}, "Checking for updates")
	})
	apply := u.busyButton("Apply safe updates", func() {
		p.lastApply = true
		u.run(map[string]any{"action": "update", "apply": true}, "Applying safe updates")
	})
	apply.Importance = widget.HighImportance
	verify := u.busyButton("Verify files", func() { u.run(map[string]any{"action": "verify"}, "Verifying files") })
	find := u.busyButton("Find already-installed", func() {
		u.run(map[string]any{"action": "find-installed"}, "Looking for mods installed without this app")
	})
	p.openMods = widget.NewButton("Open Mods folder", func() { u.open("mods") })
	p.openContr = widget.NewButton("Open Contraptions folder", func() { u.open("contraptions") })
	p.openMods.Importance, p.openContr.Importance = widget.LowImportance, widget.LowImportance
	p.missing.Hide()
	p.verify.Hide()
	p.root = view(container.New(&vlist{gap: 12}, h1("Installed"), p.kind.root,
		flowBox(8, check, apply, verify, find, p.openMods, p.openContr), p.compiler, p.missing, p.verify, p.list))
	return p
}

func (p *installedPane) kindValue() string {
	if p.kind.selected == 1 {
		return "contraption"
	}
	return "mod"
}

func (p *installedPane) render() {
	u := p.u
	all := u.state().Installed
	p.renderMissing(all)
	p.renderCompiler()
	isC := func(m installedView) bool { return m.ItemKind == "contraption" }
	nm, nc := 0, 0
	for _, m := range all {
		if isC(m) {
			nc++
		} else {
			nm++
		}
	}
	p.kind.SetText(0, fmt.Sprintf("Mods  %d", nm))
	p.kind.SetText(1, fmt.Sprintf("Contraptions  %d", nc))
	wantC := p.kind.selected == 1
	if wantC {
		p.openMods.Hide()
		p.openContr.Show()
	} else {
		p.openMods.Show()
		p.openContr.Hide()
	}
	var rows []fyne.CanvasObject
	for _, m := range all {
		if isC(m) == wantC {
			rows = append(rows, p.row(m))
		}
	}
	if len(rows) == 0 {
		msg := "No mods installed yet. Find mods under Browse, or recover them from your Workshop cache."
		if wantC {
			msg = "No contraptions installed yet. Switch Browse to Contraptions to find some."
		}
		l := muted(msg)
		l.Alignment = fyne.TextAlignCenter
		l.SizeName = theme.SizeNameText
		rows = []fyne.CanvasObject{container.New(layout.NewCustomPaddedLayout(30, 30, 0, 0), l)}
	}
	p.list.Objects = rows
	p.list.Refresh()
}

func kindPill(kind string) pillKind {
	switch kind {
	case "GameBanana":
		return pGB
	case "Steam Workshop":
		return pSky
	case "True Workshop":
		return pTW
	case "Open Workshop":
		return pOW
	case "Nexus Mods":
		return pNX
	}
	return pNeutral
}

func (p *installedPane) row(m installedView) fyne.CanvasObject {
	u := p.u
	name := widget.NewLabelWithStyle(m.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	pills := []fyne.CanvasObject{pill(m.Kind, kindPill(m.Kind))}
	if m.Adopted {
		pills = append(pills, pill("found on this PC", pNeutral))
	}
	if m.ScanMax == "HIGH" || m.ScanMax == "CRITICAL" {
		pills = append(pills, pill("scanner: "+m.ScanMax, pBad))
	}
	if m.Missing {
		pills = append(pills, pill("missing", pBad))
	}
	if m.Withdrawn != "" {
		pills = append(pills, pill("withdrawn", pBad))
	}
	if m.RiskAccepted {
		pills = append(pills, pill("risk accepted", pBad))
	}
	if m.Pinned {
		pills = append(pills, pill("pinned", pNeutral))
	}
	if m.Quarantined != "" {
		pills = append(pills, pill("quarantined", pBad))
	}
	meta := ""
	if m.Author != "" {
		meta = "by " + m.Author + " · "
	}
	meta += m.Key + " · installed " + m.InstalledAt.Format("2006-01-02")
	if !m.Revision.IsZero() {
		meta += " · source date " + m.Revision.Format("2006-01-02")
	}
	meta += " · " + strings.Join(m.Folders, ", ")
	if m.Withdrawn != "" {
		meta += "\nWithdrawn from the Open Workshop: " + m.Withdrawn
	}
	if m.Quarantined != "" {
		meta += "\nIn quarantine (" + m.Quarantined + "): moved out of the game folder, so the game can't load it."
	}
	actions := container.NewHBox()
	if m.Quarantined != "" {
		// Only putting it back or deleting it make sense while it's out.
		release := widget.NewButton("Release", func() {
			u.confirm("Put "+m.Name+" back?", text("It goes back into your game folder and the game loads it again. Only do this if you have checked it."), "Release", false, func() {
				u.run(map[string]any{"action": "release", "key": m.Key}, "Releasing "+m.Name)
			})
		})
		actions.Add(release)
		actions.Add(widget.NewButton("Remove", func() {
			u.confirm("Remove "+m.Name+"?", text("This deletes its quarantined copy."), "Remove", true, func() {
				u.run(map[string]any{"action": "remove", "key": m.Key}, "Removing "+m.Name)
			})
		}))
	} else if m.Link != "" {
		link := m.Link
		b := widget.NewButton("Open page", func() { u.openURL(link) })
		b.Importance = widget.LowImportance
		actions.Add(b)
	}
	if m.Quarantined == "" {
		if strings.HasPrefix(m.Key, "gb:") {
			l := "Pin"
			if m.Pinned {
				l = "Unpin"
			}
			actions.Add(widget.NewButton(l, func() {
				u.run(map[string]any{"action": "pin", "key": m.Key, "pinned": !m.Pinned}, map[bool]string{true: "Resuming updates", false: "Pinning"}[m.Pinned])
			}))
		}
		actions.Add(widget.NewButton("Rollback", func() { u.run(map[string]any{"action": "rollback", "key": m.Key}, "Rolling back "+m.Name) }))
		share := widget.NewButton("Share", func() { u.run(map[string]any{"action": "ow-share", "key": m.Key}, "Packing "+m.Name) })
		share.Importance = widget.LowImportance
		actions.Add(share)
		actions.Add(widget.NewButton("Quarantine", func() {
			u.confirm("Quarantine "+m.Name+"?", text("Its folder is moved out of the game folder, so the game can't load it. Nothing is deleted: Release puts it back."), "Quarantine", false, func() {
				u.run(map[string]any{"action": "quarantine", "key": m.Key}, "Quarantining "+m.Name)
			})
		}))
		actions.Add(widget.NewButton("Remove", func() {
			u.confirm("Remove "+m.Name+"?", text("This deletes "+strings.Join(m.Folders, ", ")+" from your game folder."), "Remove", true, func() {
				u.run(map[string]any{"action": "remove", "key": m.Key}, "Removing "+m.Name)
			})
		}))
	}
	thumb := fixed(u.thumbs.box(m.Key, m.Name, "", 96, 54, nil, true), 96, 54)
	main := container.New(&vlist{gap: -6}, flowBox(6, append([]fyne.CanvasObject{name}, pills...)...), muted(meta))
	vcenter := func(o fyne.CanvasObject) fyne.CanvasObject {
		return container.NewVBox(layout.NewSpacer(), o, layout.NewSpacer())
	}
	return panelBox(container.NewBorder(nil, nil, vcenter(thumb), vcenter(actions),
		container.New(layout.NewCustomPaddedLayout(0, 0, 8, 8), vcenter(main))))
}

// renderCompiler explains that script mods need RE_PPG on 1.27 and later.
func (p *installedPane) renderCompiler() {
	st := p.u.state()
	if st.NeedCompiler == 0 {
		p.compiler.Hide()
		return
	}
	pl := pal()
	msg := fmt.Sprintf("%d installed mods are C# script mods. People Playground 1.27 and later can't run C# mods on its own (it removed its compiler after the worms); they need RE_PPG, a community loader. Contraptions and mods without scripts work as usual.", st.NeedCompiler)
	if st.Loader.REPPG && st.Loader.REPPGDisabled {
		msg = fmt.Sprintf("RE_PPG is installed but turned off (RE_PPG/disable-runtime.flag), so your %d C# script mods won't run.", st.NeedCompiler)
	}
	l := text(msg)
	l.Importance = widget.WarningImportance
	page := widget.NewButton("About RE_PPG", func() { p.u.openURL("https://github.com/AlibardaWasTaken/RE_PPG") })
	page.Importance = widget.LowImportance
	p.compiler.Objects = []fyne.CanvasObject{tintBox(container.New(&vlist{gap: 0}, l,
		muted("RE_PPG runs mods with full access to your PC, like the game used to. ppgmods' checks still apply to everything you install here."),
		container.NewHBox(page)), pl.warnBg)}
	p.compiler.Refresh()
	p.compiler.Show()
}

// renderMissing offers to restore tracked items whose folders disappeared.
func (p *installedPane) renderMissing(all []installedView) {
	u := p.u
	var gone []installedView
	for _, m := range all {
		if m.Missing {
			gone = append(gone, m)
		}
	}
	if len(gone) == 0 {
		p.missing.Hide()
		return
	}
	var names []string
	for i, m := range gone {
		if i == 4 {
			names = append(names, fmt.Sprintf("and %d more", len(gone)-4))
			break
		}
		names = append(names, m.Name)
	}
	var restorable, all2 []string
	for _, m := range gone {
		all2 = append(all2, m.Key)
		if !strings.HasPrefix(m.Key, "local:") {
			restorable = append(restorable, m.Key)
		}
	}
	pl := pal()
	what := fmt.Sprintf("%d installed items are missing from your game folder", len(gone))
	if len(gone) == 1 {
		what = "1 installed item is missing from your game folder"
	}
	head := widget.NewLabelWithStyle(what+": "+strings.Join(names, ", ")+".", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	head.Wrapping = fyne.TextWrapWord
	head.Importance = widget.WarningImportance
	objs := []fyne.CanvasObject{head, muted("If you didn't delete them, check your PC for malware before restoring: whatever deleted them could do it again.")}
	if n := len(gone) - len(restorable); n > 0 {
		objs = append(objs, muted(fmt.Sprintf("%d of them came from a file on this PC, not a mod site, so ppgmods can't download them again.", n)))
	}
	row := container.NewHBox()
	if len(restorable) > 0 {
		l := "Restore them"
		if len(restorable) != len(gone) {
			l = fmt.Sprintf("Restore %d from their sites", len(restorable))
		}
		b := u.busyButton(l, func() {
			u.run(map[string]any{"action": "repair", "refs": restorable}, fmt.Sprintf("Restoring %d item(s)", len(restorable)))
		})
		b.Importance = widget.HighImportance
		row.Add(b)
	}
	stop := widget.NewButton("Stop tracking", func() {
		u.confirm("Stop tracking?", text("ppgmods forgets "+strings.Join(names, ", ")+". Nothing is deleted; their folders are already gone."), "Stop tracking", false, func() {
			u.run(map[string]any{"action": "forget", "refs": all2}, fmt.Sprintf("Forgetting %d item(s)", len(all2)))
		})
	})
	stop.Importance = widget.LowImportance
	row.Add(stop)
	objs = append(objs, row)
	p.missing.Objects = []fyne.CanvasObject{tintBox(container.New(&vlist{gap: 2}, objs...), pl.warnBg)}
	p.missing.Refresh()
	p.missing.Show()
}

func (p *installedPane) showVerify(probs []manager.Problem) {
	u := p.u
	var bad []manager.Problem
	keys := map[string]bool{}
	var refs []string
	for _, x := range probs {
		if x.Bad {
			bad = append(bad, x)
			if x.Key != "" && !keys[x.Key] {
				keys[x.Key] = true
				refs = append(refs, x.Key)
			}
		}
	}
	head := widget.NewLabelWithStyle("✓ All managed mods match their install fingerprints", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	head.Importance = widget.SuccessImportance
	if len(bad) > 0 {
		head.SetText(fmt.Sprintf("⚠ %d problem(s) found", len(bad)))
		head.Importance = widget.DangerImportance
	}
	objs := []fyne.CanvasObject{head}
	for _, x := range probs {
		l := widget.NewLabel("•  " + x.Folder + ": " + x.Issue)
		l.Wrapping = fyne.TextWrapWord
		l.SizeName = sSmall
		if x.Bad {
			l.Importance = widget.DangerImportance
		}
		objs = append(objs, l)
	}
	if len(refs) > 0 {
		b := u.busyButton(fmt.Sprintf("Restore %d damaged item(s) from their source", len(refs)), func() {
			u.run(map[string]any{"action": "repair", "refs": refs}, fmt.Sprintf("Restoring %d item(s)", len(refs)))
		})
		b.Importance = widget.HighImportance
		objs = append(objs, container.NewHBox(b))
	}
	p.verify.Objects = []fyne.CanvasObject{panelBox(container.New(&vlist{gap: 0}, objs...))}
	p.verify.Refresh()
	p.verify.Show()
}

// ---------- Recover ----------

type recoverView struct {
	u      *ui
	root   fyne.CanvasObject
	cache  *widget.Label
	backup *widget.Button
	cutoff *widget.Label
	path   *widget.Entry
	file   *widget.Entry
	ws     *widget.Entry
}

var reWorkshopID = regexp.MustCompile(`^\d{6,12}$`)

// step is one numbered recovery step (the page's .step).
func step(n, title string, desc fyne.CanvasObject, body ...fyne.CanvasObject) fyne.CanvasObject {
	p := pal()
	circle := canvas.NewCircle(p.accent)
	num := canvas.NewText(n, p.accentInk)
	num.TextStyle = fyne.TextStyle{Bold: true}
	num.TextSize = 14
	badge := fixed(container.NewStack(circle, container.NewCenter(num)), 30, 30)
	content := container.New(&vlist{gap: 6}, append([]fyne.CanvasObject{h2(title), desc}, body...)...)
	return panelWith(container.NewBorder(nil, nil, container.NewVBox(badge), nil,
		container.New(layout.NewCustomPaddedLayout(0, 0, 12, 0), content)), p.surface, 16, 16)
}

func newRecover(u *ui) *recoverView {
	r := &recoverView{u: u, cache: widget.NewLabel(""), cutoff: muted(""), path: widget.NewEntry(), file: widget.NewEntry(), ws: widget.NewEntry()}
	r.cache.TextStyle = fyne.TextStyle{Monospace: true}
	r.cache.SizeName = sSmall
	r.cache.Wrapping = fyne.TextWrapBreak
	r.backup = u.busyButton("Back up now", func() { u.run(map[string]any{"action": "backup"}, "Backing up the Workshop cache") })
	r.backup.Importance = widget.HighImportance
	r.path.SetPlaceHolder("Backup folder")
	pick := widget.NewButton("Choose…", func() {
		dialog.ShowFolderOpen(func(l fyne.ListableURI, err error) {
			if l != nil {
				r.path.SetText(l.Path())
			}
		}, u.win)
	})
	restore := u.busyButton("Restore", func() {
		p := strings.TrimSpace(r.path.Text)
		if p == "" {
			u.toast("Back up first, or choose a backup folder")
			return
		}
		u.run(map[string]any{"action": "restore", "path": p}, "Restoring from backup")
	})
	restore.Importance = widget.HighImportance
	r.file.SetPlaceHolder("A .zip, .rar or .7z mod archive")
	r.ws.SetPlaceHolder("Workshop ID (optional)")
	choose := widget.NewButton("Choose…", func() {
		fd := dialog.NewFileOpen(func(f fyne.URIReadCloser, err error) {
			if f != nil {
				r.file.SetText(f.URI().Path())
				f.Close()
			}
		}, u.win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".zip", ".rar", ".7z"}))
		fd.Show()
	})
	imp := u.busyButton("Import", func() {
		f := strings.TrimSpace(r.file.Text)
		ws := strings.TrimSpace(r.ws.Text)
		lf := strings.ToLower(f)
		switch {
		case f == "":
			u.toast("Choose a .zip, .rar or .7z file first")
		case !strings.HasSuffix(lf, ".zip") && !strings.HasSuffix(lf, ".rar") && !strings.HasSuffix(lf, ".7z"):
			u.toast("Only .zip, .rar or .7z archives")
		case ws != "" && !reWorkshopID.MatchString(ws):
			u.toast("Workshop ID should be digits only")
		default:
			u.run(map[string]any{"action": "import", "path": f, "workshop_id": ws}, "Importing "+f)
		}
	})
	imp.Importance = widget.HighImportance
	lede := muted("Steam deletes removed Workshop items from your PC the next time it syncs. Back up the cache first, then install only the copies that predate the worm.")
	lede.SizeName = theme.SizeNameText
	r.root = view(container.New(&vlist{gap: 12},
		h1("Recover Workshop mods"), lede,
		step("1", "Back up your Steam Workshop cache",
			muted("Copies steamapps/workshop/content/1118200 somewhere Steam cannot touch, and records each item's date and scan result."),
			r.cache, container.NewHBox(r.backup)),
		step("2", "Install the safe copies", r.cutoff, container.NewBorder(nil, nil, nil, container.NewHBox(pick, restore), r.path)),
		step("3", "Import a file you downloaded",
			muted("A .zip, .rar or .7z mod archive. Add its Workshop ID if it came from Skymods/modsbase so its date can be checked."),
			container.NewBorder(nil, nil, nil, choose, r.file), container.NewBorder(nil, nil, nil, imp, r.ws)),
	))
	return r
}

func (r *recoverView) update(st *stateView) {
	r.u.mu.Lock()
	running := r.u.running
	r.u.mu.Unlock()
	if len(st.Paths.Workshop) > 0 {
		r.cache.SetText("Found: " + strings.Join(st.Paths.Workshop, "\n"))
		if !running {
			r.backup.Enable()
		}
	} else {
		r.cache.SetText("No Workshop cache found on this PC.")
		r.backup.Disable()
	}
	r.cutoff.SetText("Items changed on or after " + st.Cutoff + " are refused, as are items the scanner flags.")
	if r.path.Text == "" && st.LastBackup != "" {
		r.path.SetText(st.LastBackup)
	}
	if r.u.safetyCut != nil {
		r.u.safetyCut.SetText(fmt.Sprintf("Steam Workshop copies changed on or after %s are refused: that is when the worm started inserting itself into existing mods.", st.Cutoff))
		r.u.safetyCool.SetText(fmt.Sprintf("New uploads (GameBanana, Nexus Mods, unreviewed True Workshop and Open Workshop) must be at least %d hours old, giving the community and the sites' scanners time to catch bad uploads.", int(st.Settings.CooldownHours+0.5)))
	}
}

// ---------- Safety ----------

func safetyView(u *ui) fyne.CanvasObject {
	u.safetyCut, u.safetyCool = muted(""), muted("")
	card := func(title string, body fyne.CanvasObject) fyne.CanvasObject {
		return panelBox(container.New(&vlist{gap: 0}, widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), body))
	}
	m := func(s string) fyne.CanvasObject { return muted(s) }
	lede := widget.NewRichTextFromMarkdown("The September 2026 worm spread because the Workshop pushed code to every subscriber within hours. Each check below closes part of that path. **No scanner makes mods safe**: install mods from authors you trust.")
	lede.Wrapping = fyne.TextWrapWord
	grid := newCardGrid(250, 12)
	grid.Objects = []fyne.CanvasObject{
		card("Worm cutoff", u.safetyCut),
		card("Cooldown", u.safetyCool),
		card("Source checks", m("GameBanana's own antivirus result must be clean and the file checksum must match; True Workshop and Open Workshop files must match their published SHA-256.")),
		card("Code scanner", m("Blocks process launching, networking, Steam Workshop uploads, Steam friends/chat, Steam login tickets, file deletion, self-copying into other mods, hidden code, the FPS++ worms' deserialization and UnityEvent tricks, executables hidden in base64, and shipped .exe/.dll files.")),
		card("Pre-worm archive", m("Mirror copies are checked against the archive's record from before the worm; a copy that changed since is refused.")),
		card("Blocklist", m("Known-bad mods listed in the GitHub repository are refused. The list can only block, never allow.")),
		card("Tamper check", m("Every installed file is fingerprinted. Verify files reports anything changed or added afterwards, which is how the worm infected mods.")),
		card("Safe updates", m("An update that adds new risky code is held back. Bad update anyway? Rollback restores the previous version.")),
		card("Overrides", m("HIGH findings and the cooldown can be overridden per install. CRITICAL findings need the typed confirmation; worm-like findings and the cutoff need the command line.")),
	}
	return view(container.New(&vlist{gap: 12}, h1("How mods are checked"), lede, grid))
}

// ---------- Settings ----------

type settingsView struct {
	u         *ui
	root      fyne.CanvasObject
	game      *widget.Entry
	gameHint  *widget.Label
	cooldown  *widget.Entry
	offline   *widget.Check
	bgThumbs  *widget.Check
	dirty     bool
	nexus     *fyne.Container
	paths     *widget.Label
	setup     *fyne.Container
	appInfo   *widget.Label
	install   *widget.Button
	uninstall *widget.Button
	version   *widget.Label
	about     *widget.Label
}

// field is a form row: bold label, control, hint.
func field(label string, control fyne.CanvasObject, hint fyne.CanvasObject) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	objs := []fyne.CanvasObject{l, control}
	if hint != nil {
		objs = append(objs, hint)
	}
	return container.New(&vlist{gap: 2}, objs...)
}

// maxWidth keeps a form from stretching across a wide window.
type maxWidth struct{ w float32 }

func (m *maxWidth) MinSize(objs []fyne.CanvasObject) fyne.Size {
	s := objs[0].MinSize()
	return fyne.NewSize(minF(s.Width, m.w), s.Height)
}

func (m *maxWidth) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	w := minF(size.Width, m.w)
	objs[0].Resize(fyne.NewSize(w, objs[0].MinSize().Height))
	objs[0].Move(fyne.NewPos(0, 0))
	if h := objs[0].MinSize().Height; h != objs[0].Size().Height {
		objs[0].Resize(fyne.NewSize(w, h))
	}
}

func narrow(w float32, o fyne.CanvasObject) fyne.CanvasObject { return container.New(&maxWidth{w}, o) }

func newSettings(u *ui) *settingsView {
	v := &settingsView{u: u, game: widget.NewEntry(), gameHint: muted(""), cooldown: widget.NewEntry(), nexus: container.New(&vlist{gap: 6}),
		paths: widget.NewLabel(""), appInfo: muted(""), setup: container.New(&vlist{gap: 4}), version: widget.NewLabel(""), about: muted("")}
	v.paths.TextStyle = fyne.TextStyle{Monospace: true}
	v.paths.SizeName = sSmall
	v.paths.Wrapping = fyne.TextWrapBreak
	v.game.SetPlaceHolder("Detected automatically from Steam")
	mark := func() { v.dirty = true }
	v.game.OnChanged = func(string) { mark() }
	v.cooldown.OnChanged = func(string) { mark() }
	v.offline = widget.NewCheck("Don't download the latest blocklist", func(bool) { mark() })
	v.bgThumbs = widget.NewCheck("Load missing Workshop thumbnails in the background", func(bool) { mark() })
	pick := widget.NewButton("Choose…", func() {
		dialog.ShowFolderOpen(func(l fyne.ListableURI, err error) {
			if l != nil {
				v.game.SetText(l.Path())
			}
		}, u.win)
	})
	save := widget.NewButton("Save", func() {
		h, _ := strconv.ParseFloat(strings.TrimSpace(v.cooldown.Text), 64)
		if h < 0 {
			h = 0
		}
		st := settings{Game: strings.TrimSpace(v.game.Text), CooldownHours: h, Offline: v.offline.Checked, NoBgThumbs: !v.bgThumbs.Checked}
		go func() {
			if err := u.s.call("POST", "/api/settings", st, nil); err != nil {
				u.toast(err.Error())
				return
			}
			fyne.Do(func() { v.dirty = false })
			u.toast("Settings saved")
			u.refreshState()
		}()
	})
	save.Importance = widget.HighImportance
	thumbsHint := muted("Valve deleted the preview images of the removed mods. ppgmods can fetch small mods (under 3 MB) one at a time to show their own thumbnail; this also pre-scans them so installing is instant.")
	form := container.New(&vlist{gap: 14},
		field("People Playground folder", container.NewBorder(nil, nil, nil, pick, v.game), v.gameHint),
		field("Cooldown for new uploads (hours)", v.cooldown, nil),
		v.offline,
		container.New(&vlist{gap: 0}, v.bgThumbs, container.New(layout.NewCustomPaddedLayout(0, 0, 30, 0), thumbsHint)),
		container.NewHBox(save))
	v.install = u.busyButton("Install on this PC", u.installApp)
	v.uninstall = widget.NewButton("Uninstall from this PC", func() {
		u.confirm("Uninstall PPG Mod Manager?", text("This removes the program, its menu entry and its shortcuts, then closes this window. Your installed mods, backups and settings stay where they are."),
			"Uninstall", true, func() { u.run(map[string]any{"action": "uninstall-app"}, "Uninstalling") })
	})
	v.uninstall.Importance = widget.LowImportance
	openData := widget.NewButton("Open ppgmods data folder", func() { u.open("data") })
	openData.Importance = widget.LowImportance
	section := func(title string, objs ...fyne.CanvasObject) fyne.CanvasObject {
		return container.New(&vlist{gap: 8}, append([]fyne.CanvasObject{h2(title)}, objs...)...)
	}
	v.root = view(container.New(&vlist{gap: 22},
		tight(h1("Settings"), narrow(620, form)),
		section("Game setup", narrow(720, panelBox(v.setup))),
		section("Nexus Mods", narrow(720, panelBox(v.nexus))),
		section("Folders", panelBox(v.paths), container.NewHBox(openData)),
		section("This app", v.appInfo, container.NewHBox(v.install, v.uninstall)),
		section("About", v.version, v.about,
			container.NewHBox(widget.NewButton("Check for a new version", func() { go u.checkRelease(true) }))),
	))
	return v
}

func (v *settingsView) update(st *stateView) {
	p := st.Paths
	if !v.dirty {
		v.game.SetText(st.Settings.Game)
		v.cooldown.SetText(strconv.Itoa(int(st.Settings.CooldownHours + 0.5)))
		v.offline.SetChecked(st.Settings.Offline)
		v.bgThumbs.SetChecked(!st.Settings.NoBgThumbs)
		v.dirty = false
	}
	if p.Game != "" {
		v.gameHint.SetText("Using: " + p.Game)
	} else {
		v.gameHint.SetText(p.GameError)
	}
	join := func(l []string) string {
		if len(l) == 0 {
			return "none found"
		}
		return strings.Join(l, "\n  ")
	}
	v.paths.SetText(strings.Join([]string{
		"Game:          " + orStr(p.Game, "not found"),
		"Mods folder:   " + orStr(p.Mods, "-"),
		"Contraptions:  " + orStr(p.Contraptions, "-"),
		"Steam libraries:\n  " + join(p.Libraries),
		"Workshop cache:\n  " + join(p.Workshop),
		"Downloads:     " + orStr(p.Downloads, "-"),
		"ppgmods data:  " + p.Data,
	}, "\n"))
	v.version.SetText("ppgmods " + st.Version)
	v.renderSetup(st)
	v.renderNexus(st.Nexus)
}

func (v *settingsView) renderDesktop(st *stateView, where string) {
	d := st.Desktop
	if d.Installed {
		v.appInfo.SetText("Installed at " + d.InstallPath + ". Start it from " + where + ".")
		v.install.SetText("Repair shortcuts")
	} else {
		s := "Running from " + d.RunningFrom + ". "
		if d.CopyExists {
			s += "An installed copy exists at " + d.InstallPath + "."
		} else {
			s += "Not installed."
		}
		v.appInfo.SetText(s)
		v.install.SetText("Install on this PC")
	}
	if d.Installed || d.CopyExists {
		v.uninstall.Show()
	} else {
		v.uninstall.Hide()
	}
}

// renderSetup shows how C# mods run in this game: BepInEx and RE_PPG.
func (v *settingsView) renderSetup(st *stateView) {
	l := st.Loader
	row := func(name string, ok bool, detail string) fyne.CanvasObject {
		k, s := pNeutral, "not installed"
		if ok {
			k, s = pOK, "installed"
		}
		return container.NewHBox(widget.NewLabelWithStyle(name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			container.NewCenter(pill(s, k)), muted(detail))
	}
	if st.Paths.Game == "" {
		v.setup.Objects = []fyne.CanvasObject{muted("Set the game folder above first.")}
		v.setup.Refresh()
		return
	}
	bepDetail := ""
	if l.BepInExVersion != "" {
		bepDetail = "version " + l.BepInExVersion
	}
	reDetail := "needed for C# mods on 1.27 and later"
	if l.REPPG {
		reDetail = orStr(map[bool]string{true: "version " + l.REPPGVersion}[l.REPPGVersion != ""], "")
		if l.REPPGDisabled {
			reDetail = "turned off (disable-runtime.flag)"
		}
	}
	objs := []fyne.CanvasObject{
		row("BepInEx", l.BepInEx, bepDetail),
		row("RE_PPG", l.REPPG, reDetail),
		muted(fmt.Sprintf("%d BepInEx plugins, %d patchers. Verify files (Installed) checks them for the worms.", len(l.Plugins), len(l.Patchers))),
	}
	v.setup.Objects = objs
	v.setup.Refresh()
}

// renderNexus shows the account link. The key is typed once and never shown
// again (the server doesn't send it back).
func (v *settingsView) renderNexus(n nexusView) {
	u := v.u
	sig := fmt.Sprint(n)
	if sig == u.nexusSig {
		return
	}
	u.nexusSig = sig
	keyPage := widget.NewHyperlink("your Nexus account's API page", nil)
	keyPage.OnTapped = func() { u.openURL("https://www.nexusmods.com/users/myaccount?tab=api+access") }
	keyPage.SizeName = sSmall
	post := func(body map[string]any, ok string) {
		go func() {
			if err := u.s.call("POST", "/api/nexus", body, nil); err != nil {
				u.toast(err.Error())
			} else if ok != "" {
				u.toast(ok)
			}
			fyne.Do(func() { u.nexusSig = "" })
			u.refreshState()
		}()
	}
	hint := func(s string) fyne.CanvasObject {
		return container.New(layout.NewCustomPaddedLayout(0, 0, 30, 0), muted(s))
	}
	if !n.Linked {
		key := widget.NewPasswordEntry()
		key.SetPlaceHolder("Personal API key")
		handler := widget.NewCheck("Handle \"Mod Manager Download\" links", nil)
		handler.SetChecked(false)
		link := widget.NewButton("Link account", func() {
			k := strings.TrimSpace(key.Text)
			key.SetText("")
			post(map[string]any{"key": k, "handler": handler.Checked}, "Nexus Mods account linked")
		})
		link.Importance = widget.HighImportance
		v.nexus.Objects = []fyne.CanvasObject{
			muted("Link your account to install from Nexus Mods without saving files by hand. Copy your Personal API Key (at the bottom of the page) from"),
			keyPage,
			muted("It stays on this PC and is only sent to Nexus Mods."),
			container.NewBorder(nil, nil, nil, link, key),
			container.New(&vlist{gap: 0}, handler, hint("Only for nxm:// links (People Playground files on Nexus only have Manual download, which works without this). While this is on, Vortex or Mod Organizer don't get Nexus links, for other games either.")),
		}
		v.nexus.Refresh()
		return
	}
	acct, kind := "free", pNeutral
	how := "Free accounts download through the site: Install opens the mod's download page; click Manual, then Slow download, and ppgmods picks the file up from your Downloads folder, checks it with Nexus Mods and installs it."
	if n.Premium {
		acct, kind, how = "premium", pOK, "Install downloads from Nexus Mods directly."
	}
	handler := widget.NewCheck("Handle \"Mod Manager Download\" links", nil)
	handler.SetChecked(n.Handler)
	handler.OnChanged = func(on bool) { post(map[string]any{"handler": on}, "") }
	unlink := widget.NewButton("Unlink account", func() {
		u.confirm("Unlink Nexus Mods?", text("ppgmods forgets your API key and gives Nexus links back to the previous handler."), "Unlink", false, func() {
			go func() {
				if err := u.s.call("DELETE", "/api/nexus", nil, nil); err != nil {
					u.toast(err.Error())
				}
				fyne.Do(func() { u.nexusSig = "" })
				u.refreshState()
			}()
		})
	})
	unlink.Importance = widget.LowImportance
	who := widget.NewLabelWithStyle(n.User, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.nexus.Objects = []fyne.CanvasObject{
		container.NewHBox(widget.NewLabel("Linked as"), who, container.NewCenter(pill(acct, kind))),
		muted(how),
		container.New(&vlist{gap: 0}, handler, hint("While this is on, Vortex or Mod Organizer don't get Nexus links, for other games either; turning it off gives them back.")),
		container.NewHBox(unlink),
	}
	v.nexus.Refresh()
}

// nexusAccountURL is Nexus Mods' account page: sign in, or create a free
// account (Nexus only lets signed-in users download, free accounts too).
const nexusAccountURL = "https://users.nexusmods.com/"

func nexusAccountBox(u *ui) fyne.CanvasObject {
	b := widget.NewButton("Sign in / create a free account", func() { u.openURL(nexusAccountURL) })
	b.Importance = widget.LowImportance
	return tight(muted("Nexus Mods only lets signed-in users download (a free account works; free downloads are slower). Not signed in on nexusmods.com yet?"), container.NewHBox(b))
}
