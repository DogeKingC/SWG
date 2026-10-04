// workshop runs the Open Workshop's GitHub Actions jobs.
//
//	workshop issue -data <dir> -out <dir>
//	    handles a submission issue event (see .github/workflows/workshop.yml):
//	    checks and publishes a submission, applies the owner's labels, or an
//	    author's /withdraw comment. Event details come from the environment;
//	    everything in them is treated as data.
//	workshop refresh -data <dir>
//	    refreshes download counts (daily)
//	workshop keygen
//	    prints a new signing key pair
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/version"
	"github.com/DogeKingC/SWG/internal/workshop"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: workshop issue|refresh|keygen")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "issue":
		err = issue(os.Args[2:])
	case "refresh":
		err = refresh(os.Args[2:])
	case "keygen":
		pub, priv, kerr := workshop.NewKey()
		if kerr != nil {
			err = kerr
			break
		}
		fmt.Println("Public key (goes in internal/workshop/key.go):", pub)
		fmt.Println("Private key (add as the repository secret WORKSHOP_SIGNING_KEY, keep nowhere else):", priv)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "workshop:", err)
		os.Exit(1)
	}
}

var client = &http.Client{Timeout: 10 * time.Minute}

// event is what the workflow passes in the environment.
type event struct {
	name, action             string
	number                   int
	author, body             string
	label, sender, repoOwner string
	commentBody, commentBy   string
}

func readEvent() event {
	n, _ := strconv.Atoi(os.Getenv("ISSUE_NUMBER"))
	return event{
		name: os.Getenv("EVENT_NAME"), action: os.Getenv("ACTION"), number: n,
		author: os.Getenv("ISSUE_AUTHOR"), body: os.Getenv("ISSUE_BODY"),
		label: os.Getenv("LABEL"), sender: os.Getenv("SENDER"), repoOwner: os.Getenv("REPO_OWNER"),
		commentBody: os.Getenv("COMMENT_BODY"), commentBy: os.Getenv("COMMENT_AUTHOR"),
	}
}

// outcome is what the workflow does on the issue afterwards.
type outcome struct {
	Reply  string   `json:"reply"`
	Labels []string `json:"labels,omitempty"`
	Remove []string `json:"remove,omitempty"`
	Close  bool     `json:"close,omitempty"`
}

func loadIndex(data string) (*workshop.Index, error) {
	ix := &workshop.Index{}
	if b, err := os.ReadFile(filepath.Join(data, "index.json")); err == nil {
		if err := json.Unmarshal(b, ix); err != nil {
			return nil, fmt.Errorf("index.json: %v", err)
		}
	}
	return ix, nil
}

func saveIndex(data string, ix *workshop.Index) error {
	ix.Generated = time.Now().UTC()
	ix.Sort()
	b := ix.Marshal()
	if err := os.WriteFile(filepath.Join(data, "index.json"), b, 0o644); err != nil {
		return err
	}
	if key := os.Getenv("WORKSHOP_SIGNING_KEY"); key != "" {
		sig, err := workshop.Sign(b, key)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(data, "index.sig"), []byte(sig+"\n"), 0o644)
	}
	os.Remove(filepath.Join(data, "index.sig"))
	return nil
}

func issue(args []string) error {
	fs := flag.NewFlagSet("issue", flag.ExitOnError)
	data := fs.String("data", "data", "checkout of the workshop-data branch")
	out := fs.String("out", "out", "new files to upload")
	fs.Parse(args)
	os.MkdirAll(*data, 0o755)
	os.MkdirAll(*out, 0o755)
	ev := readEvent()
	ix, err := loadIndex(*data)
	if err != nil {
		return err
	}
	var o *outcome
	byOwner := ev.sender != "" && strings.EqualFold(ev.sender, ev.repoOwner)
	switch {
	case ev.name == "issue_comment":
		o = comment(ix, ev)
	case ev.action == "labeled" && byOwner && ev.label == "reviewed":
		o = markReviewed(ix, ev)
	case ev.action == "labeled" && byOwner && ev.label == "withdrawn":
		o = withdraw(ix, ev, "withdrawn by the Open Workshop's maintainers")
	case ev.action == "labeled" && byOwner && ev.label == "approved":
		o = submit(ix, ev, *out, true)
	case ev.action == "opened" || ev.action == "edited":
		o = submit(ix, ev, *out, false)
	}
	if o == nil {
		return nil // nothing to do
	}
	if err := saveIndex(*data, ix); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(o, "", "  ")
	return os.WriteFile("outcome.json", b, 0o644) // read by the workflow
}

// entryForIssue is the entry published from this issue or, for an author
// acting on an older issue of their mod, their entry the issue names. The
// issue text is the author's to edit, so a name in it never reaches an
// entry someone else owns.
func entryForIssue(ix *workshop.Index, ev event) *workshop.Entry {
	var named *workshop.Entry
	if s, err := workshop.SubmissionFromIssue(ev.body, ev.author); err == nil {
		named = ix.Find(workshop.Slugify(s.Name))
	}
	if named != nil && named.Issue == ev.number {
		return named
	}
	for i := range ix.Entries {
		if ix.Entries[i].Issue == ev.number {
			return &ix.Entries[i]
		}
	}
	if named != nil && strings.EqualFold(named.Owner, ev.author) {
		return named
	}
	return nil
}

// Submission rules. The Open Workshop has no reviewers, so it is strict by
// default: anything not plainly safe waits for the owner's "approved" label.
const (
	minAccountAge = 30 * 24 * time.Hour
	maxPerDay     = 3
)

func submit(ix *workshop.Index, ev event, out string, approved bool) *outcome {
	var r bytes.Buffer
	reject := func(problems []string) *outcome {
		fmt.Fprintf(&r, "**Not published.** Please fix this and edit the issue (it's checked again automatically):\n\n")
		for _, p := range problems {
			fmt.Fprintf(&r, "- %s\n", p)
		}
		return &outcome{Reply: r.String(), Labels: []string{"needs-changes"}, Remove: []string{"published", "needs-approval"}}
	}
	s, err := workshop.SubmissionFromIssue(ev.body, ev.author)
	if err != nil {
		return reject([]string{err.Error()})
	}
	slug := workshop.Slugify(s.Name)
	if !workshop.ValidSlug(slug) {
		return reject([]string{"Name: use at least a few letters or digits"})
	}
	old := ix.Find(slug)
	if old != nil && !strings.EqualFold(old.Owner, ev.author) {
		return reject([]string{fmt.Sprintf("Name: %q is already published by @%s. Only they can update it; choose another name if this is a different mod.", s.Name, old.Owner)})
	}
	if old != nil && !old.Withdrawn && old.Issue != ev.number && version.Compare(s.Version, old.Version) <= 0 {
		return reject([]string{fmt.Sprintf("Version: %s is already published; an update needs a higher version", old.Version)})
	}
	var holds []string
	if !approved {
		if age, err := accountAge(ev.author); err != nil {
			holds = append(holds, "Account: couldn't check the account's age ("+err.Error()+"), so the owner approves it")
		} else if age < minAccountAge {
			holds = append(holds, fmt.Sprintf("Account: GitHub accounts younger than %d days need the owner's approval", int(minAccountAge.Hours()/24)))
		}
		n := 0
		for _, e := range ix.Entries {
			if strings.EqualFold(e.Owner, ev.author) && time.Since(e.Published) < 24*time.Hour && e.Issue != ev.number {
				n++
			}
		}
		if n >= maxPerDay {
			return reject([]string{fmt.Sprintf("Too many: at most %d publications per day per account; try again tomorrow", maxPerDay)})
		}
	}
	work, _ := os.MkdirTemp("", "ws-")
	defer os.RemoveAll(work)
	res := workshop.Check(client, s, work)
	if len(res.Problems) > 0 {
		o := reject(res.Problems)
		addFindings(o, res)
		return o
	}
	holds = append(holds, res.Holds...)
	if old != nil && old.Withdrawn {
		holds = append(holds, "Withdrawn: this mod was taken down ("+old.WithdrawnReason+"), so publishing it again needs the owner's approval")
	}
	if old != nil && !old.Withdrawn {
		// An update must not start doing new kinds of things unnoticed.
		had := map[string]bool{}
		for _, rule := range old.Rules {
			had[rule] = true
		}
		for _, rule := range res.Rules {
			if !had[rule] {
				holds = append(holds, "Update: the new version does something the previous one didn't (scanner rule "+rule+")")
			}
		}
	}
	if len(holds) > 0 && !approved {
		fmt.Fprintf(&r, "**Waiting for approval.** The file passed the hard checks, but the Open Workshop only publishes this with the owner's approval:\n\n")
		for _, h := range holds {
			fmt.Fprintf(&r, "- %s\n", h)
		}
		fmt.Fprintf(&r, "\nIf something here is a mistake, edit the issue to fix it; otherwise the owner will look at it.\n")
		o := &outcome{Reply: r.String(), Labels: []string{"needs-approval"}, Remove: []string{"published", "needs-changes"}}
		addFindings(o, res)
		return o
	}
	if !approved {
		s.WorkshopID = "" // a Workshop ID claim is held above; never kept unapproved
	}
	if old != nil && old.SHA256 == res.SHA256 {
		old.Name, old.Author, old.Description, old.Tags, old.Image, old.License = s.Name, s.Author, s.Description, s.Tags, s.Image, s.License
		old.Version, old.Issue, old.WorkshopID = s.Version, ev.number, s.WorkshopID
		old.Approved = old.Approved || approved && len(holds) > 0
		old.Withdrawn, old.WithdrawnReason = false, ""
		return &outcome{Reply: "Details updated (same file).", Labels: []string{"published"}, Remove: []string{"needs-changes", "needs-approval"}, Close: true}
	}
	name := workshop.AssetName(slug, s.Version, res.SHA256, res.Ext)
	if err := copyFile(res.Path, filepath.Join(out, name)); err != nil {
		return reject([]string{"internal error: " + err.Error()})
	}
	now := time.Now().UTC()
	e := workshop.Entry{Slug: slug, Name: s.Name, Author: s.Author, Kind: s.Kind, Version: s.Version, Description: s.Description,
		Tags: s.Tags, Image: s.Image, WorkshopID: s.WorkshopID, License: s.License, File: workshop.AssetURL(name),
		SHA256: res.SHA256, Size: res.Size, ScanMax: res.ScanMax, Findings: res.Findings, Rules: res.Rules, Published: now, First: now,
		Maintainers: []string{ev.author}, Owner: ev.author, Issue: ev.number, Approved: approved && len(holds) > 0}
	if old != nil {
		e.First, e.Downloads = old.First, old.Downloads
		*old = e
	} else {
		ix.Entries = append(ix.Entries, e)
	}
	fmt.Fprintf(&r, "**Published** %s %s by %s (`%s`)", s.Name, s.Version, s.Author, slug)
	if e.Approved {
		fmt.Fprintf(&r, " with the owner's approval")
	}
	fmt.Fprintf(&r, ". It's in PPG Mod Manager now, marked *checked automatically*; the app waits 48 hours before installing new uploads that nobody reviewed.\n\n")
	for _, w := range res.Warnings {
		fmt.Fprintf(&r, "- ⚠ %s\n", w)
	}
	fmt.Fprintf(&r, "\nTo update it, open a new submission with the same name and a higher version. To take it down, comment `/withdraw <reason>` here.\n")
	return &outcome{Reply: r.String(), Labels: []string{"published"}, Remove: []string{"needs-changes", "needs-approval"}, Close: true}
}

func addFindings(o *outcome, res *workshop.Result) {
	if len(res.Findings) == 0 {
		return
	}
	o.Reply += "\n<details><summary>Scanner findings</summary>\n\n"
	for _, f := range res.Findings {
		o.Reply += "- `" + strings.ReplaceAll(f, "`", "'") + "`\n"
	}
	o.Reply += "\n</details>\n"
}

// accountAge asks GitHub how old an account is.
func accountAge(login string) (time.Duration, error) {
	req, _ := http.NewRequest("GET", "https://api.github.com/users/"+url.PathEscape(login), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var u struct {
		Created time.Time `json:"created_at"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&u) != nil || u.Created.IsZero() {
		return 0, fmt.Errorf("GitHub user %s: HTTP %d", login, resp.StatusCode)
	}
	return time.Since(u.Created), nil
}

// markReviewed vouches for the version published from this issue, and only
// that one: a newer version (published from another issue) skips the
// install cooldown only once it is reviewed itself.
func markReviewed(ix *workshop.Index, ev event) *outcome {
	e := entryForIssue(ix, ev)
	if e == nil || e.Issue != ev.number {
		return &outcome{Reply: "Nothing published from this issue to mark as reviewed (a newer version, if any, is reviewed on its own issue)."}
	}
	e.Reviewed = true
	return &outcome{Reply: fmt.Sprintf("%s %s (file SHA-256 `%s`) is now marked **reviewed**.", e.Name, e.Version, e.SHA256)}
}

func withdraw(ix *workshop.Index, ev event, reason string) *outcome {
	e := entryForIssue(ix, ev)
	if e == nil {
		return &outcome{Reply: "Nothing published from this issue to withdraw."}
	}
	e.Withdrawn, e.WithdrawnReason = true, reason
	return &outcome{Reply: fmt.Sprintf("%s is **withdrawn**: %s. Everyone who has it is warned.", e.Name, reason), Labels: []string{"withdrawn"}}
}

// comment handles "/withdraw <reason>" from the entry's owner (or the
// repository owner).
func comment(ix *workshop.Index, ev event) *outcome {
	line := strings.TrimSpace(strings.SplitN(ev.commentBody, "\n", 2)[0])
	if !strings.HasPrefix(line, "/withdraw") {
		return nil
	}
	e := entryForIssue(ix, ev)
	if e == nil {
		return &outcome{Reply: "Nothing published from this issue to withdraw."}
	}
	if !strings.EqualFold(ev.commentBy, e.Owner) && !strings.EqualFold(ev.commentBy, ev.repoOwner) {
		return &outcome{Reply: fmt.Sprintf("Only @%s can withdraw %s.", e.Owner, e.Name)}
	}
	reason := strings.TrimSpace(strings.TrimPrefix(line, "/withdraw"))
	if reason == "" {
		reason = "withdrawn by its author"
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return withdraw(ix, ev, reason)
}

func refresh(args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	data := fs.String("data", "data", "checkout of the workshop-data branch")
	fs.Parse(args)
	ix, err := loadIndex(*data)
	if err != nil {
		return err
	}
	downloadCounts(ix)
	rescan(ix)
	return saveIndex(*data, ix)
}

// rescan checks every published file again with today's scanner: a file
// that is flagged now (and wasn't approved) is withdrawn.
func rescan(ix *workshop.Index) {
	for i := range ix.Entries {
		e := &ix.Entries[i]
		if e.Withdrawn {
			continue
		}
		work, _ := os.MkdirTemp("", "ws-rescan-")
		sub := &workshop.Submission{Name: e.Name, Author: e.Author, Kind: e.Kind, Version: e.Version, Download: e.File,
			SHA256: e.SHA256, Maintainers: []string{e.Owner}}
		res := workshop.Check(client, sub, work)
		os.RemoveAll(work)
		var why []string
		for _, p := range res.Problems {
			if strings.HasPrefix(p, "does what the worm did") || strings.HasPrefix(p, "sha256") {
				why = append(why, p)
			}
		}
		if !e.Approved {
			for _, h := range res.Holds {
				if strings.HasPrefix(h, "Safety scan") || strings.HasPrefix(h, "File type") {
					why = append(why, h)
				}
			}
		}
		if len(why) > 0 {
			e.Withdrawn, e.WithdrawnReason = true, "flagged by an updated safety check: "+why[0]
			fmt.Printf("withdrew %s: %s\n", e.Slug, why[0])
		}
	}
}

// downloadCounts adds up each entry's release-asset downloads.
func downloadCounts(ix *workshop.Index) {
	tok := os.Getenv("GITHUB_TOKEN")
	if tok == "" {
		return
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+workshop.Repo+"/releases/tags/"+workshop.FilesTag, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var rel struct {
		Assets []struct {
			Name      string `json:"name"`
			Downloads int    `json:"download_count"`
		} `json:"assets"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&rel) != nil {
		return
	}
	for i := range ix.Entries {
		n := 0
		for _, a := range rel.Assets {
			if strings.HasPrefix(a.Name, ix.Entries[i].Slug+"-") {
				n += a.Downloads
			}
		}
		ix.Entries[i].Downloads = n
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}
