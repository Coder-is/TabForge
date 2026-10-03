package main

import (
	"flag"
	"fmt"
	"github.com/Coder-is/TabForge/build"
	"github.com/pkg/profile"
	"os"
)

var enableProfile = false

func main() {

	flag.Parse()

	// 版本
	if *paramVersion {
		build.Print()
		return
	}

	if *paramMode != "v3" {
		fmt.Fprintf(os.Stderr, "unsupported mode %q: only v3 is supported\n", *paramMode)
		os.Exit(1)
	}

	if enableProfile {
		profiler := profile.Start(profile.CPUProfile, profile.ProfilePath("."))
		defer profiler.Stop()
	}
	V3Entry()
}
