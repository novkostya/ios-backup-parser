package cocoa

import (
	"testing"
	"time"
)

// The round trip is the property paging depends on: a cursor built from a Message.Time must
// compare equal to the column it came from.
func TestNanosecondRoundTrip(t *testing.T) {
	for _, n := range []int64{0, 1, 700000000000000000, 731000000123456789} {
		if got := ToNanoseconds(FromNanoseconds(n)); got != n {
			t.Errorf("round trip of %d = %d", n, got)
		}
	}
	// And the epoch is Cocoa, not Unix — the mistake this exists to prevent.
	if ToNanoseconds(time.Unix(0, 0).UTC()) == 0 {
		t.Error("ToNanoseconds treated the Unix epoch as the Cocoa epoch")
	}
}
