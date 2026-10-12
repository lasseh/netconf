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
		"",
	)
	be.Equal(t, 3, cs.Len())

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
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.capability, func(t *testing.T) {
			be.Equal(t, tc.want, cs.Has(tc.capability))
		})
	}
}
