# 完整示例项目

这是策划填写、导出、程序读取的完整路径。所有数据是演示数据。

## 策划使用

便携包解压后直接双击 `Tools/TabForge/Export.bat`（Windows）或 `Export.command`（macOS）。不需要安装 Go、Node、protoc，也不用设置 PATH。项目程序已经在 `tabforge.json` 中设置输出规则。

修改 `Tables/Items.xlsx`，保存并关闭后导出；成功结果在 `Generated/`。增加同类型的数据文件时，在 `Tables/Index.csv` 中加一行。`Items.csv` 是 Excel 的原始内容对照，不参与默认导出，不要同时把它与 Excel 加入索引导致重复 ID。

仓库源码不带预编译可执行文件，开发者可在仓库根目录执行 `go run . -project=examples/complete`。用 `go run . -init=/自己的/项目目录` 创建独立模板、复制当前系统的工具并首次导出；有开发环境的人只需做一次，之后策划双击即可。

## 文件职责

| 文件 | 用途 |
| --- | --- |
| `tabforge.json` | 项目级配置；程序维护一次 |
| `Tables/Index.csv` | 引用类型表、Excel 物品表、合并表和设置表 |
| `Tables/Type.csv` | 输入列名、类型和数组分隔符 |
| `Tables/Items.xlsx` | 策划填写的复杂结构数据 |
| `Tables/mapping.json` | 输入字段与 Proto 字段的映射 |
| `Protocols/` | 程序维护的 Proto；import 与标准类型均在工具内部编译 |
| `Protocols/contract.json` | 可选的网络路由和流事件配置 |
| `Generated/schema/` | 全部结构、Go 代码、TS 类型、数据校验 SDK 与结构文档 |
| `Generated/data/` | 相同数据的 Protobuf、ProtoJSON、按表 Protobuf |
| `Generated/basic/` | 原有导表功能的各格式示例 |
| `Generated/protocol/` | 普通/流式 RPC 生成包；沿用网络 SDK 接入方式 |

`Generated/` 是完整生成目录，下次成功导出会整体替换；不要在里面写业务代码。输入或生成失败会保留上次成功的整个目录。发布时由项目构建流程复制/打包所需文件。

## 结构填写对照

| 结构 | 示例字段/文件 | 输入方式及读取结果 |
| --- | --- | --- |
| 基础类型 | ID、名称、启用、重量、价格、库存 | 数值/文本/布尔；空标量使用 Proto 默认值 |
| int64/uint64 | 有符号大整数、拥有者、奖励数量 | Excel 以文本保存；JSON 为十进制字符串，不转浮点数 |
| 枚举 | 稀有度 | `NORMAL`、`EPIC`；Basic 表也演示中文枚举别名 |
| 嵌套对象 | 奖励ID、奖励数量 | 两列映射到 `reward.item_id` 与 `reward.count` |
| 基础数组 | 等级、标签 | `1\|5\|10`、`新手\|武器` |
| 对象数组 | 消耗 | `[{"itemId":2002,"count":"3"}]` |
| map | 属性、额外奖励 | `{"attack":12}`、`{"login":{"itemId":2004,"count":"2"}}` |
| optional | 解锁等级 | 第一行填 `0`，第二行留空；Go `UnlockLevel != nil` 区分 presence |
| oneof | 文字效果、数值效果 | 一行最多填写其中一列；不能同时填写 |
| bytes | 图标Hash | Base64 `YWJj`；Go 读取为 `[]byte("abc")` |
| Timestamp、Duration | 创建时间、有效期 | JSON 字符串：`"2026-10-04T00:00:00Z"`、`"1.500s"`，双引号也是单元格内容 |
| Struct、FieldMask | 扩展属性、字段选择 | `{"level":1,"visible":true}`、`"id,name"` |
| Any | 动态消息 | `{"@type":"type.googleapis.com/tabforge.demo.common.Reward","itemId":2003,"count":"9"}` |
| wrapper | 优先级 | 第一行填 `0`，保留消息 presence |
| repeated bool | 开关列表 | `[true,false]` |
| map 根表 | `mapping-map.json` | 默认同时导出 `tables-map.json/.pbb`；通过 `tables.byId["1001"]` 读取 |
| 递归、嵌套类型、未被 RPC 引用的类型 | `structures.proto` | 也会生成；TS `loadTree` 展示结构校验 |
| 多列数组 | `Basic/Actors.csv` | 两列多列奖励合成 `[100,200]`；空元素按 `0` 处理 |
| 索引、多文件合并 | `Basic/` | `Actors` 与 `ActorsExtra` 合并；生成 Go `ActorByID` 索引 |
| KV 表 | `Basic/Settings.csv` | 服务地址、端口与欢迎语直接声明 |
| 标签过滤 | 服务端备注 | 普通 JSON 不输出该字段；其他格式规则可在配置中分别设置 |

Proto 为最终结构和字段编号来源。类型表描述输入格式，mapping 连接输入列与 Proto 字段。普通导表不具有 Proto 模式的任意嵌套结构能力。

## 程序读取

仓库根目录：

```bash
go run . -project=examples/complete
go run ./examples/complete/Clients/go
node --input-type=module -e "import('node:fs/promises').then(async fs => (await import('./examples/complete/Clients/web/client.ts')).loadTables(await fs.readFile('examples/complete/Generated/data/tables.json','utf8')))"
```

Node 示例使用 Node 24+；浏览器使用业务构建器处理 TypeScript。客户端从 `Generated/schema/` 导入 `DataSchema`、`MessageTypes` 和 `wireSchema`，得到字段补全和运行时校验。TS 数据 SDK 当前读取 ProtoJSON；没有 Protobuf 二进制编解码器。

Go 示例同时展示生成类型的 `proto.Unmarshal` 和第三方包 `protocol.LoadSchema` 的动态读取。导入自己的业务项目前，把 Proto 的 `go_package` 改成业务模块中生成代码的导入路径。生成消息、描述和数据来自同一次导出。

只需要数据结构时，从 `tabforge.json` 删除 `protocol` 项即可；仅生成结构时再删除 `tables`。不必编写 RPC。网络服务与各平台客户端仍沿用 TabForge 的协议接入指南，版本/hash 必须一致。

## 错误定位练习

- 将物品 ID 改成重复值：导出失败并报告源位置。
- 稀有度填写不存在的名称：报非法枚举。
- 同时填写文字效果和数值效果：报 oneof 冲突。
- 有符号大整数填写 `9223372036854775808`：报溢出。
- 消耗 JSON 拼写未知字段：报字段错误。

恢复后再次导出。失败期间上次的生成文件应保持原样。输入文件必须保留第一行列头，不要在数据中插入整行空白；Excel 的大整数必须保持文本格式。
