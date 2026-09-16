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
// `llm` is the STAGE-LOCAL override of the entity's llm block: it sits between
// the env override and the entity block in the field-wise precedence
// (env > stage > entity > built-in default), so one stage can retarget the
// model or tighten a sampling knob without disturbing its siblings. `llm.model`
// is the common case (a cheap model for a mechanical stage); `llm.params`
// overlays #LLMParams field-wise. A nil/absent block is a no-op.
#AgentStage:   { kind: "agent",    id: string, prompt: string, skill?: [...string], tools?: [...string], outputs?: { [string]: #OutputType }, max_turns?: int & >0, llm?: #StageLLMSpec, redo?: #RedoSpec, skip_when?: string, cache?: #CacheSpec }
// #StageLLMSpec — the per-stage llm override: the endpoint knobs a stage may
// retarget (model/base_url/api_key), plus a field-wise #LLMParams overlay.
// timeout/idle_timeout/max_retries/headers/organization/project are
// CONNECTION-level and intentionally NOT overridable per stage — one lane
// speaks to one endpoint with one liveness policy.
#StageLLMSpec: {
	model?:    string
	base_url?: string
	api_key?:  string
	params?:   #LLMParams
}
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
//           contains `${name}` markers is rendered with the SHARED MARKER SYNTAX
//           but emit's OWN transform set — NOT generate's full set:
//             ${name}           scalar default (the value itself)
//             ${name:indent}    the value as-is (a multi-line prose block)
//             ${name:bullets}   a markdown bullet list (one line per element)
//             ${name:yaml}      the value rendered inline as JSON
//             ${name:json}      same as :yaml for a string leaf
//           `negate` is DELIBERATELY REJECTED (a string leaf has no check to
//           negate) and hard-errors like any other unknown transform. The RESULT
//           is a string leaf of the structured value, so the CUE encoder owns the
//           YAML quoting/block-scalar — which is what lets a prose field (the
//           user-voice report) be composed WITHOUT a hand-written YAML template.
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

// ── The LLM surface (the OpenAI-compatible API) ─────────────────────────────
//
// #LLMSpec is the endpoint + connection config; #LLMParams is the GENERAL
// request-parameter block applied to every completion the engine issues. Both
// are CLOSED: an unknown or wrongly-typed field is a LOAD error, never a
// silent drop (the `extra` escape hatch exists for genuinely undocumented
// keys, and is the only place an arbitrary key is legal).
//
// Resolution precedence, applied FIELD-WISE (a lower layer fills only what the
// higher layers left unset):
//
//	env override > stage llm block > entity llm block > built-in default
//
// The built-in default is the LOCAL ollama server (http://localhost:11434/v1,
// deepseek-v4.1-flash:cloud) so a lane needs no authored block to run. An
// empty RESOLVED api_key means ABSENT: the client sends NO Authorization
// header at all (the local ollama needs none) — a missing secret can never
// zero out another layer.
//
// api_key is REF-RESOLVED like every other authored string, so the correct
// authoring is a reference — `api_key: $env.OPENAI_API_KEY` (or a secret ref) —
// never a literal key committed to the repo.
//
// Every scalar the engine would otherwise hardcode is authorable here: the
// sampling knobs, the token bound, the reasoning control, the structured-output
// contract, the retry/idle policy, and the request metadata.
#LLMSpec: close({
	// base_url: the OpenAI-compatible endpoint root INCLUDING the /v1 suffix
	// (e.g. http://localhost:11434/v1). The engine appends /chat/completions.
	base_url?: string
	// model: the model identifier sent in the request (e.g. deepseek-v4.1-flash:cloud).
	model?: string
	// api_key: bearer credential; empty/absent => NO auth header is sent.
	api_key?: string
	// organization / project: sent as the OpenAI-Organization / OpenAI-Project
	// headers for a multi-org key.
	organization?: string
	project?:      string
	// timeout: a Go duration bounding the WHOLE request (e.g. "10m"). Empty
	// means no whole-request deadline — the idle_timeout is the bound instead.
	timeout?: string
	// idle_timeout: a Go duration bounding the gap BETWEEN streaming chunks.
	// This is the primary liveness bound: a slow-but-progressing generation is
	// never cut off, while a silent provider fails in bounded time.
	idle_timeout?: string
	// max_retries: automatic retries on a retryable HTTP status. Defaults to 2.
	max_retries?: int & >=0 @go(Max_retries,optional=nillable)
	// headers: extra request headers (e.g. an OpenRouter HTTP-Referer/X-Title).
	headers?: {[string]: string}
	// params: the general request parameters (see #LLMParams).
	params?: #LLMParams
})

// #LLMParams is the general OpenAI chat-completions request parameter block.
// Field names match the wire API exactly. All fields are OPTIONAL: an omitted
// field is not sent at all (the server's own default applies), so the engine
// never injects a value the author did not ask for.
#LLMParams: close({
	// temperature: sampling temperature (0..2).
	temperature?: number & >=0 & <=2 @go(Temperature,type=*float64)
	// top_p: nucleus sampling probability mass (0..1).
	top_p?: number & >=0 & <=1 @go(Top_p,type=*float64)
	// max_tokens: the completion token bound (ollama: num_predict).
	max_tokens?: int & >0 @go(Max_tokens,optional=nillable)
	// max_completion_tokens: the newer alias of max_tokens.
	max_completion_tokens?: int & >0 @go(Max_completion_tokens,optional=nillable)
	// frequency_penalty / presence_penalty: repetition controls (-2..2).
	frequency_penalty?: number & >=-2 & <=2 @go(Frequency_penalty,type=*float64)
	presence_penalty?:  number & >=-2 & <=2 @go(Presence_penalty,type=*float64)
	// seed: requests a reproducible generation where the server supports it.
	seed?: int @go(Seed,optional=nillable)
	// stop: one stop sequence, or a list of them.
	stop?: string | [...string]
	// response_format: the structured-output contract (text | json_object |
	// json_schema).
	response_format?: #ResponseFormat
	// reasoning_effort: thinking control for reasoning models ("none" disables
	// thinking where the server honours it).
	reasoning_effort?: "high" | "medium" | "low" | "none" @go(Reasoning_effort,type=string)
	// reasoning: the object form of the same control (ollama accepts either).
	reasoning?: #Reasoning
	// stream_options: streaming response options.
	stream_options?: #StreamOptions
	// parallel_tool_calls: permit the model to emit several tool calls per turn.
	parallel_tool_calls?: bool @go(Parallel_tool_calls,optional=nillable)
	// tool_choice: "none" | "auto" | "required" | {function: {name}}.
	tool_choice?: "none" | "auto" | "required" | #NamedToolChoice
	// logprobs / top_logprobs: token log-probability reporting (unsupported by
	// the local ollama OpenAI layer; authorable for a full OpenAI endpoint).
	logprobs?:     bool @go(Logprobs,optional=nillable)
	top_logprobs?: int  @go(Top_logprobs,optional=nillable)
	// user: an end-user identifier for abuse monitoring.
	user?: string
	// metadata: arbitrary string metadata attached to the request.
	metadata?: {[string]: string}
	// logit_bias: per-token-id bias map.
	logit_bias?: {[string]: int}
	// extra: undocumented request fields, merged into the request body verbatim
	// as dotted JSON paths (sjson). The ONE legal place for an unknown key.
	extra?: {[string]: _}
})

// #ResponseFormat — the structured-output contract. type "json_schema" requires
// the json_schema block; the schema field is the JSON Schema itself.
#ResponseFormat: close({
	type: "text" | "json_object" | "json_schema" @go(Type,type=string)
	json_schema?: close({
		name:         string
		description?: string
		schema:       {[string]: _}
		strict?:      bool
	})
})

// #Reasoning — the object form of the reasoning/thinking control.
#Reasoning: close({
	effort?: "high" | "medium" | "low" | "none" @go(Effort,type=string)
})

// #StreamOptions — streaming response options.
#StreamOptions: close({
	include_usage?: bool @go(Include_usage,optional=nillable)
})

// #NamedToolChoice — force one named function tool.
#NamedToolChoice: close({
	function: close({name: string})
})
