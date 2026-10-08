package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/workshop"
)

// The review queue: bulk items whose only obstacle is a decision for the
// owner (scanner findings to judge, an unusual file type) are kept in
// held.json on the workshop-data branch. "review" lists them with their
// findings; "approve-held" publishes the ones the owner names, after
// downloading and checking each again. Items with hard problems (what the
// worm did, files from after the cutoff, a broken mod.json) never get here.

// HeldItem is a bulk item waiting for the owner's decision.
type HeldItem struct {
	workshop.BulkItem
	Base     string    `json:"base"` // download URL of its upload release's assets
	Reasons  []string  `json:"reasons"`
	Findings []string  `json:"findings,omitempty"`
	HeldAt   time.Time `json:"held_at"`
}

type heldList struct {
	Items []HeldItem `json:"items"`
}

func loadHeld(data string) (*heldList, error) {
	h := &heldList{}
	b, err := os.ReadFile(filepath.Join(data, "held.json"))
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, h); err != nil {
		return nil, fmt.Errorf("held.json: %v", err)
	}
	return h, nil
}

func saveHeld(data string, h *heldList) error {
	p := filepath.Join(data, "held.json")
	if len(h.Items) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	sort.Slice(h.Items, func(i, j int) bool { return h.Items[i].WorkshopID < h.Items[j].WorkshopID })
	b, _ := json.MarshalIndent(h, "", "  ")
	return os.WriteFile(p, b, 0o644)
}

func (h *heldList) put(it HeldItem) {
	h.drop(it.WorkshopID)
	h.Items = append(h.Items, it)
}

func (h *heldList) drop(id string) {
	out := h.Items[:0]
	for _, it := range h.Items {
		if it.WorkshopID != id {
			out = append(out, it)
		}
	}
	h.Items = out
}

func (h *heldList) find(id string) *HeldItem {
	for i := range h.Items {
		if h.Items[i].WorkshopID == id {
			return &h.Items[i]
		}
	}
	return nil
}

// review writes the queue, with findings, to the run summary.
func review(args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	data := fs.String("data", "data", "checkout of the workshop-data branch")
	fs.Parse(args)
	h, err := loadHeld(*data)
	if err != nil {
		return err
	}
	text := reviewSummary(h)
	fmt.Print(text)
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		f.WriteString(text)
	}
	return nil
}

func reviewSummary(h *heldList) string {
	var s strings.Builder
	s.WriteString("## Open Workshop review queue\n\n")
	if len(h.Items) == 0 {
		s.WriteString("No bulk items are waiting for a decision.\n")
		return s.String()
	}
	fmt.Fprintf(&s, "%d bulk item(s) wait for your decision. Read the findings; to publish some, run the workflow with action **approve-held** and their Workshop IDs (separated by spaces or commas). Anything you don't approve stays unpublished.\n\n", len(h.Items))
	for _, it := range h.Items {
		fmt.Fprintf(&s, "### %s · %s (%s by %s)\n\n", it.WorkshopID, it.Name, it.Kind, it.Author)
		for _, r := range it.Reasons {
			s.WriteString("- " + r + "\n")
		}
		if len(it.Findings) > 0 {
			s.WriteString("\n<details><summary>All scanner findings</summary>\n\n```\n")
			for _, f := range it.Findings {
				s.WriteString(strings.ReplaceAll(f, "```", "'''") + "\n")
			}
			s.WriteString("```\n</details>\n")
		}
		s.WriteString("\n")
	}
	return s.String()
}

var reWorkshopIDs = regexp.MustCompile(`^\d{6,12}$`)

// approveHeld publishes the held items the owner names: each is downloaded
// and checked again, must still be the same file, and must have no hard
// problem; its holds are what the owner approves.
func approveHeld(args []string) error {
	fs := flag.NewFlagSet("approve-held", flag.ExitOnError)
	data := fs.String("data", "data", "checkout of the workshop-data branch")
	out := fs.String("out", "out", "new files to upload")
	owner := fs.String("owner", "", "the repository owner, who publishes them")
	ids := fs.String("ids", "", "Workshop IDs to publish, separated by spaces or commas")
	fs.Parse(args)
	if *owner == "" {
		return fmt.Errorf("approve-held needs -owner")
	}
	var want []string
	for _, id := range strings.FieldsFunc(*ids, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if !reWorkshopIDs.MatchString(id) {
			return fmt.Errorf("%q is not a Workshop ID", id)
		}
		want = append(want, id)
	}
	if len(want) == 0 {
		return fmt.Errorf("name the Workshop IDs to publish")
	}
	ix, err := loadIndex(*data)
	if err != nil {
		return err
	}
	if ix.Paused {
		return fmt.Errorf("the Open Workshop is paused (%s); resume it first", ix.PausedReason)
	}
	h, err := loadHeld(*data)
	if err != nil {
		return err
	}
	os.MkdirAll(*out, 0o755)
	var report strings.Builder
	report.WriteString("## Open Workshop: approved held items\n\n")
	for _, id := range want {
		line := publishHeld(ix, h, id, *owner, *out)
		report.WriteString("- " + line + "\n")
	}
	fmt.Print(report.String())
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		os.WriteFile(p, []byte(report.String()), 0o644)
	}
	if err := saveHeld(*data, h); err != nil {
		return err
	}
	return saveIndex(*data, ix)
}

// publishHeld publishes one held item and says what happened.
func publishHeld(ix *workshop.Index, h *heldList, id, owner, out string) string {
	it := h.find(id)
	if it == nil {
		return id + ": not in the review queue"
	}
	for _, e := range ix.Entries {
		if e.WorkshopID == id {
			h.drop(id)
			return fmt.Sprintf("%s %s: already published (`%s`)", id, it.Name, e.Slug)
		}
	}
	if !it.Newest.IsZero() && !it.Newest.Before(manager.WormCutoff) {
		return fmt.Sprintf("%s %s: NOT published: has files dated on or after the worm cutoff", id, it.Name)
	}
	slug := workshop.BulkSlug(it.BulkItem)
	if !workshop.ValidSlug(slug) || ix.Find(slug) != nil {
		return fmt.Sprintf("%s %s: no free entry name (%s)", id, it.Name, slug)
	}
	s := bulkSubmission(it.BulkItem, it.Base, owner)
	work, _ := os.MkdirTemp("", "ws-held-")
	defer os.RemoveAll(work)
	res := workshop.Check(client, s, work)
	if len(res.Problems) > 0 {
		// A different file, or one that now does what the worm did: never.
		return fmt.Sprintf("%s %s: NOT published: %s", id, it.Name, strings.Join(res.Problems, "; "))
	}
	if err := publishChecked(ix, s, res, slug, owner, out); err != nil {
		return fmt.Sprintf("%s %s: internal error: %v", id, it.Name, err)
	}
	h.drop(id)
	return fmt.Sprintf("%s %s: published as `%s` (approved by you; its findings are shown to everyone who installs it)", id, it.Name, slug)
}
