---
name: pipeline
description: 'The `charly pipeline` workflow front-end — resolve/validate/lower a
  declared

  kind:pipeline entity and dispatch it to the workflow engine, the bare agent

  runtime, deterministic probe verbs, template rendering.'
---

# charly pipeline — the workflow front-end

`charly pipeline` is the domain-neutral workflow front-end. Served by
`candy/plugin-pipeline` (`command:pipeline`, out-of-process over gRPC).

## What it provides

- the bare agent runtime (the standalone `charly pipeline agent`);
- the run leg — `charly pipeline run <entity>`: resolve the authored
  `kind: pipeline` entity, validate it with `sdk/workflowkit.ValidatePipeline`,
  lower it with `sdk/workflowkit.Lower` to `workflow.lobster` + a generated
  `charly.yml`, and dispatch the pair to the `workflow` provider class as
  `workflow-run` (the workflow ENGINE owns execution, control flow, redo, and
  iteration);
- deterministic probe verbs;
- template/frontmatter rendering;
- the seven stage kinds as ordinary reachable verbs
  (`verb:agent/probe/ade/generate/emit/media/gate`);
- skills by `@github` candy refs.

Every workflow is a DECLARED pipeline (`kind:pipeline` entity: `spec.Pipeline`
with `steps:`), so the engine is config-driven rather than code-driven.

## When to use it

To run or author a `kind:pipeline` entity, or to build an agent workflow that joins
the runtime through configuration (e.g. the `charly review --plan` executor).

## Cross-References

- `/charly-internals:plugin` — the plugin/provider model.
- `/charly-internals:go` — `kind:` schema authoring (SDD).
