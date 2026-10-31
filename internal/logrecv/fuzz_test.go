package logrecv

import "testing"

// FuzzParseSyslog: no message may crash the receiver (it listens to anyone).
func FuzzParseSyslog(f *testing.F) {
	f.Add([]byte("<34>Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8"))
	f.Add([]byte("<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut=\"3\"] An application event"))
	f.Add([]byte("<0>"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _, _, _ = ParseSyslog(b)
	})
}
