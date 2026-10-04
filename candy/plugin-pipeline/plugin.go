// Package pluginpipeline — the workflow FRONT-END (domain-neutral).
// Provides: kind:pipeline (a declared `steps:` workflow as a charly.yml entity,
// validated by sdk/workflowkit), command:pipeline (the CLI + the standalone agent
// runtime), verb:pipeline (deterministic probes), and the seven LIFTED WORKFLOW
// VERBS (verb:agent/probe/ade/generate/emit/media/gate, each reachable as a
// `<word>: <input>` step from any plan).
// SDD: the CUE schema (schema/pipeline.cue) is the single source; params are generated
// (params/cue_types_gen.go); every authored input is validated at load.
package pluginpipeline

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/workflowkit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"
)

//go:embed schema/*.cue
var schemaFS embed.FS

const calver = "2026.251.0000"

func NewProvider() pb.ProviderServer { return &provider{} }

func NewMeta() pb.PluginMetaServer {
	return sdk.NewMeta(calver, []sdk.ProvidedCapability{
		{Class: "command", Word: "pipeline"},
		{Class: "verb", Word: "pipeline"},
		{Class: "kind", Word: "pipeline", InputDef: "#PipelineValue"},
		// the LIFTED WORKFLOW VERBS: every stage kind (except the inline
		// `command`) is an ordinary verb, so `<word>: <input>` reaches it from ANY
		// plan, not only from a kind:pipeline entity's steps:. The InputDef is the
		// verb's frozen #Pipeline<Word>Input shape (see verb_input_defs_test.go);
		// no Primary — a lifted verb is declarative, it never claims a scalar sugar
		// spelling (D3).
		{Class: "verb", Word: "agent", InputDef: "#PipelineAgentInput"},
		{Class: "verb", Word: "probe", InputDef: "#PipelineProbeInput"},
		{Class: "verb", Word: "ade", InputDef: "#PipelineAdeInput"},
		{Class: "verb", Word: "generate", InputDef: "#PipelineGenerateInput"},
		{Class: "verb", Word: "emit", InputDef: "#PipelineEmitInput"},
		{Class: "verb", Word: "media", InputDef: "#PipelineMediaInput"},
		{Class: "verb", Word: "gate", InputDef: "#PipelineGateInput"},
	}, schemaFS)
}

type provider struct{ pb.UnimplementedProviderServer }

func (provider) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	switch {
	case req.GetOp() == sdk.OpRun:
		// OpRun carries BOTH surfaces, split by the reserved word (the same
		// shape plugin-task uses): the bare word is the command:pipeline CLI;
		// each LIFTED VERB word routes to its handler (verb_<word>.go) with a
		// standalone env. An unknown word is a NAMED error — never a fall-through
		// to the CLI, which would run the wrong thing on a typo.
		switch req.GetReserved() {
		case "", "pipeline":
			var in struct {
				Args []string `json:"args"`
			}
			if len(req.GetParamsJson()) > 0 {
				_ = json.Unmarshal(req.GetParamsJson(), &in)
			}
			ex, xerr := sdk.ExecutorForInvoke(ctx, req.GetExecutorBrokerId())
			if xerr != nil {
				return nil, xerr
			}
			code, err := runCLI(in.Args, ex)
			if err != nil {
				return nil, err
			}
			if code != 0 {
				return nil, fmt.Errorf("pipeline: exit %d", code)
			}
			return &pb.InvokeReply{}, nil
		case "agent":
			return invokeLiftedVerb(ctx, req, runVerbAgent)
		case "probe":
			return invokeLiftedVerb(ctx, req, runVerbProbe)
		case "ade":
			return invokeLiftedVerb(ctx, req, runVerbAde)
		case "generate":
			return invokeLiftedVerb(ctx, req, runVerbGenerate)
		case "emit":
			return invokeLiftedVerb(ctx, req, runVerbEmit)
		case "media":
			return invokeLiftedVerb(ctx, req, runVerbMedia)
		case "gate":
			return invokeLiftedVerb(ctx, req, runVerbGate)
		default:
			return nil, fmt.Errorf("pipeline: unsupported word %q", req.GetReserved())
		}

	case req.GetOp() == sdk.OpLoad: // kind decode: validate the entity body against #PipelineValue
		var body spec.Pipeline
		if len(req.GetParamsJson()) > 0 {
			if err := json.Unmarshal(req.GetParamsJson(), &body); err != nil {
				return nil, fmt.Errorf("pipeline entity decode: %w", err)
			}
		}
		if err := workflowkit.ValidatePipeline(&body); err != nil {
			return nil, err
		}
		return &pb.InvokeReply{}, nil

	default: // any other op (check/execute) → verb:pipeline probes
		return runVerb(req)
	}
}
