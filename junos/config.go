package junos

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/internal/operr"
	"github.com/lasseh/netconf/internal/xmlutil"
)

// GetConfiguration is the Junos <get-configuration> operation.  Unlike the
// standard <get-config> it can return the configuration in the formats the
// CLI offers.
//
// It reads the active configuration, the one the device runs.
type GetConfiguration struct {
	// Format of the returned configuration.  The default is FormatXML.
	Format Format

	// Candidate reads the candidate configuration instead, which includes
	// changes nobody has committed yet: this session's or another user's.
	// When the session has opened a database of its own with
	// OpenConfiguration, that one is read.
	Candidate bool

	// Inherit shows the configuration with its groups and interface ranges
	// applied, the way the device uses it.
	Inherit bool

	// Filter limits the reply to part of the hierarchy, given as a
	// <configuration> element: a string or []byte holding literal XML or a
	// value that marshals to it.  Nil requests the whole configuration.
	Filter any
}

// MarshalXML implements xml.Marshaler.  It fails for an unknown Format.
func (op GetConfiguration) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	switch op.Format {
	case "", FormatXML, FormatText, FormatSet, FormatJSON, FormatXMLMinified, FormatJSONMinified:
	default:
		return fmt.Errorf("get-configuration: unknown format %q", op.Format)
	}

	start := xml.StartElement{Name: xml.Name{Local: "get-configuration"}}
	attr := func(name, value string) {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: value})
	}
	if op.Format != "" {
		attr("format", string(op.Format))
	}
	// Junos reads the candidate unless told otherwise
	if !op.Candidate {
		attr("database", "committed")
	}
	if op.Inherit {
		attr("inherit", "inherit")
	}

	return xmlutil.EncodePayload(e, start, op.Filter)
}

// Exec returns the configuration in the requested format: the
// <configuration> element for FormatXML, the JSON document for FormatJSON
// and plain text otherwise.
//
// It is bytes in every format, as XML and JSON are for a decoder to read;
// text and set commands are a string(...) away.  CompareRollback and
// Command, which only ever have text to give, return a string.
//
// Junos does not refuse a format it does not know.  It answers in XML with
// indentation, the whole configuration when there is no filter, and Exec
// fails when what came is not what was asked for: no JSON document where
// JSON was to come, no element of the format, and for FormatXMLMinified a
// <configuration> that is indented all the same.
//
// The XML is the element as it is in the reply.  Its junos: attributes rely
// on the namespace declared on the enclosing <rpc-reply>, which encoding/xml
// accepts but a namespace-strict parser does not.  To read them, when the
// configuration was committed and by whom, give the operation to
// netconf.Session.ExecReply: the Element called "configuration" of that
// reply has them in its Attr, with their namespace.
//
// On a session with chunked framing Exec refuses FormatXMLMinified and
// FormatJSONMinified and sends nothing: Junos 26.2 starts such a reply
// there and never finishes it, and the session is lost to the wait.
func (op GetConfiguration) Exec(ctx context.Context, session *netconf.Session) ([]byte, error) {
	config, err := op.exec(ctx, session)
	return config, operr.Named("get-configuration", err)
}

func (op GetConfiguration) exec(ctx context.Context, session *netconf.Session) ([]byte, error) {
	if (op.Format == FormatXMLMinified || op.Format == FormatJSONMinified) && chunked(session) {
		return nil, fmt.Errorf("format %q is not asked for over chunked framing: Junos starts such a reply and does not finish it, which costs the session", string(op.Format))
	}

	reply, err := session.ExecReply(ctx, op)
	if err != nil {
		return nil, err
	}

	var element string
	switch op.Format {
	case FormatJSON:
		return JSON(reply)
	case FormatJSONMinified:
		doc, err := JSON(reply)
		if err != nil {
			// no JSON: the configuration as XML is what a release that
			// does not know the format sends
			if _, xmlErr := reply.Element("configuration"); xmlErr == nil {
				return nil, otherFormat(op.Format)
			}
		}
		return doc, err
	case FormatText:
		element = "configuration-text"
	case FormatSet:
		element = "configuration-set"
	default:
		element = "configuration"
	}

	found, err := reply.Element(element)
	if err != nil {
		return nil, err
	}
	if element != "configuration" {
		return []byte(trimNewlines(found.Text)), nil
	}
	// Indentation begins with a line break behind the start tag.  One
	// further in is part of a value.
	if op.Format == FormatXMLMinified && len(found.Inner) > 0 && found.Inner[0] == '\n' {
		return nil, otherFormat(op.Format)
	}
	return found.Raw, nil
}

// chunked reports whether session speaks chunked framing, which it does when
// both sides of its hello exchange advertised base:1.1.
func chunked(session *netconf.Session) bool {
	return session.ServerCaps().Has(netconf.CapNetConf11) && session.ClientCaps().Has(netconf.CapNetConf11)
}

// otherFormat is the error of a configuration that came in another format
// than the one asked for.
func otherFormat(asked Format) error {
	return fmt.Errorf("the device answered in another format than %q: its release may not know that one", string(asked))
}

// CompareRollback returns the differences between the candidate
// configuration and a previously committed one in patch format, like the
// CLI's "show | compare rollback N".  Rollback 0 is the active
// configuration, so the zero value shows what a commit would change.
type CompareRollback struct {
	Rollback int
}

// MarshalXML implements xml.Marshaler.  It fails for a negative Rollback.
func (op CompareRollback) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if op.Rollback < 0 {
		return fmt.Errorf("compare rollback: invalid rollback number %d", op.Rollback)
	}
	req := struct {
		XMLName  xml.Name `xml:"get-configuration"`
		Compare  string   `xml:"compare,attr"`
		Rollback int      `xml:"rollback,attr"`
		Format   Format   `xml:"format,attr"`
	}{
		Compare:  "rollback",
		Rollback: op.Rollback,
		Format:   FormatText,
	}
	return e.Encode(&req)
}

// Exec returns the patch, which is empty when the configurations are equal.
func (op CompareRollback) Exec(ctx context.Context, session *netconf.Session) (string, error) {
	const name = "compare rollback"

	reply, err := session.ExecReply(ctx, op)
	if err != nil {
		return "", operr.Named(name, err)
	}

	// An empty patch must come from the device saying so, never from a
	// reply we did not understand.
	found, err := reply.Element("configuration-output")
	if err != nil {
		return "", operr.Named(name, err)
	}
	return trimNewlines(found.Text), nil
}

// LoadAction says how loaded configuration is combined with what the
// candidate already holds.
type LoadAction string

const (
	// LoadMerge adds the loaded statements to the candidate and overrides
	// those that conflict.  It is the default.
	LoadMerge LoadAction = "merge"
	// LoadOverride replaces the whole candidate.
	LoadOverride LoadAction = "override"
	// LoadReplace replaces the hierarchies marked for it, with "replace:" in
	// text or replace="replace" in XML, and merges the rest.
	LoadReplace LoadAction = "replace"
	// LoadUpdate takes a complete configuration and changes only what
	// differs from the candidate, so only the affected processes are told.
	LoadUpdate LoadAction = "update"
	// LoadPatch takes the differences between two configurations, in the
	// form CompareRollback returns and "show | compare" prints, and makes
	// them in the candidate.  A patch is text, whatever Format says.
	//
	// Junos has only warnings for a patch that does not fit the candidate,
	// one that takes out a statement which is not there: it loads none of
	// it and leaves out the <ok/>, which is what fails the load.  What is no
	// patch at all it calls noise and takes for a patch that changes
	// nothing: such a load goes through, with a warning in the reply.
	LoadPatch LoadAction = "patch"
)

// LoadConfiguration is the Junos <load-configuration> operation: it loads
// configuration into the candidate without committing it.  It is the way to
// load set commands, text or JSON; the standard <edit-config> takes XML.
// It also loads a patch, the output of a compare, and a file that Junos
// reads itself, see URL.
type LoadConfiguration struct {
	// Format of Config.  The default is FormatXML.
	Format Format

	// Action says how Config is combined with the candidate, LoadMerge by
	// default.  It has no say with FormatSet, where each command says what
	// it does: set, delete, deactivate, rename and so on.
	Action LoadAction

	// Config is the configuration to load.  For FormatXML that is a
	// <configuration> element: a string or []byte holding literal XML, or a
	// value that marshals to it.  For the other formats it is the text, as
	// a string or []byte.
	//
	// In a JSON string Junos reads the escapes \" and \\ and stores every
	// other one as written, backslash included.  So a JSON document is sent
	// with the escapes that have another spelling written that way: \u00e5
	// as å, \/ as /, and \u003c as <, which is how encoding/json writes <,
	// > and &.  The document means the same.  What is left are the control
	// characters, which JSON can only write as an escape: a string with a
	// line break in it, \n, is in the configuration with a backslash and an
	// n.  A document that is not JSON is sent as it is.
	//
	// For LoadPatch it is the patch, as a string or []byte.
	Config any

	// URL has Junos load a file in place of Config: a path on the device,
	// "/var/tmp/golden.conf".  Juniper documents ftp and http locations for
	// it as well, which were not run here.  Format and Action say what is in
	// the file and what to do with it, as they do for Config.  Junos answers
	// a file it cannot open with "Cannot open configuration file".
	URL string
}

// MarshalXML implements xml.Marshaler.  It fails for an unknown Format or
// Action, a combination Junos does not have, a missing Config, and a Config
// together with a URL.
func (op LoadConfiguration) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	switch {
	case op.Config == nil && op.URL == "":
		return errors.New("load-configuration: config cannot be nil")
	case op.Config != nil && op.URL != "":
		return errors.New("load-configuration: give Config or URL, not both")
	}
	switch op.Action {
	case "", LoadMerge, LoadOverride, LoadReplace, LoadUpdate, LoadPatch:
	default:
		return fmt.Errorf("load-configuration: unknown action %q", op.Action)
	}

	start := xml.StartElement{Name: xml.Name{Local: "load-configuration"}}
	attr := func(name, value string) {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: value})
	}
	if op.URL != "" {
		attr("url", op.URL)
	}

	// each format has its own element for the configuration
	var element string
	switch {
	case op.Action == LoadPatch:
		// a patch is text and has an element of its own
		if op.Format != "" && op.Format != FormatText {
			return fmt.Errorf("load-configuration: a patch is text, not %s", op.Format)
		}
		attr("action", "patch")
		attr("format", "text")
		element = "configuration-patch"
	case op.Format == "" || op.Format == FormatXML:
	case op.Format == FormatText:
		attr("format", "text")
		element = "configuration-text"
	case op.Format == FormatJSON:
		if op.Action == LoadReplace {
			return errors.New("load-configuration: JSON has no way to mark what to replace")
		}
		attr("format", "json")
		element = "configuration-json"
	case op.Format == FormatSet:
		if op.Action != "" {
			return fmt.Errorf("load-configuration: action %q does not apply to set commands", op.Action)
		}
		// set commands are an action of their own, in text format
		attr("action", "set")
		attr("format", "text")
		element = "configuration-set"
	default:
		return fmt.Errorf("load-configuration: unknown format %q", op.Format)
	}
	if op.Action != "" && op.Action != LoadPatch {
		attr("action", string(op.Action))
	}

	// a file is named and not sent
	if op.URL != "" {
		return xmlutil.EncodePayload(e, start, nil)
	}
	if element == "" {
		return xmlutil.EncodePayload(e, start, op.Config)
	}

	// everything but XML is text, and has to be escaped as such
	var text string
	switch config := op.Config.(type) {
	case string:
		text = config
	case []byte:
		text = string(config)
	default:
		what := string(op.Format) + " configuration"
		if op.Action == LoadPatch {
			what = "a patch"
		}
		return fmt.Errorf("load-configuration: %s must be a string or []byte, not %T", what, op.Config)
	}
	if op.Format == FormatJSON {
		text = junosJSON(text)
	}

	return xmlutil.EncodePayload(e, start, "<"+element+">"+textEscaper.Replace(text)+"</"+element+">")
}

// textEscaper escapes what character data cannot hold and nothing more.
// encoding/xml would also turn every line break and quote into a character
// reference, which Junos reads back correctly but which makes a trace of a
// set of commands unreadable.
var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// junosJSON returns the JSON document doc in the spelling Junos reads.  In a
// string Junos 26.2 takes \" and \\ for what they stand for and stores every
// other escape as written, backslash included.  So the escapes that have
// another spelling are written that way: \uXXXX as the character it stands
// for, \/ as the slash.  A control character has none and stays escaped.
//
// What is not JSON goes out as it came: saying so is up to Junos.
func junosJSON(doc string) string {
	// most documents have nothing to spell out
	if !strings.Contains(doc, `\u`) && !strings.Contains(doc, `\/`) || !json.Valid([]byte(doc)) {
		return doc
	}

	var b strings.Builder
	b.Grow(len(doc))
	for {
		// a backslash in a JSON document begins an escape in a string
		i := strings.IndexByte(doc, '\\')
		if i < 0 {
			b.WriteString(doc)
			return b.String()
		}
		b.WriteString(doc[:i])
		doc = doc[i+spellOut(&b, doc[i:]):]
	}
}

// spellOut writes the escape that s begins with to b the way Junos reads it
// and returns how much of s that was.
func spellOut(b *strings.Builder, s string) int {
	switch s[1] {
	case 'u':
	case '/':
		b.WriteByte('/')
		return 2
	default:
		b.WriteString(s[:2])
		return 2
	}

	r, size := escapedRune(s)
	switch {
	case r < 0x20, r == 0xFFFE, r == 0xFFFF, utf16.IsSurrogate(r):
		// JSON has no other spelling for a control character, and the
		// XML that carries the document cannot hold the others: two that
		// are no characters, and half a surrogate pair
		b.WriteString(s[:size])
	case r == '"', r == '\\':
		b.WriteByte('\\')
		b.WriteRune(r)
	default:
		b.WriteRune(r)
	}
	return size
}

// escapedRune reads the \uXXXX escape that s begins with, together with the
// one after it when the two are a surrogate pair.
func escapedRune(s string) (r rune, size int) {
	unit := func(s string) rune {
		v, _ := strconv.ParseUint(s[2:6], 16, 16)
		return rune(v)
	}

	r = unit(s)
	if utf16.IsSurrogate(r) && strings.HasPrefix(s[6:], `\u`) {
		if pair := utf16.DecodeRune(r, unit(s[6:])); pair != utf8.RuneError {
			return pair, 12
		}
	}
	return r, 6
}

// Exec loads the configuration.  When Junos rejects part of it the error
// names the statement.  The reply carries the warnings of a load that went
// through, such as "statement not found" for a delete of nothing.
//
// A load that fails may still have changed the candidate.  Junos runs set
// commands one by one and keeps the ones that worked, so after an error the
// candidate has to be put back: with Rollback(0), or by closing the private
// copy.  Change.Apply does that.
//
// Junos answers a load that went through with <ok/>, and Exec fails without
// it.
func (op LoadConfiguration) Exec(ctx context.Context, session *netconf.Session) (*netconf.Reply, error) {
	return execOK(ctx, session, op, "load-configuration")
}

// execOK issues a load or a commit and holds Junos to the <ok/> with which it
// answers one that it carried out.  A reply without it is not always an
// error to Junos: a commit from a private copy that no longer fits the
// configuration, because somebody else committed in the meantime, is
// answered with a warning for each statement and a count of them, and
// commits nothing.
//
// The count is what makes such a reply a refusal.  The error then holds the
// warnings as netconf.RPCErrors, like the error of an operation that Junos
// rejects.  A reply with warnings and no count has not been seen from a
// device and says nothing certain, so its error holds their text only.
//
// name is the name of the operation, which every error begins with.
func execOK(ctx context.Context, session *netconf.Session, op any, name string) (*netconf.Reply, error) {
	reply, err := session.ExecReply(ctx, op)
	if err != nil {
		return reply, operr.Named(name, err)
	}

	_, err = reply.Element("ok")
	if err == nil {
		return reply, nil
	}
	_, counted := reply.Element("load-error-count")
	switch {
	case len(reply.Warnings) == 0:
	case counted == nil:
		err = fmt.Errorf("%w: %w", err, reply.Warnings)
	default:
		// the text, not the errors: this is no refusal
		err = fmt.Errorf("%w: %s", err, reply.Warnings.Error())
	}
	return reply, operr.Named(name, err)
}

// LoadRescue loads the rescue configuration into the candidate, the one that
// "request system configuration rescue save" has put aside, like the CLI's
// "rollback rescue".  Nothing is committed.
type LoadRescue struct{}

// MarshalXML implements xml.Marshaler.
func (LoadRescue) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	return e.Encode(struct {
		XMLName xml.Name `xml:"load-configuration"`
		Rescue  string   `xml:"rescue,attr"`
	}{Rescue: "rescue"})
}

// Exec loads the configuration.  A device that has no rescue configuration
// fails with "Cannot open configuration file: /config/rescue.conf.gz".
func (op LoadRescue) Exec(ctx context.Context, session *netconf.Session) (*netconf.Reply, error) {
	return execOK(ctx, session, op, "load rescue")
}

// Rollback loads a previously committed configuration into the candidate,
// like the CLI's "rollback N".  Rollback 0 is the active configuration, so
// it drops the changes in the candidate.  Nothing is committed.
type Rollback int

// MarshalXML implements xml.Marshaler.  It fails outside the 50
// configurations Junos keeps.
func (r Rollback) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	if r < 0 || r > 49 {
		return fmt.Errorf("rollback: invalid rollback number %d", int(r))
	}
	return e.Encode(struct {
		XMLName  xml.Name `xml:"load-configuration"`
		Rollback int      `xml:"rollback,attr"`
	}{Rollback: int(r)})
}

// Exec loads the configuration.  A number Junos has no configuration for
// fails with "Cannot open configuration file".  Junos 26.2 then answers the
// next load of set commands on the session with a syntax error and loads
// nothing; Rollback(0) or one more load gets it back in step.
func (r Rollback) Exec(ctx context.Context, session *netconf.Session) (*netconf.Reply, error) {
	return execOK(ctx, session, r, "rollback")
}

// CommitConfiguration is the Junos <commit-configuration> operation.  The
// zero value commits the candidate.  The standard <commit> does that too;
// this one can also check without committing, record a comment and
// synchronize routing engines.
type CommitConfiguration struct {
	// Check validates the candidate and commits nothing.  It is not
	// without effect on a device where a confirmed commit is pending: Junos
	// takes a check for the confirmation, from any session, as it takes a
	// commit.
	Check bool

	// Comment is recorded with the commit, where "show system commit" and
	// the next person can read it.  Junos takes 512 bytes of it: a commit
	// with a longer one is refused by Junos 26.2, and is not sent.
	Comment string

	// Confirm makes this a confirmed commit: Junos activates the
	// configuration and goes back to the previous one after this time,
	// unless a commit or a commit check confirms it first.  That does not
	// have to come from this session: Junos 26.2 took a check from another
	// one for it.  A session that ends does not take the commit back before
	// its time.  It is what protects against a change that cuts off the
	// device.
	//
	// Junos counts in minutes.  The time is rounded up to the next one.
	// A private copy of the candidate cannot be committed this way.
	Confirm time.Duration

	// Synchronize commits on both routing engines.
	Synchronize bool

	// ForceSynchronize does the same and overrides what stands in the way
	// on the other routing engine, such as uncommitted changes there.
	ForceSynchronize bool

	// Full has every process read the whole configuration again, and not
	// only those the change concerns, like the CLI's "commit full".  It is
	// what operators reach for after an upgrade, or when a process has not
	// picked up a change.
	Full bool
}

// maxComment is the length in bytes of the longest commit comment Junos
// takes.
const maxComment = 512

// writtenLen returns the length in bytes of text as encoding/xml writes it
// and a device reads it: every byte that is no UTF-8 and every character
// XML does not allow goes out as U+FFFD, which is three bytes.
func writtenLen(text string) int {
	n := 0
	for _, r := range text {
		// a byte that is no UTF-8 ranges as U+FFFD too
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 {
			n += utf8.RuneLen(r)
		} else {
			n += len("\uFFFD")
		}
	}
	return n
}

// MarshalXML implements xml.Marshaler.  It fails for combinations Junos does
// not have: a check that is confirmed or forced, a confirmation time that
// is negative or beyond what Junos takes, and a comment that is too long.
func (op CommitConfiguration) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	minutes := int64(math.Ceil(op.Confirm.Minutes()))
	switch {
	case op.Confirm < 0:
		return errors.New("commit-configuration: confirm time cannot be negative")
	case minutes > 65535:
		return fmt.Errorf("commit-configuration: confirm time %v is more than Junos takes", op.Confirm)
	case op.Check && op.Confirm > 0:
		return errors.New("commit-configuration: a check commits nothing, so there is nothing to confirm")
	case op.Check && op.ForceSynchronize:
		return errors.New("commit-configuration: force-synchronize cannot be combined with check")
	case op.Confirm > 0 && op.ForceSynchronize:
		return errors.New("commit-configuration: force-synchronize cannot be combined with a confirmed commit")
	}
	if n := writtenLen(op.Comment); n > maxComment {
		return fmt.Errorf("commit-configuration: the comment is %d bytes long, and Junos takes %d", n, maxComment)
	}

	type flag *struct{}
	set := func(on bool) flag {
		if on {
			return &struct{}{}
		}
		return nil
	}

	return e.Encode(struct {
		XMLName          xml.Name `xml:"commit-configuration"`
		Check            flag     `xml:"check"`
		Confirmed        flag     `xml:"confirmed"`
		ConfirmTimeout   int64    `xml:"confirm-timeout,omitempty"`
		Synchronize      flag     `xml:"synchronize"`
		ForceSynchronize flag     `xml:"force-synchronize"`
		Full             flag     `xml:"full"`
		Log              string   `xml:"log,omitempty"`
	}{
		Check:          set(op.Check),
		Confirmed:      set(op.Confirm > 0),
		ConfirmTimeout: minutes,
		// forcing implies synchronizing
		Synchronize:      set(op.Synchronize || op.ForceSynchronize),
		ForceSynchronize: set(op.ForceSynchronize),
		Full:             set(op.Full),
		Log:              op.Comment,
	})
}

// Exec checks or commits.  When Junos rejects the configuration the error
// lists what is wrong with it, each statement with its place.  The reply
// carries the warnings of a commit that went through.
//
// Junos answers a commit that went through with <ok/>, and Exec fails
// without it.  That is what tells of a private copy that no longer fits:
// when somebody else has committed another value for a statement the copy
// changes, Junos commits nothing and has only a warning for it, "statement
// does not match patch", and a count of such statements.  The error is a
// netconf.ErrElementNotFound then, with the warnings in it as
// netconf.RPCErrors.  Junos has brought the copy up to date by that time,
// so the same commit again goes through and replaces what the other one
// committed.  Close the copy and make the change anew to see what it does
// to the configuration as it is now.
func (op CommitConfiguration) Exec(ctx context.Context, session *netconf.Session) (*netconf.Reply, error) {
	return execOK(ctx, session, op, "commit-configuration")
}

// OpenConfiguration gives the session a configuration database of its own
// to work in: exactly one of the fields says which.  LoadConfiguration,
// CommitConfiguration, GetConfiguration with Candidate and the standard
// operations on the candidate then act on it, until CloseConfiguration or
// the end of the session.
//
// One standard operation does not: Junos refuses <validate> in a session
// with a private copy ("private edits in use").  CommitConfiguration with
// Check has Junos check the copy.
//
// The ephemeral database is another matter than a copy of the candidate.
// What is committed there is no part of the configuration, which
// GetConfiguration without Candidate goes on to show without it, and makes
// no entry in the commit history.  The session that has the database open
// reads it with GetConfiguration and Candidate.  It takes no configuration
// groups, no confirmed commit and no CompareRollback, so Change is not for
// it.
//
// Junos gives an instance to one session at a time.  An OpenConfiguration
// for one that another session has open is not refused: it waits until that
// session closes it.  Give it a context with a deadline, and end the session
// when that runs out, as Junos may still open the database for it later.
// An instance that nobody has configured opens like one that is.
type OpenConfiguration struct {
	// Private opens a private copy of the candidate, like the CLI's
	// "configure private".  Changes other users make to the shared
	// candidate stay out of it, and a commit applies this session's changes
	// only.
	Private bool

	// Ephemeral opens the default instance of the ephemeral database,
	// EphemeralInstance the instance with that name.
	Ephemeral         bool
	EphemeralInstance string
}

// MarshalXML implements xml.Marshaler.  It fails unless exactly one database
// is asked for.
func (op OpenConfiguration) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	asked := 0
	for _, on := range []bool{op.Private, op.Ephemeral, op.EphemeralInstance != ""} {
		if on {
			asked++
		}
	}
	if asked != 1 {
		return errors.New("open-configuration: name exactly one database to open")
	}

	type flag *struct{}
	set := func(on bool) flag {
		if on {
			return &struct{}{}
		}
		return nil
	}
	return e.Encode(struct {
		XMLName           xml.Name `xml:"open-configuration"`
		Private           flag     `xml:"private"`
		Ephemeral         flag     `xml:"ephemeral"`
		EphemeralInstance string   `xml:"ephemeral-instance,omitempty"`
	}{
		Private:           set(op.Private),
		Ephemeral:         set(op.Ephemeral),
		EphemeralInstance: op.EphemeralInstance,
	})
}

// Exec opens the database.
func (op OpenConfiguration) Exec(ctx context.Context, session *netconf.Session) error {
	// Junos answers with an empty reply, there is no <ok/> to wait for
	_, err := session.ExecReply(ctx, op)
	return operr.Named("open-configuration", err)
}

// CloseConfiguration closes the database opened with OpenConfiguration and
// drops what was not committed from it.
type CloseConfiguration struct{}

// MarshalXML implements xml.Marshaler.
func (CloseConfiguration) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	return e.Encode(struct {
		XMLName xml.Name `xml:"close-configuration"`
	}{})
}

// Exec closes the database.
func (op CloseConfiguration) Exec(ctx context.Context, session *netconf.Session) error {
	_, err := session.ExecReply(ctx, op)
	return operr.Named("close-configuration", err)
}
