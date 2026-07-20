// Package cocoa converts Apple "Cocoa epoch" timestamps — counted from
// 2001-01-01T00:00:00Z — to time.Time.
//
// The UNIT varies per column across the domain databases (the cross-domain
// trap documented in docs/schemas/README.md): contacts, calls, calendar and
// notes store seconds, while messages stores NANOseconds. Callers pick the
// function matching the documented unit of each column — there is no
// magnitude-guessing helper here on purpose: guessing is how off-by-31-years
// and off-by-10⁹ bugs are born.
package cocoa

import "time"

// unixDelta is the offset between the Cocoa epoch (2001-01-01T00:00:00Z) and
// the Unix epoch (1970-01-01T00:00:00Z), in seconds.
const unixDelta = 978307200

// FromSeconds converts a Cocoa timestamp in whole seconds (INTEGER columns,
// e.g. ABPerson.CreationDate).
func FromSeconds(s int64) time.Time {
	return time.Unix(s+unixDelta, 0).UTC()
}

// FromSecondsFloat converts a Cocoa timestamp in fractional seconds (REAL
// columns, e.g. ZCALLRECORD.ZDATE).
func FromSecondsFloat(s float64) time.Time {
	sec := int64(s)
	nsec := int64((s - float64(sec)) * 1e9)
	return time.Unix(sec+unixDelta, nsec).UTC()
}

// FromNanoseconds converts a Cocoa timestamp in nanoseconds (message.date on
// modern schemas).
func FromNanoseconds(n int64) time.Time {
	return time.Unix(unixDelta, n).UTC()
}
