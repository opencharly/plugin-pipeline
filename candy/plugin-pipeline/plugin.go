// Package pluginpipeline — the generic agent/workflow engine (domain-neutral).
// Provides: kind:pipeline (a declared plan as a charly.yml entity), command:pipeline
// (the executor + the standalone agent runtime), verb:pipeline (deterministic probes).
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
	}, schemaFS)
}

type provider struct{ pb.UnimplementedProviderServer }

func (provider) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	switch {
	case req.GetOp() == sdk.OpRun:
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
		case "agent", "probe", "ade", "generate", "media", "gate", "command":
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
