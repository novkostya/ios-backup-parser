package contacts

import "strings"

// CanonicalLabel strips Apple's built-in label wrapper: "_$!<Home>!$_" →
// "Home". User-defined labels (no wrapper) are returned unchanged. The
// wrapper form is cross-referenced from iLEAPP's addressBook artifact (MIT,
// see NOTICE).
func CanonicalLabel(label string) string {
	if inner, ok := strings.CutPrefix(label, "_$!<"); ok {
		if inner, ok := strings.CutSuffix(inner, ">!$_"); ok {
			return inner
		}
	}
	return label
}
