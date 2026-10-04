# 代码重构记录

日期：2026-10-04。

这次整理覆盖 CLI、表格编译与校验、导出器、文件与缓存工具、HTTP 协议服务，以及 TypeScript、微信、Cocos、Unity、Godot 和 Unreal SDK 的相关实现。

## 整理原则

- 用提前返回表达失败路径，减少嵌套和跳转。
- 对实际重复的逻辑提取小函数，让调用者直接表达业务意图。
- 用明确的数据类型表达文件加载结果和协议事件元数据。
- 按职责拆分长文件，让协议解析、类型校验、网络生命周期分别可读。
- 使用标准库和普通循环处理简单任务。保留现有导出函数和可注入网络接口，作为实现差异的边界。
- 展开挤在一行里的控制流程，方便定位错误和逐步调试。

## 主要变更

| 范围 | 变更 |
| --- | --- |
| CLI | 用返回错误的 `runV3` 组织执行流程；简化输出日志和失败路径；清理永远没有启用的性能分析分支。 |
| 编译与校验 | 简化表格合并和数组拼接；合并重复的数值校验分支；使用普通循环检查类型和收集 KV 类型；将 tag 目标识别移到匹配循环外。 |
| 导出器 | JSON 单文件和目录导出共用行转换，分别保留各自的 tag 过滤动作；所有目录导出通过 `helper.WriteFile` 创建父目录；模板通过 `text/template` 执行；Go 源码维持原来的格式化规则。 |
| 文件与工具 | 文件加载显式返回 `(TableFile, error)`；并发缓存保存明确的结果结构；简化 Excel 列标转换、CSV 编码转换和内存表创建；生产代码中涉及的文件读取使用 `os` / `io`。 |
| 协议服务 | 请求处理放在 `server.go`，SSE 写入与心跳放在 `stream.go`，响应校验与错误编码放在 `response.go`；事件元数据使用结构体。 |
| TypeScript / 微信 / Cocos | Fetch 和回调适配器共用请求准备、JSON 解析和请求 ID 校验；分离普通消息与 Protobuf 内建类型校验；整理密集的控制流程。 |
| Unity | 将严格 JSON 语法检查、SSE 解析和 WireSchema 校验拆到独立源文件，更新核心测试工程的编译清单；整理协议核心与网络适配器格式。 |
| Godot / Unreal | Godot 分离 socket 轮询与响应块处理，统一流式响应判断；展开 Unreal 独立 SSE 核心的 UTF-8 校验和分帧流程。 |

清理了四个 Go 直接依赖：`go-linq`、`protoplus`、`pkg/errors`、`pkg/profile`。生产 Protobuf 导出器统一使用现代 Protobuf API；示例生成代码仍需要的旧 Protobuf 兼容依赖保留在模块中。

## 有意修复的行为

- 枚举数组按每个元素校验，非法元素会报 `UnknownEnumValue`。
- 二进制导出按无符号类型解析 uint16 / uint32 / uint64，可以导出各类型最大值，同时拒绝负数和溢出。
- XLSX 对象重新载入工作簿时替换旧 sheet，避免重复保留之前的表单。
- 目录导出自动创建缺失的父目录。
- Fetch 与回调 SDK 一致拒绝带换行的请求 ID / token、非法 timeout、错误的调用类型，以及不能序列化成 JSON 的请求。

这些修复有对应回归用例；JSON 的单文件与目录 tag 过滤也单独验证。

## 验证结果

- `go test -race -count=1 ./...`：全部通过，包含实际运行的 Node 和 C# HTTP/SSE 联调。C# 测试通过 `DOTNET_BIN` 指定本机已有的 .NET SDK，并将回环地址加入 `NO_PROXY`。
- `npm --prefix sdk/typescript test`：25 项通过。
- `npm --prefix sdk/typescript run check`：通过。
- `npm --prefix sdk/typescript run build:platforms`：通过。
- Godot 4.5.1 headless 单元测试与 Go HTTP/SSE 联调：均为 0 failures。
- C++17 独立 SSE / JSON 核心：`-Wall -Wextra -Werror`、ASan / UBSan 通过。
- 使用重构前的 CLI 和最终 CLI 导出相同 XLSX 示例及协议包：13 种表格导出方式共生成 22 个文件，协议包生成 7 个文件，29 个文件逐字节相同。
- `git diff --check`：通过。

Godot 联调脚本退出时的资源保留提示，在重构前代码上也能复现。Unity、Unreal 和 Cocos Creator 的编辑器构建仍需在安装相应引擎的环境验证；本次完成的是上述核心、适配器及构建资产验证。
