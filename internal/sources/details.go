package sources

import (
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
)

// Details is what the GUI's overview shows for one mod, before installing.
type Details struct {
	Description string     `json:"description"` // plain text, paragraphs separated by blank lines
	Images      []string   `json:"images,omitempty"`
	Required    []Required `json:"required,omitempty"` // Workshop items this one depends on
	Downloads   int        `json:"downloads,omitempty"`
	Likes       int        `json:"likes,omitempty"`
	Views       int        `json:"views,omitempty"`
}

type Required struct {
	Title      string `json:"title"`
	WorkshopID string `json:"workshop_id"`
}

var (
	reBlockTags = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/h\d|/li|/tr|hr)[^>]*>`)
	reLiOpen    = regexp.MustCompile(`(?i)<\s*li[^>]*>`)
	reScripts   = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reTags      = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces    = regexp.MustCompile(`[ \t\x{00a0}]+`)
	reBlank     = regexp.MustCompile(`\n{3,}`)
)

// HTMLText turns a snippet of HTML into readable plain text.
func HTMLText(h string) string {
	h = reScripts.ReplaceAllString(h, "")
	h = reLiOpen.ReplaceAllString(h, "\n• ")
	h = reBlockTags.ReplaceAllString(h, "\n")
	h = reTags.ReplaceAllString(h, "")
	h = html.UnescapeString(h)
	lines := strings.Split(h, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var (
	reSkyDesc     = regexp.MustCompile(`(?s)<h5>Description:</h5>\s*</div>(.*?)(?:<div[^>]+id="required-items"|<div class="skymods-single-after"|</article>)`)
	reSkyRequired = regexp.MustCompile(`(?s)id="required-items".*?</div>\s*</div>`)
	reSkyReqItem  = regexp.MustCompile(`(?s)<a href="https://catalogue\.smods\.ru/\?s=(\d+)"[^>]*>(.*?)</a>`)
	reSkyBigImg   = regexp.MustCompile(`skymods-single-preview-wrap">\s*<img src="(https://[^"?]+)`)
)

// SkyDetails reads a Skymods item page.
func SkyDetails(pageURL string) (*Details, error) {
	if !strings.HasPrefix(pageURL, skyBase+"/archives/") {
		return nil, fmt.Errorf("not a Skymods item page: %s", pageURL)
	}
	resp, err := get(pageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return ParseSkyDetails(string(b)), nil
}

func ParseSkyDetails(page string) *Details {
	d := &Details{Description: HTMLText(first(reSkyDesc, page))}
	if img := first(reSkyBigImg, page); img != "" {
		d.Images = []string{img}
	}
	if block := reSkyRequired.FindString(page); block != "" {
		for _, m := range reSkyReqItem.FindAllStringSubmatch(block, -1) {
			d.Required = append(d.Required, Required{Title: HTMLText(m[2]), WorkshopID: m[1]})
		}
	}
	return d
}

// GBDetails reads a GameBanana mod's description, screenshots and counters.
func GBDetails(id int) (*Details, error) {
	var out struct {
		Text      string `json:"_sText"`
		Short     string `json:"_sDescription"`
		Downloads int    `json:"_nDownloadCount"`
		Likes     int    `json:"_nLikeCount"`
		Views     int    `json:"_nViewCount"`
		Preview   struct {
			Images []struct {
				Base string `json:"_sBaseUrl"`
				File string `json:"_sFile"`
				F530 string `json:"_sFile530"`
			} `json:"_aImages"`
		} `json:"_aPreviewMedia"`
	}
	u := fmt.Sprintf("%s/Mod/%d?_csvProperties=_sText,_sDescription,_nDownloadCount,_nLikeCount,_nViewCount,_aPreviewMedia", gbAPI, id)
	if err := getJSON(u, &out); err != nil {
		return nil, err
	}
	d := &Details{Description: HTMLText(out.Text), Downloads: out.Downloads, Likes: out.Likes, Views: out.Views}
	if d.Description == "" {
		d.Description = out.Short
	} else if out.Short != "" && !strings.Contains(d.Description, out.Short) {
		d.Description = out.Short + "\n\n" + d.Description
	}
	for _, im := range out.Preview.Images {
		f := im.F530
		if f == "" {
			f = im.File
		}
		if im.Base != "" && f != "" {
			d.Images = append(d.Images, im.Base+"/"+f)
		}
	}
	return d, nil
}
