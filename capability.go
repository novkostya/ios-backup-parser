package backup

// Capability is a domain's capability report, returned when the domain opens.
// It states what schema introspection found — never what an iOS version
// string claims.
type Capability struct {
	// Domain is the domain package name, e.g. "contacts".
	Domain string `json:"domain"`

	// Supported reports whether the introspected structure matched a known
	// schema fingerprint. Domain Open fails with ErrUnsupportedSchema instead
	// of returning an unsupported capability, so callers holding a Capability
	// always see Supported == true today; the field exists so the report can
	// also describe a rejected database.
	Supported bool `json:"supported"`

	// Schema is the human alias of the detected fingerprint — a
	// project-internal, discovery-order ordinal such as "contacts.1", NEVER an
	// iOS-version-shaped name. The fingerprint's identity is the introspected
	// structure itself; which iOS versions it was observed on is recorded as
	// evidence in docs/schemas/.
	Schema string `json:"schema"`

	// Missing lists the record fields this backup's schema cannot provide:
	// fields whose backing tables/columns are absent from this database, plus
	// fields the fingerprint can never provide (out-of-scope sources such as
	// the contacts photo database). Names are stable identifiers documented
	// per domain.
	Missing []string `json:"missing,omitempty"`
}
