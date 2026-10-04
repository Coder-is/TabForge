# 第二版：编辑器生成、导入与读取

第二版覆盖 Unity、Cocos Creator 和 Godot，继续使用第一版 `tabforge.json` 配置。各编辑器调用同一个导出内核；允许在编辑器中生成，也允许其他项目生成后只分发 Generated 目录。

## 插件安装

| 编辑器 | 安装方式 | 源文件默认位置 | 专用生成目录 |
| --- | --- | --- | --- |
| Unity 2022.3+ | 解压插件 ZIP，UPM 从磁盘添加 `com.tabforge.editor/package.json` | `TabForge/` | `Assets/TabForgeGenerated/` |
| Creator 3.8 | 解压 `tabforge/` 到 `extensions/`，扩展管理器启用 | `TabForge/` 或根配置 | `assets/resources/tabforge/` |
| Godot 4.5.1+ | 解压到 `addons/tabforge/`，启用插件 | `TabForge/` 或根配置 | `tabforge_generated/` |
| VS Code | 安装对应平台 VSIX | 当前工作区或上级配置 | 配置中的 output |

插件 ZIP 自带对应平台工具。Unity 的 JSON 依赖通过 UPM 安装；其他插件不需要配置开发运行时。Mac 开发包仍未签名、公证；首次下载运行受系统策略约束。

打开插件面板后，先“创建完整示例”，再改 Protocols 和 Tables；“校验”会运行完整生成检查但不发布任何资产；“导出并导入”生成协议类型、数据和加载器，并刷新编辑器资源。已有源项目可以沿用自己的文件列表。

## 独立生成后导入

```bash
# 导出源项目；export.json 增加 data 清单，将文件关联到完整 Proto 消息名。
tabforge -project=/path/to/SourceProject
# 无需源 Excel 或 Proto，仅导入导出目录。
tabforge -import=/path/to/SourceProject/Generated -editor=unity -editor_project=/path/to/Game -report
# cocos/godot 使用同样的入口。
tabforge -project=/path/to/SourceProject -editor=cocos -editor_project=/path/to/Game -report
tabforge -project=/path/to/SourceProject -check -report
```

导入前检查 descriptor 的 schema hash、数据的根类型与 ProtoJSON。只复制 `protojson` 输出及所需运行时，不把 Go、Java 或普通表 C# 产物放进引擎。纯结构项目可以只导入结构和加载器。第一版导出目录没有 data 清单，仍能导入结构，但要重新导出才能导入对应表数据。

导入目录由 `.tabforge-client.json` 标记。已有未标记目录会拒绝覆盖；不要在该目录放手写代码。导入失败保留旧资产，重复导入保留仍存在的 Unity/Cocos `.meta` 和 Godot `.uid`，删除过期资产及其元数据。不要在引擎导入器扫描期间自行读取暂存目录。

取消后，插件会在子进程退出时只清理该 PID 所拥有的导出/导入锁。强制终止若恰好发生在目录发布期间，仍可能留下 `.tabforge-backup`；先检查并恢复该备份，工具不会直接覆盖它。

## Unity 读取

```csharp
using UnityEngine;
using TabForge.Data;
using TabForge.Data.Generated;

var schemaJson = Resources.Load<TextAsset>("TabForge/wire_schema").text;
var json = Resources.Load<TextAsset>("TabForge/data/tables").text;
var loader = new DataSchema(schemaJson);
var tables = loader.Decode<M_tabforge__demo__config__Tables>(json);
Debug.Log(tables.items[0].reward.count); // uint64 是字符串，保持精度。
```

`DataTypes.cs` 来自 Proto 全部消息和枚举，包括未被 RPC 使用的嵌套、递归类型。类名以 M_ 开头，Proto 的点变为双下划线、原下划线转义为 _0；枚举以 E_ 开头。属性用 Proto 字段名，JsonProperty 映射 ProtoJSON 名。nullable 标量保留缺失和显式零的区别；先验证 oneof，再反序列化。DTO 表达 ProtoJSON，不是 Google.Protobuf 二进制类。

## Creator 读取

```typescript
import { JsonAsset, resources } from 'cc';
import { DataSchema } from './resources/tabforge/data';
import { wireSchema } from './resources/tabforge/types';
import type { MessageTypes } from './resources/tabforge/types';

const loader = new DataSchema<MessageTypes>(wireSchema);
resources.load('tabforge/data/tables', JsonAsset, (error, asset) => {
    if (error) { console.error(error); return; }
    const tables = loader.validate('tabforge.demo.config.Tables', asset.json);
    console.log(tables.items?.[0].reward?.count);
});
```

示例脚本位于 assets 根目录；其他目录请调整 import 路径。资源加载后校验，不把大整数转成 JavaScript number。原始文本可用 loader.decode。客户端读取目前使用 ProtoJSON，二进制数据继续供后端使用。

## Godot 读取

```gdscript
const Schema = preload("res://tabforge_generated/schema.gd")
const Loader = preload("res://tabforge_generated/data_schema.gd")

func _ready() -> void:
    var loader = Loader.new(Schema.WIRE_SCHEMA)
    var tables = loader.read_file("tabforge.demo.config.Tables", "res://tabforge_generated/data/tables.json")
    if not loader.error.is_empty():
        push_error(loader.error)
        return
    print(tables.items[0].reward.count)
```

返回字典或数组；失败返回 null，原因在 error 中。缺失字段不会补零，请用 has/get 判断可选字段。打包时包含数据 JSON 文件。

## 诊断与校验

`-report` 在项目或指定游戏根目录写入 `.tabforge-report.json`，格式为 `tabforge.report.v1`；包含成功状态、生成目录、引擎目录、文件清单和 diagnostics。Proto 诊断有文件及行列，表格诊断有文件、工作表及 A1 单元格。旧 CLI 文本日志继续保留。

VS Code 新增“校验项目”和“查看最近报告”，并为 tabforge.json 提供配置补全。错误写入 Problems；Proto 可跳转到源码，Excel 以文件名/工作表/单元格描述定位，不把工作簿当文本打开。其配置补全和诊断使用 [VS Code 官方扩展 API](https://code.visualstudio.com/api/references/contribution-points#contributes.jsonValidation)。

## 构建和验证

```bash
go run ./cmd/package -out=outputs/releases/v2
node --test editors/cocos/runner.test.cjs editors/vscode/extension.test.cjs
DOTNET_BIN=/path/to/dotnet GODOT_BIN=/path/to/godot go test -race ./clientbundle
```

构建为四个平台各生成便携项目 ZIP、VSIX 和三个引擎插件 ZIP。插件 API 依据 [Unity MenuItem](https://docs.unity3d.com/2022.3/Documentation/ScriptReference/MenuItem.html)、[Creator 扩展](https://docs.cocos.com/creator/3.8/manual/en/editor/extension/first.html)、[Godot EditorPlugin](https://docs.godotengine.org/en/4.5/classes/class_editorplugin.html)。

本机已通过三个引擎导入回归、独立 C# 编译和数据读取、真实 Godot 进程调用/数据读取/EditorPlugin headless 注册。Unity 与 Creator 编辑器当前未安装，窗口交互、UPM/扩展管理器安装和 Creator 资源刷新需后续原生验收；headless 通过不代表有窗口交互已验收。Windows/Intel 包为交叉编译，原生执行未验证。

2026-10-04 验证记录：Go 全量竞态回归通过（启用 C# 与 Godot 集成），TypeScript 28 项与编辑器入口 9 项通过；独立 C#、TS 和 Godot 读取覆盖大整数、可选字段及错误拒绝。20 个 ZIP/VSIX 的完整性、CPU 架构、许可和校验和通过；解包后的三个 Mac 插件工具在空 PATH、中文/空格目录下完成初始化、导出、导入、校验及失败保留资产验证。验证报告位于本地发行目录的 validation.json，不提交到源码。
