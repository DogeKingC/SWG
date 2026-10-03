// Command ppgmods recovers, installs and updates People Playground C# mods
// from community mirrors, scanning every mod before it reaches the Mods folder.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/game"
	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/scan"
	"github.com/DogeKingC/SWG/internal/sources"
)

var version = "dev"

const usage = `ppgmods - People Playground mod recovery and update tool

Usage: ppgmods <command> [args] [flags]

Find and install:
  search <text>              search GameBanana and Skymods (Workshop mirror)
  install gb:<id>            download, scan and install a GameBanana mod (id or URL)
  install sky:<workshop id>  look up a Skymods mirror copy and open its download page
  import <file|folder>       scan and install a downloaded archive or mod folder
                             (--workshop-id <id> for Workshop/Skymods copies)

Keep up to date:
  update                     check GameBanana mods for new files (dry run)
  update --yes               apply updates that pass every safety check
  list                       installed mods
  pin|unpin <key>            stop/resume updates for a mod
  rollback <key>             restore the previous version (and pin it)
  remove <key>               uninstall

Safety and recovery:
  scan <path>                scan an archive or folder without installing
  verify                     detect tampered/injected files in installed mods
                             and scan Mods folders ppgmods did not install
  backup-workshop            copy the Steam Workshop cache before Steam deletes it
  restore-workshop <dir>     import every item from a backup-workshop folder
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
`

type opts struct {
	game, dest, workshopID, name string
	revision                     time.Time // known revision date (from a backup manifest)
	yes, offline                 bool
	fileID                       int
	policy                       manager.Policy
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Print(usage)
		return
	}
	sources.UserAgent = fmt.Sprintf("ppgmods/%s (+https://github.com/DogeKingC/SWG)", version)
	cmd := os.Args[1]
	o, args, err := parseFlags(os.Args[2:])
	if err != nil {
		fatal(err)
	}
	if err := run(cmd, args, o); err != nil {
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

func parseFlags(argv []string) (*opts, []string, error) {
	o := &opts{}
	fs := flag.NewFlagSet("ppgmods", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(usage) }
	fs.StringVar(&o.game, "game", "", "")
	fs.StringVar(&o.dest, "dest", "", "")
	fs.StringVar(&o.workshopID, "workshop-id", "", "")
	fs.StringVar(&o.name, "name", "", "")
	fs.IntVar(&o.fileID, "file", 0, "")
	fs.BoolVar(&o.yes, "yes", false, "")
	fs.BoolVar(&o.offline, "offline", false, "")
	fs.BoolVar(&o.policy.AllowHigh, "allow-high", false, "")
	fs.BoolVar(&o.policy.AllowCritical, "allow-critical", false, "")
	fs.BoolVar(&o.policy.AllowAfterCutoff, "allow-after-cutoff", false, "")
	fs.BoolVar(&o.policy.AllowNewFindings, "allow-new-findings", false, "")
	fs.BoolVar(&o.policy.DryRun, "dry-run", false, "")
	fs.DurationVar(&o.policy.Cooldown, "cooldown", 48*time.Hour, "")
	var pos []string
	for {
		if err := fs.Parse(argv); err != nil {
			return nil, nil, err
		}
		argv = fs.Args()
		if len(argv) == 0 {
			break
		}
		pos = append(pos, argv[0])
		argv = argv[1:]
	}
	return o, pos, nil
}

func logf(format string, a ...any) { fmt.Printf(format+"\n", a...) }

func newManager(o *opts, needGame bool) (*manager.Manager, error) {
	st, err := manager.LoadState()
	if err != nil {
		return nil, err
	}
	m := &manager.Manager{State: st, Policy: o.policy, Log: logf}
	if needGame {
		dir, err := game.FindGameDir(o.game)
		if err != nil {
			return nil, err
		}
		m.ModsDir = game.ModsDir(dir)
		m.Blocklist = manager.LoadBlocklist(!o.offline, logf)
	}
	return m, nil
}

func need(args []string, n int, what string) error {
	if len(args) < n {
		return fmt.Errorf("missing %s (see `ppgmods help`)", what)
	}
	return nil
}

func run(cmd string, args []string, o *opts) error {
	switch cmd {
	case "version":
		fmt.Println("ppgmods", version)
		return nil
	case "paths":
		return cmdPaths(o)
	case "search":
		if err := need(args, 1, "search text"); err != nil {
			return err
		}
		return cmdSearch(strings.Join(args, " "))
	case "scan":
		if err := need(args, 1, "path"); err != nil {
			return err
		}
		return cmdScan(args[0], o)
	case "backup-workshop":
		return cmdBackup(o)
	}

	m, err := newManager(o, cmd != "list")
	if err != nil {
		return err
	}
	switch cmd {
	case "install":
		if err := need(args, 1, "mod reference"); err != nil {
			return err
		}
		return cmdInstall(m, args[0], o)
	case "import":
		if err := need(args, 1, "file or folder"); err != nil {
			return err
		}
		return cmdImport(m, args[0], o)
	case "restore-workshop":
		if err := need(args, 1, "backup folder"); err != nil {
			return err
		}
		return cmdRestore(m, args[0], o)
	case "update":
		return cmdUpdate(m, o)
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
		i := m.State.Mods[args[0]]
		if i == nil {
			return fmt.Errorf("%s is not installed", args[0])
		}
		i.Pinned = cmd == "pin"
		return m.State.Save()
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
	case "verify":
		return cmdVerify(m)
	}
	return fmt.Errorf("unknown command %q (see `ppgmods help`)", cmd)
}

func cmdPaths(o *opts) error {
	fmt.Println("Steam libraries:")
	for _, l := range game.Libraries() {
		fmt.Println("  " + l)
	}
	if dir, err := game.FindGameDir(o.game); err == nil {
		fmt.Println("Game:   " + dir)
		fmt.Println("Mods:   " + game.ModsDir(dir))
	} else {
		fmt.Println("Game:   " + err.Error())
	}
	fmt.Println("Workshop cache:")
	for _, w := range game.WorkshopDirs() {
		fmt.Println("  " + w)
	}
	if d, err := manager.ConfigDir(); err == nil {
		fmt.Println("ppgmods data: " + d)
	}
	return nil
}

func cmdSearch(q string) error {
	gb, gerr := sources.GBSearch(q, 1)
	fmt.Println("GameBanana (install with: ppgmods install gb:<id>)")
	if gerr != nil {
		fmt.Println("  error:", gerr)
	}
	for _, m := range gb {
		fmt.Printf("  gb:%-8d %-45s %-12s by %s, updated %s\n", m.ID, m.Name, m.Category.Name, m.Submitter.Name, time.Unix(m.Modified, 0).Format("2006-01-02"))
	}
	sky, serr := sources.SkySearch(q, 1)
	fmt.Println("Skymods Workshop mirror (ppgmods install sky:<workshop id>)")
	if serr != nil {
		fmt.Println("  error:", serr)
	}
	for _, it := range sky {
		warn := ""
		if !it.Revision.IsZero() && !it.Revision.Before(manager.WormCutoff) {
			warn = "  [after worm cutoff]"
		}
		fmt.Printf("  sky:%-11s %-45s by %s, revision %s%s\n", it.WorkshopID, it.Title, it.Author, fmtTime(it.Revision), warn)
	}
	return nil
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("2006-01-02")
}

func cmdScan(p string, o *opts) error {
	m := &manager.Manager{Log: logf, Blocklist: manager.LoadBlocklist(!o.offline, logf)}
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
	fmt.Printf("%d files, %d C# scripts, highest: %s\n", rep.Files, rep.Scripts, sevName(rep))
	if rep.Max() >= scan.High {
		os.Exit(3)
	}
	return nil
}

func sevName(r *scan.Report) string {
	if r.Max() < 0 {
		return "none"
	}
	return r.Max().String()
}

var reGBURL = regexp.MustCompile(`gamebanana\.com/mods/(\d+)`)

func cmdInstall(m *manager.Manager, ref string, o *opts) error {
	if mm := reGBURL.FindStringSubmatch(ref); mm != nil {
		ref = "gb:" + mm[1]
	}
	switch {
	case strings.HasPrefix(ref, "gb:"):
		id, err := strconv.Atoi(strings.TrimPrefix(ref, "gb:"))
		if err != nil {
			return fmt.Errorf("bad GameBanana id %q", ref)
		}
		return installGB(m, id, o.fileID, nil)
	case strings.HasPrefix(ref, "sky:"):
		return openSky(strings.TrimPrefix(ref, "sky:"), o)
	}
	return fmt.Errorf("unknown reference %q; use gb:<id>, a GameBanana URL, or sky:<workshop id>", ref)
}

// installGB downloads and installs a GameBanana file. prev is non-nil for updates.
func installGB(m *manager.Manager, id, fileID int, prev *manager.Installed) error {
	mod, err := sources.GBGetMod(id)
	if err != nil {
		return err
	}
	files, err := sources.GBFiles(id)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("gb:%d has no files", id)
	}
	f := files[0]
	if fileID != 0 {
		found := false
		for _, x := range files {
			if x.ID == fileID {
				f, found = x, true
			}
		}
		if !found {
			return fmt.Errorf("file %d not found in gb:%d", fileID, id)
		}
	}
	if prev != nil && prev.FileID == f.ID {
		return nil
	}
	if !f.Clean() {
		return &manager.Rejection{Reasons: []string{fmt.Sprintf("GameBanana malware analysis for %s is %q/%q/%q, not clean", f.Name, f.AVState, f.AVResult, f.Analysis)}}
	}
	logf("gb:%d %s - file %s (%s, uploaded %s)", id, mod.Name, f.Name, humanSize(f.Size), f.AddedTime().Format("2006-01-02 15:04"))
	dl, err := downloadsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dl, fmt.Sprintf("gb-%d-%d-%s", id, f.ID, filepath.Base(f.Name)))
	md5hex, shahex, err := sources.Download(f.DownloadURL, path, 1<<30)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if f.MD5 != "" && !strings.EqualFold(md5hex, f.MD5) {
		return &manager.Rejection{Reasons: []string{fmt.Sprintf("checksum mismatch: GameBanana says %s, got %s", f.MD5, md5hex)}}
	}
	return m.Install(&manager.Candidate{
		Key: fmt.Sprintf("gb:%d", id), Name: mod.Name, Source: mod.URL, Path: path,
		FileID: f.ID, Version: f.Version, Revision: f.AddedTime(), ArchiveSHA: shahex,
	})
}

func downloadsDir() (string, error) {
	d, err := manager.ConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, "downloads")
	return p, os.MkdirAll(p, 0o755)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func openSky(ws string, o *opts) error {
	it, err := sources.SkyByWorkshopID(ws)
	if err != nil {
		return err
	}
	logf("Skymods: %s by %s (Workshop %s), revision %s, %s", it.Title, it.Author, it.WorkshopID, fmtTime(it.Revision), it.Size)
	if !it.Revision.IsZero() && !it.Revision.Before(manager.WormCutoff) && !o.policy.AllowAfterCutoff {
		return &manager.Rejection{Reasons: []string{fmt.Sprintf("mirrored copy was revised %s, on/after the worm cutoff; it may contain the worm (override: --allow-after-cutoff)", it.Revision.Format("2006-01-02 15:04"))}}
	}
	if it.DownloadURL == "" {
		return fmt.Errorf("no download link on %s", it.PageURL)
	}
	logf("")
	logf("modsbase.com blocks automated downloads (Cloudflare check), so the page opens in your browser.")
	logf("Download the .zip (ignore ads and anything that is not the mod file, never run an .exe), then run:")
	logf("  ppgmods import <downloaded file> --workshop-id %s", ws)
	logf("")
	logf("Download page: %s", it.DownloadURL)
	openBrowser(it.DownloadURL)
	return nil
}

func openBrowser(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		c = exec.Command("open", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	_ = c.Start()
}

func cmdImport(m *manager.Manager, p string, o *opts) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	ws := o.workshopID
	if ws == "" && st.IsDir() && regexp.MustCompile(`^\d{6,12}$`).MatchString(filepath.Base(abs)) {
		ws = filepath.Base(abs) // a folder copied from steamapps/workshop/content/1118200
	}
	c := &manager.Candidate{Path: abs, Name: o.name}
	if !st.IsDir() {
		if c.ArchiveSHA, err = manager.FileSHA(abs); err != nil {
			return err
		}
	}
	if ws != "" {
		c.Key, c.SteamOrig, c.Source = "sky:"+ws, true, "https://steamcommunity.com/sharedfiles/filedetails/?id="+ws
		if it, err := sources.SkyByWorkshopID(ws); err == nil && !it.Revision.IsZero() && !st.IsDir() {
			c.Revision = it.Revision
			if c.Name == "" {
				c.Name = it.Title
			}
			logf("Skymods lists Workshop %s revision %s", ws, it.Revision.Format("2006-01-02 15:04"))
		} else if st.IsDir() {
			c.Revision = o.revision
			if c.Revision.IsZero() {
				c.Revision = manager.NewestMtime(abs)
			}
			logf("using newest file time %s as the revision date", fmtTime(c.Revision))
		}
	} else {
		if c.ArchiveSHA == "" {
			h, err := manager.TreeSHA(abs)
			if err != nil {
				return err
			}
			c.ArchiveSHA = h
		}
		c.Key, c.Source = "local:"+c.ArchiveSHA[:12], abs
	}
	if c.Name == "" {
		c.Name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	return m.Install(c)
}

func cmdRestore(m *manager.Manager, dir string, o *opts) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	dates := manager.ReadBackupDates(dir)
	if dates == nil {
		logf("warning: no manifest.json in %s; dating items by file times, which may be the copy time", dir)
	}
	var ok, refused, failed int
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		oo := *o
		oo.workshopID = e.Name()
		oo.revision = dates[e.Name()]
		logf("== %s", e.Name())
		err := cmdImport(m, filepath.Join(dir, e.Name()), &oo)
		var rej *manager.Rejection
		switch {
		case err == nil:
			ok++
		case errors.As(err, &rej):
			refused++
			for _, r := range rej.Reasons {
				logf("  REFUSED: %s", r)
			}
		default:
			failed++
			logf("  error: %v", err)
		}
	}
	logf("restore finished: %d installed, %d refused, %d errors", ok, refused, failed)
	return nil
}

func cmdUpdate(m *manager.Manager, o *opts) error {
	if !o.yes {
		m.Policy.DryRun = true
		logf("dry run (add --yes to apply updates that pass every check)")
	}
	var updated, held, current int
	for _, inst := range m.State.Sorted() {
		if !strings.HasPrefix(inst.Key, "gb:") {
			continue // Workshop copies are frozen: Steam no longer hosts them
		}
		if inst.Pinned {
			logf("%s pinned, skipped", inst.Key)
			continue
		}
		id, _ := strconv.Atoi(strings.TrimPrefix(inst.Key, "gb:"))
		files, err := sources.GBFiles(id)
		if err != nil {
			logf("%s: %v", inst.Key, err)
			continue
		}
		if len(files) == 0 || files[0].ID == inst.FileID {
			current++
			continue
		}
		err = installGB(m, id, 0, inst)
		var rej *manager.Rejection
		switch {
		case err == nil:
			updated++
		case errors.As(err, &rej):
			held++
			for _, r := range rej.Reasons {
				logf("  HELD: %s", r)
			}
		default:
			held++
			logf("  error: %v", err)
		}
	}
	verb := "updated"
	if m.Policy.DryRun {
		verb = "would update"
	}
	logf("%d up to date, %d %s, %d held back", current, updated, verb, held)
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
		if !strings.HasSuffix(p.Issue, "scan clean") {
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

func cmdBackup(o *opts) error {
	src := game.WorkshopDirs()
	if len(src) == 0 {
		return errors.New("no People Playground Workshop cache found (steamapps/workshop/content/1118200)")
	}
	dest := o.dest
	if dest == "" {
		dest = "ppg-workshop-backup-" + time.Now().Format("20060102")
	}
	logf("backing up %s -> %s", strings.Join(src, ", "), dest)
	items, err := manager.BackupWorkshop(src, dest, logf)
	if err != nil {
		return err
	}
	after := 0
	for _, it := range items {
		if it.AfterCutoff {
			after++
		}
	}
	logf("backed up %d items (%d modified after the worm cutoff). Manifest: %s", len(items), after, filepath.Join(dest, "manifest.json"))
	logf("Install the safe ones with: ppgmods restore-workshop %s", dest)
	return nil
}
