package main

// The catalog of real device models: each is drawn by the generator from
// its form, ports and drive bays (front panel, own drawings, the model name
// as text, no logos). Port counts follow the vendors' public data sheets;
// combo ports count once.

// model is one device model of the catalog.
type model struct {
	vendor string // as shown: "Fortinet", "HPE", "MikroTik" …
	name   string // model as sold
	kind   string // router, switch, firewall, server, accesspoint
	form   string // rack, desk, tower, outdoor, wifi (desktop with antennas)
	u      int    // rack units (rack form)
	copper int    // RJ45 ports
	sfp    int    // SFP/SFP+/SFP28/QSFP cages
	bays   int    // drive bays on the front (servers)
	lff    bool   // 3.5" bays instead of 2.5"
	nic    int    // servers: network ports drawn next to the management port
}

// vendor colours: the accent line of the front panel
var vendorAccent = map[string]string{
	"Fortinet": "#da291c", "HPE": "#01a982", "MikroTik": "#9aa5b5", "Dell": "#0076ce", "Lenovo": "#e2231a",
	"Supermicro": "#1e64c8", "Cisco": "#049fd9", "Fujitsu": "#e60012", "Huawei": "#c7000b", "xFusion": "#c7000b", "Inspur": "#0055a5",
	"Synology": "#2f3b4c", "QNAP": "#1d6fb8", "Cisco Meraki": "#67b346", "HPE Aruba": "#ff8300", "Juniper": "#84b135", "Ubiquiti": "#006fff",
	"Netgear": "#5b2d90", "TP-Link": "#4acbd6", "Palo Alto": "#fa582d", "Sophos": "#005bc8", "NetApp": "#0067c5", "Pure Storage": "#fe5000", "IBM": "#0f62fe", "FS": "#c8102e",
}

func catalog() []model {
	var ms []model
	add := func(m ...model) { ms = append(ms, m...) }

	// ---- Fortinet FortiGate (and FortiWiFi)
	fg := func(name, form string, u, copper, sfp int) model {
		return model{vendor: "Fortinet", name: name, kind: "firewall", form: form, u: u, copper: copper, sfp: sfp}
	}
	add(
		fg("FortiGate 30D", "desk", 0, 5, 0), fg("FortiGate 30E", "desk", 0, 5, 0), fg("FortiGate 40F", "desk", 0, 5, 0),
		fg("FortiGate 50E", "desk", 0, 7, 0), fg("FortiGate 50G", "desk", 0, 5, 0),
		fg("FortiGate 60D", "desk", 0, 10, 0), fg("FortiGate 60E", "desk", 0, 10, 0), fg("FortiGate 60F", "desk", 0, 10, 0),
		fg("FortiGate 61E", "desk", 0, 10, 0), fg("FortiGate 61F", "desk", 0, 10, 0),
		fg("FortiGate 70F", "desk", 0, 10, 0), fg("FortiGate 70G", "desk", 0, 10, 0), fg("FortiGate 71F", "desk", 0, 10, 0),
		fg("FortiGate 80E", "desk", 0, 12, 2), fg("FortiGate 80F", "desk", 0, 8, 2), fg("FortiGate 81F", "desk", 0, 8, 2),
		fg("FortiGate 90D", "desk", 0, 16, 0), fg("FortiGate 90G", "desk", 0, 8, 2), fg("FortiGate 91G", "desk", 0, 8, 2),
		fg("FortiGate 100D", "rack", 1, 20, 2), fg("FortiGate 100E", "rack", 1, 20, 2), fg("FortiGate 100F", "rack", 1, 22, 6),
		fg("FortiGate 101E", "rack", 1, 20, 2), fg("FortiGate 101F", "rack", 1, 22, 6),
		fg("FortiGate 120G", "rack", 1, 18, 12), fg("FortiGate 121G", "rack", 1, 18, 12),
		fg("FortiGate 140E", "rack", 1, 42, 2),
		fg("FortiGate 200D", "rack", 1, 18, 2), fg("FortiGate 200E", "rack", 1, 18, 4), fg("FortiGate 200F", "rack", 1, 18, 10),
		fg("FortiGate 201E", "rack", 1, 18, 4), fg("FortiGate 201F", "rack", 1, 18, 10), fg("FortiGate 200G", "rack", 1, 18, 12),
		fg("FortiGate 300D", "rack", 1, 6, 4), fg("FortiGate 300E", "rack", 1, 18, 18), fg("FortiGate 301E", "rack", 1, 18, 18),
		fg("FortiGate 400D", "rack", 1, 18, 10), fg("FortiGate 400E", "rack", 1, 18, 16), fg("FortiGate 400F", "rack", 1, 18, 16),
		fg("FortiGate 401E", "rack", 1, 18, 16), fg("FortiGate 401F", "rack", 1, 18, 16),
		fg("FortiGate 500D", "rack", 1, 10, 10), fg("FortiGate 500E", "rack", 1, 10, 10),
		fg("FortiGate 600D", "rack", 1, 10, 10), fg("FortiGate 600E", "rack", 1, 10, 12), fg("FortiGate 600F", "rack", 1, 8, 12),
		fg("FortiGate 800D", "rack", 1, 18, 18), fg("FortiGate 900D", "rack", 1, 18, 18), fg("FortiGate 900G", "rack", 1, 8, 24),
		fg("FortiGate 1000D", "rack", 2, 18, 18), fg("FortiGate 1100E", "rack", 1, 18, 24), fg("FortiGate 1500D", "rack", 2, 18, 24),
		fg("FortiGate 1800F", "rack", 2, 18, 40), fg("FortiGate 2000E", "rack", 2, 18, 30), fg("FortiGate 2500E", "rack", 2, 18, 36),
		fg("FortiGate 2600F", "rack", 2, 18, 40), fg("FortiGate 3000D", "rack", 2, 2, 16), fg("FortiGate 3000F", "rack", 2, 2, 48),
		fg("FortiGate 3200D", "rack", 2, 2, 48), fg("FortiGate 3200F", "rack", 2, 2, 48), fg("FortiGate 3400E", "rack", 2, 2, 28),
		fg("FortiGate 3500F", "rack", 2, 2, 48), fg("FortiGate 3600E", "rack", 3, 2, 34), fg("FortiGate 3700F", "rack", 2, 2, 48),
		fg("FortiGate 4200F", "rack", 3, 2, 32), fg("FortiGate 4400F", "rack", 3, 2, 32), fg("FortiGate 4800F", "rack", 3, 2, 32),
		fg("FortiGate 6000F", "rack", 3, 4, 28), fg("FortiGate 7000F", "rack", 8, 4, 24),
		model{vendor: "Fortinet", name: "FortiWiFi 40F", kind: "firewall", form: "wifi", copper: 5},
		model{vendor: "Fortinet", name: "FortiWiFi 60E", kind: "firewall", form: "wifi", copper: 10},
		model{vendor: "Fortinet", name: "FortiWiFi 60F", kind: "firewall", form: "wifi", copper: 10},
		model{vendor: "Fortinet", name: "FortiWiFi 61F", kind: "firewall", form: "wifi", copper: 10},
		model{vendor: "Fortinet", name: "FortiWiFi 80F-2R", kind: "firewall", form: "wifi", copper: 8, sfp: 2},
	)

	// ---- HPE ProLiant G6 to Gen12
	hp := func(name string, u, bays int, lff bool, nic int) model {
		return model{vendor: "HPE", name: "ProLiant " + name, kind: "server", form: "rack", u: u, bays: bays, lff: lff, nic: nic}
	}
	hpt := func(name string, bays int) model {
		return model{vendor: "HPE", name: "ProLiant " + name, kind: "server", form: "tower", bays: bays, lff: true, nic: 2}
	}
	add(
		hp("DL120 G6", 1, 4, true, 2), hp("DL160 G6", 1, 4, true, 2), hp("DL180 G6", 2, 12, true, 2), hp("DL320 G6", 1, 4, true, 2),
		hp("DL360 G6", 1, 8, false, 4), hp("DL380 G6", 2, 8, false, 4), hp("DL385 G6", 2, 8, false, 4),
		hpt("ML110 G6", 4), hpt("ML150 G6", 6), hpt("ML330 G6", 6), hpt("ML350 G6", 8),
		hp("DL120 G7", 1, 4, true, 2), hp("DL360 G7", 1, 8, false, 4), hp("DL380 G7", 2, 8, false, 4), hp("DL385 G7", 2, 8, false, 4),
		hp("DL580 G7", 4, 8, false, 4), hpt("ML110 G7", 4), hpt("MicroServer N40L", 4), hpt("MicroServer N54L", 4),
		hp("DL320e Gen8", 1, 4, true, 2), hp("DL360e Gen8", 1, 8, false, 4), hp("DL360p Gen8", 1, 8, false, 4),
		hp("DL380e Gen8", 2, 12, true, 4), hp("DL380p Gen8", 2, 8, false, 4), hp("DL385p Gen8", 2, 8, false, 4), hp("DL560 Gen8", 2, 5, false, 4),
		hpt("ML310e Gen8", 4), hpt("ML350e Gen8", 8), hpt("ML350p Gen8", 8), hpt("MicroServer Gen8", 4),
		hp("DL20 Gen9", 1, 2, true, 2), hp("DL60 Gen9", 1, 4, true, 2), hp("DL80 Gen9", 2, 8, true, 2), hp("DL120 Gen9", 1, 8, false, 2),
		hp("DL160 Gen9", 1, 8, false, 2), hp("DL180 Gen9", 2, 12, true, 2), hp("DL360 Gen9", 1, 8, false, 4),
		hp("DL380 Gen9", 2, 8, false, 4), hp("DL380 Gen9 LFF", 2, 12, true, 4), hp("DL560 Gen9", 2, 8, false, 4), hp("DL580 Gen9", 4, 10, false, 4),
		hpt("ML30 Gen9", 4), hpt("ML110 Gen9", 4), hpt("ML150 Gen9", 8), hpt("ML350 Gen9", 8),
		hp("DL20 Gen10", 1, 2, true, 2), hp("DL160 Gen10", 1, 8, false, 2), hp("DL180 Gen10", 2, 12, true, 2), hp("DL325 Gen10", 1, 10, false, 2),
		hp("DL360 Gen10", 1, 8, false, 4), hp("DL380 Gen10", 2, 8, false, 4), hp("DL380 Gen10 LFF", 2, 12, true, 4),
		hp("DL385 Gen10", 2, 24, false, 4), hp("DL560 Gen10", 2, 8, false, 4), hp("DL580 Gen10", 4, 8, false, 4),
		hpt("ML30 Gen10", 4), hpt("ML110 Gen10", 4), hpt("ML350 Gen10", 8), hpt("MicroServer Gen10", 4),
		hp("DL20 Gen10 Plus", 1, 2, true, 2), hp("DL110 Gen10 Plus", 1, 4, false, 2), hp("DL325 Gen10 Plus", 1, 8, false, 2),
		hp("DL345 Gen10 Plus", 2, 8, true, 2), hp("DL360 Gen10 Plus", 1, 8, false, 4), hp("DL365 Gen10 Plus", 1, 10, false, 4),
		hp("DL380 Gen10 Plus", 2, 24, false, 4), hp("DL385 Gen10 Plus", 2, 24, false, 4),
		hpt("ML30 Gen10 Plus", 4), hpt("ML110 Gen10 Plus", 4), hpt("MicroServer Gen10 Plus", 4), hpt("MicroServer Gen10 Plus v2", 4),
		hp("DL20 Gen11", 1, 2, true, 2), hp("DL320 Gen11", 1, 8, false, 2), hp("DL325 Gen11", 1, 10, false, 2), hp("DL345 Gen11", 2, 12, true, 2),
		hp("DL360 Gen11", 1, 8, false, 4), hp("DL365 Gen11", 1, 10, false, 4), hp("DL380 Gen11", 2, 24, false, 4),
		hp("DL380a Gen11", 2, 8, false, 4), hp("DL385 Gen11", 2, 24, false, 4), hp("DL560 Gen11", 2, 24, false, 4), hp("DL145 Gen11", 2, 6, false, 2),
		hpt("ML30 Gen11", 4), hpt("ML110 Gen11", 4), hpt("ML350 Gen11", 8), hpt("MicroServer Gen11", 4),
		hp("DL320 Gen12", 1, 8, false, 2), hp("DL340 Gen12", 2, 12, true, 2), hp("DL360 Gen12", 1, 8, false, 4),
		hp("DL380 Gen12", 2, 24, false, 4), hp("DL380a Gen12", 2, 8, false, 4), hpt("ML350 Gen12", 8),
	)

	// ---- MikroTik
	mt := func(name, kind, form string, u, copper, sfp int) model {
		return model{vendor: "MikroTik", name: name, kind: kind, form: form, u: u, copper: copper, sfp: sfp}
	}
	add(
		mt("hEX lite (RB750r2)", "router", "desk", 0, 5, 0), mt("hEX (RB750Gr3)", "router", "desk", 0, 5, 0),
		mt("hEX refresh (E50UG)", "router", "desk", 0, 5, 0), mt("hEX S (RB760iGS)", "router", "desk", 0, 5, 1),
		mt("hEX S (E60iUGS)", "router", "desk", 0, 5, 1), mt("hEX PoE lite", "router", "desk", 0, 5, 0), mt("hEX PoE", "router", "desk", 0, 5, 1),
		mt("RB5009UG+S+IN", "router", "desk", 0, 8, 1), mt("RB5009UPr+S+IN", "router", "desk", 0, 8, 1),
		mt("L009UiGS-RM", "router", "rack", 1, 8, 1), mt("RB2011UiAS-RM", "router", "rack", 1, 10, 1),
		mt("RB3011UiAS-RM", "router", "rack", 1, 10, 1), mt("RB4011iGS+RM", "router", "rack", 1, 10, 1),
		mt("RB4011iGS+5HacQ2HnD", "router", "wifi", 0, 10, 1), mt("RB1100AHx4", "router", "rack", 1, 13, 0),
		mt("RB1100AHx4 Dude Edition", "router", "rack", 1, 13, 0),
		mt("CCR1009-7G-1C-1S+", "router", "rack", 1, 8, 2), mt("CCR1016-12G", "router", "rack", 1, 12, 0),
		mt("CCR1016-12S-1S+", "router", "rack", 1, 1, 13), mt("CCR1036-12G-4S", "router", "rack", 1, 12, 4),
		mt("CCR1036-8G-2S+", "router", "rack", 1, 8, 2), mt("CCR1072-1G-8S+", "router", "rack", 1, 1, 8),
		mt("CCR2004-16G-2S+", "router", "rack", 1, 16, 2), mt("CCR2004-1G-12S+2XS", "router", "rack", 1, 1, 14),
		mt("CCR2004-1G-2XS-PCIe", "router", "desk", 0, 1, 2), mt("CCR2116-12G-4S+", "router", "rack", 1, 13, 4),
		mt("CCR2216-1G-12XS-2XQ", "router", "rack", 1, 1, 14),
		mt("CRS106-1C-5S", "switch", "desk", 0, 1, 6), mt("CRS112-8G-4S-IN", "switch", "desk", 0, 8, 4), mt("CRS112-8P-4S-IN", "switch", "desk", 0, 8, 4),
		mt("CRS125-24G-1S-RM", "switch", "rack", 1, 24, 1), mt("CRS226-24G-2S+RM", "switch", "rack", 1, 24, 2),
		mt("CRS305-1G-4S+IN", "switch", "desk", 0, 1, 4), mt("CRS309-1G-8S+IN", "switch", "desk", 0, 1, 8),
		mt("CRS310-1G-5S-4S+IN", "switch", "desk", 0, 1, 9), mt("CRS310-8G+2S+IN", "switch", "desk", 0, 8, 2),
		mt("CRS317-1G-16S+RM", "switch", "rack", 1, 1, 16), mt("CRS318-16P-2S+OUT", "switch", "outdoor", 0, 16, 2),
		mt("CRS318-1Fi-15Fr-2S-OUT", "switch", "outdoor", 0, 16, 2),
		mt("CRS326-24G-2S+RM", "switch", "rack", 1, 24, 2), mt("CRS326-24S+2Q+RM", "switch", "rack", 1, 1, 26),
		mt("CRS326-4C+20G+2Q+RM", "switch", "rack", 1, 24, 2), mt("CRS328-24P-4S+RM", "switch", "rack", 1, 24, 4),
		mt("CRS328-4C-20S-4S+RM", "switch", "rack", 1, 4, 24), mt("CRS354-48G-4S+2Q+RM", "switch", "rack", 1, 48, 6),
		mt("CRS354-48P-4S+2Q+RM", "switch", "rack", 1, 48, 6), mt("CRS504-4XQ-IN", "switch", "desk", 0, 1, 4),
		mt("CRS510-8XS-2XQ-IN", "switch", "desk", 0, 1, 10), mt("CRS518-16XS-2XQ-RM", "switch", "rack", 1, 1, 18),
		mt("CRS520-4XS-16XQ-RM", "switch", "rack", 1, 2, 20),
		mt("CSS106-5G-1S (RB260GS)", "switch", "desk", 0, 5, 1), mt("CSS106-1G-4P-1S", "switch", "desk", 0, 5, 1),
		mt("CSS318-16G-2S+IN", "switch", "desk", 0, 16, 2), mt("CSS326-24G-2S+RM", "switch", "rack", 1, 24, 2),
		mt("CSS610-8G-2S+IN", "switch", "desk", 0, 8, 2), mt("CSS610-8P-2S+IN", "switch", "desk", 0, 8, 2),
		mt("netPower 16P", "switch", "outdoor", 0, 16, 2), mt("netPower 15FR", "switch", "outdoor", 0, 16, 2),
		mt("PowerBox", "router", "outdoor", 0, 5, 0), mt("PowerBox Pro", "router", "outdoor", 0, 5, 1),
		mt("hAP lite", "accesspoint", "wifi", 0, 4, 0), mt("hAP ac lite", "accesspoint", "wifi", 0, 5, 0),
		mt("hAP ac²", "accesspoint", "wifi", 0, 5, 0), mt("hAP ac³", "accesspoint", "wifi", 0, 5, 0),
		mt("hAP ax lite", "accesspoint", "wifi", 0, 4, 0), mt("hAP ax²", "accesspoint", "wifi", 0, 5, 0), mt("hAP ax³", "accesspoint", "wifi", 0, 5, 0),
		mt("hAP ax S", "accesspoint", "wifi", 0, 5, 1), mt("Chateau LTE12", "router", "wifi", 0, 5, 0), mt("Chateau 5G ax", "router", "wifi", 0, 5, 0),
		mt("Chateau LTE18 ax", "router", "wifi", 0, 5, 0), mt("Audience", "accesspoint", "wifi", 0, 2, 0),
		mt("cAP lite", "accesspoint", "wifi", 0, 1, 0), mt("cAP ac", "accesspoint", "wifi", 0, 2, 0), mt("cAP ax", "accesspoint", "wifi", 0, 2, 0),
		mt("wAP ac", "accesspoint", "wifi", 0, 1, 0), mt("wAP ax", "accesspoint", "wifi", 0, 2, 0), mt("wAP LTE kit", "accesspoint", "wifi", 0, 1, 0),
		mt("SXTsq 5 ac", "accesspoint", "outdoor", 0, 1, 0), mt("SXTsq 5 ax", "accesspoint", "outdoor", 0, 1, 0),
		mt("LHG 5 ac", "accesspoint", "outdoor", 0, 1, 0), mt("LHG XL 5 ax", "accesspoint", "outdoor", 0, 1, 0),
		mt("LHG LTE18 kit", "router", "outdoor", 0, 1, 0), mt("mANTBox 52 15s", "accesspoint", "outdoor", 0, 1, 1),
		mt("mANTBox ax 15s", "accesspoint", "outdoor", 0, 1, 1), mt("NetMetal ax", "accesspoint", "outdoor", 0, 1, 1),
		mt("Cube 60Pro ac", "accesspoint", "outdoor", 0, 1, 0), mt("Wireless Wire nRAY", "accesspoint", "outdoor", 0, 1, 1),
		mt("ATL 5G R16", "router", "outdoor", 0, 1, 0), mt("KNOT", "router", "desk", 0, 2, 0),
	)

	// ---- Dell PowerEdge 11G to 16G
	dl := func(name string, u, bays int, lff bool, nic int) model {
		return model{vendor: "Dell", name: "PowerEdge " + name, kind: "server", form: "rack", u: u, bays: bays, lff: lff, nic: nic}
	}
	dt := func(name string, bays int) model {
		return model{vendor: "Dell", name: "PowerEdge " + name, kind: "server", form: "tower", bays: bays, lff: true, nic: 2}
	}
	add(
		dl("R210", 1, 2, true, 2), dl("R310", 1, 4, true, 2), dl("R410", 1, 4, true, 2), dl("R510", 2, 12, true, 2),
		dl("R610", 1, 6, false, 4), dl("R710", 2, 8, false, 4), dl("R810", 2, 6, false, 4), dl("R910", 4, 16, false, 4),
		dt("T110", 4), dt("T310", 4), dt("T410", 6), dt("T610", 8), dt("T710", 8),
		dl("R220", 1, 2, true, 2), dl("R320", 1, 4, true, 2), dl("R420", 1, 4, true, 2), dl("R520", 2, 8, true, 2),
		dl("R620", 1, 10, false, 4), dl("R720", 2, 16, false, 4), dl("R720xd", 2, 24, false, 4), dl("R820", 2, 16, false, 4), dl("R920", 4, 24, false, 4),
		dt("T20", 4), dt("T320", 8), dt("T420", 8), dt("T620", 8),
		dl("R230", 1, 4, true, 2), dl("R330", 1, 4, true, 2), dl("R430", 1, 4, true, 2), dl("R530", 2, 8, true, 2),
		dl("R630", 1, 10, false, 4), dl("R730", 2, 16, false, 4), dl("R730xd", 2, 24, false, 4), dl("R830", 2, 16, false, 4), dl("R930", 4, 24, false, 4),
		dt("T30", 4), dt("T130", 4), dt("T330", 8), dt("T430", 8), dt("T630", 8),
		dl("R240", 1, 4, true, 2), dl("R340", 1, 4, true, 2), dl("R440", 1, 10, false, 2), dl("R540", 2, 12, true, 2),
		dl("R640", 1, 10, false, 4), dl("R740", 2, 16, false, 4), dl("R740xd", 2, 24, false, 4), dl("R840", 2, 24, false, 4), dl("R940", 3, 24, false, 4),
		dl("R6415", 1, 10, false, 2), dl("R7415", 2, 24, false, 2), dl("R7425", 2, 24, false, 2),
		dt("T40", 3), dt("T140", 4), dt("T340", 8), dt("T440", 8), dt("T640", 8),
		dl("R250", 1, 4, true, 2), dl("R350", 1, 4, true, 2), dl("R450", 1, 8, false, 2), dl("R550", 2, 8, true, 2),
		dl("R650", 1, 10, false, 4), dl("R650xs", 1, 10, false, 4), dl("R750", 2, 24, false, 4), dl("R750xs", 2, 16, false, 4), dl("R750xa", 2, 8, false, 4),
		dl("R6515", 1, 10, false, 2), dl("R6525", 1, 10, false, 2), dl("R7515", 2, 24, false, 2), dl("R7525", 2, 24, false, 2),
		dt("T150", 4), dt("T350", 8), dt("T550", 8),
		dl("R260", 1, 4, true, 2), dl("R360", 1, 4, true, 2), dl("R660", 1, 10, false, 4), dl("R660xs", 1, 10, false, 4),
		dl("R760", 2, 24, false, 4), dl("R760xs", 2, 16, false, 4), dl("R760xa", 2, 8, false, 4), dl("R860", 2, 24, false, 4), dl("R960", 2, 24, false, 4),
		dl("R6615", 1, 10, false, 2), dl("R6625", 1, 10, false, 2), dl("R7615", 2, 24, false, 2), dl("R7625", 2, 24, false, 2),
		dt("T160", 4), dt("T360", 8), dt("T560", 8),
		dl("R470", 1, 8, false, 2), dl("R570", 2, 8, true, 2), dl("R670", 1, 10, false, 4), dl("R770", 2, 24, false, 4),
	)

	// ---- Lenovo ThinkSystem and System x
	ln := func(name string, u, bays int, lff bool, nic int) model {
		return model{vendor: "Lenovo", name: name, kind: "server", form: "rack", u: u, bays: bays, lff: lff, nic: nic}
	}
	lt := func(name string, bays int) model {
		return model{vendor: "Lenovo", name: name, kind: "server", form: "tower", bays: bays, lff: true, nic: 2}
	}
	add(
		ln("System x3250 M6", 1, 4, true, 2), ln("System x3550 M4", 1, 8, false, 4), ln("System x3550 M5", 1, 8, false, 4),
		ln("System x3650 M4", 2, 16, false, 4), ln("System x3650 M5", 2, 24, false, 4), lt("System x3100 M5", 4), lt("System x3500 M5", 8),
		ln("ThinkSystem SR250", 1, 4, true, 2), ln("ThinkSystem SR250 V2", 1, 4, true, 2), ln("ThinkSystem SR250 V3", 1, 4, true, 2),
		ln("ThinkSystem SR530", 1, 8, false, 2), ln("ThinkSystem SR550", 2, 8, true, 2), ln("ThinkSystem SR570", 1, 10, false, 2),
		ln("ThinkSystem SR590", 2, 12, true, 2), ln("ThinkSystem SR630", 1, 10, false, 4), ln("ThinkSystem SR650", 2, 24, false, 4),
		ln("ThinkSystem SR630 V2", 1, 10, false, 4), ln("ThinkSystem SR650 V2", 2, 24, false, 4),
		ln("ThinkSystem SR630 V3", 1, 10, false, 4), ln("ThinkSystem SR650 V3", 2, 24, false, 4),
		ln("ThinkSystem SR635", 1, 10, false, 2), ln("ThinkSystem SR655", 2, 24, false, 2),
		ln("ThinkSystem SR645", 1, 10, false, 2), ln("ThinkSystem SR665", 2, 24, false, 2),
		ln("ThinkSystem SR645 V3", 1, 10, false, 2), ln("ThinkSystem SR665 V3", 2, 24, false, 2),
		ln("ThinkSystem SR850", 2, 24, false, 4), ln("ThinkSystem SR850 V2", 2, 24, false, 4), ln("ThinkSystem SR860", 4, 24, false, 4),
		ln("ThinkSystem SR950", 4, 24, false, 4),
		lt("ThinkSystem ST50", 3), lt("ThinkSystem ST50 V2", 3), lt("ThinkSystem ST50 V3", 3), lt("ThinkSystem ST250", 4),
		lt("ThinkSystem ST250 V2", 4), lt("ThinkSystem ST250 V3", 4), lt("ThinkSystem ST550", 8), lt("ThinkSystem ST650 V2", 8), lt("ThinkSystem ST650 V3", 8),
	)

	// ---- Supermicro (common SuperServer families)
	sm := func(name string, u, bays int, lff bool) model {
		return model{vendor: "Supermicro", name: name, kind: "server", form: "rack", u: u, bays: bays, lff: lff, nic: 2}
	}
	add(
		sm("SYS-5019C (1U short)", 1, 4, true), sm("SYS-6019P", 1, 4, true), sm("SYS-1029P", 1, 10, false), sm("SYS-1029U", 1, 10, false),
		sm("SYS-2029P", 2, 24, false), sm("SYS-2029U", 2, 24, false), sm("SYS-6029P", 2, 12, true), sm("SYS-510T", 1, 4, true),
		sm("SYS-511R", 1, 4, true), sm("SYS-121H", 1, 10, false), sm("SYS-221H", 2, 24, false), sm("AS-1014S", 1, 10, false),
		sm("AS-1114S", 1, 10, false), sm("AS-2014S", 2, 24, false), sm("AS-2124BT (2U Twin)", 2, 24, false), sm("SSG-6049P (storage)", 4, 24, true),
		model{vendor: "Supermicro", name: "SYS-7049A (tower)", kind: "server", form: "tower", bays: 8, lff: true, nic: 2},
		model{vendor: "Supermicro", name: "SYS-5039 (tower)", kind: "server", form: "tower", bays: 4, lff: true, nic: 2},
	)

	// ---- Cisco UCS C-series
	cs := func(name string, u, bays int) model {
		return model{vendor: "Cisco", name: "UCS " + name, kind: "server", form: "rack", u: u, bays: bays, nic: 2}
	}
	add(cs("C220 M3", 1, 8), cs("C220 M4", 1, 8), cs("C220 M5", 1, 10), cs("C220 M6", 1, 10), cs("C220 M7", 1, 10),
		cs("C240 M3", 2, 24), cs("C240 M4", 2, 24), cs("C240 M5", 2, 24), cs("C240 M6", 2, 24), cs("C240 M7", 2, 24),
		cs("C225 M6", 1, 10), cs("C245 M6", 2, 24), cs("C480 M5", 4, 24))

	// ---- Fujitsu PRIMERGY
	fj := func(name string, u, bays int, lff bool) model {
		return model{vendor: "Fujitsu", name: "PRIMERGY " + name, kind: "server", form: "rack", u: u, bays: bays, lff: lff, nic: 2}
	}
	ft := func(name string, bays int) model {
		return model{vendor: "Fujitsu", name: "PRIMERGY " + name, kind: "server", form: "tower", bays: bays, lff: true, nic: 2}
	}
	add(fj("RX1330 M3", 1, 4, true), fj("RX1330 M4", 1, 4, true), fj("RX1330 M5", 1, 4, true),
		fj("RX2530 M4", 1, 10, false), fj("RX2530 M5", 1, 10, false), fj("RX2530 M6", 1, 10, false), fj("RX2530 M7", 1, 10, false),
		fj("RX2540 M4", 2, 24, false), fj("RX2540 M5", 2, 24, false), fj("RX2540 M6", 2, 24, false), fj("RX2540 M7", 2, 24, false),
		fj("RX2520 M5", 2, 12, true), fj("RX4770 M5", 2, 8, false),
		ft("TX1310 M3", 2), ft("TX1320 M4", 4), ft("TX1330 M4", 4), ft("TX1330 M5", 4), ft("TX2550 M5", 8), ft("TX2550 M7", 8))

	// ---- Huawei / xFusion FusionServer, Inspur
	add(
		model{vendor: "Huawei", name: "RH1288 V3", kind: "server", form: "rack", u: 1, bays: 10, nic: 2},
		model{vendor: "Huawei", name: "RH2288 V3", kind: "server", form: "rack", u: 2, bays: 24, nic: 2},
		model{vendor: "Huawei", name: "FusionServer 1288H V5", kind: "server", form: "rack", u: 1, bays: 10, nic: 2},
		model{vendor: "Huawei", name: "FusionServer 2288H V5", kind: "server", form: "rack", u: 2, bays: 24, nic: 2},
		model{vendor: "Huawei", name: "FusionServer 5288 V5", kind: "server", form: "rack", u: 4, bays: 24, lff: true, nic: 2},
		model{vendor: "xFusion", name: "FusionServer 1288H V6", kind: "server", form: "rack", u: 1, bays: 10, nic: 2},
		model{vendor: "xFusion", name: "FusionServer 2288H V6", kind: "server", form: "rack", u: 2, bays: 24, nic: 2},
		model{vendor: "xFusion", name: "FusionServer 1288H V7", kind: "server", form: "rack", u: 1, bays: 10, nic: 2},
		model{vendor: "xFusion", name: "FusionServer 2288H V7", kind: "server", form: "rack", u: 2, bays: 24, nic: 2},
		model{vendor: "Inspur", name: "NF5180M6", kind: "server", form: "rack", u: 1, bays: 10, nic: 2},
		model{vendor: "Inspur", name: "NF5280M5", kind: "server", form: "rack", u: 2, bays: 24, nic: 2},
		model{vendor: "Inspur", name: "NF5280M6", kind: "server", form: "rack", u: 2, bays: 24, nic: 2},
		model{vendor: "Inspur", name: "NF5280M7", kind: "server", form: "rack", u: 2, bays: 24, nic: 2},
		model{vendor: "Inspur", name: "NF5466M6", kind: "server", form: "rack", u: 4, bays: 24, lff: true, nic: 2},
	)
	ms = append(ms, catalogMore()...)
	ms = append(ms, catalogExtra()...)
	ms = append(ms, catalogMisc()...)
	ms = append(ms, catalogISP()...)
	return append(ms, catalogFS()...)
}
