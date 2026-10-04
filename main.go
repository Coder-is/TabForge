package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Coder-is/TabForge/build"
)

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

	if *paramProtocol != "" || *paramProtocolOut != "" || *paramProtocolAgainst != "" {
		if err := protocolEntry(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	V3Entry()
}
