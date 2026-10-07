# TabForge

将 Excel/CSV 编译为经过校验的跨语言配置、代码与 Protobuf 数据，并从 Proto 与接口清单生成统一的数据接入约定。

基于 [davyxu/tabtoy](https://github.com/davyxu/tabtoy) 的 V3 版本开发，保留原作者的 MIT 许可和版权说明，使用独立的 Git 提交历史。导表当前只支持 V3，新增已有 Proto 映射、ProtoJSON 导出，以及缓存、并发加载和错误处理改进。

## 项目工作流与编辑器接入

策划按模板填写文件，放入 `Tables/` 后双击项目导出工具；开发者在 `Protocols/` 维护 Proto，在 VS Code 中导出，或作为 Go 包引入生成与结构加载能力。项目规则统一存放在 `tabforge.json`，路径不依赖命令执行目录。内置 Proto 编译与 Go 消息生成，不需要安装 protoc。

```bash
go run . -project=examples/complete  # 导出完整结构示例
go run . -init=/path/to/NewProject  # 创建模板与当前平台便携工具
go run ./cmd/package -out=outputs/releases/v2 # 构建便携包与四种编辑器插件
go run ./cmd/package-backend -out=outputs/releases/v3 # 构建四种语言的后端分发包
```

便携包内置可执行文件，策划无需安装开发环境。完整示例覆盖复杂 Proto 结构、普通导表、数据读取与 RPC。纯消息定义也能生成，导出失败会保留上次成功产物。见 [项目工作流](doc/project-workflow.md)、[完整示例](examples/complete/README.md)。

第二版新增 Unity、Cocos Creator、Godot 编辑器入口，支持创建示例、只校验、导出并导入、导入已有 Generated 包及错误报告。Unity 生成 C# ProtoJSON 类型，Cocos 生成 TS 类型，Godot 使用结构脚本和字典校验；资产重新导入保留 UUID。VS Code 新增 Problems 诊断与配置补全。安装、读取代码和验证范围见 [第二版工作流](doc/editor-workflow.md)。

第三版补齐 Go、Node.js/TypeScript、Java、Python 的后端包。Generated 新增数据清单，关联文件与根消息并记录校验值；运行时直接加载整个包，支持结构校验、独立数据快照与失败保留旧数据。Go 读取 ProtoJSON / Protobuf，其他三端读取 ProtoJSON。安装、示例和验证范围见 [第三版工作流](doc/backend-workflow.md)。

第四版提供统一版本源与发布交付入口：`go run ./cmd/release` 构建便携项目、四种编辑器插件和四种后端包，完成独立安装及本机解包验收后生成完整交付目录。包含构建身份、验收报告、发布清单和 SHA-256；版本检查用 `go run ./cmd/release -check`。维护与安装步骤见 [第四部分：发布交付与安装验收](doc/release-workflow.md)。

原有工具提供两个独立入口：

| 场景 | 维护的输入 | 输出与接入 |
| --- | --- | --- |
| 游戏或应用配置 | Excel/CSV、索引表；可映射已有 Proto | V3 配置代码、JSON、Lua、专用二进制或 Protobuf，见下文导表教程 |
| 前后端网络协议 | Proto RPC 与 `contract.json` 接口清单 | 七类协议产物、Go HTTP 服务和多平台 JSON/SSE 客户端，见 [前后端接入指南](doc/protocol-integration.md) |

统一协议模块包含字段验证、协议身份检查、全程超时/取消、大小限制、SSE 心跳和兼容性检查。普通请求返回 ProtoJSON，流式接口使用 SSE，可承载大模型文本、工具调用和用量事件。当前模型示例是固定数据，供应商适配需由业务后端实现。

| 接入端 | SDK | 当前验证范围 |
| --- | --- | --- |
| Go 后端 | [HTTP/SSE 服务](doc/protocol-integration.md) | Go 竞态回归与跨语言 HTTP 联调 |
| 网页 / Node | [TypeScript Fetch](sdk/typescript/README.md) | Node 24、Chromium Fetch/XHR |
| 微信小程序 | [wx.request 分块](sdk/wechat/README.md) | 开发者工具真实网络 12 项；真机与小游戏构建待测 |
| Cocos Creator 3.8 目标 | [Fetch/XHR/微信](sdk/cocos/README.md) | Web 网络后端通过；Creator/JSB/移动构建待测 |
| Unity 2022.3 / Unity 6 原生目标 | [UPM C#](sdk/unity/README.md) | C# 核心与 Go 联调；编辑器/IL2CPP 待测，WebGL SSE 未实现 |
| Unreal 5.5+ 目标 | [Runtime C++ 插件](sdk/unreal/README.md) | 独立 C++ 核心 sanitizer；引擎编译和原生 HTTP 待测 |
| Godot 4.5.1+ 原生目标 | [GDScript](sdk/godot/README.md) | 4.5.1 macOS headless 与 Go 联调；Web 流与移动导出待测 |

完整环境与用例见 [平台验收](doc/platform-validation.md)。代码实现与目标设备实测分开记录，不能从核心测试推断所有引擎构建均已兼容。

## 网络协议快速开始

```bash
# 仓库内已包含描述文件，可直接生成接入包。
go run . -protocol=examples/protocol/contract.json -protocol_out=examples/protocol/generated
go run ./examples/protocol
```

服务监听 `127.0.0.1:18082`。生成目录包含 `contract.json`、`schema.pb`、`types.ts`、`PROTOCOL.md`、`wire_schema.json`、`protocol.gd` 和 `runtime.json`；前后端分发同一生成包。`-protocol` 只校验，`-protocol_out` 指定生成目录，`-protocol_against` 检查升级兼容性；这些参数不能与 V3 导表参数混用。

首次接入从 [前后端指南](doc/protocol-integration.md) 和 [可运行协议示例](examples/protocol/README.md) 开始；设计细节见 [统一协议架构](doc/unified-protocol.md)，上线配置见 [生产部署说明](doc/production.md)。[平台验收工程](examples/platforms/README.md) 使用另一套服务，默认端口为 `18083`。

## 目录导航

| 目录 | 用途 |
| --- | --- |
| `project/`、`clientbundle/`、`editors/` | 项目导出、引擎资产导入和四种编辑器插件 |
| `examples/complete/`、`cmd/package/` | 完整模板和便携项目/插件 ZIP、VSIX 打包 |
| `release.json`、`cmd/release/`、`cmd/package-backend/` | 统一交付版本、完整构建与安装验收、四种后端包打包 |
| `protocol/` | 清单/描述文件校验、产物生成、兼容性检查和 Go HTTP/SSE 服务 |
| `sdk/` | TypeScript、微信、Cocos、Unity、Unreal、Godot 客户端 |
| `examples/protocol/` | 示例 Proto、清单、七类生成文件和固定响应服务 |
| `examples/platforms/` | 平台验收服务、打包入口、编辑器工程、运行器和实测记录 |
| `v3/` | Excel/CSV 导表、配置读取库和各语言配置示例 |
| `doc/` | [文档导航](doc/README.md)、接入、架构、部署、验收与[开发维护](doc/development.md) |

## 构建

项目最低使用 Go 1.26.6，toolchain 和 CI 固定 Go 1.26.8，在 Linux、macOS 和 Windows 上验证 Go/TypeScript；另有 Godot 4.5.1 Linux 测试任务，以及 Linux/macOS C#、C++ 协议核心任务。建议使用已修复安全问题的 Go 补丁版本。

```bash
git clone https://github.com/Coder-is/TabForge.git
cd TabForge
go build -o bin/tabforge .
./bin/tabforge -h
export PATH="$PWD/bin:$PATH"
```

以上为 Bash 命令。Windows 可执行 `go build -o bin/tabforge.exe .`，然后运行 `.\bin\tabforge.exe -h`。下文使用已加入 `PATH` 的 `tabforge`，Windows 对应 `tabforge.exe`。

也可执行 `go install github.com/Coder-is/TabForge@latest`，安装后的命令名为 `TabForge`。普通 `go build` 不注入版本元数据，`-version` 中的版本、提交和构建时间可能为空。

## V3 导表快速开始

在仓库根目录运行已有教程，无须手工创建表格：

```bash
bash v3/example/tutorial/Make.sh
```

结果为 `v3/example/tutorial/table_gen.json`。教程中的三个文件分别是：

**类型表 `Type.xlsx`**：定义对象的字段和输入类型。

| 种类 | 对象类型 | 标识名 | 字段名 | 字段类型 |
| --- | --- | --- | --- | --- |
| 表头 | MyData | ID | ID | int32 |
| 表头 | MyData | 名称 | Name | string |

**数据表 `MyData.xlsx`**：列头可使用类型表的标识名或字段名。

| ID | 名称 |
| --- | --- |
| 1 | 坦克 |
| 2 | 法师 |

**索引表 `Index.xlsx`**：列出需要加载的文件。

| 模式 | 表类型 | 表文件名 |
| --- | --- | --- |
| 类型表 | | Type.xlsx |
| 数据表 | MyData | MyData.xlsx |

在这三个文件所在目录中运行：

```bash
tabforge -index=Index.xlsx -json_out=table_gen.json
```

源文件路径相对于**命令执行目录**解析，不会自动以索引表所在目录为基准。表类型留空时，会使用源文件名去掉扩展名后的名称；类型表的模式必须为“类型表”。

## 功能与输出格式

支持 XLSX/CSV 混合输入、类型和数据校验、枚举、数组、多列数组、字段索引、拆分表、KV 表和标签过滤。输出任务并发执行，缓存和并发加载可单独开启。

| 输出 | 合并文件参数 | 按表目录参数 | 读取方式 |
| --- | --- | --- | --- |
| Go 源码 | `-go_out` | — | 与 JSON 配合使用 |
| C# 源码 | `-csharp_out` | — | 与专用 `.bin` 和 C# 读取库配合使用 |
| Java 源码 | `-java_out` | — | 与 JSON 配合使用 |
| 普通 JSON | `-json_out` | `-json_dir` | 生成代码或应用自己的 JSON 读取流程 |
| Lua | `-lua_out` | `-lua_dir` | `require("模块名").init(tab)` |
| 专用二进制 `.bin` | `-binary_out` | `-binary_dir` | C# `tabtoy.TableReader` |
| JSON 类型信息 | `-jsontype_out` | — | 查看表结构 |
| Proto3 定义 | `-proto_out` | — | 用 `protoc` 生成语言代码 |
| Protobuf 二进制 `.pbb` | `-pbbin_out` | `-pbbin_dir` | `proto.Unmarshal` 或对应语言的 Protobuf SDK |
| 已有 Proto 的 ProtoJSON | `-pbjson_out` | — | `protojson.Unmarshal` 或对应 SDK |

`.bin` 和 `.pbb` 使用不同的格式。`-json_out` 是 TabForge 普通 JSON，`-pbjson_out` 是已有消息的 ProtoJSON。分表 JSON 仍包含表名作为顶层键；分表 `.pbb` 仍使用合并根消息，只填充对应表字段。

Go JSON 读取库：

```go
import tabtoy "github.com/Coder-is/TabForge/v3/api/golang"

// 以下代码放在生成的 Table 类型所在包的函数内。
tab := NewTable()
err := tabtoy.LoadFromFile(tab, "table_gen.json")
// 或按表加载：err := tabtoy.LoadTableFromFile(tab, "ExampleData.json")
```

整表加载会执行注册的 Pre/Post 回调；分表加载只重置并构建对应表的索引。完整用法见 [Go 示例](v3/example/golang/main.go)。V3 读取库的 `tabtoy` 包名、C# 命名空间和二进制标识 `TABTOY` 保持原有定义。

### 示例入口

下面的脚本均可在仓库根目录调用，输出写入 `v3/example` 下的对应目录，会覆盖已有生成示例。

```bash
# 导出完整 XLSX 示例，包括合并文件、分表文件和 Proto3 定义。
bash v3/example/xlsx/Make.sh

# 导出 CSV 示例。
bash v3/example/csv/Make.sh

# 读取普通 JSON：先运行 XLSX 导出，确保合并和分表数据均已生成。
bash v3/example/golang/Make.sh

# 读取 Protobuf 数据：先运行 XLSX 导出；直接使用仓库内已有的 Go 消息代码。
bash v3/example/protobuf/golang/Make.sh

# 已有 Proto 映射导出：需要 protoc，无须 protoc-gen-go。
bash v3/example/existingproto/Make.sh
```

其他语言用法见 [C# 示例](v3/example/csharp/TabtoyExample/Program.cs)、[Java 示例](v3/example/java/src/test/java/Main.java) 和 [Lua 示例](v3/example/lua/main.lua)，需对应语言运行时。C# 读取库位于 [TableReader.cs](v3/api/csharp/TableReader.cs)。

## 命令行参数

执行 `tabforge -h` 查看完整参数。V3 未指定的输出不会生成，一次命令可指定多个输出；各输出须使用不同文件路径。协议入口独立使用下面前三个参数。

| 参数 | 用途与默认值 |
| --- | --- |
| `-project` | 按 tabforge.json 导出；传入配置文件或项目目录，不与旧导出参数混用 |
| `-init` | 创建完整模板、复制当前平台工具并首次导出，不覆盖已有文件 |
| `-protocol` | 协议清单路径；不带输出参数时只校验 Proto RPC 与清单 |
| `-protocol_out` | 协议七类产物的输出目录，覆盖同名文件，要求 `-protocol` |
| `-protocol_against` | 已发布的旧清单路径，检查旧客户端接入新服务端的兼容性，要求 `-protocol` |
| `-index` | V3 索引表文件，正常导出时指定 |
| `-mode` | 默认 `v3`，只接受 `v3` |
| `-package` | 生成代码的包名或命名空间，默认空；生成 Go、C#、Java 或 Proto 定义时建议明确填写 |
| `-combinename` | 合并根类型名，默认 `Table` |
| `-go_out` / `-csharp_out` / `-java_out` | Go / C# / Java 源码文件 |
| `-json_out` / `-lua_out` / `-binary_out` | 合并的 JSON / Lua / 专用 `.bin` 文件 |
| `-jsontype_out` | JSON 类型信息文件 |
| `-proto_out` | 从类型表生成 Proto3 定义，不能与 `-proto_desc` 同用 |
| `-pbbin_out` | 合并的 Protobuf `.pbb` 文件 |
| `-json_dir` / `-lua_dir` / `-binary_dir` / `-pbbin_dir` | 每个表各一个文件的输出目录 |
| `-proto_desc` / `-proto_map` | 已有 Proto 的描述文件与 JSON 映射，必须同时提供 |
| `-pbjson_out` | 已有 Proto 消息的 ProtoJSON 文件，要求 `-proto_desc` 和 `-proto_map` |
| `-tag_action` | 标签动作，格式为 `action:tag1+tag2\|action2:tag3` |
| `-para` | 并发加载，默认 `false` |
| `-usecache` | 启用 XLSX 缓存，默认 `false` |
| `-cachedir` | 缓存目录，默认 `./.tabtoycache`，启用缓存后生效 |
| `-version` | 显示构建版本信息 |

单文件和分表输出都会自动创建缺失的父目录：

```bash
tabforge -index=Index.xlsx -package=main -go_out=out/table_gen.go \
  -json_dir=out/json -proto_out=out/table.proto -pbbin_dir=out/pb
```

Lua 分表输出还包含枚举模块 `_TableType.lua`；修改 `-combinename` 时文件名随之变化。

## Protobuf 的两种使用方式

### 从表格生成 Proto3 定义

```bash
tabforge -index=Index.xlsx -package=main -proto_out=table.proto -pbbin_out=all.pbb
```

字段编号按类型表和输出表的顺序分配，调整排列可能改变编号。需要保持项目已有字段编号时，使用已有 Proto 映射。

Go 示例已包含生成的 [table.pb.go](v3/example/protobuf/golang/table.pb.go)。修改类型表后需要重新导出并生成 Go 消息代码：

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
# 确保 protoc 与 protoc-gen-go 均在 PATH 中，然后在 v3/example/protobuf/golang 执行：
protoc -I .. --go_out=. --go_opt=paths=source_relative \
  --go_opt='Mtable.proto=github.com/Coder-is/TabForge/v3/example/protobuf/golang;main' ../table.proto
```

`protoc` 下载见 [Protobuf Releases](https://github.com/protocolbuffers/protobuf/releases)。其他项目需将映射中的 Go 导入路径和包名替换为自己的值。

### 复用项目已有的 Proto

```bash
protoc -I ./proto --include_imports --descriptor_set_out=schema.pb ./proto/config.proto
tabforge -index=Index.xlsx -proto_desc=schema.pb -proto_map=mapping.json \
  -pbbin_out=tables.pbb -pbjson_out=tables.json
```

描述文件决定目标消息和字段编号，映射文件决定源表及列的对应关系。支持共享类型、嵌套字段、repeated、map、枚举、optional 和 oneof；不会生成另一套 Proto 定义。

映射格式、JSON 单元格、map 导出、校验和接入流程统一见 [已有 Proto 示例文档](v3/example/existingproto/README.md)。

## 表格规则

### 基础类型

类型表支持 `int16`、`int32`、`int64`、`uint16`、`uint32`、`uint64`、`float` / `float32`、`double` / `float64`、`bool` 和 `string`；`int`、`uint` 分别按 32 位处理。Java 的无符号整数映射到相应有符号类型，应用需自行处理取值范围。

专用二进制导出支持无符号类型的完整范围：`uint16` 最大为 `65535`，`uint32` 最大为 `4294967295`，`uint64` 最大为 `18446744073709551615`；负数和超出范围的输入会报错。

布尔值接受 `true` / `false`、`1` / `0`、`是` / `否`，也接受源码中定义的部分大小写形式。CSV 中包含逗号、双引号或换行的单元格须按 CSV 规则加双引号，内部双引号写为两个双引号；已有 Proto 示例中的 JSON 单元格采用此写法。

V3 不支持自定义默认值。普通输出中的空标量使用类型默认值；空枚举使用类型表的第一个枚举项。已有 Proto 映射中的空单元格不赋值，optional / oneof 可区分空值与显式填写的 `0`、`false`。Excel 中的大整数建议存为文本，避免输入文件先丢失精度。

### 枚举

| 种类 | 对象类型 | 标识名 | 字段名 | 字段类型 | 值 |
| --- | --- | --- | --- | --- | --- |
| 枚举 | ActorType | | None | int32 | 0 |
| 枚举 | ActorType | 狂鼠 | Junkrat | int32 | 1 |
| 表头 | ExampleData | 类型 | Type | ActorType | |

数据表可填写 `狂鼠` 或 `Junkrat`。普通数据输出使用枚举数值；生成代码保留枚举名称。已有 Proto 模式的枚举输入规则见其示例文档。

### 数组与多列数组

类型表中填写“数组切割”即定义数组，例如 `int32` 字段的分隔符为 `|` 时，单列单元格 `2|3` 导出为 `[2, 3]`。单列空单元格导出空数组。

多个同名数组列按列合并，每个单元格是一个元素，例如两列分别为 `1`、空，导出为 `[1, 0]`。**多列模式不会再按分隔符拆分每个单元格**。同一数组字段在拆分表中的列数须一致。

枚举数组逐个校验元素，每个非空元素须对应已定义的枚举标识名或字段名；非法元素会报 `UnknownEnumValue`。空元素按该枚举的默认项处理。

### 索引与拆分表

在类型表的“索引”列填写“是”，生成代码会建立索引，例如 `ExampleDataByID`。非空索引按实际类型比较：整数 `1` 与 `01`、布尔值 `true` 与“是”、同一枚举的标识名与字段名会被识别为重复。浮点数按定义精度比较，`-0` 与 `0` 相同；字符串 `1` 与 `01` 仍不同。空索引保持可选，不参与重复检查。数组、消息对象和非有限浮点数不能作为索引。

同一表类型可在索引表中引用多个文件，各文件的行会合并，未填写的字段使用默认值：

| 模式 | 表类型 | 表文件名 |
| --- | --- | --- |
| 数据表 | ExampleData | Data.csv |
| 数据表 | ExampleData | Data2.csv |

### KV 表

KV 字段直接定义在键值表中，索引表的模式使用“键值表”：

| 模式 | 表类型 | 表文件名 |
| --- | --- | --- |
| 键值表 | ExampleKV | KV.csv |

`KV.csv` 内容：

| 字段名 | 字段类型 | 标识名 | 值 | 数组切割 | 标记 |
| --- | --- | --- | --- | --- | --- |
| ServerIP | string | 服务器 IP | 8.8.8.8 | | |
| ServerPort | uint16 | 服务器端口 | 1024 | | |

### 空行、空列和注释

- 第一行是列头。列头遇到空列即停止，后续列不会加载。
- 遇到完整空行即停止读取，后续行不会导出。
- 首列有效单元格以 `#` 开头时，该行被忽略。避免注释首列列头，以免影响行识别。
- 列头以 `#` 开头时，该列不读取源数据；类型表仍定义的字段在普通输出中使用默认值。

## 名称校验

同一对象内的非空标识名须唯一，不能与其他字段的字段名冲突；标识名与自身字段名相同是允许的。同名枚举与表头类型、内建类型名称冲突会在加载阶段报错，KV 表也使用这些检查。

选择源码或 Proto 输出时，会进一步检查该格式使用的类型、字段、包名和生成成员名称，并报告源位置：

| 输出 | 名称要求 |
| --- | --- |
| Go | 单一包名；使用 Go 标识符；表名及数据字段须导出，不能与生成方法、索引、枚举辅助类型或变量重名 |
| C# | 包名作为命名空间，可用点分隔；拒绝保留关键字、非法字符和成员与所在类型同名 |
| Java | 支持点分包名；拒绝关键字、受限类型名，以及与模板依赖类型或辅助类型冲突的名称 |
| Lua | 使用 ASCII 字母、数字和下划线，不能以数字开头或使用关键字；表、枚举和索引在输出根表中不得重名 |
| Proto3 | 使用 ASCII 标识符；检查枚举值的包级作用域，以及去掉下划线、大小写与枚举前缀后产生的名称冲突 |

生成的合并根类型也不能与输出类型或辅助类型重名，可用 `-combinename` 调整。模板不自动转义关键字。普通 JSON 不受其他输出语言的额外名称规则限制；已有 Proto 映射使用目标描述文件的名称，不校验未使用的 `-package` 和 `-combinename`。

名称规则参考 [C# 规范](https://learn.microsoft.com/en-us/dotnet/csharp/language-reference/language-specification/lexical-structure)、[Java 规范](https://docs.oracle.com/en/java/javase/26/docs/specs/jls/jls-3.html#jls-3.8)、[Lua 手册](https://www.lua.org/manual/5.4/manual.html#3.1) 和 [Proto3 规范](https://protobuf.dev/reference/protobuf/proto3-spec/)；Proto3 冲突检查也与项目所用 Protobuf 描述验证器保持一致。

## 标签过滤

在索引表或类型表的“标记”列填写标签，多个标签用 `|` 分隔。通过 `-tag_action` 选择动作，多个动作使用 `|`，同一动作的多个标签使用 `+`；shell 中须给整段值加引号。

```bash
# 客户端：不导出 server 表，普通 JSON 也不包含 server 字段。
tabforge -index=Index.xlsx -json_out=client.json \
  -tag_action='nogentab:server|nogenfield_json:server'

# 服务端：不导出 client 表和字段。
tabforge -index=Index.xlsx -json_out=server.json \
  -tag_action='nogentab:client|nogenfield_json:client'
```

| 动作 | 标记位置 | 作用 |
| --- | --- | --- |
| `nogentab` | 索引表 | 不导出带该标签的表 |
| `nogenfield_json` | 类型表 | 普通合并 JSON 不包含该字段 |
| `nogenfield_jsondir` | 类型表 | 普通分表 JSON 不包含该字段 |
| `nogenfield_binary` | 类型表 | 专用二进制不包含该字段 |
| `nogenfield_pbbin` | 类型表 | Protobuf 二进制不包含该字段；已有 Proto 模式下也作用于 ProtoJSON |
| `nogenfield_lua` | 类型表 | Lua 不包含该字段 |
| `nogenfield_csharp` | 类型表 | 生成的 C# 不包含该字段 |

字段动作按输出格式分别生效，例如 `nogenfield_json` 不过滤分表 JSON，也不过滤 Go 源码。已有 Proto 映射引用的表须存在于编译结果中，经 `nogentab` 过滤后仍被映射引用的表会报错。

## 缓存、并发和失败处理

```bash
tabforge -index=Index.xlsx -json_out=out/tables.json \
  -usecache=true -cachedir=.tabtoycache -para=true
```

XLSX 使用缓存，CSV 不使用；缓存目录会自动创建。缓存文件名来自源文件完整路径，并校验源表与缓存内容。缓存缺失、损坏或版本过旧时会重新读取源表；缓存写入失败显示警告，仍可继续导出。

`-para` 的同时加载数量受 Go 运行时 `GOMAXPROCS` 限制，同一批次中规范化后重复的路径只加载一次。多个输出任务全部结束后统一报告失败，失败返回非零退出码；成功任务可能已写出文件，输出不保证整体原子性。

## 开发验证

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run build:platforms
go test -race -count=1 ./...
go test ./v3/model -run '^$' -bench BenchmarkTypeFieldLookup -benchmem
```

配置测试覆盖类型和数据校验、已有 Proto 映射、缓存故障恢复、并发失败处理，以及无缓存、冷缓存、热缓存和并发加载时的导出一致性，见 [检查清单](v3/checker/TODO.md)。协议测试覆盖产物生成/升级、JSON/SSE、类型与身份校验、超时/取消、大小限制与各端联调。

TS 集成测试需要 Node 24+；Godot/C# 联调需要分别设置 GODOT_BIN / DOTNET_BIN，未提供运行时会跳过对应测试。独立 C++ 核心与编辑器验收命令见 [平台验收](doc/platform-validation.md)，CI 安装范围见 [生产说明](doc/production.md)。

源码职责、扩展导出器和分层验证步骤见 [开发维护指南](doc/development.md)；2026-10-04 的变更与回归结果见 [代码重构记录](doc/refactoring.md)。

V2 导出器、V2→V3 迁移工具及其专用参数、示例和文档已移除。旧模式 `v2`、`exportorv2`、`v2tov3` 会报错；旧参数如 `-protover`、`-cpp_out`、`-type_out`、`-pbt_out` 不再可用。迁移输入须使用本文的 V3 索引表、类型表和数据表格式。

## 许可与反馈

原始版权声明见 [MIT LICENSE](LICENSE)。问题和功能建议请提交到 [TabForge Issues](https://github.com/Coder-is/TabForge/issues)。
