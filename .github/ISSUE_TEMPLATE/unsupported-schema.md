---
name: Unsupported schema
about: A backup that fails at Open with ErrUnsupportedSchema, or a domain that seems to be missing data
title: "[schema] <domain> unsupported on iOS <version>"
labels: schema-support
---

<!--
This library recognizes a backup by introspecting its actual tables and columns
(a "fingerprint"), never by iOS version. When it meets a schema it hasn't seen, it
fails loudly at Open with the observed fingerprint. Please share that fingerprint so a
new fingerprint can be added.

The fingerprint is STRUCTURAL ONLY — table and column names. It contains none of your
personal data. Please do NOT attach a real backup or any decoded records.
-->

**Domain:** <!-- contacts / calls / messages / calendar / notes -->

**Device iOS/iPadOS version:** <!-- e.g. 17.5.1 — Settings > General > About -->

**Observed fingerprint** (the string carried by the `ErrUnsupportedSchema` /
`*UnsupportedSchemaError` — this is safe to paste):

```
<paste the observed fingerprint here>
```

**What happened:**
<!-- Open returned ErrUnsupportedSchema, or a specific field/record was missing, etc. -->

**Anything else:**
<!-- optional: how the backup was produced (idevicebackup2, Finder, iTunes), etc. -->
