package netconf

import (
	"testing"

	"github.com/carlmjohnson/be"
)

func TestCapabilitySetHas(t *testing.T) {
	cs := NewCapabilitySet(
		CapNetConf10,
		"\n    urn:ietf:params:netconf:capability:candidate:1.0\n  ",
		"urn:ietf:params:netconf:capability:url:1.0?scheme=http,ftp,file",
		"urn:ietf:params:netconf:capability:validate:1.1",
		"http://xml.juniper.net/netconf/junos/1.0",
		"urn:ietf:params:xml:ns:yang:ietf-inet-types?module=ietf-inet-types&revision=2013-07-15",
		"",
	)
	be.Equal(t, 6, cs.Len())

	tests := []struct {
		capability string
		want       bool
	}{
		{CapNetConf10, true},
		{CapNetConf11, false},
		{":candidate:1.0", true},
		{"urn:ietf:params:netconf:capability:candidate:1.0", true},
		{":url:1.0", true},
		{":url:1.0?scheme=http,ftp,file", true},
		{":url:1.0?scheme=sftp", false},
		{":candidate:1.0?x=y", false},
		{":xpath:1.0", false},
		// without a version, any version
		{":candidate", true},
		{":validate", true},
		{":validate:1.1", true},
		{":validate:1.0", false},
		{":url", true},
		{":url?scheme=http,ftp,file", false},
		{":xpath", false},
		{":valid", false},
		{"urn:ietf:params:netconf:base", true},
		{"http://xml.juniper.net/netconf/junos/1.0", true},
		{"http://xml.juniper.net/netconf/junos", false},
		{"urn:ietf:params:xml:ns:yang:ietf-inet-types", true},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.capability, func(t *testing.T) {
			be.Equal(t, tc.want, cs.Has(tc.capability))
		})
	}
}
