package main

import (
	"flag"
	"fmt"

	"github.com/Coder-is/TabForge/protocol"
)

var (
	paramProtocol        = flag.String("protocol", "", "validate a Proto transport contract JSON (independent of table exports)")
	paramProtocolOut     = flag.String("protocol_out", "", "generate contract review bundle and ProtoJSON TypeScript types to a directory")
	paramProtocolAgainst = flag.String("protocol_against", "", "reject breaking changes against an older protocol manifest")
)

func protocolEntry() error {
	if *paramProtocol == "" {
		return fmt.Errorf("-protocol_out and -protocol_against require -protocol")
	}
	// Do not silently ignore existing table exporter options in protocol mode.
	var conflicting string
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "protocol", "protocol_out", "protocol_against", "mode", "version":
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
	if *paramProtocolAgainst != "" {
		previous, err := protocol.Load(*paramProtocolAgainst)
		if err != nil {
			return err
		}
		changes := protocol.BreakingChanges(previous, c)
		if len(changes) != 0 {
			for _, change := range changes {
				fmt.Printf("BREAKING %s: %s\n", change.Path, change.Reason)
			}
			return fmt.Errorf("protocol has %d breaking changes", len(changes))
		}
		fmt.Println("No breaking changes against the previous client contract")
	}
	if *paramProtocolOut != "" {
		if err := c.Generate(*paramProtocolOut); err != nil {
			return err
		}
		fmt.Printf("Generated protocol bundle: %s\n", *paramProtocolOut)
	}
	fmt.Printf("Validated protocol %s %s (%d endpoints)\n", c.Manifest.Name, c.Manifest.Version, len(c.Endpoints))
	fmt.Printf("Schema hash: %s\n", c.Fingerprint())
	return nil
}
