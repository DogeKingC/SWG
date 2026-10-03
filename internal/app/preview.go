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
	Ref         string        `json:"ref"`
	Name        string        `json:"name"`
	Author      string        `json:"author,omitempty"`
	Version     string        `json:"version,omitempty"`
	Description string        `json:"description,omitempty"` // from mod.json
	Mods        int           `json:"mods"`                  // mod.json files in the archive
	Files       int           `json:"files"`
	Scripts     int           `json:"scripts"`
	ScanMax     string        `json:"scan_max"`
	Findings    []string      `json:"findings,omitempty"`
	Verdict     string        `json:"verdict"` // ok, review (overridable), blocked
	Reasons     []string      `json:"reasons,omitempty"`
	Thumb       bool          `json:"thumb"`
	Browser     *NeedsBrowser `json:"browser,omitempty"`
	Mirrors     []Mirror      `json:"mirrors,omitempty"` // Workshop items: every mirror copy, newest first
	Chosen      string        `json:"chosen,omitempty"`  // the copy this preview checked
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
	if strings.HasPrefix(ref, "sky:") {
		p.Mirrors, _ = WorkshopMirrors(strings.TrimPrefix(ref, "sky:"), a.Opt.Name)
	}
	c, err := a.Fetch(m, ref, nil)
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
	if len(roots) == 0 {
		p.Verdict = "blocked"
		p.Reasons = append(p.Reasons, "no mod.json found: this is not a C# mod (contraptions and skins are not supported yet)")
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
