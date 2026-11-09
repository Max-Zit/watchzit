package probe

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"sort"
	"time"
)

// Hop is one router (or the destination) on the way to a device, as seen
// by a traceroute with several probes per TTL.
type Hop struct {
	TTL   int
	Addr  netip.Addr // the address that answered most often; invalid: none
	Other int        // answers from other addresses (several equal paths)
	Sent  int
	Recv  int
	RTTs  []time.Duration
	Final bool // the destination itself
}

// Loss is the share of probes without an answer, in percent.
func (h Hop) Loss() float64 {
	if h.Sent == 0 {
		return 100
	}
	return float64(h.Sent-h.Recv) / float64(h.Sent) * 100
}

// Stats are the best, average and worst round-trip time in milliseconds.
func (h Hop) Stats() (best, avg, worst float64, ok bool) {
	if len(h.RTTs) == 0 {
		return 0, 0, 0, false
	}
	best = math.Inf(1)
	var sum float64
	for _, d := range h.RTTs {
		v := float64(d) / float64(time.Millisecond)
		best, worst = math.Min(best, v), math.Max(worst, v)
		sum += v
	}
	return best, sum / float64(len(h.RTTs)), worst, true
}

// probeReply is one answer to a probe sent with a TTL in a round.
type probeReply struct {
	round, ttl int
	from       netip.Addr
	rtt        time.Duration
	final      bool // an echo reply from the destination (or it refused the probe)
}

// Trace sends probes rounds of ICMP echo requests with TTL 1..maxHops to
// dst and returns the hops up to the destination (or up to maxHops when it
// does not answer). wait is how long a round waits for answers.
func Trace(ctx context.Context, dst netip.Addr, maxHops, probes int, wait time.Duration) ([]Hop, error) {
	if maxHops < 1 || maxHops > 64 || probes < 1 || probes > 10 {
		return nil, errors.New("1–64 hops and 1–10 probes per hop")
	}
	tr, err := newTracer(dst)
	if err != nil {
		return nil, err
	}
	defer tr.close()
	limit := maxHops // lowered once the destination answered
	var replies []probeReply
	sent := make([]int, maxHops+1)
	for round := 0; round < probes; round++ {
		if ctx.Err() != nil {
			break
		}
		rs, err := tr.round(ctx, round, limit, wait)
		if err != nil {
			return nil, err
		}
		for ttl := 1; ttl <= limit; ttl++ {
			sent[ttl]++
		}
		for _, r := range rs {
			if r.final && r.ttl < limit {
				limit = r.ttl
			}
		}
		replies = append(replies, rs...)
	}
	return hopsOf(replies, sent, limit), nil
}

// hopsOf gathers the answers per TTL up to the last one.
func hopsOf(replies []probeReply, sent []int, last int) []Hop {
	hops := make([]Hop, last)
	addrs := make([]map[netip.Addr]int, last)
	for i := range hops {
		hops[i] = Hop{TTL: i + 1, Sent: sent[i+1]}
		addrs[i] = map[netip.Addr]int{}
	}
	for _, r := range replies {
		if r.ttl < 1 || r.ttl > last {
			continue
		}
		h := &hops[r.ttl-1]
		h.Recv++
		h.RTTs = append(h.RTTs, r.rtt)
		h.Final = h.Final || r.final
		addrs[r.ttl-1][r.from]++
	}
	for i := range hops {
		type ac struct {
			a netip.Addr
			n int
		}
		var l []ac
		for a, n := range addrs[i] {
			l = append(l, ac{a, n})
		}
		sort.Slice(l, func(x, y int) bool { return l[x].n > l[y].n || (l[x].n == l[y].n && l[x].a.Less(l[y].a)) })
		if len(l) > 0 {
			hops[i].Addr = l[0].a
			for _, o := range l[1:] {
				hops[i].Other += o.n
			}
		}
		if hops[i].Recv > hops[i].Sent {
			hops[i].Recv = hops[i].Sent // a duplicated answer
		}
	}
	return hops
}
