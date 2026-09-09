// pipeline.cue — the generic agent/workflow engine's own schema (SDD single source).
// Self-contained: no package clause, no base references — compiles standalone and
// splices onto the host base. cue:gen (wrapped with package params + @go(params))
// emits params/cue_types_gen.go; the provider serves this over Describe so every
// authored kind:pipeline entity body and probe input is validated at load.

// The kind:pipeline ENTITY body (a declared plan).
#PipelineInput: {
	version?: int & >0
	gates?: [...string]
	redo?: { max?: int, escalate_after?: int }
	concurrency?: { lanes?: int & >0 }
	channels?: { [string]: { golden: string, provision: string } }
	media?: #MediaSpec
	report?: #ReportSpec
	stages: [#Stage, ...#Stage]
}
#Stage: #AgentStage | #ProbeStage | #AdeStage | #GenerateStage | #MediaStage | #GateStage | #CommandStage
#AgentStage:   { kind: "agent",    id: string, prompt: string, skill?: [...string], tools?: [...string], outputs?: [...string], max_turns?: int & >0, redo?: #RedoSpec, skip_when?: string }
#ProbeStage:   { kind: "probe",    id: string, verbs: [string, ...string], input?: {[string]: _}, outputs?: [...string], redo?: #RedoSpec, skip_when?: string }
// #CheckStage is REMOVED: the custom bed-runner stage is gone. The org-wide
// evaluation is the ADE surface (#AdeStage): the bed's plan carries the oracle's
// agent-check: steps, graded by the live agent in the venue via the SDK.
#AdeStage:     { kind: "ade",     id: string, bed: string, redo?: #RedoSpec, skip_when?: string }
#GenerateStage: { kind: "generate", id: string, template: string, vars?: {[string]: _}, out: string, validate?: string, skip_when?: string }
#MediaStage:   { kind: "media",    id: string, assemble: bool, transcode?: string, skip_when?: string }
#GateStage:    { kind: "gate",     id: string, condition: string }
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
