# Performance Measurement Boundaries

## Lesson

A throughput number is meaningful only when the start and end boundaries match the question.
Dispatch has at least three independent rates: API acceptance, steady-state delivery, and retry
recovery. Combining them hides bottlenecks and can produce confident but incorrect conclusions.

## What the baseline exposed

The first automated Kafka drain started its timer immediately before starting the worker. The
result therefore included process startup, Kafka consumer-group join, partition assignment, HTTP
delivery, and PostgreSQL persistence. That is a valid cold-start recovery measurement, but it is
not a sustained-delivery measurement.

Comparing that number directly with a sustained 1,000 deliveries/second objective would mix two
different service-level questions. The harness now labels it as diagnostic instead of declaring
the target met or missed.

## Practical rules

1. Stop producers when measuring drain capacity; keep them active when measuring sustained flow.
2. Define whether startup, rebalance, retries, and persistence are inside the timed boundary.
3. Use durable PostgreSQL state as completion authority, not receiver request counts alone.
4. Preserve correctness assertions even when throughput targets are informational.
5. Change one capacity control at a time and compare medians from repeated runs.
6. Keep smoke datasets for mechanics; do not evaluate capacity targets from startup-dominated runs.

These rules prevent optimization work from being driven by measurements that answer a different
question from the one the product actually cares about.
