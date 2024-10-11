package main

import (
	"flag"
	"os"
	"path"
	"strings"
	"svelte-ssr-to-templ/builder"

	"golang.org/x/sync/errgroup"
)

var (
	queueDir       = flag.String("in", "", "Directory containing the files to be processed")
	outputBuildDir = flag.String("out", "", "Directory to output the built files")
)

func main() {
	flag.Parse()

	output, found := os.LookupEnv("PWD")
	if !found {
		panic("PWD not found")
	}

	calledFromDir := strings.ReplaceAll(string(output), "\\", "/")
	calledFromDir = strings.Trim(calledFromDir, "\n")

	// Convert the path to a windows path
	if calledFromDir[0] == '/' {
		calledFromDir = calledFromDir[2:]
		calledFromDir = "C:" + calledFromDir
	}

	buildOpts := &builder.BuildOptions{
		QueueDir:       *queueDir,
		OutputBuildDir: *outputBuildDir,
		WaitGroup:      &errgroup.Group{},
	}

	if buildOpts.QueueDir == "" {
		panic("queueDir is required")
	}
	if buildOpts.OutputBuildDir == "" {
		panic("outputBuildDir is required")
	}

	// Resolve the paths to the queue and output relative to the executable path
	buildOpts.QueueDir = path.Join(calledFromDir, buildOpts.QueueDir)
	buildOpts.OutputBuildDir = path.Join(calledFromDir, buildOpts.OutputBuildDir)

	builder.Build(buildOpts)
}
