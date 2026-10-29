package main

import (
	"fmt"
	"math"
	"strings"
)

// Drawings of catalog models: the same building blocks as the generic
// pictures, sized by the model's ports and bays, with the vendor and model
// written on the front.

// mgmtName is the server management port as each vendor calls it.
var mgmtName = map[string]string{"HPE": "iLO", "Dell": "iDRAC", "Lenovo": "XCC", "Cisco": "CIMC", "Fujitsu": "iRMC", "Supermicro": "IPMI"}

func drawModel(m model) *pic {
	switch m.form {
	case "rack":
		if m.kind == "server" || m.kind == "storage" {
			return serverModel(m)
		}
		return rackModel(m)
	case "tower":
		return towerModel(m)
	case "outdoor":
		if m.copper+m.sfp > 3 {
			return deskModel(m, false, true)
		}
		return outdoorModel(m)
	case "wifi":
		return deskModel(m, true, false)
	case "nas":
		return nasModel(m)
	case "ceiling":
		return ceilingModel(m)
	case "mesh":
		return meshModel(m)
	case "bullet", "dome", "turret", "ptz":
		return cameraModel(m)
	case "dish", "omni", "sector", "panel":
		return antennaModel(m)
	case "upstower":
		return upsTowerModel(m)
	case "printer":
		return printerModel(m)
	case "phone":
		return phoneModel(m)
	case "mini":
		return miniModel(m)
	}
	return deskModel(m, false, false)
}

// esc keeps model names like "hAP ac²" or "CRS326-24S+2Q+RM" safe in SVG.
func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// label writes vendor and model in a box of the given width, the model
// shrunk to fit (about 0.56 em per character).
func (p *pic) label(x, y, width float64, vendor, name, light, dark string) {
	p.text(x, y, 5.5, dark, esc(strings.ToUpper(vendor)))
	size := math.Min(7.5, width/(0.56*float64(len([]rune(name)))))
	p.text(x, y+size+3, math.Max(size, 3.5), light, esc(name))
}

// mport draws an RJ45 jack of width w (≤ 14) at x,y and records the port.
func (p *pic) mport(x, y, w float64, up bool) {
	h := w * 11 / 14
	p.add(`<rect x="%g" y="%g" width="%g" height="%g" rx="1" fill="#0c0f14" stroke="#7d8796" stroke-width=".8"/>`, r1(x-w/2), r1(y-h/2), r1(w), r1(h))
	ty := y + h/2 - 3
	if !up {
		ty = y - h/2
	}
	p.add(`<rect x="%g" y="%g" width="%g" height="3" fill="#1f2530"/>`, r1(x-w*0.22), r1(ty), r1(w*0.44))
	p.ports = append(p.ports, [2]float64{r1(x), r1(y)})
}

// mcage draws an SFP cage of width w at x,y and records the port.
func (p *pic) mcage(x, y, w float64) {
	h := w * 9 / 13
	p.add(`<rect x="%g" y="%g" width="%g" height="%g" rx="1" fill="#c4cad3" stroke="#7d8796" stroke-width=".8"/><rect x="%g" y="%g" width="%g" height="%g" fill="#1a1f27"/>`,
		r1(x-w/2), r1(y-h/2), r1(w), r1(h), r1(x-w*0.35), r1(y-h*0.28), r1(w*0.7), r1(h*0.56))
	p.ports = append(p.ports, [2]float64{r1(x), r1(y)})
}

func r1(v float64) float64 { return math.Round(v*10) / 10 }

// portGrid draws copper then SFP ports in `rows` rows between x0 and x1,
// numbered column by column (1 top, 2 below …) like on switches.
func (p *pic) portGrid(copper, sfp, rows int, x0, x1, top, bottom float64) {
	ccols := (copper + rows - 1) / rows
	scols := (sfp + rows - 1) / rows
	gap := 0.0
	if copper > 0 && sfp > 0 {
		gap = 1
	}
	cols := float64(ccols+scols) + gap
	step := math.Min(18, (x1-x0)/math.Max(cols, 1))
	w := math.Min(14, step-1.5)
	dy := 0.0
	if rows > 1 {
		dy = (bottom - top) / float64(rows-1)
	}
	x := x0 + step/2
	for c := 0; c < ccols; c++ {
		for r := 0; r < rows && c*rows+r < copper; r++ {
			p.mport(x, top+float64(r)*dy, w, r%2 == 0)
		}
		x += step
	}
	x += gap * step
	for c := 0; c < scols; c++ {
		for r := 0; r < rows && c*rows+r < sfp; r++ {
			p.mcage(x, top+float64(r)*dy, w)
		}
		x += step
	}
}

// rackModel: routers, switches and firewalls in a rack.
func rackModel(m model) *pic {
	u := max(m.u, 1)
	h := float64(u) * uH
	p := newPic(rackW, h)
	p.chassis(u, vendorAccent[m.vendor])
	if m.vendor == "MikroTik" {
		p.add(`<rect x="16" y="1.5" width="%g" height="%g" rx="2" fill="url(#l)"/>`, rackW-32, h-3)
	}
	light, dark := "#e6ebf2", "#c9d2df"
	if m.vendor == "MikroTik" {
		light, dark = "#1b2330", "#4a5466"
	}
	p.label(22, 14, 80, m.vendor, m.name, light, dark)
	p.led(24, h-9, "#36d27a")
	p.led(30, h-9, "#4ea1ff")
	rows := 1
	if m.copper+m.sfp > 12 {
		rows = 2
	}
	if u >= 2 && m.copper+m.sfp > 24 {
		rows = 4
	}
	top, bottom := h/2, h/2
	if rows > 1 {
		top, bottom = 13, h-13
	}
	p.portGrid(m.copper, m.sfp, rows, 108, rackW-24, top, bottom)
	return p
}

// serverModel: a rack server's front with its drive bays and, on the
// right, the network ports and the management port.
func serverModel(m model) *pic {
	u := max(m.u, 1)
	h := float64(u) * uH
	p := newPic(rackW, h)
	p.chassis(u, vendorAccent[m.vendor])
	p.add(`<rect x="20" y="5" width="80" height="%g" rx="2" fill="url(#s)"/>`, h-10)
	p.label(24, 14, 72, m.vendor, m.name, "#e6ebf2", "#9aa5b5")
	p.led(26, h-11, "#36d27a")
	p.add(`<circle cx="90" cy="%g" r="4" fill="#1f2530" stroke="#4ea1ff" stroke-width="1.3"/>`, h-12)
	// ports: NICs then the management port
	right := rackW - 22
	nicW := float64(m.nic+1)*16 + 8
	px := right - nicW + 12
	py := h / 2
	if u >= 2 {
		py = h - 14
	}
	for i := 0; i < m.nic; i++ {
		p.mport(px, py, 13, true)
		px += 16
	}
	p.mport(px, py, 13, true)
	p.add(`<rect x="%g" y="%g" width="16" height="13" rx="1.5" fill="none" stroke="#c9a227" stroke-width=".9"/>`, px-8, py-6.5)
	if n := mgmtName[m.vendor]; n != "" && m.kind == "server" {
		p.text(px-7, py-9, 4.5, "#c9a227", n)
	} else if m.kind == "storage" {
		p.text(px-9, py-9, 4.5, "#c9a227", "MGMT")
	}
	// drive bays between the label and the ports
	x0, x1 := 106.0, right-nicW-6
	if m.bays == 0 {
		return p
	}
	if m.lff {
		cols := max(4, int(math.Ceil(float64(m.bays)/(float64(u)*1.5))))
		if m.bays < 4 {
			cols = m.bays
		}
		rows := (m.bays + cols - 1) / cols
		bw := (x1 - x0) / float64(cols)
		bh := (h - 8) / float64(rows)
		for i := 0; i < m.bays; i++ {
			p.bay35(x0+float64(i%cols)*bw, 4+float64(i/cols)*bh, bw-3, bh-2)
		}
		return p
	}
	rows := 1
	if u >= 3 {
		rows = 2
	}
	per := (m.bays + rows - 1) / rows
	bw := math.Min(22, (x1-x0)/float64(per))
	bh := (h - 8) / float64(rows)
	for i := 0; i < m.bays; i++ {
		x := x0 + float64(i%per)*bw
		y := 4 + float64(i/per)*bh
		p.add(`<rect x="%g" y="%g" width="%g" height="%g" rx="1" fill="#20262f" stroke="#59616e" stroke-width=".7"/>`, r1(x), r1(y), r1(bw-2), r1(bh-2))
		p.led(r1(x+(bw-2)/2), r1(y+bh-6), "#36d27a")
	}
	return p
}

// deskModel: desktop boxes (routers, small switches and firewalls), with
// antennas for Wi-Fi models, rugged for outdoor switches.
func deskModel(m model, wifi, outdoor bool) *pic {
	n := m.copper + m.sfp
	w := math.Max(130, 70+float64(n)*17+map[bool]float64{true: 10, false: 0}[m.copper > 0 && m.sfp > 0])
	top := 0.0
	if wifi {
		top = 18
	}
	h := top + 56
	p := newPic(w, h)
	body, light, dark := "url(#m)", "#e6ebf2", "#9aa5b5"
	if m.vendor == "MikroTik" {
		body, light, dark = "url(#l)", "#1b2330", "#4a5466"
	}
	if wifi {
		for _, x := range []float64{w * 0.2, w * 0.5, w * 0.8} {
			p.add(`<rect x="%g" y="1" width="5" height="%g" rx="2.5" fill="#3a424f"/>`, r1(x-2.5), top+4)
		}
	}
	p.add(`<rect x="1" y="%g" width="%g" height="54" rx="%g" fill="%s" stroke="#59616e"/>`, top+1, r1(w-2), map[bool]float64{true: 9, false: 5}[outdoor], body)
	if a := vendorAccent[m.vendor]; a != "" && m.vendor != "MikroTik" {
		p.add(`<rect x="6" y="%g" width="%g" height="2.5" fill="%s"/>`, top+51, r1(w-12), a)
	}
	p.label(10, top+12, math.Min(w-60, 160), m.vendor, m.name, light, dark)
	p.led(w-14, top+10, "#36d27a")
	p.led(w-22, top+10, "#4ea1ff")
	p.portGrid(m.copper, m.sfp, 1, 10, w-10, top+38, top+38)
	return p
}

// towerModel: a tower server with its bays and, at the bottom, the network
// ports and the management port.
func towerModel(m model) *pic {
	w, h := 120.0, 210.0
	p := newPic(w, h)
	p.add(`<rect x="2" y="2" width="%g" height="%g" rx="6" fill="url(#m)" stroke="#59616e"/>`, w-4, h-4)
	if a := vendorAccent[m.vendor]; a != "" {
		p.add(`<rect x="8" y="8" width="3" height="%g" fill="%s"/>`, h-16, a)
	}
	p.label(16, 16, 96, m.vendor, m.name, "#e6ebf2", "#9aa5b5")
	p.add(`<circle cx="%g" cy="44" r="5" fill="#1f2530" stroke="#4ea1ff" stroke-width="1.5"/>`, w-20)
	bays := max(m.bays, 1)
	bh := math.Min(16, 110/float64(bays))
	for i := 0; i < bays; i++ {
		p.bay35(18, 58+float64(i)*bh, w-36, bh-2)
	}
	px := 20.0
	for i := 0; i < m.nic; i++ {
		p.mport(px, h-18, 12, true)
		px += 15
	}
	p.mport(px, h-18, 12, true)
	p.add(`<rect x="%g" y="%g" width="15" height="12" rx="1.5" fill="none" stroke="#c9a227" stroke-width=".9"/>`, px-7.5, h-24)
	return p
}

// outdoorModel: antennas and outdoor units with one or two ports.
func outdoorModel(m model) *pic {
	w, h := 90.0, 110.0
	p := newPic(w, h)
	p.add(`<rect x="4" y="4" width="%g" height="%g" rx="14" fill="url(#l)" stroke="#9aa5b5"/>`, w-8, h-30)
	p.add(`<circle cx="%g" cy="38" r="22" fill="none" stroke="#c4cad3" stroke-width="2"/>`, w/2)
	p.label(12, 16, w-24, m.vendor, m.name, "#1b2330", "#4a5466")
	p.add(`<rect x="%g" y="%g" width="40" height="22" rx="3" fill="#3a424f"/>`, w/2-20, h-28)
	p.portGrid(m.copper, m.sfp, 1, w/2-20, w/2+20, h-17, h-17)
	return p
}

// slug makes a file name of a vendor and model.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '+':
			b.WriteString("plus")
			dash = false
		case r == '²':
			b.WriteString("2")
		case r == '³':
			b.WriteString("3")
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// modelIcons draws the catalog and returns its index entries.
func modelIcons(out string) []Icon {
	count := map[string]int{}
	for _, m := range catalog() {
		count[slug(m.vendor+" "+m.name)]++
	}
	var dups []string
	for n, c := range count {
		if c > 1 {
			dups = append(dups, n)
		}
	}
	if len(dups) > 0 {
		panic("models named twice: " + strings.Join(dups, ", "))
	}
	cats := map[string]string{"server": "server", "switch": "network", "router": "network", "firewall": "security", "accesspoint": "wireless", "storage": "server", "ups": "power", "camera": "security", "printer": "end", "pc": "end", "other": "rack"}
	seen := map[string]bool{}
	var icons []Icon
	for _, m := range catalog() {
		name := slug(m.vendor + " " + m.name)
		if seen[name] {
			panic("two models named " + name)
		}
		seen[name] = true
		p := drawModel(m)
		ic := Icon{Name: name, Label: m.vendor + " " + m.name, Cat: cats[m.kind], Kind: deviceKind(m.kind), Vendor: m.vendor, Type: map[bool]string{true: "storage"}[m.kind == "storage"], W: p.w, H: p.h, Ports: p.ports}
		switch m.form {
		case "rack":
			ic.U, ic.Show = max(m.u, 1), 240
			if m.kind != "server" && m.kind != "storage" {
				ic.Fiber = m.sfp
			}
		case "tower":
			ic.Show = 55
		case "nas", "mesh":
			ic.Show = math.Max(45, p.w*0.55)
		case "bullet", "dome", "turret", "ptz":
			ic.Show = 70
		case "dish", "omni", "sector", "panel":
			ic.Show = 60
		case "upstower":
			ic.Show = 50
		case "printer":
			ic.Show = 80
		case "phone":
			ic.Show = 45
		case "mini":
			ic.Show = 60
		case "ceiling":
			ic.Show = 80
		case "outdoor":
			ic.Show = math.Min(150, p.w*0.6)
		default:
			ic.Show = math.Min(220, p.w*0.6)
			ic.Fiber = m.sfp
		}
		icons = append(icons, ic)
		write(out, name, p)
	}
	fmt.Println(len(icons), "model pictures")
	return icons
}

// nasModel: a desktop NAS, its bays side by side and the network ports
// below them.
func nasModel(m model) *pic {
	bays := max(m.bays, 1)
	n := m.copper + m.sfp
	w := math.Max(30+float64(bays)*26, 24+float64(n)*16)
	h := 128.0
	p := newPic(w, h)
	p.add(`<rect x="2" y="2" width="%g" height="%g" rx="8" fill="url(#m)" stroke="#59616e"/>`, r1(w-4), h-4)
	p.label(12, 13, w-24, m.vendor, m.name, "#e6ebf2", "#9aa5b5")
	bx := (w - float64(bays)*26) / 2
	for i := 0; i < bays; i++ {
		x := bx + 2 + float64(i)*26
		p.add(`<rect x="%g" y="28" width="22" height="66" rx="2" fill="#20262f" stroke="#59616e"/>`, r1(x))
		p.led(r1(x+11), 88, "#36d27a")
	}
	p.portGrid(m.copper, m.sfp, 1, 12, w-12, h-20, h-20)
	return p
}

// ceilingModel: a ceiling access point, seen from below, its ports at the
// edge.
func ceilingModel(m model) *pic {
	w, h := 120.0, 92.0
	p := newPic(w, h)
	p.add(`<ellipse cx="60" cy="44" rx="56" ry="40" fill="url(#l)" stroke="#9aa5b5"/><ellipse cx="60" cy="40" rx="48" ry="30" fill="#f8fafc"/>`)
	p.add(`<circle cx="60" cy="52" r="3.5" fill="#4ea1ff"/>`)
	p.label(22, 26, 76, m.vendor, m.name, "#1b2330", "#4a5466")
	p.portGrid(max(m.copper, 1), m.sfp, 1, 60-float64(max(m.copper, 1)+m.sfp)*8, 60+float64(max(m.copper, 1)+m.sfp)*8, h-9, h-9)
	return p
}

// meshModel: a mesh Wi-Fi unit (tall rounded box), its ports at the bottom.
func meshModel(m model) *pic {
	n := max(m.copper+m.sfp, 1)
	w := math.Max(70, 20+float64(n)*16)
	h := 120.0
	p := newPic(w, h)
	p.add(`<rect x="2" y="2" width="%g" height="%g" rx="16" fill="url(#l)" stroke="#9aa5b5"/>`, r1(w-4), h-4)
	p.label(10, 22, w-20, m.vendor, m.name, "#1b2330", "#4a5466")
	p.add(`<circle cx="%g" cy="62" r="4" fill="#4ea1ff"/>`, r1(w/2))
	p.portGrid(m.copper, m.sfp, 1, 10, w-10, h-18, h-18)
	return p
}

// deviceKind maps a catalog kind to the console's device kinds: storage
// systems are servers there.
func deviceKind(k string) string {
	if k == "storage" {
		return "server"
	}
	return k
}

// cameraModel: IP cameras — bullet, dome, turret (eyeball) or PTZ — with
// the model written below and the network port at the bottom.
func cameraModel(m model) *pic {
	w, h := 120.0, 110.0
	p := newPic(w, h)
	light, dark := "#1b2330", "#4a5466"
	switch m.form {
	case "bullet":
		p.add(`<rect x="44" y="6" width="32" height="12" rx="3" fill="url(#l)" stroke="#9aa5b5"/><rect x="56" y="16" width="8" height="14" fill="#c4cad3"/>`)
		p.add(`<rect x="10" y="28" width="98" height="30" rx="10" fill="url(#l)" stroke="#9aa5b5"/><rect x="92" y="30" width="22" height="26" rx="6" fill="#e6ebf2" stroke="#9aa5b5"/>`)
		p.add(`<circle cx="100" cy="43" r="9" fill="#0c0f14"/><circle cx="100" cy="43" r="4" fill="#1f3a5f"/><circle cx="97" cy="40" r="1.5" fill="#9cc7ff"/>`)
	case "dome":
		p.add(`<ellipse cx="60" cy="40" rx="48" ry="12" fill="url(#l)" stroke="#9aa5b5"/><path d="M24 40a36 30 0 0 0 72 0Z" fill="#1b2330" opacity=".85"/>`)
		p.add(`<circle cx="60" cy="52" r="9" fill="#0c0f14"/><circle cx="60" cy="52" r="4" fill="#1f3a5f"/><circle cx="57" cy="49" r="1.5" fill="#9cc7ff"/>`)
	case "turret":
		p.add(`<ellipse cx="60" cy="18" rx="44" ry="10" fill="url(#l)" stroke="#9aa5b5"/><circle cx="60" cy="42" r="24" fill="url(#l)" stroke="#9aa5b5"/>`)
		p.add(`<circle cx="60" cy="44" r="13" fill="#0c0f14"/><circle cx="60" cy="44" r="6" fill="#1f3a5f"/><circle cx="56" cy="40" r="2" fill="#9cc7ff"/>`)
	default: // ptz
		p.add(`<rect x="38" y="4" width="44" height="10" rx="3" fill="url(#l)" stroke="#9aa5b5"/><rect x="54" y="12" width="12" height="10" fill="#c4cad3"/>`)
		p.add(`<rect x="30" y="20" width="60" height="16" rx="6" fill="url(#l)" stroke="#9aa5b5"/><circle cx="60" cy="50" r="22" fill="#1b2330" stroke="#9aa5b5"/>`)
		p.add(`<circle cx="60" cy="52" r="10" fill="#0c0f14"/><circle cx="60" cy="52" r="5" fill="#1f3a5f"/><circle cx="57" cy="49" r="1.6" fill="#9cc7ff"/>`)
	}
	if a := vendorAccent[m.vendor]; a != "" {
		p.add(`<rect x="30" y="74" width="60" height="2" fill="%s"/>`, a)
	}
	p.label(8, 84, w-16, m.vendor, m.name, light, dark)
	p.add(`<rect x="1" y="78" width="%g" height="31" rx="4" fill="none"/>`, w-2)
	p.ports = append(p.ports, [2]float64{60, 106})
	return p
}

// antennaModel: outdoor wireless — a dish for point to point, an omni
// rod, a sector or a flat panel — on a mast, its port at the foot.
func antennaModel(m model) *pic {
	w, h := 100.0, 150.0
	p := newPic(w, h)
	p.add(`<rect x="47" y="60" width="6" height="88" fill="#59616e"/>`)
	switch m.form {
	case "dish":
		p.add(`<ellipse cx="50" cy="44" rx="40" ry="40" fill="url(#l)" stroke="#9aa5b5"/><ellipse cx="50" cy="44" rx="28" ry="28" fill="#e2e7ee"/><rect x="43" y="36" width="14" height="16" rx="3" fill="#59616e"/>`)
	case "omni":
		p.add(`<rect x="42" y="4" width="16" height="76" rx="8" fill="url(#l)" stroke="#9aa5b5"/><path d="M30 24a28 28 0 0 0 0 30M70 24a28 28 0 0 1 0 30M22 18a38 38 0 0 0 0 42M78 18a38 38 0 0 1 0 42" stroke="#4ea1ff" stroke-width="2" fill="none" stroke-linecap="round"/>`)
	case "sector":
		p.add(`<rect x="34" y="4" width="32" height="78" rx="6" fill="url(#l)" stroke="#9aa5b5"/><path d="M70 30a24 24 0 0 1 0 26M78 24a34 34 0 0 1 0 38" stroke="#4ea1ff" stroke-width="2" fill="none" stroke-linecap="round"/>`)
	default: // panel
		p.add(`<rect x="22" y="10" width="56" height="64" rx="8" fill="url(#l)" stroke="#9aa5b5"/><circle cx="50" cy="42" r="4" fill="#4ea1ff"/>`)
	}
	p.add(`<rect x="2" y="96" width="%g" height="34" rx="4" fill="url(#m)" stroke="#59616e"/>`, w-4)
	p.label(8, 106, w-16, m.vendor, m.name, "#e6ebf2", "#9aa5b5")
	p.portGrid(max(m.copper, 1), m.sfp, 1, 50-float64(max(m.copper, 1)+m.sfp)*8, 50+float64(max(m.copper, 1)+m.sfp)*8, 140, 140)
	return p
}

// upsTowerModel: a tower UPS with its display, power button and, if it has
// one, the network card port.
func upsTowerModel(m model) *pic {
	w, h := 90.0, 160.0
	p := newPic(w, h)
	p.add(`<rect x="2" y="2" width="%g" height="%g" rx="6" fill="url(#m)" stroke="#59616e"/>`, w-4, h-4)
	if a := vendorAccent[m.vendor]; a != "" {
		p.add(`<rect x="8" y="8" width="%g" height="3" fill="%s"/>`, w-16, a)
	}
	p.label(10, 22, w-20, m.vendor, m.name, "#e6ebf2", "#9aa5b5")
	p.add(`<rect x="14" y="44" width="%g" height="22" rx="2" fill="url(#s)"/>`, w-28)
	p.text(20, 59, 8, "#7fe0a6", "100%")
	p.add(`<circle cx="%g" cy="84" r="7" fill="#1f2530" stroke="#36d27a" stroke-width="2"/>`, w/2)
	for i := 0; i < 5; i++ {
		p.add(`<rect x="14" y="%g" width="%g" height="3" rx="1.5" fill="#2a303b"/>`, 100+float64(i)*7, w-28)
	}
	if m.copper > 0 {
		p.mport(w/2, h-16, 12, true)
	}
	return p
}

// printerModel: an office printer or multifunction device (with a scanner
// lid when it has "MFP" or a multifunction name).
func printerModel(m model) *pic {
	w, h := 130.0, 110.0
	p := newPic(w, h)
	mfp := m.bays > 0 // bays: 1 = scanner on top
	top := 4.0
	if mfp {
		p.add(`<rect x="10" y="4" width="110" height="16" rx="3" fill="url(#l)" stroke="#9aa5b5"/>`)
		top = 20
	}
	p.add(`<rect x="4" y="%g" width="122" height="%g" rx="6" fill="url(#l)" stroke="#9aa5b5"/>`, top, 80-top)
	p.add(`<rect x="86" y="%g" width="30" height="12" rx="2" fill="url(#s)"/><rect x="18" y="62" width="94" height="8" rx="2" fill="#c4cad3"/>`, top+6)
	if a := vendorAccent[m.vendor]; a != "" {
		p.add(`<rect x="10" y="76" width="110" height="2" fill="%s"/>`, a)
	}
	p.label(10, top+12, 72, m.vendor, m.name, "#1b2330", "#4a5466")
	p.add(`<rect x="2" y="82" width="126" height="26" rx="4" fill="url(#m)"/>`)
	p.mport(w/2, 95, 12, true)
	return p
}

// phoneModel: a desk IP phone with its display; LAN and PC ports below.
func phoneModel(m model) *pic {
	w, h := 110.0, 120.0
	p := newPic(w, h)
	p.add(`<rect x="4" y="4" width="102" height="82" rx="10" fill="url(#m)" stroke="#59616e"/><rect x="8" y="8" width="20" height="74" rx="8" fill="#2a303b"/>`)
	p.add(`<rect x="34" y="10" width="66" height="30" rx="3" fill="url(#s)"/>`)
	for i := 0; i < 9; i++ {
		p.add(`<rect x="%g" y="%g" width="14" height="8" rx="2" fill="#3a424f"/>`, 40+float64(i%3)*20, 46+float64(i/3)*12)
	}
	p.add(`<rect x="2" y="88" width="106" height="30" rx="4" fill="url(#m)"/>`)
	p.label(6, 96, 98, m.vendor, m.name, "#e6ebf2", "#9aa5b5")
	p.portGrid(max(m.copper, 1), 0, 1, 70, 104, 112, 112)
	return p
}

// miniModel: a mini PC or single-board computer, ports on the front.
func miniModel(m model) *pic {
	n := max(m.copper, 1)
	w := math.Max(110, 60+float64(n)*16)
	p := newPic(w, 70)
	p.add(`<rect x="2" y="2" width="%g" height="66" rx="10" fill="url(#m)" stroke="#59616e"/>`, w-4)
	if a := vendorAccent[m.vendor]; a != "" {
		p.add(`<rect x="10" y="60" width="%g" height="2" fill="%s"/>`, w-20, a)
	}
	p.label(10, 14, w-20, m.vendor, m.name, "#e6ebf2", "#9aa5b5")
	p.add(`<circle cx="%g" cy="46" r="4" fill="#1f2530" stroke="#4ea1ff" stroke-width="1.3"/>`, w-16)
	p.portGrid(n, m.sfp, 1, 8, w-30, 46, 46)
	return p
}
