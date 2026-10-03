//go:build ignore

// gen downloads the official NuGet packages of libraries that People
// Playground mods bundle and writes the SHA-256 of every DLL in them to
// knownlibs.json. Run from the repository root:
//
//	go run ./internal/scan/knownlibs/gen.go
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
)

// package id -> lowest version to include
var packages = []struct{ id, min string }{
	{"Mono.Cecil", "0.9.6"},
	{"Lib.Harmony", "2.0.0"},
	{"Lib.Harmony.Thin", "2.0.0"},
	{"Newtonsoft.Json", "9.0.1"},
}

type lib struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	File    string `json:"file"`
}

func main() {
	out := map[string]lib{}
	for _, p := range packages {
		id := strings.ToLower(p.id)
		var idx struct{ Versions []string }
		if err := getJSON("https://api.nuget.org/v3-flatcontainer/"+id+"/index.json", &idx); err != nil {
			fail(err)
		}
		for _, v := range idx.Versions {
			if strings.Contains(v, "-") || cmp(v, p.min) < 0 {
				continue // prereleases and old versions
			}
			b, err := get(fmt.Sprintf("https://api.nuget.org/v3-flatcontainer/%s/%s/%s.%s.nupkg", id, v, id, v))
			if err != nil {
				fail(err)
			}
			zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
			if err != nil {
				fail(err)
			}
			n := 0
			for _, f := range zr.File {
				if !strings.HasPrefix(f.Name, "lib/") || !strings.EqualFold(path.Ext(f.Name), ".dll") {
					continue
				}
				rc, err := f.Open()
				if err != nil {
					fail(err)
				}
				h := sha256.New()
				io.Copy(h, rc)
				rc.Close()
				out[hex.EncodeToString(h.Sum(nil))] = lib{p.id, v, f.Name}
				n++
			}
			fmt.Fprintf(os.Stderr, "%s %s: %d dlls\n", p.id, v, n)
		}
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range keys {
		v, _ := json.Marshal(out[k])
		sep := ","
		if i == len(keys)-1 {
			sep = ""
		}
		fmt.Fprintf(&buf, "  %q: %s%s\n", k, v, sep)
	}
	buf.WriteString("}\n")
	if err := os.WriteFile("internal/scan/knownlibs/knownlibs.json", buf.Bytes(), 0o644); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "%d known DLLs\n", len(keys))
}

func cmp(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func get(u string) ([]byte, error) {
	r, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", u, r.Status)
	}
	return io.ReadAll(r.Body)
}

func getJSON(u string, v any) error {
	b, err := get(u)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
