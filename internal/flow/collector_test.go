package flow

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

type fakeStore struct{ rows []store.FlowRow }

func (f *fakeStore) AddFlows(_ context.Context, rows []store.FlowRow) error {
	f.rows = append(f.rows, rows...)
	return nil
}
func (f *fakeStore) DeviceAddresses(context.Context) (map[netip.Addr]int64, error) {
	return map[netip.Addr]int64{exp: 3}, nil
}
func (f *fakeStore) PruneFlows(context.Context, time.Time) (int64, error) { return 0, nil }

func v5(src, dst string, bytes uint32, sp, dp uint16) []byte {
	b := buf{}.u16(5).u16(1).u32(0).u32(0).u32(0).u32(0).u8(0).u8(0).u16(0)
	return buf(b).ip(src).ip(dst).ip("0.0.0.0").u16(0).u16(0).u32(1).u32(bytes).u32(0).u32(0).u16(sp).u16(dp).u8(0).u8(0).u8(6).u8(0).u16(0).u16(0).u8(0).u8(0).u16(0)
}

// TestCollector: flows of known exporters are summed per conversation and
// minute; unknown exporters and garbage are left out.
func TestCollector(t *testing.T) {
	st := &fakeStore{}
	c := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 10, 31, 10, 0, 30, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.RefreshDevices(context.Background())
	c.Take(exp, v5("10.0.0.5", "8.8.8.8", 100, 50000, 443))
	c.Take(exp, v5("10.0.0.5", "8.8.8.8", 50, 50001, 443)) // same conversation, other client port
	c.Take(exp, v5("10.0.0.6", "8.8.8.8", 10, 50000, 443))
	c.Take(netip.MustParseAddr("192.0.2.1"), v5("10.0.0.7", "8.8.8.8", 10, 50000, 443))
	c.Take(exp, []byte("hello"))
	c.Flush(context.Background(), false)
	if len(st.rows) != 0 {
		t.Fatal("the minute was stored before it was over")
	}
	now = now.Add(time.Minute)
	c.Flush(context.Background(), false)
	if len(st.rows) != 2 || st.rows[0].Bytes != 150 || st.rows[0].Port != 443 || st.rows[0].DeviceID != 3 || !st.rows[0].At.Equal(now.Add(-time.Minute).Truncate(time.Minute)) {
		t.Fatalf("rows: %+v", st.rows)
	}
	_, dg, recs, errs, _ := c.Status()
	if dg != 5 || recs != 3 || errs != 1 {
		t.Fatalf("status %d %d %d", dg, recs, errs)
	}
}
