# telemetry

本包由项目负责人维护，提供业务无关的 OpenTelemetry 追踪。`New` 保留本地 SDK 与 W3C 传播的原有行为；应用启动时显式调用 `LoadTraceConfig`，再把结果、根 context 和静态 `Service{Name, ScopeName}` 交给 `NewWithConfig`。公共库不硬编码服务名或导出范围名。这里不创建 Collector、不安装监控平台，也不加载业务数据。

配置使用下列标准 OTEL 环境变量。`OTEL_TRACES_EXPORTER` 未设置或为 `none` 时完全不构造导出器，单独配置端点不会打开出口。只有 `otlp` 会启用 HTTP/protobuf，并要求显式配置目的地。traces 专用配置优先于同名通用配置；通用端点追加 `/v1/traces`，专用端点使用自身路径。超时采用标准毫秒整数，范围为 1 至 60000，默认 10000。

启用时也会在 SDK 构造前校验批处理环境变量：`OTEL_BSP_MAX_QUEUE_SIZE` 为 1 至 65536，`OTEL_BSP_MAX_EXPORT_BATCH_SIZE` 为 1 至 4096，显式批次不能超过有效队列容量；`OTEL_BSP_SCHEDULE_DELAY` 与 `OTEL_BSP_EXPORT_TIMEOUT` 均为 1 至 60000 毫秒。不接受空白、溢出或非整数值，错误只返回变量名和规则。未配置时保留 SDK 队列与批次默认值；最终每批发送时限由前述 OTLP 超时统一控制，`OTEL_BSP_EXPORT_TIMEOUT` 即使被覆盖也须先通过校验。

远端使用系统信任库验证的 HTTPS；HTTP 只允许 `localhost` 或回环 IP。端点不能包含用户信息、查询或片段；认证头由部署环境注入，禁止记录整个配置。首批不支持自定义 CA、mTLS 或 `INSECURE` 开关，配置这些变量会明确失败。客户端不读取代理环境变量，不跟随重定向，不自动重试。

导出前只保留服务名、关联 ID、时间、Span 类型、状态码，以及允许的 HTTP 方法、最终状态和 `http.route`。路由模板唯一来源是 API 的 `chi.RoutePattern`，不得以原始请求路径补值。事件、链接、tracestate、任意名称、错误描述、原始 URL、查询、请求头、客户端地址和个人信息都不发送。

SDK 批处理队列避免导出阻塞业务。Collector 失败时丢弃该批次，并由 SDK 输出已脱敏的失败或超时信号；队列满时采用 SDK 的丢弃策略。`ForceFlush` 返回安全错误；导出失败且 context 已结束时保留取消或超时类别，不暴露 SDK 的自定义原因。应用关闭时调用 `Shutdown` 刷新排队数据，刷新失败仍输出安全信号，关闭等待另受 `SHUTDOWN_TIMEOUT` 限制；运行错误与关闭错误由应用保留。未新增持久队列或可靠投递承诺。

测试只向临时本地接收端发送合成 span，覆盖默认关闭、错误配置、路径和头优先级、实际 protobuf 载荷、字段裁剪、TLS、重定向、超时、取消和关闭刷新。应用层另以真实路由验证动态路径和查询不会进入导出数据。
