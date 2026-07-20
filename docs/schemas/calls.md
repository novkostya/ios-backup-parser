# calls — `CallHistory.storedata`

- **Backup location:** `HomeDomain` / `Library/CallHistoryDB/CallHistory.storedata`
  (siblings `CallHistoryTemp.storedata`, `com.apple.callhistory.databaseInfo.plist`,
  `third_party_deletions.log` — not the record store).
- **Storage idiom:** **CoreData** (`Z_PRIMARYKEY`, `Z_METADATA`, `Z_MODELCACHE`
  present). `Z_METADATA.Z_VERSION = 1`.
- **Fingerprint:** `calls.1` — status **validated** (M2 differential vs iLEAPP,
  2026-07-20; see *Status & evidence* below). iOS 18.x baseline.
- **WAL:** header `wal`; no sidecar present (checkpointed).
- **Scope:** the `calls` parser reads only this canonical store. The sibling
  `CallHistoryTemp.storedata` (a short-lived buffer of recent, not-yet-migrated
  calls) is out of scope for M2 — it would need two-database merge and Z_PK
  namespacing; recorded as a forward note, not a blocker. iLEAPP merges the two,
  which the differential accounts for (parser records ⊆ iLEAPP records).

## CoreData entity map (`Z_PRIMARYKEY`)

| `Z_ENT` | Entity | Table |
|---|---|---|
| 1 | CallDBProperties | `ZCALLDBPROPERTIES` (aggregate call timers) |
| 2 | CallRecord | `ZCALLRECORD` |
| 3 | EmergencyMediaItem | `ZEMERGENCYMEDIAITEM` |
| 4 | Handle | `ZHANDLE` |

`Z_MAX` in `Z_PRIMARYKEY` is the high-water primary key (exceeds live row count
because deletions leave gaps) — do not treat it as a count.

## Core tables

- **`ZCALLRECORD`** — one row per call. Key columns:
  - `ZDATE` (TIMESTAMP) — call time; `ZDURATION` (FLOAT) — seconds.
  - `ZADDRESS` (VARCHAR) — the remote number/identifier, denormalized onto the record.
  - `ZORIGINATED` (INTEGER) — direction: **0 = incoming, 1 = outgoing** (validated).
  - `ZANSWERED` (INTEGER) — answered flag (0/1); a **missed** call ≈ `ZORIGINATED=0
    AND ZANSWERED=0` (validated).
  - `ZCALLTYPE` (INTEGER) — service/kind: **0 = third-party app** (e.g. WhatsApp),
    **1 = telephony**, **8 = FaceTime *video***, **16 = FaceTime *audio***
    (validated — the FaceTime ordering corrects M0's "8/16 audio/video" guess,
    which had it backwards; per iLEAPP `callHistory.py`).
  - `ZCALL_CATEGORY`, `ZFACE_TIME_DATA`, `ZREAD`, `ZDISCONNECTED_CAUSE`,
    `ZFILTERED_OUT_REASON`, `ZNAME`, `ZSERVICE_PROVIDER`, `ZISO_COUNTRY_CODE`,
    `ZLOCATION`, `ZUNIQUE_ID`. Spam/junk signals are **two columns of different
    type**: `ZJUNKCONFIDENCE` (INTEGER) and `ZJUNKIDENTIFICATIONCATEGORY`
    (**VARCHAR** — a category identifier string, corrects M0's implicit lumping
    of both as integers).
- **`ZHANDLE`** — participant handles: `ZVALUE`, `ZNORMALIZEDVALUE`, `ZTYPE`.
- **`Z_2REMOTEPARTICIPANTHANDLES`** — many-to-many join for multi-party / FaceTime
  group calls: `Z_2REMOTEPARTICIPANTCALLS → ZCALLRECORD.Z_PK`,
  `Z_4REMOTEPARTICIPANTHANDLES → ZHANDLE.Z_PK`.

## Join topology

```
ZCALLRECORD (Z_PK)
  ├─ ZADDRESS                              remote party (denormalized, always present)
  └─◀ Z_2REMOTEPARTICIPANTHANDLES.Z_2REMOTEPARTICIPANTCALLS   (group/FaceTime participants)
        └─ .Z_4REMOTEPARTICIPANTHANDLES → ZHANDLE.Z_PK
```

For a 1:1 call the counterpart is in `ZADDRESS`; for multi-party calls the full
participant set is via the join to `ZHANDLE`. A parser should surface both.

## Timestamps

| Column | Epoch | Unit | Type |
|---|---|---|---|
| `ZCALLRECORD.ZDATE` | Cocoa 2001 | seconds | REAL (fractional) |
| `ZCALLRECORD.ZDURATION` | — | seconds (elapsed) | FLOAT |

Cocoa **seconds** (REAL) — same epoch as contacts/calendar/notes, **not** the
nanosecond units of messages.

## Capability mapping

| Record field (intended) | Source | Notes |
|---|---|---|
| timestamp | `ZDATE` | Cocoa **seconds**, REAL |
| duration | `ZDURATION` | seconds |
| direction | `ZORIGINATED` | 0 in / 1 out |
| answered / missed | `ZANSWERED` (+ `ZORIGINATED`) | derived |
| call kind (phone/FaceTime) | `ZCALLTYPE` | enum 0/1/8/16 (validated) |
| remote party | `ZADDRESS` (+ `ZHANDLE` via join) | 1:1 vs multi-party |
| display name | `ZNAME` | when resolved by the OS |
| service provider / country | `ZSERVICE_PROVIDER` / `ZISO_COUNTRY_CODE` | |
| spam signal | `ZJUNKCONFIDENCE` (INT) / `ZJUNKIDENTIFICATIONCATEGORY` (VARCHAR) | newer columns |

**Parser mapping (M2).** The `calls` package surfaces the required fields
(time, duration, direction, answered, call type, address) plus optional units
`name`, `service_provider`, `iso_country_code`, `unique_id`, `read`, `spam`
(both junk columns together) and `participants` (the `ZHANDLE` join). Each
absent optional unit lands its name in `Capability.Missing`.

**`Missing[]` candidates:** the trust/spam and FaceTime-extension columns
(`ZCOMMUNICATIONTRUSTSCORE`, `ZJUNK*`, `ZSCREENSHARINGTYPE`, satellite/emergency
video fields) are recent CoreData model additions — absent on older fingerprints.
Other present-but-unsurfaced columns (`ZWASEMERGENCYCALL`, `ZDISCONNECTED_CAUSE`,
`ZCALL_CATEGORY`, `ZLOCATION`, `ZHASMESSAGE`, …) are additive future optional
units, not part of the M2 record.

## Status & evidence

- **`calls.1` = validated (2026-07-20).** Differential vs iLEAPP `callHistory.py`
  (MIT, v2026.1.0) on the operator-local study backup passed with zero
  disagreements: a black-box phase (parser stream vs iLEAPP's Call History
  export) and an oracle-logic phase (parser vs the store's own SQL, keyed by
  `ZCALLRECORD.Z_PK`, covering every surfaced field including participants).
- On the observed schema every optional unit is present, so `Capability.Missing`
  is empty — the fingerprint is complete on this iOS 18.x line.
- The `ZCALLTYPE` and `ZORIGINATED` interpretations, and the FaceTime video/audio
  ordering, come from iLEAPP `callHistory.py` (attributed in `NOTICE`) and are
  confirmed by the passing differential; the exact schema (table/column names,
  the `Z_2REMOTEPARTICIPANTHANDLES` join) was re-confirmed by read-only
  introspection of a scratch copy before the parser was written.
