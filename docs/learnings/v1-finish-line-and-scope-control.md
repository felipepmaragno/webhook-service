# Defining a V1 Finish Line

## Why a roadmap is not enough

A list of desirable features does not define completion. It tends to grow whenever a new
technical possibility appears. A finish line instead starts with a target user and a
product promise, then requires evidence that the promise is satisfied.

For Dispatch, the accepted user is an engineering or platform team operating the service
inside one trusted organization. This rules out multi-tenancy, billing, customer portals,
and managed-service obligations without claiming those ideas have no value.

## Architecture is not product progress

Kafka, Redis, token buckets, databases, and service decomposition are implementation
choices. They count as progress only when they strengthen an accepted behavior at the
expected workload.

The token-bucket plan illustrated this distinction. Normalizing rate, burst, concurrency,
throttling, and degradation is required because current behavior is contradictory.
Replacing the Redis algorithm is optional until measurement shows that the current
algorithm cannot satisfy the normalized contract.

## The domain model determines feature quality

The most important remaining correction is per-subscription delivery state. Replay,
auditing, retries, and operator visibility are all weaker when one aggregate event owns
several independent destination outcomes.

This correction is split into two increments:

1. establish identity, schema, repositories, projections, and compatibility;
2. move initial processing, retry ownership, and queries to that model.

Splitting the work reduces migration risk and makes the source-of-truth transition
explicit. The intermediate state is acceptable only because the new model is not falsely
claimed as active processing behavior.

## Release criteria must be observable

“Production ready” is not a useful checkbox by itself. The v1 release gate asks for
evidence:

- independent destination outcomes can be queried;
- recoverable work has no known silent-loss path;
- terminal deliveries can be replayed through a supported operation;
- signatures have test vectors;
- backlog and degraded operation are observable;
- installation, upgrade, backup, restore, and retention procedures are exercised;
- the complete validation pipeline passes.

Each statement can fail and can be demonstrated. That makes it an engineering criterion
rather than an aspiration.

## Feature freeze as a decision tool

Until v1, new work must close a release criterion, fix a threatening defect, or reduce a
demonstrated risk in the active increment. Everything else remains a spike or post-v1
note.

This is not opposition to learning or experimentation. It protects the chosen learning
objective: completing a coherent complex system is a different and valuable skill from
continuously expanding one.
