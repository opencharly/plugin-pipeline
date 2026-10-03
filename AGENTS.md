# AGENTS.md — plugin-pipeline

Standalone plugin repo owning the workflow FRONT-END
(`command:pipeline` + `verb:pipeline` + `kind:pipeline`). The plugin is a Go
module at `candy/plugin-pipeline/` (module path
`github.com/opencharly/plugin-pipeline/candy/plugin-pipeline`); the root
`charly.yml` declares `discover: candy` (so the repo is a project and its candy is
scanned) and carries the embedded `pipeline-skill:` skill entity.

`charly pipeline run <entity>` resolves the authored entity, validates it with
`sdk/workflowkit.ValidatePipeline`, lowers it to the `(workflow.lobster,
charly.yml)` pair with `sdk/workflowkit.Lower`, and dispatches the pair to the
`workflow` PROVIDER CLASS as `workflow-run`. EXECUTION is the engine's — the
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
- `.github/workflows/ci.yml` — the repo's own `gofmt`/`vet`/`test` + generated-
  params-reproducibility job.
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
  `params/cue_types_gen.go`, and `ci.yml` fails if the committed generated params
  do not reproduce.
- Keep this engine **domain-neutral**: the omarchy PR-eval lane and the org
  `plugin-review` validator are separate workflows. Do not add a `review` verb or
  auto-merge here.
- Keep the authored `kind: pipeline` grammar in ONE place: the entity body IS
  `spec.Pipeline` (`steps:`) — this repo declares no grammar for it, and the load
  gate is `sdk/workflowkit.ValidatePipeline`. `schema/pipeline.cue` declares only
  the per-verb input shapes + the LLM/probe surface.
- Keep the `pipeline-skill:` entity in step with any provider-surface change — it
  is the projected source for `/charly-pipeline:pipeline`.

## Landing

Load `/charly-internals:git-workflow` before any git/PR action; it owns the
landing mechanics. The authoritative rulebook is the umbrella `AGENTS.md` in
`opencharly/opencharly` and `charly/AGENTS.md` in the charly repo.
