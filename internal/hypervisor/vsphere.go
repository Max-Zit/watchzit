package hypervisor

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// VMware vSphere over the web services (SOAP) API, which standalone ESXi
// hosts and vCenter both have: a container view of all hosts, machines and
// datastores, read with one property collector call (and its
// continuations).

type moref struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

func (m moref) xml(tag string) string {
	return "<" + tag + ` type="` + esc(m.Type) + `">` + esc(m.Value) + "</" + tag + ">"
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

type soap struct {
	hc  *http.Client
	url string
}

const soapAction = "urn:vim25/6.7"

func (s *soap) call(ctx context.Context, body string, out any) error {
	env := `<?xml version="1.0" encoding="UTF-8"?><soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:urn="urn:vim25"><soapenv:Body>` +
		body + `</soapenv:Body></soapenv:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(env))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", soapAction)
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	var e struct {
		Body struct {
			Inner []byte `xml:",innerxml"`
			Fault *struct {
				String string `xml:"faultstring"`
			} `xml:"Fault"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(data, &e); err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the server answered %s; is this a vSphere host or vCenter?", resp.Status)
		}
		return errors.New("the answer is not a vSphere answer")
	}
	if e.Body.Fault != nil {
		return errors.New(strings.TrimSpace(e.Body.Fault.String))
	}
	if out == nil {
		return nil
	}
	return xml.Unmarshal(e.Body.Inner, out)
}

type serviceContent struct {
	Returnval struct {
		RootFolder        moref `xml:"rootFolder"`
		PropertyCollector moref `xml:"propertyCollector"`
		ViewManager       moref `xml:"viewManager"`
		SessionManager    moref `xml:"sessionManager"`
		About             struct {
			FullName string `xml:"fullName"`
			APIType  string `xml:"apiType"`
		} `xml:"about"`
	} `xml:"returnval"`
}

type propResult struct {
	Returnval struct {
		Token   string `xml:"token"`
		Objects []struct {
			Obj     moref `xml:"obj"`
			PropSet []struct {
				Name string `xml:"name"`
				Val  string `xml:"val"`
			} `xml:"propSet"`
		} `xml:"objects"`
	} `xml:"returnval"`
}

var vsphereProps = map[string][]string{
	"HostSystem": {"name", "runtime.connectionState", "runtime.powerState", "summary.hardware.numCpuCores", "summary.hardware.cpuMhz",
		"summary.hardware.memorySize", "summary.quickStats.overallCpuUsage", "summary.quickStats.overallMemoryUsage", "summary.config.product.fullName"},
	"VirtualMachine": {"name", "runtime.powerState", "runtime.host", "config.guestId", "config.template", "config.hardware.numCPU",
		"config.hardware.memoryMB", "summary.quickStats.overallCpuUsage", "summary.quickStats.guestMemoryUsage", "guest.ipAddress"},
	"Datastore": {"name", "summary.capacity", "summary.freeSpace", "summary.type", "summary.accessible"},
}

func readVSphere(ctx context.Context, c Conn) (*Inventory, error) {
	s := &soap{hc: client(c.Insecure), url: baseURL(c, 443) + "/sdk"}
	defer s.hc.CloseIdleConnections()
	var sc serviceContent
	if err := s.call(ctx, `<urn:RetrieveServiceContent><urn:_this type="ServiceInstance">ServiceInstance</urn:_this></urn:RetrieveServiceContent>`, &sc); err != nil {
		return nil, err
	}
	rv := sc.Returnval
	if rv.PropertyCollector.Value == "" || rv.ViewManager.Value == "" {
		return nil, errors.New("the answer is not a vSphere answer")
	}
	if err := s.call(ctx, `<urn:Login>`+rv.SessionManager.xml("urn:_this")+`<urn:userName>`+esc(c.User)+`</urn:userName><urn:password>`+esc(c.Secret)+`</urn:password></urn:Login>`, nil); err != nil {
		return nil, err
	}
	defer func() {
		_ = s.call(context.WithoutCancel(ctx), `<urn:Logout>`+rv.SessionManager.xml("urn:_this")+`</urn:Logout>`, nil)
	}()
	var view struct {
		Returnval moref `xml:"returnval"`
	}
	if err := s.call(ctx, `<urn:CreateContainerView>`+rv.ViewManager.xml("urn:_this")+rv.RootFolder.xml("urn:container")+
		`<urn:type>HostSystem</urn:type><urn:type>VirtualMachine</urn:type><urn:type>Datastore</urn:type><urn:recursive>true</urn:recursive></urn:CreateContainerView>`, &view); err != nil {
		return nil, err
	}
	defer func() {
		_ = s.call(context.WithoutCancel(ctx), `<urn:DestroyView>`+view.Returnval.xml("urn:_this")+`</urn:DestroyView>`, nil)
	}()
	var spec strings.Builder
	spec.WriteString(`<urn:specSet>`)
	for _, typ := range []string{"HostSystem", "VirtualMachine", "Datastore"} {
		spec.WriteString(`<urn:propSet><urn:type>` + typ + `</urn:type>`)
		for _, p := range vsphereProps[typ] {
			spec.WriteString(`<urn:pathSet>` + p + `</urn:pathSet>`)
		}
		spec.WriteString(`</urn:propSet>`)
	}
	spec.WriteString(`<urn:objectSet>` + view.Returnval.xml("urn:obj") + `<urn:skip>true</urn:skip><urn:selectSet xsi:type="urn:TraversalSpec"><urn:name>v</urn:name><urn:type>ContainerView</urn:type><urn:path>view</urn:path><urn:skip>false</urn:skip></urn:selectSet></urn:objectSet></urn:specSet>`)
	objs := map[string]map[string]map[string]string{} // type → id → property → value
	add := func(r *propResult) {
		for _, o := range r.Returnval.Objects {
			if objs[o.Obj.Type] == nil {
				objs[o.Obj.Type] = map[string]map[string]string{}
			}
			ps := map[string]string{}
			for _, p := range o.PropSet {
				ps[p.Name] = strings.TrimSpace(p.Val)
			}
			objs[o.Obj.Type][o.Obj.Value] = ps
		}
	}
	var r propResult
	if err := s.call(ctx, `<urn:RetrievePropertiesEx>`+rv.PropertyCollector.xml("urn:_this")+spec.String()+`<urn:options><urn:maxObjects>500</urn:maxObjects></urn:options></urn:RetrievePropertiesEx>`, &r); err != nil {
		return nil, err
	}
	add(&r)
	for n := 0; r.Returnval.Token != "" && n < 100; n++ {
		tok := r.Returnval.Token
		r = propResult{}
		if err := s.call(ctx, `<urn:ContinueRetrievePropertiesEx>`+rv.PropertyCollector.xml("urn:_this")+`<urn:token>`+esc(tok)+`</urn:token></urn:ContinueRetrievePropertiesEx>`, &r); err != nil {
			return nil, err
		}
		add(&r)
	}
	return vsphereInventory(rv.About.FullName, objs), nil
}

func atoi(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func vsphereInventory(product string, objs map[string]map[string]map[string]string) *Inventory {
	inv := &Inventory{Kind: VSphere, Product: product, Hosts: []Host{}, VMs: []VM{}}
	hostName := map[string]string{}
	hostMHz := map[string]int64{}
	for id, p := range objs["HostSystem"] {
		h := Host{Name: p["name"], Address: p["name"], Version: p["summary.config.product.fullName"],
			Up:   p["runtime.connectionState"] == "connected" && p["runtime.powerState"] != "poweredOff" && p["runtime.powerState"] != "standBy",
			CPUs: int(atoi(p["summary.hardware.numCpuCores"])), MemB: atoi(p["summary.hardware.memorySize"]),
			MemUsedB: atoi(p["summary.quickStats.overallMemoryUsage"]) << 20}
		mhz := atoi(p["summary.hardware.cpuMhz"])
		if total := mhz * int64(h.CPUs); total > 0 {
			h.CPUPct = round2(float64(atoi(p["summary.quickStats.overallCpuUsage"])) / float64(total) * 100)
		}
		hostName[id], hostMHz[id] = h.Name, mhz
		inv.Hosts = append(inv.Hosts, h)
	}
	for id, p := range objs["VirtualMachine"] {
		v := VM{ID: id, Name: p["name"], Host: hostName[p["runtime.host"]], OS: p["config.guestId"], Template: p["config.template"] == "true",
			CPUs: int(atoi(p["config.hardware.numCPU"])), MemB: atoi(p["config.hardware.memoryMB"]) << 20}
		switch p["runtime.powerState"] {
		case "poweredOn":
			v.State = "running"
		case "suspended":
			v.State = "suspended"
		default:
			v.State = "stopped"
		}
		if v.State == "running" {
			if total := hostMHz[p["runtime.host"]] * int64(max(v.CPUs, 1)); total > 0 {
				v.CPUPct = round2(min(float64(atoi(p["summary.quickStats.overallCpuUsage"]))/float64(total)*100, 100))
			}
			if v.MemB > 0 {
				v.MemPct = round2(min(float64(atoi(p["summary.quickStats.guestMemoryUsage"])<<20)/float64(v.MemB)*100, 100))
			}
			if ip := p["guest.ipAddress"]; ip != "" {
				v.IPs = []string{ip}
			}
		}
		inv.VMs = append(inv.VMs, v)
	}
	for _, p := range objs["Datastore"] {
		inv.Datastores = append(inv.Datastores, Datastore{Name: p["name"], Type: p["summary.type"], CapB: atoi(p["summary.capacity"]),
			FreeB: atoi(p["summary.freeSpace"]), Active: p["summary.accessible"] == "true"})
	}
	return inv
}
