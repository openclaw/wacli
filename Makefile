TAG ?=
COMMIT ?=
CROSS_PLATFORM_DIR ?=
CROSS_PLATFORM_MANIFEST_SHA256 ?=
RELEASE_OUTPUT ?= dist/release/$(TAG)
CANDIDATE_DIR ?=
MAC_RELEASE ?=

.DEFAULT_GOAL := help

.PHONY: help build test fmt lint release-check check snapshot release verify-release

help:
	@printf '%s\n' \
		'Available targets:' \
		'  help            Print available targets (default).' \
		'  build           Build the CLI into dist/wacli.' \
		'  test            Run the full Go, FTS, cross-build, and docs test suite.' \
		'  fmt             Check Go formatting.' \
		'  lint            Run vet, vulnerability, and dead-code checks.' \
		'  check           Run every local gate enforced by CI.' \
		'  snapshot        Build credential-free release artifacts.' \
		'  release         Build, sign, notarize, and verify official release artifacts.' \
		'  verify-release  Verify existing release artifacts.'

build:
	GOWORK=off pnpm --silent build

test:
	GOWORK=off pnpm --silent test

fmt:
	GOWORK=off pnpm --silent format:check

lint:
	GOWORK=off pnpm --silent lint
	@test "$$(GOWORK=off go env GOVERSION)" = go1.27.1
	GOWORK=off pnpm --silent govulncheck:source
	@set -e; \
	output_file="$$(mktemp)"; \
	trap 'rm -f "$$output_file"' EXIT; \
	if ! CGO_ENABLED=1 GOWORK=off go run golang.org/x/tools/cmd/deadcode@v0.49.0 -test -tags sqlite_fts5 ./... > "$$output_file"; then cat "$$output_file"; exit 1; fi; \
	if [ -s "$$output_file" ]; then cat "$$output_file"; exit 1; fi

release-check:
	$${GORELEASER:-goreleaser} check --config .goreleaser.yaml
	$${GORELEASER:-goreleaser} check --config .goreleaser-linux-windows.yaml

check: fmt lint test build release-check
	git diff --check

snapshot:
	GOWORK=off $${GORELEASER:-goreleaser} release --snapshot --clean --skip=publish --config .goreleaser.yaml

release:
	@test -n "$(TAG)" -a -n "$(COMMIT)" -a -n "$(CROSS_PLATFORM_DIR)" -a -n "$(CROSS_PLATFORM_MANIFEST_SHA256)" -a -n "$(MAC_RELEASE)" || \
		(printf '%s\n' 'usage: make release TAG=vX.Y.Z COMMIT=<full-sha> CROSS_PLATFORM_DIR=<dir> CROSS_PLATFORM_MANIFEST_SHA256=<sha256> MAC_RELEASE=<path> [RELEASE_OUTPUT=<dir>]' >&2; exit 2)
	"$(MAC_RELEASE)" codesign-run -- \
		node scripts/release-local.mjs prepare \
			--tag "$(TAG)" \
			--commit "$(COMMIT)" \
			--cross-platform-dir "$(CROSS_PLATFORM_DIR)" \
			--cross-platform-manifest-sha256 "$(CROSS_PLATFORM_MANIFEST_SHA256)" \
			--output "$(RELEASE_OUTPUT)"

verify-release:
	@test -n "$(TAG)" -a -n "$(COMMIT)" -a -n "$(CANDIDATE_DIR)" || \
		(printf '%s\n' 'usage: make verify-release TAG=vX.Y.Z COMMIT=<full-sha> CANDIDATE_DIR=<dir>' >&2; exit 2)
	node scripts/verify-release-candidate.mjs \
		--dir "$(CANDIDATE_DIR)" \
		--tag "$(TAG)" \
		--commit "$(COMMIT)"
