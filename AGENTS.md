# Backroom 开发约束

遵循 [通用编码规范](../paro-docs/standards/coding-standard.md)、[Go 技术规范](../paro-docs/standards/technology/go-standard.md) 和 [AI 执行标准](../paro-docs/standards/ai-execution-standard.md)。

- 这里只接受有现有调用方的业务无关 Go 能力；不能导入 Member 或其他应用的 `internal` 包。
- 不包含业务模型、权限规则、业务错误码、应用环境配置、密钥或业务 SQL；错误由调用服务映射。
- 提取实现时同步迁移直接测试，并修改消费者直接引用；不保留重复实现或转发壳。
- 沿用已验证依赖版本，优先直接函数和明确结构，不建立通用业务框架。
- 公共 API 使用浅显中文注释说明约束、失败方式及生命周期；公共包边界与验证入口见 [README](README.md)。
- 根目录 `VERSION` 是发布版本的唯一来源。通过任务 PR 合入 `release/X.Y` 后，工作流创建不可变附注 Tag `vX.Y.Z`；库不生成服务二进制或部署制品。
- 本库是公开仓库，禁止提交真实密钥、凭据、个人数据或业务配置。消费者必须固定已发布版本，不能把本地 `replace` 当成正式依赖。
- 单元、并发和静态检查运行 `make verify`；迁移行为必须运行真实 PostgreSQL 集成测试。
