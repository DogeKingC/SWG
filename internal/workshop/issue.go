package workshop

import (
	"errors"
	"regexp"
	"strings"
)

// Submissions arrive as GitHub issues made with the form in
// .github/ISSUE_TEMPLATE/workshop-submit.yml. GitHub renders a form as
// "### <label>" headings, each followed by the answer ("_No response_" when
// an optional field is empty). The text is untrusted data: it is only
// parsed, never run or put in a command.

// Form labels (keep in sync with the issue template).
const (
	fName     = "Name"
	fAuthor   = "Author"
	fKind     = "Type"
	fVersion  = "Version"
	fDownload = "File"
	fDesc     = "Description"
	fTags     = "Tags"
	fWS       = "Steam Workshop ID"
	fLicense  = "License"
	fImage    = "Picture"
)

var (
	reHeading = regexp.MustCompile(`(?m)^###[ \t]+(.+?)[ \t]*$`)
	reURL     = regexp.MustCompile(`https://[^\s<>"'()\[\]]+`)
	reSlugBad = regexp.MustCompile(`[^a-z0-9]+`)
)

// ParseIssueForm splits an issue form's body into label -> answer.
func ParseIssueForm(body string) map[string]string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	out := map[string]string{}
	idx := reHeading.FindAllStringSubmatchIndex(body, -1)
	for i, m := range idx {
		label := strings.TrimSpace(body[m[2]:m[3]])
		end := len(body)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		v := strings.TrimSpace(body[m[1]:end])
		if v == "_No response_" || v == "None" {
			v = ""
		}
		out[label] = v
	}
	return out
}

// Slugify makes an entry's name from a mod's name.
func Slugify(name string) string {
	s := strings.Trim(reSlugBad.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

// SubmissionFromIssue builds a submission from an issue form. The file is
// the first https link in the File field: a link the author pasted, or the
// address GitHub gives a zip dragged into the field.
func SubmissionFromIssue(body, author string) (*Submission, error) {
	f := ParseIssueForm(body)
	if len(f) == 0 {
		return nil, errors.New("this issue wasn't made with the submission form")
	}
	one := func(k string) string { return strings.TrimSpace(strings.SplitN(f[k], "\n", 2)[0]) }
	s := &Submission{
		Name: one(fName), Author: one(fAuthor), Version: one(fVersion), Description: strings.TrimSpace(f[fDesc]),
		WorkshopID: one(fWS), License: one(fLicense), Maintainers: []string{author},
	}
	switch strings.ToLower(one(fKind)) {
	case "mod":
		s.Kind = "mod"
	case "contraption":
		s.Kind = "contraption"
	}
	if u := reURL.FindString(f[fDownload]); u != "" {
		s.Download = strings.TrimRight(u, ".,;")
	}
	if u := reURL.FindString(f[fImage]); u != "" {
		s.Image = strings.TrimRight(u, ".,;")
	}
	for _, t := range strings.Split(f[fTags], ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			s.Tags = append(s.Tags, t)
		}
	}
	return s, nil
}
