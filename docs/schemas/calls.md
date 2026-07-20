# calls — `CallHistory.storedata`

- **Backup location:** `HomeDomain` / `Library/CallHistoryDB/CallHistory.storedata`
  (siblings `CallHistoryTemp.storedata`, `com.apple.callhistory.databaseInfo.plist`,
  `third_party_deletions.log` — not the record store).
- **Storage idiom:** **CoreData** (`Z_PRIMARYKEY`, `Z_METADATA`, `Z_MODELCACHE`
  present). `Z_METADATA.Z_VERSION = 1`.
- **Fingerprint:** `calls.1` — status **observed** (iOS 18.x baseline).
- **WAL:** header `wal`; no sidecar present (checkpointed).

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
  - `ZORIGINATED` (INTEGER) — direction: **0 = incoming, 1 = outgoing** (interpretation, to validate).
  - `ZANSWERED` (INTEGER) — answered flag; a **missed** call ≈ `ZORIGINATED=0 AND ZANSWERED=0`.
  - `ZCALLTYPE` (INTEGER) — service/kind: **1 = telephony**, **8 / 16 = FaceTime**
    (audio/video); interpretation, to validate.
  - `ZCALL_CATEGORY`, `ZFACE_TIME_DATA`, `ZREAD`, `ZDISCONNECTED_CAUSE`,
    `ZFILTERED_OUT_REASON`, `ZJUNKCONFIDENCE`/`ZJUNKIDENTIFICATIONCATEGORY` (spam),
    `ZNAME`, `ZSERVICE_PROVIDER`, `ZISO_COUNTRY_CODE`, `ZLOCATION`, `ZUNIQUE_ID`.
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
| call kind (phone/FaceTime) | `ZCALLTYPE` | enum, interpretation |
| remote party | `ZADDRESS` (+ `ZHANDLE` via join) | 1:1 vs multi-party |
| display name | `ZNAME` | when resolved by the OS |
| service provider / country | `ZSERVICE_PROVIDER` / `ZISO_COUNTRY_CODE` | |
| spam signal | `ZJUNKCONFIDENCE` / `ZJUNKIDENTIFICATIONCATEGORY` | newer columns |

**`Missing[]` candidates:** the trust/spam and FaceTime-extension columns
(`ZCOMMUNICATIONTRUSTSCORE`, `ZJUNK*`, `ZSCREENSHARINGTYPE`, satellite/emergency
video fields) are recent CoreData model additions — absent on older fingerprints.
