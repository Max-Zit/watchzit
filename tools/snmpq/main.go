// Command snmpq queries an SNMP agent with WatchZit's own SNMP code (for
// development and lab tests). Secrets come from environment variables so
// they do not end up in shell history:
//
//	SNMPQ_COMMUNITY, SNMPQ_AUTH, SNMPQ_PRIV
//
//	snmpq -v 3 -u user -a SHA -x AES 192.168.1.1 walk 1.3.6.1.2.1.2.2.1.2
package main

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"time"

	"github.com/max-zit/watchzit/internal/snmp"
)

func main() {
	ver := flag.String("v", "2c", "version: 1, 2c or 3")
	user := flag.String("u", "", "SNMPv3 user")
	auth := flag.String("a", "", "SNMPv3 authentication: MD5, SHA, SHA-224, SHA-256, SHA-384, SHA-512")
	priv := flag.String("x", "", "SNMPv3 privacy: DES, AES, AES-192, AES-256")
	port := flag.Int("p", 161, "UDP port")
	flag.Parse()
	if flag.NArg() < 3 {
		fmt.Fprintln(os.Stderr, "usage: snmpq [flags] host get|walk OID...")
		os.Exit(2)
	}
	a, err := netip.ParseAddr(flag.Arg(0))
	if err != nil {
		fail(err)
	}
	cfg := snmp.Config{Community: os.Getenv("SNMPQ_COMMUNITY")}
	switch *ver {
	case "1":
		cfg.Version = 1
	case "2c":
		cfg.Version = 2
	case "3":
		cfg.Version = 3
		cfg.V3 = snmp.V3{User: *user, AuthProto: *auth, AuthPass: os.Getenv("SNMPQ_AUTH"), PrivProto: *priv, PrivPass: os.Getenv("SNMPQ_PRIV")}
	default:
		fail(fmt.Errorf("unknown version %q", *ver))
	}
	c, err := snmp.Dial(netip.AddrPortFrom(a, uint16(*port)), cfg) // #nosec G115 -- port flag
	if err != nil {
		fail(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var oids []snmp.OID
	for _, s := range flag.Args()[2:] {
		o, err := snmp.ParseOID(s)
		if err != nil {
			fail(err)
		}
		oids = append(oids, o)
	}
	start := time.Now()
	n := 0
	print := func(v snmp.Variable) error {
		n++
		fmt.Printf("%s = %s\n", v.OID, v.String())
		return nil
	}
	switch flag.Arg(1) {
	case "get":
		vs, err := c.Get(ctx, oids...)
		if err != nil {
			fail(err)
		}
		for _, v := range vs {
			_ = print(v)
		}
	case "walk":
		for _, o := range oids {
			if err := c.Walk(ctx, o, print); err != nil {
				fail(err)
			}
		}
	default:
		fail(fmt.Errorf("unknown command %q", flag.Arg(1)))
	}
	fmt.Fprintf(os.Stderr, "%d values in %v\n", n, time.Since(start).Round(time.Millisecond))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "snmpq:", err)
	os.Exit(1)
}
