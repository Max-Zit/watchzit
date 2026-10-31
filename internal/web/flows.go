package web

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/flow"
	"github.com/max-zit/watchzit/internal/store"
)

// Traffic flows: the collector's settings and the page with the top
// talkers, destinations, conversations and services.

const settingFlows = "flows"

// FlowConfig reads the collector's settings.
func (s *Server) FlowConfig(ctx context.Context) flow.Config {
	c := flow.Defaults()
	_ = s.store.GetSetting(ctx, settingFlows, &c)
	return c
}

type flowSettingsData struct {
	C                          flow.Config
	Datagrams, Records, Errors int64
	Err                        string
	Off                        bool
}

func (s *Server) handleFlowSettings(w http.ResponseWriter, r *http.Request) {
	d := &flowSettingsData{C: s.FlowConfig(r.Context()), Off: s.Flows == nil}
	if s.Flows != nil {
		_, d.Datagrams, d.Records, d.Errors, d.Err = s.Flows.Status()
	}
	s.render(w, r, http.StatusOK, "flowsettings", &Page{Title: "Traffic flows", Nav: "settings", Data: d})
}

func (s *Server) handleFlowSettingsSave(w http.ResponseWriter, r *http.Request) {
	const back = "/settings/flows"
	c := flow.Config{Enabled: r.FormValue("enabled") == "1", Unknown: r.FormValue("unknown") == "1",
		NetFlowPort: formInt(r, "netflow_port"), SFlowPort: formInt(r, "sflow_port"), KeepDays: formInt(r, "keep_days")}
	if c.NetFlowPort < 1 || c.NetFlowPort > 65535 || c.SFlowPort < 1 || c.SFlowPort > 65535 || c.NetFlowPort == c.SFlowPort {
		s.redirectErr(w, r, back, "Enter two different ports between 1 and 65535.")
		return
	}
	if c.KeepDays < 1 || c.KeepDays > 90 {
		s.redirectErr(w, r, back, "Keep the flows 1–90 days.")
		return
	}
	if err := s.store.SetSetting(r.Context(), settingFlows, c); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "settings.flows", "settings", "flows %v (NetFlow/IPFIX port %d, sFlow port %d), keep %d days", c.Enabled, c.NetFlowPort, c.SFlowPort, c.KeepDays)
	if s.Flows != nil {
		if err := s.Flows.Apply(c); err != nil {
			s.redirectErr(w, r, back, translate(s.lang(r, userFrom(r)), "Saved, but: %s", err.Error()))
			return
		}
	}
	s.redirect(w, r, back, "Saved.")
}

// flowLine is a row of a top list as the page shows it.
type flowLine struct {
	Src, Dst       string // address or device name
	SrcID, DstID   int64  // devices
	Service        string
	Exporter       string
	ExporterID     int64
	Bytes, Packets int64
	Pct            float64
	BPS            float64 // average bit/s over the range
}

type flowsData struct {
	DeviceID                                    int64
	Exporters                                   []*store.Device
	Range                                       string
	Ranges                                      []string
	Bytes, Packets                              int64
	BPS                                         float64
	Sources, Dests, Conversations, Services, By []flowLine
	On                                          bool // the collector is on
}

var flowRanges = []string{"1h", "6h", "24h", "7d"}

var protoNames = map[int]string{1: "ICMP", 2: "IGMP", 6: "TCP", 17: "UDP", 47: "GRE", 50: "ESP", 51: "AH", 58: "ICMPv6", 89: "OSPF", 112: "VRRP", 132: "SCTP"}

var portNames = map[int]string{20: "FTP data", 21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS", 67: "DHCP", 68: "DHCP", 69: "TFTP", 80: "HTTP",
	110: "POP3", 123: "NTP", 137: "NetBIOS", 138: "NetBIOS", 139: "NetBIOS", 143: "IMAP", 161: "SNMP", 162: "SNMP trap", 179: "BGP", 389: "LDAP", 443: "HTTPS",
	445: "SMB", 465: "SMTPS", 500: "IKE", 514: "Syslog", 587: "Submission", 636: "LDAPS", 853: "DNS over TLS", 993: "IMAPS", 995: "POP3S", 1194: "OpenVPN",
	1433: "SQL Server", 1701: "L2TP", 1723: "PPTP", 1812: "RADIUS", 2055: "NetFlow", 3306: "MySQL", 3389: "RDP", 4500: "IPsec NAT-T", 5060: "SIP",
	5432: "PostgreSQL", 5900: "VNC", 6343: "sFlow", 8080: "HTTP alt", 8291: "WinBox", 8443: "HTTPS alt", 8728: "RouterOS API", 51820: "WireGuard"}

func serviceName(proto, port int) string {
	p, ok := protoNames[proto]
	if !ok {
		p = "IP " + strconv.Itoa(proto)
	}
	if port == 0 {
		return p
	}
	s := p + " " + strconv.Itoa(port)
	if n, ok := portNames[port]; ok {
		s += " (" + n + ")"
	}
	return s
}

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := &flowsData{Range: r.FormValue("range"), Ranges: flowRanges}
	if !slices.Contains(flowRanges, d.Range) {
		d.Range = "1h"
	}
	d.DeviceID, _ = strconv.ParseInt(r.FormValue("device"), 10, 64)
	to := s.now()
	f := store.FlowFilter{DeviceID: d.DeviceID, From: to.Add(-rangeDuration(d.Range)), To: to.Add(time.Minute)}
	devs, err := s.store.Devices(ctx, store.DeviceFilter{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	byAddr, byID := map[netip.Addr]*store.Device{}, map[int64]*store.Device{}
	for _, x := range devs {
		byID[x.ID] = x
		if a, err := netip.ParseAddr(x.Address); err == nil {
			byAddr[a] = x
		}
	}
	ids, err := s.store.FlowExporterIDs(ctx, to.AddDate(0, 0, -s.FlowConfig(ctx).KeepDays))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, id := range ids {
		if x := byID[id]; x != nil {
			d.Exporters = append(d.Exporters, x)
		}
	}
	sort.Slice(d.Exporters, func(i, j int) bool {
		return strings.ToLower(d.Exporters[i].Name) < strings.ToLower(d.Exporters[j].Name)
	})
	c := s.FlowConfig(ctx)
	d.On = c.Enabled
	if d.Bytes, d.Packets, err = s.store.FlowTotal(ctx, f); err != nil {
		s.serverError(w, r, err)
		return
	}
	secs := rangeDuration(d.Range).Seconds()
	d.BPS = float64(d.Bytes) * 8 / secs
	name := func(a netip.Addr) (string, int64) {
		if x := byAddr[a]; x != nil {
			return x.Name, x.ID
		}
		return a.String(), 0
	}
	line := func(t store.FlowTop) flowLine {
		l := flowLine{Bytes: t.Bytes, Packets: t.Packets, BPS: float64(t.Bytes) * 8 / secs}
		if d.Bytes > 0 {
			l.Pct = float64(t.Bytes) / float64(d.Bytes) * 100
		}
		if t.Src.IsValid() {
			l.Src, l.SrcID = name(t.Src)
		}
		if t.Dst.IsValid() {
			l.Dst, l.DstID = name(t.Dst)
		}
		if t.Proto != 0 {
			l.Service = serviceName(t.Proto, t.Port)
		}
		if t.DeviceID > 0 {
			if x := byID[t.DeviceID]; x != nil {
				l.Exporter, l.ExporterID = x.Name, x.ID
			}
		}
		return l
	}
	for _, q := range []struct {
		by  string
		out *[]flowLine
	}{{store.FlowBySource, &d.Sources}, {store.FlowByDestination, &d.Dests}, {store.FlowByConversation, &d.Conversations}, {store.FlowByService, &d.Services}, {store.FlowByExporter, &d.By}} {
		if q.by == store.FlowByExporter && d.DeviceID > 0 {
			continue
		}
		limit := 10
		if q.by == store.FlowByConversation {
			limit = 25
		}
		ts, err := s.store.FlowTops(ctx, f, q.by, limit)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, t := range ts {
			*q.out = append(*q.out, line(t))
		}
	}
	s.render(w, r, http.StatusOK, "flows", &Page{Title: "Traffic flows", Nav: "flows", Data: d})
}

// handleFlowSeries gives the traffic over time as a graph series (bit/s).
func (s *Server) handleFlowSeries(w http.ResponseWriter, r *http.Request) {
	dev, err := strconv.ParseInt(r.PathValue("device"), 10, 64)
	if err != nil || dev < 0 {
		s.notFound(w, r)
		return
	}
	from, to, points := s.queryRange(r)
	step := max(time.Minute, (to.Sub(from) / time.Duration(points)).Truncate(time.Minute))
	rates, err := s.store.FlowRate(r.Context(), store.FlowFilter{DeviceID: dev, From: from, To: to}, step)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := int64(step / time.Second)
	out := seriesJSON{Step: st, From: from.Unix(), To: to.Unix(), Unit: "bit/s", Label: translate(s.lang(r, userFrom(r)), "Traffic"), T: []int64{}, Min: []*float64{}, Avg: []*float64{}, Max: []*float64{}}
	for t := from.Unix() / st * st; t < to.Unix(); t += st {
		var v *float64
		if b, ok := rates[t]; ok {
			v = num(float64(b) * 8 / float64(st))
		} else if t < s.now().Unix()-st {
			v = num(0)
		}
		out.T = append(out.T, t)
		out.Min, out.Avg, out.Max = append(out.Min, v), append(out.Avg, v), append(out.Max, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, queryAnswer{Series: []seriesJSON{out}})
}

type flowTopCtx struct {
	P     *Page
	Title string
	Kind  string
	Rows  []flowLine
}

type flowAddrCtx struct {
	Name string
	ID   int64
}
