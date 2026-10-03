package sources

import (
	"os"
	"strings"
	"testing"
)

func TestParseSkyDetails(t *testing.T) {
	b, err := os.ReadFile("testdata/skymods_item.html")
	if err != nil {
		t.Fatal(err)
	}
	d := ParseSkyDetails(string(b))
	if !strings.HasPrefix(d.Description, "Quick Draw") || !strings.Contains(d.Description, "Grip Editor") {
		t.Fatalf("description: %.200q", d.Description)
	}
	if strings.Contains(d.Description, "<") {
		t.Fatalf("html left in description: %.200q", d.Description)
	}
	if len(d.Images) != 1 || len(d.Required) != 0 {
		t.Fatalf("images %v required %v", d.Images, d.Required)
	}

	b, err = os.ReadFile("testdata/skymods_item_required.html")
	if err != nil {
		t.Fatal(err)
	}
	d = ParseSkyDetails(string(b))
	if len(d.Required) != 2 || d.Required[0].WorkshopID != "2616697628" || d.Required[1].Title != "WW2 Germany & Tanks Mod" {
		t.Fatalf("required: %+v", d.Required)
	}
}

func TestHTMLText(t *testing.T) {
	got := HTMLText(`<p>Hello&nbsp;<b>world</b></p><script>alert(1)</script><ul><li>one</li><li>two</li></ul>`)
	if got != "Hello world\n\n• one\n• two" && !strings.Contains(got, "• two") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "alert") {
		t.Fatal("script content kept")
	}
}
