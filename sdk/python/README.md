# Python 数据包

`tabforge-data` 使用 Python 3.9+，无运行时第三方依赖。通过 wheel 安装并从 `tabforge_data` 引入 `DataBundle`、`DataStore`。运行时只读取完整 Generated 目录，支持 ProtoJSON、字段校验及失败保留快照；64 位整数是字符串。

安装和示例见 [第三版工作流](../../doc/backend-workflow.md)。本包尚未发布到 PyPI。

开发验证：`PYTHONPATH=sdk/python python3 -m unittest discover -s sdk/python/tests -v`。维护者打包使用 setuptools 与 wheel，详见 `cmd/package-backend`。
