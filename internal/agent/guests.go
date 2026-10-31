package agent

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"github.com/max-zit/watchzit/internal/proto"
)

// Machines and containers the computer runs: Hyper-V on Windows (through
// PowerShell's Get-VM), Docker on Linux (through its socket). The parsers
// are here so they are tested on every system.

// hyperVScript lists the machines as JSON (an array, also for one).
const hyperVScript = `$ErrorActionPreference='Stop'
$v = @(Get-VM | ForEach-Object { [pscustomobject]@{ id = [string]$_.VMId; name = $_.Name; state = [string]$_.State; cpu = $_.CPUUsage; cpus = $_.ProcessorCount; mem = $_.MemoryAssigned; demand = $_.MemoryDemand; startup = $_.MemoryStartup; ips = @($_.NetworkAdapters | ForEach-Object { $_.IPAddresses }) } })
ConvertTo-Json -InputObject $v -Compress -Depth 3`

func usableIPv4(s string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	return err == nil && a.Is4() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsUnspecified()
}

func sortGuests(g []proto.Guest) {
	sort.Slice(g, func(i, j int) bool { return strings.ToLower(g[i].Name) < strings.ToLower(g[j].Name) })
}

// parseHyperV reads the output of hyperVScript.
func parseHyperV(data []byte) (*proto.Guests, error) {
	var vs []struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		State   string   `json:"state"`
		CPU     float64  `json:"cpu"`
		CPUs    int      `json:"cpus"`
		Mem     int64    `json:"mem"`
		Demand  int64    `json:"demand"`
		Startup int64    `json:"startup"`
		IPs     []string `json:"ips"`
	}
	if err := json.Unmarshal(data, &vs); err != nil {
		return nil, err
	}
	g := &proto.Guests{Kind: "hyperv", Product: "Hyper-V", Items: []proto.Guest{}}
	for _, v := range vs {
		x := proto.Guest{ID: v.ID, Name: v.Name, CPUs: v.CPUs, MemB: v.Mem}
		switch strings.ToLower(v.State) {
		case "running":
			x.State = "running"
			x.CPUPct = v.CPU
			if v.Mem > 0 {
				x.MemPct = float64(v.Demand) / float64(v.Mem) * 100
			}
		case "paused", "saved":
			x.State = "suspended"
		default:
			x.State = "stopped"
		}
		if x.MemB == 0 {
			x.MemB = v.Startup
		}
		for _, ip := range v.IPs {
			if usableIPv4(ip) {
				x.IPs = append(x.IPs, ip)
			}
		}
		g.Items = append(g.Items, x)
	}
	sortGuests(g.Items)
	return g, nil
}

// parseDocker reads GET /containers/json?all=1.
func parseDocker(data []byte, version string) (*proto.Guests, error) {
	var cs []struct {
		ID              string   `json:"Id"`
		Names           []string `json:"Names"`
		Image           string   `json:"Image"`
		State           string   `json:"State"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, err
	}
	g := &proto.Guests{Kind: "docker", Product: strings.TrimSpace("Docker " + version), Items: []proto.Guest{}}
	for _, c := range cs {
		x := proto.Guest{ID: c.ID, Name: c.ID, OS: c.Image}
		if len(x.ID) > 12 {
			x.ID = x.ID[:12]
		}
		if len(c.Names) > 0 {
			x.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		switch c.State {
		case "running":
			x.State = "running"
		case "paused":
			x.State = "suspended"
		default:
			x.State = "stopped"
		}
		for _, n := range c.NetworkSettings.Networks {
			if usableIPv4(n.IPAddress) {
				x.IPs = append(x.IPs, n.IPAddress)
			}
		}
		sort.Strings(x.IPs)
		g.Items = append(g.Items, x)
	}
	sortGuests(g.Items)
	return g, nil
}
