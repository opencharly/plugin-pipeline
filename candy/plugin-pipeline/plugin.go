// Package pluginpipeline — the generic agent/workflow engine (domain-neutral).
// Provides: kind:pipeline (a declared plan as a charly.yml entity), command:pipeline
// (the executor + the standalone agent runtime), verb:pipeline (deterministic
// probes), and the seven LIFTED WORKFLOW VERBS (verb:agent/probe/ade/generate/
// emit/media/gate — every stage kind except the inline `command`, each reachable
// as a `<word>: <input>` step from any plan).
// SDD: the CUE schema (schema/pipeline.cue) is the single source; params are generated
// (params/cue_types_gen.go); every authored input is validated at load.
package pluginpipeline

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/opencharly/plugin-pipeline/candy/plugin-pipeline/params"
	"github.com/opencharly/sdk"
	pb "github.com/opencharly/spec/proto"
)

//go:embed schema/*.cue
var schemaFS embed.FS

const calver = "2026.251.0000"

// entityCache holds the kind:pipeline entity bodies the loader decoded (OpLoad),
// keyed by entity name — the command reads them here (same process, no reparsing).
var entityCache = map[string]params.PipelineInput{}

func NewProvider() pb.ProviderServer { return &provider{} }

func NewMeta() pb.PluginMetaServer {
	return sdk.NewMeta(calver, []sdk.ProvidedCapability{
		{Class: "command", Word: "pipeline"},
		{Class: "verb", Word: "pipeline"},
		{Class: "kind", Word: "pipeline", InputDef: "#PipelineInput"},
		// the LIFTED WORKFLOW VERBS: every kind:pipeline stage kind (except the
		// inline `command`) is an ordinary verb, so `<word>: <input>` reaches it
		// from ANY plan, not only from a kind:pipeline entity's stages:. The
		// InputDef is the stage's frozen #Pipeline<Word>Input shape (see
		// verb_input_defs_test.go); no Primary — a lifted verb is declarative,
		// it never claims a scalar sugar spelling (D3).
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

	case req.GetOp() == sdk.OpLoad: // kind decode: validate the entity body against #PipelineInput
		var body params.PipelineInput
		if len(req.GetParamsJson()) > 0 {
			if err := json.Unmarshal(req.GetParamsJson(), &body); err != nil {
				return nil, fmt.Errorf("pipeline entity decode: %w", err)
			}
		}
		if err := validatePipeline(body); err != nil {
			return nil, err
		}
		name := req.GetReserved()
		if name != "" {
			entityCache[name] = body
		}
		return &pb.InvokeReply{}, nil

	default: // any other op (check/execute) → verb:pipeline probes
		return runVerb(req)
	}
}

func validatePipeline(p params.PipelineInput) error {
	if len(p.Stages) == 0 {
		return errors.New("pipeline: entity has no stages")
	}
	for _, s := range p.Stages {
		if s["id"] == "" || s["kind"] == "" {
			return errors.New("pipeline: stage missing id or kind")
		}
		switch s["kind"] {
		case "agent", "probe", "ade", "generate", "emit", "media", "gate", "command":
		default:
			return fmt.Errorf("pipeline: unknown stage kind %q", s["kind"])
		}
	}
	return nil
}

// lookup loads the named pipeline entity (already validated at OpLoad).
func lookup(name string) (params.PipelineInput, error) {
	p, ok := entityCache[name]
	if !ok {
		return p, fmt.Errorf("pipeline entity %q not found (not loaded by kind:pipeline)", name)
	}
	return p, nil
}
