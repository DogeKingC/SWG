package manager

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Trlydev/SWG/internal/scan"
)

type BackupItem struct {
	WorkshopID   string            `json:"workshop_id"`
	NewestFile   time.Time         `json:"newest_file_mtime"`
	AfterCutoff  bool              `json:"after_worm_cutoff"`
	InFebWindow  bool              `json:"in_february_window"`
	ScanMax      string            `json:"scan_max"`
	FindingCount int               `json:"finding_count"`
	Files        map[string]string `json:"files"`
}

// BackupWorkshop copies every People Playground Workshop item out of the
// Steam cache into dest and writes manifest.json. Steam removes items Valve
// deleted the next time it syncs, so this should run before Steam goes online.
func BackupWorkshop(srcDirs []string, dest string, logf func(string, ...any)) ([]BackupItem, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	var items []BackupItem
	if b, err := os.ReadFile(filepath.Join(dest, "manifest.json")); err == nil {
		var mf struct {
			Items []BackupItem `json:"items"`
		}
		if json.Unmarshal(b, &mf) == nil {
			items = mf.Items
		}
	}
	for _, src := range srcDirs {
		ents, err := os.ReadDir(src)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			from := filepath.Join(src, e.Name())
			to := filepath.Join(dest, e.Name())
			if _, err := os.Stat(to); err == nil {
				logf("  %s: already in backup, skipped", e.Name())
				continue
			}
			if err := copyTree(from, to); err != nil {
				return items, fmt.Errorf("%s: %w", e.Name(), err)
			}
			it := BackupItem{WorkshopID: e.Name(), Files: map[string]string{}}
			filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if info, err := d.Info(); err == nil && info.ModTime().After(it.NewestFile) {
						it.NewestFile = info.ModTime().UTC()
					}
				}
				return nil
			})
			if err := hashTree(to, ".", it.Files); err != nil {
				return items, err
			}
			it.AfterCutoff = !it.NewestFile.Before(WormCutoff)
			it.InFebWindow = !it.NewestFile.Before(FebruaryWindow[0]) && it.NewestFile.Before(FebruaryWindow[1])
			rep, err := scan.Dir(to)
			if err != nil {
				return items, err
			}
			it.ScanMax, it.FindingCount = maxName(rep), len(rep.Findings)
			flag := ""
			if it.AfterCutoff {
				flag = "  <-- modified after worm cutoff, treat as infected"
			} else if rep.Max() >= scan.High {
				flag = "  <-- review findings"
			}
			logf("  %s: %d files, newest %s, scan %s%s", it.WorkshopID, len(it.Files), it.NewestFile.Format("2006-01-02"), it.ScanMax, flag)
			items = append(items, it)
		}
	}
	b, _ := json.MarshalIndent(map[string]any{
		"created":     time.Now().UTC(),
		"worm_cutoff": WormCutoff,
		"items":       items,
	}, "", "  ")
	return items, os.WriteFile(filepath.Join(dest, "manifest.json"), b, 0o644)
}

// NewestMtime returns the latest file modification time under dir.
func NewestMtime(dir string) time.Time {
	var t time.Time
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil && info.ModTime().After(t) {
				t = info.ModTime().UTC()
			}
		}
		return nil
	})
	return t
}

// ReadBackupDates returns the newest original file time per Workshop ID from a
// backup-workshop manifest, or nil if there is none.
func ReadBackupDates(dir string) map[string]time.Time {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil
	}
	var mf struct {
		Items []BackupItem `json:"items"`
	}
	if json.Unmarshal(b, &mf) != nil {
		return nil
	}
	out := map[string]time.Time{}
	for _, it := range mf.Items {
		out[it.WorkshopID] = it.NewestFile
	}
	return out
}
