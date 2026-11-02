package cfgbackup

import (
	"bufio"
	"net/netip"
	"strings"
)

// What a saved configuration tells about the device's ports: their
// addresses (two devices in the same point-to-point network are linked)
// and their descriptions (which often name the device at the other end).

// PortInfo is a port of a configuration.
type PortInfo struct {
	Interface   string
	Prefixes    []netip.Prefix
	Description string
}

// Ports reads the ports of a configuration of the given method.
func Ports(method, text string) []PortInfo {
	switch method {
	case MikroTik:
		return mikrotikPorts(text)
	case Cisco:
		return blockPorts(text, "interface ", " ip address ", " description ", false)
	case FortiGate:
		return fortiPorts(text)
	}
	return nil
}

type portSet struct {
	order []string
	m     map[string]*PortInfo
}

func newPortSet() *portSet { return &portSet{m: map[string]*PortInfo{}} }

func (p *portSet) get(name string) *PortInfo {
	if x := p.m[name]; x != nil {
		return x
	}
	x := &PortInfo{Interface: name}
	p.m[name] = x
	p.order = append(p.order, name)
	return x
}

func (p *portSet) list() []PortInfo {
	out := make([]PortInfo, 0, len(p.order))
	for _, n := range p.order {
		out = append(out, *p.m[n])
	}
	return out
}

// fields splits a RouterOS command line into key=value pairs (values may
// be quoted).
func fields(line string) map[string]string {
	out := map[string]string{}
	for len(line) > 0 {
		line = strings.TrimLeft(line, " ")
		eq := strings.IndexByte(line, '=')
		sp := strings.IndexByte(line, ' ')
		if eq < 0 {
			break
		}
		if sp >= 0 && sp < eq { // a word without a value
			line = line[sp+1:]
			continue
		}
		key := line[:eq]
		line = line[eq+1:]
		var val string
		if strings.HasPrefix(line, `"`) {
			end := 1
			for end < len(line) && (line[end] != '"' || line[end-1] == '\\') {
				end++
			}
			val = strings.ReplaceAll(line[1:min(end, len(line))], `\"`, `"`)
			line = line[min(end+1, len(line)):]
		} else {
			sp = strings.IndexByte(line, ' ')
			if sp < 0 {
				val, line = line, ""
			} else {
				val, line = line[:sp], line[sp+1:]
			}
		}
		out[key] = val
	}
	return out
}

func mikrotikPorts(text string) []PortInfo {
	ps := newPortSet()
	section := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "/") {
			// "/ip address add …" (terse) or a section header "/ip address"
			parts := strings.SplitN(line, " add ", 2)
			if len(parts) == 2 {
				section = parts[0]
				line = "add " + parts[1]
			} else if p := strings.SplitN(line, " set ", 2); len(p) == 2 {
				section = p[0]
				line = "set " + p[1]
			} else {
				section = line
				continue
			}
		}
		f := fields(line)
		switch {
		case section == "/ip address" && strings.HasPrefix(line, "add "):
			if p, err := netip.ParsePrefix(f["address"]); err == nil && f["interface"] != "" {
				x := ps.get(f["interface"])
				x.Prefixes = append(x.Prefixes, p)
			}
		case strings.HasPrefix(section, "/interface") && f["comment"] != "":
			name := f["name"]
			if name == "" {
				name = f["default-name"]
			}
			if strings.Contains(line, "[ find default-name=") {
				if i := strings.Index(line, "default-name="); i >= 0 {
					rest := line[i+len("default-name="):]
					name, _, _ = strings.Cut(rest, " ")
				}
			}
			if name != "" {
				ps.get(name).Description = f["comment"]
			}
		}
	}
	return ps.list()
}

// blockPorts reads IOS-like configurations: "interface X" blocks with
// indented "ip address A M" and "description …" lines.
func blockPorts(text, head, addr, desc string, _ bool) []PortInfo {
	ps := newPortSet()
	var cur *PortInfo
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case strings.HasPrefix(line, head):
			cur = ps.get(strings.TrimSpace(strings.TrimPrefix(line, head)))
		case cur != nil && strings.HasPrefix(line, addr):
			f := strings.Fields(strings.TrimPrefix(line, addr))
			if len(f) >= 2 {
				if p, ok := prefixOf(f[0], f[1]); ok {
					cur.Prefixes = append(cur.Prefixes, p)
				}
			}
		case cur != nil && strings.HasPrefix(line, desc):
			cur.Description = strings.TrimSpace(strings.TrimPrefix(line, desc))
		case len(line) > 0 && line[0] != ' ':
			cur = nil
		}
	}
	return ps.list()
}

func fortiPorts(text string) []PortInfo {
	ps := newPortSet()
	var cur *PortInfo
	in := false
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), `"`) }
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "config system interface":
			in = true
		case !in:
		case strings.HasPrefix(line, "edit "):
			cur = ps.get(unq(strings.TrimPrefix(line, "edit ")))
		case line == "next":
			cur = nil
		case line == "end" && cur == nil:
			in = false
		case cur != nil && strings.HasPrefix(line, "set ip "):
			f := strings.Fields(strings.TrimPrefix(line, "set ip "))
			if len(f) >= 2 {
				if p, ok := prefixOf(f[0], f[1]); ok {
					cur.Prefixes = append(cur.Prefixes, p)
				}
			}
		case cur != nil && strings.HasPrefix(line, "set description "):
			cur.Description = unq(strings.TrimPrefix(line, "set description "))
		case cur != nil && strings.HasPrefix(line, "set alias ") && cur.Description == "":
			cur.Description = unq(strings.TrimPrefix(line, "set alias "))
		}
	}
	return ps.list()
}

// prefixOf turns an address and a dotted mask into a prefix.
func prefixOf(addr, mask string) (netip.Prefix, bool) {
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return netip.Prefix{}, false
	}
	m, err := netip.ParseAddr(mask)
	if err != nil || !m.Is4() {
		return netip.Prefix{}, false
	}
	b := m.As4()
	bits, done := 0, false
	for _, x := range b {
		for i := 7; i >= 0; i-- {
			if x&(1<<i) != 0 {
				if done {
					return netip.Prefix{}, false
				}
				bits++
			} else {
				done = true
			}
		}
	}
	return netip.PrefixFrom(a, bits), true
}
