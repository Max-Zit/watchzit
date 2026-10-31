package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/hypervisor"
	"github.com/max-zit/watchzit/internal/store"
)

// TestHypervisorInventory: a hypervisor check is set up from the device
// page, its inventory shows there, and a map host box with automatic
// machines gets them in its live data (matched to devices by address).
func TestHypervisorInventory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "vcenter", "address", "10.0.0.2")
	c.post("/devices/new", "name", "esx01", "address", "10.0.0.5")
	c.post("/devices/new", "name", "dc01", "address", "10.0.0.6")

	if r := c.post("/devices/1/checks", "type", "hypervisor", "hv", "vsphere", "user", "monitor@vsphere.local"); r.code != http.StatusBadRequest {
		t.Fatalf("no password accepted? %d", r.code)
	} else if r := c.post("/devices/1/checks", "type", "hypervisor", "hv", "xen", "user", "u", "secret", "p"); r.code != http.StatusBadRequest || !strings.Contains(r.body, "platform") {
		t.Fatalf("unknown platform: %d", r.code)
	}
	if r := c.post("/devices/1/checks", "type", "hypervisor", "hv", "vsphere", "user", "monitor@vsphere.local", "secret", "pw", "insecure", "1"); r.code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", r.code, r.body)
	}
	cs, _ := e.st.DeviceChecks(ctx, 1)
	if len(cs) != 1 || cs[0].Type != "hypervisor" || cs[0].Params.Port != 443 || cs[0].Name != "VMware vSphere" || !strings.HasPrefix(cs[0].Params.Secret, "v1:") {
		t.Fatalf("check: %+v", cs)
	}

	inv := &hypervisor.Inventory{Kind: "vsphere", Product: "VMware vCenter Server 8.0.3",
		Hosts: []hypervisor.Host{{Name: "esx01.lab", Address: "10.0.0.5", Up: true, CPUs: 8, CPUPct: 12}, {Name: "esx02.lab", Address: "10.0.0.7", Up: true}},
		VMs: []hypervisor.VM{{ID: "vm-1", Name: "DC01", Host: "esx01.lab", State: "running", OS: "windows2019srv_64Guest", IPs: []string{"10.0.0.6"}},
			{ID: "vm-2", Name: "web", Host: "esx01.lab", State: "stopped", OS: "ubuntu64Guest"},
			{ID: "vm-3", Name: "tpl", Host: "esx01.lab", State: "stopped", Template: true},
			{ID: "vm-4", Name: "other", Host: "esx02.lab", State: "running"}},
		Datastores: []hypervisor.Datastore{{Name: "datastore1", CapB: 100 << 30, FreeB: 5 << 30, Active: true}}}
	if err := e.st.SetHVInventory(ctx, 1, time.Now(), inv); err != nil {
		t.Fatal(err)
	}
	r := c.get("/devices/1")
	for _, want := range []string{`id="vms"`, "VMware vCenter Server 8.0.3", "esx02.lab", "DC01", "10.0.0.6", "datastore1", "95 %"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("device page lacks %q", want)
		}
	}

	c.post("/maps", "name", "Virtual")
	l := store.MapLayout{Nodes: []store.MapNode{{ID: -1, Kind: "host", Icon: "lib:hv-esxi", HostDevice: 2, AutoVMs: true}}}
	if r := c.postJSON("/maps/1/layout", l); r.code != http.StatusOK {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	var lay struct {
		Layout store.MapLayout `json:"layout"`
	}
	_ = json.Unmarshal([]byte(c.get("/maps/1/layout").body), &lay)
	if len(lay.Layout.Nodes) != 1 || !lay.Layout.Nodes[0].AutoVMs {
		t.Fatalf("auto_vms not kept: %+v", lay.Layout.Nodes)
	}
	var live store.MapLive
	_ = json.Unmarshal([]byte(c.get("/maps/1/live").body), &live)
	vms := live.VMs[lay.Layout.Nodes[0].ID]
	if len(vms) != 2 || vms[0].Name != "DC01" || vms[0].Device != 3 || vms[0].Pic != "vm-dc" || vms[1].Name != "web" || vms[1].State != "stopped" {
		t.Fatalf("live machines: %+v", live.VMs)
	}

	// an agent's Docker containers are an inventory of their own
	inv2 := &hypervisor.Inventory{Kind: "docker", Product: "Docker 27.3.1", Hosts: []hypervisor.Host{{Name: "esx01", Up: true}},
		VMs: []hypervisor.VM{{ID: "abc", Name: "nginx", Host: "esx01", State: "running", Kind: "ct"}}}
	_ = e.st.SetHVInventory(ctx, 2, time.Now(), inv2)
	live = store.MapLive{}
	_ = json.Unmarshal([]byte(c.get("/maps/1/live").body), &live)
	if vms := live.VMs[lay.Layout.Nodes[0].ID]; len(vms) != 3 || vms[2].Pic != "ct-docker" {
		t.Fatalf("containers: %+v", vms)
	}
	if r := c.get("/devices/2"); !strings.Contains(r.body, "Containers") || !strings.Contains(r.body, "nginx") {
		t.Fatal("containers not on the device page")
	}

	// removing the check forgets the inventory
	c.post("/checks/" + strconv.FormatInt(cs[0].ID, 10) + "/delete")
	if i, _ := e.st.HVInventoryOf(ctx, 1); i != nil {
		t.Fatal("inventory kept after the check was removed")
	}
}
