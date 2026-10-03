package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/scan"
)

// Preview is the pre-install check shown in the GUI's overview: what is in
// the archive, what the scanner found, and whether ppgmods would install it.
type Preview struct {
	Ref          string        `json:"ref"`
	Name         string        `json:"name"`
	Author       string        `json:"author,omitempty"`
	Version      string        `json:"version,omitempty"`
	Description  string        `json:"description,omitempty"` // from mod.json
	Mods         int           `json:"mods"`                  // mod.json files in the archive
	Files        int           `json:"files"`
	Scripts      int           `json:"scripts"`
	ScanMax      string        `json:"scan_max"`
	Findings     []string      `json:"findings,omitempty"`
	Verdict      string        `json:"verdict"` // ok, review (overridable), blocked
	Reasons      []string      `json:"reasons,omitempty"`
	Thumb        bool          `json:"thumb"`
	Kind         string        `json:"kind"`
	Contraptions []string      `json:"contraptions,omitempty"`
	Browser      *NeedsBrowser `json:"browser,omitempty"`
	Mirrors      []Mirror      `json:"mirrors,omitempty"` // Workshop items: every mirror copy, newest first
	Chosen       string        `json:"chosen,omitempty"`  // the copy this preview checked
}

type modJSON struct {
	Name          string `json:"Name"`
	Author        string `json:"Author"`
	Description   string `json:"Description"`
	ModVersion    string `json:"ModVersion"`
	ThumbnailPath string `json:"ThumbnailPath"`
}

// Preview downloads (or reuses) a mod, scans it and applies the install
// policy without changing the Mods folder.
func (a *App) Preview(m *manager.Manager, ref string) (*Preview, error) {
	ref = NormalizeRef(ref)
	p := &Preview{Ref: ref}
	c, err := a.Fetch(m, ref, nil)
	if strings.HasPrefix(ref, "sky:") {
		// After the download, so each copy's mod.json version is known.
		if list, merr := WorkshopMirrors(strings.TrimPrefix(ref, "sky:"), a.Opt.Name); merr == nil {
			p.Mirrors = append([]Mirror(nil), list...)
			fillModVersions(p.Mirrors)
		}
	}
	var nb *NeedsBrowser
	var rej *manager.Rejection
	var un *Unavailable
	switch {
	case errors.As(err, &nb):
		p.Verdict, p.Browser = "browser", nb
		p.Reasons = []string{nb.Error()}
		return p, nil
	case errors.As(err, &rej):
		p.Verdict, p.Reasons = "blocked", rej.Reasons
		return p, nil
	case errors.As(err, &un):
		p.Verdict, p.Reasons = "unavailable", []string{un.Reason}
		return p, nil
	case err != nil:
		return nil, err
	}
	dir, rep, err := m.Stage(c)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p.Name, p.Files, p.Scripts, p.Chosen = c.Name, rep.Files, rep.Scripts, c.Mirror
	p.ScanMax = "none"
	if rep.Max() >= 0 {
		p.ScanMax = rep.Max().String()
	}
	for _, f := range rep.Findings {
		if f.Severity >= scan.Medium && len(p.Findings) < 30 {
			p.Findings = append(p.Findings, "["+f.Severity.String()+" "+f.Rule+"] "+filepath.ToSlash(f.File)+": "+f.Detail)
		}
	}
	roots, _ := modRoots(dir)
	p.Mods = len(roots)
	if len(roots) > 0 {
		var mj modJSON
		if b, err := os.ReadFile(filepath.Join(roots[0], "mod.json")); err == nil {
			json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &mj)
		}
		if mj.Name != "" {
			p.Name = mj.Name
		}
		p.Author, p.Version, p.Description = mj.Author, mj.ModVersion, strings.TrimSpace(mj.Description)
		p.Thumb = saveThumb(roots[0], mj.ThumbnailPath, c.Key)
	}

	p.Verdict = "ok"
	if err := m.Check(c, rep, m.State.Mods[c.Key]); err != nil {
		if errors.As(err, &rej) {
			p.Reasons = rej.Reasons
			p.Verdict = "review"
			for _, r := range rej.Reasons {
				if !manager.Overridable(r) {
					p.Verdict = "blocked"
				}
			}
		} else {
			return nil, err
		}
	}
	p.Kind = manager.KindMod
	if len(roots) == 0 {
		if cs, err := contraptions(dir); err == nil && len(cs) > 0 {
			p.Kind, p.Contraptions = manager.KindContraption, cs
			p.Thumb = saveContraptionThumb(dir, c.Key)
		} else {
			p.Verdict = "blocked"
			p.Reasons = append(p.Reasons, "found neither a mod (mod.json) nor a contraption (.jaap); skins and other content are not supported")
		}
	}
	return p, nil
}

func modRoots(dir string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), "mod.json") {
			roots = append(roots, filepath.Dir(p))
			return filepath.SkipDir
		}
		return nil
	})
	return roots, err
}

// saveThumb copies the mod's own thumbnail into the cache so the GUI can show
// it (Valve deleted the Workshop preview images along with the mods).
func saveThumb(root, rel, key string) bool {
	var cands []string
	if rel != "" {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if !filepath.IsAbs(clean) && !strings.HasPrefix(clean, "..") {
			cands = append(cands, filepath.Join(root, clean))
		}
	}
	for _, n := range []string{"thumb.png", "thumbnail.png", "thumb.jpg", "icon.png"} {
		cands = append(cands, filepath.Join(root, n))
	}
	for _, c := range cands {
		b, err := os.ReadFile(c)
		if err != nil || len(b) > 8<<20 || ImageType(b) == "" {
			continue
		}
		dir, err := CacheDir(key)
		if err != nil {
			return false
		}
		return os.WriteFile(filepath.Join(dir, "thumb.img"), b, 0o644) == nil
	}
	return false
}

// ImageType returns the MIME type of a PNG/JPEG/GIF/WebP image, or "".
func ImageType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF8")):
		return "image/gif"
	case len(b) > 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

// ThumbPath returns the cached thumbnail for a ref, if one was extracted.
func ThumbPath(ref string) string {
	dir, err := CacheDir(NormalizeRef(ref))
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "thumb.img")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// contraptions lists the contraption names (.jaap files) in a staged archive.
func contraptions(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".jaap") {
			out = append(out, strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
		}
		return err
	})
	return out, err
}

// saveContraptionThumb caches the first contraption's .png as the thumbnail.
func saveContraptionThumb(dir, key string) bool {
	found := false
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if found || err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".png") {
			return err
		}
		found = saveThumb(filepath.Dir(p), d.Name(), key)
		return nil
	})
	return found
}
