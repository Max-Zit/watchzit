package poller

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"time"

	"github.com/max-zit/watchzit/internal/discovery"
	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
)

// Jobs the console gives a remote collector besides its checks: scan a
// network range for discovery, or read a device over SNMP (facts,
// interfaces, VLANs) for its device page and maps.

// Job kinds.
const (
	JobDiscover = "discover"
	JobRead     = "read"
	JobSNMP     = "snmp" // SNMP discovery of a device: facts, interfaces and the checks it offers
)

// RemoteJob is one job (proto.TypeCollectorJob).
type RemoteJob struct {
	ID      string               `json:"id"`
	Kind    string               `json:"kind"`
	CIDR    string               `json:"cidr,omitempty"`    // discover
	Address string               `json:"address,omitempty"` // read
	Profile int64                `json:"profile,omitempty"` // read: the device's SNMP profile
	SNMP    map[int64]RemoteSNMP `json:"snmp,omitempty"`    // profiles with secrets opened
}

// RemoteJobResult answers a job (proto.TypeCollectorJobDone).
type RemoteJobResult struct {
	ID    string           `json:"id"`
	Error string           `json:"error,omitempty"`
	Hosts []discovery.Host `json:"hosts,omitempty"`
	Read  *DeviceRead      `json:"read,omitempty"`
	SNMP  *SNMPFound       `json:"snmp,omitempty"`
}

// RunJob does a job on the collector.
func RunJob(ctx context.Context, j RemoteJob) RemoteJobResult {
	out := RemoteJobResult{ID: j.ID}
	var err error
	switch j.Kind {
	case JobDiscover:
		out.Hosts, err = discover(ctx, j)
	case JobRead:
		out.Read, err = readRemote(ctx, j)
	case JobSNMP:
		out.SNMP, err = snmpRemote(ctx, j)
	default:
		err = errors.New("unknown job")
	}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

func dialProfile(rs RemoteSNMP, a netip.Addr, timeout time.Duration, retries int) (*snmp.Client, error) {
	if rs.Port < 1 || rs.Port > 65535 {
		return nil, errors.New("the device has no SNMP profile")
	}
	cfg := rs.Config
	cfg.Timeout, cfg.Retries = timeout, retries
	return snmp.Dial(netip.AddrPortFrom(a, uint16(rs.Port)), cfg) // #nosec G115 -- checked above
}

func discover(ctx context.Context, j RemoteJob) ([]discovery.Host, error) {
	addrs, err := discovery.Range(j.CIDR)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	ids := make([]int64, 0, len(j.SNMP))
	for id := range j.SNMP {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	pr := &discovery.Prober{
		Ping: func(ctx context.Context, a netip.Addr) (time.Duration, bool) {
			r, err := probe.Ping(ctx, a, 1, time.Second)
			if err != nil || r.Received == 0 {
				return 0, false
			}
			rtt, _ := r.AvgRTT()
			return rtt, true
		},
		Dial: discovery.DialTCP,
		Identify: func(ctx context.Context, a netip.Addr) (string, string, string, int64) {
			for _, id := range ids {
				c, err := dialProfile(j.SNMP[id], a, 1500*time.Millisecond, 0)
				if err != nil {
					continue
				}
				f, err := snmpmon.Facts(ctx, c)
				_ = c.Close()
				if err == nil {
					return f.SysName, f.SysDescr, f.Vendor, id
				}
			}
			return "", "", "", 0
		},
		Lookup: discovery.LookupName,
	}
	return pr.Scan(ctx, addrs), nil
}

func readRemote(ctx context.Context, j RemoteJob) (*DeviceRead, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	a, err := probe.Resolve(ctx, j.Address)
	if err != nil {
		return nil, err
	}
	rs, ok := j.SNMP[j.Profile]
	if !ok {
		return nil, errors.New("the device has no SNMP profile")
	}
	cl, err := dialProfile(rs, a, 3*time.Second, 1)
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	return ReadDevice(ctx, cl)
}

// SNMPFound is what the SNMP discovery of a device found.
type SNMPFound struct {
	Facts      store.Facts        `json:"facts"`
	Interfaces []store.Interface  `json:"interfaces,omitempty"`
	Proposals  []snmpmon.Proposal `json:"proposals,omitempty"`
	Error      string             `json:"error,omitempty"` // a part that failed (the rest is there)
}

func snmpRemote(ctx context.Context, j RemoteJob) (*SNMPFound, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	a, err := probe.Resolve(ctx, j.Address)
	if err != nil {
		return nil, err
	}
	rs, ok := j.SNMP[j.Profile]
	if !ok {
		return nil, errors.New("the device has no SNMP profile")
	}
	if rs.Port < 1 || rs.Port > 65535 {
		return nil, errors.New("the SNMP port is wrong")
	}
	snmp.Forget(netip.AddrPortFrom(a, uint16(rs.Port))) // #nosec G115 -- checked in dialProfile
	cl, err := dialProfile(rs, a, 3*time.Second, 1)
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	f, ifs, props, err := snmpmon.Discover(ctx, cl)
	out := &SNMPFound{Facts: f, Interfaces: ifs, Proposals: props}
	if err != nil {
		if len(props) == 0 {
			return nil, err
		}
		out.Error = err.Error()
	}
	return out, nil
}
