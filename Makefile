# ios-backup-parser — the one entrypoint. CI calls only these targets (no logic in YAML).
#
# The dev host is a PURE CONTAINER HOST: no Go toolchain is installed on it. The gates
# run inside a pinned Go toolchain container built from deploy/Dockerfile, so dev and
# CI compile with identical toolchains. All version + image pins live in versions.env
# (the single source of truth). Mirrors ios-backup-crypt / quince — Go-only.
#
# Requirements on the box: `make` + a container runtime (nerdctl or docker) with buildkit.

include versions.env

ROOT       := $(abspath .)
RUNTIME    ?= $(shell command -v nerdctl 2>/dev/null || command -v docker 2>/dev/null)
IMAGE_TAG  ?= local

# Named cache volumes — persistent across runs, safe to lose (they live on disposable
# runtime storage). They are what keep the containerized gates fast.
GO_BUILD_VOL := ios-backup-parser-go-build
GO_MOD_VOL   := ios-backup-parser-go-mod

# Locally-built toolchain images (== the deploy/Dockerfile stages).
TC_GO     := ios-backup-parser-toolchain-go:$(IMAGE_TAG)
TC_ILEAPP := ios-backup-parser-toolchain-ileapp:$(IMAGE_TAG)

# Build-args threaded into the image build so the Dockerfile and the gates agree on pins.
BUILD_ARGS := \
	--build-arg GO_IMAGE=$(GO_IMAGE) \
	--build-arg GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION)

ILEAPP_BUILD_ARGS := \
	--build-arg PYTHON_IMAGE=$(PYTHON_IMAGE) \
	--build-arg ILEAPP_VERSION=$(ILEAPP_VERSION)

# `RUN`: repo bind-mounted at /src. `GO_RUN`: plus Go caches + env for the gate.
RUN    := $(RUNTIME) run --rm -v $(ROOT):/src
GO_RUN := $(RUN) -w /src \
	-v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	-e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "ios-backup-parser gates (run in a pinned Go toolchain container via $(RUNTIME)):"
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
	@echo "Runtime detected: $(RUNTIME)"

.PHONY: preflight
preflight:
	@test -n "$(RUNTIME)" || { echo "ERROR: no container runtime (nerdctl/docker) found. This box must be a container host."; exit 1; }

.PHONY: tc-go
tc-go: preflight ## Ensure the Go toolchain image exists (builds only if missing — see tc-go-force)
	@$(RUNTIME) image inspect $(TC_GO) >/dev/null 2>&1 || $(MAKE) --no-print-directory tc-go-force

.PHONY: tc-go-force
tc-go-force: preflight ## Rebuild the Go toolchain image (run after a versions.env / Dockerfile change)
	$(RUNTIME) build $(BUILD_ARGS) --target toolchain-go -t $(TC_GO) -f deploy/Dockerfile .

.PHONY: gates
gates: tc-go ## Run the gates: gofmt -l (empty) + go vet + golangci-lint + go test -race
	$(GO_RUN) sh -euc '\
	    unformatted=$$(gofmt -l .); \
	    if [ -n "$$unformatted" ]; then echo "gofmt needs to run on:"; echo "$$unformatted"; exit 1; fi; \
	    go vet ./...; \
	    golangci-lint run; \
	    go test -race ./...'

.PHONY: test
test: tc-go ## Just the tests (go test -race), no lint — for a fast inner loop
	$(GO_RUN) sh -euc 'go test -race ./...'

.PHONY: tidy
tidy: tc-go ## Run `go mod tidy` inside the toolchain container
	$(GO_RUN) sh -euc 'go mod tidy'

.PHONY: fixtures
fixtures: tc-go ## Regenerate every committed synthetic fixture (testing ladder rung 1)
	$(GO_RUN) sh -euc 'FIXTURE_WRITE=1 go test -count=1 -run TestWriteCommittedFixture ./...'

# ---------------------------------------------------------------------------
# OPERATOR-LOCAL study targets (testing ladder rung 3). NEVER in CI: they read the
# decrypted study backup. Nothing they produce is committed — .difftmp/ is gitignored
# and stays on the operator's box. STUDY_DIR is the reconstructed <Domain>/<path>
# tree, bind-mounted read-only; the parser only ever opens scratch COPIES of it
# (BackupFS.Materialize semantics — the never-mutate-input hard rule).
# ---------------------------------------------------------------------------
STUDY_DIR ?= /mnt/iphone-decrypted

.PHONY: dump-study
dump-study: tc-go ## Operator-local: stream contacts from the study tree to .difftmp/parser.jsonl
	$(RUN) -w /src \
	  -v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	  -v "$(STUDY_DIR):/study:ro" \
	  -e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO) sh -euc '\
	    mkdir -p .difftmp && \
	    go run ./cmd/ibp-dump -root /study > .difftmp/parser.jsonl && \
	    wc -l .difftmp/parser.jsonl'

.PHONY: tc-ileapp
tc-ileapp: preflight ## Ensure the iLEAPP oracle image exists (builds only if missing)
	@$(RUNTIME) image inspect $(TC_ILEAPP) >/dev/null 2>&1 || $(MAKE) --no-print-directory tc-ileapp-force

.PHONY: tc-ileapp-force
tc-ileapp-force: preflight ## Rebuild the iLEAPP oracle image (after a versions.env bump)
	$(RUNTIME) build $(ILEAPP_BUILD_ARGS) --target toolchain-ileapp -t $(TC_ILEAPP) -f deploy/Dockerfile .

# INPUT-TYPE CAVEAT (interface fact, VERIFIED live 2026-07-20): iLEAPP's `fs`
# type expects an iOS *filesystem* extraction (/private/var/… layout), while the
# study tree is a *backup-domain* layout (HomeDomain/Library/…). The addressBook
# artifact globs `*/mobile/Library/AddressBook/AddressBook*.sqlitedb*`, so a raw
# fs-mode run over the domain tree finds MANY artifacts (filename-only globs)
# but NOT contacts — an empty differential means this, not a parser gap. So the
# target stages scratch COPIES of the AddressBook databases into a
# /private/var/mobile/… shim (container-side tmp; the ro study mount is never
# opened by SQLite) and runs iLEAPP on the shim. The stronger oracle remains
# `-t itunes` against the ORIGINAL (encrypted) backup — iLEAPP then does its own
# decryption, cross-checking the whole decrypt+parse pipeline without sharing
# ios-backup-crypt as a common ancestor. That needs the encrypted backup and its
# password (Operator-held): a deliberate manual escalation, not the default.
.PHONY: diff-study
diff-study: dump-study tc-ileapp ## Operator-local differential: parser vs iLEAPP contacts, record-by-record
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) sh -euc '\
	    rm -rf /src/.difftmp/ileapp && mkdir -p /src/.difftmp/ileapp && \
	    stage=$$(mktemp -d)/private/var/mobile/Library/AddressBook && mkdir -p "$$stage" && \
	    cp /study/HomeDomain/Library/AddressBook/AddressBook*.sqlitedb* "$$stage"/ && \
	    python /opt/iLEAPP/ileapp.py -t fs -i "$${stage%%/private/var/*}" -o /src/.difftmp/ileapp'
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) python /src/deploy/diff_contacts.py /src/.difftmp \
	    --db /study/HomeDomain/Library/AddressBook/AddressBook.sqlitedb

.PHONY: dump-study-calls
dump-study-calls: tc-go ## Operator-local: stream calls from the study tree to .difftmp/parser-calls.jsonl
	$(RUN) -w /src \
	  -v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	  -v "$(STUDY_DIR):/study:ro" \
	  -e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO) sh -euc '\
	    mkdir -p .difftmp && \
	    go run ./cmd/ibp-dump -root /study -domain calls > .difftmp/parser-calls.jsonl && \
	    wc -l .difftmp/parser-calls.jsonl'

# Same input-type caveat as diff-study (see above): iLEAPP's callHistory artifact
# globs `*/mobile/Library/CallHistoryDB/CallHistory*`, so an fs-mode run over the
# backup-domain tree needs the DBs staged into a /private/var/mobile/… shim.
# iLEAPP merges CallHistory.storedata with CallHistoryTemp.storedata; the parser
# reads only the canonical store, so diff_calls.py treats iLEAPP-only records as
# the expected temp-DB delta (phase 2's ROWID cross-check is the exact gate).
.PHONY: diff-study-calls
diff-study-calls: dump-study-calls tc-ileapp ## Operator-local differential: parser vs iLEAPP calls, record-by-record
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) sh -euc '\
	    rm -rf /src/.difftmp/ileapp-calls && mkdir -p /src/.difftmp/ileapp-calls && \
	    stage=$$(mktemp -d)/private/var/mobile/Library/CallHistoryDB && mkdir -p "$$stage" && \
	    cp /study/HomeDomain/Library/CallHistoryDB/CallHistory*.storedata* "$$stage"/ && \
	    python /opt/iLEAPP/ileapp.py -t fs -i "$${stage%%/private/var/*}" -o /src/.difftmp/ileapp-calls'
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) python /src/deploy/diff_calls.py /src/.difftmp \
	    --db /study/HomeDomain/Library/CallHistoryDB/CallHistory.storedata

.PHONY: dump-study-messages
dump-study-messages: tc-go ## Operator-local: stream messages from the study tree to .difftmp/parser-messages.jsonl
	$(RUN) -w /src \
	  -v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	  -v "$(STUDY_DIR):/study:ro" \
	  -e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO) sh -euc '\
	    mkdir -p .difftmp && \
	    go run ./cmd/ibp-dump -root /study -domain messages > .difftmp/parser-messages.jsonl && \
	    wc -l .difftmp/parser-messages.jsonl'

# Same input-type caveat as diff-study (see above): iLEAPP's sms artifact globs
# `*/Library/SMS/sms.db*`, so an fs-mode run over the backup-domain tree needs the
# DB staged into a /private/var/mobile/… shim. iLEAPP decodes attributedBody with
# the independent python-typedstream library, so phase 1 (its SMS export) and
# phase 2 (diff_messages.py re-running python-typedstream against a scratch copy,
# keyed by message.ROWID with an exact both-directions set check) together
# cross-check our from-scratch Go typedstream decoder against a different
# implementation. Phase 2 alone is comprehensive and does NOT require the full
# iLEAPP run, so it still validates even on a very large sms.db.
#
# STRONGER, CHARTER-NAMED MANUAL ESCALATION — imessage-exporter (GPL, BLACK BOX
# ONLY: run it, diff its output; its source is NEVER read). Install the published
# binary operator-side (e.g. `cargo install imessage-exporter`, or a release
# binary) — NO source clone lands in this repo or its scratch — then export the
# study db to text and diff against .difftmp/parser-messages.jsonl:
#     imessage-exporter -f txt -p <study>/HomeDomain/Library/SMS -o .difftmp/ime
# and compare message bodies per conversation. Deliberately not wired into the
# default oracle image (a Rust build is heavy; python-typedstream already gives an
# independent decoder), matching M1's documented `-t itunes` escalation.
.PHONY: diff-study-messages
diff-study-messages: dump-study-messages tc-ileapp ## Operator-local differential: parser vs iLEAPP messages, record-by-record
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) sh -euc '\
	    rm -rf /src/.difftmp/ileapp-messages && mkdir -p /src/.difftmp/ileapp-messages && \
	    stage=$$(mktemp -d)/private/var/mobile/Library/SMS && mkdir -p "$$stage" && \
	    cp /study/HomeDomain/Library/SMS/sms.db* "$$stage"/ && \
	    python /opt/iLEAPP/ileapp.py -t fs -i "$${stage%%/private/var/*}" -o /src/.difftmp/ileapp-messages'
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) python /src/deploy/diff_messages.py /src/.difftmp \
	    --db /study/HomeDomain/Library/SMS/sms.db

.PHONY: dump-study-calendar
dump-study-calendar: tc-go ## Operator-local: stream calendar events from the study tree to .difftmp/parser-calendar.jsonl
	$(RUN) -w /src \
	  -v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	  -v "$(STUDY_DIR):/study:ro" \
	  -e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO) sh -euc '\
	    mkdir -p .difftmp && \
	    go run ./cmd/ibp-dump -root /study -domain calendar > .difftmp/parser-calendar.jsonl && \
	    wc -l .difftmp/parser-calendar.jsonl'

# Same input-type caveat as diff-study (see above): iLEAPP's calendar artifact
# globs `*/Calendar.sqlitedb`, so an fs-mode run over the backup-domain tree needs
# the DB staged into a /private/var/mobile/… shim. iLEAPP's calendarAll.py splits
# events from birthday items by calendar_scale (events = NOT 'gregorian'), which
# the parser mirrors; phase 1 compares its Calendar Events export and phase 2
# re-runs its query semantics against a scratch copy (keyed by CalendarItem.ROWID,
# both-directions set check) to cover the fields the export omits (recurrence,
# alarms, status/availability/privacy).
.PHONY: diff-study-calendar
diff-study-calendar: dump-study-calendar tc-ileapp ## Operator-local differential: parser vs iLEAPP calendar, record-by-record
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) sh -euc '\
	    rm -rf /src/.difftmp/ileapp-calendar && mkdir -p /src/.difftmp/ileapp-calendar && \
	    stage=$$(mktemp -d)/private/var/mobile/Library/Calendar && mkdir -p "$$stage" && \
	    cp /study/HomeDomain/Library/Calendar/Calendar.sqlitedb* "$$stage"/ && \
	    python /opt/iLEAPP/ileapp.py -t fs -i "$${stage%%/private/var/*}" -o /src/.difftmp/ileapp-calendar'
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) python /src/deploy/diff_calendar.py /src/.difftmp \
	    --db /study/HomeDomain/Library/Calendar/Calendar.sqlitedb

NOTES_DB := AppDomainGroup-group.com.apple.notes/NoteStore.sqlite

.PHONY: dump-study-notes
dump-study-notes: tc-go ## Operator-local: stream notes from the study tree to .difftmp/parser-notes.jsonl
	$(RUN) -w /src \
	  -v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	  -v "$(STUDY_DIR):/study:ro" \
	  -e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO) sh -euc '\
	    mkdir -p .difftmp && \
	    go run ./cmd/ibp-dump -root /study -domain notes > .difftmp/parser-notes.jsonl && \
	    wc -l .difftmp/parser-notes.jsonl'

# NOTES DIFFERENTIAL — a departure from the other domains' harness, for a reason.
# iLEAPP's notes.py hard-codes the note→account join on ZACCOUNT4; on the iOS 17/18
# schema a note's account lives in ZACCOUNT7, so its INNER JOIN matches nothing and
# iLEAPP returns ZERO notes (see its own sample_data: "iOS 18.x | 0 rows"). Running
# the iLEAPP artifact here would therefore produce an empty, useless export. So the
# oracle is split (still an INDEPENDENT implementation, still MIT):
#   - BODY DECODER: diff_notes.py ports iLEAPP notes.py's own decoder
#     (get_uncompressed_data + process_note_body_blob — a fixed-offset byte-walk,
#     MIT, attributed) and runs it against a scratch copy, cross-checking our
#     from-scratch Go recursive-descent protobuf reader blob-for-blob — the same
#     independent-decoder validation diff_messages.py gets from python-typedstream.
#   - METADATA + SET: iLEAPP's query semantics (its column choices) re-run against a
#     scratch copy keyed by ICNote Z_PK, both-directions set check.
#   - SNIPPET: every decoded body is cross-checked against Apple's own stored
#     ZSNIPPET preview — an oracle-independent confirmation of the text.
#   - MEDIA: each media FileRef is checked to os.path.exists under the study tree.
# The parser only ever opens a scratch COPY (BackupFS.Materialize); diff_notes.py
# opens the study DB read-only+immutable. STRONGER MANUAL ESCALATION: Apple Notes is
# also parsed by apple_cloud_notes_parser (threeplanetssoftware) — run it operator-
# side as a black box and diff its export, mirroring M1's `-t itunes` note.
.PHONY: diff-study-notes
diff-study-notes: dump-study-notes tc-ileapp ## Operator-local differential: parser vs iLEAPP's decoder + store SQL + snippet
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) python /src/deploy/diff_notes.py /src/.difftmp \
	    --db "/study/$(NOTES_DB)" --study /study

.PHONY: dump-study-safari
dump-study-safari: tc-go ## Operator-local: stream safari from the study tree to .difftmp/parser-safari.jsonl
	$(RUN) -w /src \
	  -v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	  -v "$(STUDY_DIR):/study:ro" \
	  -e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO) sh -euc '\
	    mkdir -p .difftmp && \
	    go run ./cmd/ibp-dump -root /study -domain safari > .difftmp/parser-safari.jsonl && \
	    wc -l .difftmp/parser-safari.jsonl'

# Same input-type caveat as diff-study (see above): iLEAPP's safari artifacts glob
# `**/Safari/Bookmarks.db*` and `**/Safari/History.db*`, so an fs-mode run over the
# backup-domain tree needs BOTH databases staged into a /private/var/mobile/… shim.
# iLEAPP's safariBookmarks.py reads `SELECT title,url,hidden FROM bookmarks` (every
# row — folders, special folders and reading-list items included) and safariHistory.py
# joins history_visits ⟕ history_items with origin/redirect resolution; the parser
# mirrors both and additionally splits reading-list items (bookmarks.read IS NOT NULL).
# Phase 1 compares the two exports; phase 2 re-runs the query semantics against scratch
# copies of both stores (keyed by bookmarks.id and history_visits.id, both-directions
# set check) to cover every field the exports omit — and asserts the two-epoch trap
# (Bookmarks Unix seconds vs History Cocoa seconds).
.PHONY: diff-study-safari
diff-study-safari: dump-study-safari tc-ileapp ## Operator-local differential: parser vs iLEAPP safari, record-by-record
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) sh -euc '\
	    rm -rf /src/.difftmp/ileapp-safari && mkdir -p /src/.difftmp/ileapp-safari && \
	    stage=$$(mktemp -d)/private/var/mobile/Library/Safari && mkdir -p "$$stage" && \
	    cp /study/HomeDomain/Library/Safari/Bookmarks.db* /study/HomeDomain/Library/Safari/History.db* "$$stage"/ && \
	    python /opt/iLEAPP/ileapp.py -t fs -i "$${stage%%/private/var/*}" -o /src/.difftmp/ileapp-safari'
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) python /src/deploy/diff_safari.py /src/.difftmp \
	    --db /study/HomeDomain/Library/Safari/Bookmarks.db --history-db /study/HomeDomain/Library/Safari/History.db

REMINDERS_STORES := AppDomainGroup-group.com.apple.reminders/Container_v1/Stores

.PHONY: dump-study-reminders
dump-study-reminders: tc-go ## Operator-local: stream reminders from the study tree to .difftmp/parser-reminders.jsonl
	$(RUN) -w /src \
	  -v $(GO_BUILD_VOL):/root/.cache/go-build -v $(GO_MOD_VOL):/go/pkg/mod \
	  -v "$(STUDY_DIR):/study:ro" \
	  -e CGO_ENABLED=1 -e GOTOOLCHAIN=local $(TC_GO) sh -euc '\
	    mkdir -p .difftmp && \
	    go run ./cmd/ibp-dump -root /study -domain reminders > .difftmp/parser-reminders.jsonl && \
	    wc -l .difftmp/parser-reminders.jsonl'

# REMINDERS DIFFERENTIAL — split-oracle, like notes, and for the same reason.
# iLEAPP's reminders.py queries `FROM ZREMCDOBJECT WHERE ZTITLE1 <> ''` and guards
# on does_column_exist_in_db(ZREMCDOBJECT, ZLASTMODIFIEDDATE); on the iOS 17/18
# schema reminders live in ZREMCDREMINDER (title ZTITLE) and ZREMCDOBJECT has no
# ZLASTMODIFIEDDATE, so the guard is false and iLEAPP returns ZERO reminders — the
# same notes-class staleness. Running the artifact here would yield an empty,
# useless export, so the oracle is diff_reminders.py's OWN SQL against a scratch
# copy of every store (the reminders domain spans MANY stores: Data-<UUID>.sqlite
# per account + Data-local.sqlite), keyed by (store, ZREMCDREMINDER.Z_PK) with the
# both-directions set check. iLEAPP is still credited (MIT, NOTICE) for the store
# glob and the Cocoa `+978307200` epoch, which diff_reminders.py reuses. The parser
# opens scratch copies (BackupFS.Materialize); the harness copies each store too.
.PHONY: diff-study-reminders
diff-study-reminders: dump-study-reminders tc-ileapp ## Operator-local differential: parser vs each store's own SQL, reminder-by-reminder
	$(RUN) -v "$(STUDY_DIR):/study:ro" $(TC_ILEAPP) python /src/deploy/diff_reminders.py /src/.difftmp \
	    --stores "/study/$(REMINDERS_STORES)"

.PHONY: clean
clean: ## Drop cache volumes and the locally-built toolchain images
	-$(RUNTIME) run --rm -v $(ROOT):/src $(ALPINE_IMAGE) rm -rf /src/.difftmp
	-$(RUNTIME) volume rm $(GO_BUILD_VOL) $(GO_MOD_VOL)
	-$(RUNTIME) rmi $(TC_GO) $(TC_ILEAPP)

# ---------------------------------------------------------------------------
# Commit-time privacy gate.
# The Operator-private pattern list lives in the quince checkout's private local/
# layer; on Operator machines that checkout sits next to this repo. Absent file =>
# the target no-ops (contributor / CI mode).
# ---------------------------------------------------------------------------
PRIVACY_PATTERNS ?= $(firstword $(wildcard \
	../iphone-backup-app/local/privacy-patterns.txt \
	../quince/local/privacy-patterns.txt \
	../quince-local/privacy-patterns.txt))

.PHONY: privacy-check
privacy-check: ## Grep the staged diff against the Operator-private pattern list (no-op if absent)
	@if [ -n "$(PRIVACY_PATTERNS)" ] && [ -f "$(PRIVACY_PATTERNS)" ]; then \
		if git diff --cached -U0 | grep '^+' | grep -inEf "$(PRIVACY_PATTERNS)"; then \
			echo "privacy-check: FAIL — staged lines above match the private pattern list"; \
			exit 1; \
		else \
			echo "privacy-check: OK (patterns: $(PRIVACY_PATTERNS))"; \
		fi \
	else \
		echo "privacy-check: no pattern file found — skipped (contributor/CI mode)"; \
	fi
