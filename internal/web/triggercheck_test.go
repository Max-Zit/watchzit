package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// TestTriggerOneCheck: an SNMP OID value (each with its own unit) is
// watched check by check — a trigger from the device page watches that
// check only, and one for all checks is refused.
func TestTriggerOneCheck(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "mikrotik", "address", "10.0.0.1", "kind", "router")
	temp, err := e.st.CreateCheck(ctx, 1, store.CheckInput{Type: store.CheckSNMPOID, Name: "Temperature", Params: store.CheckParams{OID: "1.3.6.1.4.1.14988.1.1.3.10.0", Unit: "°C"}, IntervalS: 60})
	if err != nil {
		t.Fatal(err)
	}
	volt, err := e.st.CreateCheck(ctx, 1, store.CheckInput{Type: store.CheckSNMPOID, Name: "Voltage", Params: store.CheckParams{OID: "1.3.6.1.4.1.14988.1.1.3.8.0", Unit: "V"}, IntervalS: 60})
	if err != nil {
		t.Fatal(err)
	}
	tid := strconv.FormatInt(temp, 10)
	if b := c.get("/devices/1").body; !strings.Contains(b, "/alerts/triggers?check="+tid) {
		t.Fatal("no Add trigger link on the check")
	}
	if b := c.get("/alerts/triggers?check=" + tid).body; !strings.Contains(b, `name="check" value="`+tid+`" checked`) || !strings.Contains(b, `value="value"`) {
		t.Fatal("the form does not watch the check")
	}
	if r := c.post("/alerts/triggers", "name", "Any OID", "kind", "metric", "metric", "value", "op", ">", "threshold", "60", "severity", "warning", "enabled", "1"); r.code != http.StatusBadRequest {
		t.Fatalf("an OID trigger for all checks was accepted: %d", r.code)
	}
	if r := c.post("/alerts/triggers", "name", "Hot router", "kind", "metric", "metric", "value", "op", ">", "threshold", "60", "for", "0", "severity", "critical", "enabled", "1", "check", tid); r.code != http.StatusSeeOther {
		t.Fatalf("trigger: %d %s", r.code, r.body)
	}
	ts, _ := e.st.Triggers(ctx)
	var tr *store.Trigger
	for _, x := range ts {
		if x.Name == "Hot router" {
			tr = x
		}
	}
	if tr == nil || tr.CheckID != temp || tr.DeviceID != 1 || tr.Check != "Temperature" {
		t.Fatalf("stored: %+v", tr)
	}
	if b := c.get("/alerts/triggers").body; !strings.Contains(b, "mikrotik · Temperature") {
		t.Fatal("the list does not name the check")
	}

	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	// both are above 60 in their units; only the temperature counts
	for id, v := range map[int64]float64{temp: 71, volt: 230} {
		if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: id, At: e.now, Status: store.StatusUp, Text: "ok", Values: map[string]float64{"value": v}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	ps, _ := e.st.OpenProblems(ctx)
	n := 0
	for _, p := range ps {
		if p.TriggerID == tr.ID {
			n++
			if p.CheckID != temp || !strings.Contains(p.Detail, "71") {
				t.Fatalf("problem: %+v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d problems from the one-check trigger", n)
	}
	// removing the check removes its trigger
	c.post("/checks/" + tid + "/delete")
	if _, err := e.st.TriggerByID(ctx, tr.ID); err == nil {
		t.Fatal("the trigger stayed without its check")
	}
}
