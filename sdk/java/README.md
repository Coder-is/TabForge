# Java 数据包

`io.tabforge:tabforge-data:0.3.0` 目标 Java 17+，依赖 Gson 2.13.2。`DataBundle.open` 校验完整 Generated 包，再返回可并发读取的 ProtoJSON 快照；`DataStore.reload` 失败保留旧数据。64 位整数以 JSON 字符串保留。

`mvn -f sdk/java/pom.xml install` 构建并安装本地 Maven 包，也可安装维护者提供的 JAR/POM。包尚未发布到 Maven Central。安装、动态读取及格式限制见 [第三版工作流](../../doc/backend-workflow.md)。

`BackendSmoke` 用独立 main 运行共享结构用例及快照测试；Maven 编译它，CI 和分发包验证器实际执行它，不能把没有运行测试的 Maven BUILD SUCCESS 当成验收完成。
