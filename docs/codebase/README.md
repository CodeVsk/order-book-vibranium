# docs/codebase — core knowledge base for AI agents

This folder is the deep reference for AI agents (and humans) working in this
repository. Root `CLAUDE.md` stays the short, auto-loaded orientation layer —
purpose, make commands, quick invariant summaries, pointers here. These files
hold the detail CLAUDE.md deliberately omits to stay small: exact versions,
full test-suite inventory, verbatim domain-comment citations, deployment
topology, and tracing wiring.

| File | Read this when you need to know... |
|---|---|
| [`ARCHITECTURE.md`](./ARCHITECTURE.md) | How the three binaries fit together, the deployment topology (docker-compose, nginx, Dockerfiles), the key invariants with file:line citations, package layout, and the database schema. |
| [`STACK.md`](./STACK.md) | Exact pinned versions of every dependency, datastore image, and tool (go.mod, docker-compose, k6 profiles), grouped by concern. |
| [`TESTS.md`](./TESTS.md) | The full test taxonomy (unit / integration / concurrency / idempotency), what each test file covers, and how the shared `testenv` harness works. |
| [`PRINCIPLES.md`](./PRINCIPLES.md) | *Why* the domain code is shaped the way it is — verbatim invariant comments from `internal/domain/*` with citations, plus the small glossary of domain terms. |
| [`OBSERVABILITY.md`](./OBSERVABILITY.md) | How OpenTelemetry tracing and Jaeger are wired across all three binaries, including two security-relevant caveats in `internal/platform/telemetry`. |

## Relationship to other docs

- Root `README.md` remains the human-facing doc (getting started, full HTTP
  API examples); this folder is agent-oriented reference, not a replacement.

## Single-source-of-truth rule

Each fact lives in exactly one file here; other files link to it instead of
repeating it (e.g. observability wiring lives only in `OBSERVABILITY.md`,
dependency versions only in `STACK.md`). When adding new facts, extend the
file that already owns that topic rather than creating overlap.

## Deliberately not (yet) created

No ADR directory: the two architectural decisions on record ("design
decision #1/#2") already live in `PRINCIPLES.md`. If a third one comes
along, that's the trigger to introduce `docs/adr/`.
