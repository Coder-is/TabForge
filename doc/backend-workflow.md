# 第三版：后端第三方包接入

第三版覆盖 Go、Node.js/TypeScript、Java 和 Python。策划或编辑器仍用 `tabforge.json` 导出；后端复制完整 Generated 目录，安装运行时包，按数据路径读取。运行时不需要 Excel、Proto 源码、protoc 或 TabForge 命令行。

## 生成与分发

```bash
go run . -project=examples/complete
```

新增 `Generated/data_manifest.json`：记录结构身份、描述文件、wire schema，以及每个已映射数据文件的路径、根消息、编码和 SHA-256。合并 ProtoJSON、合并 Protobuf 和按表 Protobuf 都登记；普通 V3 JSON、`.bin` 不登记到 Proto 数据清单。纯结构项目也生成清单，data 为 `[]`。

将完整 Generated 目录分发到后端。第一、二版产物需要用新工具重新导出，旧 `export.json` 格式和编辑器导入仍可用。只改变数据不会改变 schemaHash，文件校验值会改变；升级结构需要同步生成代码及全部产物。

| 端 | 包入口 | 数据返回 | 最低目标 |
| --- | --- | --- | --- |
| Go | `github.com/Coder-is/TabForge/databundle` | 动态消息、生成消息；ProtoJSON / Protobuf | Go 1.26.6，项目工具链 1.26.8 |
| Node.js / TS | `@tabforge/protocol-runtime/node` | ProtoJSON 对象，TS 可绑定 MessageTypes | Node 24+；仅 ESM |
| Python | `tabforge-data` / `tabforge_data` | dict / list / 基础类型 | Python 3.9+；无运行时依赖 |
| Java | `io.tabforge:tabforge-data:0.3.0` | Gson JsonElement / JsonObject | Java 17+；Gson 2.13.2 |

Node、Java、Python 对清单中的二进制文件做完整性检查，但读取时要求选择 `.json` 数据。它们校验工具生成的规范 ProtoJSON；64 位整数始终是字符串。Go 使用官方 protobuf 解码，支持官方 ProtoJSON 输入形式及二进制。Java / Python 当前没有生成业务消息类或二进制编解码；动态结构来自同一 wire schema。

## Go 接入

代码在仓库中，可用本地 `replace github.com/Coder-is/TabForge => /path/to/tabtoy` 引入。维护者打包提供的 Go 源码 ZIP 只含运行时模块，解压后也能作为 replace 目标。需要构建阶段 `project.Load` / `Export` 时，使用完整仓库。

```go
import (
    "github.com/Coder-is/TabForge/databundle"
    configpb "your.module/Generated/schema/go"
)

bundle, err := databundle.Open("Generated", "")
if err != nil { return err }
var tables configpb.Tables
if err := bundle.ReadInto("data/tables.pbb", &tables); err != nil { return err }
fmt.Println(tables.Items[0].OwnerId)

// 动态消息不要求引入生成类型，也不要求手动填写根消息名。
message, err := bundle.Read("data/tables.json")
if err != nil { return err }
_ = message.ProtoReflect()
```

开启 `schema.go`，并把业务 Proto 的 `go_package` 设置为自己项目的模块路径。`ReadInto` 对比生成类型及依赖的描述，拒绝旧代码；失败不改写目标消息。`OpenFS(embedFS, "Generated", expectedHash)` 支持把完整产物嵌入服务；`DecodeJSON` / `DecodeBinary` 可校验结构中未被表格或 RPC 引用的消息。

## Node.js / TypeScript 接入

```bash
npm install /path/to/tabforge-protocol-runtime-0.3.0.tgz
```

```typescript
import { DataBundle } from '@tabforge/protocol-runtime/node';
import type { MessageTypes } from './Generated/schema/types.js';

const bundle = await DataBundle.open<MessageTypes>('Generated');
const tables = bundle.read('data/tables.json', 'tabforge.demo.config.Tables');
console.log(tables.items?.[0].ownerId); // 字符串，保留 uint64 精度
```

TypeScript 将生成的 types.ts 编译为 types.js；这里只使用类型，不需要执行生成的 TypeScript。普通 JS 可直接 `bundle.read('data/tables.json')`。`/node` 独立使用 Node 文件系统，原 `/data` 继续供浏览器与编辑器使用。

## Python 接入

```bash
python -m pip install /path/to/tabforge_data-0.3.0-py3-none-any.whl
```

```python
from tabforge_data import DataBundle

bundle = DataBundle.open("Generated")
tables = bundle.read("data/tables.json")
print(tables["items"][0]["ownerId"])
```

默认字段可能省略；用 `dict.get` 读取可选字段。需要数字运算时显式 `int(value)`，Python 的整数可精确表示 uint64。`decode(message, text)` 可校验任意已生成的消息结构。

## Java 接入

维护者提供 JAR 与 POM，先安装到本地 Maven 仓库：

```bash
mvn install:install-file -Dfile=/path/to/tabforge-data-0.3.0.jar -DpomFile=/path/to/tabforge-data-0.3.0.pom
```

业务 pom.xml 添加依赖：

```xml
<dependency>
  <groupId>io.tabforge</groupId>
  <artifactId>tabforge-data</artifactId>
  <version>0.3.0</version>
</dependency>
```

```java
import io.tabforge.data.DataBundle;
import java.nio.file.Path;

var bundle = DataBundle.open(Path.of("Generated"));
var tables = bundle.read("data/tables.json").getAsJsonObject();
var item = tables.getAsJsonArray("items").get(0).getAsJsonObject();
System.out.println(item.get("ownerId").getAsString());
```

使用 `BigInteger` 处理 uint64 的数值运算，避免 `getAsLong` 溢出。Gson 由 Maven 解析；本项目使用 [Gson 严格 JSON 模式](https://google.github.io/gson/Troubleshooting.html)，并在读取时额外拒绝重复字段。

## 加载、校验与重新加载

所有入口先读取并校验整份清单及所有文件校验值，再校验全部 ProtoJSON。未知字段、oneof 冲突、错误类型、数值溢出、重复 JSON 键、错误路径、重复路径和文件缺失会使加载失败。Go 还独立重算描述的 schemaHash，并检查 wire schema 与描述一致。其他三端使用清单给出的 schemaHash 与文件校验值，不解析二进制描述。

每端可传 expectedHash 固定期望的结构身份；默认空字符串允许加载新的结构。校验值用于发现传输损坏或混合产物，不代替分发渠道的身份验证。

每次 `read` 返回独立数据；修改返回结果不会改动快照。Go `Store`、其他端 `DataStore` 都在新包完整加载成功后替换快照，失败保留旧快照；首个加载前 snapshot 为空。每个请求先取一次 snapshot，在请求期间使用同一个对象。并发 reload 的完成顺序决定最终快照；部署端应串行发布版本。目录替换过程中可能加载失败，等待导出完成后重试即可，正在使用的快照不依赖磁盘文件。

## 本地打包与验证

维护者需要 Go、Node/npm、Python（setuptools + wheel）、JDK 17 和 Maven。第三方包接入项目只需要自己的语言运行时及依赖。

```bash
go run ./cmd/package-backend -out=outputs/releases/v3
```

生成 Go 运行时源码 ZIP、npm tgz、Python wheel、Java JAR/POM 与 SHA256SUMS。支持 `-maven`、`-python`、`-npm` 指定工具及 `-maven-repo` 指定依赖缓存。未发布 registry、Maven Central、PyPI，也未创建 Git 标签。

四端的可运行示例见 [后端示例](../examples/backend/README.md)。Node、Python、Java 共用 30 个规范结构用例；Go 验证动态 / 强类型、旧描述拒绝、embed 文件系统、并发读取和失败保持快照。CI 增加实际分发包消费验证。Windows 的三语言入口和 Go 竞态测试由 CI 覆盖，本机验证环境为 macOS ARM64；CI 配置本身不代表远端运行已完成。

2026-10-04 本地验证：完整 Go 竞态回归通过，包含 C# / Godot 读取链路；TS 严格检查与 30 项测试通过。Go 源码 ZIP、npm tgz、Python wheel、Java JAR 都在独立消费工程中安装并读取成功。20 个便携 / 编辑器归档及全部 25 个交付文件的校验值通过；解包后的三种编辑器内置 Mac ARM64 工具在空 PATH、中文与空格路径下完成生成、导入、校验及失败保持旧资产。记录在 `outputs/releases/v3/validation.json` 与 `client-validation.json`。

Windows / Intel 归档完成交叉编译及架构检查，尚未在对应系统实际执行；Unity、Creator、VS Code 的 UI 安装和按钮交互仍待验证。Mac 便携工具为未签名开发包。
