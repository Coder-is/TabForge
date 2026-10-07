# TabForge 文档导航

| 需要做什么 | 阅读入口 |
| --- | --- |
| 策划双击导出、纯 Proto 结构生成、编辑器/后端包接入 | [第一版项目工作流](project-workflow.md)、[完整示例](../examples/complete/README.md) |
| Unity/Cocos/Godot 插件，独立协议包导入和运行时读取 | [第二版编辑器工作流](editor-workflow.md) |
| Go、Node、Java、Python 安装、读取、重新加载与排错 | [第三部分：后端第三方包接入](backend-workflow.md)、[四端示例](../examples/backend/README.md) |
| 同步交付版本、一键构建发布包、核对清单和安装验收 | [第四部分：发布交付与安装验收](release-workflow.md) |
| 了解项目、编译工具、导出 Excel/CSV 配置 | [项目首页与 V3 教程](../README.md) |
| 从 Proto 建立前后端协议并接入客户端 | [前后端接入指南](protocol-integration.md) |
| 运行普通 JSON 与 SSE 演示 | [协议示例](../examples/protocol/README.md) |
| 理解包络、ProtoJSON、事件与兼容性规则 | [统一协议架构](unified-protocol.md) |
| 配置认证、CORS、超时、限流边界与升级 | [生产部署说明](production.md) |
| 构建平台验收资产、执行测试 | [验收工程入口](../examples/platforms/README.md)、[平台验收矩阵与步骤](platform-validation.md) |
| 定位源码、扩展导出器、运行开发检查 | [开发维护指南](development.md) |
| 查看本次整理范围、行为修复与验证结果 | [2026-10-04 代码重构记录](refactoring.md) |

客户端文档：[TypeScript](../sdk/typescript/README.md)、[微信](../sdk/wechat/README.md)、[Cocos](../sdk/cocos/README.md)、[Unity](../sdk/unity/README.md)、[Unreal](../sdk/unreal/README.md)、[Godot](../sdk/godot/README.md)。

协议生成的接口、消息字段与事件清单见示例 [PROTOCOL.md](../examples/protocol/generated/PROTOCOL.md)。该文件由工具生成，修改源 Proto/清单后重新生成，不直接编辑。
