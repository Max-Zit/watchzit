package hypervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Proxmox VE over its REST API: the cluster resources (nodes, machines,
// containers, storage) in one request, the node addresses from the
// cluster status, and with Details the guest type and addresses of each
// machine. A user with the PVEAuditor role is enough; an API token
// (user@realm!name) is used as it is, a password signs in for a ticket.

type pve struct {
	hc    *http.Client
	base  string
	token string
}

func (p *pve) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/api2/json/"+path, nil)
	if err != nil {
		return err
	}
	if p.token != "" {
		req.Header.Set("Authorization", p.token)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return pveErr(resp, data)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return errors.New("the answer is not a Proxmox VE answer")
	}
	return json.Unmarshal(env.Data, out)
}

func pveErr(resp *http.Response, data []byte) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return errors.New("the Proxmox VE server refused the user or token")
	case http.StatusForbidden:
		return errors.New("the user or token may not read this (give it the PVEAuditor role on /)")
	}
	msg := strings.TrimSpace(resp.Status)
	if len(data) > 0 && len(data) < 300 {
		msg += ": " + strings.TrimSpace(string(data))
	}
	return fmt.Errorf("the Proxmox VE server answered %s", msg)
}

func (p *pve) login(ctx context.Context, user, password string) error {
	if strings.Contains(user, "!") {
		p.token = "PVEAPIToken=" + user + "=" + password
		return nil
	}
	if !strings.Contains(user, "@") {
		user += "@pam"
	}
	form := url.Values{"username": {user}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return pveErr(resp, data)
	}
	var t struct {
		Data struct {
			Ticket string `json:"ticket"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &t) != nil || t.Data.Ticket == "" {
		return errors.New("the Proxmox VE server refused the user or token")
	}
	u, _ := url.Parse(p.base)
	p.hc.Jar.SetCookies(u, []*http.Cookie{{Name: "PVEAuthCookie", Value: t.Data.Ticket}}) // #nosec G124 -- client-side jar for the API ticket, not a cookie we serve
	return nil
}

type pveResource struct {
	Type     string  `json:"type"`
	ID       string  `json:"id"`
	Node     string  `json:"node"`
	Name     string  `json:"name"`
	Status   string  `json:"status"`
	VMID     int     `json:"vmid"`
	CPU      float64 `json:"cpu"`
	MaxCPU   float64 `json:"maxcpu"`
	Mem      int64   `json:"mem"`
	MaxMem   int64   `json:"maxmem"`
	Disk     int64   `json:"disk"`
	MaxDisk  int64   `json:"maxdisk"`
	Template int     `json:"template"`
	Storage  string  `json:"storage"`
	Shared   int     `json:"shared"`
	Plugin   string  `json:"plugintype"`
}

func readProxmox(ctx context.Context, c Conn) (*Inventory, error) {
	p := &pve{hc: client(c.Insecure), base: baseURL(c, 8006)}
	defer p.hc.CloseIdleConnections()
	if err := p.login(ctx, c.User, c.Secret); err != nil {
		return nil, err
	}
	var res []pveResource
	if err := p.get(ctx, "cluster/resources", &res); err != nil {
		return nil, err
	}
	var status []struct {
		Type string `json:"type"`
		Name string `json:"name"`
		IP   string `json:"ip"`
	}
	_ = p.get(ctx, "cluster/status", &status)
	var ver struct {
		Version string `json:"version"`
	}
	_ = p.get(ctx, "version", &ver)
	inv := pveInventory(res)
	if ver.Version != "" {
		inv.Product = "Proxmox VE " + ver.Version
	}
	for i := range inv.Hosts {
		for _, s := range status {
			if s.Type == "node" && s.Name == inv.Hosts[i].Name {
				inv.Hosts[i].Address = s.IP
			}
		}
	}
	if c.Details {
		p.details(ctx, inv)
	}
	return inv, nil
}

func pveInventory(res []pveResource) *Inventory {
	inv := &Inventory{Kind: Proxmox, Hosts: []Host{}, VMs: []VM{}}
	shared := map[string]bool{}
	for _, r := range res {
		switch r.Type {
		case "node":
			h := Host{Name: r.Node, Up: r.Status == "online", CPUs: int(r.MaxCPU), CPUPct: round2(r.CPU * 100), MemB: r.MaxMem, MemUsedB: r.Mem}
			inv.Hosts = append(inv.Hosts, h)
		case "qemu", "lxc":
			v := VM{ID: strconv.Itoa(r.VMID), Name: r.Name, Host: r.Node, Template: r.Template == 1, CPUs: int(r.MaxCPU), MemB: r.MaxMem}
			if r.Type == "lxc" {
				v.Kind = "ct"
			}
			switch r.Status {
			case "running":
				v.State = "running"
				v.CPUPct = round2(r.CPU * 100)
				if r.MaxMem > 0 {
					v.MemPct = round2(float64(r.Mem) / float64(r.MaxMem) * 100)
				}
			case "paused", "suspended":
				v.State = "suspended"
			default:
				v.State = "stopped"
			}
			inv.VMs = append(inv.VMs, v)
		case "storage":
			if r.Shared == 1 {
				if shared[r.Storage] {
					continue
				}
				shared[r.Storage] = true
			}
			d := Datastore{Name: r.Storage, Type: r.Plugin, CapB: r.MaxDisk, FreeB: r.MaxDisk - r.Disk, Active: r.Status == "available"}
			if r.Shared != 1 {
				d.Host = r.Node
				d.Name = r.Storage + " (" + r.Node + ")"
			}
			inv.Datastores = append(inv.Datastores, d)
		}
	}
	return inv
}

// details reads the guest type of each machine and the addresses of the
// running ones (from the QEMU guest agent or the container), at most 200.
func (p *pve) details(ctx context.Context, inv *Inventory) {
	for i := range inv.VMs {
		if i >= 200 || ctx.Err() != nil {
			return
		}
		v := &inv.VMs[i]
		kind := "qemu"
		if v.Kind == "ct" {
			kind = "lxc"
		}
		base := "nodes/" + url.PathEscape(v.Host) + "/" + kind + "/" + v.ID
		var cfg struct {
			OSType string `json:"ostype"`
		}
		if p.get(ctx, base+"/config", &cfg) == nil {
			v.OS = cfg.OSType
		}
		if v.State != "running" || v.Template {
			continue
		}
		if kind == "lxc" {
			var ifs []struct {
				Name string `json:"name"`
				Inet string `json:"inet"`
			}
			if p.get(ctx, base+"/interfaces", &ifs) == nil {
				for _, f := range ifs {
					if f.Name != "lo" && f.Inet != "" {
						v.IPs = append(v.IPs, strings.Split(f.Inet, "/")[0])
					}
				}
			}
			continue
		}
		var ag struct {
			Result []struct {
				Name string `json:"name"`
				IPs  []struct {
					Type string `json:"ip-address-type"`
					IP   string `json:"ip-address"`
				} `json:"ip-addresses"`
			} `json:"result"`
		}
		if p.get(ctx, base+"/agent/network-get-interfaces", &ag) == nil {
			for _, f := range ag.Result {
				if f.Name == "lo" || strings.HasPrefix(strings.ToLower(f.Name), "loopback") {
					continue
				}
				for _, a := range f.IPs {
					if a.Type == "ipv4" && !strings.HasPrefix(a.IP, "127.") && !strings.HasPrefix(a.IP, "169.254.") {
						v.IPs = append(v.IPs, a.IP)
					}
				}
			}
		}
	}
}
