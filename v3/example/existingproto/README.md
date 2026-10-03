# 使用已有 Protobuf 定义导出配置

这个示例演示 `Excel/CSV → 已有 Protobuf 类型 → .pbb/ProtoJSON`。`common.proto` 定义共享奖励和枚举，`config.proto` 引用这些结构；TabForge 不会重新生成或修改它们。

## 运行示例

在仓库根目录执行：

```bash
bash v3/example/existingproto/Make.sh
```

需要 Go 和 `protoc`，仅导出数据不需要 `protoc-gen-go`。示例已包含 `schema.pb`，也可以跳过重新编译 Proto，从本目录直接运行：

```bash
go run ../../.. -mode=v3 -index=Index.csv \
  -proto_desc=schema.pb -proto_map=mapping.json \
  -pbbin_out=out/tables.pbb -pbjson_out=out/tables.json \
  -pbbin_dir=out/by-table
```

索引表中的源文件路径相对于当前工作目录解析；手动执行上述命令前应切换到 `v3/example/existingproto`。

结果：

- `out/tables.pbb`：`example.config.Tables` 的完整二进制配置。
- `out/tables.json`：相同消息的标准 ProtoJSON，便于检查数据。
- `out/by-table/Item.pbb`、`Settings.pbb`：仍以 `Tables` 为根消息，分别只填充对应表。

## 接入自己的项目

1. 用现有 Proto 定义一个配置根消息，包含各表对应的 `repeated Message`、`map<Key, Message>` 或单个 `Message` 字段。
2. 编译描述文件，包含所有依赖：

   ```bash
   protoc -I ./proto --include_imports \
     --descriptor_set_out=./schema.pb ./proto/config.proto
   ```

3. 按 TabForge V3 格式准备索引表、类型表和数据表。类型表定义源列的名称和输入格式，Proto 负责最终消息类型与字段编号。
4. 编写映射文件并使用 `-proto_desc`、`-proto_map` 导出。

TabForge 无须导入使用者的 Go 包，也无须链接使用者的生成代码。不同游戏的消息名称、包名和字段编号均从描述文件中读取。

## 映射文件

```json
{
  "root_message": "example.config.Tables",
  "tables": {
    "Item": {
      "field": "items",
      "fields": {
        "ID": "id",
        "Name": "name",
        "RewardID": "reward.item_id",
        "RewardCount": "reward.count",
        "Costs": "costs",
        "Levels": "levels",
        "Stats": "stats",
        "Rarity": "rarity",
        "IconHash": "",
        "UnlockLevel": "unlock_level",
        "TextEffect": "text_effect",
        "PowerEffect": "power_effect"
      }
    }
  }
}
```

- `root_message` 是已有 Proto 消息的完整名称。
- `tables` 的键是索引表中导出的表名；只导出这里列出的表。
- `field` 指定根消息中的目标字段，可以通过点号进入单个嵌套消息。
- `fields` 的键是类型表的“字段名”，不是中文“标识名”；值是目标 Proto 字段路径。
- 未指定映射的源字段按同名匹配。目标支持 Proto 原字段名和 JSON 字段名，如 `item_id`、`itemId`。
- 映射到空字符串表示忽略该源字段，`nogenfield_pbbin` 标记也有效。
- 点号路径的中间层必须是单个消息；数组和 map 的内容使用 JSON 单元格表示。
- 不允许两个源字段映射到相同路径，也不允许整条消息与其子字段同时映射。

对于根消息中的 map，使用 `mapping_map.json`，其中 `key_field` 指定源表的键列：

```json
{ "field": "by_id", "key_field": "ID", "fields": { "ID": "id" } }
```

从示例目录运行 map 导出：

```bash
go run ../../.. -index=Index.csv -proto_desc=schema.pb -proto_map=mapping_map.json \
  -pbbin_out=out/tables-map.pbb -pbjson_out=out/tables-map.json
```

上述 JSON 是 map 目标的配置片段，完整示例见 [mapping_map.json](mapping_map.json)。键必须能解析为 Proto 定义的键类型，重复键会报错。单个消息的目标只允许一行数据。

## 单元格表示

| Proto 类型 | 表格内容 | 类型表输入类型 |
| --- | --- | --- |
| 数字、布尔、字符串 | `100`、`true`、`是`、`新手剑` | 对应基础类型，或 `string` |
| 枚举 | `EPIC` 或已定义的编号 `2` | `string`；也可映射类型表中的枚举别名 |
| 嵌套消息 | `{"itemId":2001,"count":"3"}`，或拆成列映射到 `reward.item_id`、`reward.count` | JSON 单元格使用 `string` |
| repeated 基础类型 | `1\|5\|10`，分隔符设为 `\|`；或 JSON 数组 `[1,5,10]` | 前者使用数组类型，后者使用 `string` |
| repeated 消息 | `[{"itemId":2002,"count":"3"}]` | `string`；也支持按分隔符拆分多个 JSON 消息 |
| map | `{"attack":12,"defense":5}` | `string` |
| bytes | Base64，如 `YWJj` | `string` |
| optional / oneof | 非空列赋值，空列不赋值；`0` 与空单元格可区分 | 对应基础类型，或 `string` |
| Timestamp / Duration / Any | 按标准 ProtoJSON 写完整 JSON 值 | `string` |

64 位整数可使用 `int64`、`uint64` 或 `string` 输入类型，导出过程中不会先转成浮点数。所有 JSON 单元格都遵循 ProtoJSON 的字段类型、枚举和 oneof 校验；未知字段不会被静默忽略。

## Go 中使用已有类型

继续用项目自己的 `protoc` 流程生成 Go 代码，然后直接读取配置：

```go
data, err := os.ReadFile("tables.pbb")
if err != nil {
    return err
}
cfg := new(configpb.Tables)
if err := proto.Unmarshal(data, cfg); err != nil {
    return err
}
// cfg.Items[0].Reward 就是 commonpb.Reward，无需手写结构体或逐字段转换。
```

这里的 `proto` 使用 `google.golang.org/protobuf/proto`，`configpb` 使用你现有的生成包。使用 `mapping_map.json` 导出后，可直接通过 `cfg.ById[1001]` 查找。

## 兼容性与校验

- 现有 `-proto_out`、不带描述文件的 `-pbbin_out` 等行为保留。
- `-proto_desc` 和 `-proto_map` 必须同时提供；此模式不能再使用 `-proto_out` 生成另一套定义。
- `-json_out` 仍是 TabForge 原有 JSON 格式；读取已有 Proto 消息的 JSON 请用 `-pbjson_out` 和 `protojson.Unmarshal`。
- 更改 Proto 后重新编译 `schema.pb`。字段编号始终取自 Proto，不依赖表格或 Proto 声明的排列顺序。
- 配置错误、缺失依赖、未知列/字段、数值溢出、非法 JSON、重复 map 键和 oneof 冲突会返回错误。
- `-package` 和 `-combinename` 不会重命名已有 Proto 消息；根类型由 `root_message` 决定。
- `nogenfield_pbbin` 同时作用于此模式的 `.pbb` 和 ProtoJSON；映射中的表须存在于编译结果中，经 `nogentab` 过滤后仍被映射引用的表会报错。
- `.pbb` 使用确定性序列化；ProtoJSON 的空白排版不作为兼容性约定。
- 此接口面向描述文件中的普通消息字段；不支持用字段映射设置 Proto2 扩展字段。运行时索引、跨表业务关联和热更新切换由应用管理。
