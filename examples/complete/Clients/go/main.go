package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Coder-is/TabForge/databundle"
	configpb "github.com/Coder-is/TabForge/examples/complete/Generated/schema/go"
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
	bundle, err := databundle.Open(filepath.Join(root, "Generated"), "")
	if err != nil {
		return err
	}
	tables := new(configpb.Tables)
	if err := bundle.ReadInto("data/tables.pbb", tables); err != nil {
		return err
	}
	fmt.Printf("强类型读取：%d 件物品，第一件=%s，奖励数量=%d\n", len(tables.Items), tables.Items[0].Name, tables.Items[0].Reward.Count)
	message, err := bundle.Read("data/tables.json")
	if err != nil {
		return err
	}
	items := message.ProtoReflect().Descriptor().Fields().ByName("items")
	fmt.Printf("动态结构读取：%d 件物品，schema=%s\n", message.ProtoReflect().Get(items).List().Len(), bundle.SchemaHash())
	return nil
}
