// Package workshop is the Open Workshop: a mod repository that works like
// Steam's Workshop without its weak spot, code pushed to every subscriber
// unreviewed.
//
// Authors submit a mod by adding workshop/submissions/<slug>/submission.json
// in a pull request. A check job downloads the file it names, verifies its
// SHA-256 and scans it with the main branch's scanner. A maintainer reviews
// and merges. A publish job then stores the file as a permanent,
// content-addressed release asset and adds it to index.json (signed when a
// signing key is configured), which ppgmods installs from.
package workshop

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/DogeKingC/SWG/internal/archive"
	"github.com/DogeKingC/SWG/internal/scan"
	"github.com/DogeKingC/SWG/internal/version"
)

const (
	// Repo hosts the submissions, the files (release FilesTag) and the index
	// (branch DataBranch).
	Repo       = "DogeKingC/SWG"
	FilesTag   = "workshop-files"
	DataBranch = "workshop-data"
	// MaxSize is the largest archive accepted.
	MaxSize = 50 << 20
)

// Submission is workshop/submissions/<slug>/submission.json.
type Submission struct {
	Name        string   `json:"name"`
	Author      string   `json:"author"`
	Kind        string   `json:"kind"` // "mod" or "contraption"
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Download    string   `json:"download"` // https link to the .zip/.rar/.7z
	SHA256      string   `json:"sha256"`
	Image       string   `json:"image,omitempty"`       // https link to a preview image
	WorkshopID  string   `json:"workshop_id,omitempty"` // the deleted Steam Workshop item this replaces
	License     string   `json:"license,omitempty"`
	// Maintainers are the GitHub accounts allowed to change this entry.
	Maintainers []string `json:"maintainers"`
	// Withdrawn takes the mod off the Workshop and warns everyone who has it.
	Withdrawn       bool   `json:"withdrawn,omitempty"`
	WithdrawnReason string `json:"withdrawn_reason,omitempty"`
}

var (
	reSlug    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)
	reSHA     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reGHUser  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	reWS      = regexp.MustCompile(`^\d{6,12}$`)
	reTag     = regexp.MustCompile(`^[a-z0-9][a-z0-9 -]{0,23}$`)
	reVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_ -]{0,31}$`)
)

// ValidSlug reports whether s can name a submission.
func ValidSlug(s string) bool { return reSlug.MatchString(s) }

// Validate checks a submission's fields (not its file).
func (s *Submission) Validate() []string {
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }
	if n := strings.TrimSpace(s.Name); n == "" || len(n) > 80 {
		bad("name: required, at most 80 characters")
	}
	if a := strings.TrimSpace(s.Author); a == "" || len(a) > 60 {
		bad("author: required, at most 60 characters")
	}
	if s.Kind != "mod" && s.Kind != "contraption" {
		bad(`kind: "mod" or "contraption"`)
	}
	if !reVersion.MatchString(s.Version) {
		bad("version: required, like 1.2 or 2.0.1")
	}
	if len(s.Description) > 4000 {
		bad("description: at most 4000 characters")
	}
	if len(s.Tags) > 8 {
		bad("tags: at most 8")
	}
	for _, t := range s.Tags {
		if !reTag.MatchString(t) {
			bad("tag %q: lowercase letters, digits, spaces and dashes", t)
		}
	}
	if !s.Withdrawn {
		if u, err := url.Parse(s.Download); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			bad("download: an https link to the archive")
		}
		if s.SHA256 != "" && !reSHA.MatchString(s.SHA256) {
			bad("sha256: the file's SHA-256, 64 lowercase hex digits (or leave it out)")
		}
	}
	if s.Image != "" {
		if u, err := url.Parse(s.Image); err != nil || u.Scheme != "https" {
			bad("image: an https link")
		}
	}
	if s.WorkshopID != "" && !reWS.MatchString(s.WorkshopID) {
		bad("workshop_id: the Steam Workshop item's number")
	}
	if len(s.Maintainers) == 0 || len(s.Maintainers) > 5 {
		bad("maintainers: 1 to 5 GitHub usernames")
	}
	for _, m := range s.Maintainers {
		if !reGHUser.MatchString(m) {
			bad("maintainer %q: not a GitHub username", m)
		}
	}
	if s.Withdrawn && strings.TrimSpace(s.WithdrawnReason) == "" {
		bad("withdrawn_reason: say why")
	}
	return p
}

// IsMaintainer reports whether a GitHub user may change the entry.
func (s *Submission) IsMaintainer(user string) bool {
	for _, m := range s.Maintainers {
		if strings.EqualFold(m, user) {
			return true
		}
	}
	return false
}

// LoadSubmission reads a submission file.
func LoadSubmission(path string) (*Submission, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var s Submission
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return &s, nil
}

// Result is what checking a submission's file found.
type Result struct {
	Problems []string `json:"problems,omitempty"` // never published
	Holds    []string `json:"holds,omitempty"`    // published only with the owner's approval
	Warnings []string `json:"warnings,omitempty"` // shown, not blocking
	Rules    []string `json:"rules,omitempty"`    // scanner rules found (MEDIUM and up)
	SHA256   string   `json:"sha256"`
	Size     int64    `json:"size"`
	Ext      string   `json:"ext"`
	Kind     string   `json:"kind"`
	ScanMax  string   `json:"scan_max"`
	Findings []string `json:"findings,omitempty"`
	Path     string   `json:"-"` // the downloaded file
}

// wormRules can't be published at all (see manager.wormRules).
var wormRules = map[string]bool{"steam-ugc": true, "steam-friends": true, "steam-auth": true, "self-replication": true,
	"game-path-tamper": true, "mass-delete": true, "encoded-code": true, "symlink": true,
	"deserialization": true, "json-gadget": true, "embedded-executable": true, "disables-protection": true, "worm-dll": true}

// Fetch downloads a submission's file into dir and checks its SHA-256.
func Fetch(client *http.Client, s *Submission, dir string) (string, string, int64, error) {
	resp, err := client.Get(s.Download)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", 0, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		return "", "", 0, errors.New("download: the link gives a web page, not the file (use a direct link)")
	}
	p := filepath.Join(dir, "file")
	f, err := os.Create(p)
	if err != nil {
		return "", "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, MaxSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", "", 0, err
	}
	if n > MaxSize {
		return "", "", 0, fmt.Errorf("the file is larger than %d MB", MaxSize>>20)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if s.SHA256 != "" && sum != s.SHA256 {
		return p, sum, n, fmt.Errorf("sha256 mismatch: the submission says %s, the file is %s", s.SHA256, sum)
	}
	return p, sum, n, nil
}

// Check downloads and inspects a submission's file: checksum, archive
// safety, mod.json or .jaap, names, and the scanner.
func Check(client *http.Client, s *Submission, work string) *Result {
	r := &Result{}
	r.Problems = append(r.Problems, s.Validate()...)
	if s.Withdrawn || len(r.Problems) > 0 {
		return r
	}
	path, sum, size, err := Fetch(client, s, work)
	r.SHA256, r.Size, r.Path = sum, size, path
	if err != nil {
		r.Problems = append(r.Problems, err.Error())
		return r
	}
	dir := filepath.Join(work, "x")
	os.MkdirAll(dir, 0o755)
	if err := archive.Extract(path, dir); err != nil {
		r.Problems = append(r.Problems, "archive: "+err.Error())
		return r
	}
	r.Ext = archiveExt(path)
	scan.PruneBuildOutput(dir)
	var modJSONs, jaaps []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			switch {
			case strings.EqualFold(d.Name(), "mod.json"):
				modJSONs = append(modJSONs, p)
			case strings.EqualFold(filepath.Ext(p), ".jaap"):
				jaaps = append(jaaps, p)
			}
		}
		return nil
	})
	switch {
	case len(modJSONs) > 0:
		r.Kind = "mod"
	case len(jaaps) > 0:
		r.Kind = "contraption"
	default:
		r.Problems = append(r.Problems, "the archive has neither a mod.json (mod) nor a .jaap file (contraption)")
	}
	if r.Kind != "" && r.Kind != s.Kind {
		r.Problems = append(r.Problems, fmt.Sprintf("kind: the file is a %s, the submission says %s", r.Kind, s.Kind))
	}
	for _, p := range modJSONs {
		var mj struct {
			Name, Author, ModVersion string
			CreatorUGCIdentity       json.RawMessage
		}
		b, _ := os.ReadFile(p)
		if json.Unmarshal([]byte(strings.TrimPrefix(string(b), "\ufeff")), &mj) != nil {
			r.Problems = append(r.Problems, "mod.json is not valid JSON")
			continue
		}
		if len(modJSONs) == 1 {
			if !strings.EqualFold(strings.TrimSpace(mj.Author), strings.TrimSpace(s.Author)) {
				r.Holds = append(r.Holds, fmt.Sprintf("Author: the mod's mod.json says %q, the form says %q (only the author, or someone with their permission, may publish it)", mj.Author, s.Author))
			}
			if mj.ModVersion != "" && version.Compare(mj.ModVersion, s.Version) != 0 {
				r.Warnings = append(r.Warnings, fmt.Sprintf("version: mod.json says %q, the submission says %q", mj.ModVersion, s.Version))
			}
			ugc := strings.Trim(string(mj.CreatorUGCIdentity), `" `)
			if s.WorkshopID != "" && ugc != "" && ugc != "0" && ugc != "null" && ugc != s.WorkshopID {
				r.Problems = append(r.Problems, fmt.Sprintf("workshop_id: mod.json belongs to Workshop item %s, not %s", ugc, s.WorkshopID))
			}
		}
	}
	if s.WorkshopID != "" {
		r.Holds = append(r.Holds, "Steam Workshop ID: claiming to replace Workshop item "+s.WorkshopID+" sends this to everyone who has the old copy, so the owner confirms it's really the author")
	}
	// Only the kinds of files mods are made of.
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if !allowedFile(d.Name()) {
			r.Holds = append(r.Holds, "File type: "+filepath.ToSlash(rel)+" isn't C# source, JSON, an image, a sound, a font, text or a contraption file")
		}
		return nil
	})
	rep, err := scan.Dir(dir)
	if err != nil {
		r.Problems = append(r.Problems, "scan: "+err.Error())
		return r
	}
	r.ScanMax = "none"
	if rep.Max() >= 0 {
		r.ScanMax = rep.Max().String()
	}
	seen := map[string]bool{}
	for _, f := range rep.Findings {
		line := fmt.Sprintf("[%s %s] %s: %s", f.Severity, f.Rule, filepath.ToSlash(f.File), f.Detail)
		if f.Severity >= scan.Medium {
			r.Findings = append(r.Findings, line)
			if !seen[f.Rule] {
				seen[f.Rule] = true
				r.Rules = append(r.Rules, f.Rule)
			}
		}
		switch {
		case f.Severity >= scan.Critical && wormRules[f.Rule]:
			r.Problems = append(r.Problems, "does what the worm did, never published: "+line)
		case f.Severity >= scan.High:
			r.Holds = append(r.Holds, "Safety scan: "+line)
		}
	}
	sort.Strings(r.Rules)
	return r
}

var allowedExt = map[string]bool{".cs": true, ".json": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".bmp": true, ".tga": true, ".wav": true, ".ogg": true, ".mp3": true, ".txt": true, ".md": true, ".jaap": true,
	".outline": true, ".ttf": true, ".otf": true}

// allowedFile reports whether a file is a kind mods are made of.
func allowedFile(name string) bool {
	switch strings.ToLower(name) {
	case "license", "readme", "changelog", "credits":
		return true
	}
	return allowedExt[strings.ToLower(filepath.Ext(name))]
}

func archiveExt(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ".zip"
	}
	defer f.Close()
	head := make([]byte, 8)
	io.ReadFull(f, head)
	switch {
	case strings.HasPrefix(string(head), "Rar!"):
		return ".rar"
	case strings.HasPrefix(string(head), "7z\xbc\xaf"):
		return ".7z"
	}
	return ".zip"
}

// Entry is one published mod in the index.
type Entry struct {
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Author          string    `json:"author"`
	Kind            string    `json:"kind"`
	Version         string    `json:"version"`
	Description     string    `json:"description,omitempty"`
	Tags            []string  `json:"tags,omitempty"`
	Image           string    `json:"image,omitempty"`
	WorkshopID      string    `json:"workshop_id,omitempty"`
	License         string    `json:"license,omitempty"`
	File            string    `json:"file"` // permanent copy: a release asset of Repo
	SHA256          string    `json:"sha256"`
	Size            int64     `json:"size"`
	ScanMax         string    `json:"scan_max"`
	Findings        []string  `json:"findings,omitempty"`
	Published       time.Time `json:"published"` // this version
	First           time.Time `json:"first"`     // first version
	Commit          string    `json:"commit"`    // the reviewed commit that published it
	Maintainers     []string  `json:"maintainers"`
	Downloads       int       `json:"downloads,omitempty"`
	Owner           string    `json:"owner"`              // GitHub account that publishes it
	Issue           int       `json:"issue,omitempty"`    // the submission issue of this version
	Reviewed        bool      `json:"reviewed,omitempty"` // a maintainer vouched for this version
	Approved        bool      `json:"approved,omitempty"` // published with the owner's approval of its holds
	Rules           []string  `json:"rules,omitempty"`    // scanner rules found (an update adding new ones needs approval)
	Withdrawn       bool      `json:"withdrawn,omitempty"`
	WithdrawnReason string    `json:"withdrawn_reason,omitempty"`
}

// Index is index.json.
type Index struct {
	Generated time.Time `json:"generated"`
	// Paused freezes the Open Workshop while a worm may be spreading: no
	// submission is published and the app installs and updates nothing
	// from it. Only the owner's "pause"/"resume" run changes it.
	Paused       bool       `json:"paused,omitempty"`
	PausedReason string     `json:"paused_reason,omitempty"`
	PausedAt     *time.Time `json:"paused_at,omitempty"`
	// SuspectSince: versions published at or after it are treated as
	// withdrawn until the owner resumes (the worm may have been spreading
	// before anyone noticed).
	SuspectSince *time.Time `json:"suspect_since,omitempty"`
	Entries      []Entry    `json:"entries"`
}

// Blocked returns why an entry must not be installed, kept or updated to:
// it was withdrawn, or the Open Workshop is paused and this version was
// published in the suspect window. "" means it is fine.
func (ix *Index) Blocked(e *Entry) string {
	switch {
	case e.Withdrawn:
		return e.WithdrawnReason
	case ix.Paused && ix.SuspectSince != nil && !e.Published.Before(*ix.SuspectSince):
		return "published while a worm may have been spreading (" + ix.PausedReason + "); held back until the Open Workshop resumes"
	}
	return ""
}

// AssetName is the release asset for a version: content-addressed, so a
// published file can never be replaced by another one under the same name.
func AssetName(slug, ver, sha, ext string) string {
	v := regexp.MustCompile(`[^0-9A-Za-z.]+`).ReplaceAllString(ver, "-")
	return fmt.Sprintf("%s-%s-%s%s", slug, v, sha[:12], ext)
}

// AssetURL is where a release asset is downloaded from.
func AssetURL(name string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", Repo, FilesTag, url.PathEscape(name))
}

// Sort orders the index (stable output, small diffs).
func (ix *Index) Sort() {
	sort.Slice(ix.Entries, func(i, j int) bool { return ix.Entries[i].Slug < ix.Entries[j].Slug })
}

// Find returns the entry for a slug.
func (ix *Index) Find(slug string) *Entry {
	for i := range ix.Entries {
		if ix.Entries[i].Slug == slug {
			return &ix.Entries[i]
		}
	}
	return nil
}

// Marshal is the exact bytes that are published and signed.
func (ix *Index) Marshal() []byte {
	b, _ := json.MarshalIndent(ix, "", " ")
	return append(b, '\n')
}

// Sign signs index bytes with a base64 ed25519 private key.
func Sign(index []byte, privB64 string) (string, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil || len(k) != ed25519.PrivateKeySize {
		return "", errors.New("bad signing key")
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(k), index)), nil
}

// Verify checks an index signature against a base64 ed25519 public key.
func Verify(index []byte, sigB64, pubB64 string) bool {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(k) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	return err == nil && ed25519.Verify(ed25519.PublicKey(k), index, sig)
}

// NewKey makes a signing key pair (base64).
func NewKey() (pub, priv string, err error) {
	p, k, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(p), base64.StdEncoding.EncodeToString(k), nil
}
