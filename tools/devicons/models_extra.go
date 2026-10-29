package main

// The third part of the catalog: IP cameras, UPSs (tower and rack), more
// access points and outdoor wireless (point to point dishes, sectors and
// omni antennas). Same rules as catalog().

func catalogExtra() []model {
	var ms []model
	add := func(m ...model) { ms = append(ms, m...) }
	cam := func(v, n, form string) model { return model{vendor: v, name: n, kind: "camera", form: form, copper: 1} }
	ups := func(v, n string, card bool) model {
		m := model{vendor: v, name: n, kind: "ups", form: "upstower"}
		if card {
			m.copper = 1
		}
		return m
	}
	upr := func(v, n string, u int) model {
		return model{vendor: v, name: n, kind: "ups", form: "rack", u: u, copper: 1}
	}
	ce := func(v, n string, copper int) model {
		return model{vendor: v, name: n, kind: "accesspoint", form: "ceiling", copper: copper}
	}
	ant := func(v, n, form string, copper, sfp int) model {
		return model{vendor: v, name: n, kind: "accesspoint", form: form, copper: copper, sfp: sfp}
	}

	// ---- Dahua cameras (WizSense, WizMind, Lite, PTZ)
	for _, n := range []string{"IPC-HFW1230S", "IPC-HFW1431S", "IPC-HFW1230S-S5", "IPC-HFW2231S-S-S2", "IPC-HFW2431S-S-S2", "IPC-HFW2441S-S", "IPC-HFW2541S-S",
		"IPC-HFW2849S-S-IL", "IPC-HFW3241E-AS", "IPC-HFW3441E-AS", "IPC-HFW3449T1-AS-PV", "IPC-HFW3549T1-AS-PV", "IPC-HFW3842T-ZAS", "IPC-HFW5241E-ZE",
		"IPC-HFW5442E-ZE", "IPC-HFW5449T1-ZE-LED", "IPC-HFW5541T-ASE", "IPC-HFW5842T-ASE", "IPC-HFW71242H-Z", "IPC-HFW2431T-ZS-S2", "IPC-HFW2541T-ZS"} {
		add(cam("Dahua", n, "bullet"))
	}
	for _, n := range []string{"IPC-HDW1230T1-S5", "IPC-HDW1431T1", "IPC-HDW2231T-AS-S2", "IPC-HDW2431T-AS-S2", "IPC-HDW2441T-S", "IPC-HDW2541T-S",
		"IPC-HDW2849H-S-IL", "IPC-HDW3241EM-AS", "IPC-HDW3441EM-AS", "IPC-HDW3449H-AS-PV", "IPC-HDW3549H-AS-PV", "IPC-HDW3841T-ZAS", "IPC-HDW5241TM-ASE",
		"IPC-HDW5442TM-ASE", "IPC-HDW5541TM-ASE", "IPC-HDW5842H-ASE"} {
		add(cam("Dahua", n, "turret"))
	}
	for _, n := range []string{"IPC-HDBW1230E-S5", "IPC-HDBW1431E", "IPC-HDBW2231E-S-S2", "IPC-HDBW2431E-S-S2", "IPC-HDBW2441E-S", "IPC-HDBW2541E-S",
		"IPC-HDBW2831E-S-S2", "IPC-HDBW3241E-AS", "IPC-HDBW3441E-AS", "IPC-HDBW3441R-ZAS", "IPC-HDBW5241E-ZE", "IPC-HDBW5442R-ASE", "IPC-HDBW5541R-ASE",
		"IPC-EBW81242 (fisheye)", "IPC-EW5541-AS (fisheye)", "IPC-HDPW1431FP-0280B-S4"} {
		add(cam("Dahua", n, "dome"))
	}
	for _, n := range []string{"SD1A203XB-GNB", "SD1A404XB-GNR", "SD22404DB-GNY", "SD3A400-GN-A-PV", "SD49225XA-HNR", "SD49425XB-HNR", "SD5A432XA-HNR",
		"SD6AL245XA-HNR", "SD8A840WA-HNF", "SDT4E425-4F-GB-A-PV"} {
		add(cam("Dahua", n, "ptz"))
	}
	// ---- other common cameras
	for _, c := range [][3]string{
		{"Hikvision", "DS-2CD1043G2-I", "bullet"}, {"Hikvision", "DS-2CD1143G2-I", "dome"}, {"Hikvision", "DS-2CD1343G2-I", "turret"},
		{"Hikvision", "DS-2CD2043G2-I", "bullet"}, {"Hikvision", "DS-2CD2143G2-I", "dome"}, {"Hikvision", "DS-2CD2343G2-I", "turret"},
		{"Hikvision", "DS-2CD2387G2-LU (ColorVu)", "turret"}, {"Hikvision", "DS-2CD2T47G2-L (ColorVu)", "bullet"}, {"Hikvision", "DS-2CD2646G2-IZS", "bullet"},
		{"Hikvision", "DS-2CD2746G2-IZS", "dome"}, {"Hikvision", "DS-2DE4425IW-DE", "ptz"}, {"Hikvision", "DS-2DE7A432IW-AEB", "ptz"},
		{"Ubiquiti", "UniFi G4 Bullet", "bullet"}, {"Ubiquiti", "UniFi G4 Dome", "dome"}, {"Ubiquiti", "UniFi G4 Instant", "turret"}, {"Ubiquiti", "UniFi G4 PTZ", "ptz"},
		{"Ubiquiti", "UniFi G5 Bullet", "bullet"}, {"Ubiquiti", "UniFi G5 Dome", "dome"}, {"Ubiquiti", "UniFi G5 Turret Ultra", "turret"}, {"Ubiquiti", "UniFi G5 Pro", "bullet"},
		{"Ubiquiti", "UniFi AI Pro", "bullet"}, {"Ubiquiti", "UniFi AI 360", "dome"},
		{"Axis", "M2035-LE", "bullet"}, {"Axis", "M3086-V", "dome"}, {"Axis", "P1465-LE", "bullet"}, {"Axis", "P3265-LVE", "dome"}, {"Axis", "Q6135-LE", "ptz"},
		{"Reolink", "RLC-510A", "bullet"}, {"Reolink", "RLC-520A", "dome"}, {"Reolink", "RLC-811A", "bullet"}, {"Reolink", "RLC-823A", "ptz"},
		{"Uniview", "IPC2124LB-SF28KM-G", "bullet"}, {"Uniview", "IPC3614SB-ADF28KM-I0", "turret"}, {"Uniview", "IPC322LB-SF28-A", "dome"},
		{"TP-Link", "VIGI C340", "bullet"}, {"TP-Link", "VIGI C440", "turret"}, {"TP-Link", "VIGI C240", "dome"},
		{"Bosch", "FLEXIDOME IP 5100i", "dome"}, {"Bosch", "DINION IP 5100i", "bullet"}, {"Hanwha", "XNO-6080R", "bullet"}, {"Hanwha", "XND-6080RV", "dome"},
	} {
		add(cam(c[0], c[1], c[2]))
	}

	// ---- UPS: tower and rack/tower models
	for _, n := range []string{"Back-UPS BX500CI", "Back-UPS BX700UI", "Back-UPS BX750MI", "Back-UPS BX950MI", "Back-UPS BX1200MI", "Back-UPS BX1600MI",
		"Back-UPS BX2200MI", "Back-UPS Pro BR900MI", "Back-UPS Pro BR1200GI", "Back-UPS Pro BR1500GI", "Back-UPS Pro BR1600SI", "Easy UPS BVX700LI",
		"Easy UPS BVX1600LI"} {
		add(ups("APC", n, false))
	}
	for _, n := range []string{"Smart-UPS SMT750I", "Smart-UPS SMT1000I", "Smart-UPS SMT1500I", "Smart-UPS SMT2200I", "Smart-UPS SMT3000I",
		"Smart-UPS SMC1000I", "Smart-UPS SMC1500I", "Smart-UPS SMV750CAI", "Smart-UPS SMV1500CAI", "Smart-UPS SMV3000CAI", "Smart-UPS SRT1000XLI",
		"Smart-UPS SRT2200XLI", "Smart-UPS SRT3000XLI", "Smart-UPS SRV1KI", "Smart-UPS SRV3KI", "Smart-UPS SRV6KI", "Smart-UPS SRV10KI", "Smart-UPS SMX1500RMI2U (tower)"} {
		add(ups("APC", n, true))
	}
	for _, n := range []string{"3S 550", "3S 700", "5E 650i", "5E 850i", "5E 1100i", "5E 1500i", "5S 700i", "5S 1000i", "5S 1500i", "Ellipse ECO 650",
		"Ellipse ECO 1600", "Ellipse PRO 650", "Ellipse PRO 1200", "Ellipse PRO 1600"} {
		add(ups("Eaton", n, false))
	}
	for _, n := range []string{"5SC 500i", "5SC 1000i", "5SC 1500i", "5SC 3000i", "5P 650i", "5P 1150i", "5P 1550i", "9SX 1000i", "9SX 2000i", "9SX 3000i",
		"9SX 6000i", "9SX 11000i", "9E 1000i", "9E 3000i", "9E 6000i", "93PS 20kW"} {
		add(ups("Eaton", n, true))
	}
	for _, n := range []string{"Value 600EI", "Value 1200EI", "Value 2200EI", "VP1000ELCD", "VP1600ELCD", "CP1500EPFCLCD", "BR1200ELCD", "UT650EG", "UT1050EG"} {
		add(ups("CyberPower", n, false))
	}
	for _, n := range []string{"OLS1000EA", "OLS2000EA", "OLS3000EA", "OL6KERTHD", "OL10KERTHD", "PR1000ELCD", "PR1500ELCD", "PR2200ELCD"} {
		add(ups("CyberPower", n, true))
	}
	for _, c := range [][2]string{{"Vertiv", "Liebert GXT5-1000IRT2UXL"}, {"Vertiv", "Liebert itON 1000VA"}, {"Vertiv", "Liebert PSA5-1500MT230"},
		{"Riello", "iPlug IPG 800"}, {"Riello", "Net Power NPW 1000"}, {"Riello", "Sentinel Pro SEP 1000"}, {"Riello", "Sentinel Pro SEP 3000"}, {"Riello", "Sentinel Power SPW 6000"},
		{"Riello", "Multi Sentry MST 10"}, {"Socomec", "NeTYS PE 1000"}, {"Socomec", "NeTYS RT 3000"}, {"Socomec", "ITYS 2000"}, {"Socomec", "MASTERYS GP4 20"},
		{"Legrand", "Keor SP 1000"}, {"Legrand", "Keor Multiplug 1000"}, {"Legrand", "Daker DK Plus 3000"}, {"Legrand", "Keor S 10"},
		{"PowerWalker", "VI 1000 SW"}, {"PowerWalker", "VI 2200 CW"}, {"PowerWalker", "VFI 1000 TG"}, {"PowerWalker", "VFI 3000 CG PF1"}, {"PowerWalker", "VFI 10000 TGB"},
		{"FSP", "Champ 1K"}, {"FSP", "Champ 3K"}, {"FSP", "FP 1500"}, {"Huawei", "UPS2000-A-1KTTS"}, {"Huawei", "UPS2000-A-3KTTS"}, {"Huawei", "UPS2000-G-10KRTL"},
		{"Schneider Electric", "Galaxy VS 20kW"}, {"Schneider Electric", "Easy UPS 3S 10kVA"}, {"Delta", "Amplon N-1K"}, {"Delta", "Amplon RT-3K"}, {"Fortron", "DP 2000"},
		{"Ablerex", "MSII 1000"}, {"Salicru", "SLC Twin PRO2 3000"}, {"Salicru", "SPS 1500 ADV T"}, {"Mustek", "PowerMust 1000 LCD"}, {"Orvaldi", "VIP 1000"}} {
		add(ups(c[0], c[1], true))
	}
	add(upr("APC", "Smart-UPS SRT1500RMXLI", 2), upr("APC", "Smart-UPS SRT8KRMXLI", 6), upr("APC", "Smart-UPS SRV1KRI", 2),
		upr("APC", "Smart-UPS SRV3KRI", 2), upr("APC", "Smart-UPS SRV6KRI", 4), upr("APC", "Smart-UPS SRV10KRIL", 6), upr("APC", "Smart-UPS SMC2000I-2U", 2),
		upr("APC", "Smart-UPS SMC3000RMI2U", 2), upr("APC", "Smart-UPS SMT1500RMI1U", 1), upr("APC", "Smart-UPS SCL500RMI1UNC", 1),
		upr("Eaton", "9SX 1000 Rack", 2), upr("Eaton", "9SX 2000 Rack", 2), upr("Eaton", "9SX 1500 Rack", 2), upr("Eaton", "9PX 2200 Rack", 2), upr("Eaton", "5PX 1000 Rack", 2),
		upr("Eaton", "5SC 3000 Rack", 2), upr("Eaton", "5P 650 Rack", 1), upr("Eaton", "5P 850 Rack", 1), upr("Eaton", "9E 6000i Rack", 5),
		upr("CyberPower", "OLS1000ERT2U", 2), upr("CyberPower", "OLS2000ERT2U", 2), upr("CyberPower", "OLS3000ERT2U", 2), upr("CyberPower", "PR1000ERT2U", 2),
		upr("CyberPower", "PR2200ERT2U", 2), upr("CyberPower", "OR1000ERM1U", 1), upr("CyberPower", "OR1500ERM1U", 1),
		upr("Vertiv", "Liebert GXT5-2000IRT2UXL", 2), upr("Vertiv", "Liebert GXT5-10KIRT5UXLE", 5), upr("Vertiv", "Liebert GXT RT+ 3000", 2), upr("Vertiv", "Liebert PSI5-3000RT230", 2),
		upr("Riello", "Sentinel Dual SDU 1000", 2), upr("Riello", "Sentinel Dual SDU 6000", 4), upr("Riello", "Sentinel Dual SDU 10000", 4), upr("Riello", "Multi Sentry MSM 20", 6),
		upr("Socomec", "ITYS Pro 1000", 2), upr("Socomec", "NeTYS RT-M 2200", 2), upr("Socomec", "MODULYS GP 25", 8), upr("Legrand", "Keor SPE 1500 RT", 2),
		upr("Legrand", "Keor SPE 1000 RT", 2), upr("Legrand", "Daker DK Plus 1000", 2), upr("Legrand", "Daker DK Plus 6000", 4),
		upr("PowerWalker", "VI 1500 RT HID", 2), upr("PowerWalker", "VFI 2000 RT LCD", 2), upr("PowerWalker", "VFI 3000 RMG PF1", 2), upr("PowerWalker", "VFI 6000 RT HID", 3),
		upr("Huawei", "UPS2000-G-1KRTL", 2), upr("Huawei", "UPS2000-G-6KRTL", 4), upr("Huawei", "UPS2000-H-10KRTL", 4), upr("Delta", "Amplon RT-6K", 3),
		upr("Delta", "Amplon RT-10K", 4), upr("Salicru", "SLC Twin RT2 3000", 2), upr("Tripp Lite", "SMART1500RMXL2U", 2), upr("Tripp Lite", "SU3000RTXLCD3U", 3),
		upr("Tripp Lite", "SMART3000RM2U", 2), upr("Ablerex", "MS-RT 3000", 2), upr("Fortron", "Champ Rack 3K", 2), upr("Schneider Electric", "Easy UPS SRVS3KRILRK", 2),
	)

	// ---- access points: HPE Aruba, Ubiquiti
	for _, n := range []string{"AP-205", "AP-207", "AP-215", "AP-225", "AP-305 (IAP-305)", "AP-345", "AP-365", "AP-387", "AP-504", "AP-514", "AP-534", "AP-565",
		"AP-574", "AP-584", "AP-585", "AP-587", "AP-634", "AP-654", "AP-734", "AP-754", "AP-755", "Instant On AP21", "Instant On AP27", "Instant On AP22D"} {
		cp := 1
		switch n {
		case "AP-345", "AP-514", "AP-534", "AP-634", "AP-654", "AP-734", "AP-754", "AP-755":
			cp = 2
		case "Instant On AP22D":
			cp = 4
		}
		if n == "AP-365" || n == "AP-387" || n == "AP-565" || n == "AP-574" || n == "AP-584" || n == "AP-585" || n == "AP-587" || n == "Instant On AP27" {
			add(model{vendor: "HPE Aruba", name: n, kind: "accesspoint", form: "outdoor", copper: cp})
		} else {
			add(ce("HPE Aruba", n, cp))
		}
	}
	add(ce("HPE Aruba", "AP-303H (wall)", 4), ce("HPE Aruba", "AP-503H (wall)", 4), ce("HPE Aruba", "AP-505H (wall)", 4), ce("HPE Aruba", "AP-605H (wall)", 4),
		ce("Ubiquiti", "UniFi AP (UAP)", 1), ce("Ubiquiti", "UniFi AP LR (UAP-LR)", 1), ce("Ubiquiti", "UniFi AP Pro (UAP-Pro)", 2), ce("Ubiquiti", "UniFi AP AC SHD", 2),
		ce("Ubiquiti", "UniFi AP XG", 2), ce("Ubiquiti", "UniFi AP AC In-Wall", 3), ce("Ubiquiti", "UniFi AP In-Wall HD", 5), ce("Ubiquiti", "UniFi U6 Extender", 0),
		ce("Ubiquiti", "UniFi U6 In-Wall", 5), ce("Ubiquiti", "UniFi U6 Enterprise In-Wall", 5), ce("Ubiquiti", "UniFi U7 Long-Range", 1), ce("Ubiquiti", "UniFi U7 In-Wall", 5),
		ce("Ubiquiti", "UniFi E7", 1), ce("Ubiquiti", "UniFi E7 Campus", 1), ce("Ubiquiti", "UniFi U6 Mesh Pro", 1), ce("Ubiquiti", "UniFi U7 Pro Outdoor", 1),
		model{vendor: "Ubiquiti", name: "UniFi AP AC M Pro", kind: "accesspoint", form: "outdoor", copper: 2},
		model{vendor: "Ubiquiti", name: "UniFi AP AC Outdoor", kind: "accesspoint", form: "outdoor", copper: 2},
		model{vendor: "Ubiquiti", name: "UniFi U7 Outdoor Mesh", kind: "accesspoint", form: "outdoor", copper: 1},
	)

	// ---- outdoor wireless: Ubiquiti airMAX / airFiber / LTU, MikroTik
	add(ant("Ubiquiti", "PowerBeam 5AC Gen2", "dish", 1, 0), ant("Ubiquiti", "PowerBeam 5AC 500", "dish", 1, 1), ant("Ubiquiti", "PowerBeam 5AC 620", "dish", 1, 1),
		ant("Ubiquiti", "PowerBeam 2AC 400", "dish", 1, 0), ant("Ubiquiti", "PowerBeam M5-400", "dish", 1, 0), ant("Ubiquiti", "PowerBeam 5AC ISO Gen2", "dish", 1, 0),
		ant("Ubiquiti", "LiteBeam 5AC Gen2", "dish", 1, 0), ant("Ubiquiti", "LiteBeam 5AC LR", "dish", 1, 0), ant("Ubiquiti", "LiteBeam M5", "dish", 1, 0),
		ant("Ubiquiti", "LiteBeam 5AC XR", "dish", 1, 0), ant("Ubiquiti", "NanoBeam 5AC Gen2", "dish", 1, 0), ant("Ubiquiti", "NanoBeam 2AC-13", "dish", 1, 0),
		ant("Ubiquiti", "NanoBeam M5-16", "panel", 1, 0), ant("Ubiquiti", "NanoStation Loco 5AC", "panel", 1, 0), ant("Ubiquiti", "NanoStation Loco M5", "panel", 1, 0),
		ant("Ubiquiti", "NanoStation Loco M2", "panel", 1, 0), ant("Ubiquiti", "NanoStation M2", "panel", 2, 0), ant("Ubiquiti", "LiteAP AC (LAP-120)", "sector", 1, 0),
		ant("Ubiquiti", "LiteAP GPS", "sector", 1, 0), ant("Ubiquiti", "PrismStation 5AC", "panel", 1, 0), ant("Ubiquiti", "Rocket 5AC Lite", "panel", 1, 0),
		ant("Ubiquiti", "Rocket M5", "panel", 1, 0), ant("Ubiquiti", "Rocket M2", "panel", 1, 0), ant("Ubiquiti", "airMAX Sector AM-5G16-120", "sector", 0, 0),
		ant("Ubiquiti", "airMAX Sector AM-5G19-120", "sector", 0, 0), ant("Ubiquiti", "airMAX Sector AM-5G20-90", "sector", 0, 0), ant("Ubiquiti", "airMAX Sector AM-2G15-120", "sector", 0, 0),
		ant("Ubiquiti", "airMAX AC Sector 5AC-22-45", "sector", 0, 0), ant("Ubiquiti", "Horn 5-60", "sector", 0, 0), ant("Ubiquiti", "Horn 5-90", "sector", 0, 0),
		ant("Ubiquiti", "Omni AMO-5G10", "omni", 0, 0), ant("Ubiquiti", "Omni AMO-5G13", "omni", 0, 0), ant("Ubiquiti", "Omni AMO-2G10", "omni", 0, 0),
		ant("Ubiquiti", "Omni AMO-2G13", "omni", 0, 0), ant("Ubiquiti", "RocketDish 5G-30", "dish", 0, 0), ant("Ubiquiti", "RocketDish 5G-34", "dish", 0, 0),
		ant("Ubiquiti", "RocketDish LW 5G-31", "dish", 0, 0), ant("Ubiquiti", "airFiber 5XHD dish", "dish", 1, 1), ant("Ubiquiti", "airFiber 24", "dish", 1, 0),
		ant("Ubiquiti", "airFiber 24HD", "dish", 1, 0), ant("Ubiquiti", "airFiber 60", "dish", 1, 0), ant("Ubiquiti", "airFiber 60 XG", "dish", 1, 1),
		ant("Ubiquiti", "airFiber 5X", "panel", 1, 0), ant("Ubiquiti", "LTU Rocket", "panel", 1, 1), ant("Ubiquiti", "LTU Pro", "dish", 1, 0), ant("Ubiquiti", "LTU Lite", "dish", 1, 0),
		ant("Ubiquiti", "LTU Long-Range", "dish", 1, 0), ant("Ubiquiti", "LTU XR", "dish", 1, 0), ant("Ubiquiti", "Wave Pro", "dish", 1, 0), ant("Ubiquiti", "Wave Nano", "dish", 1, 0),
		ant("Ubiquiti", "Wave AP Micro", "sector", 1, 1), ant("Ubiquiti", "UniFi Building-to-Building Bridge XG", "dish", 1, 0),
		ant("MikroTik", "LHG 5", "dish", 1, 0), ant("MikroTik", "LHG HP5", "dish", 1, 0), ant("MikroTik", "LHG XL 5 ac", "dish", 1, 0), ant("MikroTik", "LHG XL HP5", "dish", 1, 0),
		ant("MikroTik", "LHG XL 52 ac", "dish", 1, 0), ant("MikroTik", "LHG 60G", "dish", 1, 0), ant("MikroTik", "LHG LTE6 kit", "dish", 1, 0), ant("MikroTik", "LDF 5 ac", "dish", 1, 0),
		ant("MikroTik", "SXTsq Lite5", "panel", 1, 0), ant("MikroTik", "SXTsq Lite2", "panel", 1, 0), ant("MikroTik", "SXT Lite5", "panel", 1, 0), ant("MikroTik", "SXT LTE6 kit", "panel", 1, 0),
		ant("MikroTik", "DynaDish 5", "dish", 1, 0), ant("MikroTik", "QRT 5 ac", "panel", 1, 0), ant("MikroTik", "Cube 60G ac", "panel", 1, 0), ant("MikroTik", "Cube Lite60", "panel", 1, 0),
		ant("MikroTik", "Wireless Wire Dish", "dish", 1, 1), ant("MikroTik", "Wireless Wire Cube Pro", "panel", 1, 0), ant("MikroTik", "nRAY 60G", "dish", 1, 1),
		ant("MikroTik", "mANTBox 2 12s", "sector", 1, 1), ant("MikroTik", "mANTBox 19s", "sector", 1, 1), ant("MikroTik", "mANTBox 52 15s ac", "sector", 1, 1),
		ant("MikroTik", "mANT 15s (antenna)", "sector", 0, 0), ant("MikroTik", "mANT 30 PA (dish)", "dish", 0, 0), ant("MikroTik", "OmniTIK 5", "omni", 5, 0),
		ant("MikroTik", "OmniTIK 5 PoE ac", "omni", 5, 0), ant("MikroTik", "OmniTIK U-5HnD", "omni", 5, 0), ant("MikroTik", "MTAD-5G-Dual-Omni", "omni", 0, 0),
		ant("MikroTik", "TOF-2400-8V-4 (omni)", "omni", 0, 0), ant("MikroTik", "BaseBox 5", "panel", 1, 1), ant("MikroTik", "BaseBox 2", "panel", 1, 1),
		ant("MikroTik", "NetBox 5", "panel", 1, 0), ant("MikroTik", "NetMetal 5", "panel", 1, 1), ant("MikroTik", "Groove 52 ac", "omni", 1, 0), ant("MikroTik", "GrooveA 52 ac", "omni", 1, 0),
		ant("MikroTik", "Metal 52 ac", "omni", 1, 0), ant("MikroTik", "KNOT LR8 kit", "panel", 1, 0),
		ant("Cambium", "ePMP Force 300-19", "dish", 1, 0), ant("Cambium", "ePMP Force 425", "dish", 1, 0), ant("Cambium", "ePMP 4500 sector", "sector", 1, 1),
		ant("TP-Link", "CPE605", "dish", 1, 0), ant("TP-Link", "CPE220", "panel", 1, 0), ant("TP-Link", "WBS510 base station", "panel", 1, 0), ant("TP-Link", "TL-ANT5819MS (sector)", "sector", 0, 0),
	)
	return ms
}
