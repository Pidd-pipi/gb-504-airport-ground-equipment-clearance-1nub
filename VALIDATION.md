# gb-504 验证记录

验证日期：2026-08-22

## 提示词逐项核验

- 领域边界：项目仅实现机场地面设备、航班周转、检查证据、安全放行与审计；未实现售票、预约、订单或通用工单。
- 核心实体：`Turnaround`、`GroundUnit`、`SafetyCheck`、`ClearanceDecision` 均独立贯穿 model、repository、service、handler、router、前端 API/store/type/page。
- 核心页面：`/turnarounds`、`/ground-units`、`/checks`、`/clearance`、`/audit` 均可访问并加载真实 API 数据。
- 跨页组件：`RiskBadge` 实际出现在周转看板和检查工作台；`ClearancePanel` 实际出现在检查工作台和放行审核页。
- 共享能力：`StatusBadge`、`EvidenceList`、`ConfirmDialog` 已复用；`useAuth`、`usePagination` 及格式化、请求工具均独立存放。
- 横切能力：JWT、后端 RBAC、前端路由守卫与按钮权限同步生效；请求追踪、统一错误处理、Redis 限流及写操作审计均已接入。
- 枚举：`UnitState` 为 `available / inspection / blocked / retired`，`ClearanceState` 为 `pending / cleared / restricted / revoked`，前后端取值一致。
- 结构：前端包含 `api/stores/types/components/common/hooks/pages/router/utils`；后端包含 `model/dto/repository/service/handler/router/middleware/constants/util`。
- 规模：38 个非测试 Go 文件，共 2775 行；满足 26-38 个 Go 文件、2700-3900 行及至少 20 个功能 Go 文件要求。
- 部署：Compose 顶层名称、端口 18504/19504、PostgreSQL、Redis、健康检查、命名卷、健康依赖、Nginx `/api` 代理和真实 `/healthz` 均已验证。

## 构建与静态检查

以下命令均以退出码 0 完成：

```text
cd backend && go test ./...
cd backend && go vet ./...
cd backend && go build ./...
cd frontend && npm run build
docker compose config --quiet
```

Compose 启动后四个服务均正常，其中后端、PostgreSQL、Redis 均达到 healthy；`GET /healthz` 返回 `status=ok`。

## API 业务链验证

- 管理员登录成功，并完成设备创建、周转创建、检查通过、readiness 查询、完全放行与审计查询。
- 实测业务数据：设备 `#5`、周转 `#3`、检查 `#4`，readiness 为 `true`，最终状态为 `cleared`。
- 普通地勤角色调用写接口返回 403。
- 放行审计同时保存前态、后态、决定依据、证据和 request id。

## 内置 Browser 验证

全程仅使用 Codex 内置 Browser，未使用外部 Chrome。

- 管理员登录后逐页检查五个核心页面，数据加载、筛选入口、状态徽标和导航均正常。
- 在检查工作台选择 `WTR-SEAL`，提交证据 `wtr-seal-ui-20260822.jpg` 和检查说明，经 `ConfirmDialog` 确认后，待检查数从 2 降为 1、已通过数从 2 升为 3。
- 在放行页对 `MU2458 / B06` 形成 `cleared` 决定，列表待决定数从 2 降为 1、已放行数从 1 升为 2，决定依据和证据同步显示。
- 对仍有关键检查未完成的 `CA1831 / A12` 尝试完全放行，页面收到 `all safety checks must be reviewed before clearance`，状态保持 `pending`，业务红线有效。
- 审计页展开最新 `CLEARANCE_TRANSITION`，确认 `previous_state=pending`、`state=cleared`、reason、evidence 与 request id 完整。
- 使用只读地勤账号登录后，“建立周转”、状态写按钮和审计导航均不显示；直接访问 `/audit` 被路由守卫重定向到 `/turnarounds`。
- 页面截图目视无布局重叠，浏览器 console error 记录为 `[]`。

## 收尾状态

- 已执行 `docker compose down -v --remove-orphans`。
- 以 Compose project label 复查，遗留容器和命名卷均为 0。
- 已初始化独立 Git 仓库，分支为 `main`；按任务约定未创建提交。

## 已知约束

项目按提示词固定使用 Angular 17。官方 npm registry 的安全审计报告包含 Angular 17 及其构建工具链的已知漏洞，自动修复要求跨到 Angular 22，属于破坏性升级并会违反技术版本要求，因此本轮未执行 `npm audit fix --force`。生产部署前应单独规划 Angular 主版本升级。

---

# 撤销恢复流程验证记录（2026-09-25）

本轮补齐设备故障撤销后的恢复流程，以下均在真实 PostgreSQL 16.9 + Redis 7.2.5 + 后端服务上通过 API 验证。

## 业务链验证

- 设备 `TUG-100` 锁定后，关联周转的 `cleared` 决定在同一事务内自动撤销为 `revoked`，周转状态由 `decisioned` 回到 `checking`，撤销原因写入决定与审计（`turnaround_status=checking`）。
- 撤销后立即重新决定被拒绝：`recovery requires at least one recheck after revocation`。
- 检查员为故障设备新增复查，服务端自动标记 `kind=recheck`；复查待处理时重新决定被拒绝：`all safety checks must be reviewed before clearance`。
- 最新复查不通过时重新决定被拒绝：`failed rechecks prevent re-clearance`；再次复查通过后旧失败复查被取代，不再阻断。
- 复查通过但设备仍 `blocked` 时，完全放行与限制放行均被拒绝：`all assigned ground units must be available before clearance`。
- 设备恢复 `available`（必须填写恢复说明）后重新决定成功：`revoked -> cleared`，周转进入 `decisioned` 并可正常 `completed`。
- 地勤只读账号：`GET /checks`、`GET /clearance`、`GET /turnarounds/:id/readiness` 返回 200；`POST /checks`、`POST /clearance`、`PATCH /checks/:id/review` 均返回 403。
- readiness 读模型与决定门槛一致：撤销后未建复查、待处理复查、最新复查失败、设备未恢复均出现在 `blockers` 中；被取代的失败复查自动移除。
- 空库种子数据包含 HU7632 撤销恢复中的周转（撤销原因、blocked 设备、待处理复查），完整恢复链路在种子数据上实测通过。

## 构建与静态检查

以下命令均以退出码 0 完成：

```text
cd backend && go test ./... && go vet ./... && go build ./...
cd frontend && npm ci && npm run build
```

新增纯函数测试覆盖 `revoked -> cleared/restricted` 迁移与 `validateRecoveryChecks` 红线（无复查、复查失败、撤销前复查不计入、复查通过放行）。
