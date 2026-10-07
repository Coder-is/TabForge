# Unreal 编辑器与数据插件

目标为 Unreal 5.5+，当前只交付源码与对应 Win/Mac 平台的便携导出工具。引擎编译、编辑器交互和目标设备测试未执行。

将对应平台 ZIP 中的 `TabForge/` 解压到项目 `Plugins/`，启用插件。源码插件需要项目的 Unreal C++ 构建环境；不能把便携导出器无开发环境运行，理解为 UE 源码插件无需编译。

Tools 菜单提供创建完整示例、校验、导出并导入、导入已有 Generated、取消和查看报告。子进程通过管道和 ticker 驱动，不在菜单回调中等待导出。默认源项目为项目目录的 `TabForge/`，也支持根目录的 tabforge.json。导入使用 `-editor=unreal`，生成到 `Content/TabForgeGenerated/`，失败保留已有生成目录。

成功导入后插件把 `TabForgeGenerated` 加入 `DirectoriesToAlwaysStageAsNonUFS`，保存项目打包配置。该目录保存原始 ProtoJSON，运行时使用文件 IO 读取；它不是 uasset，也不会生成 USTRUCT 或 Blueprint 消息类。菜单和打包配置依据 [ToolMenus](https://dev.epicgames.com/documentation/en-us/unreal-engine/API/Developer/ToolMenus/UToolMenus/RegisterStartupCallback?application_version=5.5) 和 [UProjectPackagingSettings](https://dev.epicgames.com/documentation/unreal-engine/API/Developer/DeveloperToolSettings/UProjectPackagingSettings)。

业务模块在 Build.cs 的依赖中加入 `TabForgeData`，然后读取快照：

```cpp
#include "TabForgeDataBundle.h"
#include "Misc/Paths.h"

FString Error;
auto Bundle = FTabForgeDataBundle::Open(
    FPaths::Combine(FPaths::ProjectContentDir(), TEXT("TabForgeGenerated")), Error);
if (!Bundle.IsValid()) { UE_LOG(LogTemp, Error, TEXT("%s"), *Error); return; }
auto Tables = Bundle->Read(TEXT("data/tables.json"), Error);
if (!Tables.IsValid()) { UE_LOG(LogTemp, Error, TEXT("%s"), *Error); return; }
const auto& Items = Tables->GetArrayField(TEXT("items"));
const FString Owner = Items[0]->AsObject()->GetStringField(TEXT("ownerId"));
UE_LOG(LogTemp, Log, TEXT("ownerId=%s"), *Owner);
```

Open 校验全部登记的 ProtoJSON、字段类型、oneof 和大整数；第三个参数可固定预期 schemaHash。Read 返回新 JSON 对象，修改读取结果不会污染其他读取者。重新加载时先创建新 Bundle，成功后替换业务持有的共享指针，失败继续保留旧指针。64 位整数使用字符串，避免 double 精度丢失。

取消只在子进程结束后清理该 PID 的锁；若终止发生在发布切换窗口，已有工具的备份恢复规则仍适用。网络 HTTP/SSE 使用独立的 `sdk/unreal` 插件，数据插件不依赖它。

数据模块复用网络 SDK 的 Wire、JSON syntax 和 SSE/UTF-8 核心头文件；复制件由仓库测试检查一致性。共享头文件修改后应同步到 `Source/TabForgeData/Private/`。
