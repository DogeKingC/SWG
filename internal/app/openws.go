package app

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/sources"
	"github.com/DogeKingC/SWG/internal/workshop"
)

// The Open Workshop (see package workshop): mods reviewed by maintainers
// before they are published, stored as permanent, checksummed files.

// OWResult is a search card for an Open Workshop entry.
func OWResult(e workshop.Entry) SearchResult {
	return SearchResult{Ref: "ow:" + e.Slug, Source: "Open Workshop", Name: e.Name, Author: e.Author,
		Date: FmtTime(e.Published), URL: workshop.Page(e.Slug), Image: e.Image, Kind: e.Kind, Version: e.Version,
		Reviewed: true, Downloads: e.Downloads, Size: HumanSize(e.Size), Category: strings.Join(e.Tags, ", ")}
}

func searchOW(r *SearchResults, q string, page int, opt SearchOpts) {
	ix, err := workshop.FetchIndex()
	if err != nil {
		if !strings.Contains(err.Error(), "nothing published yet") {
			r.Errors = append(r.Errors, "Open Workshop: "+err.Error())
		}
		return
	}
	var list []workshop.Entry
	for _, e := range ix.Entries {
		if !e.Withdrawn && e.Kind == opt.Kind {
			list = append(list, e)
		}
	}
	list = byRelevance(list, q, func(e workshop.Entry) string { return e.Name }, func(e workshop.Entry) string { return e.Author },
		func(e workshop.Entry) int { return e.Downloads })
	switch {
	case opt.Sort == "updated":
		sort.SliceStable(list, func(i, j int) bool { return list[i].Published.After(list[j].Published) })
	case opt.Sort == "popular" || strings.TrimSpace(q) == "":
		sort.SliceStable(list, func(i, j int) bool { return list[i].Downloads > list[j].Downloads })
	}
	const per = 24
	for i := (page - 1) * per; i < len(list) && i < page*per; i++ {
		r.OpenWS = append(r.OpenWS, OWResult(list[i]))
	}
}

// owEntry finds a published entry by slug.
func owEntry(slug string) (*workshop.Entry, error) {
	ix, err := workshop.FetchIndex()
	if err != nil {
		return nil, err
	}
	e := ix.Find(slug)
	if e == nil {
		return nil, fmt.Errorf("the Open Workshop has no %q", slug)
	}
	return e, nil
}

// owForWorkshop returns the Open Workshop entry that replaces a deleted
// Steam Workshop item, if any.
func owForWorkshop(ws string) *workshop.Entry {
	ix, err := workshop.FetchIndex()
	if err != nil || ws == "" {
		return nil
	}
	for i := range ix.Entries {
		if e := &ix.Entries[i]; e.WorkshopID == ws && !e.Withdrawn {
			return e
		}
	}
	return nil
}

// downloadOW downloads an entry's file into the cache and checks it is
// byte for byte the reviewed one.
func downloadOW(e *workshop.Entry) (string, error) {
	dir, err := CacheDir("ow:" + e.Slug)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, e.SHA256[:16]+"-"+filepath.Base(e.File))
	if sum, err := manager.FileSHA(dest); err == nil && sum == e.SHA256 {
		return dest, nil
	}
	if !strings.HasPrefix(e.File, "https://github.com/"+workshop.Repo+"/releases/download/") {
		return "", fmt.Errorf("Open Workshop entry %s points outside the Open Workshop's files", e.Slug)
	}
	part := dest + ".part"
	_, sum, err := sources.Download(e.File, part, workshop.MaxSize)
	if err != nil {
		os.Remove(part)
		return "", err
	}
	if sum != e.SHA256 {
		os.Remove(part)
		return "", fmt.Errorf("checksum mismatch: the Open Workshop reviewed %s, the download is %s", e.SHA256, sum)
	}
	return dest, os.Rename(part, dest)
}

// owCandidate turns a downloaded entry into an install candidate.
func (a *App) owCandidate(e *workshop.Entry, path string) (*manager.Candidate, error) {
	c, err := a.with(func(o *Options) { o.WorkshopID, o.Name = "", e.Name }).candidate(path)
	if err != nil {
		return nil, err
	}
	c.Key, c.Name, c.Version = "ow:"+e.Slug, e.Name, e.Version
	c.SteamOrig, c.Reviewed, c.Revision = false, true, e.Published // reviewed: no cooldown
	c.Mirror, c.Source = "openworkshop:"+e.Slug, workshop.Page(e.Slug)
	if e.WorkshopID != "" {
		c.Aliases = append(c.Aliases, "sky:"+e.WorkshopID)
	}
	return c, nil
}

// fetchOW downloads an Open Workshop mod. With UpdateOnly, a nil candidate
// means prev is already the published file.
func (a *App) fetchOW(slug string, prev *manager.Installed) (*manager.Candidate, error) {
	e, err := owEntry(slug)
	if err != nil {
		return nil, err
	}
	if e.Withdrawn {
		return nil, &manager.Rejection{Reasons: []string{"withdrawn from the Open Workshop: " + e.WithdrawnReason + " (cannot be overridden)"}}
	}
	if a.Opt.UpdateOnly && prev != nil && prev.ArchiveSHA == e.SHA256 {
		return nil, nil
	}
	a.logf("ow:%s %s%s - downloading %s from the Open Workshop (reviewed)", e.Slug, e.Name, By(e.Author), e.Version)
	path, err := downloadOW(e)
	if err != nil {
		return nil, err
	}
	return a.owCandidate(e, path)
}

// owUpdate checks an installed item against the Open Workshop: an item
// installed from it, or a Workshop item its author republished there with a
// higher version. It returns nil when there is nothing newer.
func (a *App) owUpdate(inst *manager.Installed) (*manager.Candidate, error) {
	var e *workshop.Entry
	switch {
	case strings.HasPrefix(inst.Key, "ow:"):
		var err error
		if e, err = owEntry(strings.TrimPrefix(inst.Key, "ow:")); err != nil {
			return nil, err
		}
		if e.Withdrawn {
			a.logf("%s was WITHDRAWN from the Open Workshop: %s. Consider removing it.", inst.Name, e.WithdrawnReason)
			return nil, nil
		}
		if e.SHA256 == inst.ArchiveSHA {
			return nil, nil
		}
	case strings.HasPrefix(inst.Key, "sky:"):
		e = owForWorkshop(strings.TrimPrefix(inst.Key, "sky:"))
		if e == nil || e.SHA256 == inst.ArchiveSHA || CompareVersions(e.Version, inst.Version) <= 0 {
			return nil, nil
		}
		a.logf("%s: its author published version %s on the Open Workshop (you have %s)", inst.Name, e.Version, orUnknown(inst.Version))
	default:
		return nil, nil
	}
	path, err := downloadOW(e)
	if err != nil {
		return nil, err
	}
	c, err := a.owCandidate(e, path)
	if err != nil {
		return nil, err
	}
	c.Key = inst.Key // update in place
	if strings.HasPrefix(inst.Key, "sky:") {
		c.Aliases = append(c.Aliases, "ow:"+e.Slug)
	}
	return c, nil
}

// owMirror is the Open Workshop's copy of a Workshop item.
func owMirror(e workshop.Entry) Mirror {
	return Mirror{ID: "openworkshop:" + e.Slug, Source: "Open Workshop", Title: e.Name, Author: e.Author,
		Version: "v" + e.Version + " (reviewed)", VersionTime: e.Published, Size: HumanSize(e.Size),
		Page: workshop.Page(e.Slug), Image: e.Image, Reviewed: true, ModVersion: e.Version, ow: &e}
}

// Share is what "Share on the Open Workshop" prepares for an installed mod.
type Share struct {
	Zip        string `json:"zip"`
	SHA256     string `json:"sha256"`
	Slug       string `json:"slug"`
	Submission string `json:"submission"` // submission.json to propose
	NewFileURL string `json:"new_file_url"`
}

// PrepareShare zips an installed item's folders and drafts its
// submission.json. The author uploads the zip (a link anyone can download)
// and proposes the file on GitHub, where it is checked and reviewed.
func (a *App) PrepareShare(m *manager.Manager, key, outDir string) (*Share, error) {
	inst := m.State.Mods[key]
	if inst == nil {
		return nil, fmt.Errorf("%s is not installed", key)
	}
	base := m.ModsDir
	if inst.Kind == manager.KindContraption {
		base = m.ContraptionsDir
	}
	slug := strings.Trim(reSlugChars.ReplaceAllString(strings.ToLower(inst.Name), "-"), "-")
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if !workshop.ValidSlug(slug) {
		slug = "mod-" + strings.ReplaceAll(strings.TrimPrefix(key, "local:"), ":", "-")
	}
	ver := inst.Version
	if ver == "" {
		ver = "1.0"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	zipPath := filepath.Join(outDir, slug+"-"+reSlugChars.ReplaceAllString(ver, "-")+".zip")
	if err := zipFolders(zipPath, base, inst.Folders); err != nil {
		return nil, err
	}
	f, err := os.Open(zipPath)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	io.Copy(h, f)
	f.Close()
	sum := hex.EncodeToString(h.Sum(nil))
	kind := inst.Kind
	if kind == "" {
		kind = manager.KindMod
	}
	ws := ""
	if strings.HasPrefix(key, "sky:") {
		ws = strings.TrimPrefix(key, "sky:")
	}
	sub := workshop.Submission{Name: inst.Name, Author: inst.Author, Kind: kind, Version: ver,
		Download: "https://PASTE-THE-LINK-TO-THE-ZIP-HERE", SHA256: sum, WorkshopID: ws,
		Maintainers: []string{"YOUR-GITHUB-USERNAME"}}
	b, _ := jsonIndent(sub)
	return &Share{Zip: zipPath, SHA256: sum, Slug: slug, Submission: string(b),
		NewFileURL: "https://github.com/" + workshop.Repo + "/new/main?filename=" +
			urlQuery("workshop/submissions/"+slug+"/submission.json") + "&value=" + urlQuery(string(b))}, nil
}

var errNoFolders = errors.New("nothing to share: its folders are missing")

var reSlugChars = regexp.MustCompile(`[^a-z0-9.]+`)

func jsonIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

func urlQuery(s string) string { return url.QueryEscape(s) }

// zipFolders writes the folders (inside base) into a new zip.
func zipFolders(dest, base string, folders []string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	n := 0
	for _, f := range folders {
		root := filepath.Join(base, f)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
				return err
			}
			rel, _ := filepath.Rel(base, p)
			w, err := zw.Create(filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, in)
			in.Close()
			n++
			return err
		})
		if err != nil {
			zw.Close()
			out.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if n == 0 {
		os.Remove(dest)
		return errNoFolders
	}
	return nil
}
