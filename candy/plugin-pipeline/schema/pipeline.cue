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
	// skills: the agent-stage skill corpus. corpus is a workdir-relative dir
	// holding <skill-name>/SKILL.md; every stage skill: ref names a skill in
	// this corpus. An unresolvable ref FAILS the stage informatively — the
	// decorative-ref era is gone.
	skills?: { corpus: string }
	stages: [#Stage, ...#Stage]
}
#Stage: #AgentStage | #ProbeStage | #AdeStage | #GenerateStage | #MediaStage | #GateStage | #CommandStage
// #AgentStage: TYPED outputs (the untyped [...string] form is REMOVED — hard
// cutover). Each declared output is a field name -> #OutputType; the runner
// renders the contract into the prompt mechanically and validates the reply
// against it at decode. skill: refs are SKILL NAMES in the entity's
// skills.corpus.
#AgentStage:   { kind: "agent",    id: string, prompt: string, skill?: [...string], tools?: [...string], outputs?: { [string]: #OutputType }, max_turns?: int & >0, redo?: #RedoSpec, skip_when?: string }
#OutputType: {
	type: "string" | "int" | "bool" | "enum" | "string_list" | "object"
	enum?: [...string]      // type: "enum" — the allowed values
	description?: string    // rendered into the prompt contract
}
#ProbeStage:   { kind: "probe",    id: string, verbs: [string, ...string], input?: {[string]: _}, outputs?: [...string], redo?: #RedoSpec, skip_when?: string }
// #CheckStage is REMOVED: the custom bed-runner stage is gone. The org-wide
// evaluation is the ADE surface (#AdeStage): the bed's plan carries the oracle's
// agent-check: steps, graded by the live agent in the venue via the SDK.
#AdeStage:     { kind: "ade",     id: string, bed: string, redo?: #RedoSpec, skip_when?: string }
#GenerateStage: { kind: "generate", id: string, template: string, vars?: {[string]: _}, out: string, validate?: string, skip_when?: string }
#MediaStage:   { kind: "media",    id: string, assemble: bool, transcode?: string, skip_when?: string }
#GateStage:    { kind: "gate",     id: string, condition: string, skip_when?: string }
#CommandStage: { kind: "command",  id: string, command: string, expect_exit?: int }   // EXTERNAL processes ONLY
#RedoSpec: { on_fail?: [...string] | string, triggers?: {[string]: string} }

#MediaSpec: { files: [string, ...string], min: {[string]: int}, dir: string }
#ReportSpec: { template: string, frontmatter_schema?: string, bed_template?: string }

// Probe verb inputs (deterministic, engine-native).
#MediaGateInput:      { dir: string, files: [string], min: {[string]: int} }
#LockAuditInput:      { trees: [string] }
#SequencingInput:     { lanes: int, golden: string }
#HeadFreshnessInput:  { plan_sha: string, pr: int, repo: string }
#ConfigAuditInput:    { bed: string, pr: int }
#ResolveChannelInput: { pr: int, channels: {[string]: { golden: string, provision: string } }, default: string }
#EvidenceAuditInput:  { dir: string, files: [string], min: {[string]: int}, trees: [string] }

// The P1 agent runtime input (the standalone + stage op).
#AgentRunInput: { system_prompt: string, prompt: string, skill?: [...string], tools?: [...string] }

// The LLM endpoint config (authored on the pipeline entity). Resolution:
// env overrides > the entity llm block > the built-in default (the local
// ollama server). An empty api_key means ABSENT: the client sends NO auth
// header (the local ollama needs none).
#LLMSpec: { base_url?: string, model?: string, api_key?: string }
