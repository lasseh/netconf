// Package junos provides what a NETCONF client needs for Juniper devices
// beyond the standard protocol: the Junos-native operations, and help with
// the shapes Junos gives its replies.
//
// # Reading
//
// GetConfiguration returns the configuration as XML, text, set commands or
// JSON, CompareRollback a diff, GetCommitInformation the commit history,
// GetDatabaseStatusInformation who is editing the configuration, Command the
// output of a CLI command and GetSystemInformation what kind of device this
// is.
//
// Every other operational request is literal XML given to
// netconf.Session.ExecReply.  Decode then finds the result wherever Junos
// put it and decodes it without the line breaks Junos pads values with:
//
//	reply, err := session.ExecReply(ctx, "<get-route-engine-information/>")
//	if err != nil { ... }
//	var engines []struct {
//		Slot  int    `xml:"slot"`
//		State string `xml:"mastership-state"`
//		Model string `xml:"model"`
//	}
//	err = junos.Decode(reply, "route-engine", &engines)
//
// Add format="text" or format="json" to a request to get the CLI's text or
// JSON instead of XML, and read it with Text or JSON.  Both return an error
// for a request that failed, as far as Junos says so in what it sends.
//
// An error from the Exec of an operation begins with the name of the
// operation, "commit-configuration: netconf error: ...", and has the error
// of the session underneath, for errors.Is and errors.As.
//
// Text that is not ASCII reads as it was written, in every format.  Junos
// sends it a byte at a time; the session puts it back together, see the
// netconf package.
//
// # Changing configuration
//
// A Change is a whole change in one value.  Its Apply locks the candidate,
// loads the configuration, looks at what that changes, commits, and gives
// the candidate back whatever happened on the way:
//
//	result, err := junos.Change{
//		Format:  junos.FormatSet,
//		Config:  "set system ntp server 192.0.2.1",
//		Comment: "add ntp server",
//		Confirm: 5 * time.Minute,
//	}.Apply(ctx, session)
//	if err != nil { ... } // names the statement Junos refused and why
//	fmt.Println(result.Diff)
//
// It can work in a private copy of the candidate, stop after the commit
// check, ask a function of yours with the diff before it commits, and call
// another one between a confirmed commit and its confirmation.
//
// The operations it is made of are there for the sequences it does not
// cover: LoadConfiguration loads configuration into the candidate in any
// format, CompareRollback shows what that changed, CommitConfiguration
// checks or commits it, Rollback puts the candidate back, and
// OpenConfiguration gives the session a private copy of the candidate to do
// all that in, where the alternative is the candidate under rpc.Lock.
//
// The package is pinned by replies captured from four devices.  Junos 25.4
// on an EX4100 gave the reading operations, loading in every format, the
// commit check and the private copy.  Junos 26.2 on a vJunos-router gave
// commits, confirmed commits, the lock on the candidate and what all of
// them answer when somebody else is in the way.  Junos 26.2 on a
// vJunos-switch and Junos OS Evolved 26.2 on a vJunosEvolved gave the
// errors of requests made in JSON.  Change has committed on the last three
// from this library, and the default instance of the ephemeral database has
// been loaded, committed and read back there.
package junos

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/lasseh/netconf"
)

// Capability is the capability every Junos device advertises, and the one
// Juniper ties its extensions to the NETCONF protocol to.
const Capability = "http://xml.juniper.net/netconf/junos/1.0"

// IsJunos reports whether the device at the other end of session is a Junos
// device.
func IsJunos(session *netconf.Session) bool {
	return session.ServerCaps().Has(Capability)
}

// Format selects a representation: of a configuration, or of the output of
// an operational request.
type Format string

const (
	// FormatXML is the Junos XML representation, the default.
	FormatXML Format = "xml"
	// FormatText is what the CLI prints: a configuration in curly-brace
	// format, or the output of a command.
	FormatText Format = "text"
	// FormatSet is a configuration as a list of "set" commands.
	FormatSet Format = "set"
	// FormatJSON is the JSON representation.
	FormatJSON Format = "json"

	// FormatXMLMinified is a configuration as XML without the indentation,
	// which is half the bytes of a large one.  It is for GetConfiguration.
	// A release that does not know it answers in XML with indentation and
	// says nothing; GetConfiguration takes that for the failure it is.
	//
	// Not over chunked framing: Junos 26.2 never finishes such a reply
	// there.  GetConfiguration.Exec refuses the format on such a session,
	// and a request that is sent all the same, with Session.ExecReply, is
	// waited for until its context ends, with the session lost behind it.
	FormatXMLMinified Format = "xml-minified"
	// FormatJSONMinified is the same for JSON, a third of the bytes, with
	// the same limit.
	FormatJSONMinified Format = "json-minified"
)

// Text returns the output of a request made with format="text", as the CLI
// prints it.  A multi-chassis device answers once per member; the outputs
// are joined.
//
// Junos does not send an rpc-error for a text request that fails.  It
// writes the error into the reply as plain text, which Text returns as an
// error.  In rfc-compliant mode Junos 23.4 leaves that text out: the reply
// has nothing in it, and Text cannot tell it from the reply to a request
// that prints nothing.
func Text(reply *netconf.Reply) (string, error) {
	found, err := reply.Elements("output")
	if err != nil {
		return "", err
	}
	if len(found) > 0 {
		outputs := make([]string, len(found))
		for i, output := range found {
			outputs[i] = trimNewlines(output.Text)
		}
		return strings.Join(outputs, "\n"), nil
	}

	// A request that prints nothing has an empty reply.  Anything else in
	// there is not text output and must not read as "no output".
	var body struct {
		Text     string `xml:",chardata"`
		Children []struct {
			XMLName xml.Name
		} `xml:",any"`
	}
	if err := reply.Decode(&body); err != nil {
		return "", err
	}
	if len(body.Children) > 0 {
		return "", fmt.Errorf("junos: reply has <%s> instead of <output>", body.Children[0].XMLName.Local)
	}

	text := strings.TrimSpace(body.Text)
	if strings.HasPrefix(text, "error:") || strings.Contains(text, "\nerror:") {
		return "", fmt.Errorf("junos: %s", text)
	}
	return text, nil
}

// JSON returns the JSON document of a request made with format="json".
//
// A request that fails is answered in JSON too: Junos writes most errors
// into the document, which JSON returns as an error, together with the
// document for what else is in it.  A warning is written there the same
// way, and is no error: it stays in the document.  Some errors Junos leaves
// out.  Junos answers a request for an interface that is not there with a
// document without data, which cannot be told from a result without data,
// where Junos OS Evolved writes the error.  Ask in XML when that matters.
func JSON(reply *netconf.Reply) ([]byte, error) {
	// the document is the text of the reply itself
	var body struct {
		Text string `xml:",chardata"`
	}
	if err := reply.Decode(&body); err != nil {
		return nil, err
	}

	text := strings.TrimSpace(body.Text)
	switch {
	case text == "":
		return nil, errors.New("junos: reply has no JSON document")
	case text[0] != '{' && text[0] != '[':
		return nil, fmt.Errorf("junos: reply is not JSON: %s", firstLine(text))
	}

	doc := []byte(text)
	return doc, jsonError(doc)
}

// What a document with an error in it has: the name of one kind of error,
// and a member or an attribute of the other.
var jsonErrorMarks = [][]byte{[]byte(`"rpc-error"`), []byte(`"source-daemon"`), []byte(`"xmlns:xnm"`)}

// jsonError returns the errors Junos wrote into a JSON document.
//
// The error elements of a reply become members of the document, anywhere in
// it: an <rpc-error> one called "rpc-error", the error element of Junos one
// called "error".  Both are arrays of objects whose members are arrays of
// objects with the text in "data".
func jsonError(doc []byte) error {
	// most documents hold no error; those are not read a second time
	if !slices.ContainsFunc(jsonErrorMarks, func(mark []byte) bool { return bytes.Contains(doc, mark) }) {
		return nil
	}

	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		// whoever reads the document finds that out
		return nil
	}
	messages := jsonErrors(v, nil)
	if len(messages) == 0 {
		return nil
	}
	// an object has no order
	slices.Sort(messages)
	return fmt.Errorf("junos: error: %s", strings.Join(messages, "; "))
}

// jsonErrors adds what the errors in v say to messages.
func jsonErrors(v any, messages []string) []string {
	switch v := v.(type) {
	case []any:
		for _, item := range v {
			messages = jsonErrors(item, messages)
		}
	case map[string]any:
		for name, member := range v {
			items, isArray := member.([]any)
			if !isArray {
				messages = jsonErrors(member, messages)
				continue
			}
			for _, item := range items {
				if message, ok := jsonErrorIn(name, item); ok {
					messages = append(messages, message)
				} else {
					messages = jsonErrors(item, messages)
				}
			}
		}
	}
	return messages
}

// jsonErrorIn returns what item, of a member called name, says if it is an
// error.
func jsonErrorIn(name string, item any) (message string, ok bool) {
	object, ok := item.(map[string]any)
	if !ok {
		return "", false
	}

	daemon := jsonText(object, "source-daemon")
	switch name {
	case "rpc-error":
		if jsonText(object, "error-severity") == "warning" {
			return "", false
		}
		message = jsonText(object, "error-message")
	case "error":
		// "error" is the syslog severity in a configuration too, where its
		// value is [null].  An error names who reported it, or has the
		// namespace declarations of its element as attributes.
		attributes, _ := object["attributes"].(map[string]any)
		if _, declared := attributes["xmlns:xnm"]; !declared && object["source-daemon"] == nil {
			return "", false
		}
		message = jsonText(object, "message")
	default:
		return "", false
	}

	switch {
	case message != "":
	case daemon != "":
		message = "reported by " + daemon
	default:
		message = "without a message"
	}
	return message, true
}

// jsonText returns the text of the member called name: the "data" of the
// first object in its array.
func jsonText(object map[string]any, name string) string {
	items, _ := object[name].([]any)
	if len(items) == 0 {
		return ""
	}
	first, _ := items[0].(map[string]any)
	text, _ := first["data"].(string)
	return strings.TrimSpace(text)
}

// Decode decodes the element called name in the reply into v, or every such
// element when v points to a slice.
//
// It takes care of two things Junos does to replies.  The element is found
// at any depth: a device with several routing engines or chassis members
// wraps each one's answer in a <multi-routing-engine-item>, and which
// requests get that treatment differs between platforms.  And the strings of
// v come without the white space around them, as Junos pads many values with
// line breaks.  That is every string in a field encoding/xml decodes into,
// also one that v held before and the reply has nothing for.  A field it
// leaves alone, one that is not exported or has the tag `xml:"-"`, is not
// touched.
//
// A reply without the element fails with netconf.ErrElementNotFound.
func Decode(reply *netconf.Reply, name string, v any) error {
	target := reflect.ValueOf(v)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return errors.New("junos: Decode needs a pointer to decode into")
	}
	target = target.Elem()

	found, err := reply.Elements(name)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("%w: <%s>", netconf.ErrElementNotFound, name)
	}

	// one value, or one per match
	if target.Kind() != reflect.Slice || target.Type().Elem().Kind() == reflect.Uint8 {
		if err := found[0].Decode(v); err != nil {
			return err
		}
		trimStrings(target)
		return nil
	}

	all := reflect.MakeSlice(target.Type(), 0, len(found))
	for _, el := range found {
		item := reflect.New(target.Type().Elem())
		if err := el.Decode(item.Interface()); err != nil {
			return err
		}
		all = reflect.Append(all, item.Elem())
	}
	target.Set(all)
	trimStrings(target)
	return nil
}

// trimStrings removes the white space around every string reachable from v
// through what encoding/xml decodes into.  Byte slices are left as received.
func trimStrings(v reflect.Value) {
	// the pointers followed so far: what v held before the decoding may
	// point back at itself
	type pointer struct {
		to reflect.Type
		at uintptr
	}
	seen := make(map[pointer]struct{})

	var trim func(reflect.Value)
	trim = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			if v.CanSet() {
				v.SetString(strings.TrimSpace(v.String()))
			}
		case reflect.Pointer:
			if v.IsNil() {
				return
			}
			p := pointer{v.Type(), v.Pointer()}
			if _, ok := seen[p]; ok {
				return
			}
			seen[p] = struct{}{}
			trim(v.Elem())
		case reflect.Struct:
			for field, value := range v.Fields() {
				// the fields encoding/xml leaves alone are the caller's
				if field.Tag.Get("xml") == "-" || !field.IsExported() && !field.Anonymous {
					continue
				}
				trim(value)
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				return
			}
			for i := range v.Len() {
				trim(v.Index(i))
			}
		default:
		}
	}
	trim(v)
}

// trimNewlines removes the line breaks Junos puts around text content
// without touching its indentation.
func trimNewlines(s string) string {
	return strings.Trim(s, "\r\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
