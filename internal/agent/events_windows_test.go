//go:build windows

package agent

import (
	"context"
	"testing"
	"time"
)

func TestParseWinEvents(t *testing.T) {
	x := `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='disk'/><EventID>7</EventID><Level>2</Level><TimeCreated SystemTime='2026-11-02T10:00:00.1234567Z'/><Channel>System</Channel></System><RenderingInfo Culture='en-US'><Message>The device, \Device\Harddisk1\DR1, has a bad block.</Message></RenderingInfo></Event>` +
		`<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Application Error'/><EventID>1000</EventID><Level>1</Level><TimeCreated SystemTime='2026-11-02T10:01:00Z'/><Channel>Application</Channel></System></Event>`
	evs := parseWinEvents([]byte(x))
	if len(evs) != 2 || evs[0].Source != "disk" || evs[0].ID != 7 || evs[0].Level != 3 || evs[0].Log != "System" || evs[1].Level != 2 || evs[1].Message != "(no text)" {
		t.Fatalf("%+v", evs)
	}
	if evs[0].Time.Minute() != 0 || evs[1].Time.Minute() != 1 {
		t.Fatalf("times %v %v", evs[0].Time, evs[1].Time)
	}
}

// TestReadEventsHere: wevtutil answers on this computer.
func TestReadEventsHere(t *testing.T) {
	if _, err := readEvents(context.Background(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
}
