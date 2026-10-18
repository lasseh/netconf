package netconf

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrHello is what Open fails with when the hello exchange does not make a
// session.  Look for it with errors.Is; the *HelloError that errors.As finds
// has what the device sent.
var ErrHello = errors.New("netconf: hello exchange failed")

// HelloError is the error of a hello exchange that failed: the device said
// no hello, said one a session cannot be made of, or said it together with
// errors.  It matches ErrHello, and its cause and the errors of the device
// are within reach of errors.Is and errors.As.
type HelloError struct {
	// Capabilities is what the device advertised, when it sent a hello.
	Capabilities []string

	// RPCErrors is the errors the device sent where its hello belongs, in
	// place of the hello or in front of it, warnings among them.  Junos
	// sends two to a user whose login class permits nothing, a warning and
	// "Restricted user session", and a hello behind them.
	RPCErrors RPCErrors

	// Text is what the device printed outside any element.  Junos prints
	// the two errors of a restricted user in words as well, and a device
	// that started something else than NETCONF prints what that says.
	Text string

	// Err is what went wrong.
	Err error
}

func (e *HelloError) Error() string {
	var b strings.Builder
	b.WriteString(ErrHello.Error())
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	for i := range e.RPCErrors {
		b.WriteString("; " + e.RPCErrors[i].Error())
	}
	if e.Text != "" {
		fmt.Fprintf(&b, "; the device said: %q", logValue(e.Text))
	}
	return b.String()
}

// Unwrap returns what the error is: a failed hello exchange, its cause, and
// the errors the device sent.
func (e *HelloError) Unwrap() []error {
	errs := []error{ErrHello, e.Err}
	if len(e.RPCErrors) > 0 {
		errs = append(errs, e.RPCErrors)
	}
	return errs
}

// greeting is what a device sent where its hello belongs.
type greeting struct {
	// hello is nil when there was none
	hello *helloMessage
	// errors is the RPC errors of the replies in the message, warnings
	// included
	errors RPCErrors
	// text is what is outside any element
	text string
}

// failed returns the error of a hello exchange that cause ended, with what
// the device sent.
func (g greeting) failed(cause error) *HelloError {
	e := &HelloError{RPCErrors: g.errors, Text: g.text, Err: cause}
	if g.hello != nil {
		e.Capabilities = g.hello.Capabilities
	}
	return e
}

// readGreeting goes through the first message of a device.  It is a hello by
// the protocol, and Junos can put replies with errors and plain text in
// front of it.
func readGreeting(raw []byte) (g greeting, err error) {
	var text strings.Builder
	defer func() { g.text = strings.TrimSpace(text.String()) }()

	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		begin := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return g, nil
		}
		if err != nil {
			// what is not XML is what the device printed
			text.Write(raw[begin:])
			return g, located(raw, d.InputOffset(), err)
		}

		switch tok := tok.(type) {
		case xml.CharData:
			text.Write(tok)
		case xml.StartElement:
			switch tok.Name.Local {
			case "hello":
				var hello helloMessage
				if err := d.DecodeElement(&hello, &tok); err != nil {
					return g, located(raw, d.InputOffset(), err)
				}
				g.hello = &hello
			case rpcReplyName.Local:
				if err := d.Skip(); err != nil {
					return g, located(raw, d.InputOffset(), err)
				}
				// a reply, to nothing: its errors are what it is for
				reply, err := parseReply(raw[begin:d.InputOffset()])
				var rpcErrs RPCErrors
				if err != nil && !errors.As(err, &rpcErrs) {
					return g, err
				}
				g.errors = append(append(g.errors, reply.Warnings...), rpcErrs...)
			default:
				if err := d.Skip(); err != nil {
					return g, located(raw, d.InputOffset(), err)
				}
			}
		}
	}
}

// refuses reports whether one of the errors is more than a warning.
func refuses(errs RPCErrors) bool {
	for i := range errs {
		if errs[i].Severity != SevWarning {
			return true
		}
	}
	return false
}
