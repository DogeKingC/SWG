package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DogeKingC/SWG/internal/workshop"
)

const (
	nice = "1111111111111111111111111111111111111111111111111111111111111111"
	evil = "2222222222222222222222222222222222222222222222222222222222222222"
)

func form(name string) string {
	return "### Name\n\n" + name + "\n\n### Version\n\n1.0\n"
}

// The owner's "reviewed" label vouches for the version published from that
// issue. It must not land on another entry the issue's (editable) text
// names, or on a newer version published from a later issue.
func TestReviewedLabelOnlyMarksThatIssuesVersion(t *testing.T) {
	ix := &workshop.Index{Entries: []workshop.Entry{
		{Slug: "nice-mod", Name: "Nice Mod", Owner: "mallory", Issue: 1, SHA256: nice},
		{Slug: "evil-mod", Name: "Evil Mod", Owner: "mallory", Issue: 2, SHA256: evil},
	}}
	// Issue 1 was edited to name the other mod before the owner labeled it.
	markReviewed(ix, event{number: 1, author: "mallory", body: form("Evil Mod")}, evil[:12])
	if ix.Find("evil-mod").Reviewed {
		t.Fatal("reviewing issue 1 marked the mod published from issue 2")
	}
	if ix.Find("nice-mod").Reviewed {
		t.Fatal("a review naming another file marked issue 1's mod")
	}
	markReviewed(ix, event{number: 1, author: "mallory", body: form("Evil Mod")}, nice[:12])
	if !ix.Find("nice-mod").Reviewed {
		t.Fatal("reviewing issue 1 did not mark its own mod")
	}

	// A newer version of the same mod was published from issue 3.
	ix = &workshop.Index{Entries: []workshop.Entry{{Slug: "nice-mod", Name: "Nice Mod", Owner: "mallory", Issue: 3, SHA256: evil}}}
	markReviewed(ix, event{number: 1, author: "mallory", body: form("Nice Mod")}, evil[:12])
	if ix.Find("nice-mod").Reviewed {
		t.Fatal("reviewing the old issue 1 vouched for the version from issue 3")
	}
}

// Labels and /withdraw on an issue never reach someone else's mod.
func TestWithdrawNeverReachesAnotherOwnersMod(t *testing.T) {
	ix := &workshop.Index{Entries: []workshop.Entry{{Slug: "victim-mod", Name: "Victim Mod", Owner: "alice", Issue: 7}}}
	withdraw(ix, event{number: 9, author: "mallory", body: form("Victim Mod")}, "x")
	if ix.Find("victim-mod").Withdrawn {
		t.Fatal("a label on mallory's issue withdrew alice's mod")
	}
	// The author withdrawing from an older issue of their own mod still works.
	withdraw(ix, event{number: 2, author: "alice", body: form("Victim Mod")}, "x")
	if !ix.Find("victim-mod").Withdrawn {
		t.Fatal("the owner could not withdraw their mod from an older issue")
	}
}

// modZip is a minimal mod archive whose script says what.
func modZip(t *testing.T, what string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("M/mod.json")
	w.Write([]byte(`{"Name":"Held Mod","Author":"mallory","Scripts":["s.cs"]}`))
	w, _ = zw.Create("M/s.cs")
	w.Write([]byte("class S { /* " + what + " */ }"))
	zw.Close()
	return buf.Bytes()
}

// The owner approves a file, not an issue: if the author swaps the file
// after the owner looked, the approval does not publish the new one.
func TestApproveIsBoundToTheCheckedFile(t *testing.T) {
	serve := modZip(t, "checked")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(serve)
	}))
	defer srv.Close()
	old := client
	client = srv.Client()
	defer func() { client = old }()

	checked := sha256.Sum256(serve)
	approve := hex.EncodeToString(checked[:])[:12]
	body := "### Name\n\nHeld Mod\n\n### Author\n\nmallory\n\n### Type\n\nMod\n\n### Version\n\n1.0\n\n### File\n\n" + srv.URL + "/m.zip\n"
	ev := event{name: "issue_comment", number: 4, author: "mallory", body: body, repoOwner: "owner"}

	// Not the owner: ignored.
	ix := &workshop.Index{}
	ev.commentBy, ev.commentBody = "mallory", "/approve "+approve
	if o := comment(ix, ev, t.TempDir()); o != nil || len(ix.Entries) != 0 {
		t.Fatalf("approval by the author was accepted: %+v", o)
	}

	// The author swaps the file after the owner checked it.
	serve = modZip(t, "swapped")
	ev.commentBy = "owner"
	out := t.TempDir()
	o := comment(ix, ev, out)
	if o == nil || !strings.Contains(o.Reply, "Not approved") || len(ix.Entries) != 0 {
		t.Fatalf("approval published a file the owner never saw: %+v %+v", o, ix.Entries)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatal("swapped file was staged for upload")
	}

	// The file the owner checked: published.
	serve = modZip(t, "checked")
	if o := comment(ix, ev, out); len(ix.Entries) != 1 || !strings.HasPrefix(ix.Entries[0].SHA256, approve) {
		t.Fatalf("approval of the checked file did not publish it: %+v", o)
	}
}

// The owner's emergency switch: while paused nothing is published or
// approved, withdrawing still works, and resume lifts it.
func TestPauseAndResume(t *testing.T) {
	data := t.TempDir()
	ix := &workshop.Index{Entries: []workshop.Entry{{Slug: "a-mod", Name: "A Mod", Owner: "alice", Issue: 1, Published: time.Now().Add(-time.Hour)}}}
	if err := saveIndex(data, ix); err != nil {
		t.Fatal(err)
	}
	if err := pause([]string{"-data", data}, true); err == nil {
		t.Fatal("pause without a reason was accepted")
	}
	if err := pause([]string{"-data", data, "-reason", "worm in the wild", "-since", "48h"}, true); err != nil {
		t.Fatal(err)
	}
	ix, _ = loadIndex(data)
	if !ix.Paused || ix.SuspectSince == nil || ix.Blocked(&ix.Entries[0]) == "" {
		t.Fatalf("not paused, or the recent version is not held back: %+v", ix)
	}

	// Submissions and approvals are refused while paused.
	out := t.TempDir()
	for _, ev := range []event{
		{name: "issues", action: "opened", number: 5, author: "bob", body: form("B Mod")},
		{name: "issue_comment", number: 5, author: "bob", body: form("B Mod"), commentBy: "owner", repoOwner: "owner", commentBody: "/approve 0123456789ab"},
	} {
		o := handle(ix, ev, out)
		if o == nil || !strings.Contains(o.Reply, "paused") {
			t.Errorf("%s/%s not refused while paused: %+v", ev.name, ev.action, o)
		}
	}
	if len(ix.Entries) != 1 {
		t.Fatal("something was published while paused")
	}
	// Withdrawing still works.
	handle(ix, event{name: "issue_comment", number: 1, author: "alice", body: form("A Mod"), commentBy: "alice", commentBody: "/withdraw bad"}, out)
	if !ix.Entries[0].Withdrawn {
		t.Error("withdrawing did not work while paused")
	}

	if err := pause([]string{"-data", data}, false); err != nil {
		t.Fatal(err)
	}
	ix, _ = loadIndex(data)
	if ix.Paused || ix.SuspectSince != nil {
		t.Fatal("resume did not lift the pause")
	}
	if _, err := parseSince("2999-01-01", time.Now()); err == nil {
		t.Error("a future -since was accepted")
	}
}

// pause-all / resume-all set and clear the app-wide pause in blocklist.json
// and leave the rest of the file as it was.
func TestLockdown(t *testing.T) {
	orig, err := os.ReadFile("../../blocklist/blocklist.json")
	if err != nil {
		t.Fatal(err)
	}
	f := t.TempDir() + "/blocklist.json"
	os.WriteFile(f, orig, 0o644)
	if err := lockdown([]string{"-file", f}, true); err == nil {
		t.Fatal("pause-all without a reason was accepted")
	}
	if err := lockdown([]string{"-file", f, "-reason", "worm <spreading> & more"}, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	var bl blocklistFile
	if err := json.Unmarshal(b, &bl); err != nil {
		t.Fatal(err)
	}
	if len(bl.Pause) != 1 || bl.Pause[0].Sources[0] != "*" || bl.Pause[0].Reason != "worm <spreading> & more" || len(bl.Entries) == 0 {
		t.Fatalf("pause-all wrote %+v", bl.Pause)
	}
	if !strings.Contains(string(b), "worm <spreading> & more") {
		t.Error("reason was HTML-escaped")
	}
	if err := lockdown([]string{"-file", f}, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); !bytes.Equal(b, orig) {
		t.Error("resume-all did not restore the file exactly")
	}
}
