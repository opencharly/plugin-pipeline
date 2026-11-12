# AGENTS.md — plugin-pipeline

Standalone plugin repo owning the workflow FRONT-END
(`command:pipeline` + `verb:pipeline` + `kind:pipeline`). The plugin is a Go
module at `candy/plugin-pipeline/` (module path
`github.com/opencharly/plugin-pipeline/candy/plugin-pipeline`); the root
`charly.yml` declares `discover: candy` (so the repo is a project and its candy is
scanned) and carries the embedded `pipeline-skill:` skill entity.

`charly pipeline run <entity>` resolves the authored entity, validates it with the
repo-local `workflowkit.ValidatePipeline`, lowers it to the `(workflow.lobster,
charly.yml)` pair with `workflowkit.Lower`, and dispatches the pair to the
`workflow` PROVIDER CLASS as `workflow-run`. The `kind: pipeline` ENTITY BODY is this
repo's own (`schema/pipeline.cue`'s `#Pipeline` / `#PipelineValue`), declared on the
`kind` capability as `InputDef: "#PipelineValue"` — a kind body lives with the plugin
that serves it, and the contract module keeps only #PipelineClassRoutes. EXECUTION is the engine's — the
`workflow` provider (plugin-lobster today), NOT this repo. The seven stage kinds
survive as ordinary `verb:` providers (`verb:agent/probe/ade/generate/emit/media/
gate`), each a declarative body reachable from any plan.

Canonical files:

- `candy/plugin-pipeline/charly.yml` — the `plugin-pipeline:` candy entity
  (`plugin:` block, `plan:` checks) + the `pipeline-skill:` skill entity.
- `candy/plugin-pipeline/` — the Go source: `plugin.go`, `cli.go`, `entity.go`,
  `verb_env.go`, `refs.go`, `verb_<word>.go` (the seven lifted verb bodies),
  `agent.go`, `probes.go`, `render.go`, `ade.go`, `emit.go`, `media.go`,
  `llm.go`, `tools.go`, `schema/pipeline.cue`,
  `params/cue_types_gen.go` (generated — do not hand-edit), `cmd/serve/main.go`.
- `charly.yml` — the root manifest (`discover: candy`) referencing the candy's own
  `pipeline-skill:` skill entity (declared in `candy/plugin-pipeline/charly.yml`).
- `.github/workflows/ci.yml` — the repo's own `gofmt`/`vet`/`test` + the
  generated-params-reproducibility test (in-process, via `spec/schemaparams`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.
- `README.md` — user overview only; never agent guidance.

## Load these skills first (R0)

- `/charly-pipeline:pipeline` — the `charly pipeline` engine reference (projected
  from this candy's own `pipeline-skill:` entity). Load before changing the CLI
  run leg, a lifted verb body, or a probe verb.
- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:`
  block, the unified Provider model, the per-plugin CUE-schema contract.
- `/charly-internals:go` — SDD and the schema → generated-code pipeline
  (`charly task cue-gen`).
- `/charly-internals:git-workflow` — before any git/PR action.

## Build / validate / test

- `go build ./...` in `candy/plugin-pipeline/` — compile the plugin module.
- `go test ./...` in `candy/plugin-pipeline/` — the plugin's Go tests.
- `gofmt -l .` and `go vet ./...` in `candy/plugin-pipeline/` — the repo's own
  `ci.yml` gate.
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate. Its own `ci.yml` is an additional repo-level job.

## Modify this repo

- Edit the `plugin-pipeline:` candy entity, the Go source, and
  `schema/pipeline.cue` **together** — the schema is the single source for
  `params/cue_types_gen.go` (regenerate with `charly candy params plugin-pipeline`),
  and `TestGeneratedParamsAreCurrentAndReproducible` fails if the committed params
  do not reproduce or are stale.
- Keep this engine **domain-neutral**: the omarchy PR-eval lane and the org
  `plugin-review` validator are separate workflows. Do not add a `review` verb or
  auto-merge here.
- The authored `kind: pipeline` grammar lives in ONE place and it is HERE: the
  entity body (`steps:`) is `#Pipeline`/`#PipelineValue` in `schema/pipeline.cue`, and
  the load gate is this repo's `workflowkit.ValidatePipeline`. The schema declares the
  per-verb input shapes, the probe surface and the kind body, and IMPORTS everything
  the contract module owns — the six LLM defs, and the shared grammar defs the moved
  body references (`schema.#StrVal`, `schema.#Step`, `schema.#TaskParamSpec`,
  `schema.#WorkflowArgVal`) — one name, one home, never a local copy.
- `workflowkit/` is the kind-aware machinery for THIS kind: `validate.go`, `lower.go`,
  `graph.go` plus their tests and `testdata/`. It uses the CONTRACT's kinds-blind
  helpers rather than restating them — `sdk/workflowkit.RefNames`/`Validate`/`RefIDs`
  (the expression grammar) and `sdk/workflowkit.PlanRefs`/`RewritePlanRefs` (the plan
  ref steps). A second copy of the ref grammar would be a second implementation of a
  rule the validator also uses (R2), so never fork one.
- Keep the `pipeline-skill:` entity in step with any provider-surface change — it
  is the projected source for `/charly-pipeline:pipeline`.

## Landing

Load `/charly-internals:git-workflow` before any git/PR action; it owns the
landing mechanics. The authoritative rulebook is the umbrella `AGENTS.md` in
`opencharly/opencharly` and `charly/AGENTS.md` in the charly repo.
