package app

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/sources"
	"github.com/Trlydev/SWG/internal/workshop"
)

// rewrite sends every request to the test server, whatever its host.
type rewrite struct {
	host string
	rt   http.RoundTripper
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	u.Scheme, u.Host = "https", r.host
	req2 := req.Clone(req.Context())
	req2.URL, req2.Host = &u, r.host
	return r.rt.RoundTrip(req2)
}

type fakeGitHub struct {
	mu    sync.Mutex
	files map[string][]byte // path -> body
}

func modZip(ver string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("OW Test/mod.json")
	w.Write([]byte(`{"Name":"OW Test","Author":"Me","ModVersion":"` + ver + `"}`))
	w, _ = zw.Create("OW Test/a.cs")
	w.Write([]byte(`class A {}`))
	zw.Close()
	return b.Bytes()
}

func (f *fakeGitHub) publish(t *testing.T, priv string, entries ...workshop.Entry) {
	ix := &workshop.Index{Generated: time.Now(), Entries: entries}
	b := ix.Marshal()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files["/"+workshop.Repo+"/"+strings.Repeat("a", 40)+"/index.json"] = b
	if priv != "" {
		sig, err := workshop.Sign(b, priv)
		if err != nil {
			t.Fatal(err)
		}
		f.files["/"+workshop.Repo+"/"+strings.Repeat("a", 40)+"/index.sig"] = []byte(sig)
	}
	workshop.ForgetIndex()
}

func entryFor(slug, ver string, body []byte) workshop.Entry {
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	name := workshop.AssetName(slug, ver, h, ".zip")
	return workshop.Entry{Slug: slug, Name: "OW Test", Author: "Me", Kind: "mod", Version: ver, File: workshop.AssetURL(name),
		SHA256: h, Size: int64(len(body)), ScanMax: "none", Published: time.Now().Add(-72 * time.Hour), Maintainers: []string{"me"}, Owner: "me"}
}

func TestOpenWorkshopInstallUpdateWithdraw(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	f := &fakeGitHub{files: map[string][]byte{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+workshop.Repo+"/commits/workshop-data" {
			w.Write([]byte(strings.Repeat("a", 40)))
			return
		}
		f.mu.Lock()
		b, ok := f.files[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	rt := rewrite{host, srv.Client().Transport}
	defer workshop.SetIndexForTest(nil)()
	defer workshop.SetTransport(rt)()
	defer sources.UseTestServer(rt, srv.URL)()
	pub, priv, _ := workshop.NewKey()
	old := workshop.PublicKey
	workshop.PublicKey = pub
	defer func() { workshop.PublicKey = old }()

	v1 := modZip("1.0")
	e1 := entryFor("ow-test", "1.0", v1)
	u, _ := url.Parse(e1.File)
	f.files[u.Path] = v1
	f.publish(t, priv, e1)

	st, _ := manager.LoadState()
	m := &manager.Manager{State: st, ModsDir: t.TempDir(), ContraptionsDir: t.TempDir()}
	a := &App{Opt: DefaultOptions()}
	if err := a.Install(m, "ow:ow-test"); err != nil {
		t.Fatal(err)
	}
	if in := m.State.Mods["ow:ow-test"]; in == nil || in.Version != "1.0" || in.Mirror != "openworkshop:ow-test" {
		t.Fatalf("installed: %+v", in)
	}

	// A bad signature is refused.
	f.publish(t, "", e1)
	f.mu.Lock()
	f.files["/"+workshop.Repo+"/"+strings.Repeat("a", 40)+"/index.sig"] = []byte("AAAA")
	f.mu.Unlock()
	if _, err := workshop.FetchIndex(); err == nil {
		t.Fatal("index with a bad signature accepted")
	}

	// An update the owner reviewed applies right away, even just published.
	v2 := modZip("1.1")
	e2 := entryFor("ow-test", "1.1", v2)
	e2.Reviewed, e2.Published = true, time.Now()
	u2, _ := url.Parse(e2.File)
	f.files[u2.Path] = v2
	f.publish(t, priv, e2)
	b := a.with(func(o *Options) { o.Yes = true })
	if s := b.Update(m); s.OK != 1 || m.State.Mods["ow:ow-test"].Version != "1.1" {
		t.Fatalf("update: %+v %+v", s, m.State.Mods["ow:ow-test"])
	}

	// A file that isn't the reviewed one is refused.
	e3 := entryFor("ow-test2", "1.0", v1)
	e3.Slug = "ow-test2"
	u3, _ := url.Parse(e3.File)
	f.files[u3.Path] = modZip("9.9") // different bytes
	f.publish(t, priv, e2, e3)
	if err := a.Install(m, "ow:ow-test2"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered file: %v", err)
	}

	// Withdrawn: can't be installed any more.
	e2.Withdrawn, e2.WithdrawnReason = true, "found a problem"
	f.publish(t, priv, e2)
	if _, err := a.fetchOW("ow-test", nil); err == nil || !strings.Contains(err.Error(), "withdrawn") {
		t.Fatalf("withdrawn: %v", err)
	}
}

// Someone who installed the owner's archived copy of a Workshop item is
// moved to the author's own release once the author publishes it, and
// stays there (no flipping back to the archive copy); a mirror lookup by
// Workshop ID offers the author's release first.
func TestArchiveCopyUpdatesToAuthorRelease(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	f := &fakeGitHub{files: map[string][]byte{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+workshop.Repo+"/commits/workshop-data" {
			w.Write([]byte(strings.Repeat("a", 40)))
			return
		}
		f.mu.Lock()
		b, ok := f.files[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	rt := rewrite{host, srv.Client().Transport}
	defer workshop.SetIndexForTest(nil)()
	defer workshop.SetTransport(rt)()
	defer sources.UseTestServer(rt, srv.URL)()
	pub, priv, _ := workshop.NewKey()
	old := workshop.PublicKey
	workshop.PublicKey = pub
	defer func() { workshop.PublicKey = old }()

	arch := modZip("1.0")
	ea := entryFor("ow-test-2222222", "1.0", arch)
	ea.WorkshopID, ea.Tags = "2222222", []string{"archive"}
	ua, _ := url.Parse(ea.File)
	f.files[ua.Path] = arch
	f.publish(t, priv, ea)

	st, _ := manager.LoadState()
	m := &manager.Manager{State: st, ModsDir: t.TempDir(), ContraptionsDir: t.TempDir()}
	a := &App{Opt: DefaultOptions()}
	if err := a.Install(m, "ow:ow-test-2222222"); err != nil {
		t.Fatal(err)
	}

	// An author's release older than the archived copy is no update.
	older := modZip("0.9")
	eold := entryFor("ow-test-by-me", "0.9", older)
	eold.WorkshopID = "2222222"
	uold, _ := url.Parse(eold.File)
	f.files[uold.Path] = older
	f.publish(t, priv, ea, eold)
	if s := a.with(func(o *Options) { o.Yes = true }).Update(m); s.OK != 0 || m.State.Mods["ow:ow-test-2222222"].Version != "1.0" {
		t.Fatalf("downgraded to the author's older release: %+v", s)
	}

	own := modZip("1.0.1")
	eo := entryFor("ow-test-by-me", "1.0.1", own)
	eo.WorkshopID = "2222222"
	uo, _ := url.Parse(eo.File)
	f.files[uo.Path] = own
	f.publish(t, priv, ea, eo)
	if e := owForWorkshop("2222222"); e == nil || e.Slug != "ow-test-by-me" {
		t.Fatalf("mirror lookup offers %+v, want the author's release", e)
	}
	b := a.with(func(o *Options) { o.Yes = true })
	if s := b.Update(m); s.OK != 1 {
		t.Fatalf("update: %+v", s)
	}
	in := m.State.Mods["ow:ow-test-2222222"]
	if in == nil || in.Version != "1.0.1" || in.Mirror != "openworkshop:ow-test-by-me" {
		t.Fatalf("after update: %+v", in)
	}
	if s := b.Update(m); s.OK != 0 || s.Current != 1 {
		t.Fatalf("second update changed it again: %+v", s)
	}
	if m.State.Find("ow:ow-test-by-me") != in {
		t.Error("the author's entry doesn't find the installed copy")
	}
	// Repairing it fetches the author's release, not the archived copy.
	os.RemoveAll(filepath.Join(m.ModsDir, in.Folders[0]))
	if s := b.Repair(m, []string{"ow:ow-test-2222222"}); s.OK != 1 || m.State.Mods["ow:ow-test-2222222"].Version != "1.0.1" {
		t.Fatalf("repair: %+v %+v", s, m.State.Mods["ow:ow-test-2222222"])
	}
}

// The author's own release wins over archived copies, then the version.
func TestBestForWorkshop(t *testing.T) {
	ix := &workshop.Index{Entries: []workshop.Entry{
		{Slug: "arch-new", WorkshopID: "5", Version: "9.0", Tags: []string{"archive"}},
		{Slug: "own-old", WorkshopID: "5", Version: "1.0"},
		{Slug: "own-new", WorkshopID: "5", Version: "1.2"},
		{Slug: "other", WorkshopID: "6", Version: "3.0"},
	}}
	if e := bestForWorkshop(ix, "5"); e == nil || e.Slug != "own-new" {
		t.Fatalf("got %+v", e)
	}
	if e := bestForWorkshop(ix, ""); e != nil {
		t.Fatalf("matched an empty Workshop ID: %+v", e)
	}
}

// Check for updates counts every installed item: checked, or why not.
// A Workshop copy with no newer source, an item found on the PC, a pinned
// one and one turned off are "not checked", not silently left out.
func TestUpdateCountsEverything(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	defer workshop.SetIndexForTest(&workshop.Index{})()
	defer sources.SetS01ForTest([]sources.S01Mod{})()
	st, _ := manager.LoadState()
	for _, inst := range []*manager.Installed{
		{Key: "sky:1111111", Name: "Workshop copy"},
		{Key: "sky:2222222", Name: "Workshop contraption", Kind: manager.KindContraption},
		{Key: "local:abcdef123456", Name: "Found here"},
		{Key: "gb:5", Name: "Pinned", Pinned: true},
		{Key: "sky:3333333", Name: "Off", Off: true},
	} {
		st.Mods[inst.Key] = inst
	}
	m := &manager.Manager{State: st, ModsDir: t.TempDir(), ContraptionsDir: t.TempDir()}
	var log []string
	a := &App{Opt: DefaultOptions(), Logf: func(f string, v ...any) { log = append(log, fmt.Sprintf(f, v...)) }}
	s := a.Update(m)
	if total := s.Current + s.OK + s.Refused + s.Failed + s.Manual + s.NotChecked; total != 5 || s.NotChecked != 5 {
		t.Fatalf("summary %+v covers %d of 5 items", s, total)
	}
	joined := strings.Join(log, "\n")
	for _, want := range []string{"2 Steam Workshop copies", "1 found on this PC", "1 pinned", "1 turned off"} {
		if !strings.Contains(joined, "not checked: "+want) {
			t.Errorf("log lacks %q:\n%s", want, joined)
		}
	}
}

// A 01 STUDIO mod installed from 01 STUDIO updates when the site's file
// changed; one installed from a mirror only for a higher version.
func TestS01Update(t *testing.T) {
	t.Setenv("PPGMODS_HOME", t.TempDir())
	const ws = "3704779343"
	body := modZip("1.1")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/download/"+ws+".zip" {
			w.Write(body)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	rt := rewrite{strings.TrimPrefix(srv.URL, "https://"), srv.Client().Transport}
	defer sources.UseTestServer(rt, srv.URL)()
	mod := sources.S01Mod{Slug: "pennywise-mod", Title: "OW Test", Category: "Free", Steam: 3704779343}
	defer sources.SetS01ForTest([]sources.S01Mod{mod})()
	mirrorMemo[ws] = mirrorMemoEntry{at: time.Now(), list: []Mirror{s01Mirror(mod)}}
	defer delete(mirrorMemo, ws)
	st, _ := manager.LoadState()
	m := &manager.Manager{State: st, ModsDir: t.TempDir()}
	a := &App{Opt: DefaultOptions()}

	from01 := &manager.Installed{Key: "sky:" + ws, Name: "OW Test", Version: "1.1", Mirror: "01studio:pennywise-mod", ArchiveSHA: "old"}
	c, mine, err := a.s01Update(m, from01)
	if !mine || err != nil || c == nil || c.Key != from01.Key {
		t.Fatalf("changed file on 01 STUDIO: %v %v %v", c, mine, err)
	}
	fromMirror := &manager.Installed{Key: "sky:" + ws, Name: "OW Test", Version: "1.1", Mirror: "skymods:1", ArchiveSHA: "other"}
	if c, mine, err := a.s01Update(m, fromMirror); !mine || err != nil || c != nil {
		t.Fatalf("same version from a mirror counted as an update: %v %v", c, err)
	}
	fromMirror.Version = "1.0"
	if c, _, err := a.s01Update(m, fromMirror); err != nil || c == nil {
		t.Fatalf("higher version on 01 STUDIO missed: %v", err)
	}
	other := &manager.Installed{Key: "sky:999999", Name: "Not theirs"}
	if _, mine, _ := a.s01Update(m, other); mine {
		t.Fatal("someone else's mod treated as 01 STUDIO's")
	}
}
