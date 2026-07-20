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
