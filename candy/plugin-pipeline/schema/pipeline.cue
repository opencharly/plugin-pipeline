// pipeline.cue — the generic agent/workflow engine's own schema (SDD single source).
// It declares the plugin's OWN authoring shapes — the `kind: pipeline` ENTITY body
// (`#Pipeline`, at the end of this file), its step/trigger/flow helpers, and the
// per-verb input defs — and IMPORTS the contract module for the shapes it does not
// own. It carries no package clause — spec/schemacompose composes it as ONE CUE
// module instance with the package re-established — and `charly candy params` emits
// params/cue_types_gen.go from that same instance. The provider serves this over
// Describe so every authored kind:pipeline entity body and verb input is validated
// at load.
//
// THE KIND BODY LIVES HERE, not in the contract module: a `kind` body belongs with
// the plugin that serves the kind. The `kind` capability declares the gate def
// (`plugin.go`: `{Class: "kind", Word: "pipeline", InputDef: "#PipelineValue"}`), so
// the host resolves `#PipelineValue` from that DECLARATION and looks it up in the
// schema composed from the base and every served plugin unit — no kernel change and
// no def-name knowledge in the host. This repo's `workflowkit` validates and lowers
// the authored entity; the pair is dispatched to the `workflow` provider class. The
// contract module keeps exactly ONE def for this kind: #PipelineClassRoutes, the
// class→route table over its own #ProviderClassNames, which only its own vocabulary
// generator can extract.
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

// ── The authored `kind: pipeline` body ──────────────────────────────────────
//
// The ENTITY body, moved into the plugin that owns the kind (the kernel/plugin
// boundary law). These defs were the contract module's; the only thing left behind
// is #PipelineClassRoutes, which is a table over that module's own provider-class
// vocabulary and belongs to its vocabulary generator.
//
// EXTERNAL REFERENCES are the contract module's, `schema.`-prefixed: #StrVal, #Step,
// #TaskParamSpec and #WorkflowArgVal stay contract defs because `kind: task`, a candy
// plan and the workflow wire share them — a pipeline body and the plan it lowers to
// must not be able to disagree about what a step or an arg value is.
//
// THE STAGE BODY IS THE CONTRACT'S (P26 unit 2, opencharly/plugin-pipeline#66).
// `stage:` is a MAP keyed by stage name — DESIGN.cue:890 writes
// `stage!: {[#Name]: #Stage}` — and each value is the contract's #Stage
// (`schema/stage.cue`, imported), so `after:`/`when:`/`lane:`/`concurrency:`/`retry:`/
// `timeout:`/`on_error:` and the four arms are the CONTRACT's shapes, never restated
// here (R2: one definition of the stage vocabulary, and it is the contract's).
//
// "EXACTLY ONE ARM" IS A GO RULE (`workflowkit.ValidatePipeline`). The contract
// records why it cannot be a CUE rule: both the arm spread and `or()` over the arms
// fail that module's composition, so the arms are four OPTIONAL payload fields and
// the exclusivity is validated, exactly as it was before this unit for the lobster
// arms.
//
// THERE IS NO LONGER A LOBSTER-STEP GRAMMAR HERE, and no `steps:` (hard cutover):
// `steps:`, #PipelineStepBase, #PipelineStep, #PipelineArms, #PipelineFlow,
// #PipelineSubStep, #PipelineParallel, #PipelineInput, #PipelineRetry and
// #PipelineApproval are DELETED. The authored surface is the STAGE; the lobster IR
// is what the LOWERING emits (steps/parallel/lanes are engine words now, never
// authored ones). #Retry, #Lane, #StageWhen, #StageAsk and #Answer are the contract's.
//
// A STAGE'S `id` IS ITS KEY. D-KIND-3 makes a node's key its identity, so the map key
// names the stage (`after:` refers to it) and the lowering uses it as the lobster
// step id.

// #PipelineSchedule — the cron trigger. A 5-field cron, the SAME grammar
// deploy.schedule, a k8s CronJob and GitHub Actions `on.schedule` use, so one
// authored schedule lowers to every consumer; the lobster engine converts it to a
// systemd `OnCalendar` (validated with `systemd-analyze calendar`).
//
// `args` is the scheduled run's RUNTIME arg values — the same field, and therefore
// the same `schema.#WorkflowArgVal` admission, as `#WorkflowRunRequest.args`. It is
// normalized into `#WorkflowSchedule.args` verbatim, so a lane that works on a
// manual run works on a scheduled one; a scalar-only schedule would be the silent
// half-fix that this def and its engine twin are one mechanism to prevent.
#PipelineSchedule: {
	cron!:       string & !=""
	timezone?:   string & !=""
	args?:       {[string]: schema.#WorkflowArgVal} @go(Args,type=map[string]any)
	persistent?: bool
}

// #PipelineTrigger — how a pipeline starts. Both fields optional; a trigger setting
// NEITHER is a load error raised in Go (a closed CUE struct cannot express
// "exactly one arm" without a disjunction — see the file header).
#PipelineTrigger: {
	manual?:   bool
	schedule?: #PipelineSchedule
}

// #Pipeline — the authored `kind: pipeline` entity body.
//
// `entities:` holds inline entities of ANY kind plugin (a task, a candy, a deploy,
// another pipeline, …). It is typed as an opaque map because #Node is EXCLUDED from
// param-gen (the plugin SDK contract forbids base refs in a self-contained schema);
// each inline entity is validated by its OWN kind's value def and OpValidate once
// the generated charly.yml is loaded.
#Pipeline: {
	description!: string & !=""
	// engine — the workflow engine word, resolved against the `workflow` PROVIDER
	// CLASS and dispatched as InvokeProvider("workflow", <engine>, workflow-run|…)
	// through the normal path. Default "lobster". (Whether the pipeline kind may keep
	// a provider word at all is opencharly/plugin-pipeline#67's decision, not this
	// schema's.)
	engine?:     string & !=""
	args?:       {[string]: schema.#TaskParamSpec}
	env?:        {PATH?: _|_, [string]: schema.#StrVal} @go(Env,type=map[string]string)
	cwd?:        string & !=""
	cost_limit?: number & >=0 @go(CostLimit)
	triggers?:   [...#PipelineTrigger]
	// config — the former pipeline knobs (llm/media/report/skills/channels/gates/
	// repo/redo/agent), carried opaquely so a knob the engine does not know is not a
	// schema change.
	config?:   {[string]: _} @go(Config,type=map[string]any)
	entities?: {[string]: {...}} @go(Entities,type=map[string]map[string]any)
	// stage — the pipeline's stages, keyed by NAME. The map is unordered by
	// construction, so the ONE order is the graph `after:` declares: the lowering
	// orders the stages topologically (ties broken by name, so an emission is
	// reproducible) and a cycle is a load error naming the stages in it.
	stage!: {[string]: schema.#Stage}
}

// #PipelineValue — the HOST-SIDE value gate def for the `pipeline` kind (the
// #TaskValue precedent). @go(-): the Go type comes from #Pipeline, so this def is
// validation-only. Its NAME is the contract the `kind` capability declares
// (plugin.go's InputDef), which is how the HOST resolves the gate for this kind
// without carrying any knowledge of it.
#PipelineValue: #Pipeline @go(-)
