package web

import (
	"strings"
	"testing"
)

// Every guide page exists in English and Serbian, starts with its title
// and has ids on its headings so search results can link to them.
func TestDocPages(t *testing.T) {
	for _, lang := range []string{"en", "sr"} {
		for _, p := range DocPages {
			b, err := docsFS.ReadFile("docs/" + lang + "/" + p.Slug + ".html")
			if err != nil {
				t.Errorf("%s/%s: %v", lang, p.Slug, err)
				continue
			}
			s := string(b)
			if !strings.HasPrefix(s, "<h1>") {
				t.Errorf("%s/%s: does not start with <h1>", lang, p.Slug)
			}
			if strings.Contains(s, "<h2>") || strings.Contains(s, "<script") {
				t.Errorf("%s/%s: <h2> without id or a script", lang, p.Slug)
			}
		}
	}
}

func TestSearchDocs(t *testing.T) {
	hits := searchDocs("en", "snmpv3 user")
	if len(hits) == 0 || hits[0].Slug == "" || hits[0].Anchor == "" {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(string(hits[0].Snippet), "<mark>") {
		t.Errorf("snippet not marked: %s", hits[0].Snippet)
	}
	if h := searchDocs("sr", "održavanje"); len(h) == 0 {
		t.Error("no Serbian hits")
	}
	if h := searchDocs("en", "<script>alert(1)</script>"); len(h) != 0 {
		t.Errorf("markup matched: %+v", h)
	}
	if h := searchDocs("de", "kiosk"); len(h) == 0 {
		t.Error("English fallback not searched")
	}
}
