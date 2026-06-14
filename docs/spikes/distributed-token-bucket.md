# Distributed Token Bucket Rate Limiting

> **Status:** Proposed, deferred from the required v1 sequence
> **Promotion condition:** v0.9.0 measurements show that the normalized Redis sliding-window implementation cannot satisfy the accepted rate-control contract or operating envelope.

## Hypothesis

A distributed token bucket could align Redis and in-memory traffic shape, represent
sustained rate plus explicit burst capacity, and use constant state per subscription.

## Why it is not automatically part of v1

The current product problem is contradictory policy semantics, not proven Redis sorted-set
cost. v0.9.0 must first separate rate, burst, and concurrency; persist throttling
correctly; and make fallback behavior observable.

Changing algorithms before that evidence would add Lua, time-authority, rollout, and
mixed-version complexity without proving user benefit. V1 permits the sliding-window
implementation if it satisfies the normalized contract at the measured operating
envelope.

## Potential benefits

- constant-size Redis state per active subscription;
- explicit burst capacity;
- closer semantic alignment with `golang.org/x/time/rate` fallback;
- retry delay based on token refill time;
- lower per-request Redis cleanup work for hot subscriptions.

## Costs and risks

- token bucket does not enforce a strict maximum in every rolling one-second interval;
- distributed refill requires an explicit time authority and precision model;
- old and new key formats need a mixed-version rollout strategy;
- Redis and local fallback still differ in global coordination during degradation;
- an algorithm migration can distract from delivery correctness and operations work that
  is required for v1.

## Investigation questions

1. Does the normalized sliding-window path violate latency, memory, or throughput targets
   under representative hot- and many-subscription workloads?
2. Do intended receivers prefer strict rolling-window protection or sustained rate plus
   burst?
3. What burst default preserves compatibility for existing subscriptions?
4. Should Redis server time be authoritative, and how is fractional-token precision bounded?
5. How do mixed worker versions avoid simultaneous old/new enforcement?
6. Does limiter delay materially improve retry scheduling and backlog behavior?

## Required experiment

After v0.9.0, compare sliding window and a disposable token-bucket prototype for:

- one hot subscription and many moderately active subscriptions;
- Redis script latency and memory per active subscription;
- accepted burst shape and sustained throughput;
- rejection delay and resulting retry backlog;
- Redis outage behavior.

Record the environment and methodology. Promote this spike only if the evidence justifies
the semantic and rollout cost. Promotion requires an ADR and a new exec plan; `v0.10.0`
is reserved for the required per-destination delivery model.
