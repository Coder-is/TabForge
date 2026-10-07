# 第四部分：发布交付与安装验收

第四版把策划便携项目、VS Code / Unity / Cocos / Godot / Unreal 插件以及四种后端包放进同一个版本目录，完成安装验收后再交付。版本源是仓库根目录的 `release.json`，当前为 **0.5.0**；项目配置仍用 `version: 1`，数据清单仍用 `tabforge.data.v1`。

## 维护者：统一版本

修改 `release.json` 的 `version`，然后在仓库根目录执行：

```bash
go run ./cmd/release -sync
go run ./cmd/release -check
```

接受稳定版本号 `major.minor.patch`，不接受预发布版本。同步范围是五种编辑器插件、npm 包及 lockfile 根包、Python 包和 Maven 包。依赖版本、Creator 的 `package_version`、配置格式不受影响。网络 SDK 的 Unity / Unreal 插件有独立的版本，本次交付集不包含它们。

独立执行 `cmd/package` 或 `cmd/package-backend` 也会先检查版本一致性，发现不同版本立即报错。

## 维护者：构建与验收

构建机需要仓库指定的 Go、Node 24+、Python（带 setuptools / wheel）、Java 17+、Maven。这些是维护者工具，策划收到的便携项目不需要安装它们。首次准备依赖：

```bash
npm ci --prefix sdk/typescript --ignore-scripts
python3 -m pip install setuptools wheel
go run ./cmd/release
```

默认构建 macOS ARM64 / Intel、Windows x64 / ARM64，每个平台包含便携项目、VSIX 和四个引擎插件；再构建 Go 源码 ZIP、npm tgz、Python wheel、Java JAR / POM，共 **29 个包**。

发布入口始终执行两类验收：四种后端包安装到独立消费项目并实际读取数据；客户端包检查压缩结构、插件版本、目标架构与许可证，在当前宿主有匹配工具时，还解包执行工具。Linux 构建机检查四个平台的包结构，不执行 Mac / Windows 工具。

可只构建一个客户端平台；四种后端包仍完整生成：

```bash
go run ./cmd/release -target=darwin-arm64 -out=outputs/releases/0.5.0-mac-arm64
```

`-target` 还接受 `darwin-x64`、`win32-x64`、`win32-arm64`。构建工具不在 PATH 时可指定绝对路径：

```bash
go run ./cmd/release -python=/path/to/python3 -maven=/path/to/mvn -maven-repo=/path/to/maven-cache
```

Java 工具从 PATH / JAVA_HOME 获取。Windows 使用 `-python=python`；Maven、npm 的批处理入口由后端打包器定位真正的 Java / Node 程序，参数中空格和中文保持原样。验收脚本也使用同一个 `-npm` 参数。

默认交付目录为 `outputs/releases/0.5.0`。指定目录必须不存在；重建时选一个新的 `-out`。构建先写同级临时目录，验收通过后移动到交付目录。失败清理临时产物，不覆盖已有交付目录。相同目录的并发构建由 `<out>.lock` 阻止；异常退出遗留锁时，确认构建进程已经退出，再删除锁目录。

## 交付目录与溯源

```text
outputs/releases/0.5.0/
├── tabforge-project-<platform>.zip
├── tabforge-<platform>.vsix
├── tabforge-unity-<platform>.zip
├── tabforge-cocos-<platform>.zip
├── tabforge-godot-<platform>.zip
├── tabforge-unreal-<platform>.zip
├── tabforge-go-0.5.0.zip
├── tabforge-protocol-runtime-0.5.0.tgz
├── tabforge_data-0.5.0-py3-none-any.whl
├── tabforge-data-0.5.0.jar
├── tabforge-data-0.5.0.pom
├── build-info.json
├── validation.json
├── client-validation.json
├── INSTALL.md
├── release.json
└── SHA256SUMS
```

`build-info.json` 记录版本、Git 完整提交号、源码是否有未提交改动和 UTC 构建时间。每个便携二进制的 `-version` 输出同样的信息；有本地修改时提交号带 `-dirty`。交付目录名不能代替实际版本检查。

`release.json` 使用 `tabforge.release.v1`，记录构建信息、平台选择、两个验收报告和每个文件的大小及 SHA-256。`SHA256SUMS` 还覆盖 `release.json` 本身。报告、安装说明与构建信息同样登记，最终清单不包含自身校验值。

设置 `SOURCE_DATE_EPOCH` 可固定元数据中的构建时间，便于复核。同一时间戳不保证 ZIP、npm、Maven 等所有产物字节完全可复现。

## 使用者：核对与安装

拿到完整交付目录后，从工具仓库执行只读核对：

```bash
python3 scripts/verify_release.py --out=/path/to/delivery --integrity-only
```

该检查对比目录、SHA256SUMS 和发布清单，拒绝缺失、重复、额外或被修改的文件。校验值用于发现文件变化；分发渠道的身份与信任由维护者负责。

按接入端选择包：

| 使用者 | 包 | 安装后入口 |
| --- | --- | --- |
| 策划 | 对应平台的 project ZIP | 解压，双击 `TabForgeProject/Tools/TabForge/Export.bat` 或 `Export.command` |
| VS Code | 对应平台的 VSIX | “从 VSIX 安装”，打开含 tabforge.json 的工程，执行导出命令 |
| Unity | 对应平台的 unity ZIP | 解压后从本地 package.json 添加 `com.tabforge.editor` |
| Cocos Creator | 对应平台的 cocos ZIP | 将 `tabforge/` 放进工程的 `extensions/`，启用扩展 |
| Godot | 对应平台的 godot ZIP | 把 `addons/tabforge/` 放进工程，启用插件 |
| Unreal | 对应平台的 unreal ZIP | 将 TabForge/ 放进 Plugins/，构建并启用源码插件 |
| Go 后端 | Go 源码 ZIP | 解压 `tabforge-go/`，通过 go.mod 的 replace 引用 |
| Node / TypeScript | npm tgz | `npm install /path/to/tabforge-protocol-runtime-0.5.0.tgz` |
| Python | wheel | `python -m pip install /path/to/tabforge_data-0.5.0-py3-none-any.whl` |
| Java | JAR 和 POM | 用下面的命令安装到本地 Maven 仓库 |

```bash
mvn install:install-file -Dfile=/path/to/tabforge-data-0.5.0.jar -DpomFile=/path/to/tabforge-data-0.5.0.pom
```

Java 工程依赖 `io.tabforge:tabforge-data:0.5.0`。Go 的本地源包不需要 Git 标签；远端 Go 模块应固定维护者已发布的真实提交或标签。发布命令不创建标签、不推送 Git、不上传 npm / PyPI / Maven 或编辑器市场；`registriesPublished` 为 false。当前本地构建版本不能直接当作公共仓库已发布版本安装。

安装后用同一份完整 Generated 数据目录运行后端读取示例。详细 API 与重新加载行为沿用第三部分；其中 `0.3.0` 和固定提交是第三版历史示例，安装第四版时替换为本次包路径和依赖版本。引擎导入步骤沿用第二部分。若只有单独的 INSTALL.md，可到仓库 `doc/backend-workflow.md`、`doc/editor-workflow.md` 查完整说明。

## 验收记录的范围

`validation.json` 记录四种实际安装消费结果；`client-validation.json` 分别记录所有选中归档的结构检查与当前宿主的执行检查。没有匹配平台时，nativeTarget 为 null，nativeChecks 为空。

本机工具验收覆盖版本身份、导出/导入、只校验不修改产物、失败保留旧数据、PATH 为空和中文/空格路径。它不代替 VS Code、Unity、Creator、Godot 编辑器内的安装与窗口交互，也不证明其他架构或目标设备已执行。双击脚本、下载隔离和 Mac 签名公证需要另外验收。

CI 的 backend-packages 任务在三个系统安装后端包；release-delivery 任务在 macOS 构建和验收本机平台交付目录，并把产物保留为工作流 artifact。远程 CI 是否通过应以实际工作流结果为准。

目录自动发现见 [填写与规则](discovery-workflow.md)，Unreal 入口见 [插件说明](../editors/unreal/README.md)。GitHub 交付和公共渠道发布准备见 [发布流程](publishing.md)。
