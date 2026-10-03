package main

import (
	"flag"
	"fmt"

	"github.com/Coder-is/TabForge/protocol"
)

var (
	paramProtocol    = flag.String("protocol", "", "validate a Proto transport contract JSON (independent of table exports)")
	paramProtocolOut = flag.String("protocol_out", "", "generate contract review bundle and ProtoJSON TypeScript types to a directory")
)

func protocolEntry() error {
	if *paramProtocol == "" {
		return fmt.Errorf("-protocol_out requires -protocol")
	}
	// Do not silently ignore existing table exporter options in protocol mode.
	var conflicting string
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "protocol", "protocol_out", "mode", "version":
		default:
			conflicting = f.Name
		}
	})
	if conflicting != "" {
		return fmt.Errorf("-protocol is independent of table exports; remove -%s", conflicting)
	}
	c, err := protocol.Load(*paramProtocol)
	if err != nil {
		return err
	}
	if *paramProtocolOut != "" {
		if err := c.Generate(*paramProtocolOut); err != nil {
			return err
		}
		fmt.Printf("Generated protocol bundle: %s\n", *paramProtocolOut)
	}
	fmt.Printf("Validated protocol %s %s (%d endpoints)\n", c.Manifest.Name, c.Manifest.Version, len(c.Endpoints))
	return nil
}
