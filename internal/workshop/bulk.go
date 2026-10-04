package workshop

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Bulk publishing: the owner uploads a folder of old Steam Workshop items
// (one folder per item, named by its Workshop ID, as Steam keeps them in
// steamapps/workshop/content/1118200). PackBulk, on the owner's PC, zips
// each item and lists it in bulk.json; the zips and bulk.json go to a
// GitHub release; the workflow's "bulk" action checks each like a
// submission and publishes what passes, credited to the original author.

// BulkItem is one item of bulk.json.
type BulkItem struct {
	WorkshopID  string    `json:"workshop_id"`
	Name        string    `json:"name"`
	Author      string    `json:"author"`
	Kind        string    `json:"kind"` // mod or contraption
	Version     string    `json:"version"`
	Description string    `json:"description,omitempty"`
	File        string    `json:"file"` // asset name in the upload release
	SHA256      string    `json:"sha256"`
	Newest      time.Time `json:"newest"` // newest file time in the folder
}

// BulkManifest is bulk.json.
type BulkManifest struct {
	Items []BulkItem `json:"items"`
}

var reWSFolder = regexp.MustCompile(`^\d{6,12}$`)

// PackBulk zips every Workshop-ID folder under src into out and writes
// out/bulk.json. Folders that are neither a mod nor a contraption, or too
// large, are skipped with a log line.
func PackBulk(src, out string, logf func(string, ...any)) (*BulkManifest, error) {
	ents, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	m := &BulkManifest{}
	for _, e := range ents {
		if !e.IsDir() || !reWSFolder.MatchString(e.Name()) {
			continue
		}
		it, err := packItem(filepath.Join(src, e.Name()), e.Name(), out)
		if err != nil {
			logf("  %s: skipped: %v", e.Name(), err)
			continue
		}
		logf("  %s: %s %q by %s", it.WorkshopID, it.Kind, it.Name, it.Author)
		m.Items = append(m.Items, *it)
	}
	sort.Slice(m.Items, func(i, j int) bool { return m.Items[i].WorkshopID < m.Items[j].WorkshopID })
	b, _ := json.MarshalIndent(m, "", "  ")
	return m, os.WriteFile(filepath.Join(out, "bulk.json"), b, 0o644)
}

func packItem(dir, id, out string) (*BulkItem, error) {
	it := &BulkItem{WorkshopID: id, Version: "1.0"}
	var modJSON, jaap string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(it.Newest) {
			it.Newest = info.ModTime().UTC()
		}
		switch {
		case modJSON == "" && strings.EqualFold(d.Name(), "mod.json"):
			modJSON = p
		case jaap == "" && strings.EqualFold(filepath.Ext(p), ".jaap"):
			jaap = p
		}
		return nil
	})
	switch {
	case modJSON != "":
		it.Kind = "mod"
		var mj struct{ Name, Author, ModVersion, Description string }
		if b, err := os.ReadFile(modJSON); err == nil {
			json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &mj)
		}
		it.Name, it.Author, it.Description = mj.Name, mj.Author, mj.Description
		if reVersion.MatchString(strings.TrimSpace(mj.ModVersion)) {
			it.Version = strings.TrimSpace(mj.ModVersion)
		}
	case jaap != "":
		it.Kind = "contraption"
		it.Name = strings.TrimSuffix(filepath.Base(jaap), filepath.Ext(jaap))
		var meta struct{ DisplayName, Name, Author, Creator string }
		if b, err := os.ReadFile(strings.TrimSuffix(jaap, filepath.Ext(jaap)) + ".json"); err == nil {
			json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &meta)
		}
		if meta.DisplayName != "" {
			it.Name = meta.DisplayName
		} else if meta.Name != "" {
			it.Name = meta.Name
		}
		it.Author = meta.Author
		if it.Author == "" {
			it.Author = meta.Creator
		}
	default:
		return nil, fmt.Errorf("neither a mod (mod.json) nor a contraption (.jaap)")
	}
	it.Name = clipText(strings.TrimSpace(it.Name), 80)
	if it.Name == "" {
		it.Name = "Workshop item " + id
	}
	it.Author = clipText(strings.TrimSpace(it.Author), 60)
	if it.Author == "" {
		it.Author = "unknown (Steam Workshop " + id + ")"
	}
	it.Description = clipText(strings.TrimSpace(it.Description), 3000)
	it.File = id + ".zip"
	sum, size, err := zipDir(dir, id, filepath.Join(out, it.File))
	if err != nil {
		return nil, err
	}
	if size > MaxSize {
		os.Remove(filepath.Join(out, it.File))
		return nil, fmt.Errorf("larger than %d MB packed", MaxSize>>20)
	}
	it.SHA256 = sum
	return it, nil
}

// zipDir writes dir (as top folder top) into a zip, keeping file times,
// and returns its SHA-256 and size. Symlinks are left out.
func zipDir(dir, top, dest string) (string, int64, error) {
	f, err := os.Create(dest)
	if err != nil {
		return "", 0, err
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name, h.Method = top+"/"+filepath.ToSlash(rel), zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, in)
		in.Close()
		return err
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return "", 0, err
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		return "", 0, err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), int64(len(b)), nil
}

// BulkSlug is a bulk item's entry name: its name and Workshop ID, so two
// Workshop items with the same name don't collide.
func BulkSlug(it BulkItem) string {
	name := Slugify(it.Name)
	if max := 63 - len(it.WorkshopID); len(name) > max {
		name = strings.Trim(name[:max], "-")
	}
	if name == "" {
		return "workshop-" + it.WorkshopID
	}
	return name + "-" + it.WorkshopID
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
