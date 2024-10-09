package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"svelte-ssr-to-templ/builder"

	"golang.org/x/sync/errgroup"
)

func main() {
	flag.Parse()

	var ex, _ = os.Executable()
	var exPath = strings.ReplaceAll(filepath.Dir(ex), "\\", "/")

	buildOpts := &builder.BuildOptions{
		QueueDir:       *flag.String("queueDir", "", "Directory containing the files to be processed"),
		FullQueueDir:   *flag.String("fullQueueDir", "", "Directory containing the files to be processed"),
		OutputBuildDir: *flag.String("outputBuildDir", "", "Directory to output the built files"),
		ExecutablePath: exPath,
		WaitGroup:      &errgroup.Group{},
	}

	builder.Build(buildOpts)
}
