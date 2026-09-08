package main

import (
	pluginpipeline "github.com/opencharly/plugin-pipeline/candy/plugin-pipeline"
	"github.com/opencharly/sdk"
)

func main() {
	sdk.Serve(pluginpipeline.NewProvider(), pluginpipeline.NewMeta())
}
