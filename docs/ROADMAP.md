# OpsOne 模块路线图

OpsOne 的目标是把主机与资产、运维执行、容器、监控告警、安全合规、组织权限收敛到一个工作台里，面向中小规模自建环境。本文件是模块清单与实现状态的单一来源。

菜单与权限位已全部就位（`server/internal/db/db.go` 的 `Seed`），未实现的模块指向 `/placeholder/index` 占位页。菜单 ID 按模块分段，新增子项在本段内追加即可。

状态说明：`[x]` 已实现可用 · `[~]` 部分实现 · `[ ]` 仅有菜单占位


## 工作台（ID 10-19）

- [x] 平台总览 `/dashboard/overview` — 主机总数/在线离线、环境分布、最近执行
- [x] 个人工作台 `/dashboard/personal` — 待办（未读消息、未恢复告警）、我的主机与定时任务、我最近的执行与会话
- [x] 我的资源 `/dashboard/my-resources` — 按数据范围过滤的主机统计、环境与部门分布、在线率、离线优先列表
- [x] 我的活动 `/dashboard/my-activity` — 我的操作流水、我的终端会话、我发起的批量执行


## 资产管理（ID 100-199）

- [x] 主机资产 `/asset/host` — CRUD、环境/标签/状态过滤、连通性探测、跳板机关联
- [x] 数据库资产 `/asset/database` — 实例纳管（MySQL/PG/Redis/Mongo）、按类型带默认端口、TCP 端口探测
- [ ] 云账号 `/asset/cloud` — 云厂商 AK 托管与资源同步
- [x] 标签管理 `/asset/tag` — 标签字典、分类与配色、主机/数据库用量统计，表单内可选可新建
- [x] 固定资产 `/asset/fixed` — 设备台账、SN/型号/位置/使用人、关联主机、采购与保修、到保提醒与原值合计

- [ ] 资产盘点 `/asset/inventory` — 盘点批次、盘亏盘盈
- [ ] 采购记录 `/asset/purchase` — 采购单与入库状态

## 运维执行（ID 200-299）

- [x] Web 终端 `/execute/terminal` — SSH PTY、窗口自适应、跳板机隧道、会话录像、命令拦截
- [x] 批量执行 `/execute/batch` — 并发下发（上限 10）、单机超时、生产二次确认、执行历史
- [x] 会话审计 `/execute/session` — 会话流水、命令明细、录像回放（倍速 + 命令定位）
- [x] 文件管理 `/execute/file` — SFTP 目录浏览、上传、下载、新建目录、重命名、删除空目录，动作全留痕（含连接失败的尝试）
- [x] 定时任务 `/execute/scheduler` — 标准五段 cron、多主机并发下发、启停与立即执行、运行记录复用批量执行历史
- [ ] 构建发布 `/execute/build` — 对接 Jenkins 触发与状态回显


## 容器平台（ID 300-399）

- [ ] 集群接入 `/kubernetes/source` — 多集群 kubeconfig 纳管、健康检查
- [ ] 工作负载 `/kubernetes/workload` — Deployment/StatefulSet/Pod 列表与事件
- [ ] 资源管理 `/kubernetes/resource` — YAML 编辑器、资源创建与保存
- [ ] 服务转发 `/kubernetes/forward` — port-forward 通道管理

## 监控告警（ID 400-499）

- [x] 告警态势 `/monitor/situation` — 趋势折线（24h/7d/30d 分桶）、级别与来源分布、Top 标签、平均确认与恢复时长

- [x] 告警列表 `/monitor/alerts` — 概览卡片、级别/状态/关键字筛选、确认与恢复、投递流水
- [ ] 告警规则 `/monitor/alert-rules`
- [ ] 检测规则 `/monitor/detection-rules` — 顺序链、并发窗、窗口 Join、降噪
- [x] 通知路由 `/monitor/routing` — 优先级 + 级别/标签匹配、兜底路由、选路预演

- [ ] 聚合策略 `/monitor/aggregation` — 聚合维度、归桶预览、重叠检测
- [ ] 事件中心 `/monitor/events` — 事件确认/恢复、响应处置、诊断上下文
- [ ] 指标查询 `/monitor/metrics` — 对接 Prometheus，即时/范围查询
- [ ] 日志查询 `/monitor/logs` — 对接 Loki
- [ ] 链路追踪 `/monitor/traces` — 对接 Jaeger / Tempo
- [ ] 拨测探测 `/monitor/probe` — 内置探测与自定义探测
- [ ] 业务拓扑 `/monitor/topology` — 节点绑定资源、健康分布、连线校验
- [ ] 平台健康 `/monitor/health` — 告警规则、通知投递、边缘组件自检
- [ ] 公网监测 `/monitor/public-ip` — 公网 IP 与端口暴露面监测

## 安全合规（ID 500-599）

- [ ] 证书管理 `/security/ssl` — 到期提醒、自动续签
- [ ] 防火墙策略 `/security/firewall` — 边缘规则下发与审计
- [ ] 双因子口令 `/security/twofa` — TOTP 分组与验证码
- [ ] 安全意识 `/security/awareness` — 培训与考核
- [ ] 特征库 `/security/features` — 端口/病毒特征、灰度开关

## 智能与成本（ID 600-699）

- [ ] 模型资源池 `/ai/model-pool` — 上游多供应商池化、隧道主机配置
- [ ] 用量与成本 `/ai/model-usage` — Token/请求量、按日金额排名

- [ ] Agent 运行 `/ai/agent-runs` — 运行流水、工作流步骤、工具调用
- [ ] Agent 配置 `/ai/agent-config` — 能力开关、评测集

## 配置中心（ID 700-799）

- [x] 配置项 `/config/items` — 分组键值、类型校验、内置键只可改值、自定义键可增删

- [x] Webhook 接入 `/config/webhooks` — 接入源与 Token、推送示例、Token 重置、按指纹去重
- [x] 站点导航 `/config/site-navigation` — 分类卡片墙 + 管理模式，仅允许 http/https 地址
- [x] 邮件模板 `/config/email-templates` — Go template 语法、保存前语法校验、样例数据预览；配合 email 类型通知渠道走 SMTP 发信



## 系统管理（ID 800-899）

- [x] 用户管理 `/system/user` — CRUD、角色绑定、重置口令、启停
- [x] 角色权限 `/system/role` — 菜单与按钮授权树
- [x] 命令规则 `/system/command-rule` — 正则规则、拦截/告警、试跑
- [x] 操作审计 `/system/audit` — 写操作流水
- [x] 通知渠道 `/system/notify-channel` — webhook / silent 两类渠道、鉴权头、试发
- [x] 通知记录 `/system/notify-record` — 投递流水、失败原因与耗时
- [x] 部门管理 `/system/department` — 多级部门树、负责人、人员与主机计数，删除前校验引用
- [x] 公司管理 `/system/company` — 公司台账，作为部门树的根
- [x] 菜单管理 `/system/menu` — 内置菜单可改展示（标题/图标/排序/隐藏）、自建菜单可全改可删，结构字段由代码维护

- [x] 数据权限 `/system/data-permission` — 角色级数据范围（全部/本部门/含下级/仅本人/指定部门）+ 生效诊断
- [x] 资源授权 `/system/resource-grant` — 按用户/角色把指定主机或数据库额外授予，可限定动作（终端/文件/执行/管理）与有效期，带生效诊断


- [x] 公告管理 `/system/announcement` — 草稿/发布/下线，发布即向全员投递站内消息，下线撤回未读


- [ ] IM 集成 `/system/im` — 扫码登录、组织同步、审批事件
- [x] 系统配置 `/system/config` — 内置平台参数的表单视图（平台名称、登录提示、并发、上传上限、录像保留）


## 消息中心（ID 900-999）

- [x] 我的消息 `/message/inbox` — 未读汇总、按类型筛选、逐条与批量已读、顶栏铃铛角标
- [x] 公告中心 `/message/announcement` — 已发布公告时间线


## 建议实现顺序

1. **容器平台** —— 集群接入 + 工作负载只读视图，再加 YAML 编辑
2. **告警规则与检测规则** —— 需要先接入指标源（Prometheus 查询），再做阈值与降噪
3. **组织与权限** —— 部门、公司、数据权限、资源授权，配套多租户场景
4. **构建发布** —— 对接 Jenkins，与定时任务共用执行记录模型
5. **其余模块** —— 按实际需求插队




## 暂不纳入

- 与特定企业流程强绑定的定制模块（各家审批流、计费口径差异大），保留扩展点而不内置实现
- 前端不引入任何第三方闭源产物，UI 层全部基于自建 Vue 3 + Element Plus 骨架实现

