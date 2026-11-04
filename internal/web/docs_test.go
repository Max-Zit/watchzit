package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every guide page exists in English and Serbian (the hand-written ones), starts with its title
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

// TestDocTranslations: every console language has every guide page, with
// the same headings (ids), links and element structure as the English page, so links and
// search anchors work in every language. A missing language is reported
// unless WZ_DOCS_PARTIAL is set (while translating).
func TestDocTranslations(t *testing.T) {
	ids := regexp.MustCompile(`<h[1-6][^>]*\bid="([^"]+)"`)
	hrefs := regexp.MustCompile(`\bhref="([^"]+)"`)
	tags := regexp.MustCompile(`<(h[1-6]|table|tr|ul|ol|li|pre|code|a|kbd|img)\b`)
	skel := func(re *regexp.Regexp, s string) string {
		var out []string
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return strings.Join(out, "|")
	}
	for _, l := range Languages {
		if l.Code == "en" {
			continue
		}
		for _, p := range DocPages {
			en, _ := docsFS.ReadFile("docs/en/" + p.Slug + ".html")
			b, err := docsFS.ReadFile("docs/" + l.Code + "/" + p.Slug + ".html")
			if err != nil {
				if os.Getenv("WZ_DOCS_PARTIAL") == "" {
					t.Errorf("%s/%s missing", l.Code, p.Slug)
				}
				continue
			}
			for _, c := range []struct {
				what string
				re   *regexp.Regexp
			}{{"heading ids", ids}, {"links", hrefs}, {"tags", tags}} {
				if a, b := skel(c.re, string(en)), skel(c.re, string(b)); a != b {
					t.Errorf("%s/%s: %s differ from English\n en: %.300s\n %s: %.300s", l.Code, p.Slug, c.what, a, l.Code, b)
				}
			}
			if !strings.HasPrefix(string(b), "<h1>") || strings.Contains(string(b), "<script") {
				t.Errorf("%s/%s: must start with <h1> and have no script", l.Code, p.Slug)
			}
		}
	}
}
