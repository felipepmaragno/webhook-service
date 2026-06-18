# Kafka Delivery Subsystem

> Local implementation context for engineers and coding agents. Read this file before
> changing the consumer, delivery handler, webhook protocol, or Kafka persistence flow.

## Authority

This file explains how this package currently implements established contracts. It does not
replace the living behavior contract in `docs/spec.md`, architectural decisions in `docs/adr/`,
or the active execution plan. If they disagree, stop and reconcile the durable documentation
before changing code.

## Responsibilities

- `producer.go`: publish accepted API events to Kafka and propagate trace headers.
- `consumer.go`: collect Kafka messages, decode them, invoke the handler, and manually commit offsets.
- `handler.go`: initialize frozen delivery sets, claim processable delivery rows, coordinate delivery execution, and persist outcomes.
- `delivery.go`: apply circuit breaker, rate limiter, semaphore, per-delivery HTTP execution, and retry classification.
- `webhook.go`: build the receiver HTTP request and classify HTTP responses.

## Critical invariants

1. Kafka offsets are committed only after `ProcessBatch` returns with durable outcome persistence complete.
2. A persistence error leaves the whole fetched batch uncommitted unless the initialized delivery lease already represents recoverable work.
3. Kafka-originated events initialize deliveries before external HTTP calls, then persist claimed delivery outcomes with owner/deadline fencing.
4. One event freezes matching subscriptions into delivery rows. Event status is a projection of those rows.
5. Circuit-breaker, rate-limit, and semaphore rejection do not perform an HTTP call and produce a `throttled` outcome.
6. Malformed Kafka messages are poison-message exceptions: they are committed after decode failure.
7. Trace ID is carried in a Kafka header, injected into context, and forwarded to the receiver.

Do not move the offset commit earlier, swallow repository errors, or split event-state and attempt persistence
without changing ADR 015 and adding failure-path tests first.

## Known hazards

- Delivery is at-least-once, not exactly-once. An HTTP success followed by database failure can be repeated.
- One failed delivery persistence operation can redeliver every message in the fetched Kafka batch.
- Legacy attempt rows may not contain `subscription_id`; new runtime attempts must be delivery/subscription attributed.
- `computeHMAC` is currently a placeholder, not cryptographic HMAC-SHA256. Do not treat `X-Signature` as a
  security guarantee until a dedicated fix introduces `crypto/hmac`, compatibility tests, and documentation updates.
- Subscription-load failure leaves the Kafka batch uncommitted because the destination set could not be frozen safely.
- Throttled outcomes must not increment event attempts or write delivery-attempt rows.

## Change protocol

Before editing this package:

1. Read `docs/spec.md`, ADR 012, ADR 015, and the active exec plan.
2. Add or update a focused package test first. Keep mocks local to the `_test.go` file.
3. For commit semantics, test both the successful commit path and the persistence-failure no-commit path.
4. For fan-out changes, test frozen delivery sets, duplicate Kafka processing, mixed subscription outcomes, and attempt attribution.
5. Run:

```bash
go test -race ./internal/kafka/...
go test ./internal/repository/postgres/...
go test ./internal/app/...
```

Update this file when package ownership, ordering invariants, failure behavior, or required verification changes.
