# Engineering docs

This folder is **engineering architecture**, not the public product docs.

| Place | Job |
|---|---|
| [`landing/src/data/docs-content.ts`](../landing/src/data/docs-content.ts) | Public docs on kaptan.to (HTML strings + sidebar). Source of truth for user-facing documentation. |
| [`docs/`](.) | Specs, diagrams, and operational notes that live next to the code. |
| [`CLAUDE.md`](../CLAUDE.md) | Invariants cheat sheet and package map for agents and contributors. |

## Contents

- [`architecture/technical-specification.md`](architecture/technical-specification.md) — authoritative architecture specification.
- [`architecture/kaptanto_architecture.png`](architecture/kaptanto_architecture.png) — README system diagram.
- [`architecture/`](architecture/) — companion Mermaid, Excalidraw prompt, and walkthrough notes.
- [`serverless.md`](serverless.md) — serverless action types (Lambda / Workers / Vercel via webhook).

Do not treat this folder as a substitute for the website. Generating landing pages from these markdown files is a later project; today the site still reads `docs-content.ts`.
