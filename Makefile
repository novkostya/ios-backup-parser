# ios-backup-parser — driver targets.
# Gate targets (containerized Go toolchain, nerdctl/docker autodetect) land in M1
# per the charter; until then this file carries only the commit-time privacy gate.

# The Operator-private pattern list lives in the quince checkout's private local/
# layer; on Operator machines that checkout sits next to this repo. Absent file =>
# the target no-ops (contributor / CI mode).
PRIVACY_PATTERNS ?= $(firstword $(wildcard \
	../iphone-backup-app/local/privacy-patterns.txt \
	../quince/local/privacy-patterns.txt))

.PHONY: privacy-check
privacy-check:
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
