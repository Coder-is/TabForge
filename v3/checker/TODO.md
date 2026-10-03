# V3 校验现状与修复记录

本清单按当前实现整理。校验分布于 `compiler`、`checker` 和各格式导出器中，已完成项不再作为待办。

## 已实现

- [x] 同一对象类型中的重复字段定义，包括跨类型表重复（`compiler/tab_type.go`）。
- [x] 字段名是否合法（按 Go 标识符规则，`checker_type.go`）。
- [x] 数据表引用的表头类型、字段和字段类型是否存在（`compiler/header.go`、`compiler/merge.go`）。
- [x] 数据表中的重复非数组列头（`compiler/header.go`）。
- [x] 拆分表的同一数组字段列数是否一致（`entry.go`）。
- [x] 基础类型和数组元素是否能解析为定义类型，包括整数范围（`checker_data.go`）。
- [x] 枚举定义值为空或重复，以及数据中的未知枚举项（`checker_type.go`、`checker_enumvalue.go`）。
- [x] 合并后的索引字段重复值检查；按非空值的实际类型比较（`checker_repeat.go`）。
- [x] 已有 Proto 参数配对，禁止描述文件模式与 `-proto_out` 混用，ProtoJSON 要求已有描述文件（根目录 `entry_v3.go`）。
- [x] 已有 Proto 描述依赖、根消息、目标字段路径和源列名称检查；映射文件拒绝未知配置项（`gen/pbdata/external.go`）。
- [x] 已有 Proto 映射中的重复或重叠字段路径、重复 map 键、单消息目标多行和 oneof 冲突（`gen/pbdata/external.go`）。
- [x] 已有 Proto 数据的数值范围、JSON 格式及目标类型校验（`gen/pbdata/external_value.go`）。

## 本次问题清单：全部完成

- [x] 普通 JSON 的 `uint16` 大值导出：改用无符号解析，合并与分表 JSON 的标量和数组均验证 `32767`、`32768`、`65535` 边界；非法范围仍由编译校验拒绝。
- [x] 全局类型名称冲突：加载前拒绝同名枚举与表头类型，以及与内建类型冲突的定义；所选导出器检查合并根类型与输出类型、辅助类型和成员的冲突。
- [x] 标识名冲突：类型表和 KV 表在插入定义前拒绝重复非空标识名、字段名与其他字段标识名的歧义。跨类型表也检查，标识名与自身字段名相同及多个空标识名仍允许。
- [x] 各输出语言名称约束：Go、C#、Java、Lua、Proto3 导出入口检查实际输出名称、包/命名空间及生成符号的作用域冲突；Lua 分表和生成 Proto 的二进制入口同样检查。已有 Proto 映射不受无关源码名称参数限制。
- [x] 索引按实际类型归一化后检查重复：涵盖有符号/无符号整数、浮点精度和负零、布尔别名、枚举别名；保留跳过空索引规则及字符串原值。拒绝数组、对象和非有限浮点索引，并先报告非法数据类型。

名称规则和兼容性说明见根目录 [README](../../README.md)。本次专项回归用例位于 [validation_test.go](../tests/validation_test.go)，已有数组分隔测试不再将数组声明为索引；CLI 用例还验证已有 Proto 导出忽略源码名称参数。

## 回归验证

在仓库根目录执行：

```bash
go test -race ./...
```

类型和数据校验用例在 `v3/tests`，已有 Proto 映射用例在 `v3/gen/pbdata/external_test.go`，CLI 兼容性和导出一致性用例在根目录测试文件中。缓存与并发加载用例分别位于 `util/cache_test.go` 和 `v3/helper/fileloader_test.go`。

V2 导出和 V2→V3 迁移工具已移除，本清单仅跟踪 V3。
