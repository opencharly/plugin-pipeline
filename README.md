# plugin-pipeline

The workflow front-end for OpenCharly — **domain-neutral**. `charly pipeline run
<entity>` resolves and validates a declared `kind: pipeline` entity, lowers it to
the `(workflow.lobster, charly.yml)` pair, and hands it to the `workflow` engine
that executes it. Also: the bare agent runtime, deterministic probe verbs, and SDD
CUE-first schemas. Any workflow is a declared pipeline; an eval is one pipeline,
any other agent workflow is another.

The plugin is an out-of-tree Go module served over go-plugin gRPC; it provides
`command:pipeline`, `verb:pipeline`, the `kind:pipeline` entity, and the seven
lifted stage verbs.

## What it provides

| Capability | Surface |
|---|---|
| `command:pipeline` | `charly pipeline run <entity>` and `charly pipeline agent` |
| `verb:pipeline` | deterministic probe verbs |
| `kind:pipeline` | the `kind: pipeline` entity (`steps:`, validated by `sdk/workflowkit`) |
| `verb:agent` … `verb:gate` | the seven lifted stage verbs |

- **`charly pipeline run <entity>`** — resolve a `kind: pipeline` entity from
  `charly.yml`, validate it with `sdk/workflowkit.ValidatePipeline`, lower it with
  `sdk/workflowkit.Lower` to `workflow.lobster` + a generated `charly.yml`, and
  dispatch the pair to the `workflow` provider class as `workflow-run`. The
  ENGINE owns execution, control flow, redo, and iteration. `--dry-run` validates
  and lowers without dispatching. The reference grammar (`$pr` / `$calver` /
  `$workdir` / `$env.NAME`) is resolved by the engine and by each verb body's
  template.
- **`charly pipeline agent`** — the bare runtime: direct chat-completions with a
  fully configurable system prompt, skills appended from the candies (`@github`
  refs), tools by reference.
- **`verb:pipeline`** — deterministic probes: `media_gate`, `lock_audit`,
  `sequencing`, `head_freshness`, `config_audit`, `golden_present`, `lanes_ok`,
  `resolve_channel`.
- **`verb:agent` / `verb:probe` / `verb:ade` / `verb:generate` / `verb:emit` /
  `verb:media` / `verb:gate`** — the seven stage bodies as ordinary verbs, each
  reachable as a `<word>: <input>` step from any plan.
- **SDD** — `schema/pipeline.cue` is the single source for the per-verb input
  shapes; `task cue:gen` emits
  `params/cue_types_gen.go` (committed, CI-reproducible); every authored input is
  validated at load.

## Two workflows, one engine — who is who

- **The omarchy PR-eval lane** is ONE plan on this engine:
  `opencharly/eval-charly` (`eval-lane-plan`) evaluates `omacom/omarchy` PRs on
  golden VMs (verdicts **PASS | FAIL | NO_VALIDATION**).
- **The opencharly org PR validator** is a SEPARATE workflow with a SEPARATE
  plugin — `opencharly/plugin-review` (`charly review`, Verdict **PASS|BLOCK**).
  It does NOT run on this engine.

Keep the two apart: this repo has no `review` verb and no auto-merge; the org
validator has no `pipeline` verb and no eval lane.

## Layout

- `candy/plugin-pipeline/` — the plugin module: `plugin.go`, `cli.go`,
  `entity.go`, `verb_env.go`, `refs.go`, the lifted verb bodies
  (`verb_agent.go`, `verb_probe.go`, `verb_ade.go`, `verb_generate.go`,
  `verb_emit.go`, `verb_media.go`, `verb_gate.go`), `agent.go`, `probes.go`,
  `render.go`, `ade.go`, `emit.go`, `media.go`, `llm.go`, `tools.go`,
  `schema/pipeline.cue`, `params/cue_types_gen.go`, and `cmd/serve/main.go`.
- `charly.yml` — the root project manifest (`discover: candy`); the embedded
  `pipeline-skill:` skill entity lives in `candy/plugin-pipeline/charly.yml`.
- `.github/workflows/ci.yml` — the repo's own `gofmt`/`vet`/`test` + generated-
  params-reproducibility job.
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Related

- Owning skill: `/charly-pipeline:pipeline` — the `charly pipeline` engine
  reference (projected from this candy's own `pipeline-skill:` entity).
- `/charly-internals:plugin` — the plugin/provider model.
- `/charly-internals:go` — `kind:` schema authoring (SDD).
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
