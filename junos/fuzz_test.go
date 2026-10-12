package junos_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lasseh/netconf/junos"
)

// FuzzJunosJSON holds the rewrite of a JSON document to what it promises:
// whatever it is given it returns, and a document that is JSON still is and
// means the same.
func FuzzJunosJSON(f *testing.F) {
	f.Add(`{"configuration":{"system":{"location":{"building":"R\u0026D \u003clab\u003e"}}}}`)
	f.Add(`{"a":"\ud83d\ude00","b":"\ud83d","c":"\ude00\ud83d"}`)
	f.Add(`{"a":"x\\u003cy\\\u003c\"\u0022\u005c\/"}`)
	f.Add(`{"a":"\u0000\u001f\ufffe\uffff\n\t"}`)
	f.Add(`["\u00","\u","\`)
	f.Add(`"\u\u0061000"`)
	f.Add(`{"a\/b":[1.10,true,null,"\u00e5"]}`)
	f.Fuzz(func(t *testing.T, doc string) {
		got := junos.JunosJSON(doc)
		if again := junos.JunosJSON(got); again != got {
			t.Fatalf("rewritten twice: %q, then %q", got, again)
		}

		var want, have any
		if json.Unmarshal([]byte(doc), &want) != nil {
			return
		}
		if err := json.Unmarshal([]byte(got), &have); err != nil {
			t.Fatalf("%q became %q, which is not JSON: %v", doc, got, err)
		}
		if !reflect.DeepEqual(want, have) {
			t.Fatalf("%q became %q, which means something else", doc, got)
		}
	})
}
