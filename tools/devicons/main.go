// Command devicons draws WatchZit's built-in device pictures (own
// drawings, no third-party art) into internal/web/static/devices: one SVG
// per device type plus index.json with each picture's size, rack units and
// the position of every port, so map links can end in the right port.
//
//	go run ./tools/devicons
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Icon is one picture in index.json.
type Icon struct {
	Name   string       `json:"name"`
	Label  string       `json:"label"`
	Cat    string       `json:"cat"`              // network, server, rack, end, wireless, security, power, logical
	Kind   string       `json:"kind"`             // the device kind it suits
	Vendor string       `json:"vendor,omitempty"` // catalog models: the manufacturer
	W      float64      `json:"w"`                // picture size (viewBox)
	H      float64      `json:"h"`
	U      int          `json:"u,omitempty"`     // rack units (0 = not rack-mounted)
	Show   float64      `json:"show"`            // default width on the map
	Ports  [][2]float64 `json:"ports,omitempty"` // port centres, port 1 first
	Fiber  int          `json:"fiber,omitempty"` // the last n ports are SFP cages
}

// rack front panels: 19" is 482.6 mm wide, 1U is 44.45 mm high
const (
	rackW = 482.0
	uH    = 44.0
)

type pic struct {
	b     strings.Builder
	w, h  float64
	ports [][2]float64
}

func newPic(w, h float64) *pic {
	p := &pic{w: w, h: h}
	fmt.Fprintf(&p.b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %g %g" width="%g" height="%g">`, w, h, w, h)
	p.add(`<defs><linearGradient id="m" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#4a5466"/><stop offset=".5" stop-color="#363e4d"/><stop offset="1" stop-color="#262c37"/></linearGradient>` +
		`<linearGradient id="l" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#f4f6f9"/><stop offset="1" stop-color="#d5dae2"/></linearGradient>` +
		`<linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#9aa5b5"/><stop offset="1" stop-color="#6b7687"/></linearGradient>` +
		`<linearGradient id="s" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#1b2a3f"/><stop offset="1" stop-color="#0d1624"/></linearGradient></defs>`)
	return p
}

func (p *pic) add(f string, a ...any) { fmt.Fprintf(&p.b, f, a...) }

func (p *pic) svg() string { return p.b.String() + "</svg>\n" }

// ---- building blocks

// chassis draws a rack front panel with mounting ears.
func (p *pic) chassis(u int, accent string) {
	h := float64(u) * uH
	p.add(`<rect x="0" y="0" width="%g" height="%g" rx="2" fill="url(#g)"/>`, rackW, h)
	for _, x := range []float64{5, rackW - 5} {
		for i := 0; i < u; i++ {
			y := float64(i)*uH + uH/2
			p.add(`<rect x="%g" y="%g" width="5" height="9" rx="2.5" fill="#2a303b"/>`, x-2.5, y-4.5)
		}
	}
	p.add(`<rect x="16" y="1.5" width="%g" height="%g" rx="2" fill="url(#m)"/>`, rackW-32, h-3)
	if accent != "" {
		p.add(`<rect x="16" y="%g" width="%g" height="2.5" fill="%s"/>`, h-4, rackW-32, accent)
	}
}

// rj45 draws an RJ45 jack centred at x,y (port anchor) with link LEDs.
func (p *pic) rj45(x, y float64, up bool) {
	p.add(`<rect x="%g" y="%g" width="14" height="11" rx="1" fill="#0c0f14" stroke="#7d8796" stroke-width=".8"/>`, x-7, y-5.5)
	if up {
		p.add(`<rect x="%g" y="%g" width="6" height="3" fill="#1f2530"/>`, x-3, y+2.5)
	} else {
		p.add(`<rect x="%g" y="%g" width="6" height="3" fill="#1f2530"/>`, x-3, y-5.5)
	}
	ly := y - 7.5
	if !up {
		ly = y + 7.5
	}
	p.add(`<circle cx="%g" cy="%g" r="1.1" fill="#36d27a"/><circle cx="%g" cy="%g" r="1.1" fill="#f2b21b"/>`, x-4.5, ly, x+4.5, ly)
	p.ports = append(p.ports, [2]float64{x, y})
}

// sfp draws an SFP cage centred at x,y.
func (p *pic) sfp(x, y float64) {
	p.add(`<rect x="%g" y="%g" width="13" height="9" rx="1" fill="#c4cad3" stroke="#7d8796" stroke-width=".8"/><rect x="%g" y="%g" width="9" height="5" fill="#1a1f27"/>`, x-6.5, y-4.5, x-4.5, y-2.5)
	p.ports = append(p.ports, [2]float64{x, y})
}

func (p *pic) led(x, y float64, c string) {
	p.add(`<circle cx="%g" cy="%g" r="1.6" fill="%s"/>`, x, y, c)
}

func (p *pic) text(x, y float64, size float64, fill, s string) {
	p.add(`<text x="%g" y="%g" font-family="Arial,Helvetica,sans-serif" font-size="%g" font-weight="700" fill="%s">%s</text>`, x, y, size, fill, s)
}

// portBlock draws n RJ45 ports in rows of `per` starting at x; ports are
// numbered column by column like on real switches (1 top, 2 bottom …).
func (p *pic) portBlock(n, rows int, x0, top float64) float64 {
	cols := (n + rows - 1) / rows
	x := x0
	for c := 0; c < cols; c++ {
		if c > 0 && c%6 == 0 {
			x += 6 // groups of 6 columns
		}
		for r := 0; r < rows; r++ {
			if c*rows+r >= n {
				break
			}
			y := top + float64(r)*15
			p.rj45(x, y, r == 0)
		}
		x += 16
	}
	return x
}

func (p *pic) bay35(x, y, w, h float64) {
	p.add(`<rect x="%g" y="%g" width="%g" height="%g" rx="1.5" fill="#20262f" stroke="#59616e" stroke-width=".8"/>`, x, y, w, h)
	p.add(`<rect x="%g" y="%g" width="%g" height="%g" rx="1" fill="#3a424f"/>`, x+3, y+h/2-2, w-14, 4.0)
	p.led(x+w-5, y+h/2, "#36d27a")
}

// ---- rack-mounted devices

func rackSwitch(n, sfps int, poe bool) *pic {
	u := 1
	p := newPic(rackW, uH)
	p.chassis(u, "#3b82f6")
	rows := 2
	if n <= 12 {
		rows = 1
	}
	top := 15.0
	if rows == 1 {
		top = 22
	}
	p.text(26, 15, 7, "#c9d2df", "SWITCH")
	if poe {
		p.text(26, 26, 6, "#f2b21b", "PoE+")
	}
	for i := 0; i < 4; i++ {
		p.led(30+float64(i)*5, 34, []string{"#36d27a", "#36d27a", "#4ea1ff", "#555"}[i])
	}
	end := p.portBlock(n, rows, 78, top)
	x := end + 14
	for i := 0; i < sfps; i++ {
		y := top
		if rows == 2 && i%2 == 1 {
			y = top + 15
		}
		p.sfp(x, y)
		if rows == 1 || i%2 == 1 {
			x += 16
		}
	}
	p.add(`<rect x="%g" y="13" width="9" height="7" rx="1" fill="#0c0f14" stroke="#4ea1ff" stroke-width=".8"/>`, rackW-34)
	return p
}

func rackRouter(name, accent string, n, sfps int) *pic {
	p := newPic(rackW, uH)
	p.chassis(1, accent)
	p.text(26, 18, 8, "#e6ebf2", name)
	p.led(28, 30, "#36d27a")
	p.led(34, 30, "#4ea1ff")
	p.add(`<rect x="110" y="12" width="70" height="20" rx="2" fill="url(#s)" stroke="#59616e"/>`)
	p.text(116, 26, 7, "#7fe0a6", "ONLINE")
	x := 210.0
	for i := 0; i < n; i++ {
		p.rj45(x, 22, true)
		x += 18
	}
	x += 8
	for i := 0; i < sfps; i++ {
		p.sfp(x, 22)
		x += 17
	}
	p.add(`<rect x="%g" y="16" width="12" height="11" rx="1" fill="#0c0f14" stroke="#c9a227" stroke-width=".8"/>`, rackW-44)
	return p
}

func server(u int, bays int) *pic {
	p := newPic(rackW, float64(u)*uH)
	p.chassis(u, "")
	h := float64(u) * uH
	// drive bays across the front
	cols := bays / u
	if u == 1 {
		cols = bays
	}
	bw := (rackW - 150) / float64(cols)
	for r := 0; r < u; r++ {
		for c := 0; c < cols; c++ {
			x := 92 + float64(c)*bw
			y := 5 + float64(r)*(h-10)/float64(u)
			p.bay35(x, y, bw-3, (h-10)/float64(u)-3)
		}
	}
	p.add(`<rect x="22" y="6" width="62" height="%g" rx="2" fill="url(#s)"/>`, h-12)
	p.text(28, 18, 7, "#c9d2df", "SERVER")
	p.add(`<circle cx="70" cy="%g" r="5" fill="#1f2530" stroke="#4ea1ff" stroke-width="1.5"/>`, h-14)
	p.led(30, h-14, "#36d27a")
	p.led(37, h-14, "#4ea1ff")
	p.add(`<rect x="%g" y="%g" width="40" height="%g" rx="2" fill="#20262f"/>`, rackW-60, 6.0, h-12)
	p.rj45(rackW-40, h/2, true)
	return p
}

func storageArray() *pic {
	p := newPic(rackW, 2*uH)
	p.chassis(2, "#8b5cf6")
	for r := 0; r < 3; r++ {
		for c := 0; c < 4; c++ {
			p.bay35(40+float64(c)*102, 6+float64(r)*25.5, 98, 23)
		}
	}
	p.rj45(rackW-30, 30, true)
	p.rj45(rackW-30, 58, true)
	return p
}

func patchPanel(n int) *pic {
	u := (n + 23) / 24
	p := newPic(rackW, float64(u)*uH)
	p.chassis(u, "")
	x0 := 40.0
	for i := 0; i < n; i++ {
		row := i / 24
		col := i % 24
		x := x0 + float64(col)*17.5 + float64(col/6)*6
		y := 22 + float64(row)*uH
		p.add(`<rect x="%g" y="%g" width="13" height="11" rx="1" fill="#0c0f14" stroke="#9aa5b5" stroke-width=".7"/>`, x-6.5, y-3)
		p.text(x-3, y-6, 5, "#e6ebf2", fmt.Sprint(i+1))
		p.ports = append(p.ports, [2]float64{x, y + 2.5})
	}
	return p
}

func pdu() *pic {
	p := newPic(rackW, uH)
	p.chassis(1, "#ef4444")
	p.text(24, 25, 8, "#e6ebf2", "PDU")
	for i := 0; i < 8; i++ {
		x := 90 + float64(i)*44
		p.add(`<rect x="%g" y="9" width="30" height="26" rx="4" fill="#15191f" stroke="#59616e"/><rect x="%g" y="17" width="3" height="9" fill="#555"/><rect x="%g" y="17" width="3" height="9" fill="#555"/>`, x, x+9, x+18)
	}
	p.add(`<rect x="%g" y="12" width="40" height="20" rx="2" fill="url(#s)"/>`, rackW-62)
	p.text(rackW-56, 26, 9, "#ff6b6b", "12.4A")
	return p
}

func upsRack() *pic {
	p := newPic(rackW, 2*uH)
	p.chassis(2, "#22c55e")
	p.add(`<rect x="40" y="16" width="110" height="52" rx="3" fill="url(#s)" stroke="#59616e"/>`)
	p.text(50, 38, 11, "#7fe0a6", "100%")
	p.text(50, 56, 8, "#7fe0a6", "ON LINE")
	for i := 0; i < 3; i++ {
		p.add(`<circle cx="%g" cy="42" r="7" fill="#20262f" stroke="#9aa5b5"/>`, 180+float64(i)*24)
	}
	for i := 0; i < 12; i++ {
		p.add(`<rect x="%g" y="10" width="6" height="68" rx="1" fill="#2a303b"/>`, 280+float64(i)*12)
	}
	return p
}

func kvm() *pic {
	p := newPic(rackW, uH)
	p.chassis(1, "")
	p.text(26, 26, 8, "#e6ebf2", "KVM")
	for i := 0; i < 8; i++ {
		x := 80 + float64(i)*36
		p.add(`<rect x="%g" y="12" width="26" height="20" rx="2" fill="#20262f" stroke="#59616e"/>`, x)
		p.led(x+13, 22, "#4ea1ff")
	}
	return p
}

func chassisSwitch() *pic {
	u := 6
	p := newPic(rackW, float64(u)*uH)
	p.chassis(u, "#3b82f6")
	p.text(26, 20, 9, "#e6ebf2", "CORE")
	for card := 0; card < 4; card++ {
		y := 30 + float64(card)*56
		p.add(`<rect x="24" y="%g" width="%g" height="50" rx="2" fill="#2b313c" stroke="#59616e"/>`, y, rackW-48)
		x := 60.0
		for i := 0; i < 24; i++ {
			if i == 12 {
				x += 10
			}
			yy := y + 17
			if i%2 == 1 {
				yy = y + 33
				p.sfp(x, yy)
				x += 16
			} else {
				p.sfp(x, yy)
			}
		}
	}
	return p
}

// ---- other devices (drawn in perspective-free front views)

func desktopSwitch(n int) *pic {
	w := 60 + float64(n)*20
	p := newPic(w, 46)
	p.add(`<rect x="1" y="6" width="%g" height="34" rx="5" fill="url(#m)"/>`, w-2)
	p.add(`<rect x="1" y="36" width="%g" height="4" rx="2" fill="#1b2029"/>`, w-2)
	p.led(12, 18, "#36d27a")
	p.text(8, 32, 6, "#c9d2df", "SW")
	x := 40.0
	for i := 0; i < n; i++ {
		p.rj45(x, 23, true)
		x += 20
	}
	return p
}

func soho(antennas int, wifi bool) *pic {
	p := newPic(160, 80)
	if antennas == 0 {
		// no antennas: the box alone, without the empty space above it
		p = newPic(160, 44)
		p.add(`<g transform="translate(0 -36)">`)
		defer func() {
			p.add(`</g>`)
			for i := range p.ports {
				p.ports[i][1] -= 36
			}
		}()
	}
	for i := 0; i < antennas; i++ {
		x := 30 + float64(i)*(100/float64(max(1, antennas-1)))
		if antennas == 1 {
			x = 80
		}
		p.add(`<rect x="%g" y="2" width="6" height="44" rx="3" fill="#262c37"/>`, x-3)
	}
	p.add(`<rect x="4" y="40" width="152" height="36" rx="8" fill="url(#m)"/>`)
	if wifi {
		p.add(`<path d="M18 56a10 10 0 0 1 14 0M21 59a6 6 0 0 1 8 0" stroke="#4ea1ff" stroke-width="2" fill="none" stroke-linecap="round"/><circle cx="25" cy="62" r="1.5" fill="#4ea1ff"/>`)
	}
	x := 58.0
	for i := 0; i < 5; i++ {
		p.rj45(x, 58, true)
		if i == 0 {
			p.add(`<rect x="%g" y="%g" width="14" height="2" fill="#4ea1ff"/>`, x-7, 67.0)
			x += 4
		}
		x += 18
	}
	return p
}

func tower(label string, accent string) *pic {
	p := newPic(70, 130)
	p.add(`<rect x="6" y="4" width="58" height="122" rx="5" fill="url(#m)"/>`)
	p.add(`<rect x="12" y="12" width="46" height="10" rx="2" fill="#20262f"/><rect x="12" y="26" width="46" height="10" rx="2" fill="#20262f"/>`)
	for i := 0; i < 7; i++ {
		p.add(`<rect x="14" y="%g" width="42" height="3" rx="1.5" fill="#2a303b"/>`, 48+float64(i)*7)
	}
	p.add(`<circle cx="35" cy="108" r="6" fill="#1f2530" stroke="%s" stroke-width="2"/>`, accent)
	p.text(20, 124, 6, "#c9d2df", label)
	p.ports = append(p.ports, [2]float64{35, 126})
	return p
}

func monitorPic(withTower, laptop bool) *pic {
	if laptop {
		p := newPic(130, 86)
		p.add(`<rect x="18" y="4" width="94" height="62" rx="4" fill="#262c37"/><rect x="23" y="9" width="84" height="52" fill="url(#s)"/>`)
		p.add(`<path d="M4 70h122l-8 12H12z" fill="url(#g)"/><rect x="52" y="71" width="26" height="3" rx="1.5" fill="#59616e"/>`)
		p.add(`<path d="M30 30l12-10 12 8 14-14" stroke="#4ea1ff" stroke-width="2" fill="none"/>`)
		p.ports = append(p.ports, [2]float64{65, 80})
		return p
	}
	w := 130.0
	if withTower {
		w = 170
	}
	p := newPic(w, 110)
	p.add(`<rect x="4" y="4" width="122" height="78" rx="4" fill="#262c37"/><rect x="9" y="9" width="112" height="66" fill="url(#s)"/>`)
	p.add(`<path d="M18 50l16-14 14 10 18-20 16 12" stroke="#4ea1ff" stroke-width="2" fill="none"/>`)
	p.add(`<path d="M56 82h18l4 18H52z" fill="#59616e"/><rect x="40" y="98" width="50" height="6" rx="3" fill="#59616e"/>`)
	if withTower {
		p.add(`<rect x="134" y="26" width="32" height="80" rx="4" fill="url(#m)"/><circle cx="150" cy="94" r="4" fill="#1f2530" stroke="#36d27a" stroke-width="1.5"/>`)
		p.ports = append(p.ports, [2]float64{150, 106})
	} else {
		p.ports = append(p.ports, [2]float64{65, 104})
	}
	return p
}

func printer(mfp bool) *pic {
	h := 90.0
	if mfp {
		h = 120
	}
	p := newPic(130, h)
	top := 0.0
	if mfp {
		p.add(`<rect x="10" y="4" width="110" height="26" rx="4" fill="url(#l)" stroke="#9aa5b5"/>`)
		top = 26
	}
	p.add(`<rect x="22" y="%g" width="86" height="14" fill="#f4f6f9" stroke="#c4cad3"/>`, top+2)
	p.add(`<rect x="6" y="%g" width="118" height="54" rx="6" fill="url(#l)" stroke="#9aa5b5"/>`, top+14)
	p.add(`<rect x="20" y="%g" width="90" height="8" rx="2" fill="#3a424f"/>`, top+52)
	p.add(`<rect x="90" y="%g" width="24" height="12" rx="2" fill="url(#s)"/>`, top+22)
	p.led(18, top+26, "#36d27a")
	p.add(`<rect x="6" y="%g" width="118" height="12" rx="3" fill="#c4cad3" stroke="#9aa5b5"/>`, top+70)
	p.ports = append(p.ports, [2]float64{65, top + 82})
	return p
}

func ipPhone() *pic {
	p := newPic(90, 90)
	p.add(`<path d="M8 20h74l6 62H2z" fill="url(#m)"/><rect x="12" y="4" width="18" height="70" rx="8" fill="#262c37"/>`)
	p.add(`<rect x="38" y="26" width="40" height="22" rx="2" fill="url(#s)"/>`)
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			p.add(`<rect x="%g" y="%g" width="10" height="6" rx="2" fill="#9aa5b5"/>`, 40+float64(c)*13, 54+float64(r)*9)
		}
	}
	p.ports = append(p.ports, [2]float64{45, 84})
	return p
}

func handheld(tablet bool) *pic {
	w, h := 50.0, 90.0
	if tablet {
		w, h = 90, 120
	}
	p := newPic(w, h)
	p.add(`<rect x="2" y="2" width="%g" height="%g" rx="8" fill="#262c37"/><rect x="6" y="10" width="%g" height="%g" rx="2" fill="url(#s)"/>`, w-4, h-4, w-12, h-22)
	p.add(`<rect x="%g" y="%g" width="14" height="3" rx="1.5" fill="#59616e"/>`, w/2-7, h-9)
	return p
}

func camera(dome bool) *pic {
	p := newPic(110, 80)
	if dome {
		p.add(`<rect x="10" y="6" width="90" height="12" rx="4" fill="url(#l)" stroke="#9aa5b5"/><path d="M20 18h70a35 35 0 0 1-70 0z" fill="#1f2530" stroke="#9aa5b5"/>`)
		p.add(`<circle cx="55" cy="34" r="12" fill="#0c0f14" stroke="#4ea1ff" stroke-width="2"/><circle cx="51" cy="30" r="3" fill="#4ea1ff" opacity=".6"/>`)
		p.ports = append(p.ports, [2]float64{55, 8})
		return p
	}
	p.add(`<rect x="8" y="10" width="80" height="34" rx="10" fill="url(#l)" stroke="#9aa5b5"/><rect x="6" y="6" width="84" height="10" rx="4" fill="#c4cad3"/>`)
	p.add(`<circle cx="80" cy="27" r="11" fill="#0c0f14" stroke="#4ea1ff" stroke-width="2"/><circle cx="77" cy="24" r="3" fill="#4ea1ff" opacity=".6"/>`)
	p.add(`<path d="M40 44v14h-14v10h40v-10H52V44z" fill="#9aa5b5"/>`)
	p.ports = append(p.ports, [2]float64{46, 68})
	return p
}

func apCeiling() *pic {
	p := newPic(110, 70)
	p.add(`<ellipse cx="55" cy="40" rx="50" ry="22" fill="url(#l)" stroke="#9aa5b5"/><ellipse cx="55" cy="36" rx="44" ry="16" fill="#f8fafc"/>`)
	p.add(`<circle cx="55" cy="36" r="4" fill="#4ea1ff"/><path d="M38 20a24 24 0 0 1 34 0M44 26a14 14 0 0 1 22 0" stroke="#4ea1ff" stroke-width="2.5" fill="none" stroke-linecap="round"/>`)
	p.ports = append(p.ports, [2]float64{55, 62})
	return p
}

func apWall() *pic {
	p := newPic(70, 100)
	p.add(`<rect x="6" y="6" width="58" height="88" rx="10" fill="url(#l)" stroke="#9aa5b5"/><circle cx="35" cy="34" r="4" fill="#4ea1ff"/>`)
	p.add(`<path d="M20 22a22 22 0 0 1 30 0M25 28a12 12 0 0 1 20 0" stroke="#4ea1ff" stroke-width="2.5" fill="none" stroke-linecap="round"/>`)
	p.rj45(35, 78, true)
	return p
}

func ptp() *pic {
	p := newPic(90, 120)
	p.add(`<rect x="42" y="40" width="6" height="78" fill="#59616e"/><ellipse cx="45" cy="40" rx="34" ry="36" fill="url(#l)" stroke="#9aa5b5"/>`)
	p.add(`<ellipse cx="45" cy="40" rx="22" ry="24" fill="#e2e7ee"/><rect x="38" y="32" width="14" height="16" rx="3" fill="#59616e"/>`)
	p.ports = append(p.ports, [2]float64{45, 112})
	return p
}

func boxDevice(label, accent string, n int, sfps int) *pic {
	w := 70 + float64(n+sfps)*18
	p := newPic(w, 56)
	p.add(`<rect x="2" y="8" width="%g" height="40" rx="6" fill="url(#m)"/>`, w-4)
	p.add(`<rect x="2" y="44" width="%g" height="4" rx="2" fill="%s"/>`, w-4, accent)
	p.text(10, 26, 7, "#e6ebf2", label)
	p.led(12, 36, "#36d27a")
	p.led(19, 36, "#4ea1ff")
	x := 50.0
	for i := 0; i < n; i++ {
		p.rj45(x, 27, true)
		x += 18
	}
	for i := 0; i < sfps; i++ {
		p.sfp(x, 27)
		x += 18
	}
	return p
}

func nasDesktop(bays int) *pic {
	w := 30 + float64(bays)*26
	p := newPic(w, 110)
	p.add(`<rect x="2" y="2" width="%g" height="106" rx="8" fill="url(#m)"/>`, w-4)
	for i := 0; i < bays; i++ {
		x := 14 + float64(i)*26
		p.add(`<rect x="%g" y="12" width="22" height="78" rx="2" fill="#20262f" stroke="#59616e"/>`, x)
		p.led(x+11, 84, "#36d27a")
	}
	p.led(14, 100, "#4ea1ff")
	p.ports = append(p.ports, [2]float64{w / 2, 106})
	return p
}

func upsTower() *pic {
	p := newPic(70, 120)
	p.add(`<rect x="4" y="4" width="62" height="112" rx="6" fill="url(#m)"/><rect x="12" y="14" width="46" height="26" rx="2" fill="url(#s)"/>`)
	p.text(16, 31, 9, "#7fe0a6", "100%")
	p.add(`<circle cx="35" cy="56" r="7" fill="#1f2530" stroke="#36d27a" stroke-width="2"/>`)
	for i := 0; i < 6; i++ {
		p.add(`<rect x="14" y="%g" width="42" height="3" rx="1.5" fill="#2a303b"/>`, 74+float64(i)*6)
	}
	return p
}

func cloud(label string) *pic {
	p := newPic(140, 90)
	p.add(`<path d="M36 78h70a26 26 0 0 0 2-52 32 32 0 0 0-60-6 24 24 0 0 0-12 58z" fill="url(#l)" stroke="#9aa5b5" stroke-width="2"/>`)
	p.text(70-float64(len(label))*3.6, 60, 12, "#3a424f", label)
	return p
}

func building() *pic {
	p := newPic(110, 110)
	p.add(`<rect x="14" y="20" width="60" height="86" fill="url(#l)" stroke="#9aa5b5"/><rect x="74" y="50" width="26" height="56" fill="#d5dae2" stroke="#9aa5b5"/>`)
	for r := 0; r < 5; r++ {
		for c := 0; c < 3; c++ {
			p.add(`<rect x="%g" y="%g" width="12" height="10" fill="#4ea1ff" opacity=".7"/>`, 22+float64(c)*17, 30+float64(r)*15)
		}
	}
	p.add(`<rect x="36" y="88" width="16" height="18" fill="#59616e"/>`)
	return p
}

func database() *pic {
	p := newPic(90, 110)
	for i := 2; i >= 0; i-- {
		y := 20 + float64(i)*28
		p.add(`<path d="M8 %gv28a37 12 0 0 0 74 0v-28" fill="url(#m)"/><ellipse cx="45" cy="%g" rx="37" ry="12" fill="#4a5466" stroke="#262c37"/>`, y, y)
	}
	p.led(70, 104-24, "#36d27a")
	return p
}

func vm() *pic {
	p := newPic(110, 90)
	p.add(`<rect x="20" y="4" width="86" height="60" rx="6" fill="none" stroke="#9aa5b5" stroke-width="3" stroke-dasharray="8 5"/><rect x="4" y="22" width="86" height="62" rx="6" fill="url(#m)"/>`)
	p.text(24, 60, 16, "#e6ebf2", "VM")
	return p
}

func hypervisor() *pic {
	p := server(2, 8)
	p.add(`<rect x="300" y="60" width="0" height="0"/>`)
	return p
}

func lock(label string) *pic {
	p := newPic(100, 100)
	p.add(`<path d="M30 46V32a20 20 0 0 1 40 0v14" stroke="#59616e" stroke-width="8" fill="none"/><rect x="18" y="44" width="64" height="52" rx="8" fill="url(#m)"/>`)
	p.add(`<circle cx="50" cy="66" r="7" fill="#f2b21b"/><rect x="47" y="68" width="6" height="16" rx="2" fill="#f2b21b"/>`)
	p.text(50-float64(len(label))*3, 92, 9, "#e6ebf2", label)
	return p
}

func users(group bool) *pic {
	p := newPic(100, 100)
	if group {
		p.add(`<circle cx="70" cy="34" r="14" fill="#9aa5b5"/><path d="M48 92a22 26 0 0 1 44 0z" fill="#9aa5b5"/>`)
	}
	p.add(`<circle cx="44" cy="32" r="18" fill="url(#m)"/><path d="M12 96a32 34 0 0 1 64 0z" fill="url(#m)"/>`)
	return p
}

func tv() *pic {
	p := newPic(150, 100)
	p.add(`<rect x="2" y="2" width="146" height="86" rx="4" fill="#262c37"/><rect x="7" y="7" width="136" height="76" fill="url(#s)"/>`)
	p.add(`<path d="M20 64l20-18 16 12 22-26 18 14 24-10" stroke="#36d27a" stroke-width="2.5" fill="none"/><rect x="60" y="88" width="30" height="10" fill="#59616e"/>`)
	p.ports = append(p.ports, [2]float64{75, 98})
	return p
}

func iot() *pic {
	p := newPic(70, 70)
	p.add(`<rect x="8" y="8" width="54" height="54" rx="14" fill="url(#l)" stroke="#9aa5b5"/><circle cx="35" cy="35" r="10" fill="none" stroke="#4ea1ff" stroke-width="3"/><circle cx="35" cy="35" r="3" fill="#4ea1ff"/>`)
	return p
}

func intercom() *pic {
	p := newPic(60, 110)
	p.add(`<rect x="4" y="4" width="52" height="102" rx="6" fill="url(#m)"/><circle cx="30" cy="26" r="9" fill="#0c0f14" stroke="#4ea1ff" stroke-width="2"/>`)
	for i := 0; i < 5; i++ {
		p.add(`<rect x="14" y="%g" width="32" height="3" rx="1.5" fill="#2a303b"/>`, 46+float64(i)*7)
	}
	p.add(`<circle cx="30" cy="90" r="7" fill="#9aa5b5"/>`)
	return p
}

func main() {
	out := filepath.Join("internal", "web", "static", "devices")
	if err := os.MkdirAll(out, 0o750); err != nil {
		panic(err)
	}
	var icons []Icon
	addRack := func(name, label, cat, kind string, p *pic, fiber int) {
		u := int(p.h / uH)
		icons = append(icons, Icon{Name: name, Label: label, Cat: cat, Kind: kind, W: p.w, H: p.h, U: u, Show: 240, Ports: p.ports, Fiber: fiber})
		write(out, name, p)
	}
	add := func(name, label, cat, kind string, p *pic, show float64) {
		icons = append(icons, Icon{Name: name, Label: label, Cat: cat, Kind: kind, W: p.w, H: p.h, Show: show, Ports: p.ports})
		write(out, name, p)
	}
	// network, rack-mounted
	for _, n := range []int{16, 24, 48} {
		addRack(fmt.Sprintf("switch-%d", n), fmt.Sprintf("Switch %d ports", n), "network", "switch", rackSwitch(n, 0, false), 0)
		addRack(fmt.Sprintf("switch-%d-sfp", n), fmt.Sprintf("Switch %d ports + 4 SFP", n), "network", "switch", rackSwitch(n, 4, false), 4)
	}
	addRack("switch-24-poe", "PoE switch 24 ports + 4 SFP", "network", "switch", rackSwitch(24, 4, true), 4)
	addRack("switch-48-poe", "PoE switch 48 ports + 4 SFP", "network", "switch", rackSwitch(48, 4, true), 4)
	addRack("switch-sfp-24", "Fibre switch 24 SFP", "network", "switch", func() *pic {
		p := newPic(rackW, uH)
		p.chassis(1, "#f59e0b")
		p.text(26, 18, 7, "#c9d2df", "SFP+")
		x := 80.0
		for i := 0; i < 24; i++ {
			if i > 0 && i%6 == 0 {
				x += 8
			}
			y := 15.0
			if i%2 == 1 {
				y = 30
			}
			p.sfp(x, y)
			if i%2 == 1 {
				x += 17
			}
		}
		return p
	}(), 24)
	addRack("core-switch", "Core switch chassis (6U)", "network", "switch", chassisSwitch(), 96)
	addRack("router-rack", "Router 1U", "network", "router", rackRouter("ROUTER", "#3b82f6", 8, 2), 2)
	addRack("router-edge", "Edge router 1U", "network", "router", rackRouter("EDGE", "#06b6d4", 5, 1), 1)
	addRack("firewall-rack", "Firewall 1U", "security", "firewall", rackRouter("FIREWALL", "#ef4444", 8, 2), 2)
	addRack("loadbalancer", "Load balancer 1U", "network", "other", rackRouter("BALANCER", "#8b5cf6", 4, 2), 2)
	addRack("console-server", "Console server 1U", "network", "other", rackRouter("CONSOLE", "#c9a227", 8, 0), 0)
	addRack("patch-24", "Patch panel 24", "rack", "other", patchPanel(24), 0)
	addRack("patch-48", "Patch panel 48 (2U)", "rack", "other", patchPanel(48), 0)
	addRack("server-1u", "Server 1U", "server", "server", server(1, 8), 0)
	addRack("server-2u", "Server 2U", "server", "server", server(2, 12), 0)
	addRack("server-4u", "Server 4U", "server", "server", server(4, 24), 0)
	addRack("hypervisor", "Virtualisation host 2U", "server", "server", hypervisor(), 0)
	addRack("storage-2u", "Storage array 2U", "server", "server", storageArray(), 0)
	addRack("nas-rack", "NAS 1U", "server", "server", server(1, 4), 0)
	addRack("pdu", "PDU", "power", "other", pdu(), 0)
	addRack("ups-rack", "UPS 2U", "power", "ups", upsRack(), 0)
	addRack("kvm", "KVM switch", "rack", "other", kvm(), 0)
	addRack("blank-1u", "Blank panel 1U", "rack", "other", func() *pic { p := newPic(rackW, uH); p.chassis(1, ""); return p }(), 0)
	// network, desktop and wall
	for _, n := range []int{5, 8} {
		add(fmt.Sprintf("switch-desk-%d", n), fmt.Sprintf("Desktop switch %d ports", n), "network", "switch", desktopSwitch(n), 50+float64(n)*12)
	}
	add("router-wifi", "Wi-Fi router", "wireless", "router", soho(3, true), 110)
	add("router-soho", "Small office router", "network", "router", soho(0, false), 110)
	add("router-box", "Router (desktop)", "network", "router", boxDevice("ROUTER", "#3b82f6", 5, 1), 140)
	add("firewall-box", "Firewall (desktop)", "security", "firewall", boxDevice("FIREWALL", "#ef4444", 6, 0), 140)
	add("modem", "Modem / ONT", "network", "router", boxDevice("ONT", "#22c55e", 2, 1), 100)
	add("media-converter", "Media converter", "network", "other", boxDevice("MC", "#f59e0b", 1, 1), 80)
	add("voip-gateway", "VoIP gateway", "network", "other", boxDevice("VOIP", "#8b5cf6", 4, 0), 120)
	add("nvr", "NVR (video recorder)", "security", "camera", boxDevice("NVR", "#ef4444", 4, 0), 120)
	add("ap-ceiling", "Access point (ceiling)", "wireless", "accesspoint", apCeiling(), 80)
	add("ap-wall", "Access point (wall)", "wireless", "accesspoint", apWall(), 50)
	add("ptp", "Wireless bridge (point to point)", "wireless", "accesspoint", ptp(), 60)
	// servers, storage, power
	add("server-tower", "Tower server", "server", "server", tower("SERVER", "#4ea1ff"), 50)
	add("nas-2", "NAS 2 bays", "server", "server", nasDesktop(2), 55)
	add("nas-4", "NAS 4 bays", "server", "server", nasDesktop(4), 75)
	add("ups-tower", "UPS (tower)", "power", "ups", upsTower(), 50)
	// end devices
	add("pc-tower", "PC", "end", "pc", tower("PC", "#36d27a"), 45)
	add("workstation", "Workstation", "end", "pc", monitorPic(true, false), 100)
	add("desktop", "Desktop / all-in-one", "end", "pc", monitorPic(false, false), 80)
	add("laptop", "Laptop", "end", "pc", monitorPic(false, true), 80)
	add("printer", "Printer", "end", "printer", printer(false), 80)
	add("mfp", "Multifunction printer", "end", "printer", printer(true), 80)
	add("ip-phone", "IP phone", "end", "other", ipPhone(), 55)
	add("smartphone", "Smartphone", "end", "other", handheld(false), 30)
	add("tablet", "Tablet", "end", "other", handheld(true), 50)
	add("tv", "Display / TV", "end", "other", tv(), 100)
	add("camera-bullet", "IP camera (bullet)", "security", "camera", camera(false), 70)
	add("camera-dome", "IP camera (dome)", "security", "camera", camera(true), 70)
	add("intercom", "Door intercom", "security", "other", intercom(), 35)
	add("iot", "IoT sensor", "end", "other", iot(), 40)
	// logical
	add("internet", "Internet", "logical", "other", cloud("Internet"), 110)
	add("cloud", "Cloud", "logical", "other", cloud("Cloud"), 110)
	add("vpn", "VPN", "logical", "other", lock("VPN"), 60)
	add("building", "Building / site", "logical", "other", building(), 80)
	add("database", "Database", "logical", "server", database(), 60)
	add("vm", "Virtual machine", "logical", "server", vm(), 70)
	add("user", "User", "logical", "pc", users(false), 50)
	add("users", "Users / department", "logical", "pc", users(true), 60)
	icons = append(icons, modelIcons(out)...)
	b, _ := json.Marshal(icons)
	if err := os.WriteFile(filepath.Join(out, "index.json"), append(b, '\n'), 0o600); err != nil {
		panic(err)
	}
	fmt.Println(len(icons), "pictures")
}

func write(dir, name string, p *pic) {
	if err := os.WriteFile(filepath.Join(dir, name+".svg"), []byte(p.svg()), 0o600); err != nil {
		panic(err)
	}
}
