# paro-go-backroom

ParoCube 的 Go 公共库。从 Member 中提取已实际使用的通用实现；Member 直接依赖本库，不保留第二份实现。

| 包 | 职责 | 调用方负责 |
| --- | --- | --- |
| `logging` | 创建 JSON/text 结构化日志器 | 服务名、日志设置与输出目标 |
| `telemetry` | 本地追踪、W3C 传播和受控 OTLP 导出 | 静态服务/范围名称、可信路由模板、启动和关闭 |
| `pagination` | 单页数量边界及时间/UUID 游标编码 | 业务筛选、排序、查询和 API 错误映射 |
| `migration` | PostgreSQL Goose 执行与只读有效版本检查 | 提供迁移文件、连接、执行许可及回滚策略 |
| `pgtest` | 为真实数据库测试创建独立 schema 并清理 | 提供专用测试数据库，通过 `TEST_DATABASE_URL` 注入 |

会员、OAuth、角色权限、通知、业务审计、业务 SQL 和应用配置仍属于各服务。当前实时 RBAC 判定不因提取公共库而变成权限缓存。Admin 的 React/TypeScript 共享逻辑仍在 Admin 内部维护。

## 使用与版本

公开仓库为 [parocube/paro-go-backroom](https://github.com/parocube/paro-go-backroom)，模块路径为 `github.com/parocube/paro-go-backroom`，使用 Go 1.26.0 和工具链 1.26.6。首次发布版本为 `v0.1.0`；初始化基线的 `0.0.0` 不发布 Tag，也不能作为消费者依赖。

根目录 [VERSION](VERSION) 是唯一的发布版本来源。任务分支通过 PR 合入 `release/X.Y`，通过质量和版本检查后生成不可变附注 Tag `vX.Y.Z`；每个发布 PR 递增补丁位，完成的发布分支再通过 PR 合入 `main`。已发布的 Tag 不移动、不覆盖，修复必须进入下一补丁版本。库以 Go module 源码交付，不包含服务部署。

消费者使用 `go get github.com/parocube/paro-go-backroom@v0.1.0` 固定版本，无需私有仓库凭据或同级源码目录。协同开发可以在本机使用未提交的 Go workspace，不在消费者 `go.mod` 中提交相对 `replace`。

日志器通过 `logging.New(serviceName, config)` 接收调用方标识。追踪导出通过 `telemetry.NewWithConfig(ctx, telemetry.Service{Name: serviceName, ScopeName: scopeName}, config)` 接收静态元数据，公共库不硬编码具体应用名；配置与数据出口约束见 [追踪说明](telemetry/README.md)。

分页保留原有默认 20、最大 100 的边界和时间/UUID 游标字节格式。Admin 的另一种版本化排序游标仍归 Member，不因字段相似而混用格式。迁移校验只执行 SELECT，不创建版本表或修改 Schema；有效迁移集合不一致时失败，不能用它证明结构未被人工修改。

## 验证

```bash
make verify
```

该入口执行版本与格式检查、`go vet`、全部单元测试及 `-race`、构建和发布脚本回归。`make vuln` 执行固定版本的漏洞扫描。已有测试全部随实现迁移；迁移执行和只读检查使用真实 PostgreSQL。

有安全注入的专用 `TEST_DATABASE_URL` 时，运行：

```bash
make test-integration
```

集成测试创建随机 schema，完成后自动清理。不得指向生产或共享业务数据库；缺少数据库配置会失败，不会跳过测试或改用 SQL 模拟。

从 Member 运行 `make test-integration` 时，其临时 PostgreSQL/Redis 环境会依次验证所引用版本的 Backroom 和 Member，并统一清理。`make test-browser-integration` 进一步验证真实 Admin 生产构建与 Member 的会话链路。Backroom 的 PR 质量检查独立启动 PostgreSQL，验证模块文件、完整检查、真实数据库行为和可达依赖漏洞；不访问生产数据库。
