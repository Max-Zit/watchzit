package main

// The fourth part of the catalog: what else is usually on a network map —
// printers, IP phones and PBXs, blade chassis, PDUs, KVM and console
// servers, environment monitors, mini PCs and business desktops.

func catalogMisc() []model {
	var ms []model
	add := func(m ...model) { ms = append(ms, m...) }
	prn := func(v, n string, mfp bool) model {
		m := model{vendor: v, name: n, kind: "printer", form: "printer", copper: 1}
		if mfp {
			m.bays = 1
		}
		return m
	}
	ph := func(v, n string, ports int) model {
		return model{vendor: v, name: n, kind: "other", form: "phone", copper: ports}
	}
	mini := func(v, n, kind string, ports int) model {
		return model{vendor: v, name: n, kind: kind, form: "mini", copper: ports}
	}
	rk := func(v, n, kind string, u, copper, sfp int) model {
		return model{vendor: v, name: n, kind: kind, form: "rack", u: u, copper: copper, sfp: sfp}
	}
	blade := func(v, n string, u, bays int) model {
		return model{vendor: v, name: n, kind: "server", form: "rack", u: u, bays: bays, nic: 2}
	}

	// ---- printers and multifunction devices
	for _, n := range []string{"LaserJet Pro M404dn", "LaserJet Pro 4002dn", "LaserJet M406dn", "LaserJet Enterprise M507dn", "LaserJet Enterprise M611dn",
		"Color LaserJet Pro M454dn", "Color LaserJet Pro 4202dn", "Color LaserJet Enterprise M555dn", "LaserJet Pro M15w", "LaserJet M110we", "OfficeJet Pro 9010",
		"OfficeJet Pro 9120"} {
		add(prn("HP", n, false))
	}
	for _, n := range []string{"LaserJet Pro MFP M428fdn", "LaserJet Pro MFP 4102fdn", "LaserJet MFP M140we", "LaserJet Enterprise MFP M528f",
		"Color LaserJet Pro MFP M479fdn", "Color LaserJet Pro MFP 4302fdn", "Color LaserJet Enterprise MFP M578f", "OfficeJet Pro 9022e", "LaserJet Managed MFP E52645"} {
		add(prn("HP", n, true))
	}
	for _, c := range [][2]string{{"Brother", "HL-L2350DW"}, {"Brother", "HL-L5100DN"}, {"Brother", "HL-L6400DW"}, {"Brother", "HL-L8360CDW"}, {"Brother", "HL-L3270CDW"},
		{"Kyocera", "ECOSYS P2040dn"}, {"Kyocera", "ECOSYS P3145dn"}, {"Kyocera", "ECOSYS P5026cdn"}, {"Canon", "i-SENSYS LBP223dw"}, {"Canon", "i-SENSYS LBP673Cdw"},
		{"Lexmark", "MS521dn"}, {"Lexmark", "CS431dw"}, {"Xerox", "B230"}, {"Xerox", "VersaLink C400"}, {"Ricoh", "SP 3710DN"}, {"Ricoh", "P 501"},
		{"Epson", "WorkForce Pro WF-C5390"}, {"Samsung", "Xpress M2026W"}, {"OKI", "C650dn"}, {"Zebra", "ZD421 (label)"}, {"Zebra", "ZT411 (label)"}} {
		add(prn(c[0], c[1], false))
	}
	for _, c := range [][2]string{{"Brother", "MFC-L2710DW"}, {"Brother", "MFC-L2750DW"}, {"Brother", "MFC-L5750DW"}, {"Brother", "MFC-L8900CDW"}, {"Brother", "DCP-L2550DN"},
		{"Kyocera", "ECOSYS M2040dn"}, {"Kyocera", "ECOSYS M3145dn"}, {"Kyocera", "ECOSYS M5526cdw"}, {"Kyocera", "TASKalfa 2554ci"}, {"Kyocera", "TASKalfa 3554ci"},
		{"Canon", "i-SENSYS MF445dw"}, {"Canon", "i-SENSYS MF754Cdw"}, {"Canon", "imageRUNNER ADVANCE DX C3826i"}, {"Canon", "imageRUNNER 2425"},
		{"Ricoh", "IM C3000"}, {"Ricoh", "IM C4510"}, {"Ricoh", "MP 2555"}, {"Ricoh", "M 320F"}, {"Xerox", "B225"}, {"Xerox", "VersaLink C405"}, {"Xerox", "AltaLink C8130"},
		{"Konica Minolta", "bizhub C250i"}, {"Konica Minolta", "bizhub C300i"}, {"Konica Minolta", "bizhub 4000i"}, {"Sharp", "MX-3071"}, {"Sharp", "BP-50C26"},
		{"Lexmark", "MX431adn"}, {"Lexmark", "CX431adw"}, {"Epson", "EcoTank L6290"}, {"Epson", "WorkForce Pro WF-C579R"}, {"Toshiba", "e-STUDIO 2010AC"},
		{"Develop", "ineo+ 250i"}, {"OKI", "MC573dn"}, {"Utax", "P-4026iw"}} {
		add(prn(c[0], c[1], true))
	}

	// ---- IP phones, conference phones and PBXs
	for _, c := range [][2]string{{"Yealink", "T31P"}, {"Yealink", "T33G"}, {"Yealink", "T43U"}, {"Yealink", "T46U"}, {"Yealink", "T48U"}, {"Yealink", "T53W"},
		{"Yealink", "T54W"}, {"Yealink", "T57W"}, {"Yealink", "T58W"}, {"Yealink", "T21P E2"}, {"Yealink", "T42S"}, {"Yealink", "T46S"},
		{"Cisco", "IP Phone 7821"}, {"Cisco", "IP Phone 7841"}, {"Cisco", "IP Phone 8841"}, {"Cisco", "IP Phone 8851"}, {"Cisco", "IP Phone 8865"}, {"Cisco", "IP Phone 6851"},
		{"Grandstream", "GXP1610"}, {"Grandstream", "GXP1628"}, {"Grandstream", "GXP2170"}, {"Grandstream", "GRP2612"}, {"Grandstream", "GRP2614"}, {"Grandstream", "GRP2615"},
		{"Fanvil", "X3U"}, {"Fanvil", "X4U"}, {"Fanvil", "X6U"}, {"Snom", "D315"}, {"Snom", "D735"}, {"Snom", "D785"}, {"Polycom", "VVX 250"}, {"Polycom", "VVX 450"},
		{"Poly", "Edge E220"}, {"Poly", "CCX 400"}, {"Avaya", "J179"}, {"Mitel", "6869i"}, {"Panasonic", "KX-HDV230"}, {"Gigaset", "Maxwell 3"}, {"Ubiquiti", "UniFi Talk Phone"}} {
		add(ph(c[0], c[1], 2))
	}
	add(ph("Yealink", "CP920 (conference)", 1), ph("Poly", "Trio 8800 (conference)", 1), ph("Cisco", "IP Conference Phone 8832", 1),
		rk("Grandstream", "UCM6302 (PBX)", "other", 1, 2, 0), rk("Grandstream", "UCM6304 (PBX)", "other", 1, 2, 0), rk("Grandstream", "UCM6308 (PBX)", "other", 1, 2, 0),
		mini("Grandstream", "UCM6301 (PBX)", "other", 2), mini("Yealink", "W70B (DECT base)", "other", 1), mini("Gigaset", "N870 IP PRO (DECT)", "other", 1),
		rk("Cisco", "Unified CM appliance", "server", 1, 2, 0), rk("Patton", "SmartNode SN4141", "other", 1, 2, 0), mini("Grandstream", "HT812 (ATA)", "other", 2),
		mini("Grandstream", "GXW4104 (FXO)", "other", 2), rk("3CX", "SBC appliance", "other", 1, 2, 0), mini("Yealink", "MeetingBar A20", "other", 1),
	)

	// ---- blade chassis
	add(blade("HPE", "BladeSystem c7000", 10, 16), blade("HPE", "BladeSystem c3000", 6, 8), blade("HPE", "Synergy 12000 Frame", 10, 12),
		blade("Dell", "PowerEdge M1000e", 10, 16), blade("Dell", "PowerEdge VRTX", 5, 4), blade("Dell", "PowerEdge MX7000", 7, 8), blade("Dell", "PowerEdge FX2", 2, 4),
		blade("Cisco", "UCS 5108 Blade Chassis", 6, 8), blade("Cisco", "UCS X9508 Chassis", 7, 8), blade("Lenovo", "Flex System Enterprise Chassis", 10, 14),
		blade("Lenovo", "ThinkSystem SD530 (2U4N)", 2, 4), blade("Supermicro", "SuperBlade SBE-820C", 8, 20), blade("Fujitsu", "PRIMERGY BX900 S2", 10, 18),
		rk("Cisco", "UCS 6454 Fabric Interconnect", "switch", 1, 0, 54), rk("Cisco", "UCS 6332 Fabric Interconnect", "switch", 1, 0, 32),
		rk("Cisco", "UCS 6248UP Fabric Interconnect", "switch", 1, 0, 48), rk("HPE", "Virtual Connect FlexFabric-20/40 F8", "switch", 1, 0, 12),
	)

	// ---- PDUs, KVM and console servers, environment monitors
	add(rk("APC", "Rack PDU AP7920B", "other", 1, 1, 0), rk("APC", "Rack PDU AP7900B", "other", 1, 1, 0), rk("APC", "Rack PDU AP8959EU3", "other", 0, 1, 0),
		rk("APC", "Rack PDU AP8641", "other", 0, 1, 0), rk("APC", "NetBotz 355", "other", 1, 1, 0), rk("APC", "NetBotz 750", "other", 1, 2, 0),
		rk("Eaton", "ePDU G3 Switched", "other", 1, 1, 0), rk("Eaton", "ePDU G4 Metered", "other", 1, 1, 0), rk("Raritan", "PX3-5260R", "other", 1, 1, 0),
		rk("Raritan", "Dominion KX IV-101", "other", 1, 2, 0), rk("Raritan", "Dominion KX III 216", "other", 1, 18, 0), rk("Raritan", "Dominion SX II 32", "other", 1, 2, 0),
		rk("Vertiv", "Geist rPDU", "other", 1, 1, 0), rk("Vertiv", "Avocent MergePoint Unity", "other", 1, 18, 0), rk("Vertiv", "Avocent ACS 8000", "other", 1, 2, 0),
		rk("Vertiv", "Avocent ACS 8048", "other", 1, 2, 0), rk("ATEN", "CS1716A KVM", "other", 1, 0, 0), rk("ATEN", "CS1708A KVM", "other", 1, 0, 0),
		rk("ATEN", "KN1108VA KVM over IP", "other", 1, 2, 0), rk("ATEN", "KN4116VA KVM over IP", "other", 1, 2, 0), rk("ATEN", "CN8000A", "other", 0, 1, 0),
		rk("ATEN", "PE8216 PDU", "other", 2, 1, 0), rk("Opengear", "CM8148", "other", 1, 2, 0), rk("Opengear", "OM2248", "other", 1, 6, 2),
		rk("Opengear", "IM7248", "other", 1, 2, 0), mini("Opengear", "ACM7004-5", "other", 4), rk("Lantronix", "SLC 8000", "other", 1, 2, 2),
		mini("Lantronix", "SLB882", "other", 2), rk("Digi", "Connect IT 48", "other", 1, 2, 1), mini("Digi", "Connect IT 4", "other", 2),
		rk("Cisco", "Catalyst 8300 terminal server", "other", 1, 4, 0), mini("HWg", "STE2", "other", 1), mini("HWg", "Poseidon2 3468", "other", 1),
		mini("HWg", "Damocles2 MINI", "other", 1), mini("AKCP", "sensorProbe2+", "other", 1), mini("AKCP", "SPX+", "other", 1), mini("Papouch", "TME", "other", 1),
		mini("Comet", "P8652 Web Sensor", "other", 1), mini("Ubiquiti", "UniFi Access Hub", "other", 1), mini("Shelly", "Pro 4PM", "other", 1),
	)

	// ---- mini PCs, single-board computers and business desktops
	add(mini("Intel", "NUC 11 Pro", "pc", 1), mini("Intel", "NUC 13 Pro", "pc", 1), mini("ASUS", "NUC 14 Pro", "pc", 1), mini("Raspberry Pi", "Raspberry Pi 4 Model B", "pc", 1),
		mini("Raspberry Pi", "Raspberry Pi 5", "pc", 1), mini("Raspberry Pi", "Raspberry Pi 3 Model B+", "pc", 1), mini("Raspberry Pi", "Compute Module 4 IO", "pc", 1),
		mini("Dell", "OptiPlex 7010 Micro", "pc", 1), mini("Dell", "OptiPlex 7020 Micro", "pc", 1), mini("Dell", "OptiPlex 3000 Thin Client", "pc", 1),
		mini("Dell", "Wyse 5070 Thin Client", "pc", 1), mini("HP", "EliteDesk 800 G6 Mini", "pc", 1), mini("HP", "EliteDesk 800 G9 Mini", "pc", 1),
		mini("HP", "ProDesk 400 G6 Mini", "pc", 1), mini("HP", "t640 Thin Client", "pc", 1), mini("Lenovo", "ThinkCentre M70q", "pc", 1), mini("Lenovo", "ThinkCentre M90q", "pc", 1),
		mini("Lenovo", "ThinkCentre M920q", "pc", 1), mini("Lenovo", "ThinkCentre neo 50q", "pc", 1), mini("Beelink", "EQ12", "pc", 2), mini("Minisforum", "MS-01", "pc", 2),
		mini("Protectli", "Vault VP2410", "pc", 4), mini("Zotac", "ZBOX CI337", "pc", 2), mini("Apple", "Mac mini M2", "pc", 1), mini("Apple", "Mac mini M4", "pc", 1),
		mini("Apple", "Mac Studio", "pc", 1), mini("Supermicro", "SYS-E300-9D", "server", 4), mini("Supermicro", "SYS-E200-9B", "server", 2),
	)
	for _, c := range [][2]string{{"Dell", "OptiPlex 7010 Tower"}, {"Dell", "OptiPlex 7020 SFF"}, {"Dell", "Precision 3660 Tower"}, {"HP", "EliteDesk 800 G9 Tower"},
		{"HP", "ProDesk 400 G9 SFF"}, {"HP", "Z2 G9 Tower"}, {"Lenovo", "ThinkCentre M70t"}, {"Lenovo", "ThinkCentre M90s"}, {"Lenovo", "ThinkStation P360 Tower"},
		{"Fujitsu", "ESPRIMO P5011"}, {"Acer", "Veriton X2690G"}, {"ASUS", "ExpertCenter D7 Tower"}} {
		add(model{vendor: c[0], name: c[1], kind: "pc", form: "tower", bays: 1, nic: 1})
	}
	return ms
}
