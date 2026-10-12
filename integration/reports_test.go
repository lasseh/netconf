//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/junos"
	"github.com/lasseh/netconf/rpc"
	"github.com/lasseh/netconf/transport"
)

// What users of other NETCONF libraries have reported about Junos, some of
// it years ago, looked at on a device.  Each test says what the report was
// and holds Junos to what it did on 26.2.

// The report: a request whose end-of-message delimiter falls across a
// 4096-byte boundary is not read by Junos, and a client has to pad it.  The
// requests here end at every place around three such boundaries, and each
// is answered.
//
// Over end-of-message framing Junos 26.2 reads them all, and nothing is
// padded.  Over chunked framing it does not: it loses the end of the chunks
// when one of its reads ends inside it, and ends the session over a chunk
// of 9800 bytes.  The writer of package transport keeps clear of both, and
// this is the test of that against a device.  Over chunks the requests are
// those whose end of chunks falls around each boundary on the wire, where
// the headers of the chunks count too.  It runs against any device, as none
// should mind the size of a request.
func TestRequestSizes(t *testing.T) {
	// a cheap request that every device of the flavor answers, and the
	// element its answer has
	request, answer := "<get-config><source><running/></source></get-config>", "data"
	if os.Getenv("NETCONF_DUT_FLAVOR") == "junos" {
		request, answer = "<get-system-uptime-information/>", "system-uptime-information"
	}

	// the size of each request, as the session sent it
	var (
		mu   sync.Mutex
		sent []int
	)
	trace := &netconf.Trace{OperationStart: func(op netconf.OperationInfo) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, len(op.Request))
	}}
	dialCtx, tr := dialSSH(t)
	session, err := open(dialCtx, tr, netconf.WithTrace(trace))
	be.NilErr(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(func() {
		be.NilErr(t, session.Close(ctx))
		cancel()
	})

	// ask sends a request with pad spaces behind it and returns its size
	ask := func(pad int) int {
		t.Helper()
		reply, err := session.ExecReply(ctx, request+strings.Repeat(" ", pad))
		if err != nil {
			t.Fatalf("a request with %d bytes of padding: %v", pad, err)
		}
		_, err = reply.Element(answer)
		be.NilErr(t, err)
		mu.Lock()
		defer mu.Unlock()
		return sent[len(sent)-1]
	}

	const delimiter = len("]]>]]>")
	chunked := session.ServerCaps().Has(netconf.CapNetConf11) && session.ClientCaps().Has(netconf.CapNetConf11)

	smallest := ask(0)
	requests := 1
	across := map[int]bool{}
	for _, boundary := range []int{4096, 8192, 65536} {
		// the sizes whose end falls on either side of the boundary and
		// across it: with the delimiter behind them, or cut into chunks
		var sizes []int
		if chunked {
			var moved int
			sizes, moved = chunkedAround(t, boundary)
			// every place the end of the chunks can take across the boundary
			be.Equal(t, len("\n##\n")-1, moved)
		} else {
			for size := boundary - delimiter - 4; size <= boundary+4; size++ {
				sizes = append(sizes, size)
				across[(size+delimiter)%4096] = true
			}
		}
		for _, size := range sizes {
			requests++
			// the message id of the tenth request is a digit longer
			pad := size - smallest - (len(strconv.Itoa(requests)) - 1)
			be.Equal(t, size, ask(pad))
		}
	}
	if !chunked {
		// every place the delimiter can take across a boundary was among them
		for left := range delimiter {
			be.True(t, across[left])
		}
	}
	t.Logf("%d requests of up to %d bytes, all answered", requests, 65536+4)
}

// chunkedAround returns the sizes of a message whose end of chunks lands
// within a few bytes of boundary on the wire, as the writer of package
// transport cuts the message into chunks, and for how many of them that
// writer moved the end: the sizes that would have had it across the
// boundary.  The headers of the chunks are on the wire as well, so these are
// not the sizes around the boundary itself.
func chunkedAround(t *testing.T, boundary int) (sizes []int, moved int) {
	t.Helper()

	const (
		end = "\n##\n"
		// more than the headers of the chunks of a message of boundary bytes
		// take
		headers = 160
	)
	for size := boundary - headers; size <= boundary+4; size++ {
		var wire bytes.Buffer
		framer := transport.NewFramer(strings.NewReader(""), &wire)
		framer.Upgrade()
		w, err := framer.MsgWriter()
		be.NilErr(t, err)
		_, err = w.Write(bytes.Repeat([]byte{'x'}, size))
		be.NilErr(t, err)
		be.NilErr(t, w.Close())

		at := wire.Len() - len(end)
		if at < boundary-8 || at > boundary+8 {
			continue
		}
		sizes = append(sizes, size)
		// the last byte in a chunk of its own is what moves the end
		if bytes.HasSuffix(wire.Bytes(), []byte("\n#1\nx"+end)) {
			moved++
		}
	}
	return sizes, moved
}

// The report: a confirmed commit sent as the standard <commit> does not
// hold on Junos: the time to confirm is taken for zero, or the commit is
// taken back when its session ends.  And <cancel-commit> is treated one way
// written as a pair of tags and another way as an empty element.
//
// Junos 26.2 keeps the commit for the time it was given, also when the
// session of the commit is gone, and any session confirms it.  It has no
// <cancel-commit> at all, in either spelling: it advertises
// :confirmed-commit:1.0, which has none.
//
// While the commit waits, a dry run of a junos.Change is held to leaving it
// alone: its commit check would be the confirmation.
func TestJunosStandardConfirmedCommit(t *testing.T) {
	other := commitSession(t)
	cfg := harmless(t)
	ctx := changeCtx(t)
	value := "std-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	dialCtx, tr := dialSSH(t)
	first, err := open(dialCtx, tr)
	be.NilErr(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = first.Close(ctx)
	})

	be.NilErr(t, rpc.Lock{Target: rpc.Candidate}.Exec(ctx, first))
	be.NilErr(t, rpc.EditConfig{Target: rpc.Candidate, Config: cfg.config(value)}.Exec(ctx, first))
	be.NilErr(t, rpc.Commit{Confirmed: true, ConfirmTimeout: 3 * time.Minute}.Exec(ctx, first))

	// in effect, and waiting for the three minutes it was given
	newest := commits(ctx, t, other)[0]
	be.True(t, newest.Pending)
	be.Equal(t, "commit confirmed, rollback in 3mins", newest.Note)
	be.In(t, value, activeProbe(ctx, t, other))

	if first.ServerCaps().Has(":confirmed-commit:1.1") {
		t.Log("the device advertises :confirmed-commit:1.1, so <cancel-commit> is not asked for what it is not")
	} else {
		err = rpc.CancelCommit{}.Exec(ctx, first)
		be.True(t, errors.Is(err, junos.ErrUnsupported))
		t.Logf("<cancel-commit></cancel-commit>: %v", err)
		_, err = first.ExecReply(ctx, "<cancel-commit/>")
		be.True(t, errors.Is(err, junos.ErrUnsupported))
		t.Logf("<cancel-commit/>: %v", err)
		be.True(t, commits(ctx, t, other)[0].Pending)
	}

	// the end of its session does not take the commit back
	be.NilErr(t, first.Close(ctx))
	time.Sleep(20 * time.Second)
	be.True(t, commits(ctx, t, other)[0].Pending)
	be.In(t, value, activeProbe(ctx, t, other))

	// A dry run of a change would confirm it with its commit check.  It
	// reads the commit history first, sends no check and says who has a
	// commit pending, with what the change would do.
	_, dry := probe()
	result, err := junos.Change{Format: junos.FormatSet, Config: dry, Private: true, DryRun: true}.Apply(ctx, other)
	be.True(t, errors.Is(err, junos.ErrCommitPending))
	t.Logf("a dry run while the commit is pending: %v", err)
	be.In(t, probeGroup, result.Diff)
	be.False(t, result.Committed)
	be.True(t, commits(ctx, t, other)[0].Pending)

	// and another session confirms it
	_, err = junos.CommitConfiguration{Check: true}.Exec(ctx, other)
	be.NilErr(t, err)
	newest = commits(ctx, t, other)[0]
	be.False(t, newest.Pending)
	be.Equal(t, "commit confirmed, rollback in 3mins", newest.Note)
	be.In(t, value, activeProbe(ctx, t, other))
}

// The report: the JSON of Junos is not always JSON.  A line break inside a
// string, a backslash of a regular expression written without its escape,
// an empty object in front of the document.
//
// Junos 26.2 writes valid JSON for all that was tried here: a syslog match
// with backslashes in it, a quote and a backslash in a text, and the output
// of commands.  The statements go into a private copy that is closed again.
func TestJunosJSONIsJSON(t *testing.T) {
	onlyFlavor(t, "junos")
	if os.Getenv("NETCONF_DUT_CHANGE_DRYRUN") == "" {
		t.Skip("NETCONF_DUT_CHANGE_DRYRUN not set, skipping test")
	}
	session := setupSSH(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// document checks that doc is one JSON document and nothing else
	document := func(t *testing.T, doc []byte) {
		t.Helper()
		be.True(t, json.Valid(doc))
		be.True(t, strings.HasPrefix(strings.TrimSpace(string(doc)), "{"))
	}

	for _, command := range []string{"show version", "show system uptime", "show system alarms"} {
		doc, err := junos.Command(command).JSON(ctx, session)
		be.NilErr(t, err)
		document(t, doc)
	}

	be.NilErr(t, junos.OpenConfiguration{Private: true}.Exec(ctx, session))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		be.NilErr(t, junos.CloseConfiguration{}.Exec(ctx, session))
	}()

	// Junos keeps what stands between the quotes of a set command as it is,
	// but for the escaped quote.
	const (
		match    = `a\.b|c\\d+ \[x\]`
		building = `quote \" and backslash \\ end`
	)
	_, err := junos.LoadConfiguration{Format: junos.FormatSet, Config: strings.Join([]string{
		"set groups " + probeGroup + " system syslog file nclib-probe any any",
		"set groups " + probeGroup + ` system syslog file nclib-probe match "` + match + `"`,
		"set groups " + probeGroup + ` system location building "` + building + `"`,
	}, "\n")}.Exec(ctx, session)
	be.NilErr(t, err)

	for _, format := range []junos.Format{junos.FormatJSON, junos.FormatJSONMinified} {
		if format == junos.FormatJSONMinified && session.ServerCaps().Has(netconf.CapNetConf11) {
			// see TestJunosGetConfiguration: not over chunked framing
			continue
		}
		doc, err := junos.GetConfiguration{
			Format:    format,
			Candidate: true,
			Filter:    "<configuration><groups><name>" + probeGroup + "</name></groups></configuration>",
		}.Exec(ctx, session)
		be.NilErr(t, err)
		document(t, doc)

		var read struct {
			Configuration struct {
				Groups []struct {
					System struct {
						Location struct {
							Building string `json:"building"`
						} `json:"location"`
						Syslog struct {
							File []struct {
								Match string `json:"match"`
							} `json:"file"`
						} `json:"syslog"`
					} `json:"system"`
				} `json:"groups"`
			} `json:"configuration"`
		}
		be.NilErr(t, json.Unmarshal(doc, &read))
		be.Equal(t, 1, len(read.Configuration.Groups))
		system := read.Configuration.Groups[0].System
		be.Equal(t, 1, len(system.Syslog.File))
		be.Equal(t, match, system.Syslog.File[0].Match)
		be.Equal(t, `quote " and backslash \\ end`, system.Location.Building)
	}

	_, err = junos.Rollback(0).Exec(ctx, session)
	be.NilErr(t, err)
}

// The report: a commit comment longer than 512 characters fails the commit,
// after the load went through.
//
// Junos 26.2 does refuse it, and counts bytes: "Length 513 is not within
// range (1..512)".  A commit check looks at the comment the same way and
// commits nothing, which is how this asks.  The library does not send such
// a commit, so a Change with it stops before it loads anything.
func TestJunosCommitCommentLimit(t *testing.T) {
	onlyFlavor(t, "junos")
	session := setupSSH(t)
	skipIfCommitPending(t, session)
	ctx := testCtx(t)

	check := func(comment string) error {
		_, err := session.ExecReply(ctx, "<commit-configuration><check/><log>"+comment+"</log></commit-configuration>")
		return err
	}
	refused := func(t *testing.T, err error, length string) {
		t.Helper()
		var rpcErr netconf.RPCError
		be.True(t, errors.As(err, &rpcErr))
		be.Equal(t, "Length "+length+" is not within range (1..512)", rpcErr.Message)
	}

	be.NilErr(t, check(strings.Repeat("x", 512)))
	refused(t, check(strings.Repeat("x", 513)), "513")
	// a letter of two bytes counts twice
	be.NilErr(t, check(strings.Repeat("å", 256)))
	refused(t, check(strings.Repeat("å", 257)), "514")

	_, err := junos.CommitConfiguration{Check: true, Comment: strings.Repeat("x", 513)}.Exec(ctx, session)
	be.Nonzero(t, err)
	be.In(t, "the comment is 513 bytes long, and Junos takes 512", err.Error())
}
