package junos_test

import (
	"context"
	"testing"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf/junos"
)

// What Junos 26.2R1.7 on a vJunos-switch and Junos OS Evolved 26.2R1.7 on a
// vJunosEvolved answered to requests with format="json" that failed.  The
// error is in the document, in one of two forms and at any depth.
const (
	// Junos, <get-arp-table-information> for an interface that is not there.
	jsonRPCError = `{
    "rpc-error" : [
    {
        "error-type" : [
        {
            "data" : "protocol"
        }
        ], 
        "error-tag" : [
        {
            "data" : "operation-failed"
        }
        ], 
        "error-severity" : [
        {
            "data" : "error"
        }
        ], 
        "source-daemon" : [
        {
            "data" : "arp"
        }
        ], 
        "error-message" : [
        {
            "data" : "device not found"
        }
        ]
    }
    ]
}`

	// Evolved, <get-ethernet-switching-table-information>.
	jsonRPCErrorEvolved = `{
    "rpc-error" : [
    {
        "error-type" : [
        {
            "data" : "protocol"
        }
        ], 
        "error-tag" : [
        {
            "data" : "operation-failed"
        }
        ], 
        "error-severity" : [
        {
            "data" : "error"
        }
        ], 
        "error-message" : [
        {
            "data" : "the l2-learning subsystem is not running"
        }
        ]
    }
    ]
}`

	// Both, <get-bgp-neighbor-information> for an address that is none: the
	// error element of Junos.
	jsonErrorElement = `{
    "error" : [
    {
        "source-daemon" : [
        {
            "data" : "routing"
        }
        ], 
        "message" : [
        {
            "data" : "Invalid address specified."
        }
        ]
    }
    ]
}`

	// Evolved, "show interfaces" for an interface that is not there: the
	// error element inside the result.
	jsonErrorInResult = `{
    "interface-information" : [
    {
        "error" : [
        {
            "attributes" : {"xmlns" : "http://xml.juniper.net/xnm/1.1/xnm", 
                            "xmlns:xnm" : "http://xml.juniper.net/xnm/1.1/xnm"
                           }, 
            "source-daemon" : [
            {
                "data" : "ifmand"
            }
            ], 
            "message" : [
            {
                "data" : "device nonexistent-0/0/0 not found"
            }
            ]
        }
        ]
    }
    ]
}`

	// Evolved, <get-arp-table-information> for an interface that is not
	// there: two levels down.
	jsonErrorDeeper = `{
    "arp-table-information" : [
    {
        "arp-table-entry" : [
        {
            "error" : [
            {
                "attributes" : {"xmlns" : "http://xml.juniper.net/xnm/1.1/xnm", 
                                "xmlns:xnm" : "http://xml.juniper.net/xnm/1.1/xnm"
                               }, 
                "source-daemon" : [
                {
                    "data" : "arpd"
                }
                ], 
                "message" : [
                {
                    "data" : "interface not found"
                }
                ]
            }
            ]
        }
        ]
    }
    ]
}`

	// Evolved in rfc-compliant mode, the same request: an rpc-error that
	// names nobody and says nothing.
	jsonErrorSaysNothing = `{
    "rpc-error" : [
    {
        "error-type" : [
        {
            "data" : "protocol"
        }
        ], 
        "error-tag" : [
        {
            "data" : "operation-failed"
        }
        ], 
        "error-severity" : [
        {
            "data" : "error"
        }
        ], 
        "error-message" : [
        {
        }
        ]
    }
    ]
}`

	// Both, the routes received from a BGP peer that is none: no message.
	jsonErrorWithoutMessage = `{
    "error" : [
    {
        "source-daemon" : [
        {
            "data" : "routing"
        }
        ], 
        "message" : [
        {
        }
        ]
    }
    ]
}`
)

// Documents that are no errors.
const (
	// Both, <get-vrrp-information>: a warning.
	jsonWarning = `{
    "rpc-error" : [
    {
        "error-severity" : [
        {
            "data" : "warning"
        }
        ], 
        "error-message" : [
        {
            "data" : "vrrp subsystem not running - not needed by configuration."
        }
        ]
    }
    ]
}`

	// Junos, "show interfaces" for an interface that is not there: the error
	// is left out.
	jsonErrorLeftOut = `{
    "interface-information" : [
    {
    }
    ]
}`

	// Evolved, a configuration group in a private copy: the syslog severity
	// error, in a file called rpc-error.
	jsonSyslogSeverities = `{
    "configuration" : {
        "@" : {
            "xmlns" : "http://xml.juniper.net/xnm/1.1/xnm", 
            "junos:changed-seconds" : "1791619262", 
            "junos:changed-localtime" : "2026-10-10 10:01:02 CEST"
        }, 
        "groups" : [
        {
            "name" : "netconf-lib-probe", 
            "system" : {
                "syslog" : {
                    "file" : [
                    {
                        "name" : "rpc-error", 
                        "contents" : [
                        {
                            "name" : "any", 
                            "error" : [null]
                        }, 
                        {
                            "name" : "daemon", 
                            "warning" : [null]
                        }
                        ]
                    }
                    ]
                }
            }
        }
        ]
    }
}`
)

// A request that fails must not read as a document, where Junos says that it
// failed.
func TestJSONError(t *testing.T) {
	tests := []struct {
		name, doc, want string
	}{
		{"an rpc-error", jsonRPCError, "junos: error: device not found"},
		{"an rpc-error from evolved", jsonRPCErrorEvolved, "junos: error: the l2-learning subsystem is not running"},
		{"the error element", jsonErrorElement, "junos: error: Invalid address specified."},
		{"the error element inside the result", jsonErrorInResult, "junos: error: device nonexistent-0/0/0 not found"},
		{"the error element two levels down", jsonErrorDeeper, "junos: error: interface not found"},
		{"no message", jsonErrorWithoutMessage, "junos: error: reported by routing"},
		// made up: the error element as it is inside a result, without the
		// name of who reported it
		{
			"nobody named",
			`{"interface-information" : [{"error" : [{"attributes" : {"xmlns" : "http://xml.juniper.net/xnm/1.1/xnm", "xmlns:xnm" : "http://xml.juniper.net/xnm/1.1/xnm"}, "message" : [{"data" : "device et-0/0/9 not found"}]}]}]}`,
			"junos: error: device et-0/0/9 not found",
		},
		{"nothing said", jsonErrorSaysNothing, "junos: error: without a message"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess, _ := session(t, tc.doc+"\n")

			doc, err := junos.Command("show x").JSON(context.Background(), sess)
			be.Nonzero(t, err)
			be.Equal(t, "command: "+tc.want, err.Error())

			// what else Junos sent is still there to read
			be.Equal(t, tc.doc, string(doc))
		})
	}
}

func TestJSONNoError(t *testing.T) {
	for name, want := range map[string]string{
		"a warning":               jsonWarning,
		"an error Junos left out": jsonErrorLeftOut,
	} {
		t.Run(name, func(t *testing.T) {
			sess, _ := session(t, want+"\n")

			doc, err := junos.Command("show x").JSON(context.Background(), sess)
			be.NilErr(t, err)
			be.Equal(t, want, string(doc))
		})
	}

	// "error" is what the severity is called, and its value is no object
	t.Run("a configuration", func(t *testing.T) {
		sess, _ := session(t, jsonSyslogSeverities+"\n")

		doc, err := junos.GetConfiguration{Format: junos.FormatJSON}.Exec(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, jsonSyslogSeverities, string(doc))
	})

	// Made up: members that are called like an error and are none.
	t.Run("lookalikes", func(t *testing.T) {
		const want = `{"rpc-error" : [null], "error" : [{"message" : [{"data" : "of something else"}]}], "source-daemon" : "x"}`
		sess, _ := session(t, want+"\n")

		doc, err := junos.Command("show x").JSON(context.Background(), sess)
		be.NilErr(t, err)
		be.Equal(t, want, string(doc))
	})
}
