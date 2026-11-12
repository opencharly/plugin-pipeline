// pipeline.cue — the generic agent/workflow engine's own schema (SDD single source).
// It declares the plugin's OWN authoring shapes (the per-verb input defs and the
// shared pipeline helpers) and IMPORTS the contract module for the shapes it does
// not own. It carries no package clause — spec/schemacompose composes it as ONE
// CUE module instance with the package re-established — and `charly candy params`
// emits params/cue_types_gen.go from that same instance. The provider serves this
// over Describe so every authored kind:pipeline entity body and verb input is
// validated at load.
//
// The kind:pipeline ENTITY body itself is spec.Pipeline (`steps:`), declared in
// spec/schema/pipeline.cue and validated by sdk/workflowkit.ValidatePipeline. This
// file does not declare it: the front-end lowers the authored entity with
// sdk/workflowkit.Lower and dispatches it to the `workflow` provider class.
//
// ONE NAME, ONE HOME. The six LLM definitions this file used to restate —
// #LLMSpec, #LLMParams, #LLMResponseFormat, #LLMReasoning, #LLMStreamOptions and
// #LLMNamedToolChoice — are the CONTRACT module's
// (`github.com/opencharly/spec/schema`). They are the vocabulary BOTH this engine's
// agent stages and the `vision:` check verb author against, and they generate the
// Go types (`spec.LLMSpec` / `spec.LLMParams`) the shared SDK `llmkit` client
// consumes. This schema IMPORTS them; the structurally identical local copies are
// DELETED, and the parity test that existed only to keep the copies honest retires
// with them (R2 — one name, one home).
//
// THE IMPORT RESOLVES OFFLINE. It is the shape spec/schemacompose exists for: the
// contract module is served from the spec Go module the binary already links — no
// network, no module cache, no `cue.mod`, no published CUE module — on the serve
// side (BuildCapabilities → ServedSchema) AND on the host side, which composes the
// SAME body under the DERIVED module declaration (the wire carries no module
// declaration, so both sides evaluate under one they can derive).

import "github.com/opencharly/spec/schema"

// #OutputType: the typed-output contract an agent stage declares per output field.
#OutputType: {
	type: "string" | "int" | "bool" | "enum" | "string_list" | "object"
	enum?: [...string]      // type: "enum" — the allowed values
	description?: string    // rendered into the prompt contract
}

// #RedoSpec: the per-stage redo/retry policy.
#RedoSpec: { on_fail?: [...string] | string, triggers?: {[string]: string}, max?: int & >0, escalate_after?: int & >0 }

// #CacheSpec: the committed-artifact cache for an agent's output, keyed by freshness.
#CacheSpec: {
	path:       string  // ref-resolved path to the committed plan artifact (YAML)
	key:        string  // ref-resolved freshness value (e.g. $env.PR_HEAD_SHA)
	key_field?: string  // the file field compared to key (default "head")
	source?:    string  // the sub-tree whose fields ARE the outputs (default: top level)
}

// #MediaSpec: the pipeline-level media gate config.
#MediaSpec: { files: [string, ...string], min: {[string]: int}, dir: string }

// #ReportSpec: the report-rendering config a generate/emit stage carries.
#ReportSpec: { template: string, frontmatter_schema?: string, bed_template?: string, control_bed_template?: string }

// Probe verb inputs (deterministic, engine-native).
#MediaGateInput:      { dir: string, files: [string], min: {[string]: int} }
#LockAuditInput:      { trees: [string] }
#SequencingInput:     { bed_prefix: string, golden: string }
#HeadFreshnessInput:  { plan_sha: string, pr: int, repo: string }
#ConfigAuditInput:    { bed: string, pr: int }
#ResolveChannelInput: { channel: string, channels: {[string]: { golden: string, provision: string } } }
#EvidenceAuditInput:  { dir: string, files: [string], min: {[string]: int}, trees: [string] }

// The P1 agent runtime input (the standalone + stage op).
#AgentRunInput: { system_prompt: string, prompt: string, skill?: [...string], tools?: [...string] }

// ── The LLM surface — the CONTRACT's definitions, imported ──────────────────
//
// The six definitions this file used to restate are declared once, in
// `github.com/opencharly/spec/schema`: #LLMSpec (the endpoint + connection config)
// and #LLMParams (the general request-parameter block), plus the four companions
// #LLMResponseFormat / #LLMReasoning / #LLMStreamOptions / #LLMNamedToolChoice.
// Both are CLOSED there: an unknown or wrongly-typed field is a LOAD error, never
// a silent drop (the `extra` escape hatch inside #LLMParams exists for genuinely
// undocumented keys, and is the only place an arbitrary key is legal).
//
// api_key is REF-RESOLVED like every other authored string, so the correct
// authoring is a reference — `api_key: $env.OPENAI_API_KEY` (or a secret ref) —
// never a literal key committed to the repo.
//
// The engine's resolution precedence (env override > stage llm block > entity llm
// block > built-in default), the built-in local-ollama default, and the
// field-by-field merge are IMPLEMENTED in this plugin's `resolveLLM` and the
// shared SDK client (`sdk/llmkit`), which own that behaviour; the shapes they read
// are the imported definitions above.

// ── Per-verb input shapes (the lifted-verb provider contract) ───────────────
//
// These are the per-verb input shapes for the six stage verbs (agent, probe,
// ade, generate, emit, media) plus the deterministic gate. They are the input
// contract of the ordinary `verb:` providers the stage bodies were lifted into,
// and the migrator's frozen key spellings have a SINGLE source to agree on here.
// #PipelineMediaInput.assemble is retained as a REQUIRED field even though no Go
// reader consults it: the retired grammar declared it, and a field the old
// grammar accepted is never silently dropped.
//
// WHY EVERY DEF HERE CARRIES THE `#Pipeline` PREFIX. The host splices EVERY
// loaded plugin's served schema into ONE CUE instance (the loader appends each
// served schema to the same value), and CUE UNIFIES two same-named defs instead
// of erroring — so a plugin's def names share ONE GLOBAL namespace with every
// OTHER plugin's. The load gate's splice detects a collision with the BASE only;
// it does NOT detect plugin-vs-plugin. An un-prefixed `#AgentInput` here was
// exactly that collision, and a silent one: plugin-agent is a RELEASED plugin
// declaring `{Class: "kind", Word: "agent", InputDef: "#AgentInput"}` with an
// incompatible shape (`command: [string, ...string]` + `prompt_via`), so the two
// would have unified and rejected EVERY `agent: {prompt: …}` step with no error
// at this seam. The prefix makes this plugin's seven defs collision-free by
// construction. The loader-side class fix (namespace, or reject a plugin-vs-plugin
// collision loudly) is tracked as opencharly/charly#770; until it lands, a NEW def
// added here takes the same prefix.
//
// Every referenced def is declared in THIS file (#OutputType, #RedoSpec,
// #CacheSpec, #MediaSpec, #ReportSpec) except #LLMSpec, which is IMPORTED from the
// contract module; none is re-declared here.
#PipelineAgentInput: {
	prompt:     string                  @go(Prompt)
	skill?:     [...string]             @go(Skill)
	tools?:     [...string]             @go(Tools)
	outputs?:   {[string]: #OutputType} @go(Outputs)
	max_turns?: int & >0                @go(Max_turns)
	llm?:       schema.#LLMSpec         @go(Llm)
	redo?:      #RedoSpec               @go(Redo)
	cache?:     #CacheSpec              @go(Cache)
	repo?:      string                  @go(Repo)
	skills?:    {corpus: string}        @go(Skills)
}

#PipelineProbeInput: {
	verbs:    [string, ...string] @go(Verbs)
	input?:   {[string]: _}       @go(Input)
	outputs?: [...string]         @go(Outputs)
	redo?:    #RedoSpec           @go(Redo)
	media?:   #MediaSpec          @go(Media)
}

#PipelineAdeInput: {
	bed:      string      @go(Bed)
	fail_on?: [...string] @go(Fail_on)
	redo?:    #RedoSpec   @go(Redo)
}

#PipelineGenerateInput: {
	template:      string       @go(Template)
	vars?:         {[string]: _} @go(Vars)
	out:           string       @go(Out)
	validate?:     string       @go(Validate)
	negate_checks?: bool        @go(Negate_checks)
	report?:       #ReportSpec  @go(Report)
}

#PipelineEmitInput: {
	schema:   string           @go(Schema)
	value:    {[string]: _}    @go(Value)
	vars?:    {[string]: _}    @go(Vars)
	out:      string           @go(Out)
	format?:  "yaml" | "json"  @go(Format)
	validate?: string          @go(Validate)
	report?:  #ReportSpec      @go(Report)
}

// #PipelineMediaInput — dir/files are declared here because the Go reader consults them although the retired media stage did not declare them.
#PipelineMediaInput: {
	assemble:   bool        @go(Assemble)
	transcode?: string      @go(Transcode)
	dir?:       string      @go(Dir)
	files?:     [...string] @go(Files)
	media?:     #MediaSpec  @go(Media)
}

#PipelineGateInput: {
	condition: string @go(Condition)
}
