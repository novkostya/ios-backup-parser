package cocoa

import (
	"testing"
	"time"
)

var cocoaEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

func TestFromSeconds(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want time.Time
	}{
		{0, cocoaEpoch},
		{86400, time.Date(2001, 1, 2, 0, 0, 0, 0, time.UTC)},
		{-31622400, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}, // 2000 is a leap year
		{700000000, cocoaEpoch.Add(700000000 * time.Second)},
	} {
		if got := FromSeconds(tc.in); !got.Equal(tc.want) {
			t.Errorf("FromSeconds(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFromSecondsFloat(t *testing.T) {
	got := FromSecondsFloat(1.5)
	want := cocoaEpoch.Add(1500 * time.Millisecond)
	if !got.Equal(want) {
		t.Errorf("FromSecondsFloat(1.5) = %v, want %v", got, want)
	}
	if got := FromSecondsFloat(0); !got.Equal(cocoaEpoch) {
		t.Errorf("FromSecondsFloat(0) = %v, want epoch", got)
	}
}

func TestFromNanoseconds(t *testing.T) {
	// The unit trap this package exists for: the SAME numeric value read as
	// nanoseconds must land 10⁹ closer to the epoch than read as seconds.
	if got := FromNanoseconds(1_500_000_000); !got.Equal(cocoaEpoch.Add(1500 * time.Millisecond)) {
		t.Errorf("FromNanoseconds(1.5e9) = %v, want epoch+1.5s", got)
	}
	const v = 700_000_000_000_000_000 // ~2023 as nanoseconds
	if got, want := FromNanoseconds(v), cocoaEpoch.Add(700_000_000*time.Second); !got.Equal(want) {
		t.Errorf("FromNanoseconds(7e17) = %v, want %v", got, want)
	}
}
