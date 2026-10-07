# 文件放入目录后自动导出

0.5.0 的完整模板默认使用 `discover`，策划增加或删除同类型文件后直接导出，不需要更新 Index.csv。旧项目的 `index` 模式继续可用；同一个 tables 任务只能选择其中一种。

程序提前维护 Proto、输入类型、mapping 和目录规则。新增业务结构仍需程序更新这些约定；自动发现负责文件清单，不猜测新业务类型或字段。

```json
{
  "version": 1,
  "schema": { "dir": "Protocols", "go": true },
  "tables": [{
    "discover": {
      "dir": "Tables",
      "types": ["Type.csv"],
      "rules": [
        { "pattern": "Item/**/*.xlsx", "type": "Item" },
        { "pattern": "Item/**/*.csv", "type": "Item" },
        { "pattern": "Settings/*.csv", "type": "Settings" }
      ],
      "exclude": ["Archive/**"]
    },
    "mapping": "Tables/mapping.json",
    "outputs": { "protojson": "data/tables.json", "protobuf": "data/tables.pbb" }
  }]
}
```

程序配置一次，策划只操作 `Tables/Item/` 等约定目录。`dir` 相对于项目，types / pattern / exclude 相对于该目录；路径用 `/`。支持 `*`、`?`、`[]`，单独目录段 `**` 匹配零个或多个目录。匹配区分大小写；工具拒绝会在 Windows 上冲突的大小写重复文件。

每个数据文件必须恰好匹配一个 rule。默认 mode 为 `data`，KV 输入用 `"mode":"kv"`。文件按模式、类型、相对路径稳定排序后编译，同类型文件自动合并，重复索引值照常报错。每个规则至少需要一个输入文件；删除最后一份时会报错并保留旧产物，可留一个仅有列头的空表。

自动忽略 Office 锁文件 `~$*`、隐藏文件/目录、备份 `*~`，以及留作参考的 Index 表。exclude 可跳过归档、CSV 对照等明确不参与导出的输入。目录内未匹配的 CSV/XLSX/XLSM 会报错，不会静默遗漏；其他扩展名不参与导表。符号链接、规则冲突、越界路径都被拒绝。

完整模板的物品文件仍在 Tables 根目录：`Items*.xlsx`、`ItemsExtra*.csv` 自动归入 Item，`Settings*.csv` 归入 Settings；Items.csv 是 XLSX 的对照，不参与导出，Basic 子目录由另一个任务扫描。比如创建 `ItemsExtraEvent.csv`，保持现有列头和唯一 ID，放进 Tables 后直接导出即可。新增物品 XLSX 使用 `ItemsEvent.xlsx` 命名。

导出和只校验都不会创建或修改索引文件；每次扫描当前目录，所以已删除文件不再进入输出。失败、取消、并发锁及资产回滚行为沿用项目工作流。
