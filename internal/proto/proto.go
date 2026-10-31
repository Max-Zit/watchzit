// Package proto is the protocol between the WatchZit agent and server:
// enrollment over HTTPS, then one WebSocket channel the agent keeps open
// (both sides authenticated: the agent pins the server's certificate, the
// server knows the agent by the ed25519 key of its client certificate).
package proto

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Version of the protocol.
const Version = 1

// Endpoints on the agent listener.
const (
	PathEnroll  = "/agent/enroll"
	PathConnect = "/agent/connect"
)

// Size limits.
const (
	MaxEnrollBody  = 16 << 10
	MaxMessageSize = 4 << 20
)

// Message types.
const (
	TypeHello            = "hello"     // agent → server, first message
	TypeWelcome          = "welcome"   // server → agent: interval, watched services
	TypeReport           = "report"    // agent → server: metrics
	TypeInventory        = "inventory" // agent → server: what the machine is
	TypeInventoryRequest = "inventory-request"
)

// EnrollRequest joins a machine with an enrollment token.
type EnrollRequest struct {
	Protocol     int      `json:"protocol"`
	Token        string   `json:"token"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	AgentVersion string   `json:"agent_version"`
	Addresses    []string `json:"addresses,omitempty"`
}

// EnrollResponse tells the agent who it is now.
type EnrollResponse struct {
	AgentID  string `json:"agent_id"`
	DeviceID int64  `json:"device_id"`
	Device   string `json:"device"`
}

// Error is the body of a refused request.
type Error struct {
	Error string `json:"error"`
}

// Envelope wraps every channel message.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Hello is the agent's first message on a channel.
type Hello struct {
	Protocol     int       `json:"protocol"`
	AgentVersion string    `json:"agent_version"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`
	BootTime     time.Time `json:"boot_time"`
}

// Welcome answers hello.
type Welcome struct {
	DeviceID        int64    `json:"device_id"`
	IntervalSeconds int      `json:"interval_s"`
	Services        []string `json:"services,omitempty"` // services to report the state of
}

// Report is one sample of the machine.
type Report struct {
	Time      time.Time  `json:"time"`
	CPU       float64    `json:"cpu"` // %, all cores
	Load1     *float64   `json:"load1,omitempty"`
	MemTotal  uint64     `json:"mem_total"`
	MemUsed   uint64     `json:"mem_used"`
	SwapTotal uint64     `json:"swap_total,omitempty"`
	SwapUsed  uint64     `json:"swap_used,omitempty"`
	Uptime    float64    `json:"uptime"` // seconds
	Procs     int        `json:"procs,omitempty"`
	Disks     []Disk     `json:"disks,omitempty"`
	Nets      []Net      `json:"nets,omitempty"`
	Services  []Service  `json:"services,omitempty"`
	VXLANs    []VXLAN    `json:"vxlans,omitempty"`
	Guests    *Guests    `json:"guests,omitempty"` // Hyper-V machines or Docker containers, read every five minutes
	Errors    []string   `json:"errors,omitempty"`
	Boot      *time.Time `json:"boot,omitempty"`
}

// Disk is a mounted file system or a drive.
type Disk struct {
	Mount string `json:"mount"`
	FS    string `json:"fs,omitempty"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// Net is a network interface; rates are per second since the last report
// (nil on the first report after a start).
type Net struct {
	Index  int      `json:"index"`
	Name   string   `json:"name"`
	Alias  string   `json:"alias,omitempty"`
	MAC    string   `json:"mac,omitempty"`
	Speed  int64    `json:"speed,omitempty"` // bit/s
	Up     bool     `json:"up"`
	InBps  *float64 `json:"in_bps,omitempty"`
	OutBps *float64 `json:"out_bps,omitempty"`
	InErr  *float64 `json:"in_err,omitempty"`
	OutErr *float64 `json:"out_err,omitempty"`
	VLANs  []int    `json:"vlans,omitempty"` // VLANs the port carries tagged (VLAN interfaces on it)
}

// VXLAN is a VXLAN interface of the machine (Linux): its VNI, own address
// and remote VTEPs.
type VXLAN struct {
	VNI     int      `json:"vni"`
	Name    string   `json:"name"`
	Local   string   `json:"local,omitempty"`
	Remotes []string `json:"remotes,omitempty"`
	Port    int      `json:"port,omitempty"`
}

// Service is the state of a watched service.
type Service struct {
	Name    string `json:"name"`
	Display string `json:"display,omitempty"`
	Running bool   `json:"running"`
	State   string `json:"state"` // running, stopped, failed, missing …
}

// Inventory describes the machine (sent after connecting and daily).
type Inventory struct {
	Hostname  string        `json:"hostname"`
	OS        string        `json:"os"`
	OSVersion string        `json:"os_version,omitempty"`
	Kernel    string        `json:"kernel,omitempty"`
	Arch      string        `json:"arch"`
	CPUModel  string        `json:"cpu_model,omitempty"`
	CPUs      int           `json:"cpus"`
	MemTotal  uint64        `json:"mem_total"`
	Vendor    string        `json:"vendor,omitempty"`
	Model     string        `json:"model,omitempty"`
	Serial    string        `json:"serial,omitempty"`
	Addresses []string      `json:"addresses,omitempty"`
	Services  []ServiceInfo `json:"services,omitempty"` // that could be watched
}

// ServiceInfo is a service the console offers to watch.
type ServiceInfo struct {
	Name    string `json:"name"`
	Display string `json:"display,omitempty"`
	Auto    bool   `json:"auto"` // starts with the system
}

// NormalizeFingerprint lowercases a SHA-256 fingerprint and drops ':' and spaces.
func NormalizeFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s)))
}

// EnrollCodePrefix starts every enrollment code.
const EnrollCodePrefix = "WZ1-"

// EnrollData is what an enrollment code carries: one string to paste.
type EnrollData struct {
	Server      string `json:"s"`
	Fingerprint string `json:"f"`
	Token       string `json:"t"`
}

// MakeEnrollCode encodes an enrollment code.
func MakeEnrollCode(d EnrollData) string {
	b, _ := json.Marshal(d)
	return EnrollCodePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// Guests are the virtual machines (Hyper-V) or containers (Docker) the
// machine runs.
type Guests struct {
	Kind    string  `json:"kind"` // hyperv or docker
	Product string  `json:"product,omitempty"`
	Items   []Guest `json:"items"`
}

// Guest is one machine or container. State is running, stopped or
// suspended.
type Guest struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	State  string   `json:"state"`
	OS     string   `json:"os,omitempty"` // container: its image
	CPUs   int      `json:"cpus,omitempty"`
	CPUPct float64  `json:"cpu_pct,omitempty"`
	MemB   int64    `json:"mem_b,omitempty"`
	MemPct float64  `json:"mem_pct,omitempty"`
	IPs    []string `json:"ips,omitempty"`
}
