package app

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/sources"
)

// A fake Nexus Mods API, answering the way Nexus documents it.
func fakeNexus(t *testing.T) *httptest.Server {
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	w, _ := zw.Create("Nexus Test/mod.json")
	w.Write([]byte(`{"Name":"Nexus Test","Author":"Tester","ModVersion":"2.0","Scripts":["a.cs"]}`))
	w, _ = zw.Create("Nexus Test/a.cs")
	w.Write([]byte(`class A {}`))
	zw.Close()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file.zip" && r.Header.Get("apikey") != "testkey" {
			rw.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/users/validate.json":
			json.NewEncoder(rw).Encode(map[string]any{"user_id": 1, "name": "tester", "is_premium": false})
		case "/v1/games/peopleplayground/mods/77/files.json":
			json.NewEncoder(rw).Encode(map[string]any{"files": []map[string]any{
				{"file_id": 499, "name": "Old", "file_name": "old.zip", "version": "1.0", "category_name": "OLD_VERSION", "uploaded_timestamp": 1600000000},
				{"file_id": 500, "name": "Nexus Test", "file_name": "test.zip", "version": "2.0", "category_name": "MAIN", "uploaded_timestamp": 1700000000},
			}})
		case "/v1/games/peopleplayground/mods/77/files/500/download_link.json":
			if r.URL.Query().Get("key") != "abc" || r.URL.Query().Get("expires") != "123" {
				rw.WriteHeader(403) // free account without the link's key
				return
			}
			json.NewEncoder(rw).Encode([]map[string]string{{"name": "CDN", "URI": srv.URL + "/file.zip"}})
		case "/file.zip":
			rw.Write(zipped.Bytes())
		case "/v1/games/peopleplayground/mods/md5_search/" + md5Hex(zipped.Bytes()) + ".json":
			json.NewEncoder(rw).Encode([]map[string]any{{"mod": map[string]any{"mod_id": 77, "domain_name": "peopleplayground"}}})
		default:
			rw.WriteHeader(404)
		}
	}))
	t.Cleanup(sources.UseTestServer(srv.Client().Transport, srv.URL))
	t.Cleanup(srv.Close)
	return srv
}

func TestNexusLinkAndInstallFromNXM(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	fakeNexus(t)
	if _, err := LinkNexus("wrong"); err == nil {
		t.Fatal("bad key accepted")
	}
	acct, err := LinkNexus("testkey")
	if err != nil || acct.User != "tester" || acct.Premium {
		t.Fatalf("link: %+v %v", acct, err)
	}
	if st, err := os.Stat(nexusPath()); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", st.Mode(), err)
	}
	st, _ := manager.LoadState()
	m := &manager.Manager{State: st, ModsDir: t.TempDir(), ContraptionsDir: t.TempDir()}
	a := &App{Opt: DefaultOptions()}
	// A free account can't download without the link's one-time key.
	if _, err := a.fetchNexus(77, nil); err == nil {
		t.Fatal("free account downloaded without a link")
	}
	if err := a.InstallNXM(m, "nxm://peopleplayground/mods/77/files/500?key=abc&expires=123&user_id=1"); err != nil {
		t.Fatal(err)
	}
	inst := m.State.Mods["nx:77"]
	if inst == nil || inst.Version != "2.0" || inst.Mirror != "nexus:77" {
		t.Fatalf("installed: %+v", inst)
	}
	if err := UnlinkNexus(); err != nil || LoadNexus() != nil {
		t.Fatal("unlink")
	}
}

func TestParseNXM(t *testing.T) {
	ok := "nxm://peopleplayground/mods/77/files/500?key=abc-DEF_1&expires=123&user_id=1"
	if l, err := sources.ParseNXM(ok); err != nil || l.ModID != 77 || l.FileID != 500 || l.Key != "abc-DEF_1" {
		t.Fatalf("%+v %v", l, err)
	}
	for _, bad := range []string{
		"nxm://skyrimspecialedition/mods/1/files/2?key=a&expires=1",
		"nxm://peopleplayground/mods/77/files/500/../../x",
		"nxm://peopleplayground/mods/77/files/500?key=a%22%20&expires=1",
		"nxm://peopleplayground/mods/77/files/500?key=a&expires=1;calc",
		"https://peopleplayground/mods/77/files/500",
	} {
		if _, err := sources.ParseNXM(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func md5Hex(b []byte) string {
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}

// A browser download is only taken if it is the mod's file: by its name,
// and (with a linked account) by Nexus' record of its checksum.
func TestNexusBrowserDownloadIsChecked(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	srv := fakeNexus(t)
	dl := t.TempDir()
	since := time.Now()
	os.WriteFile(filepath.Join(dl, "Free Stuff-12-1-0-1700000000.zip"), []byte("other"), 0o644)
	os.WriteFile(filepath.Join(dl, "setup.zip"), []byte("other"), 0o644)
	if _, err := waitForDownload(dl, "", "-77-", since, 3*time.Second, nil); err == nil {
		t.Fatal("an unrelated archive in Downloads was taken")
	}

	if _, err := LinkNexus("testkey"); err != nil {
		t.Fatal(err)
	}
	nb := &NeedsBrowser{Mirror: "nexus:77", Match: "-77-"}
	fake := filepath.Join(dl, "Nexus Test-77-2-0-1700000000.zip")
	os.WriteFile(fake, []byte("not the real file"), 0o644)
	if err := checkNexusDownload(nb, fake); err == nil {
		t.Fatal("a file Nexus doesn't have for mod 77 was accepted")
	}
	resp, err := srv.Client().Get(srv.URL + "/file.zip")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	os.WriteFile(fake, real, 0o644)
	if err := checkNexusDownload(nb, fake); err != nil {
		t.Fatalf("the mod's real file was refused: %v", err)
	}
}

// People Playground files on Nexus only have Manual download: even with the
// nxm:// handler on, Install waits for the file in Downloads instead of a
// "Mod manager download" link that never comes.
func TestNexusInstallUsesManualDownload(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	fakeNexus(t)
	a, err := LinkNexus("testkey")
	if err != nil {
		t.Fatal(err)
	}
	a.Handler = true
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	nb := nexusBrowser(sources.NXMod{ID: 77, Name: "Nexus Test"}, "nx:77")
	if nb.NXM || nb.Match != "-77-" || !strings.Contains(nb.Reason, "Slow download") {
		t.Fatalf("Nexus install does not wait for the manual download: %+v", nb)
	}
}

// An install that opened a Nexus file's page as a mod manager download
// takes that file's nxm:// link without a confirmation; a link for another
// file, or one nobody is waiting for, is left for the person to confirm.
func TestDeliverNXM(t *testing.T) {
	link := func(mod, file int) string {
		return fmt.Sprintf("nxm://peopleplayground/mods/%d/files/%d?key=abc&expires=123&user_id=1", mod, file)
	}
	if DeliverNXM(link(77, 500)) {
		t.Fatal("a link nobody waits for was taken")
	}
	ch, done := expectNXM(77, 500)
	if DeliverNXM(link(77, 501)) {
		t.Fatal("a link for another file of the mod was taken")
	}
	if DeliverNXM("nxm://skyrim/mods/77/files/500?key=a&expires=1") {
		t.Fatal("a link for another game was taken")
	}
	if !DeliverNXM(link(77, 500)) {
		t.Fatal("the waited-for link was not taken")
	}
	if got := <-ch; got != link(77, 500) {
		t.Fatalf("got %q", got)
	}
	if DeliverNXM(link(77, 500)) {
		t.Fatal("a second copy of the link was taken")
	}
	done()
	_, done2 := expectNXM(78, 1)
	done2()
	if DeliverNXM(link(78, 1)) {
		t.Fatal("a link was taken after the install stopped waiting")
	}
}

// Install's browser download of a Nexus file, with ppgmods handling nxm://
// links: the page opens as a mod manager download (&nmm=1), and when Nexus
// hands over that file's link, the file comes through the API and installs,
// with no Downloads folder involved.
func TestNexusInstallTakesHandedOverLink(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	fakeNexus(t)
	acct, err := LinkNexus("testkey")
	if err != nil {
		t.Fatal(err)
	}
	acct.Handler = true
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	var opened string
	openPage = func(u string) error {
		opened = u
		// The person clicks Slow download; Nexus hands the link over.
		go func() {
			for i := 0; i < 100 && !DeliverNXM("nxm://peopleplayground/mods/77/files/500?key=abc&expires=123&user_id=1"); i++ {
				time.Sleep(20 * time.Millisecond)
			}
		}()
		return nil
	}
	defer func() { openPage = OpenBrowser }()
	nb := &NeedsBrowser{URL: "https://www.nexusmods.com/peopleplayground/mods/77?tab=files&file_id=500", AnyFile: true,
		Match: "-77-", Mirror: "nexus:77", Key: "nx:77", FileID: 500, Handoff: true}
	opt := DefaultOptions()
	opt.NXMHandoff, opt.Wait = true, 10*time.Second
	for _, dl := range []string{t.TempDir(), ""} { // with and without a Downloads folder
		a := &App{Opt: opt}
		a.Opt.Downloads = dl
		if dl == "" {
			t.Setenv("HOME", t.TempDir()) // no Downloads folder to find
			t.Setenv("USERPROFILE", t.TempDir())
			t.Setenv("XDG_DOWNLOAD_DIR", "")
		}
		c, err := a.viaBrowser(nb)
		if err != nil {
			t.Fatalf("downloads %q: %v", dl, err)
		}
		if !strings.Contains(opened, "nmm=1") || !strings.Contains(opened, "file_id=500") {
			t.Fatalf("opened %q, want the mod manager download page", opened)
		}
		if c == nil || c.Key != "nx:77" || c.Version != "2.0" || c.Mirror != "nexus:77" {
			t.Fatalf("candidate: %+v", c)
		}
	}
	if DeliverNXM("nxm://peopleplayground/mods/77/files/500?key=abc&expires=123&user_id=1") {
		t.Fatal("a link was taken after the install finished")
	}
}
