// Package hypervisor reads hosts, virtual machines and datastores from
// VMware vSphere (ESXi or vCenter, over the vSphere web services API) and
// Proxmox VE (over its REST API). Hyper-V hosts are read by the agent.
package hypervisor

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Platforms.
const (
	VSphere = "vsphere"
	Proxmox = "proxmox"
	HyperV  = "hyperv"
	Docker  = "docker"
)

// Inventory is what a hypervisor (or a cluster, or vCenter) runs.
type Inventory struct {
	Kind       string      `json:"kind"`
	Product    string      `json:"product,omitempty"`
	Hosts      []Host      `json:"hosts"`
	VMs        []VM        `json:"vms"`
	Datastores []Datastore `json:"datastores,omitempty"`
}

// Host is a hypervisor host.
type Host struct {
	Name     string  `json:"name"`
	Address  string  `json:"address,omitempty"`
	Up       bool    `json:"up"`
	CPUs     int     `json:"cpus,omitempty"`
	CPUPct   float64 `json:"cpu_pct"`
	MemB     int64   `json:"mem_b,omitempty"`
	MemUsedB int64   `json:"mem_used_b,omitempty"`
	Version  string  `json:"version,omitempty"`
}

// VM is a virtual machine or container. State is running, stopped or
// suspended.
type VM struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"` // the Name of its host
	State    string   `json:"state"`
	Kind     string   `json:"kind,omitempty"` // "" virtual machine, "ct" container
	OS       string   `json:"os,omitempty"`   // the platform's guest type (l26, windows2019srv_64Guest, …)
	CPUs     int      `json:"cpus,omitempty"`
	MemB     int64    `json:"mem_b,omitempty"`
	CPUPct   float64  `json:"cpu_pct"`
	MemPct   float64  `json:"mem_pct"`
	IPs      []string `json:"ips,omitempty"`
	Template bool     `json:"template,omitempty"`
}

// Datastore is storage of virtual machines.
type Datastore struct {
	Name   string `json:"name"`
	Host   string `json:"host,omitempty"` // "" when shared
	Type   string `json:"type,omitempty"`
	CapB   int64  `json:"cap_b"`
	FreeB  int64  `json:"free_b"`
	Active bool   `json:"active"`
}

// UsedPct is how full the datastore is.
func (d Datastore) UsedPct() float64 {
	if d.CapB <= 0 {
		return 0
	}
	return round2(float64(d.CapB-d.FreeB) / float64(d.CapB) * 100)
}

// Conn is how a hypervisor is reached.
type Conn struct {
	Platform string
	Address  string // host name or IP
	Port     int
	User     string
	Secret   string
	Insecure bool // accept a certificate that is not trusted
	// Details also reads guest systems and addresses (more requests).
	Details bool
}

// Read reads the inventory.
func Read(ctx context.Context, c Conn) (*Inventory, error) {
	var inv *Inventory
	var err error
	switch c.Platform {
	case VSphere:
		inv, err = readVSphere(ctx, c)
	case Proxmox:
		inv, err = readProxmox(ctx, c)
	default:
		return nil, errors.New("unknown hypervisor platform")
	}
	if err != nil {
		return nil, err
	}
	inv.sort()
	return inv, nil
}

func (inv *Inventory) sort() {
	sort.Slice(inv.Hosts, func(i, j int) bool { return inv.Hosts[i].Name < inv.Hosts[j].Name })
	sort.Slice(inv.VMs, func(i, j int) bool {
		return strings.ToLower(inv.VMs[i].Name) < strings.ToLower(inv.VMs[j].Name)
	})
	sort.Slice(inv.Datastores, func(i, j int) bool { return inv.Datastores[i].Name < inv.Datastores[j].Name })
}

// Merge keeps the guest systems and addresses of a previous read when this
// one did not read them.
func (inv *Inventory) Merge(old *Inventory) {
	if old == nil {
		return
	}
	prev := map[string]VM{}
	for _, v := range old.VMs {
		prev[v.ID] = v
	}
	for i := range inv.VMs {
		p, ok := prev[inv.VMs[i].ID]
		if !ok {
			continue
		}
		if inv.VMs[i].OS == "" {
			inv.VMs[i].OS = p.OS
		}
		if len(inv.VMs[i].IPs) == 0 && inv.VMs[i].State == "running" {
			inv.VMs[i].IPs = p.IPs
		}
	}
}

// Metrics are the values a hypervisor check stores: CPU and memory of all
// hosts together, machines running and in all, hosts down and the fullest
// datastore.
func (inv *Inventory) Metrics() map[string]float64 {
	m := map[string]float64{}
	var cpus, cpuUsed float64
	var mem, memUsed int64
	down := 0
	for _, h := range inv.Hosts {
		if !h.Up {
			down++
			continue
		}
		c := float64(max(h.CPUs, 1))
		cpus += c
		cpuUsed += h.CPUPct * c
		mem += h.MemB
		memUsed += h.MemUsedB
	}
	if cpus > 0 {
		m["cpu_pct"] = round2(cpuUsed / cpus)
	}
	if mem > 0 {
		m["mem_pct"] = round2(float64(memUsed) / float64(mem) * 100)
	}
	run, all := 0, 0
	for _, v := range inv.VMs {
		if v.Template {
			continue
		}
		all++
		if v.State == "running" {
			run++
		}
	}
	m["vms_running"] = float64(run)
	m["vms_total"] = float64(all)
	m["hosts_down"] = float64(down)
	full := -1.0
	for _, d := range inv.Datastores {
		if d.Active && d.CapB > 0 {
			full = math.Max(full, d.UsedPct())
		}
	}
	if full >= 0 {
		m["ds_used_pct"] = full
	}
	return m
}

// Picture is the picture of the device library for a machine.
func Picture(v VM) string {
	os := strings.ToLower(v.OS + " " + v.Name)
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(os, x) {
				return true
			}
		}
		return false
	}
	if v.Kind == "ct" {
		switch {
		case has("docker"):
			return "ct-docker"
		}
		return "ct-lxc"
	}
	switch {
	case has("vcsa", "vcenter"):
		return "vm-vcsa"
	case has("chr", "mikrotik", "routeros"):
		return "vm-mikrotik-chr"
	case has("pfsense"):
		return "vm-pfsense"
	case has("opnsense"):
		return "vm-opnsense"
	case has("fortigate", "fortios"):
		return "vm-fortigate-vm"
	case has("truenas"):
		return "vm-truenas"
	case has("watchzit"):
		return "vm-watchzit"
	case has("srv", "server", "w2k", "win20", "windows20"):
		if has("dc", "domain") {
			return "vm-dc"
		}
		return "vm-windows-server"
	case has("win", "w10", "w11"):
		return "vm-windows"
	case has("ubuntu"):
		return "vm-ubuntu"
	case has("debian"):
		return "vm-debian"
	case has("rhel", "redhat"):
		return "vm-rhel"
	case has("rocky"):
		return "vm-rocky"
	case has("alma"):
		return "vm-almalinux"
	case has("centos"):
		return "vm-centos"
	case has("fedora"):
		return "vm-fedora"
	case has("suse", "sles"):
		return "vm-suse"
	case has("oracle"):
		return "vm-oracle-linux"
	case has("alpine"):
		return "vm-alpine"
	case has("freebsd"):
		return "vm-freebsd"
	case has("darwin", "macos"):
		return "vm-macos"
	}
	return "vm-linux"
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func client(insecure bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure, MinVersion: tls.VersionTLS12}, // #nosec G402 -- the user chose to accept an untrusted certificate (self-signed hypervisor)
		Proxy: nil, ResponseHeaderTimeout: 30 * time.Second}
	return &http.Client{Transport: tr, Jar: jar, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func baseURL(c Conn, def int) string {
	port := c.Port
	if port == 0 {
		port = def
	}
	host := c.Address
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	u := "https://" + host
	if port != 443 {
		u += ":" + strconv.Itoa(port)
	}
	return u
}
