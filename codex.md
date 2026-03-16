# codex.md — wacli Go Production Standards

## Build
```bash
go vet ./... && go test ./... && go build ./cmd/wacli/
```

## Seven Iron Rules
1. NO panic/log.Fatal in library code — use error returns
2. NO dead code — zero unused vars/imports/functions
3. NO incomplete implementations — no TODO stubs leaving broken paths
4. All code must pass go vet
5. Validate with go vet + go test
6. Check EVERY error return — never discard with _ silently
7. Minimize allocations — strings.Builder, pointer receivers for large structs
