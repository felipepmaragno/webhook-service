# Retention Cleanup

> Local implementation context for engineers and coding agents. Read this file before changing
> retention scheduling, cleanup ordering, or worker shutdown behavior.

## Ownership

This package coordinates cleanup timing and observability. PostgreSQL owns row eligibility,
locking, batching, and deletion semantics. Worker assembly owns configuration and lifecycle.

## Invariants

- Run attempt-body redaction before terminal-event deletion in every cycle.
- Never overlap cycles within one worker.
- Repository failures are reported but do not stop future cycles or delivery processing.
- `Stop` waits for an in-flight cycle.
- Multiple workers are expected; correctness comes from PostgreSQL `SKIP LOCKED`, not a leader lock.
- Logs and metrics expose counts and failures, never retained response bodies.

Defaults and observable behavior are defined in
[`docs/spec.md`](../../docs/spec.md) and
[`ADR 022`](../../docs/adr/022-bounded-data-retention.md).

## Verification

```bash
go test -race ./internal/retention/...
go test ./internal/repository/postgres/... -run Retention
```
