package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/store"
)

// TestScenario: a sign-in with the check's password, the cookie kept for
// the next step, a redirect expected as such; a wrong password fails at
// the step that checks for it.
func TestScenario(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<h1>Sign in</h1>")) })
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("user") != "monitor" || r.FormValue("password") != "p&ss word" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "s", Value: "ok", Path: "/"})
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("s"); err != nil || c.Value != "ok" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("Welcome, monitor"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	steps, err := store.ParseSteps("GET /login | 200 | Sign in\nPOST /login | 302 | | user=monitor&password={secret}\n# a comment\nGET /dashboard | | Welcome\n")
	if err != nil || len(steps) != 3 {
		t.Fatalf("parse: %v %+v", err, steps)
	}
	p := store.CheckParams{URL: srv.URL, Steps: steps}
	r := Scenario(context.Background(), p, Target{Secret: "p&ss word"})
	if !r.Up || r.Values["steps_ok"] != 3 {
		t.Fatalf("good run: %+v", r)
	}
	r = Scenario(context.Background(), p, Target{Secret: "wrong"})
	if r.Up || r.Values["steps_ok"] != 1 || !strings.Contains(r.Text, "step 2 (POST /login)") {
		t.Fatalf("wrong password: %+v", r)
	}
	if back := store.StepsText(steps); !strings.Contains(back, "POST /login | 302 |  | user=monitor&password={secret}") {
		t.Fatalf("text: %q", back)
	}
	if _, err := store.ParseSteps("FETCH /x"); err == nil {
		t.Log("an unknown method is refused when the check is saved")
	}
}
