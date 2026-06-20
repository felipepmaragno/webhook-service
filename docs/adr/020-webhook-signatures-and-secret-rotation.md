# ADR 020: Webhook Signatures and Secret Rotation

## Status

Accepted

## Context

Before v0.12.0, Dispatch emitted `X-Signature`, but its value was only a prefix of the payload
encoded as hex. It did not use the configured secret and provided no authenticity guarantee. V1 requires a
receiver-verifiable contract, replay guidance, test vectors, and rotation behavior compatible with
the frozen per-delivery state established by ADR 018.

The API is deployed inside one trusted organization and has no application-level authentication.
That accepted trust model does not justify returning webhook secrets to callers or presenting the
API as safe for direct public exposure.

## Decision

For a subscription with a non-empty secret, Dispatch sends:

```http
X-Dispatch-Timestamp: <Unix seconds>
X-Dispatch-Signature: v1=<lowercase hexadecimal HMAC-SHA256>
```

The HMAC input is the exact byte concatenation:

```text
ASCII(X-Dispatch-Timestamp) || "." || raw HTTP request body
```

The key is the subscription secret. Receivers must verify the raw body before parsing it and use a
constant-time comparison such as Go's `hmac.Equal`. Receiver policy should reject timestamps outside
a bounded tolerance; five minutes is the documented starting point, not a sender-side guarantee.
Receivers must also deduplicate by `X-Event-ID` when duplicates matter because valid signed requests
can be repeated by at-least-once delivery.

Unsigned subscriptions omit both headers. The old `X-Signature` placeholder is removed rather than
preserved because it was explicitly documented as non-cryptographic and had no supported security
contract.

Subscription secrets are write-only through the API. Creation, listing, and rotation responses use
API response types that cannot serialize the secret.

Rotation replaces the active subscription secret used when future delivery rows are initialized.
Existing delivery rows retain their frozen secret and continue using it for retries. Operators must
therefore accept both old and new secrets at the receiver until old-secret deliveries are terminal
or outside the accepted retention window. Dispatch provides an explicit rotation operation; direct
database edits are not the supported workflow.

Secrets remain plaintext within PostgreSQL and its backups in v1. Database access, encrypted
transport, storage encryption, backup protection, TLS termination, API authentication, and network
restriction remain deployment responsibilities.

## Test Vector

```text
secret:     test-secret
timestamp:  1700000000
body:       {"id":"evt_123","type":"order.created","source":"billing","data":{"amount":99}}
signed:     1700000000.{"id":"evt_123","type":"order.created","source":"billing","data":{"amount":99}}
digest:     11e32a31840c9130f47da0546afd791d0ce053f7dc552a0b3a4fb118bcce6096
header:     v1=11e32a31840c9130f47da0546afd791d0ce053f7dc552a0b3a4fb118bcce6096
```

## Consequences

- Receivers can authenticate Dispatch webhook requests without depending on JSON reserialization.
- Timestamp checks reduce replay exposure but do not provide exactly-once delivery or a nonce store.
- A secret rotation has an intentional overlap period because delivery snapshots are immutable.
- Removing the placeholder header is a pre-v1 compatibility break with behavior that was never a
  security guarantee.
- Database readers can still access stored secrets; deployment controls remain essential.

## Related

- [ADR 018: Per-Subscription Delivery Identity](018-per-subscription-delivery-identity.md)
- [System behavior specification](../spec.md)
- [v0.12.0 execution plan](../exec-plans/done/v0.12.0.md)
