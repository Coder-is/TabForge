# 第一版项目工作流

第一版的项目格式和后端 API 继续可用。Unity、Cocos、Godot 编辑器接入及结构化报告见 [第二版工作流](editor-workflow.md)。

项目以 `tabforge.json` 为入口。策划使用随项目提供的工具；编辑器和后端构建调用同一个 `project.Export` 内核。项目导出与原有命令行参数入口共存。

## 开始使用

```bash
# 导出完整示例，不需要 protoc。
go run . -project=examples/complete
# 新建模板、复制当前系统工具并首次导出。
go run . -init=/path/to/NewProject
# 维护者：生成四个平台的便携 ZIP 与 VSIX。
go run ./cmd/package
```

便携 ZIP 已包含模板、预生成产物和对应系统的工具。解压后双击 `Tools/TabForge/Export.bat` 或 `Export.command`。目录可带空格和中文，不需要 PATH、Go、Node 或 protoc。

macOS 发行文件仍受系统打开确认和签名策略约束，本仓库生成的是未签名开发包，没有完成公证。维护者应在首次分发时验证系统能打开；下载包的架构要与电脑对应。

VS Code 安装对应平台 `.vsix` 后，执行 **TabForge: 导出项目**。可选开启 `tabforge.exportOnSave`，外部 Excel 保存也会触发导出；失败信息显示在输出面板。第一版的编辑器入口是 VS Code 桌面。Unity/Unreal/Cocos/Godot 现有 SDK 继续提供运行时接入，各引擎的编辑器生成面板尚未新增。

## 项目配置

```json
{
  "version": 1,
  "output": "Generated",
  "schema": { "dir": "Protocols", "go": true },
  "tables": [{
    "index": "Tables/Index.csv",
    "mapping": "Tables/mapping.json",
    "outputs": { "protobuf": "data/tables.pbb", "protojson": "data/tables.json" }
  }]
}
```

| 规则 | 说明 |
| --- | --- |
| `version` | 必须为 `1`；拒绝未知配置字段 |
| `output` | 默认 `Generated`；成功后整体替换的完整生成目录 |
| `schema.dir` | 默认 `Protocols`；自动收集全部 `.proto`，包括未被 RPC 引用的类型 |
| `schema.files` | 可选，指定相对于 `schema.dir` 的入口；依赖自动编译 |
| `schema.imports` | 可选，额外 import 目录，相对于项目根目录 |
| `schema.go` | 默认 false；true 时内置生成 `.pb.go`，应用 Proto 必须设置 `go_package` |
| `protocol` | 可选接口清单；使用同一次编译的结构，不需要 descriptor 字段 |
| `tables[].index` | 索引路径；索引内源文件相对于索引所在目录 |
| `tables[].mapping` | 可选；存在时使用 Proto 结构导出 |
| `tables[].package/root/tags` | 普通导表包名、合并根类型、标签动作；root 默认 `Table` |
| `tables[].outputs` | 输出类型映射到相对于完整生成目录的路径 |

输出类型：`json`、`go`、`csharp`、`java`、`lua`、`binary`、`proto`、`protobuf`、`protojson`、`jsontype`，以及 `json_dir`、`lua_dir`、`binary_dir`、`protobuf_dir`。`protojson` 要求 mapping；已有 Proto 映射不能再选 `proto` 生成另一套定义。

路径使用 `/`，不使用绝对路径或越出项目的 `..`。输入目录与输出目录分离，索引放在 `Tables/` 中。拒绝符号链接和输出路径重叠。程序维护项目规则；策划只维护数据及文件清单。

## 纯结构生成

```json
{ "version": 1, "schema": { "dir": "Protocols", "go": true } }
```

不要求 RPC、路由或 HTTP/SSE。`Generated/schema` 包含 `schema.pb`、`types.ts`、`data.ts`、`schema.ts`、`wire_schema.json`、`SCHEMA.md`、`bundle.json` 和可选的 `go/`。

标准 Proto imports 内置。TS 类型描述 ProtoJSON，64 位整数是字符串；TS 数据 SDK 暂无 Protobuf 二进制编解码。Go 类型使用官方 Protobuf 运行时。第二版 Unity 导入增加 C# ProtoJSON DTO；C#/C++ 的 Protobuf 二进制类型生成仍未内置。普通表 C#/Java 代码生成继续可用。

## 后端第三方包

构建阶段通过 `project.Load(configPath)` 与 `p.Export(context)` 导出。运行阶段通常只加载产物：

```go
import "github.com/Coder-is/TabForge/protocol"

schema, err := protocol.LoadSchema("Generated/schema/schema.pb")
if err != nil { return err }
message, err := schema.ReadFile("tabforge.demo.config.Tables", "Generated/data/tables.pbb")
if err != nil { return err }
_ = message.ProtoReflect()
```

强类型方式引入自己生成的消息包，使用 `proto.Unmarshal` 或 `protojson.Unmarshal`。动态方式使用 `LoadSchema`、`NewMessage`、`DecodeJSON`、`DecodeBinary`、`ReadFile`。网络服务按需引入 `protocol/httptransport`。

## 客户端读取

```typescript
import { DataSchema } from './Generated/schema/data.ts';
import { wireSchema } from './Generated/schema/types.ts';
import type { MessageTypes } from './Generated/schema/types.ts';

const loader = new DataSchema<MessageTypes>(wireSchema);
const json = await (await fetch('/assets/tables.json')).text();
const tables = loader.decode('tabforge.demo.config.Tables', json);
console.log(tables.items?.[0].reward?.count);
```

可直接复制生成目录，也可在 `sdk/typescript` 目录执行 `npm pack` 创建本地 npm 包，通过 `.tgz` 安装并从 `@tabforge/protocol-runtime/data` 导入。包包含 JS 与声明文件，没有第三方 npm 运行时依赖。此版只构建本地分发包，未发布到 npm registry；Go 新增包提交/发布版本后才能从远端安装。

## 失败与恢复

编译、校验和生成先在临时目录完成，最后才发布完整结果。失败保留旧目录；发布替换失败尝试回滚。目录替换有短暂切换窗口，不是在线服务热更新；构建/编辑器应在命令成功结束后读取。

`.tabforge-export.lock` 阻止并发导出。强制终止留下锁时，确认无导出进程后删除锁。若存在 `Generated.tabforge-backup`，先核对并恢复旧目录，工具不会覆盖遗留备份。

完整填写规则、数据和错误练习见 [完整示例](../examples/complete/README.md)。

## 2026-10-04 验证记录

Go 全量竞态回归通过，覆盖完整导出、失败保留旧目录、回滚、PATH 为空、中文/空格路径、项目自动发现、独立 Go 模块使用生成类型与动态加载。TypeScript 28 项测试通过，包括不带源码的独立 npm 消费端 JS 导入和严格 NodeNext 类型检查；编辑器入口 3 项测试通过。已把这些检查接入现有 CI。

| 交付物 | 本机验证 | 仍需验证 |
| --- | --- | --- |
| macOS ARM64 便携 ZIP | 解压后直接运行，PATH 为空，双击脚本入口通过 | 签名、公证与下载隔离后的首次打开 |
| macOS Intel 便携 ZIP | 交叉编译、压缩包完整性和产物检查通过 | Intel 机器执行 |
| Windows x64/ARM64 便携 ZIP | 交叉编译、压缩包完整性和产物检查通过 | Windows 原生执行与批处理入口 |
| 四平台 VSIX | 结构/平台标识通过；Mac 包内 runner 与内置二进制实际导出通过 | VS Code 编辑器内安装和交互验收 |
| npm `.tgz` | 独立消费端导入、配置读取、大整数精度与声明检查通过 | registry 发布未执行 |

发行包在 `outputs/releases/`，包括 ZIP、VSIX、npm 包和 `SHA256SUMS`。本机解包验证结果保存在该目录的 `validation.json`；这是本地构建产物，不提交到源码。
