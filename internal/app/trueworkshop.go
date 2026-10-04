package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DogeKingC/SWG/internal/manager"
	"github.com/DogeKingC/SWG/internal/sources"
)

// fetchTW downloads a True Workshop mod and checks it against the SHA-256
// the site publishes.
func (a *App) fetchTW(id int, prev *manager.Installed) (*manager.Candidate, error) {
	it, err := sources.TWGet(id)
	if err != nil {
		return nil, err
	}
	if it.Type != "mod" && it.Type != "contraption" {
		return nil, &manager.Rejection{Reasons: []string{fmt.Sprintf("%q is a %s, which ppgmods does not install", it.Title, it.Type)}}
	}
	if it.Scan != "" && it.Scan != "clean" {
		return nil, &manager.Rejection{Reasons: []string{fmt.Sprintf("True Workshop's own scanner status for this item is %q, not clean", it.Scan)}}
	}
	if a.Opt.UpdateOnly && prev != nil && strings.EqualFold(prev.ArchiveSHA, it.SHA256) {
		return nil, nil
	}
	dir, err := CacheDir(fmt.Sprintf("tw:%d", id))
	if err != nil {
		return nil, err
	}
	name := filepath.Base(it.FileURL)
	if !strings.Contains(name, ".") {
		name += ".zip"
	}
	path := filepath.Join(dir, strings.ToLower(it.SHA256[:min(12, len(it.SHA256))])+"-"+strings.ReplaceAll(name, " ", "_"))
	sha, _ := manager.FileSHA(path)
	if !strings.EqualFold(sha, it.SHA256) {
		trust := "reviewed by the site's maintainers"
		if !it.Reviewed() {
			trust = "NOT reviewed yet, only passed the site's automated scanner"
		}
		a.logf("tw:%d %s by %s - downloading %s (%s; %s)", id, it.Title, it.Author, name, HumanSize(it.Size), trust)
		if _, sha, err = sources.Download(it.DownloadURL(), path, 1<<30); err != nil {
			return nil, err
		}
		if !strings.EqualFold(sha, it.SHA256) {
			os.Remove(path)
			return nil, &manager.Rejection{Reasons: []string{fmt.Sprintf("checksum mismatch: True Workshop says %s, got %s", it.SHA256, sha)}}
		}
		go sources.TWTrackDownload(id)
	}
	return &manager.Candidate{
		Key: fmt.Sprintf("tw:%d", id), Name: it.Title, Source: it.Page(), Path: path,
		Version: FmtTime(it.CreatedTime()), Revision: it.CreatedTime(), ArchiveSHA: sha, Reviewed: it.Reviewed(),
	}, nil
}

// TWResult describes a True Workshop item for search results.
func TWResult(it sources.TWItem) SearchResult {
	return SearchResult{
		Ref: "tw:" + strconv.Itoa(it.ID), Source: "True Workshop", Name: it.Title, Author: it.Author,
		Category: strings.Join(it.Tags, ", "), Date: FmtTime(it.CreatedTime()), Size: HumanSize(it.Size),
		URL: it.Page(), Image: it.Thumb(), Reviewed: it.Reviewed(), Downloads: it.Downloads, Kind: it.Type,
	}
}
