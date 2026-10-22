# GoForge

> 以真实业务场景驱动的 Go 后端学习项目：Gin + GORM + PostgreSQL + Redis + RabbitMQ，
> 从一个能跑的骨架，逐步演进到具备事务、并发控制、缓存、可靠消息与工程化能力的服务。

本项目面向**已有 Node.js / TypeScript / NestJS 后端经验**、希望系统掌握 Go 的开发者。

## 项目定位

业务场景：**本地生活跑腿订单调度**（同城代买、代送）。

它天然包含三个在真实后端里最难处理、也最值得练的问题：

| 业务问题 | 用到的技术 | 练到的 Go 能力 |
|---|---|---|
| 多个骑手同时抢同一单，只能有一个成功 | PostgreSQL 条件更新 / 行锁、事务 | 事务边界、并发安全、错误处理 |
| 订单超时未接单要自动取消并退款 | Redis 过期 + 延迟消息（RabbitMQ TTL + DLX） | Goroutine、context、消费者生命周期 |
| 订单状态变化后要可靠地写结算流水、发通知 | Outbox 事务消息 + 幂等消费 | Channel、优雅关闭、失败重试 |

## 技术栈

| 层次 | 选型 | 说明 |
|---|---|---|
| 语言 | Go 1.27 | 本机 1.27.1 已验证可编译 |
| Web | Gin v1.12 | 社区最主流的 HTTP 框架，中间件模型清晰 |
| ORM | GORM v1.31 + pgx v5 | 用 GORM 但坚持看 SQL，不当黑盒 |
| 主库 | PostgreSQL 16 | 事务隔离、约束、条件更新语义比 MySQL 更适合讲并发 |
| 缓存 | Redis 7 + go-redis v9 | 缓存、原子计数、分布式锁、Lua |
| 消息队列 | RabbitMQ 3.13 + amqp091-go | per-message ack、DLX、TTL 队列，适合讲清"可靠消息" |
| 日志 | zap | 结构化日志 |
| 认证 | golang-jwt v5 | 阶段三接入 |

## 目录结构

```text
GoForge/
├── cmd/server/          # 程序入口：组装依赖、启动 HTTP、处理信号退出
├── internal/
│   ├── config/          # .env / 环境变量加载与启动期校验
│   ├── httpx/           # 统一响应结构、业务错误类型
│   ├── logging/         # zap 初始化
│   ├── middleware/      # request id、访问日志、panic 恢复
│   ├── handler/         # HTTP 层：绑定、校验、状态码
│   ├── router/          # 路由注册（不含具体实现依赖）
│   ├── service/         # 业务规则（阶段三起）
│   ├── repository/      # 数据访问（阶段三起）
│   ├── model/           # 数据库模型（阶段三起）
│   ├── platform/        # PostgreSQL / Redis / RabbitMQ 连接与生命周期
│   └── consumer/        # 异步消费者（阶段五起）
├── deployments/         # docker-compose.yml
├── migrations/          # SQL 迁移（阶段三起）
├── Makefile
└── .env.example
```

依赖方向是单向的：

```text
cmd/server → router → handler → service → repository → model
                       ↘         ↘
                        httpx     platform
```

## 快速开始

```bash
# 1. 准备配置
cp .env.example .env

# 2. 启动 PostgreSQL / Redis / RabbitMQ（需要 Docker）
make infra-up

# 3. 运行服务
make run

# 4. 验证
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
```

预期响应：

```json
{
  "code": "ok",
  "message": "ok",
  "data": {
    "status": "ok",
    "version": "dev",
    "uptime_seconds": 3,
    "checks": { "postgres": "up" }
  },
  "request_id": "8e492795381094e08970d231500f12f8"
}
```

## 常用命令

```bash
make help        # 查看全部命令
make test        # go test -race ./...
make vet         # go vet ./...
make build       # 编译到 bin/goforge
make infra-down  # 停止基础设施（保留数据）
make infra-reset # 停止并清空数据卷
```

## 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /healthz | 存活检查：只说明进程还在，不检查外部依赖 |
| GET | /readyz | 就绪检查：检查 PostgreSQL，不可用时返回 503 |

所有响应统一为：

```json
{ "code": "OK", "message": "success", "data": {}, "request_id": "..." }
```

## 开发进度

- [x] 阶段一：项目设计
- [x] 阶段二：项目初始化（配置、日志、中间件、健康检查、优雅关闭、数据库连通）
- [ ] 阶段三：核心业务（模型、迁移、认证、订单、抢单、事务）
- [ ] 阶段四：Redis 缓存与原子操作
- [ ] 阶段五：RabbitMQ 异步处理与可靠消息
- [ ] 阶段六：工程完善（测试、可观测性、部署）
- [ ] 阶段七：总结与进阶
