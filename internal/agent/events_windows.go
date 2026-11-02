//go:build windows

package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// readEvents asks wevtutil for critical and error events of the System and
// Application logs since a time.
func readEvents(ctx context.Context, since time.Time) ([]proto.Event, error) {
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "wevtutil.exe")
	q := "*[System[(Level=1 or Level=2) and TimeCreated[@SystemTime>='" + since.UTC().Format("2006-01-02T15:04:05.000Z") + "']]]"
	var all []proto.Event
	var errs []error
	for _, log := range []string{"System", "Application"} {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		out, err := exec.CommandContext(cctx, exe, "qe", log, "/q:"+q, "/f:RenderedXml", "/c:200").Output() // #nosec G204 G702 -- fixed program; the query holds only a formatted time
		cancel()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		all = append(all, parseWinEvents(out)...)
	}
	if len(all) == 0 && len(errs) == 2 {
		return nil, errors.Join(errs...)
	}
	return all, nil
}

type winEvent struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID     int `xml:"EventID"`
		Level       int `xml:"Level"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
		Channel string `xml:"Channel"`
	} `xml:"System"`
	Message string `xml:"RenderingInfo>Message"`
}

// parseWinEvents reads the <Event> elements wevtutil prints one after the
// other.
func parseWinEvents(b []byte) []proto.Event {
	d := xml.NewDecoder(bytes.NewReader(b)) // #nosec G709 -- the output of wevtutil on this computer, read into plain fields
	var evs []proto.Event
	for {
		tok, err := d.Token()
		if err == io.EOF || err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && tok == nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Event" {
			continue
		}
		var e winEvent
		if d.DecodeElement(&e, &se) != nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, e.System.TimeCreated.SystemTime)
		lvl := 3 // error
		if e.System.Level == 1 {
			lvl = 2 // critical
		}
		msg := strings.Join(strings.Fields(e.Message), " ")
		if msg == "" {
			msg = "(no text)"
		}
		evs = append(evs, proto.Event{Time: at.UTC(), Level: lvl, Source: e.System.Provider.Name, Log: e.System.Channel, ID: e.System.EventID, Message: msg})
	}
	return evs
}
