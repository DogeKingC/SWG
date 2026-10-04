//go:build cgo && !nofyne

package gui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"github.com/DogeKingC/SWG/internal/app"
	"github.com/DogeKingC/SWG/internal/manager"
)

// ---------- details ----------

type detailsWin struct {
	w      fyne.Window
	ref    string
	mirror string
	m      app.SearchResult

	sub      *fyne.Container
	facts    *widget.Label
	mirrors  *fyne.Container
	check    *fyne.Container
	required *fyne.Container
	desc     *widget.Label
	warn     fyne.CanvasObject
	install  *widget.Button
	page     string
}

// openDetails shows a mod's overview and runs the safety check on it (or
// on the chosen mirror copy) before anything is installed.
func (u *ui) openDetails(m app.SearchResult, mirror string) {
	d := u.details
	same := d != nil && d.ref == m.Ref
	if d == nil {
		d = &detailsWin{}
		d.w = u.app.NewWindow(m.Name)
		d.w.Resize(fyne.NewSize(760, 820))
		d.w.SetOnClosed(func() { u.details = nil })
		u.details = d
	}
	d.ref, d.mirror, d.m = m.Ref, mirror, m
	if !same {
		d.w.SetTitle(m.Name)
		d.sub = container.NewHBox(sourceBadges(&m)...)
		if m.Author != "" {
			d.sub.Add(widget.NewLabel("by " + m.Author))
		}
		d.facts = small("")
		d.mirrors = container.NewVBox()
		d.required = container.NewVBox()
		d.desc = text("Loading description…")
		d.check = container.NewVBox()
		d.warn = warnBox("Only continue if you have read the findings above and trust this mod's author. Mods run with full access to your PC.")
		d.install = widget.NewButton("Install", nil)
		d.install.Importance = widget.HighImportance
		pageBtn := widget.NewButton("View original page", func() { u.openURL(orStr(d.page, m.URL)) })
		pageBtn.Importance = widget.LowImportance
		closeBtn := widget.NewButton("Close", func() { d.w.Close() })
		body := container.NewVBox(
			u.thumbs.box(m.Ref, m.Name, m.Image, 700, 300, nil),
			heading(m.Name), d.sub, d.facts, d.mirrors,
			heading("Safety check"), d.check, d.required,
			heading("Description"), d.desc)
		actions := container.NewVBox(d.warn, container.NewHBox(pageBtn, layout.NewSpacer(), closeBtn, d.install))
		d.w.SetContent(container.NewBorder(nil, container.NewPadded(actions), nil, nil, container.NewVScroll(container.NewPadded(body))))
		go u.loadDetails(d, m)
	}
	d.check.Objects = []fyne.CanvasObject{container.NewHBox(widget.NewProgressBarInfinite(), widget.NewLabel("Downloading and scanning before install…"))}
	d.check.Refresh()
	u.setDetailActions(d, nil)
	d.w.Show()
	d.w.RequestFocus()
	go u.previewDetails(d, m, mirror)
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
		var facts []string
		add := func(k, v string) {
			if v != "" && v != "0" {
				facts = append(facts, k+": "+v)
			}
		}
		add(when, orStr(v.Revision, v.Date))
		add("Likes", strconv.Itoa(v.Likes))
		add("Size", v.Size)
		add("Category", v.Category)
		add("Downloads", strconv.Itoa(v.Details.Downloads))
		add("Views", strconv.Itoa(v.Views))
		add("ID", m.Ref)
		d.facts.SetText(strings.Join(facts, "   ·   "))
		d.desc.SetText(orStr(v.Description, "No description."))
		if v.AfterCutoff {
			d.sub.Add(badge("revised after the worm cutoff", bBad))
		}
		if len(v.Required) > 0 {
			d.required.Add(heading("Needs these mods too"))
			var refs []string
			for _, r := range v.Required {
				r := r
				refs = append(refs, "sky:"+r.WorkshopID)
				b := widget.NewButton(r.Title+"  ·  sky:"+r.WorkshopID, func() {
					u.openDetails(app.SearchResult{Ref: "sky:" + r.WorkshopID, Name: r.Title}, "")
				})
				b.Alignment = widget.ButtonAlignLeading
				d.required.Add(b)
			}
			d.required.Add(u.busyButton("Install all required mods", func() {
				u.run(map[string]any{"action": "install", "refs": refs}, "Installing required mods")
			}))
		}
		if len(v.Mirrors) > 0 && len(d.mirrors.Objects) == 0 {
			u.renderMirrors(d, v.Mirrors, "")
		}
	})
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
			l := text("Could not check this mod: " + err.Error())
			l.Importance = widget.DangerImportance
			d.check.Objects = []fyne.CanvasObject{l}
			d.check.Refresh()
			u.setDetailActions(d, &app.Preview{Verdict: "error"})
			return
		}
		if len(p.Mirrors) > 0 {
			u.renderMirrors(d, p.Mirrors, p.Chosen)
		}
		u.showCheck(d, m, &p)
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
	const auto = "Automatic: highest mod.json version from before the worm, skipping any the scanner flags"
	opts := []string{auto}
	ids := map[string]string{auto: ""}
	var off []fyne.CanvasObject
	selected := auto
	for _, mr := range mirrors {
		var tags []string
		if newestOK != nil && mr.ID == newestOK.ID {
			tags = append(tags, map[bool]string{true: "highest version", false: "newest safe date"}[best != nil])
		}
		if mr.Reviewed {
			tags = append(tags, "reviewed")
		}
		switch mr.Archived {
		case "verified":
			tags = append(tags, "✓ pre-worm archive")
		case "recorded":
			tags = append(tags, "in pre-worm archive")
		}
		if mr.Browser {
			tags = append(tags, "in your browser")
		}
		if chosen != "" && mr.ID == chosen {
			tags = append(tags, "checked below")
		}
		line := mr.Source + " · " + orStr(mr.Version, "date unknown")
		if mr.ModVersion != "" {
			line += " · mod v" + mr.ModVersion
		} else if mr.TitleVer != "" {
			line += " · title v" + mr.TitleVer
		}
		if mr.Size != "" {
			line += " · " + mr.Size
		}
		if len(tags) > 0 {
			line += "   [" + strings.Join(tags, "] [") + "]"
		}
		if mr.AfterCutoff || mr.Gone {
			why := "after worm cutoff"
			if mr.Gone {
				why = "file gone"
			}
			l := small(line + "   (" + why + ")")
			l.Importance = widget.LowImportance
			off = append(off, l)
			continue
		}
		opts = append(opts, line)
		ids[line] = mr.ID
		if d.mirror == mr.ID {
			selected = line
		}
	}
	radio := widget.NewRadioGroup(opts, nil)
	radio.SetSelected(selected)
	radio.OnChanged = func(s string) {
		if s == "" {
			return
		}
		if id := ids[s]; id != d.mirror {
			u.openDetails(d.m, id)
		}
	}
	title := "Mirror"
	if len(mirrors) > 1 {
		title = fmt.Sprintf("Mirrors (%d copies)", len(mirrors))
	}
	d.mirrors.Objects = append([]fyne.CanvasObject{heading(title), radio}, off...)
	d.mirrors.Refresh()
}

func (u *ui) showCheck(d *detailsWin, m app.SearchResult, p *app.Preview) {
	u.browse.fillAuthor(m.Ref, p.Author)
	if p.Thumb {
		u.thumbs.reload(m.Ref)
	}
	verdicts := map[string]struct {
		imp widget.Importance
		msg string
	}{
		"ok":          {widget.SuccessImportance, "✓ Passed every check. Ready to install."},
		"review":      {widget.WarningImportance, "⚠ Needs your review before installing."},
		"blocked":     {widget.DangerImportance, "✕ ppgmods will not install this mod."},
		"risk":        {widget.DangerImportance, "✕ CRITICAL findings. ppgmods won't install this unless you read them and accept the risk."},
		"browser":     {widget.WarningImportance, "This copy has to be downloaded in your browser."},
		"unavailable": {widget.DangerImportance, "✕ This mod's mirror copy is gone."},
	}
	v, ok := verdicts[p.Verdict]
	if !ok {
		v.imp, v.msg = widget.DangerImportance, p.Verdict
	}
	head := widget.NewLabelWithStyle(v.msg, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	head.Importance = v.imp
	box := []fyne.CanvasObject{head}
	if p.Verdict == "browser" && p.Browser != nil && p.Browser.NXM {
		box = append(box, small("On the Files tab, click Mod Manager Download: your linked Nexus account lets ppgmods download, scan and install it."))
	} else if p.Verdict == "browser" && p.Browser != nil {
		box = append(box, small("Reason: "+p.Browser.Reason+". Open the download page in your browser and click download; ppgmods watches your Downloads folder and installs the file automatically."))
		if strings.HasPrefix(p.Browser.Mirror, "01studio:") {
			box = append(box, small("No 01studio.dev account, or supporters only? Pick the Nexus Mods or a mirror copy in the list above instead."))
		}
	}
	if len(p.Reasons) > 0 && p.Verdict != "browser" {
		var rs []string
		for _, r := range p.Reasons {
			rs = append(rs, "• "+stripOverride(r))
		}
		box = append(box, small(strings.Join(rs, "\n")))
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
		box = append(box, small(strings.Join(info, " · ")))
	}
	if len(p.Findings) > 0 {
		box = append(box, mono(strings.Join(p.Findings, "\n")))
	}
	if p.Description != "" && strings.HasPrefix(d.desc.Text, "No description") {
		d.desc.SetText(p.Description)
	}
	d.check.Objects = box
	d.check.Refresh()
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
	btn.OnTapped = func() { d.w.Close(); u.install(m.Ref, m.Name, nil, d.mirror, "") }
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
		btn.OnTapped = func() { d.w.Close(); u.install(m.Ref, m.Name, map[string]bool{"browser": true}, d.mirror, "") }
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
			d.w.Close()
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
		btn.OnTapped = func() { d.w.Close(); u.install(m.Ref, m.Name, over, d.mirror, "") }
	}
}

// ---------- Installed ----------

type installedPane struct {
	u         *ui
	root      fyne.CanvasObject
	kind      *widget.RadioGroup
	list      *fyne.Container
	missing   *fyne.Container
	verify    *fyne.Container
	openMods  *widget.Button
	openContr *widget.Button
	lastApply bool
}

func newInstalled(u *ui) *installedPane {
	p := &installedPane{u: u, list: container.NewVBox(), missing: container.NewVBox(), verify: container.NewVBox()}
	p.kind = widget.NewRadioGroup([]string{"Mods", "Contraptions"}, func(string) {
		u.app.Preferences().SetString("installed.kind", p.kindValue())
		p.render()
	})
	p.kind.Horizontal = true
	if u.app.Preferences().String("installed.kind") == "contraption" {
		p.kind.Selected = "Contraptions"
	} else {
		p.kind.Selected = "Mods"
	}
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
	head := container.NewVBox(heading("Installed"), p.kind,
		container.NewHBox(check, apply, verify, find, p.openMods, p.openContr), p.missing, p.verify)
	p.root = container.NewBorder(container.NewPadded(head), nil, nil, nil, container.NewVScroll(container.NewPadded(p.list)))
	return p
}

func (p *installedPane) kindValue() string {
	if strings.HasPrefix(p.kind.Selected, "Contraptions") {
		return "contraption"
	}
	return "mod"
}

func (p *installedPane) render() {
	u := p.u
	all := u.state().Installed
	p.renderMissing(all)
	isC := func(m installedView) bool { return m.ItemKind == "contraption" }
	nm, nc := 0, 0
	for _, m := range all {
		if isC(m) {
			nc++
		} else {
			nm++
		}
	}
	p.kind.Options = []string{fmt.Sprintf("Mods (%d)", nm), fmt.Sprintf("Contraptions (%d)", nc)}
	if p.kindValue() == "contraption" || strings.HasPrefix(p.kind.Selected, "Contraptions") {
		p.kind.Selected = p.kind.Options[1]
	} else {
		p.kind.Selected = p.kind.Options[0]
	}
	p.kind.Refresh()
	wantC := strings.HasPrefix(p.kind.Selected, "Contraptions")
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
			rows = append(rows, p.row(m), widget.NewSeparator())
		}
	}
	if len(rows) == 0 {
		msg := "No mods installed yet. Find mods under Browse, or recover them from your Workshop cache."
		if wantC {
			msg = "No contraptions installed yet. Switch Browse to Contraptions to find some."
		}
		rows = []fyne.CanvasObject{text(msg)}
	}
	p.list.Objects = rows
	p.list.Refresh()
}

func (p *installedPane) row(m installedView) fyne.CanvasObject {
	u := p.u
	badges := []fyne.CanvasObject{widget.NewLabelWithStyle(m.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), badge(m.Kind, bSource)}
	if m.Adopted {
		badges = append(badges, badge("found on this PC", bNeutral))
	}
	if m.ScanMax == "HIGH" || m.ScanMax == "CRITICAL" {
		badges = append(badges, badge("scanner: "+m.ScanMax, bBad))
	}
	if m.Missing {
		badges = append(badges, badge("missing", bBad))
	}
	if m.Withdrawn != "" {
		badges = append(badges, badge("withdrawn: "+m.Withdrawn, bBad))
	}
	if m.RiskAccepted {
		badges = append(badges, badge("risk accepted", bBad))
	}
	if m.Pinned {
		badges = append(badges, badge("pinned", bNeutral))
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
	actions := container.NewHBox()
	if m.Link != "" {
		link := m.Link
		b := widget.NewButton("Open page", func() { u.openURL(link) })
		b.Importance = widget.LowImportance
		actions.Add(b)
	}
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
	actions.Add(widget.NewButton("Remove", func() {
		u.confirm("Remove "+m.Name+"?", text("This deletes "+strings.Join(m.Folders, ", ")+" from your game folder."), "Remove", true, func() {
			u.run(map[string]any{"action": "remove", "key": m.Key}, "Removing "+m.Name)
		})
	}))
	thumb := u.thumbs.box(m.Key, m.Name, "", 64, 64, nil)
	return container.NewBorder(nil, nil, thumb, actions, container.NewVBox(container.NewHBox(badges...), small(meta)))
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
	head := text(fmt.Sprintf("%d installed item(s) missing from your game folder: %s.", len(gone), strings.Join(names, ", ")))
	head.Importance = widget.WarningImportance
	objs := []fyne.CanvasObject{head, small("If you didn't delete them, check your PC for malware before restoring: whatever deleted them could do it again.")}
	if n := len(gone) - len(restorable); n > 0 {
		objs = append(objs, small(fmt.Sprintf("%d of them came from a file on this PC, not a mod site, so ppgmods can't download them again.", n)))
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
	row.Add(widget.NewButton("Stop tracking", func() {
		u.confirm("Stop tracking?", text("ppgmods forgets "+strings.Join(names, ", ")+". Nothing is deleted; their folders are already gone."), "Stop tracking", false, func() {
			u.run(map[string]any{"action": "forget", "refs": all2}, fmt.Sprintf("Forgetting %d item(s)", len(all2)))
		})
	}))
	objs = append(objs, row)
	p.missing.Objects = []fyne.CanvasObject{banner(container.NewVBox(objs...))}
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
	var lines []string
	for _, x := range probs {
		lines = append(lines, x.Folder+": "+x.Issue)
	}
	if len(lines) > 0 {
		objs = append(objs, mono(strings.Join(lines, "\n")))
	}
	if len(refs) > 0 {
		b := u.busyButton(fmt.Sprintf("Restore %d damaged item(s) from their source", len(refs)), func() {
			u.run(map[string]any{"action": "repair", "refs": refs}, fmt.Sprintf("Restoring %d item(s)", len(refs)))
		})
		b.Importance = widget.HighImportance
		objs = append(objs, b)
	}
	p.verify.Objects = []fyne.CanvasObject{banner(container.NewVBox(objs...))}
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

func newRecover(u *ui) *recoverView {
	r := &recoverView{u: u, cache: mono(""), cutoff: text(""), path: widget.NewEntry(), file: widget.NewEntry(), ws: widget.NewEntry()}
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
	step := func(n, title, desc string, body ...fyne.CanvasObject) fyne.CanvasObject {
		return container.NewVBox(heading(n+". "+title), text(desc), container.NewVBox(body...), widget.NewSeparator())
	}
	r.root = container.NewVScroll(container.NewPadded(container.NewVBox(
		heading("Recover Workshop mods"),
		text("Steam deletes removed Workshop items from your PC the next time it syncs. Back up the cache first, then install only the copies that predate the worm."),
		step("1", "Back up your Steam Workshop cache", "Copies steamapps/workshop/content/1118200 somewhere Steam cannot touch, and records each item's date and scan result.", r.cache, container.NewHBox(r.backup)),
		step("2", "Install the safe copies", "", r.cutoff, container.NewBorder(nil, nil, nil, container.NewHBox(pick, restore), r.path)),
		step("3", "Import a file you downloaded", "A .zip, .rar or .7z mod archive. Add its Workshop ID if it came from Skymods/modsbase so its date can be checked.",
			container.NewBorder(nil, nil, nil, choose, r.file), container.NewBorder(nil, nil, nil, imp, r.ws)),
	)))
	return r
}

func (r *recoverView) update(st *stateView) {
	if len(st.Paths.Workshop) > 0 {
		r.cache.SetText("Found: " + strings.Join(st.Paths.Workshop, "\n"))
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
	u.safetyCut, u.safetyCool = text(""), text("")
	card := func(title string, body fyne.CanvasObject) fyne.CanvasObject {
		return banner(container.NewVBox(widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), body))
	}
	return container.NewVScroll(container.NewPadded(container.NewVBox(
		heading("How mods are checked"),
		text("The September 2026 worm spread because the Workshop pushed code to every subscriber within hours. Each check below closes part of that path. No scanner makes mods safe: install mods from authors you trust."),
		container.NewGridWithColumns(2,
			card("Worm cutoff", u.safetyCut),
			card("Cooldown", u.safetyCool),
			card("Source checks", text("GameBanana's own antivirus result must be clean and the file checksum must match; True Workshop and Open Workshop files must match their published SHA-256.")),
			card("Code scanner", text("Blocks process launching, networking, Steam Workshop uploads, Steam friends/chat, Steam login tickets, file deletion, self-copying into other mods, hidden code and shipped .exe/.dll files.")),
			card("Pre-worm archive", text("Mirror copies are checked against the archive's record from before the worm; a copy that changed since is refused.")),
			card("Blocklist", text("Known-bad mods listed in the GitHub repository are refused. The list can only block, never allow.")),
			card("Tamper check", text("Every installed file is fingerprinted. Verify files reports anything changed or added afterwards, which is how the worm infected mods.")),
			card("Safe updates", text("An update that adds new risky code is held back. Bad update anyway? Rollback restores the previous version.")),
			card("Overrides", text("HIGH findings and the cooldown can be overridden per install. CRITICAL findings need the typed confirmation; worm-like findings and the cutoff need the command line.")),
		))))
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
	appInfo   *widget.Label
	install   *widget.Button
	uninstall *widget.Button
	version   *widget.Label
	about     *widget.Label
}

func newSettings(u *ui) *settingsView {
	v := &settingsView{u: u, game: widget.NewEntry(), gameHint: small(""), cooldown: widget.NewEntry(), nexus: container.NewVBox(),
		paths: mono(""), appInfo: small(""), version: widget.NewLabel(""), about: small("")}
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
	form := container.NewVBox(
		widget.NewLabel("People Playground folder"), container.NewBorder(nil, nil, nil, pick, v.game), v.gameHint,
		widget.NewLabel("Cooldown for new uploads (hours)"), v.cooldown,
		v.offline, v.bgThumbs,
		small("Valve deleted the preview images of the removed mods. ppgmods can fetch small mods (under 3 MB) one at a time to show their own thumbnail; this also pre-scans them so installing is instant."),
		container.NewHBox(save))
	v.install = u.busyButton("Install on this PC", u.installApp)
	v.uninstall = widget.NewButton("Uninstall from this PC", func() {
		u.confirm("Uninstall PPG Mod Manager?", text("This removes the program, its menu entry and its shortcuts, then closes this window. Your installed mods, backups and settings stay where they are."),
			"Uninstall", true, func() { u.run(map[string]any{"action": "uninstall-app"}, "Uninstalling") })
	})
	v.uninstall.Importance = widget.LowImportance
	openData := widget.NewButton("Open ppgmods data folder", func() { u.open("data") })
	openData.Importance = widget.LowImportance
	repo := widget.NewButton("GitHub repository", func() { u.openURL("https://github.com/DogeKingC/SWG") })
	repo.Importance = widget.LowImportance
	v.root = container.NewVScroll(container.NewPadded(container.NewVBox(
		heading("Settings"), form,
		heading("Nexus Mods"), v.nexus,
		heading("Folders"), v.paths, container.NewHBox(openData),
		heading("This app"), v.appInfo, container.NewHBox(v.install, v.uninstall),
		heading("About"), v.version, v.about,
		container.NewHBox(widget.NewButton("Check for a new version", func() { go u.checkRelease(true) }), repo),
	)))
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

// renderNexus shows the account link. The key is typed once and never shown
// again (the server doesn't send it back).
func (v *settingsView) renderNexus(n nexusView) {
	u := v.u
	sig := fmt.Sprint(n)
	if sig == u.nexusSig {
		return
	}
	u.nexusSig = sig
	keyPage := widget.NewButton("Open your Nexus account's API page", func() {
		u.openURL("https://www.nexusmods.com/users/myaccount?tab=api+access")
	})
	keyPage.Importance = widget.LowImportance
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
	if !n.Linked {
		key := widget.NewPasswordEntry()
		key.SetPlaceHolder("Personal API key")
		handler := widget.NewCheck("Handle \"Mod Manager Download\" links", nil)
		handler.SetChecked(true)
		link := widget.NewButton("Link account", func() {
			k := strings.TrimSpace(key.Text)
			key.SetText("")
			post(map[string]any{"key": k, "handler": handler.Checked}, "Nexus Mods account linked")
		})
		link.Importance = widget.HighImportance
		v.nexus.Objects = []fyne.CanvasObject{
			small("Link your account to install from Nexus Mods without saving files by hand. Copy your Personal API Key from your account's API page (at the bottom). It stays on this PC and is only sent to Nexus Mods."),
			container.NewHBox(keyPage),
			container.NewBorder(nil, nil, nil, link, key), handler,
			small("Free accounts: that button on a mod's Files tab sends the file to ppgmods. While this is on, Vortex or Mod Organizer don't get Nexus links (for other games either); turning it off gives them back."),
		}
		v.nexus.Refresh()
		return
	}
	acct := "free"
	how := "Free accounts download through the site: Install opens the mod's Files tab, where \"Mod Manager Download\" sends the file to ppgmods."
	if n.Premium {
		acct, how = "premium", "Install downloads from Nexus Mods directly."
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
	v.nexus.Objects = []fyne.CanvasObject{
		container.NewHBox(widget.NewLabel("Linked as "+n.User), badge(acct, bOK)),
		small(how), handler,
		small("While this is on, Vortex or Mod Organizer don't get Nexus links, for other games either; turning it off gives them back."),
		container.NewHBox(unlink),
	}
	v.nexus.Refresh()
}
