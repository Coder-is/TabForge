# 后端接入示例

四种语言共用 `examples/complete/Generated`，运行前用当前工具完整导出。具体包安装方式、可读取格式和快照 API 见 [第三版工作流](../../doc/backend-workflow.md)。

```bash
go run . -project=examples/complete
go run ./examples/complete/Clients/go
# npm 包已在当前项目安装；这里需要 node_modules 可从示例目录向上发现。
node examples/backend/node/client.mjs examples/complete/Generated
# Python wheel 已安装到当前 Python 环境。
python examples/backend/python/client.py examples/complete/Generated
```

Java 使用 Maven 引入包后，在自己的工程中运行 [Client.java](java/Client.java)。不安装 Maven 的本地演示也可以将 TabForge JAR 与 Gson JAR 放入 classpath，编译并运行该文件。

所有输出展示第一件物品名称和 uint64。Go 同时展示强类型二进制读取和动态 ProtoJSON；另外三端读取 ProtoJSON。`cases.json` 是三个 JSON 运行时共用的验收输入，涵盖完整标量、递归结构、内建类型与错误输入。
