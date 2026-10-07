# Java 数据包

`io.tabforge:tabforge-data:0.5.0` 目标 Java 17+，依赖 Gson 2.13.2。`DataBundle.open` 校验完整 Generated 包，再返回可并发读取的 ProtoJSON 快照；`DataStore.reload` 失败保留旧数据。64 位整数以 JSON 字符串保留。

`mvn -f sdk/java/pom.xml install` 构建并安装本地 Maven 包，也可安装维护者提供的 JAR/POM。包尚未发布到 Maven Central。安装、动态读取及格式限制见 [第三版工作流](../../doc/backend-workflow.md)。

`BackendSmoke` 用独立 main 运行共享结构用例及快照测试；Maven 编译它，CI 和分发包验证器实际执行它，不能把没有运行测试的 Maven BUILD SUCCESS 当成验收完成。

Windows Java 17 的命令行启动器会按 ANSI 代码页转换参数。消费验收在中文临时目录中使用相对路径启动和读取，不要求修改系统区域设置；业务代码可从 UTF-8 配置读取路径后交给 `Path` / `DataBundle.open`。启动器行为见 [Temurin 17.0.17 官方发行说明](https://docs.redhat.com/en/documentation/red_hat_build_of_openjdk/17/pdf/release_notes_for_eclipse_temurin_17.0.17/Red_Hat_build_of_OpenJDK-17-Release_notes_for_Eclipse_Temurin_17.0.17-en-US.pdf)。
