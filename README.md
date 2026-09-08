# plugin-pipeline

The generic agent/workflow engine — **domain-neutral**: the plan executor
(`charly pipeline run <entity>`), the bare agent runtime (the plan `agent` stage +
the standalone `charly pipeline agent`), deterministic probe verbs (`verb:pipeline`),
the `kind: pipeline` entity, and SDD CUE-first schemas (`task cue:gen` → generated
params). Any pipeline is a declared plan; an eval is one plan, any other agent
workflow is another.

- **`charly pipeline run <entity>`** — execute a `kind: pipeline` entity from
  charly.yml: stages (`agent`/`check`/`probe`/`generate`/`media`/`gate`/`command`),
  a per-run ledger, bounded redo with the loop guard, FAIL-HARD, and the reference
  grammar (`$pr` / `$calver` / `$workdir` / `$env.NAME` / `@stage.output`).
- **`charly pipeline agent`** — the P1 runtime: direct chat-completions with a fully
  configurable system prompt, skills appended from the candies (@github refs),
  tools by reference.
- **`verb:pipeline`** — deterministic probes: media_gate, lock_audit, sequencing,
  head_freshness, config_audit, golden_present, lanes_ok, resolve_channel.
- **SDD** — `schema/pipeline.cue` is the single source; `task cue:gen` emits
  `params/cue_types_gen.go` (committed, CI-reproducible); every authored input is
  validated at load.
