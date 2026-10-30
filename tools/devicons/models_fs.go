package main

// FS (fs.com): campus, data centre and industrial switches, PoE and
// desktop switches, wireless, media converters and the like. The models
// already in models_more.go are not repeated here.

func catalogFS() []model {
	var ms []model
	add := func(m ...model) { ms = append(ms, m...) }
	rk := func(n, kind string, u, copper, sfp int) model {
		return model{vendor: "FS", name: n, kind: kind, form: "rack", u: u, copper: copper, sfp: sfp}
	}
	sw := func(n string, copper, sfp int) model { return rk(n, "switch", 1, copper, sfp) }
	sd := func(n string, copper, sfp int) model {
		return model{vendor: "FS", name: n, kind: "switch", form: "desk", copper: copper, sfp: sfp}
	}
	ap := func(n string, copper int) model {
		return model{vendor: "FS", name: n, kind: "accesspoint", form: "ceiling", copper: copper}
	}
	mini := func(n, kind string, ports int) model {
		return model{vendor: "FS", name: n, kind: kind, form: "mini", copper: ports}
	}

	// ---- access and campus switches (1G, 2.5G, PoE)
	add(
		sw("S3410-24TS-P", 24, 4), sw("S3410-48TS", 48, 4), sw("S3410-48TS-P", 48, 4), sw("S3410C-16TF", 16, 2), sw("S3410C-16TF-P", 16, 2),
		sw("S3900-24F4S", 0, 28), sw("S3900-48T6S-R", 48, 6), sw("S3910-24TF", 24, 4), sw("S3910-24TS-P", 24, 4), sw("S3910-48TS-P", 48, 4),
		sw("S3910-48TF", 48, 4), sw("S3260-16T4FP", 16, 4), sw("S3270-24TM", 24, 4), sw("S3270-48TM", 48, 4), sw("S3270-24TM-P", 24, 4),
		sw("S3270-48TM-P", 48, 4), sw("S3400-24T4FP", 24, 4), sw("S3400-48T4SP", 48, 4), sw("S2805S-24TF", 24, 4), sw("S2805S-48TF", 48, 4),
		sw("S3700-24T4F", 24, 4), sw("S3800-24F4S", 0, 28), sw("S3800-48T4S", 48, 4), sw("S5800-48T4S", 48, 4), sw("S5800-48F4S", 0, 52),
		sw("S5810-28FS", 4, 24), sw("S5810-48TS-P", 48, 4), sw("S5810-48FS", 0, 52), sw("S5860-24XB-U", 24, 8), sw("S5860-24XMG", 24, 4),
		sw("S5860-48XMG", 48, 6), sw("S5860-48XMG-U", 48, 6), sw("S5860-48SC", 0, 56), sw("S5870-48T6BC-U", 48, 6), sw("S5870-48MX6BC-U", 48, 6),
	)
	// ---- desktop and small switches
	add(
		sd("S2805S-8TF", 8, 2), sd("S2805S-8TF-P", 8, 2), sd("S3150-8T2F", 8, 2), sd("S3150-8T2FP-E", 8, 2), sd("S3260-8T2FP", 8, 2),
		sd("S1150-8T2F", 8, 2), sd("S1130-8T", 8, 0), sd("S1600-8T2S", 8, 2), sd("S2800S-8T", 8, 0),
		sd("S3410-10TF-P", 8, 2), sd("S5800-8TF12S", 8, 12), sd("S5860-8XMG-U", 8, 2), sd("S3910-8TF-P", 8, 2), sd("FMS-8S2T (fanless 10G)", 2, 8),
	)
	// ---- data centre: 10G / 25G / 40G / 100G / 400G
	add(
		sw("S5850-48T4Q", 48, 4), sw("S5850-32S2Q", 0, 34), sw("S5850-48S2Q4C", 0, 54), sw("S5860-20SQ-U", 0, 24),
		sw("N5850-48S6Q", 0, 54), sw("N5860-48SC-U", 0, 56), sw("N5860-48XMG", 48, 8), sw("N5860-48S6Q", 0, 54), sw("N5850-48X6C", 48, 6),
		sw("N8550-32C", 0, 32), sw("N8550-48B8C", 0, 56), sw("N8550-64C", 0, 64), sw("N8560-48BC", 0, 56), sw("N8560-64C", 0, 64),
		sw("N8570-32C", 0, 32), sw("N9550-32D", 0, 32), sw("N9550-64D", 0, 64), sw("N9600-64OD", 0, 64), sw("N8510-32C", 0, 32),
		sw("NC8200-4TD", 4, 2),
	)
	// ---- industrial (DIN rail, drawn as desktop)
	add(
		sd("IES3110-8TFP", 8, 2), sd("IES3110-8TF", 8, 2), sd("IES3220-8T4F", 8, 4), sd("IES5100-8TF", 8, 2), sd("IES5220-16T4F", 16, 4),
		sd("IES2105-5T", 5, 0), sd("IES2108-8T", 8, 0), sd("IES3210-6T2F (PoE)", 6, 2),
	)
	// ---- wireless
	add(
		ap("AP-N505", 1), ap("AP-W6D2", 1), ap("AP-W6T3", 2), ap("AP-W6D2-I (in-wall)", 2), ap("AP-W7T3", 2), ap("AP-W6D2-O (outdoor)", 1),
		rk("AC-224AP (controller)", "other", 1, 8, 2), rk("AC-1024 (controller)", "other", 1, 8, 4),
	)
	// ---- media converters, PoE injectors, security and controllers
	add(
		mini("UMC-1F1T (media converter)", "other", 1), mini("UMC-1S1T (10G media converter)", "other", 1), mini("UMC-1F1T-P (PoE media converter)", "other", 1),
		mini("PoE injector PIN-60W", "power", 1), mini("PoE extender PE-1T2T", "power", 3),
		rk("NSG-5220 (firewall)", "firewall", 1, 8, 2), rk("NSG-5320 (firewall)", "firewall", 1, 8, 4), rk("NSG-5520 (firewall)", "firewall", 1, 16, 8),
		rk("FS-ER-4510 (router)", "router", 1, 4, 2), rk("M6200-24MF (WDM)", "other", 1, 0, 24), rk("FHD-1UFCE (fiber enclosure)", "other", 1, 0, 24),
		rk("FHD-2UFCE (fiber enclosure)", "other", 2, 0, 48), rk("FHD-4UFCE (fiber enclosure)", "other", 4, 0, 96),
	)
	return ms
}
