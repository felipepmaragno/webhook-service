# Documentation Model

This document defines how Dispatch documentation is organized and which file owns each kind of
information. Its purpose is to prevent duplicated project descriptions and conflicting statements as
the project changes.

## Core Rule

Each durable fact should have one authoritative home. Other documents may link to that authority or
summarize it briefly for navigation, but they should not restate the full explanation.

When two documents explain the same behavior in detail, treat it as documentation drift risk.

## Authorities

| File | Owns | Should not own |
|------|------|----------------|
| `README.md` | Entry point, quick start, main commands, links to deeper docs | Full product definition, full behavior spec, long operational procedures |
| `docs/product.md` | Product purpose, users, promise, boundaries, maturity, non-goals | Implementation mechanisms, API field-by-field behavior, validation logs |
| `docs/spec.md` | Observable behavior, API semantics, delivery states, invariants | Product strategy, architecture diagrams, implementation history |
| `docs/architecture.md` | Runtime structure, component boundaries, implementation mechanisms | Product promise, future roadmap, detailed runbooks |
| `docs/operations.md` | Minimal run/validate/inspect/failure guidance | Product definition, full behavior spec, historical performance discussion |
| `docs/LIMITATIONS.md` | Accepted limitations and possible future responses | Active roadmap authority, implementation procedure |
| `docs/v1-roadmap.md` | Accepted finite v1 sequence and release gate | Day-to-day progress, detailed execution steps |
| `PROGRESS.md` | Current verified state, validation evidence, next starting point | Product definition, long explanations, duplicated docs model |
| `docs/next-steps.md` | Strategic orientation when no exec plan is active | Active execution authority once a plan exists |
| `docs/exec-plans/active/` | Temporary authority for the current bounded increment | Permanent product or behavior authority after completion |
| `docs/exec-plans/done/` | Historical execution evidence | Current behavior source of truth |
| `docs/adr/` | Durable technical decisions and supersession history | Current behavior when later docs supersede the decision |
| `docs/spikes/` | Unaccepted investigations and future options | Roadmap commitment |
| `internal/*/README.md` | Local package ownership, invariants, hazards, verification guidance | Product description, roadmap, broad architecture narrative |
| `docs/learnings/` | Lessons and mentoring notes from development | Current system authority |

## Project Description Duplication

The project description should be layered:

1. `README.md` gives a short entry-point description.
2. `docs/product.md` owns the complete product description.
3. `docs/spec.md` owns behavior visible to callers, workers, operators, and receivers.
4. `docs/architecture.md` owns how the implementation is structured.

If README, product, spec, and architecture all contain similar paragraphs, keep the shortest useful
summary in README and replace duplicated detail with links to the authoritative document.

## Exec Plan Closure

When an exec plan completes:

1. Move durable product meaning into `docs/product.md` only if the product changed.
2. Move durable observable behavior into `docs/spec.md` only if behavior changed.
3. Move durable technical rationale into ADRs only if a decision should persist.
4. Move local implementation guidance into package READMEs only if package ownership or hazards
   changed.
5. Update `PROGRESS.md` with verified state and the next starting point.
6. Leave the completed exec plan as historical evidence, not current authority.

## Editing Checklist

Before adding a new paragraph to a durable doc, ask:

- Is this current behavior, planned behavior, or historical context?
- Which document owns that kind of fact?
- Can this document link to the authority instead of restating it?
- Will this sentence still be true after the current exec plan is done?
- Is this explanation local to one package, or is it system-level?

Prefer shorter summaries plus links over repeated full explanations.
