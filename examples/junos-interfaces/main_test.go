package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/lasseh/netconf"
)

func TestInterfaces(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"direct", `<rpc-reply><interface-information><physical-interface><name>ge-0/0/0\n</name><oper-status>up\n</oper-status></physical-interface></interface-information></rpc-reply>`},
		{"wrapped", `<rpc-reply><multi-routing-engine-results><multi-routing-engine-item><interface-information><physical-interface><name>ge-0/0/0</name><oper-status>up</oper-status></physical-interface></interface-information></multi-routing-engine-item></multi-routing-engine-results></rpc-reply>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := interfaces(&netconf.Reply{Raw: []byte(strings.ReplaceAll(tt.raw, `\n`, "\n"))})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d interfaces, want 1", len(got))
			}
			if got[0] != (physicalInterface{Name: "ge-0/0/0", Oper: "up"}) {
				t.Fatalf("unexpected interface: %+v", got[0])
			}
		})
	}
}

func TestInterfacesMissing(t *testing.T) {
	_, err := interfaces(&netconf.Reply{Raw: []byte(`<rpc-reply><ok/></rpc-reply>`)})
	if !errors.Is(err, netconf.ErrElementNotFound) {
		t.Fatalf("got %v, want ErrElementNotFound", err)
	}
}
