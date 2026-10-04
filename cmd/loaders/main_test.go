package main

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/DogeKingC/SWG/internal/loaders"
)

func TestIndexZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"BepInEx/core/BepInEx.dll": "core", "RE_PPG/RE_PPG.Compiler.exe": "compiler", "BepInEx/plugins/": ""} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	ix := &loaders.Index{}
	if n := index(ix, "RE_PPG", "v0.2.16", "RE_PPG-base.zip", buf.Bytes()); n != 3 {
		t.Fatalf("indexed %d files, want 3 (the zip and two files)", n)
	}
	f := ix.Lookup(sum([]byte("compiler")))
	if len(f) != 1 || f[0].Path != "RE_PPG/RE_PPG.Compiler.exe" || f[0].Version != "v0.2.16" {
		t.Errorf("lookup: %+v", f)
	}
	if len(ix.Lookup(sum(buf.Bytes()))) != 1 {
		t.Error("the archive itself is not indexed")
	}
}
