//go:build linux

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// readEvents asks journalctl for messages of priority err and worse.
func readEvents(ctx context.Context, since time.Time) ([]proto.Event, error) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return nil, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "journalctl", "--no-pager", "-q", "-o", "json", "-p", "err", "-n", "500",
		"--since", "@"+strconv.FormatInt(since.Unix(), 10)).Output() // #nosec G204 -- fixed program, a number
	if err != nil {
		return nil, err
	}
	return parseJournal(out), nil
}

// parseJournal reads journalctl -o json lines.
func parseJournal(b []byte) []proto.Event {
	var evs []proto.Event
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		text := func(k string) string {
			switch v := m[k].(type) {
			case string:
				return v
			case []any: // binary fields come as byte arrays
				var sb strings.Builder
				for _, x := range v {
					if f, ok := x.(float64); ok && f >= 0 && f < 256 {
						sb.WriteByte(byte(f))
					}
				}
				return sb.String()
			}
			return ""
		}
		us, _ := strconv.ParseInt(text("__REALTIME_TIMESTAMP"), 10, 64)
		lvl, err := strconv.Atoi(text("PRIORITY"))
		if err != nil {
			lvl = 3
		}
		src := text("SYSLOG_IDENTIFIER")
		if u := text("_SYSTEMD_UNIT"); u != "" && src == "" {
			src = u
		}
		msg := strings.TrimSpace(text("MESSAGE"))
		if msg == "" {
			continue
		}
		evs = append(evs, proto.Event{Time: time.UnixMicro(us).UTC(), Level: lvl, Source: src, Message: msg})
	}
	return evs
}
