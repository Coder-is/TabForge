# GitHub 交付与公共渠道发布准备

## 本次执行的渠道

本次只向 GitHub 推送代码、版本标签和 prerelease；公共包仓库与插件市场只做准备。`scripts/prepare_publish.py` 不读取登录凭据，也不上传文件。

仓库 `release.json` 当前为 0.5.0。从干净提交构建后：

```bash
go run ./cmd/release -require-clean
python3 scripts/prepare_publish.py --out=outputs/releases/0.5.0 --plan=outputs/releases/0.5.0-publish-plan.json
```

脚本核对清单、构建提交、dirty 标记和包版本，输出每个渠道的包名、权限要求与实际命令。计划写在交付目录之外，避免破坏已封存的校验清单。

将同一提交标记为 `v0.5.0` 后推送，会触发 `.github/workflows/release.yml`。工作流核对 tag 与版本，运行自动化回归、构建 29 个安装包、独立消费验收、校验最终清单，并使用 GITHUB_TOKEN 创建 GitHub prerelease 和上传交付文件。只上传成功构建的产物；构建失败不会创建该版本 release。发布前提是仓库已启用 GitHub Actions，且允许该任务写入 contents。

此流程不安装或启动引擎，不执行编辑器 UI 和目标设备实测。Unreal ZIP 是需要引擎构建的源码插件，不能作为已经编译并实测的引擎二进制承诺。当前版本使用 prerelease 标记，完整验收范围见报告及 release notes。

## npm

包名为 `@tabforge/protocol-runtime`，已设置公开发布元数据。发布者先确认拥有 @tabforge scope；使用账号登录、授权 token 或为包配置 Trusted Publisher。首次发布的包名权限必须在对应服务验证，GitHub 仓库权限不代表 npm 权限。

计划中的命令发布已验收的 tgz，使用 `--access public --tag next`。本次不执行 npm publish。CI 后续可按 [npm Trusted Publishing](https://docs.npmjs.com/trusted-publishers/) 配置 OIDC；公开 scope 的规则见 [npm 官方说明](https://docs.npmjs.com/creating-and-publishing-scoped-public-packages/)。

## PyPI

包名为 `tabforge-data`。wheel 已包含说明和项目链接；上传前执行计划中的 `twine check`。账号需要拥有名称权限，或按 [PyPA 官方指南](https://packaging.python.org/en/latest/guides/publishing-package-distribution-releases-using-github-actions-ci-cd-workflows/) 建立 Trusted Publisher。首次验证可在 TestPyPI 使用独立账号和配置；本次不上传。

## Maven Central

坐标为 `io.tabforge:tabforge-data`。POM 补齐 SCM、维护者与许可，`central-release` profile 生成 sources、Javadoc 与 GPG 签名，使用 Central Portal 插件。namespace 所有权、账号 token 与 GPG 私钥由发布者配置；仓库不保存凭据。

账号就绪后，计划中的 `verify` 构建签名产物；`deploy` 上传 Central staging，autoPublish 为 false，需在 Central Portal 审核后发布。profile 不在普通打包时启用。本次已通过 `-Pcentral-release -Dgpg.skip=true verify` 检查 sources、Javadoc 与发布插件配置；没有验证私钥签名或上传 Central。要求与配置见 [Central 官方 Maven 指南](https://central.sonatype.org/publish/publish-portal-maven/)。

## 编辑器市场

VS Code 的 publisher 候选为 `tabforge`，需要在 Marketplace 确认名称归属并配置 VSCE_PAT；计划列出每个平台现有 VSIX 的 `vsce publish --pre-release --packagePath` 命令。按 [VS Code 官方指南](https://code.visualstudio.com/api/working-with-extensions/publishing-extension) 提交，不重新打包成缺少平台工具的通用包。

Unity、Creator、Godot、Unreal 目前通过 GitHub 对应 ZIP 分发。各市场还需自己的发布账号、商品信息、许可和审核材料；这些材料不能由一个通用自动发布命令代替。本次已提供安装、入口、读取示例、版本、许可与验收边界，暂不提交市场审核。
