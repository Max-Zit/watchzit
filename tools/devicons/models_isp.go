package main

// The fifth part of the catalog: what ISPs and industry use — GPON OLTs,
// service provider and core routers, SD-WAN edges, NTP time servers — and
// more servers, intercoms, video conferencing and PLCs.

func catalogISP() []model {
	var ms []model
	add := func(m ...model) { ms = append(ms, m...) }
	rk := func(v, n, kind string, u, copper, sfp int) model {
		return model{vendor: v, name: n, kind: kind, form: "rack", u: u, copper: copper, sfp: sfp}
	}
	dk := func(v, n, kind string, copper, sfp int) model {
		return model{vendor: v, name: n, kind: kind, form: "desk", copper: copper, sfp: sfp}
	}
	mini := func(v, n, kind string, ports int) model {
		return model{vendor: v, name: n, kind: kind, form: "mini", copper: ports}
	}
	srv := func(v, n string, u, bays int, lff bool) model {
		return model{vendor: v, name: n, kind: "server", form: "rack", u: u, bays: bays, lff: lff, nic: 2}
	}

	// ---- GPON / XGS-PON OLTs (the PON ports drawn as SFP cages)
	add(rk("Huawei", "MA5800-X2", "switch", 1, 0, 20), rk("Huawei", "MA5800-X7", "switch", 6, 2, 64), rk("Huawei", "MA5800-X15", "switch", 10, 2, 96),
		rk("Huawei", "MA5800-X17", "switch", 10, 2, 96), rk("Huawei", "MA5608T", "switch", 2, 2, 32), rk("Huawei", "MA5683T", "switch", 6, 2, 64),
		rk("Huawei", "EA5800-X2", "switch", 1, 0, 20), rk("Huawei", "EA5800-X7", "switch", 6, 2, 64),
		rk("ZTE", "ZXA10 C300", "switch", 10, 2, 96), rk("ZTE", "ZXA10 C320", "switch", 2, 2, 32), rk("ZTE", "ZXA10 C600", "switch", 10, 2, 96),
		rk("ZTE", "ZXA10 C610", "switch", 2, 2, 32), rk("ZTE", "ZXA10 C620", "switch", 4, 2, 48), rk("ZTE", "ZXA10 C650", "switch", 6, 2, 64),
		rk("Nokia", "ISAM FX-8", "switch", 6, 2, 64), rk("Nokia", "Lightspan MF-2", "switch", 2, 0, 32), rk("Nokia", "Lightspan SF-8M", "switch", 1, 0, 12),
		rk("VSOL", "V1600D4", "switch", 1, 4, 8), rk("VSOL", "V1600D8", "switch", 1, 4, 12), rk("VSOL", "V1600G1", "switch", 1, 4, 12),
		rk("VSOL", "V1600G2", "switch", 1, 4, 20), rk("VSOL", "V1600GT", "switch", 1, 4, 20), rk("VSOL", "V1600XG02", "switch", 1, 4, 10),
		rk("BDCOM", "P3310D", "switch", 1, 4, 8), rk("BDCOM", "P3608-2TE", "switch", 1, 4, 10), rk("BDCOM", "GP3600-08", "switch", 1, 4, 12),
		rk("BDCOM", "GP3600-16", "switch", 1, 4, 20), rk("C-Data", "FD1608GS", "switch", 1, 4, 12), rk("C-Data", "FD1616GS", "switch", 1, 4, 20),
		rk("C-Data", "FD1104SN", "switch", 1, 4, 6), rk("Ubiquiti", "UFiber OLT", "switch", 1, 1, 10), rk("Ubiquiti", "UFiber OLT XGS", "switch", 1, 1, 12),
		rk("TP-Link", "DS-P7001-08 (OLT)", "switch", 1, 4, 12), rk("TP-Link", "DS-P7001-16 (OLT)", "switch", 1, 4, 20), rk("FiberHome", "AN5516-04", "switch", 6, 2, 48),
		rk("FiberHome", "AN6000-2", "switch", 2, 2, 32), rk("Calix", "E7-2", "switch", 2, 2, 32), rk("Adtran", "SDX 6320-16", "switch", 1, 0, 20),
		dk("Ubiquiti", "UFiber Nano G (ONT)", "router", 1, 1), dk("Ubiquiti", "UFiber Loco (ONT)", "router", 1, 1), dk("VSOL", "V2802GWT (ONT)", "router", 4, 1),
		dk("Huawei", "HG8010H (ONT)", "router", 1, 1), dk("Huawei", "EG8010H (ONT)", "router", 1, 1), dk("ZTE", "F601 (ONT)", "router", 1, 1),
		dk("Nokia", "G-010G-A (ONT)", "router", 1, 1), dk("TP-Link", "XZ000-G3 (ONT)", "router", 1, 1),
	)

	// ---- service provider and core routers
	add(rk("Cisco", "NCS 540", "router", 1, 4, 28), rk("Cisco", "NCS 55A1-24H", "router", 1, 0, 24), rk("Cisco", "NCS 5501", "router", 1, 0, 52),
		rk("Cisco", "ASR 9901", "router", 2, 0, 44), rk("Cisco", "ASR 9903", "router", 3, 0, 36), rk("Cisco", "ASR 9006", "router", 10, 0, 48),
		rk("Cisco", "ASR 903", "router", 3, 0, 24), rk("Cisco", "ASR 1001-HX", "router", 1, 8, 8), rk("Cisco", "8201-32FH", "router", 1, 0, 32),
		rk("Juniper", "MX10003", "router", 3, 0, 36), rk("Juniper", "MX304", "router", 2, 0, 48), rk("Juniper", "ACX7100-48L", "router", 1, 0, 54),
		rk("Juniper", "ACX5448", "router", 1, 0, 52), rk("Juniper", "PTX10001-36MR", "router", 1, 0, 36), rk("Juniper", "MX960", "router", 16, 0, 96),
		rk("Nokia", "7750 SR-1", "router", 2, 0, 36), rk("Nokia", "7750 SR-7s", "router", 10, 0, 96), rk("Nokia", "7250 IXR-e", "router", 1, 4, 24),
		rk("Nokia", "7250 IXR-R6", "router", 3, 0, 48), rk("Nokia", "7210 SAS-Sx", "router", 1, 24, 4), rk("Nokia", "7705 SAR-8", "router", 2, 8, 8),
		rk("Huawei", "NetEngine 8000 M8", "router", 2, 0, 32), rk("Huawei", "NetEngine 8000 F1A", "router", 1, 0, 36), rk("Huawei", "NE40E-X3A", "router", 4, 0, 24),
		rk("Huawei", "NE20E-S2F", "router", 1, 8, 16), rk("Huawei", "ATN 910C", "router", 1, 8, 12), rk("Arista", "7280R3", "router", 1, 0, 48),
		rk("Arista", "7500R3 (8 slot)", "router", 13, 0, 96), rk("Ciena", "3930", "switch", 1, 4, 8), rk("Ciena", "8110", "router", 1, 0, 24),
		rk("Extreme", "SLX 9540", "router", 1, 0, 54), rk("MikroTik", "CCR2216-1G-12XS-2XQ (BGP)", "router", 1, 1, 14), rk("UfiSpace", "S9600-32X", "router", 1, 0, 32),
		rk("ZTE", "ZXR10 M6000-2S4", "router", 2, 0, 32), rk("ZTE", "ZXR10 5960-56QU", "switch", 1, 0, 56),
	)

	// ---- SD-WAN edges and branch appliances
	add(dk("VMware", "VeloCloud Edge 510", "router", 6, 2), dk("VMware", "VeloCloud Edge 520", "router", 6, 2), dk("VMware", "VeloCloud Edge 610", "router", 6, 2),
		rk("VMware", "VeloCloud Edge 640", "router", 1, 8, 4), rk("VMware", "VeloCloud Edge 680", "router", 1, 8, 4), rk("VMware", "VeloCloud Edge 840", "router", 1, 8, 4),
		dk("HPE Aruba", "EdgeConnect EC-XS", "router", 6, 0), rk("HPE Aruba", "EdgeConnect EC-S", "router", 1, 6, 2), rk("HPE Aruba", "EdgeConnect EC-M", "router", 1, 6, 4),
		dk("Cisco Meraki", "Z3", "router", 5, 0), dk("Cisco Meraki", "Z4", "router", 5, 0), dk("Cisco", "Catalyst 8200L-1N-4T", "router", 4, 2),
		dk("Versa", "CSG355", "router", 6, 2), rk("Versa", "CSG770", "router", 1, 8, 4), dk("Fortinet", "FortiExtender 211F", "router", 2, 0),
		dk("Peplink", "Balance 20X", "router", 5, 0), dk("Peplink", "MAX BR1 Pro 5G", "router", 2, 0), rk("Peplink", "Balance 310X", "router", 1, 8, 1),
		rk("Peplink", "Balance 710", "router", 1, 10, 0), dk("Cradlepoint", "E300", "router", 5, 0), dk("Cradlepoint", "IBR900", "router", 5, 0),
	)

	// ---- NTP / PTP time servers
	add(rk("Meinberg", "LANTIME M300", "server", 1, 4, 0), rk("Meinberg", "LANTIME M600", "server", 1, 4, 2), rk("Meinberg", "LANTIME M1000", "server", 1, 6, 2),
		mini("Meinberg", "LANTIME M100", "server", 1), rk("Microchip", "SyncServer S650", "server", 1, 6, 2), rk("Microchip", "SyncServer S200", "server", 1, 3, 0),
		rk("Oscilloquartz", "OSA 5401", "server", 1, 2, 2), mini("TimeMachines", "TM2000B", "server", 1), mini("Leo Bodnar", "LBE-1420 GPS NTP", "server", 1),
	)

	// ---- more servers
	add(srv("Gigabyte", "R162-Z11", 1, 8, false), srv("Gigabyte", "R182-Z91", 1, 10, false), srv("Gigabyte", "R272-Z32", 2, 24, false),
		srv("Gigabyte", "R282-Z90", 2, 12, true), srv("Gigabyte", "G292-Z20 (GPU)", 2, 8, false), srv("Gigabyte", "S452-Z30 (storage)", 4, 36, true),
		srv("ASUS", "RS500A-E11", 1, 10, false), srv("ASUS", "RS520A-E12", 2, 24, false), srv("ASUS", "RS720-E11", 2, 24, false), srv("ASUS", "ESC4000A-E12 (GPU)", 2, 8, false),
		srv("Supermicro", "SYS-110P", 1, 4, true), srv("Supermicro", "SYS-120U", 1, 10, false), srv("Supermicro", "SYS-220U", 2, 12, true), srv("Supermicro", "SYS-620P", 2, 12, true),
		srv("Supermicro", "SYS-420GP (GPU)", 4, 8, false), srv("Supermicro", "AS-1115S", 1, 4, true), srv("Supermicro", "AS-2015CS", 2, 12, true), srv("Supermicro", "SSG-640SP", 4, 60, true),
		srv("Supermicro", "SYS-2029TP (2U4N)", 2, 24, false), srv("Supermicro", "SYS-5019D (short)", 1, 4, true),
		srv("HPE", "Apollo 4200 Gen10", 2, 24, true), srv("HPE", "Apollo 4510 Gen10", 4, 60, true), srv("HPE", "Apollo 2000 Gen10 Plus", 2, 24, false),
		srv("HPE", "ProLiant DL110 Gen11", 1, 4, false), srv("HPE", "ProLiant DL345 Gen12", 2, 12, true), srv("HPE", "Edgeline EL8000", 5, 4, false),
		srv("Dell", "PowerEdge XR11", 1, 2, false), srv("Dell", "PowerEdge XR12", 2, 4, false), srv("Dell", "PowerEdge XR8000", 2, 2, false), srv("Dell", "PowerEdge C6420 (2U4N)", 2, 24, false),
		srv("Dell", "PowerEdge C6525 (2U4N)", 2, 24, false), srv("Dell", "PowerEdge XE8640 (GPU)", 4, 8, false), srv("Dell", "PowerEdge R6715", 1, 10, false), srv("Dell", "PowerEdge R7715", 2, 24, false),
		srv("Lenovo", "ThinkSystem SE350 (edge)", 1, 2, false), srv("Lenovo", "ThinkSystem SR670 V2 (GPU)", 3, 8, false),
		srv("IBM", "Power S1022", 2, 8, false), srv("IBM", "Power S1024", 4, 16, false), srv("IBM", "Power S922", 2, 8, false), srv("IBM", "Power E1050", 4, 10, false),
		srv("Oracle", "Server X9-2", 1, 8, false), srv("Oracle", "Exadata X9M storage", 2, 12, true), srv("Inspur", "NF5468M6 (GPU)", 4, 8, false),
		srv("Huawei", "TaiShan 200 2280", 2, 24, false), srv("xFusion", "FusionServer 2488H V7", 2, 24, false), srv("Nutanix", "NX-1065-G8", 2, 24, false),
	)

	// ---- intercoms, video conferencing, PLCs and HMIs
	add(mini("2N", "IP Verso", "camera", 1), mini("2N", "IP Style", "camera", 1), mini("2N", "IP Solo", "camera", 1), mini("2N", "IP Force", "camera", 1),
		mini("Hikvision", "DS-KV6113-WPE1 (door station)", "camera", 1), mini("Hikvision", "DS-KH6320-WTE1 (monitor)", "other", 1), mini("Dahua", "VTO2202F-P (door station)", "camera", 1),
		mini("Dahua", "VTH5321GW (monitor)", "other", 1), mini("Akuvox", "R20A", "camera", 1), mini("Fanvil", "i30 (door phone)", "camera", 1), mini("Ubiquiti", "UniFi G4 Doorbell Pro", "camera", 0),
		mini("Ubiquiti", "UniFi Access Reader Pro", "other", 1), mini("Cisco", "Webex Room Kit", "other", 1), mini("Cisco", "Webex Room Bar", "other", 1), mini("Cisco", "Webex Board Pro", "other", 1),
		mini("Poly", "Studio X50", "other", 1), mini("Poly", "Studio X30", "other", 1), mini("Poly", "G7500", "other", 2), mini("Yealink", "MeetingBar A30", "other", 1),
		mini("Yealink", "MVC640", "other", 2), mini("Logitech", "Rally Bar", "other", 1), mini("Logitech", "Tap IP", "other", 1), mini("Crestron", "Flex UC-B160", "other", 1),
		mini("Siemens", "SIMATIC S7-1200 CPU 1214C", "other", 1), mini("Siemens", "SIMATIC S7-1500 CPU 1515-2 PN", "other", 3), mini("Siemens", "SIMATIC S7-300 CPU 315-2 PN/DP", "other", 2),
		mini("Siemens", "SIMATIC ET 200SP", "other", 2), mini("Siemens", "SIMATIC HMI TP700 Comfort", "other", 1), mini("Schneider Electric", "Modicon M221", "other", 1),
		mini("Schneider Electric", "Modicon M580", "other", 3), mini("Allen-Bradley", "CompactLogix 5380", "other", 2), mini("Allen-Bradley", "ControlLogix 1756-EN2T", "other", 1),
		mini("Allen-Bradley", "Micro850", "other", 1), mini("Allen-Bradley", "PanelView Plus 7", "other", 1), mini("Beckhoff", "CX9020", "other", 2), mini("Beckhoff", "CX5140", "other", 2),
		mini("WAGO", "PFC200 750-8212", "other", 2), mini("Phoenix Contact", "PLCnext AXC F 2152", "other", 2), mini("Omron", "NX1P2", "other", 1), mini("Mitsubishi", "MELSEC iQ-R", "other", 1),
		mini("Unitronics", "UniStream USP-070", "other", 2), mini("Teltonika", "TRB140 (LTE gateway)", "router", 1), mini("Moxa", "NPort 5150 (serial)", "other", 1),
		mini("Moxa", "MGate MB3170 (Modbus)", "other", 2), mini("Lantronix", "EDS1100 (serial)", "other", 1),
	)
	return ms
}
