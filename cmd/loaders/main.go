// Command loaders records the SHA-256 of every file in the official RE_PPG
// and BepInEx 5 release archives (see package loaders). Archives are only
// read, never extracted to disk or run. Releases already in the index are
// skipped, so a daily run only downloads new ones.
//
//	go run ./cmd/loaders -data <dir>
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/loaders"
)

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(url string, limit int64) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "ppgmods-loaders")
	if strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d MB", url, limit>>20)
	}
	return b, nil
}

func releases(repo string) ([]release, error) {
	var all []release
	for page := 1; page <= 5; page++ {
		b, err := get(fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", repo, page), 16<<20)
		if err != nil {
			return nil, err
		}
		var rs []release
		if err := json.Unmarshal(b, &rs); err != nil {
			return nil, err
		}
		all = append(all, rs...)
		if len(rs) < 100 {
			break
		}
	}
	return all, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// index adds an asset: the file itself, and every file inside it if it is
// a zip.
func index(ix *loaders.Index, project, version, name string, b []byte) int {
	n := 1
	ix.Add(sum(b), loaders.File{Project: project, Version: version, Path: name})
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return n
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		log.Printf("  %s: not a readable zip: %v", name, err)
		return n
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > 512<<20 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, io.LimitReader(rc, 512<<20))
		rc.Close()
		if err != nil {
			continue
		}
		ix.Add(hex.EncodeToString(h.Sum(nil)), loaders.File{Project: project, Version: version, Path: f.Name})
		n++
	}
	return n
}

func main() {
	data := flag.String("data", "data", "folder holding loaders.json")
	flag.Parse()
	path := filepath.Join(*data, "loaders.json")
	ix := &loaders.Index{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, ix); err != nil {
			log.Fatalf("%s: %v", path, err)
		}
	}
	sources := []struct {
		project, repo string
		want          func(release) bool
	}{
		{"RE_PPG", "AlibardaWasTaken/RE_PPG", func(r release) bool { return !r.Draft }},
		// BepInEx 5 (RE_PPG uses 5.4.x); 6 is a different loader.
		{"BepInEx", "BepInEx/BepInEx", func(r release) bool { return !r.Draft && strings.HasPrefix(r.Tag, "v5.") }},
	}
	added := 0
	for _, src := range sources {
		rs, err := releases(src.repo)
		if err != nil {
			log.Fatalf("%s: %v", src.repo, err)
		}
		for _, r := range rs {
			name := src.project + " " + r.Tag
			if !src.want(r) || ix.HasRelease(name) {
				continue
			}
			files := 0
			ok := true
			for _, a := range r.Assets {
				b, err := get(a.URL, 300<<20)
				if err != nil {
					log.Printf("  %s %s: %v", name, a.Name, err)
					ok = false
					continue
				}
				files += index(ix, src.project, r.Tag, a.Name, b)
			}
			if ok {
				ix.Releases = append(ix.Releases, name)
			}
			log.Printf("%s: %d files from %d assets", name, files, len(r.Assets))
			added++
		}
	}
	ix.Updated = time.Now().UTC()
	os.MkdirAll(*data, 0o755)
	b, _ := json.Marshal(ix)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("%d new releases; %d releases and %d distinct files in the index", added, len(ix.Releases), len(ix.Files))
}
