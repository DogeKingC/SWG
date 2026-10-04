// workshop runs the Open Workshop's CI jobs.
//
//	workshop check -head <git ref> -base <git ref> -author <github login>
//	    checks the submissions a pull request changes (run from a checkout
//	    of the base branch: the pull request's files are read with git, its
//	    code is never run)
//	workshop publish -data <dir> -out <dir> -commit <sha>
//	    after a merge: re-checks changed submissions, puts new files in -out
//	    (uploaded as release assets by the workflow) and writes index.json
//	    (and index.sig with WORKSHOP_SIGNING_KEY) in -data
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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/workshop"
)

const subDir = "workshop/submissions"

var reSubPath = regexp.MustCompile(`^workshop/submissions/([a-z0-9][a-z0-9-]{1,62}[a-z0-9])/submission\.json$`)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: workshop check|publish|keygen")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = check(os.Args[2:])
	case "publish":
		err = publish(os.Args[2:])
	case "keygen":
		pub, priv, kerr := workshop.NewKey()
		if kerr != nil {
			err = kerr
			break
		}
		fmt.Println("Public key (put in internal/workshop/key.go):", pub)
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

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, ee.Stderr)
		}
		return "", err
	}
	return string(out), nil
}

// showSubmission reads a submission at a git ref (nil if it doesn't exist).
func showSubmission(ref, path string) (*workshop.Submission, error) {
	out, err := exec.Command("git", "show", ref+":"+path).Output()
	if err != nil {
		return nil, nil // not there
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	var s workshop.Submission
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return &s, nil
}

// summary collects the report (also written to the GitHub job summary).
type summary struct{ bytes.Buffer }

func (s *summary) flush() {
	os.Stdout.Write(s.Bytes())
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.Write(s.Bytes())
			f.Close()
		}
	}
}

func check(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	head := fs.String("head", "", "git ref of the pull request")
	base := fs.String("base", "", "git ref of the base branch")
	author := fs.String("author", "", "GitHub login of the pull request's author")
	fs.Parse(args)
	if *head == "" || *base == "" || *author == "" {
		return fmt.Errorf("check needs -head, -base and -author")
	}
	mb, err := git("merge-base", *base, *head)
	if err != nil {
		return err
	}
	mb = strings.TrimSpace(mb)
	changed, err := git("diff", "--name-only", "-z", mb, *head)
	if err != nil {
		return err
	}
	var s summary
	defer s.flush()
	fmt.Fprintf(&s, "## Open Workshop check\n\n")
	failed := false
	var paths []string
	for _, p := range strings.Split(changed, "\x00") {
		if p == "" {
			continue
		}
		if !reSubPath.MatchString(p) {
			fmt.Fprintf(&s, "- ✕ `%s`: a submission may only change `workshop/submissions/<name>/submission.json`\n", p)
			failed = true
			continue
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 && !failed {
		fmt.Fprintf(&s, "No submissions changed.\n")
		return nil
	}
	for _, p := range paths {
		slug := reSubPath.FindStringSubmatch(p)[1]
		fmt.Fprintf(&s, "\n### `%s`\n\n", slug)
		sub, err := showSubmission(*head, p)
		if err != nil {
			fmt.Fprintf(&s, "- ✕ %v\n", err)
			failed = true
			continue
		}
		old, _ := showSubmission(*base, p)
		if sub == nil {
			fmt.Fprintf(&s, "- Removes the entry. Set `\"withdrawn\": true` with a reason instead, so people who have it are warned.\n")
			if old != nil && !old.IsMaintainer(*author) {
				fmt.Fprintf(&s, "- ✕ only %s may change this entry\n", strings.Join(old.Maintainers, ", "))
				failed = true
			}
			continue
		}
		problems := workshop.UpdateProblems(old, sub, *author)
		work, _ := os.MkdirTemp("", "ws-check-")
		res := workshop.Check(client, sub, work)
		os.RemoveAll(work)
		problems = append(problems, res.Problems...)
		problems = approvedCritical(problems, sub.SHA256)
		fmt.Fprintf(&s, "%s by %s, %s %s", sub.Name, sub.Author, sub.Kind, sub.Version)
		if res.SHA256 != "" {
			fmt.Fprintf(&s, " · %d KB · sha256 `%s…`", res.Size/1024, res.SHA256[:12])
			if res.ScanMax != "" {
				fmt.Fprintf(&s, " · scan: **%s**", res.ScanMax)
			}
		}
		fmt.Fprintln(&s)
		fmt.Fprintln(&s)
		for _, p := range problems {
			fmt.Fprintf(&s, "- ✕ %s\n", p)
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(&s, "- ⚠ %s\n", w)
		}
		if len(res.Findings) > 0 {
			fmt.Fprintf(&s, "\n<details><summary>Scanner findings (%d)</summary>\n\n", len(res.Findings))
			for _, f := range res.Findings {
				fmt.Fprintf(&s, "- `%s`\n", strings.ReplaceAll(f, "`", "'"))
			}
			fmt.Fprintf(&s, "\n</details>\n")
		}
		if len(problems) > 0 {
			failed = true
		} else {
			fmt.Fprintf(&s, "- ✓ passes the automatic checks; a maintainer reviews it before merging\n")
		}
	}
	if failed {
		return fmt.Errorf("the submission has problems (see the summary)")
	}
	return nil
}

// approvedCritical lets a CRITICAL finding through when the Workshop's
// maintainers approved that exact file in workshop/approved-critical.txt
// (one SHA-256 per line, with a comment saying why). Worm findings never.
func approvedCritical(problems []string, sha string) []string {
	b, err := os.ReadFile("workshop/approved-critical.txt")
	if err != nil || sha == "" {
		return problems
	}
	ok := false
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == sha {
			ok = true
		}
	}
	if !ok {
		return problems
	}
	var out []string
	for _, p := range problems {
		if !strings.HasPrefix(p, "CRITICAL finding") {
			out = append(out, p)
		}
	}
	return out
}

func publish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	data := fs.String("data", "workshop-data", "checkout of the data branch")
	out := fs.String("out", "workshop-out", "where to put new files for upload")
	commit := fs.String("commit", "", "the commit being published")
	fs.Parse(args)
	os.MkdirAll(*out, 0o755)
	os.MkdirAll(*data, 0o755)

	ix := &workshop.Index{}
	if b, err := os.ReadFile(filepath.Join(*data, "index.json")); err == nil {
		if err := json.Unmarshal(b, ix); err != nil {
			return fmt.Errorf("index.json: %v", err)
		}
	}
	ents, err := os.ReadDir(subDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	now := time.Now().UTC()
	seen := map[string]bool{}
	var log summary
	defer log.flush()
	fmt.Fprintf(&log, "## Open Workshop publish\n\n")
	for _, e := range ents {
		slug := e.Name()
		if !e.IsDir() || !workshop.ValidSlug(slug) {
			continue
		}
		sub, err := workshop.LoadSubmission(filepath.Join(subDir, slug, "submission.json"))
		if err != nil {
			fmt.Fprintf(&log, "- `%s`: skipped: %v\n", slug, err)
			continue
		}
		seen[slug] = true
		old := ix.Find(slug)
		if sub.Withdrawn {
			if old != nil {
				old.Withdrawn, old.WithdrawnReason = true, sub.WithdrawnReason
				fmt.Fprintf(&log, "- `%s`: withdrawn (%s)\n", slug, sub.WithdrawnReason)
			}
			continue
		}
		meta := func(en *workshop.Entry) {
			en.Name, en.Author, en.Kind, en.Description, en.Tags = sub.Name, sub.Author, sub.Kind, sub.Description, sub.Tags
			en.Image, en.WorkshopID, en.License, en.Maintainers = sub.Image, sub.WorkshopID, sub.License, sub.Maintainers
			en.Withdrawn, en.WithdrawnReason = false, ""
		}
		if old != nil && old.SHA256 == sub.SHA256 {
			meta(old) // same file: details only
			continue
		}
		work, _ := os.MkdirTemp("", "ws-pub-")
		res := workshop.Check(client, sub, work)
		problems := approvedCritical(res.Problems, sub.SHA256)
		if len(problems) > 0 {
			os.RemoveAll(work)
			fmt.Fprintf(&log, "- `%s` %s: NOT published: %s\n", slug, sub.Version, strings.Join(problems, "; "))
			continue
		}
		name := workshop.AssetName(slug, sub.Version, res.SHA256, res.Ext)
		if err := copyFile(res.Path, filepath.Join(*out, name)); err != nil {
			os.RemoveAll(work)
			return err
		}
		os.RemoveAll(work)
		en := workshop.Entry{Slug: slug, Version: sub.Version, File: workshop.AssetURL(name), SHA256: res.SHA256, Size: res.Size,
			ScanMax: res.ScanMax, Findings: res.Findings, Published: now, First: now, Commit: *commit}
		if old != nil {
			en.First, en.Downloads = old.First, old.Downloads
		}
		meta(&en)
		if old != nil {
			*old = en
		} else {
			ix.Entries = append(ix.Entries, en)
		}
		fmt.Fprintf(&log, "- `%s` %s published (%s)\n", slug, sub.Version, name)
	}
	// Entries whose submission was deleted are withdrawn, not dropped:
	// people who installed them should hear about it.
	for i := range ix.Entries {
		if en := &ix.Entries[i]; !seen[en.Slug] && !en.Withdrawn {
			en.Withdrawn, en.WithdrawnReason = true, "removed from the Open Workshop"
		}
	}
	downloadCounts(ix)
	ix.Generated = now
	ix.Sort()
	b := ix.Marshal()
	if err := os.WriteFile(filepath.Join(*data, "index.json"), b, 0o644); err != nil {
		return err
	}
	if key := os.Getenv("WORKSHOP_SIGNING_KEY"); key != "" {
		sig, err := workshop.Sign(b, key)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*data, "index.sig"), []byte(sig+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(&log, "\nIndex signed.\n")
	} else {
		os.Remove(filepath.Join(*data, "index.sig"))
		fmt.Fprintf(&log, "\nIndex not signed (no WORKSHOP_SIGNING_KEY secret).\n")
	}
	fmt.Fprintf(&log, "\n%d entries.\n", len(ix.Entries))
	return nil
}

// downloadCounts adds up each entry's release-asset downloads (all
// versions), when a token is available.
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
	count := map[string]int{}
	for _, a := range rel.Assets {
		for i := range ix.Entries {
			if strings.HasPrefix(a.Name, ix.Entries[i].Slug+"-") {
				count[ix.Entries[i].Slug] += a.Downloads
			}
		}
	}
	slugs := make([]string, 0, len(count))
	for s := range count {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, s := range slugs {
		if en := ix.Find(s); en != nil {
			en.Downloads = count[s]
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
