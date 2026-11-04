package web_test

import "testing"

// TestOddIDs: huge numbers in filters do not break pages (the SQL compared
// them as int4), and a malformed id in a path is not found instead of
// showing the "new" form.
func TestOddIDs(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	huge := "64374999022756117317911099462587766518629200"
	for _, u := range []string{
		"/devices?group=" + huge, "/devices?group=3000000000",
		"/logs?device=" + huge, "/logs?device=3000000000&before=3000000000",
		"/reports?group=3000000000&period=30d",
		"/audit?before=3000000000",
		"/problems?device=3000000000", "/problems?device=" + huge,
	} {
		if r := c.get(u); r.code != 200 {
			t.Errorf("%s: %d", u, r.code)
		}
	}
	for _, u := range []string{"/alerts/triggers/3.bak", "/alerts/triggers/3%20-%20Copy", "/settings/tools/1.bak", "/alerts/triggers/0"} {
		if r := c.get(u); r.code != 404 {
			t.Errorf("%s: %d", u, r.code)
		}
	}
}
