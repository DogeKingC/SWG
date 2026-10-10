// Command ppgmods recovers, installs and updates People Playground C# mods
// from community mirrors, scanning every mod before it reaches the Mods folder.
// Run without arguments it opens the GUI.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Trlydev/SWG/internal/app"
	"github.com/Trlydev/SWG/internal/cfclear"
	"github.com/Trlydev/SWG/internal/desktop"
	"github.com/Trlydev/SWG/internal/game"
	"github.com/Trlydev/SWG/internal/gui"
	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/scan"
	"github.com/Trlydev/SWG/internal/selfupdate"
	"github.com/Trlydev/SWG/internal/sources"
	"github.com/Trlydev/SWG/internal/workshop"
)

var version = "dev"

const usage = `ppgmods - People Playground mod recovery and update tool

Run without arguments (or "ppgmods gui") to open the window.

Usage: ppgmods <command> [args] [flags]

Find and install:
  search <text>              search GameBanana and Skymods (Workshop mirror)
  install <ref> [<ref>...]   install gb:<id>, sky:<workshop id>, tw:<id> (True
                             Workshop), or a GameBanana /
                             Steam Workshop link (Workshop items come from the
                             Skymods mirror via modsbase.com)
  import <file|folder>       scan and install a downloaded archive or mod folder
                             (--workshop-id <id> for Workshop/Skymods copies)

Keep up to date:
  update                     check GameBanana mods for new files (dry run)
  update --yes               apply updates that pass every safety check
  list                       installed mods
  pin|unpin <key>            stop/resume updates for a mod
  rollback <key>             restore the previous version (and pin it)
  remove <key>               uninstall
  quarantine <key> [reason]  move it out of the game folder (the game can't load it)
  release <key>              put a quarantined item back
  revoke-approvals           delete RE_PPG's "Trust and run" approvals (it asks again)
  export-list [file]         save the installed mods as a list to share
  import-list <file>         install a list's mods (each downloaded and scanned)
  off <key> / on <key>       turn a mod off (moved out, the game won't load it) or back on
  profile [list | save <name> | use <name> | delete <name>]
                             saved sets of the mods that are on
  find-installed             track mods/contraptions already in the game folders
                             (installed by hand or from the sites)
  self-update                update ppgmods itself to the latest release
  install-app [no-desktop]   install as a desktop app (Start menu / app menu,
                             desktop shortcut); no admin rights needed
  uninstall-app              remove the app, its shortcuts and menu entries
                             (your mods and settings stay)

Safety and recovery:
	scan <path>                scan an archive or folder without installing
  verify                     detect tampered/injected files in installed mods
                             and scan Mods folders ppgmods did not install
  skymods-check              pass smods.ru's Cloudflare check with a browser
                             window (clears "Skymods is showing a browser check")
  backup-workshop            copy the Steam Workshop cache before Steam deletes it
  restore-workshop <dir>     import every item from a backup-workshop folder
  nexus-probe <mod id>       check whether your Nexus account gets download
                             links from Nexus's API (downloads nothing)
  ow-pack <dir> [out]        pack a folder of Workshop-ID folders for an Open
                             Workshop bulk upload (owner)
  paths                      show detected game, Mods and Workshop folders

Flags (any command):
  --game <dir>               People Playground folder (or env PPG_DIR)
  --allow-high               install despite HIGH findings (review them first)
  --allow-critical           install despite CRITICAL findings (read the code first)
  --allow-after-cutoff       accept Steam copies revised on/after 2026-09-21
  --allow-new-findings       accept updates that add findings
  --cooldown <dur>           minimum age of GameBanana files (default 48h)
  --dry-run                  check everything, change nothing
  --offline                  do not refresh the remote blocklist
  --downloads <dir>          browser download folder to watch (default: Downloads)
  --wait <dur>               how long to wait for a browser download (default 10m)
  --no-watch                 on browser fallback, just open the page
  --dest <dir>               backup-workshop target folder
  --mirror <id>              install this mirror copy (skymods:<id> / topmods:<id>)
                             instead of the newest clean one
  --port <n>                 gui: fixed port (default random)
  --no-window                gui: do not open a window, just print the address
  --web                      gui: use the browser window instead of the native one
`

func main() {
	sources.UserAgent = fmt.Sprintf("ppgmods/%s (+https://github.com/Trlydev/SWG)", version)
	sources.Warn = logf
	cfclear.WireSkymods(logf) // Skymods passes Cloudflare's check by itself in the background
	selfupdate.Current = version
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"gui"}
	} else {
		attachConsole()
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return
	}
	cmd := args[0]
	a, rest, gopt, err := parseFlags(args[1:])
	if err != nil {
		fatal(err)
	}
	if err := run(cmd, rest, a, gopt); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	var rej *manager.Rejection
	if errors.As(err, &rej) {
		fmt.Fprintln(os.Stderr, "REFUSED:")
		for _, r := range rej.Reasons {
			fmt.Fprintln(os.Stderr, "  - "+r)
		}
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func parseFlags(argv []string) (*app.App, []string, gui.Options, error) {
	o := app.DefaultOptions()
	var g gui.Options
	fs := flag.NewFlagSet("ppgmods", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	fs.StringVar(&o.Game, "game", "", "")
	fs.StringVar(&o.Dest, "dest", "", "")
	fs.StringVar(&o.WorkshopID, "workshop-id", "", "")
	fs.StringVar(&o.Name, "name", "", "")
	fs.StringVar(&o.Mirror, "mirror", "", "")
	fs.IntVar(&o.FileID, "file", 0, "")
	fs.StringVar(&o.Downloads, "downloads", "", "")
	fs.DurationVar(&o.Wait, "wait", o.Wait, "")
	fs.BoolVar(&o.NoWatch, "no-watch", false, "")
	fs.BoolVar(&o.Yes, "yes", false, "")
	fs.BoolVar(&o.Offline, "offline", false, "")
	fs.BoolVar(&o.Policy.AllowHigh, "allow-high", false, "")
	fs.BoolVar(&o.Policy.AllowCritical, "allow-critical", false, "")
	fs.BoolVar(&o.Policy.AllowAfterCutoff, "allow-after-cutoff", false, "")
	fs.BoolVar(&o.Policy.AllowNewFindings, "allow-new-findings", false, "")
	fs.BoolVar(&o.Policy.DryRun, "dry-run", false, "")
	fs.DurationVar(&o.Policy.Cooldown, "cooldown", o.Policy.Cooldown, "")
	fs.IntVar(&g.Port, "port", 0, "")
	fs.BoolVar(&g.NoWindow, "no-window", false, "")
	fs.BoolVar(&g.Web, "web", false, "")
	var pos []string
	for {
		if err := fs.Parse(argv); err != nil {
			return nil, nil, g, err
		}
		argv = fs.Args()
		if len(argv) == 0 {
			break
		}
		pos = append(pos, argv[0])
		argv = argv[1:]
	}
	o.BrowserFallback = true
	return &app.App{Opt: o, Logf: logf}, pos, g, nil
}

func logf(format string, a ...any) { fmt.Printf(format+"\n", a...) }

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func need(args []string, n int, what string) error {
	if len(args) < n {
		return fmt.Errorf("missing %s (see `ppgmods help`)", what)
	}
	return nil
}

func run(cmd string, args []string, a *app.App, g gui.Options) error {
	switch cmd {
	case "gui":
		g.Version = version
		return gui.Run(a.Opt, g)
	case "nxm":
		// Run by the browser for Nexus Mods' "Mod Manager Download" button.
		if len(args) != 1 {
			return fmt.Errorf("usage: ppgmods nxm <nxm://peopleplayground/mods/...>")
		}
		g.Version = version
		return gui.OpenNXM(a.Opt, g, args[0])
	case "version":
		fmt.Println("ppgmods", version)
		return nil
	case "install-app":
		exe, err := desktop.Install(desktop.Options{DesktopShortcut: !hasFlag(args, "no-desktop"), Version: version}, logf)
		if err == nil {
			logf("%s is installed: start it from the Start menu / application menu (%s)", desktop.AppName, exe)
		}
		return err
	case "uninstall-app":
		return desktop.Uninstall(logf)
	case "self-update":
		return cmdSelfUpdate()
	case "paths":
		return cmdPaths(a)
	case "search":
		if err := need(args, 1, "search text"); err != nil {
			return err
		}
		return cmdSearch(strings.Join(args, " "))
	case "scan":
		if err := need(args, 1, "path"); err != nil {
			return err
		}
		return cmdScan(args[0], a)
	case "nexus-probe":
		if err := need(args, 1, "Nexus Mods mod id (from its page address)"); err != nil {
			return err
		}
		return cmdNexusProbe(args[0])
	case "skymods-check":
		return cmdSkymodsCheck()
	case "backup-workshop":
		dest, err := a.Backup()
		if err == nil {
			logf("Install the safe ones with: ppgmods restore-workshop %q", dest)
		}
		return err
	case "ow-pack":
		if err := need(args, 1, "folder of Workshop-ID folders"); err != nil {
			return err
		}
		out := filepath.Join(filepath.Dir(filepath.Clean(args[0])), "open-workshop-upload")
		if len(args) > 1 {
			out = args[1]
		}
		managed := ""
		if dir, err := game.FindGameDir(a.Opt.Game); err == nil {
			managed = filepath.Join(dir, "People Playground_Data", "Managed")
			logf("DLLs identical to the game's own in %s are left out", managed)
		} else {
			logf("game not found (%v): bundled game DLLs stay in and those items will be held; pass --game to fix", err)
		}
		logf("packing %s -> %s", args[0], out)
		mf, err := workshop.PackBulk(args[0], out, managed, logf)
		if err != nil {
			return err
		}
		logf("packed %d items. Upload everything in %s (the zips and bulk.json) to a new release, e.g.:", len(mf.Items), out)
		logf("  gh release create bulk-%s %s --repo %s --prerelease --title \"Bulk upload\" --notes \"Old Workshop items\"",
			time.Now().Format("20060102"), filepath.Join(out, "*"), workshop.Repo)
		logf("then run the workshop workflow with action bulk and that release's tag.")
		return nil
	}

	if cmd != "list" && cmd != "verify" {
		// These commands change state.json. If the window is running a task,
		// wait for it rather than writing the mod list past each other.
		lock, lerr := manager.LockState(10 * time.Minute)
		if lerr != nil {
			return lerr
		}
		defer lock.Unlock()
	}
	m, err := a.Manager(cmd != "list" && cmd != "pin" && cmd != "unpin")
	if err != nil {
		return err
	}
	switch cmd {
	case "install":
		if err := need(args, 1, "mod reference"); err != nil {
			return err
		}
		if len(args) == 1 {
			return a.Install(m, args[0])
		}
		if s := a.InstallMany(m, args); s.Refused+s.Failed > 0 {
			return fmt.Errorf("%d of %d mods not installed", s.Refused+s.Failed, len(args))
		}
		return nil
	case "import":
		if err := need(args, 1, "file or folder"); err != nil {
			return err
		}
		return a.Import(m, args[0])
	case "restore-workshop":
		if err := need(args, 1, "backup folder"); err != nil {
			return err
		}
		_, err := a.Restore(m, args[0])
		return err
	case "update":
		a.Update(m)
		return nil
	case "list":
		for _, i := range m.State.Sorted() {
			pin := ""
			if i.Pinned {
				pin = " [pinned]"
			}
			fmt.Printf("%-18s %-40s %s  %s%s\n", i.Key, i.Name, i.InstalledAt.Format("2006-01-02"), strings.Join(i.Folders, ", "), pin)
		}
		return nil
	case "pin", "unpin":
		if err := need(args, 1, "mod key"); err != nil {
			return err
		}
		return m.SetPinned(args[0], cmd == "pin")
	case "rollback":
		if err := need(args, 1, "mod key"); err != nil {
			return err
		}
		return m.Rollback(args[0])
	case "remove":
		if err := need(args, 1, "mod key"); err != nil {
			return err
		}
		return m.Remove(args[0])
	case "quarantine":
		if err := need(args, 1, "mod key"); err != nil {
			return err
		}
		return m.Quarantine(args[0], strings.Join(args[1:], " "))
	case "release":
		if err := need(args, 1, "mod key"); err != nil {
			return err
		}
		return m.Release(args[0])
	case "off":
		if err := need(args, 1, "mod key"); err != nil {
			return err
		}
		return m.TurnOff(args[0])
	case "on":
		if err := need(args, 1, "mod key"); err != nil {
			return err
		}
		return m.TurnOn(args[0])
	case "profile":
		return cmdProfile(m, args)
	case "revoke-approvals":
		_, err := m.RevokeUnsafeApprovals()
		return err
	case "export-list":
		l, skipped := app.ExportList(m.State)
		b, _ := json.MarshalIndent(l, "", "  ")
		out := "ppgmods-mod-list.json"
		if len(args) > 0 {
			out = args[0]
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return err
		}
		logf("saved %d item(s) to %s", len(l.Mods), out)
		if skipped > 0 {
			logf("%d item(s) found on this PC without a source can't be downloaded again, so they aren't in the list", skipped)
		}
		return nil
	case "import-list":
		if err := need(args, 1, "mod list file"); err != nil {
			return err
		}
		b, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		l, err := app.ParseList(b)
		if err != nil {
			return err
		}
		// A shared list must not open a browser page for each item: mods
		// that need a browser download are reported instead.
		imp := *a
		imp.Opt.BrowserFallback = false
		sum := imp.ImportList(m, l)
		if sum.Refused+sum.Failed > 0 {
			return fmt.Errorf("%d not installed; see above for reasons", sum.Refused+sum.Failed)
		}
		return nil
	case "verify":
		return cmdVerify(m)
	case "find-installed":
		found, err := a.FindExisting(m)
		if err == nil {
			logf("%d already-installed item(s) are now tracked", len(found))
		}
		return err
	}
	return fmt.Errorf("unknown command %q (see `ppgmods help`)", cmd)
}

func cmdPaths(a *app.App) error {
	p := a.Paths()
	fmt.Println("Steam libraries:")
	for _, l := range p.Libraries {
		fmt.Println("  " + l)
	}
	if p.GameError != "" {
		fmt.Println("Game:   " + p.GameError)
	} else {
		fmt.Println("Game:   " + p.Game)
		fmt.Println("Mods:   " + p.Mods)
	}
	fmt.Println("Workshop cache:")
	for _, w := range p.Workshop {
		fmt.Println("  " + w)
	}
	fmt.Println("ppgmods data: " + p.Data)
	return nil
}

func cmdSearch(q string) error {
	r := app.Search(q, 1)
	for _, e := range r.Errors {
		fmt.Println("error:", e)
	}
	fmt.Println("GameBanana (install with: ppgmods install gb:<id>)")
	for _, m := range r.GameBanana {
		fmt.Printf("  %-11s %-45s %-12s%s, updated %s\n", m.Ref, m.Name, m.Category, app.By(m.Author), m.Date)
	}
	fmt.Println("True Workshop: uploads reviewed by its maintainers (ppgmods install tw:<id>)")
	for _, it := range r.TrueWS {
		rev := "reviewed"
		if !it.Reviewed {
			rev = "NOT reviewed yet"
		}
		fmt.Printf("  %-11s %-45s%s, %s, %s\n", it.Ref, it.Name, app.By(it.Author), it.Date, rev)
	}
	fmt.Println("Steam Workshop mirrors: Skymods + top-mods (ppgmods install sky:<workshop id>)")
	for _, it := range r.Workshop {
		warn := ""
		if it.AfterCutoff {
			warn = "  [after worm cutoff]"
		}
		fmt.Printf("  %-15s %-40s%s; %s%s\n", it.Ref, it.Name, app.By(it.Author), strings.Join(it.Mirrors, ", "), warn)
	}
	return nil
}

func cmdScan(p string, a *app.App) error {
	m := &manager.Manager{Log: logf, Blocklist: manager.LoadBlocklist(!a.Opt.Offline, logf)}
	dir, rep, err := m.Stage(&manager.Candidate{Name: p, Path: p})
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, f := range rep.Findings {
		loc := filepath.ToSlash(f.File)
		if f.Line > 0 {
			loc += ":" + strconv.Itoa(f.Line)
		}
		fmt.Printf("[%s] %s %s: %s\n", f.Severity, f.Rule, loc, f.Detail)
	}
	max := "none"
	if rep.Max() >= 0 {
		max = rep.Max().String()
	}
	fmt.Printf("%d files, %d C# scripts, highest: %s\n", rep.Files, rep.Scripts, max)
	if rep.Max() >= scan.High {
		os.Exit(3)
	}
	return nil
}

// cmdSkymodsCheck passes smods.ru's Cloudflare check with a real browser:
// in the background if it passes by itself, otherwise in a window where the
// person ticks its box. ppgmods' browser profile keeps the clearance, so
// Skymods works again for installs and searches.
func cmdSkymodsCheck() error {
	if !cfclear.RunSkymodsCheck(logf) {
		return fmt.Errorf("the Skymods browser check did not pass")
	}
	return nil
}

func cmdVerify(m *manager.Manager) error {
	probs, err := m.Verify()
	if err != nil {
		return err
	}
	bad := 0
	for _, p := range probs {
		fmt.Printf("%-50s %s\n", p.Folder, p.Issue)
		if p.Bad {
			bad++
		}
	}
	if bad == 0 {
		fmt.Println("all managed mods match their install hashes")
		return nil
	}
	os.Exit(4)
	return nil
}

func cmdSelfUpdate() error {
	rel, err := selfupdate.Latest()
	if err != nil {
		return err
	}
	if !rel.Newer {
		fmt.Printf("ppgmods %s is the latest version\n", version)
		return nil
	}
	fmt.Printf("updating ppgmods %s -> %s\n", version, rel.Version)
	if err := selfupdate.Apply(rel, logf); err != nil {
		return err
	}
	fmt.Println("done; run ppgmods again to use the new version")
	return nil
}

// cmdNexusProbe asks Nexus Mods' own API, with the linked account's key, for
// a download link of a mod's main file, the v1 way and the v3 way, and
// says which one gives it. Nothing is downloaded.
func cmdNexusProbe(arg string) error {
	modID, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || modID <= 0 {
		return fmt.Errorf("%q is not a mod id (the number in nexusmods.com/peopleplayground/mods/<id>)", arg)
	}
	acct := app.LoadNexus()
	if acct == nil {
		return errors.New("link your Nexus Mods account first (Settings → Nexus Mods, with your personal API key)")
	}
	kind := "free"
	if acct.Premium {
		kind = "premium"
	}
	logf("account: %s (%s)", acct.User, kind)
	fileID := sources.NXMainFileID(modID)
	if fileID <= 0 {
		return fmt.Errorf("mod %d: no main file found", modID)
	}
	logf("mod %d, main file %d", modID, fileID)
	host := func(link string) string {
		if u, err := url.Parse(link); err == nil {
			return u.Host
		}
		return "?"
	}
	if link, err := sources.NXDownloadURL(acct.Key, modID, fileID, "", ""); err != nil {
		logf("v1 download_link: refused: %v", err)
	} else {
		logf("v1 download_link: OK, a link on %s", host(link))
	}
	if link, exp, err := sources.NXRepackedLink(acct.Key, fileID); err != nil {
		logf("v3 download-repacked: refused: %v", err)
	} else {
		logf("v3 download-repacked: OK, a link on %s (expires %s)", host(link), exp.Local().Format("15:04"))
	}
	return nil
}

// cmdProfile is `ppgmods profile [list | save <name> | use <name> | delete <name>]`.
func cmdProfile(m *manager.Manager, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		names := m.State.ProfileNames()
		if len(names) == 0 {
			logf("no profiles yet; save the mods that are on now with: ppgmods profile save <name>")
		}
		for _, n := range names {
			cur := ""
			if n == m.State.Profile {
				cur = "  (current)"
			}
			logf("%s: %d mod(s)%s", n, len(m.State.Profiles[n]), cur)
		}
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: ppgmods profile [list | save <name> | use <name> | delete <name>]")
	}
	name := strings.Join(args[1:], " ")
	switch args[0] {
	case "save":
		return m.SaveProfile(name)
	case "use":
		missing, err := m.UseProfile(name)
		if len(missing) > 0 {
			logf("not installed (install them to have them on): %s", strings.Join(missing, ", "))
		}
		return err
	case "delete":
		return m.DeleteProfile(name)
	}
	return fmt.Errorf("usage: ppgmods profile [list | save <name> | use <name> | delete <name>]")
}
