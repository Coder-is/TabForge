# Python 数据包

`tabforge-data` 使用 Python 3.9+，无运行时第三方依赖。通过 wheel 安装并从 `tabforge_data` 引入 `DataBundle`、`DataStore`。运行时只读取完整 Generated 目录，支持 ProtoJSON、字段校验及失败保留快照。输入规则与 Go protojson 一致（如整数接受 `1.0`、`1e2` 与数字字符串，`null` 视为未设置），读取结果统一为规范 ProtoJSON：64 位整数是十进制字符串，bytes 为带填充的标准 Base64。每个 JSON 文件的数据包预算为深度 64、10,000,000 个 JSON 值，与其他运行时一致（网络消息为 100,000）。

安装和示例见 [第三版工作流](../../doc/backend-workflow.md)。本包尚未发布到 PyPI。

开发验证：`PYTHONPATH=sdk/python python3 -m unittest discover -s sdk/python/tests -v`。维护者打包使用 setuptools 与 wheel，详见 `cmd/package-backend`。
