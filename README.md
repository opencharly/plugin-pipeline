# plugin-pipeline

The generic agent/workflow engine for OpenCharly — **domain-neutral**. The plan
executor (`charly pipeline run <entity>`), the bare agent runtime, deterministic
probe verbs, and SDD CUE-first schemas. Any pipeline is a declared plan; an eval
is one plan, any other agent workflow is another.

The plugin is an out-of-tree Go module served over go-plugin gRPC; it provides
`command:pipeline`, `verb:pipeline`, and the `kind:pipeline` entity.

## What it provides

| Capability | Surface |
|---|---|
| `command:pipeline` | `charly pipeline run <entity>` and `charly pipeline agent` |
| `verb:pipeline` | deterministic probe verbs |
| `kind:pipeline` | the `kind: pipeline` entity |

- **`charly pipeline run <entity>`** — execute a `kind: pipeline` entity from
  `charly.yml`: stages (`agent`/`probe`/`ade`/`generate`/`emit`/`media`/`gate`/
  `command`), a per-run ledger, bounded redo with the loop guard, FAIL-HARD, and
  the reference grammar (`$pr` / `$calver` / `$workdir` / `$env.NAME` /
  `@stage.output`).
- **`charly pipeline agent`** — the bare runtime: direct chat-completions with a
  fully configurable system prompt, skills appended from the candies (`@github`
  refs), tools by reference.
- **`verb:pipeline`** — deterministic probes: `media_gate`, `lock_audit`,
  `sequencing`, `head_freshness`, `config_audit`, `golden_present`, `lanes_ok`,
  `resolve_channel`.
- **SDD** — `schema/pipeline.cue` is the single source; `task cue:gen` emits
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
  `executor.go`, `agent.go`, `probes.go`, `render.go`, `schema/pipeline.cue`,
  `params/cue_types_gen.go`, and `cmd/serve/main.go`.
- `charly.yml` — the root project manifest (`discover: candy`) + the embedded
  `pipeline-skill:` skill entity.
- `.github/workflows/ci.yml` — the repo's own `gofmt`/`vet`/`test` + generated-
  params-reproducibility job.
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Related

- Owning skill: `/charly-pipeline:pipeline` — the `charly pipeline` engine
  reference (projected from this candy's own `pipeline-skill:` entity).
- `/charly-internals:plugin` — the plugin/provider model.
- `/charly-internals:go` — `kind:` schema authoring (SDD).
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
