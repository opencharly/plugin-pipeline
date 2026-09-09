# plugin-pipeline

The generic agent/workflow engine — **domain-neutral**: the plan executor
(`charly pipeline run <entity>`), the bare agent runtime (the `agent` stage +
standalone `charly pipeline agent`), deterministic probe verbs (`verb:pipeline`),
the `kind: pipeline` entity, and SDD CUE-first schemas (`task cue:gen` →
generated params). Any pipeline is a declared plan; an eval is one plan, any
other agent workflow is another.

- **`charly pipeline run <entity>`** — execute a `kind: pipeline` entity from
  charly.yml: stages (`agent`/`check`/`probe`/`generate`/`media`/`gate`/`command`),
  a per-run ledger, bounded redo with the loop guard, FAIL-HARD, and the
  reference grammar (`$pr` / `$calver` / `$workdir` / `$env.NAME` / `@stage.output`).
- **`charly pipeline agent`** — the P1 runtime: direct chat-completions with a
  fully configurable system prompt, skills appended from the candies (@github
  refs), tools by reference.
- **`verb:pipeline`** — deterministic probes: media_gate, lock_audit, sequencing,
  head_freshness, config_audit, golden_present, lanes_ok, resolve_channel.
- **SDD** — `schema/pipeline.cue` is the single source; `task cue:gen` emits
  `params/cue_types_gen.go` (committed, CI-reproducible); every authored input is
  validated at load.

## Two workflows, one engine — who is who

- **The omarchy PR-eval lane** is ONE plan on this engine:
  `opencharly/eval-charly` (`eval-lane-plan`) evaluates `omacom/omarchy` PRs on
  golden VMs (known-red probe, oracle behavior checks, media evidence; verdicts
  **PASS | FAIL | NO_VALIDATION**).
- **The opencharly org PR validator** is a SEPARATE workflow with a SEPARATE
  plugin — `opencharly/plugin-review` (`charly review`, Verdict **PASS|BLOCK**,
  `opencharly/action-review` config, `opencharly/.github` workflow) validates
  opencharly ORG repo PRs against the rulebook. It does NOT run on this engine.

Keep the two apart: this repo has no `review` verb and no auto-merge; the org
validator has no `pipeline` verb and no eval lane.
