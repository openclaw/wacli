# CLAUDE.md — wacli Go Production Code Standards

This file is loaded by Claude Code on every session. These rules are MANDATORY.

## Build & Test
```bash
go vet ./...
go test ./...
go build ./cmd/wacli/
```

## Seven Iron Rules (Strictly Enforced)

1. **NO panic-capable shortcuts** — `log.Fatal()`, `panic()`, and bare `os.Exit()` are BANNED in library code. Use error returns. Only allowed in `main()` for startup failures.
2. **NO dead code** — No unused variables, parameters, imports, or functions. Code must compile with zero warnings.
3. **NO incomplete implementations** — No `// TODO` stubs that leave broken code paths. Every function must be fully implemented.
4. **Business logic must be verifiable** — All code must pass `go vet`. No speculative interfaces or pseudo-implementations.
5. **Validate with `go vet` and `go test`** — Check correctness before building.
6. **Explicit API and error handling** — Check every error return. Never use `_` to discard errors silently without justification. Wrap errors with `fmt.Errorf("context: %w", err)`.
7. **Minimize allocations** — Prefer slices over maps when order matters. Use `strings.Builder` for concatenation. Pass pointers for large structs. Avoid unnecessary copies.

## Go Error Handling Rules

```go
// ❌ BANNED:
result, _ := riskyOperation()     // silently discarding error
if err != nil { panic(err) }       // panic instead of return

// ✅ REQUIRED:
result, err := riskyOperation()
if err != nil {
    return fmt.Errorf("operation context: %w", err)
}
```

## Logging
- NEVER log tokens, API keys, passwords
- Use structured logging (slog preferred)

## Commit Style
```
feat(scope): description
fix(scope): description
```
English only in commits and code comments.
