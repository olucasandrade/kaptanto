# Engineering docs

This folder is **engineering architecture**, not the public product docs.

| Place | Job |
|---|---|
| [`landing/src/data/docs-content.ts`](../landing/src/data/docs-content.ts) | Public docs on kaptan.to (HTML strings + sidebar). Source of truth for user-facing documentation. |
| [`docs/`](.) | Specs, diagrams, and operational notes that live next to the code. |
| [`CLAUDE.md`](../CLAUDE.md) | Invariants cheat sheet and package map for agents and contributors. |

## Contents

- [`architecture/kaptanto_architecture.png`](architecture/kaptanto_architecture.png) — README system diagram.
- [`serverless.md`](serverless.md) — serverless action types (Lambda / Workers / Vercel via webhook).

Companion architecture drafts (Mermaid, Excalidraw prompt, walkthrough notes, technical specification) stay local under `architecture/` and are gitignored.

Do not treat this folder as a substitute for the website. Generating landing pages from these markdown files is a later project; today the site still reads `docs-content.ts`.
