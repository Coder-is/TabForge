package main

import (
	"fmt"
	"os"
	"path/filepath"

	configpb "github.com/Coder-is/TabForge/examples/complete/Generated/schema/go"
	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/proto"
)

func main() {
	root := "examples/complete"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if err := run(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "Generated/data/tables.pbb"))
	if err != nil {
		return err
	}
	tables := new(configpb.Tables)
	if err := proto.Unmarshal(data, tables); err != nil {
		return err
	}
	fmt.Printf("强类型读取：%d 件物品，第一件=%s，奖励数量=%d\n", len(tables.Items), tables.Items[0].Name, tables.Items[0].Reward.Count)
	schema, err := protocol.LoadSchema(filepath.Join(root, "Generated/schema/schema.pb"))
	if err != nil {
		return err
	}
	message, err := schema.ReadFile("tabforge.demo.config.Tables", filepath.Join(root, "Generated/data/tables.json"))
	if err != nil {
		return err
	}
	items := message.ProtoReflect().Descriptor().Fields().ByName("items")
	fmt.Printf("动态结构读取：%d 件物品，schema=%s\n", message.ProtoReflect().Get(items).List().Len(), schema.Fingerprint())
	return nil
}
