package main

import (
	"testing"

	"github.com/DogeKingC/SWG/internal/workshop"
)

func form(name string) string {
	return "### Name\n\n" + name + "\n\n### Version\n\n1.0\n"
}

// The owner's "reviewed" label vouches for the version published from that
// issue. It must not land on another entry the issue's (editable) text
// names, or on a newer version published from a later issue.
func TestReviewedLabelOnlyMarksThatIssuesVersion(t *testing.T) {
	ix := &workshop.Index{Entries: []workshop.Entry{
		{Slug: "nice-mod", Name: "Nice Mod", Owner: "mallory", Issue: 1},
		{Slug: "evil-mod", Name: "Evil Mod", Owner: "mallory", Issue: 2},
	}}
	// Issue 1 was edited to name the other mod before the owner labeled it.
	markReviewed(ix, event{number: 1, author: "mallory", body: form("Evil Mod")})
	if ix.Find("evil-mod").Reviewed {
		t.Fatal("reviewing issue 1 marked the mod published from issue 2")
	}
	if !ix.Find("nice-mod").Reviewed {
		t.Fatal("reviewing issue 1 did not mark its own mod")
	}

	// A newer version of the same mod was published from issue 3.
	ix = &workshop.Index{Entries: []workshop.Entry{{Slug: "nice-mod", Name: "Nice Mod", Owner: "mallory", Issue: 3}}}
	markReviewed(ix, event{number: 1, author: "mallory", body: form("Nice Mod")})
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
