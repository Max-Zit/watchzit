package web

import (
	"strings"
	"testing"
)

// FuzzParseState: the SSO state comes back from the browser; only states we
// signed may be accepted.
func FuzzParseState(f *testing.F) {
	f.Add(signState("next", "/devices"))
	f.Add("abc.def")
	f.Add(".")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		vals, ok := parseState(s)
		if ok && !strings.Contains(s, ".") {
			t.Fatalf("accepted an unsigned state %q -> %q", s, vals)
		}
	})
}

// FuzzParseIDPMetadata: SAML metadata is pasted or fetched from an identity
// provider; it must not panic.
func FuzzParseIDPMetadata(f *testing.F) {
	f.Add([]byte(`<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp"><IDPSSODescriptor/></EntityDescriptor>`))
	f.Add([]byte(`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "b">]><x>&a;</x>`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = parseIDPMetadata(b)
	})
}

// FuzzFormLists: ports and mail recipients are typed into forms.
func FuzzFormLists(f *testing.F) {
	f.Add("22, 80,443-445")
	f.Add("a@b.example, c@d.example")
	f.Add("-1,99999,,")
	f.Fuzz(func(t *testing.T, s string) {
		if ports, err := parsePorts(s); err == nil {
			for _, p := range ports {
				if p < 1 || p > 65535 {
					t.Fatalf("%q gave port %d", s, p)
				}
			}
		}
		_, _ = parseRecipients(s)
	})
}
