# Deployment Security Contract

## Supported Trust Model

Dispatch v1 is self-hosted infrastructure for one trusted organization. The application does not
authenticate API callers, authorize subscription administration, isolate tenants, or terminate
TLS. A supported deployment must place the business API inside a private network or behind an
authenticating reverse proxy/API gateway.

Do not expose the API, `/metrics`, PostgreSQL, Kafka, or Redis directly to the public internet.
Dispatch does not trust identity headers from a proxy and does not make authorization decisions
from them; the gateway remains the enforcement point.

## Network and Transport Responsibilities

- Terminate HTTPS at a trusted ingress, load balancer, or service mesh and encrypt traffic from the
  public/client boundary.
- Restrict API access to approved producer and operator identities at that boundary.
- Restrict `/metrics` to the monitoring network. Metrics are operational data, not a public API.
- Use authenticated, encrypted PostgreSQL, Kafka, and Redis connections when traffic crosses an
  untrusted network. The local Compose files intentionally use development-only plaintext links.
- Apply firewall, security-group, Kubernetes NetworkPolicy, or equivalent rules so workers can
  reach approved receivers and required datastores without granting unnecessary inbound access.
- Treat subscription URLs as trusted operator configuration. Dispatch v1 does not provide complete
  SSRF isolation or destination ownership verification.

## Secret Storage

Subscription secrets and frozen delivery secrets are stored in PostgreSQL without application-level
encryption. They are also present in database backups and available to sufficiently privileged
database operators. Protect database credentials, storage volumes, replicas, logs, exports, and
backups accordingly.

The Kubernetes base expects a pre-provisioned `dispatch-secrets` Secret and does not commit usable
connection values. Prefer an external secret controller or platform secret manager. Do not commit
rendered Secrets or production environment files.

## Webhook Verification

For signed subscriptions, receivers must read the raw request body before decoding JSON and verify:

```text
signed bytes = X-Dispatch-Timestamp + "." + raw request body
expected     = HMAC-SHA256(subscription secret, signed bytes)
header       = "v1=" + lowercase hexadecimal expected digest
```

Compare the received `X-Dispatch-Signature` in constant time. Reject unsupported versions and
timestamps outside the receiver's tolerance; five minutes is the documented starting point. Then
deduplicate by `X-Event-ID` when duplicates matter. A valid signature authenticates the request but
does not change Dispatch's at-least-once delivery guarantee.

The core of a Go receiver check is:

```go
mac := hmac.New(sha256.New, []byte(secret))
mac.Write([]byte(timestamp))
mac.Write([]byte("."))
mac.Write(rawBody)
expected := "v1=" + hex.EncodeToString(mac.Sum(nil))
valid := hmac.Equal([]byte(receivedSignature), []byte(expected))
```

Validate the header version and timestamp before accepting the request, and place a strict size
limit on `rawBody` before reading it.

## Secret Rotation Procedure

1. Configure the receiver to accept the current and replacement secrets.
2. Record the rotation time, then call `PUT /subscriptions/{id}/secret` with
   `{"secret":"<replacement>"}` through the protected operator API boundary.
3. New delivery rows snapshot the replacement secret. Existing delivery rows and their retries
   continue using the previous secret.
4. Keep both secrets valid until no non-terminal deliveries remain from before rotation and the
   accepted retention window has passed.
5. Remove the previous secret from the receiver.

Until v0.13 adds bounded retention operations, operators can check the overlap directly in
PostgreSQL without reading either secret:

```sql
SELECT count(*)
FROM deliveries
WHERE subscription_id = '<subscription-id>'
  AND created_at < '<recorded-rotation-time>'
  AND status IN ('pending', 'processing', 'retrying', 'throttled');
```

Remove the old receiver secret only after this count is zero and the chosen retention/timestamp
overlap has elapsed.

If rollback is required during overlap, rotate the active subscription back to the previous secret.
This affects future delivery initialization only and does not rewrite existing delivery snapshots.

## Explicit Remaining Boundaries

- no application-level API authentication, authorization, or audit identity;
- no tenant isolation;
- no application-level encryption of stored subscription secrets;
- no complete SSRF defense or destination verification;
- no exactly-once or cryptographic anti-replay nonce store;
- no automatic external secret-manager synchronization.

These boundaries are accepted for the v1 single-trust-domain product and must not be represented as
implemented security controls.
