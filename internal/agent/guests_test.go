package agent

import "testing"

func TestParseGuests(t *testing.T) {
	hv, err := parseHyperV([]byte(`[{"id":"a1","name":"web","state":"Running","cpu":7,"cpus":2,"mem":4294967296,"demand":2147483648,"startup":2147483648,"ips":["10.0.0.5","fe80::1","169.254.3.4"]},{"id":"b2","name":"Db","state":"Off","cpu":0,"cpus":4,"mem":0,"demand":0,"startup":1073741824,"ips":[]},{"id":"c3","name":"old","state":"Saved","ips":null}]`))
	if err != nil || len(hv.Items) != 3 {
		t.Fatalf("hyper-v: %v %+v", err, hv)
	}
	db, old, web := hv.Items[0], hv.Items[1], hv.Items[2]
	if web.State != "running" || web.MemPct != 50 || web.CPUPct != 7 || len(web.IPs) != 1 || web.IPs[0] != "10.0.0.5" {
		t.Fatalf("web: %+v", web)
	}
	if db.State != "stopped" || db.MemB != 1073741824 || old.State != "suspended" {
		t.Fatalf("db/old: %+v %+v", db, old)
	}
	d, err := parseDocker([]byte(`[{"Id":"0123456789abcdef","Names":["/nginx"],"Image":"nginx:1.27","State":"running","NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.2"}}}},{"Id":"ff","Names":["/job"],"Image":"busybox","State":"exited","NetworkSettings":{"Networks":{}}}]`), "27.3.1")
	if err != nil || d.Product != "Docker 27.3.1" || len(d.Items) != 2 {
		t.Fatalf("docker: %v %+v", err, d)
	}
	if n := d.Items[1]; n.Name != "nginx" || n.ID != "0123456789ab" || n.State != "running" || n.IPs[0] != "172.17.0.2" || n.OS != "nginx:1.27" {
		t.Fatalf("nginx: %+v", n)
	}
	if d.Items[0].State != "stopped" {
		t.Fatalf("job: %+v", d.Items[0])
	}
}
