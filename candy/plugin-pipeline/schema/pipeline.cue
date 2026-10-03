// pipeline.cue — the generic agent/workflow engine's own schema (SDD single source).
// Self-contained: no package clause, no base references — compiles standalone and
// splices onto the host base. cue:gen (wrapped with package params + @go(params))
// emits params/cue_types_gen.go; the provider serves this over Describe so every
// authored kind:pipeline entity body and probe input is validated at load.
//
// The kind:pipeline ENTITY body itself is spec.Pipeline (`steps:`), declared in
// spec/schema/pipeline.cue and validated by sdk/workflowkit.ValidatePipeline. This
// file no longer declares it: the front-end lowers the authored entity with
// sdk/workflowkit.Lower and dispatches it to the `workflow` provider class.

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
	response_format?: #LLMResponseFormat
	// reasoning_effort: thinking control for reasoning models ("none" disables
	// thinking where the server honours it).
	reasoning_effort?: "high" | "medium" | "low" | "none" @go(Reasoning_effort,type=string)
	// reasoning: the object form of the same control (ollama accepts either).
	reasoning?: #LLMReasoning
	// stream_options: streaming response options.
	stream_options?: #LLMStreamOptions
	// parallel_tool_calls: permit the model to emit several tool calls per turn.
	parallel_tool_calls?: bool @go(Parallel_tool_calls,optional=nillable)
	// tool_choice: "none" | "auto" | "required" | {function: {name}}.
	tool_choice?: "none" | "auto" | "required" | #LLMNamedToolChoice
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

// #LLMResponseFormat — the structured-output contract. type "json_schema" requires
// the json_schema block; the schema field is the JSON Schema itself.
#LLMResponseFormat: close({
	type: "text" | "json_object" | "json_schema" @go(Type,type=string)
	json_schema?: close({
		name:         string
		description?: string
		schema:       {[string]: _}
		strict?:      bool
	})
})

// #LLMReasoning — the object form of the reasoning/thinking control.
#LLMReasoning: close({
	effort?: "high" | "medium" | "low" | "none" @go(Effort,type=string)
})

// #LLMStreamOptions — streaming response options.
#LLMStreamOptions: close({
	include_usage?: bool @go(Include_usage,optional=nillable)
})

// #LLMNamedToolChoice — force one named function tool.
#LLMNamedToolChoice: close({
	function: close({name: string})
})

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
// Every referenced def (#OutputType, #LLMSpec, #RedoSpec, #CacheSpec,
// #MediaSpec, #ReportSpec) is declared in THIS file; none is re-declared here.
#PipelineAgentInput: {
	prompt:     string                  @go(Prompt)
	skill?:     [...string]             @go(Skill)
	tools?:     [...string]             @go(Tools)
	outputs?:   {[string]: #OutputType} @go(Outputs)
	max_turns?: int & >0                @go(Max_turns)
	llm?:       #LLMSpec                @go(Llm)
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
