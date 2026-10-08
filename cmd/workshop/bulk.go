package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Trlydev/SWG/internal/manager"
	"github.com/Trlydev/SWG/internal/workshop"
)

// bulk publishes the items of a bulk.json (see workshop.PackBulk) whose
// files are assets of an upload release. Run by the owner from the
// workflow. Every item gets the same checks as a submission; only items
// that pass them all are published (the owner's run approves the Workshop
// ID claim, nothing else). The rest are listed for a look by hand.
func bulk(args []string) error {
	fs := flag.NewFlagSet("bulk", flag.ExitOnError)
	data := fs.String("data", "data", "checkout of the workshop-data branch")
	out := fs.String("out", "out", "new files to upload")
	manifest := fs.String("manifest", "bulk.json", "the bulk.json of the upload")
	base := fs.String("base", "", "download URL of the upload release's assets (ending in /)")
	owner := fs.String("owner", "", "the repository owner, who publishes them")
	max := fs.Int("max", 150, "publish at most this many in one run (run again for the rest)")
	fs.Parse(args)
	if !strings.HasPrefix(*base, "https://") || !strings.HasSuffix(*base, "/") || *owner == "" {
		return fmt.Errorf("bulk needs -base https://.../ and -owner")
	}
	b, err := os.ReadFile(*manifest)
	if err != nil {
		return err
	}
	var m workshop.BulkManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("%s: %v", *manifest, err)
	}
	ix, err := loadIndex(*data)
	if err != nil {
		return err
	}
	if ix.Paused {
		return fmt.Errorf("the Open Workshop is paused (%s); resume it first", ix.PausedReason)
	}
	os.MkdirAll(*out, 0o755)
	held, err := loadHeld(*data)
	if err != nil {
		return err
	}
	r := bulkPublish(ix, m.Items, *base, *owner, *out, *max, held)
	fmt.Print(r.summary())
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		os.WriteFile(p, []byte(r.summary()), 0o644)
	}
	if err := saveHeld(*data, held); err != nil {
		return err
	}
	return saveIndex(*data, ix)
}

type bulkResult struct {
	published []string
	present   int
	skipped   []string // "id name: why"
	held      int      // of skipped: waiting in the review queue
	left      int
}

func (r *bulkResult) summary() string {
	var s strings.Builder
	fmt.Fprintf(&s, "## Open Workshop bulk upload\n\nPublished %d, already there %d, skipped %d", len(r.published), r.present, len(r.skipped))
	if r.left > 0 {
		fmt.Fprintf(&s, ", %d left for the next run (run bulk again with the same release)", r.left)
	}
	s.WriteString(".\n")
	if r.held > 0 {
		fmt.Fprintf(&s, "\n%d of the skipped items only need your decision: run the workflow with action **review** to see them with their findings, then **approve-held** with the Workshop IDs to publish.\n", r.held)
	}
	if len(r.skipped) > 0 {
		s.WriteString("\n### Skipped (look at these by hand)\n\n")
		for _, x := range r.skipped {
			s.WriteString("- " + x + "\n")
		}
	}
	if len(r.published) > 0 {
		s.WriteString("\n### Published\n\n")
		for _, x := range r.published {
			s.WriteString("- " + x + "\n")
		}
	}
	return s.String()
}

// bulkSubmission is the submission a bulk item is checked as.
func bulkSubmission(it workshop.BulkItem, base, owner string) *workshop.Submission {
	desc := it.Description
	if desc != "" {
		desc += "\n\n"
	}
	desc += fmt.Sprintf("Archived copy of Steam Workshop item %s by %s, from before the worm, published by the Open Workshop's owner. Authors can ask for it to be taken down.", it.WorkshopID, it.Author)
	return &workshop.Submission{Name: it.Name, Author: it.Author, Kind: it.Kind, Version: it.Version, Description: clip(desc, 4000),
		Tags: []string{"archive"}, Download: base + it.File, SHA256: it.SHA256, WorkshopID: it.WorkshopID, Maintainers: []string{owner}}
}

// publishChecked copies a checked file to out and adds its entry.
func publishChecked(ix *workshop.Index, s *workshop.Submission, res *workshop.Result, slug, owner, out string) error {
	name := workshop.AssetName(slug, s.Version, res.SHA256, res.Ext)
	if err := copyFile(res.Path, filepath.Join(out, name)); err != nil {
		return err
	}
	now := time.Now().UTC()
	ix.Entries = append(ix.Entries, workshop.Entry{Slug: slug, Name: s.Name, Author: s.Author, Kind: s.Kind, Version: s.Version,
		Description: s.Description, Tags: s.Tags, WorkshopID: s.WorkshopID, File: workshop.AssetURL(name), SHA256: res.SHA256,
		Size: res.Size, ScanMax: res.ScanMax, Findings: res.Findings, Rules: res.Rules, Published: now, First: now,
		Maintainers: []string{owner}, Owner: owner, Approved: true})
	return nil
}

// approvable reports whether a hold is one the owner's bulk run accepts
// by itself: the Workshop ID claim, and the author taken from the item's
// own mod.json (or unknown).
func approvable(h string) bool {
	return strings.HasPrefix(h, "Steam Workshop ID:") || strings.HasPrefix(h, "Author:")
}

func bulkPublish(ix *workshop.Index, items []workshop.BulkItem, base, owner, out string, max int, held *heldList) *bulkResult {
	r := &bulkResult{}
	have := map[string]bool{}
	for _, e := range ix.Entries {
		if e.WorkshopID != "" {
			have[e.WorkshopID] = true
		}
	}
	skip := func(it workshop.BulkItem, why string) {
		r.skipped = append(r.skipped, fmt.Sprintf("%s %s: %s", it.WorkshopID, it.Name, why))
	}
	done := 0
	for i, it := range items {
		if have[it.WorkshopID] {
			r.present++
			continue
		}
		if done >= max {
			r.left = len(items) - i
			break
		}
		// The worm inserted itself into Workshop items from this date on.
		if !it.Newest.IsZero() && !it.Newest.Before(manager.WormCutoff) {
			skip(it, "has files dated on or after the worm cutoff ("+manager.WormCutoff.Format("2006-01-02")+")")
			continue
		}
		slug := workshop.BulkSlug(it)
		if !workshop.ValidSlug(slug) || ix.Find(slug) != nil {
			skip(it, "no free entry name ("+slug+")")
			continue
		}
		s := bulkSubmission(it, base, owner)
		work, _ := os.MkdirTemp("", "ws-bulk-")
		res := workshop.Check(client, s, work)
		var holds []string
		for _, h := range res.Holds {
			if !approvable(h) {
				holds = append(holds, h)
			}
		}
		if len(res.Problems) > 0 {
			os.RemoveAll(work)
			skip(it, strings.Join(append(res.Problems, holds...), "; "))
			continue
		}
		if len(holds) > 0 {
			// Only the owner's decision is missing: into the review queue.
			os.RemoveAll(work)
			if held != nil {
				held.put(HeldItem{BulkItem: it, Base: base, Reasons: holds, Findings: res.Findings, HeldAt: time.Now().UTC()})
				r.held++
			}
			skip(it, "held for review: "+strings.Join(holds, "; "))
			continue
		}
		err := publishChecked(ix, s, res, slug, owner, out)
		os.RemoveAll(work)
		if err != nil {
			skip(it, "internal error: "+err.Error())
			continue
		}
		if held != nil {
			held.drop(it.WorkshopID)
		}
		have[it.WorkshopID] = true
		r.published = append(r.published, fmt.Sprintf("%s %s (`%s`)", it.WorkshopID, it.Name, slug))
		done++
	}
	return r
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
