package snmpmon

import (
	"context"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

// VRRP-MIB (RFC 2787) and VRRPv3-MIB (RFC 6527): the state of each virtual
// router — initialize(1), backup(2), master(3).
var (
	vrrpOperState     = snmp.MustOID("1.3.6.1.2.1.68.1.3.1.3")
	vrrpv3OperationSt = snmp.MustOID("1.3.6.1.2.1.207.1.1.1.1.3")
)

// VRRP returns the HA role of a device from its virtual routers: master
// when it is master of any, backup when it is backup of some, "" when it
// has none.
func VRRP(ctx context.Context, cl *snmp.Client) (string, error) {
	state := ""
	for _, root := range []snmp.OID{vrrpOperState, vrrpv3OperationSt} {
		vs, err := cl.WalkAll(ctx, root)
		if err != nil {
			return "", err
		}
		for _, v := range vs {
			switch whole(v) {
			case 3:
				return store.HAMaster, nil
			case 2:
				state = store.HABackup
			}
		}
	}
	return state, nil
}
