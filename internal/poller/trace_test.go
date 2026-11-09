package poller

import (
	"testing"

	"github.com/max-zit/watchzit/internal/store"
)

func TestPathChanged(t *testing.T) {
	a := []string{"10.0.0.1", "", "192.0.2.1"}
	for _, c := range []struct {
		now  []string
		want bool
	}{
		{[]string{"10.0.0.1", "", "192.0.2.1"}, false},
		{[]string{"10.0.0.1", "10.9.9.9", "192.0.2.1"}, false}, // a hop that was silent answers now
		{[]string{"", "", "192.0.2.1"}, false},                 // and the other way round
		{[]string{"10.0.0.2", "", "192.0.2.1"}, true},
		{[]string{"10.0.0.1", "192.0.2.1"}, true}, // one hop shorter
	} {
		if got := pathChanged(a, c.now); got != c.want {
			t.Errorf("%v → %v: %v", a, c.now, got)
		}
	}
}

func TestLossFrom(t *testing.T) {
	h := func(ttl int, addr string, recv int) store.PathHop {
		return store.PathHop{TTL: ttl, Addr: addr, Sent: 10, Recv: recv}
	}
	// hop 2 limits its answers (hop 3 loses nothing): no loss on the path
	if f := lossFrom([]store.PathHop{h(1, "a", 10), h(2, "b", 2), h(3, "c", 10)}); f != nil {
		t.Errorf("rate limit named: %+v", f)
	}
	// loss from hop 2 on, a silent hop between
	if f := lossFrom([]store.PathHop{h(1, "a", 10), h(2, "b", 7), h(3, "", 0), h(4, "d", 7)}); f == nil || f.TTL != 2 {
		t.Errorf("loss from: %+v", f)
	}
}
