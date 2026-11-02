package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// TestTopologyLinks: links come from neighbour tables, MAC address tables
// (also for a device known only from a router's ARP table), port
// descriptions and point-to-point networks of saved configurations; a map
// that draws by itself adds them and the devices they lead to, and what
// the user removes stays removed.
func TestTopologyLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	for _, d := range [][2]string{{"sw1", "10.0.0.1"}, {"sw2", "10.0.0.2"}, {"srv", "10.0.0.10"}, {"printer", "10.0.0.20"}, {"r1", "10.0.1.1"}, {"r2", "10.0.1.2"}, {"fw1", "10.0.0.30"}} {
		c.post("/devices/new", "name", d[0], "address", d[1])
	}
	const sw1, sw2, srv, printer, r1, r2, fw1 = 1, 2, 3, 4, 5, 6, 7
	ifs := func(dev int64, list ...store.Interface) {
		if err := e.st.SetInterfaces(ctx, dev, list); err != nil {
			t.Fatal(err)
		}
	}
	ifs(sw1, store.Interface{Index: 3, Name: "ether3"}, store.Interface{Index: 24, Name: "ether24", MAC: "02:00:00:00:01:18"})
	ifs(sw2, store.Interface{Index: 1, Name: "ether1", MAC: "02:00:00:00:02:01"}, store.Interface{Index: 5, Name: "ether5"}, store.Interface{Index: 7, Name: "ether7"})
	ifs(srv, store.Interface{Index: 2, Name: "eth0", MAC: "AA-BB-CC-00-00-10"})
	ifs(r1, store.Interface{Index: 1, Name: "ether1"})
	ifs(r2, store.Interface{Index: 4, Name: "GigabitEthernet0/1"})
	ifs(fw1, store.Interface{Index: 1, Name: "port1", Alias: "uplink to sw2 ether7"})
	now := time.Now()
	set := func(dev int64, nb []store.TopoNeighbour, fdb []store.TopoFDB, arp []store.TopoARP) {
		if err := e.st.SetTopology(ctx, dev, now, nb, fdb, arp); err != nil {
			t.Fatal(err)
		}
	}
	set(sw1, []store.TopoNeighbour{{IfIndex: 24, Name: "sw2", RemotePort: "ether1", Via: "LLDP"}},
		[]store.TopoFDB{{IfIndex: 3, MAC: "aa:bb:cc:00:00:10"}, {IfIndex: 24, MAC: "aa:bb:cc:00:00:10"}}, nil)
	var many []store.TopoFDB
	for i := 0; i < 10; i++ {
		many = append(many, store.TopoFDB{IfIndex: 1, MAC: "02:00:00:00:99:0" + string(rune('0'+i))})
	}
	many = append(many, store.TopoFDB{IfIndex: 1, MAC: "aa:bb:cc:00:00:10"}, store.TopoFDB{IfIndex: 5, MAC: "11:22:33:44:55:66"}, store.TopoFDB{IfIndex: 5, MAC: "11:22:33:44:55:67"})
	set(sw2, []store.TopoNeighbour{}, many, nil)
	set(r1, nil, nil, []store.TopoARP{{Address: netip.MustParseAddr("10.0.0.20"), MAC: "11:22:33:44:55:66"}})
	for _, b := range []struct {
		dev          int64
		method, conf string
	}{{r1, "mikrotik", "/ip address add address=10.9.9.1/30 interface=ether1 network=10.9.9.0\n"},
		{r2, "cisco", "interface GigabitEthernet0/1\n ip address 10.9.9.2 255.255.255.252\n"}} {
		if err := e.st.SaveDeviceBackup(ctx, store.DeviceBackup{DeviceID: b.dev, Enabled: true, Method: b.method, Port: 22, User: "backup", Secret: "pw"}, false, false); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.AddConfigVersion(ctx, store.ConfigVersion{DeviceID: b.dev, At: now, Hash: "h", Content: b.conf}, 10); err != nil {
			t.Fatal(err)
		}
	}

	c.post("/maps", "name", "LAN")
	l := store.MapLayout{Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: sw1, X: 0}, {ID: -2, Kind: "device", DeviceID: sw2, X: 400}, {ID: -3, Kind: "device", DeviceID: srv, X: 0, Y: 300},
		{ID: -4, Kind: "device", DeviceID: r1, X: 1000}, {ID: -5, Kind: "device", DeviceID: r2, X: 1400}}}
	if r := c.postJSON("/maps/1/layout", l); r.code != http.StatusOK {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	var ans struct {
		Links []struct {
			A, B  int64
			AIf   int `json:"a_if"`
			BIf   int `json:"b_if"`
			Via   string
			AName string `json:"a_name"`
			BName string `json:"b_name"`
		}
		Devices []struct {
			ID, Near int64
			Via      string
		}
	}
	if err := json.Unmarshal([]byte(c.get("/maps/1/suggest").body), &ans); err != nil {
		t.Fatal(err)
	}
	via := map[string]string{}
	for _, k := range ans.Links {
		via[itoa(int32(k.A))+"-"+itoa(int32(k.B))] = k.Via
	}
	if via["1-2"] != "LLDP" || via["1-3"] != "MAC table" || !strings.HasPrefix(via["5-6"], "Configuration 10.9.9.0/30") || len(ans.Links) != 3 {
		t.Fatalf("links: %+v", ans.Links)
	}
	for _, k := range ans.Links {
		if k.A == sw1 && k.B == srv && (k.AIf != 3 || k.BIf != 2) {
			t.Fatalf("server port: %+v", k)
		}
	}
	got := map[int64]string{}
	for _, d := range ans.Devices {
		got[d.ID] = d.Via
	}
	if got[printer] != "MAC table" || got[fw1] != "Port description" || len(ans.Devices) != 2 {
		t.Fatalf("devices: %+v", ans.Devices)
	}

	// drawing by itself
	if r := c.postJSON("/maps/1/auto", map[string]bool{"auto_links": true, "auto_devices": true}); r.code != http.StatusOK || !strings.Contains(r.body, `"added":7`) {
		t.Fatalf("auto: %d %s", r.code, r.body)
	}
	var lay struct {
		Layout store.MapLayout `json:"layout"`
	}
	_ = json.Unmarshal([]byte(c.get("/maps/1/layout").body), &lay)
	if len(lay.Layout.Nodes) != 7 || len(lay.Layout.Links) != 5 {
		t.Fatalf("auto layout: %d nodes, %d links", len(lay.Layout.Nodes), len(lay.Layout.Links))
	}
	// the user removes the server link and the firewall
	var keep store.MapLayout
	keep.Nodes = lay.Layout.Nodes[:0:0]
	var fwNode int64
	for _, n := range lay.Layout.Nodes {
		if n.DeviceID == fw1 {
			fwNode = n.ID
			continue
		}
		keep.Nodes = append(keep.Nodes, n)
		keep.SeenNode = max(keep.SeenNode, n.ID)
	}
	keep.SeenNode = max(keep.SeenNode, fwNode)
	for _, k := range lay.Layout.Links {
		keep.SeenLink = max(keep.SeenLink, k.ID)
		var a, b int64
		for _, n := range lay.Layout.Nodes {
			if n.ID == k.A {
				a = n.DeviceID
			}
			if n.ID == k.B {
				b = n.DeviceID
			}
		}
		if (a == sw1 && b == srv) || a == fw1 || b == fw1 {
			continue
		}
		keep.Links = append(keep.Links, k)
	}
	if r := c.postJSON("/maps/1/layout", keep); r.code != http.StatusOK {
		t.Fatalf("save by hand: %d %s", r.code, r.body)
	}
	e.srv.AutoMapsOnce(ctx)
	_ = json.Unmarshal([]byte(c.get("/maps/1/layout").body), &lay)
	if len(lay.Layout.Nodes) != 6 || len(lay.Layout.Links) != 3 {
		t.Fatalf("removed items came back: %d nodes, %d links", len(lay.Layout.Nodes), len(lay.Layout.Links))
	}
	// an editor that does not say what it loaded removes nothing for good
	keep.SeenLink, keep.SeenNode = 0, 0
	keep.Links = keep.Links[:1]
	c.postJSON("/maps/1/layout", keep)
	e.srv.AutoMapsOnce(ctx)
	_ = json.Unmarshal([]byte(c.get("/maps/1/layout").body), &lay)
	if len(lay.Layout.Links) != 3 {
		t.Fatalf("links after an old editor saved: %d", len(lay.Layout.Links))
	}
}
