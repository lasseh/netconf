// Command stream-config counts matching elements in a streamed standard get-config reply.
package main

import (
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/lasseh/netconf"
	"github.com/lasseh/netconf/examples/internal/exampleutil"
	"github.com/lasseh/netconf/rpc"
)

func main() {
	element := flag.String("element", "interface", "local name of the configuration element to count")
	readTimeout := flag.Duration("read-timeout", 2*time.Minute, "total time allowed for this read, including its streamed body")
	maxBytes := flag.Int64("max-bytes", 512<<20, "maximum streamed reply size in bytes")
	exampleutil.Main(func(ctx context.Context, session *netconf.Session) error {
		if *readTimeout <= 0 || *maxBytes <= 0 || *maxBytes == math.MaxInt64 || *element == "" {
			return errors.New("read-timeout and max-bytes must be positive, max-bytes must be below MaxInt64, and element must not be empty")
		}
		count, err := streamConfig(ctx, session, *element, *readTimeout, *maxBytes)
		if err != nil {
			return err
		}
		if _, err := fmt.Printf("%s: %d\n", *element, count); err != nil {
			return fmt.Errorf("write count: %w", err)
		}
		return nil
	})
}

func streamConfig(ctx context.Context, session *netconf.Session, element string, timeout time.Duration, maxBytes int64) (count int, err error) {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg, err := session.Stream(readCtx, rpc.GetConfig{Source: rpc.Running})
	if err != nil {
		return 0, fmt.Errorf("start stream: %w", err)
	}
	// Stream's context no longer bounds reads after it returns. Closing the
	// session is how this example interrupts a blocked body read or drain.
	stop := context.AfterFunc(readCtx, func() {
		// Close ends the session even with an expired context. Any close error
		// is secondary to the read timeout/cancellation reported below.
		_ = session.Close(readCtx)
	})
	defer func() {
		stop()
		if readCtx.Err() != nil {
			err = errors.Join(err, readCtx.Err())
		}
		if err != nil {
			// Stop the transport before Close tries to drain a failed stream.
			err = errors.Join(err, session.Close(context.Background()))
		}
		err = errors.Join(err, msg.Close())
	}()

	limited := &io.LimitedReader{R: msg, N: maxBytes + 1}
	count, err = countElements(limited, element)
	if limited.N == 0 {
		return 0, fmt.Errorf("stream exceeded %d bytes", maxBytes)
	}
	return count, err
}

// countElements illustrates incremental parsing, not a general Junos reply
// parser. It recognizes standard rpc-error elements but not xnm:error or
// error documents encoded inside JSON. Names are matched without namespaces.
func countElements(r io.Reader, name string) (int, error) {
	decoder := xml.NewDecoder(r)
	var deviceErrors netconf.RPCErrors
	count, depth := 0, 0
	seenRoot, seenData := false, false
	dataDepth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, errors.Join(deviceErrorsOrNil(deviceErrors), fmt.Errorf("parse streamed XML: %w", err))
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seenRoot || token.Name.Local != "rpc-reply" {
					return 0, errors.New("expected one rpc-reply document")
				}
				seenRoot = true
			}
			depth++
			if token.Name.Local == "rpc-error" {
				var rpcErr netconf.RPCError
				if err := decoder.DecodeElement(&rpcErr, &token); err != nil {
					return 0, fmt.Errorf("decode streamed rpc-error: %w", err)
				}
				if rpcErr.Severity != netconf.SevWarning {
					deviceErrors = append(deviceErrors, rpcErr)
				}
				depth--
				continue
			}
			if depth == 2 && token.Name.Local == "data" {
				seenData, dataDepth = true, depth
			} else if dataDepth > 0 && token.Name.Local == name {
				count++
			}
		case xml.EndElement:
			if depth == dataDepth {
				dataDepth = 0
			}
			depth--
		}
	}
	if len(deviceErrors) > 0 {
		return 0, deviceErrors
	}
	if !seenData {
		return 0, fmt.Errorf("streamed reply: %w: data", netconf.ErrElementNotFound)
	}
	return count, nil
}

func deviceErrorsOrNil(errs netconf.RPCErrors) error {
	if len(errs) == 0 {
		return nil
	}
	return errs
}
