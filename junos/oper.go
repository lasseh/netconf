package junos

import (
	"context"
	"encoding/xml"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
)

// Command is an operational mode CLI command such as "show version".
//
// The command is one CLI line.  Do not build it from untrusted input.  Junos
// does not apply filters such as "| match" to a command given this way; it
// runs the command and returns all of its output.
type Command string

// MarshalXML implements xml.Marshaler.  It asks for text output, which is
// what Exec reads.
func (c Command) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	return e.Encode(command{Format: FormatText, Line: string(c)})
}

type command struct {
	XMLName xml.Name `xml:"command"`
	Format  Format   `xml:"format,attr,omitempty"`
	Line    string   `xml:",chardata"`
}

// Exec runs the command and returns its output as shown by the CLI.
func (c Command) Exec(ctx context.Context, session *netconf.Session) (string, error) {
	reply, err := session.ExecReply(ctx, c)
	if err != nil {
		return "", operr.Named(commandName, err)
	}
	out, err := Text(reply)
	return out, operr.Named(commandName, err)
}

// commandName is what the errors of a Command begin with.
const commandName = "command"

// JSON runs the command and returns its output as a JSON document, see the
// JSON function for what that does to errors.
func (c Command) JSON(ctx context.Context, session *netconf.Session) ([]byte, error) {
	reply, err := session.ExecReply(ctx, command{Format: FormatJSON, Line: string(c)})
	if err != nil {
		return nil, operr.Named(commandName, err)
	}
	doc, err := JSON(reply)
	return doc, operr.Named(commandName, err)
}

// XML runs the command and returns the reply holding its output as XML, to
// be read with Decode or netconf.Reply.Element.
func (c Command) XML(ctx context.Context, session *netconf.Session) (*netconf.Reply, error) {
	reply, err := session.ExecReply(ctx, command{Line: string(c)})
	return reply, operr.Named(commandName, err)
}

// SystemInformation says what kind of device this is.
type SystemInformation struct {
	HostName     string `xml:"host-name"`
	Model        string `xml:"hardware-model"`
	OSName       string `xml:"os-name"`
	Version      string `xml:"os-version"`
	SerialNumber string `xml:"serial-number"`
}

// GetSystemInformation is the Junos <get-system-information> operation: the
// name, model, serial number and software version of a device in one
// request.
type GetSystemInformation struct{}

// MarshalXML implements xml.Marshaler.
func (GetSystemInformation) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	return e.Encode(struct {
		XMLName xml.Name `xml:"get-system-information"`
	}{})
}

// Exec issues the operation on session.
func (op GetSystemInformation) Exec(ctx context.Context, session *netconf.Session) (*SystemInformation, error) {
	const name = "get-system-information"

	reply, err := session.ExecReply(ctx, op)
	if err != nil {
		return nil, operr.Named(name, err)
	}

	var info SystemInformation
	if err := Decode(reply, "system-information", &info); err != nil {
		return nil, operr.Named(name, err)
	}
	return &info, nil
}
