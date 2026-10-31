package hypervisor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func addrPort(t *testing.T, srv *httptest.Server) (string, int) {
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return u.Hostname(), port
}

func prop(name, val string) string {
	return `<propSet><name>` + name + `</name><val xsi:type="xsd:string">` + val + `</val></propSet>`
}

// fakeVSphere answers the calls readVSphere makes, the objects in two
// pages (a continuation token).
func fakeVSphere(t *testing.T) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		ret := ""
		switch {
		case strings.Contains(body, "RetrieveServiceContent"):
			ret = `<RetrieveServiceContentResponse xmlns="urn:vim25"><returnval><rootFolder type="Folder">group-d1</rootFolder><propertyCollector type="PropertyCollector">propertyCollector</propertyCollector><viewManager type="ViewManager">ViewManager</viewManager><about><fullName>VMware vCenter Server 8.0.3</fullName><apiType>VirtualCenter</apiType></about><sessionManager type="SessionManager">SessionManager</sessionManager></returnval></RetrieveServiceContentResponse>`
		case strings.Contains(body, "<urn:Login>"):
			if !strings.Contains(body, "<urn:userName>monitor@vsphere.local</urn:userName><urn:password>s3cr&amp;t</urn:password>") {
				ret = `<soapenv:Fault><faultcode>ServerFaultCode</faultcode><faultstring>Cannot complete login due to an incorrect user name or password.</faultstring></soapenv:Fault>`
				break
			}
			http.SetCookie(w, &http.Cookie{Name: "vmware_soap_session", Value: "abc"})
			ret = `<LoginResponse xmlns="urn:vim25"><returnval><key>k</key></returnval></LoginResponse>`
		case strings.Contains(body, "CreateContainerView"):
			if c, err := r.Cookie("vmware_soap_session"); err != nil || c.Value != "abc" {
				t.Error("no session cookie")
			}
			ret = `<CreateContainerViewResponse xmlns="urn:vim25"><returnval type="ContainerView">session[1]view</returnval></CreateContainerViewResponse>`
		case strings.Contains(body, "<urn:RetrievePropertiesEx>"):
			if !strings.Contains(body, "<urn:pathSet>guest.ipAddress</urn:pathSet>") || !strings.Contains(body, `<urn:obj type="ContainerView">session[1]view</urn:obj>`) {
				t.Error("property spec")
			}
			ret = `<RetrievePropertiesExResponse xmlns="urn:vim25"><returnval><token>next</token>` +
				`<objects><obj type="HostSystem">host-1</obj>` + prop("name", "esx01.lab") + prop("runtime.connectionState", "connected") + prop("runtime.powerState", "poweredOn") +
				prop("summary.hardware.numCpuCores", "8") + prop("summary.hardware.cpuMhz", "2000") + prop("summary.hardware.memorySize", "68719476736") +
				prop("summary.quickStats.overallCpuUsage", "4000") + prop("summary.quickStats.overallMemoryUsage", "16384") + prop("summary.config.product.fullName", "VMware ESXi 8.0.3") + `</objects>` +
				`<objects><obj type="HostSystem">host-2</obj>` + prop("name", "esx02.lab") + prop("runtime.connectionState", "notResponding") + `</objects>` +
				`<objects><obj type="VirtualMachine">vm-10</obj>` + prop("name", "dc01") + prop("runtime.powerState", "poweredOn") + `<propSet><name>runtime.host</name><val type="HostSystem" xsi:type="ManagedObjectReference">host-1</val></propSet>` +
				prop("config.guestId", "windows2019srv_64Guest") + prop("config.template", "false") + prop("config.hardware.numCPU", "2") + prop("config.hardware.memoryMB", "4096") +
				prop("summary.quickStats.overallCpuUsage", "400") + prop("summary.quickStats.guestMemoryUsage", "1024") + prop("guest.ipAddress", "10.1.1.10") + `</objects>` +
				`</returnval></RetrievePropertiesExResponse>`
		case strings.Contains(body, "ContinueRetrievePropertiesEx"):
			ret = `<ContinueRetrievePropertiesExResponse xmlns="urn:vim25"><returnval>` +
				`<objects><obj type="VirtualMachine">vm-11</obj>` + prop("name", "tpl-ubuntu") + prop("runtime.powerState", "poweredOff") + `<propSet><name>runtime.host</name><val type="HostSystem" xsi:type="ManagedObjectReference">host-1</val></propSet>` + prop("config.guestId", "ubuntu64Guest") + prop("config.template", "true") + `</objects>` +
				`<objects><obj type="Datastore">ds-1</obj>` + prop("name", "vsan") + prop("summary.capacity", "1000") + prop("summary.freeSpace", "50") + prop("summary.type", "vsan") + prop("summary.accessible", "true") + `</objects>` +
				`<objects><obj type="Datastore">ds-2</obj>` + prop("name", "old-nfs") + prop("summary.capacity", "1000") + prop("summary.freeSpace", "0") + prop("summary.accessible", "false") + `</objects>` +
				`</returnval></ContinueRetrievePropertiesExResponse>`
		case strings.Contains(body, "Logout"), strings.Contains(body, "DestroyView"):
			ret = `<Done xmlns="urn:vim25"/>`
		default:
			t.Errorf("unexpected call %s", body)
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0"?><soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><soapenv:Body>`+ret+`</soapenv:Body></soapenv:Envelope>`)
	}))
}

func TestVSphere(t *testing.T) {
	srv := fakeVSphere(t)
	defer srv.Close()
	host, port := addrPort(t, srv)
	c := Conn{Platform: VSphere, Address: host, Port: port, User: "monitor@vsphere.local", Secret: "s3cr&t", Insecure: true}
	inv, err := Read(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Product != "VMware vCenter Server 8.0.3" || len(inv.Hosts) != 2 || len(inv.VMs) != 2 || len(inv.Datastores) != 2 {
		t.Fatalf("inventory: %+v", inv)
	}
	h := inv.Hosts[0]
	if h.Name != "esx01.lab" || !h.Up || h.CPUPct != 25 || h.MemUsedB != 16<<30 || inv.Hosts[1].Up {
		t.Fatalf("hosts: %+v", inv.Hosts)
	}
	dc := inv.VMs[0]
	if dc.Name != "dc01" || dc.Host != "esx01.lab" || dc.State != "running" || dc.CPUPct != 10 || dc.MemPct != 25 || dc.IPs[0] != "10.1.1.10" || Picture(dc) != "vm-dc" {
		t.Fatalf("vm: %+v %s", dc, Picture(dc))
	}
	if !inv.VMs[1].Template || inv.VMs[1].State != "stopped" {
		t.Fatalf("template: %+v", inv.VMs[1])
	}
	m := inv.Metrics()
	if m["cpu_pct"] != 25 || m["vms_running"] != 1 || m["vms_total"] != 1 || m["hosts_down"] != 1 || m["ds_used_pct"] != 95 || m["mem_pct"] != 25 {
		t.Fatalf("metrics: %v", m)
	}
	c.Secret = "wrong"
	if _, err := Read(context.Background(), c); err == nil || !strings.Contains(err.Error(), "incorrect user name") {
		t.Fatalf("bad password: %v", err)
	}
	// a certificate that is not trusted is refused unless accepted
	c.Secret, c.Insecure = "s3cr&t", false
	if _, err := Read(context.Background(), c); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestProxmox(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Path == "/api2/json/access/ticket" {
			_ = r.ParseForm()
			if r.Form.Get("username") != "monitor@pve" || r.Form.Get("password") != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"ticket":"PVE:monitor@pve:T"}}`)
			return
		}
		auth := r.Header.Get("Authorization")
		if c, err := r.Cookie("PVEAuthCookie"); (err != nil || c.Value != "PVE:monitor@pve:T") && auth != "PVEAPIToken=monitor@pve!wz=tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api2/json/cluster/resources":
			_, _ = io.WriteString(w, `{"data":[
				{"type":"node","node":"pve1","status":"online","cpu":0.5,"maxcpu":16,"mem":8589934592,"maxmem":34359738368},
				{"type":"node","node":"pve2","status":"offline"},
				{"type":"qemu","vmid":100,"name":"web01","node":"pve1","status":"running","cpu":0.2,"maxcpu":2,"mem":1073741824,"maxmem":4294967296,"template":0},
				{"type":"lxc","vmid":200,"name":"dns","node":"pve1","status":"stopped","maxmem":536870912},
				{"type":"qemu","vmid":900,"name":"tpl","node":"pve1","status":"stopped","template":1},
				{"type":"storage","storage":"local-lvm","node":"pve1","status":"available","disk":800,"maxdisk":1000,"shared":0,"plugintype":"lvmthin"},
				{"type":"storage","storage":"ceph","node":"pve1","status":"available","disk":100,"maxdisk":1000,"shared":1,"plugintype":"rbd"},
				{"type":"storage","storage":"ceph","node":"pve2","status":"available","disk":100,"maxdisk":1000,"shared":1,"plugintype":"rbd"}]}`)
		case "/api2/json/cluster/status":
			_, _ = io.WriteString(w, `{"data":[{"type":"cluster","name":"lab"},{"type":"node","name":"pve1","ip":"10.0.0.11"},{"type":"node","name":"pve2","ip":"10.0.0.12"}]}`)
		case "/api2/json/version":
			_, _ = io.WriteString(w, `{"data":{"version":"9.0.3"}}`)
		case "/api2/json/nodes/pve1/qemu/100/config":
			_, _ = io.WriteString(w, `{"data":{"ostype":"l26"}}`)
		case "/api2/json/nodes/pve1/qemu/100/agent/network-get-interfaces":
			_, _ = io.WriteString(w, `{"data":{"result":[{"name":"lo","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"127.0.0.1"}]},{"name":"eth0","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.50"},{"ip-address-type":"ipv6","ip-address":"fe80::1"}]}]}}`)
		case "/api2/json/nodes/pve1/lxc/200/config":
			_, _ = io.WriteString(w, `{"data":{"ostype":"alpine"}}`)
		case "/api2/json/nodes/pve1/qemu/900/config":
			_, _ = io.WriteString(w, `{"data":{"ostype":"win11"}}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	host, port := addrPort(t, srv)
	c := Conn{Platform: Proxmox, Address: host, Port: port, User: "monitor@pve", Secret: "pw", Insecure: true, Details: true}
	inv, err := Read(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Product != "Proxmox VE 9.0.3" || len(inv.Hosts) != 2 || inv.Hosts[0].Address != "10.0.0.11" || inv.Hosts[0].CPUPct != 50 || inv.Hosts[1].Up {
		t.Fatalf("hosts: %+v", inv)
	}
	if len(inv.Datastores) != 2 || inv.Datastores[0].Name != "ceph" || inv.Datastores[1].Name != "local-lvm (pve1)" {
		t.Fatalf("storage: %+v", inv.Datastores)
	}
	dns, tpl, web := inv.VMs[0], inv.VMs[1], inv.VMs[2]
	if web.Name != "web01" || web.OS != "l26" || web.CPUPct != 20 || web.MemPct != 25 || len(web.IPs) != 1 || web.IPs[0] != "10.0.0.50" {
		t.Fatalf("web01: %+v", web)
	}
	if dns.Kind != "ct" || dns.State != "stopped" || Picture(dns) != "ct-lxc" || !tpl.Template {
		t.Fatalf("dns/tpl: %+v %+v", dns, tpl)
	}
	m := inv.Metrics()
	if m["vms_running"] != 1 || m["vms_total"] != 2 || m["hosts_down"] != 1 || m["ds_used_pct"] != 80 || m["cpu_pct"] != 50 || m["mem_pct"] != 25 {
		t.Fatalf("metrics: %v", m)
	}
	// without details the guest data of the last read is kept
	c.Details, c.User, c.Secret = false, "monitor@pve!wz", "tok"
	again, err := Read(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if again.VMs[2].OS != "" {
		t.Fatal("details read without Details")
	}
	again.Merge(inv)
	if again.VMs[2].OS != "l26" || again.VMs[2].IPs[0] != "10.0.0.50" {
		t.Fatalf("merge: %+v", again.VMs[2])
	}
	if calls["/api2/json/access/ticket"] != 1 {
		t.Fatalf("a token signed in: %v", calls)
	}
	c.Secret = "bad"
	if _, err := Read(context.Background(), c); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("bad token: %v", err)
	}
}

func TestPicture(t *testing.T) {
	for _, c := range []struct {
		in   VM
		want string
	}{
		{VM{OS: "win11"}, "vm-windows"},
		{VM{OS: "win11", Name: "srv-files"}, "vm-windows-server"},
		{VM{OS: "debian12_64Guest"}, "vm-debian"},
		{VM{OS: "l26", Name: "chr-edge"}, "vm-mikrotik-chr"},
		{VM{OS: "other3xLinux64Guest", Name: "vcenter"}, "vm-vcsa"},
		{VM{Kind: "ct", OS: "ubuntu"}, "ct-lxc"},
		{VM{OS: "l26"}, "vm-linux"},
	} {
		if got := Picture(c.in); got != c.want {
			t.Errorf("%+v: %s, want %s", c.in, got, c.want)
		}
	}
}
