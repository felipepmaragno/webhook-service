# Pre-v0.14 Destination Protection Simplification

## Lesson

A study project can become harder to learn from when it keeps adding realistic features without a
clear product need. Redis-backed rate limiting, circuit breakers, distributed semaphores, burst
settings, and concurrency settings were individually reasonable ideas, but together they made the
destination-protection contract larger than the v1 goal required.

The better v1 tradeoff is one understandable promise:

> Dispatch applies a per-destination `max_delivery_rate` guardrail before HTTP delivery.

That keeps the behavior visible, testable, and operationally explainable without forcing the project
to own a distributed coordination product.

## Engineering reasoning

- Product contracts should decide implementation complexity, not the other way around.
- A removed feature is only safe when product, spec, docs, tests, schema, and runtime all stop
  depending on it.
- One configuration field is easier to operate than three only if its semantics are honest.
  `max_delivery_rate` is documented as a guardrail, not a precise global guarantee.
- Historical ADRs still have value, but current-state docs must clearly supersede them when the
  accepted direction changes.
- Shrinking the runtime surface before operational readiness reduces the number of runbooks, alerts,
  failure modes, and capacity claims v1 must support.

## Practical guidance for future increments

- If a feature mainly demonstrates sophistication, question it before it enters v1.
- If a feature creates a new state machine, external dependency, or cross-worker guarantee, require
  a measured bottleneck or explicit user problem.
- Keep one increment per MR. When implementation starts pulling in a second independent objective,
  stop and split the plan.
- Prefer a smaller product with stronger tests, documentation, and operability over a broader system
  with weak ownership of each behavior.
