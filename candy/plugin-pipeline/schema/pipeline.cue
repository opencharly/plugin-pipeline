// pipeline.cue — the generic agent/workflow engine's own schema (SDD single source).
// Self-contained: no package clause, no base references — compiles standalone and
// splices onto the host base. cue:gen (wrapped with package params + @go(params))
// emits params/cue_types_gen.go; the provider serves this over Describe so every
// authored kind:pipeline entity body and probe input is validated at load.

// The kind:pipeline ENTITY body (a declared plan).
#PipelineInput: {
	version?: int & >0
	repo?:   string // the eval TARGET repo (the pr tools' gh target) — authored on the entity; the env is the CLI fallback only
	gates?: [...string]
	redo?: { max?: int, escalate_after?: int }
	concurrency?: { lanes?: int & >0 }
	channels?: { [string]: { golden: string, provision: string } }
	llm?: #LLMSpec
	media?: #MediaSpec
	report?: #ReportSpec
	// skills: the agent-stage skill corpus. corpus is a dir holding
	// <skill-name>/SKILL.md; it is REF-RESOLVED ($env.NAME / $workdir / ...)
	// and a relative result is joined with the run workdir, so a lane can point
	// at a generated corpus outside its own tree (e.g.
	// $env.EVAL_UMBRELLA/marketplace/distros/skills). Every stage skill: ref
	// names a skill in this corpus. An unresolvable ref FAILS the stage
	// informatively — the decorative-ref era is gone.
	skills?: { corpus: string }
	stages: [#Stage, ...#Stage]
}
#Stage: #AgentStage | #ProbeStage | #AdeStage | #GenerateStage | #EmitStage | #MediaStage | #GateStage | #CommandStage
// #AgentStage: TYPED outputs (the untyped [...string] form is REMOVED — hard
// cutover). Each declared output is a field name -> #OutputType; the runner
// renders the contract into the prompt mechanically and validates the reply
// against it at decode. skill: refs are SKILL NAMES in the entity's
// skills.corpus.
//
// `cache` makes the agent's OUTPUT a COMMITTED ARTIFACT keyed by freshness: on a
// hit (the file exists and its key_field equals `key`) the declared outputs are
// READ from the file and the agent never runs; on a miss the agent runs normally.
// That is the render-once-per-<key> primitive a lane needs to reuse a committed
// plan (e.g. per pr@sha) instead of re-authoring it every run.
#AgentStage:   { kind: "agent",    id: string, prompt: string, skill?: [...string], tools?: [...string], outputs?: { [string]: #OutputType }, max_turns?: int & >0, redo?: #RedoSpec, skip_when?: string, cache?: #CacheSpec }
#CacheSpec: {
	path:       string  // ref-resolved path to the committed plan artifact (YAML)
	key:        string  // ref-resolved freshness value (e.g. $env.PR_HEAD_SHA)
	key_field?: string  // the file field compared to key (default "head")
	source?:    string  // the sub-tree whose fields ARE the outputs (default: top level)
}
#OutputType: {
	type: "string" | "int" | "bool" | "enum" | "string_list" | "object"
	enum?: [...string]      // type: "enum" — the allowed values
	description?: string    // rendered into the prompt contract
}
#ProbeStage:   { kind: "probe",    id: string, verbs: [string, ...string], input?: {[string]: _}, outputs?: [...string], redo?: #RedoSpec, skip_when?: string }
// #ProbeStage outputs: the probe's VALUE spreads as named outputs when it is a
// map (e.g. ledger_gate -> executed_checks/control_ok/media_ok/eval_steps/
// control_steps); a scalar value is exposed under the output named in `outputs`.
// #CheckStage is REMOVED: the custom bed-runner stage is gone. The org-wide
// evaluation is the ADE surface (#AdeStage): the bed's plan carries the oracle's
// agent-check: steps, graded by the live agent in the venue via the SDK.
#AdeStage:     { kind: "ade",     id: string, bed: string, fail_on?: [...string], redo?: #RedoSpec, skip_when?: string }
// #GenerateStage: render an inline template to `out`. `negate_checks` negates
// EVERY `checks` marker in the template. A per-marker transform
// (`${checks:negate}`, `${checks:json}`, `${var:yaml}`, `${var:indent}`,
// `${var:bullets}`) applies to that marker ALONE, so ONE template can render BOTH
// the treatment bed and its negative-control twin (plus a structured record with
// an indented multi-line report and bullet lists) into a single file. An unknown
// transform is a HARD error — never a silent no-op.
#GenerateStage: { kind: "generate", id: string, template: string, vars?: {[string]: _}, out: string, validate?: string, negate_checks?: bool, skip_when?: string }
// #EmitStage: the SCHEMA-FIRST artifact writer — the replacement for hand-written
// YAML/JSON. Instead of rendering a free-form string template (which can emit
// invalid YAML, as a `what: text: with-colon` scalar did before this existed),
// `value` is assembled as a STRUCTURED value (the SAME ref grammar as `vars`),
// validated against the authored CUE `schema` def BEFORE any bytes hit disk, and
// only then marshalled to `out` (YAML by default). The record an agent or a lane
// produces therefore CANNOT be malformed: a value that violates the schema fails
// the stage informatively, exactly like an ingress kind body.
//
//   schema: one of THREE forms, each unified against `value` with Concreteness
//           required (an unknown or wrong-typed field is a stage failure, never a
//           silent drop):
//             1. a bare def NAME (`#StageFindings`) resolving in the plugin's own
//                served schema (schema/pipeline.cue);
//             2. a `.cue` FILE path (`candy/eval-pr/record.cue`, project-relative
//                to the run workdir) — the strongest form, a committed and
//                reviewable schema the project owns; a `#Def` suffix
//                (`record.cue#NotTestableRecord`) selects a def explicitly, a
//                bare path uses the file's first `#Def`;
//             3. a literal CUE source string (a self-contained def block).
//   value:  a structured map assembled from refs (@stage.output, $pr, $env.NAME).
//           Every leaf resolves through the ref grammar; nested maps/lists are
//           resolved recursively (resolveValue), so `@oracle.checks` lands as a
//           real list of objects, not a stringified one.
//   format: "yaml" (default) | "json".
//
//   vars:   OPTIONAL named values for string-leaf TEMPLATES. A string leaf that
//           contains `${name}` markers is rendered with the SAME per-marker
//           grammar as `generate` (`${name:indent}` keeps a multi-line prose
//           block; `${name:bullets}` renders a markdown list; a bare `${name}`
//           scalar-resolves) — but the RESULT is a string leaf of the structured
//           value, so the CUE encoder owns the YAML quoting/block-scalar. This is
//           what lets a prose field (the user-voice report) be composed WITHOUT a
//           hand-written YAML template.
#EmitStage: { kind: "emit", id: string, schema: string, value: {[string]: _}, vars?: {[string]: _}, out: string, format?: "yaml" | "json", validate?: string, skip_when?: string }
#MediaStage:   { kind: "media",    id: string, assemble: bool, transcode?: string, skip_when?: string }
#GateStage:    { kind: "gate",     id: string, condition: string, skip_when?: string }
#CommandStage: { kind: "command",  id: string, command: string, expect_exit?: int }   // EXTERNAL processes ONLY
#RedoSpec: { on_fail?: [...string] | string, triggers?: {[string]: string}, max?: int & >0, escalate_after?: int & >0 }

#MediaSpec: { files: [string, ...string], min: {[string]: int}, dir: string }
#ReportSpec: { template: string, frontmatter_schema?: string, bed_template?: string, control_bed_template?: string }

// #StageFinding — one row of the per-run ledger dump (stage-findings.yml). The
// dump is emitted schema-first (marshalled + validated), so it is always valid
// YAML — a debug artifact no reader can parse is worthless.
#StageFinding: close({
	stage!:   string
	kind!:    string
	status!:  string
	trigger?: string
	message?: string
	outputs?: {...}
})
#StageFindings: [...#StageFinding]

// Probe verb inputs (deterministic, engine-native).
#MediaGateInput:      { dir: string, files: [string], min: {[string]: int} }
#LockAuditInput:      { trees: [string] }
#SequencingInput:     { lanes: int, golden: string }
#HeadFreshnessInput:  { plan_sha: string, pr: int, repo: string }
#ConfigAuditInput:    { bed: string, pr: int }
#ResolveChannelInput: { channel: string, channels: {[string]: { golden: string, provision: string } } }
#EvidenceAuditInput:  { dir: string, files: [string], min: {[string]: int}, trees: [string] }

// The P1 agent runtime input (the standalone + stage op).
#AgentRunInput: { system_prompt: string, prompt: string, skill?: [...string], tools?: [...string] }

// The LLM endpoint config (authored on the pipeline entity). Resolution:
// env overrides > the entity llm block > the built-in default (the local
// ollama server). An empty api_key means ABSENT: the client sends NO auth
// header (the local ollama needs none).
#LLMSpec: { base_url?: string, model?: string, api_key?: string }
