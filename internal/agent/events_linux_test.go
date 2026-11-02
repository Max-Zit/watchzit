//go:build linux

package agent

import "testing"

func TestParseJournal(t *testing.T) {
	j := `{"__REALTIME_TIMESTAMP":"1793620800000000","PRIORITY":"3","SYSLOG_IDENTIFIER":"kernel","MESSAGE":"EXT4-fs error (device sda1)"}
{"__REALTIME_TIMESTAMP":"1793620801000000","PRIORITY":"2","_SYSTEMD_UNIT":"nginx.service","MESSAGE":[104,105]}
{"PRIORITY":"3","MESSAGE":""}
not json`
	evs := parseJournal([]byte(j))
	if len(evs) != 2 || evs[0].Source != "kernel" || evs[0].Level != 3 || evs[1].Source != "nginx.service" || evs[1].Message != "hi" || evs[1].Level != 2 {
		t.Fatalf("%+v", evs)
	}
	if evs[0].Time.Unix() != 1793620800 {
		t.Fatalf("time %v", evs[0].Time)
	}
}
