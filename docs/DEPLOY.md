# 部署与运维手册

面向「把 OpsOne 装到公司内网、长期跑下去」的人。读完这一篇就能上线、能备份、能升级、能排障。

平台自身**不依赖** Redis / MQ / 外部数据库：一个进程 + 一个 SQLite 文件 + 一个录像目录。

---

## 0. 先确认这几件事

- **单机部署，不支持多实例**。定时任务是进程内 cron，没有选主；两个实例会把批量命令下发两次、值班升级重复叫人、留存清理同时删同一个库。要做高可用得先把调度抢占和共享存储补上（见第 9 节）。
- **数据全在两处**：`OPS_DSN` 指向的 SQLite 文件、`OPS_RECORD_DIR` 里的会话录像。备份这两样就够（第 7 节）。
- **主机登录凭据在库里是明文**（已知取舍，见 `docs/SECURITY.md` 第 1 节）。数据库文件与备份要按「凭据库」的级别控制权限。
- **系统要求**：Linux x86_64（systemd 或 Docker 任选）、能访问被纳管主机的 22 端口。平台进程不需要 root。

目录布局（本文一律按这个走）：

```
/usr/local/bin/ops              # 单个静态二进制（含 API + 前端托管 + 备份子命令）
/etc/opsone/opsone.env          # 配置，chmod 600
/var/lib/opsone/
├── ops.db                      # 数据库
├── recordings/                 # 会话录像
├── backups/                    # 自动备份
└── web/                        # 前端构建产物（web/dist 的内容）
```

---

## 1. 拿到交付物

在有 Go 1.26+ / Node 22+ / pnpm 的机器上打包（不需要在目标机器上装工具链）：

```bash
make dist
# 产出 dist/opsone-<版本>-linux-amd64.tar.gz
# 里面是：ops 二进制、web/（前端产物）、deploy/（部署样例）、DEPLOY.md
```

版本号取自 `git describe`，编进二进制里，装完可以用 `ops version` 核对。

---

## 2. 方式 A：二进制 + systemd（推荐）

```bash
# 1) 解包
tar xzf opsone-<版本>-linux-amd64.tar.gz && cd opsone

# 2) 账号与目录
sudo useradd -r -s /usr/sbin/nologin opsone
sudo install -d -o opsone -g opsone /var/lib/opsone
sudo cp -r web /var/lib/opsone/web && sudo chown -R opsone:opsone /var/lib/opsone/web
sudo install -m 0755 ops /usr/local/bin/ops

# 3) 配置
sudo install -d -m 0750 -g opsone /etc/opsone
sudo install -m 0640 -o root -g opsone deploy/opsone.env.example /etc/opsone/opsone.env
sudo vi /etc/opsone/opsone.env        # 至少改掉三项「必改」，见第 5 节


# 4) 服务
sudo cp deploy/opsone.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now opsone

# 5) 验收
curl -fsS http://127.0.0.1:8080/healthz   # 进程活着
curl -fsS http://127.0.0.1:8080/readyz    # 库连得上、表能读（这条才代表能干活）
journalctl -u opsone -n 50 --no-pager
```

浏览器打开 `http://<服务器>:8080`，用 `admin` + `OPS_ADMIN_PASSWORD` 登录，**第一件事是改口令**。

> 配置文件权限给的是 `root:opsone 0640` 而不是 `0600`：`ops backup` / `ops restore` 是以服务账号身份跑的，读不到配置就只能手输一串路径参数。

> 配了 `OPS_WEB_DIR` 后端就自己托管前端，不需要 nginx。只有要上 HTTPS 或与其它站点共用 443 才需要第 4 节。

---

## 3. 方式 B：Docker Compose

```bash
cp deploy/opsone.env.example .env     # 同样先改「必改」三项
docker compose up -d --build
docker compose logs -f opsone
```

依赖源拉不动时（国内直连 npm / Go 代理经常超时）：

```bash
docker build -t opsone:latest \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --build-arg NPM_REGISTRY=https://registry.npmmirror.com .
```

要点：

- 数据落在命名卷 `opsone-data`（容器内 `/app/data`），数据库、录像、备份都在里面。
- `stop_grace_period: 40s` 要大于 `OPS_SHUTDOWN_TIMEOUT_SEC`，否则收尾没做完就被 SIGKILL。
- 健康检查用的是 `/readyz`。
- 集群服务转发的端口区间（默认 30000-30099）已在 compose 里映射；不用这个功能就把那段删掉。
- 备份在容器里跑：`docker compose exec opsone ops backup`。

---

## 4. 反向代理与 TLS（可选）

样例：`deploy/nginx.conf.example`。三个最容易踩的点：

1. **WebSocket 必须透传**：Web 终端、集群服务转发都靠它。漏了 `Upgrade` / `Connection` 头就是「终端连上立刻断开」。
2. **读超时要放大**：终端空闲时没有数据流动，nginx 默认 60s 会把它踢掉；样例里给的是 3600s。
3. **想让审计记录真实客户端 IP**，必须把反代地址填进 `OPS_TRUSTED_PROXIES`。平台默认**不信任任何代理**、忽略 `X-Forwarded-For`（否则任何人都能伪造审计里的来源 IP）；不配就会把 nginx 的 IP 记成操作来源。

另外 `OPS_ALLOW_ORIGINS` 要写成用户实际访问的地址（`https://ops.example.com`），它同时是 WebSocket 的 Origin 白名单。

---

## 5. 配置项

全部通过环境变量给。完整清单见 `deploy/opsone.env.example`，这里只说关键的。

**`OPS_ENV=prod` 下这三项不改就拒绝启动**（故意如此：这三项是最常见的「忘了改就上线」）：

- `OPS_JWT_SECRET` — 令牌签名密钥，≥32 字节随机串（`openssl rand -base64 48`）。不设就是用仓库里公开的开发默认值，等于任何人能伪造任意用户登录。
- `OPS_ADMIN_PASSWORD` — 内置 admin 的初始口令，不能留默认值。
- `OPS_ALLOW_ORIGINS` — 前端来源白名单，不能为空、不能是 `*`、不能还留着 localhost。

另外 prod 下 `OPS_DEBUG` 默认关闭，显式开启会被拒（它会打含参数的 SQL 日志）。

以下项目配不好只会 warn，但都值得看一眼：`OPS_SSH_STRICT_HOST_KEY`（主机指纹校验）、`OPS_FORWARD_BIND`（隧道监听地址，隧道本身无认证）、`OPS_TRUSTED_PROXIES`、`OPS_BACKUP_SPEC`、`OPS_WEB_DIR`、`OPS_SECRET_KEY`（凭据字段加密，留空即不加密存储 —— 这是被允许的选择，界面会照实标注；要换密钥或退回明文走「凭证库 → 密钥加密体检 → 更换密钥 / 取消加密」，不要直接改这个变量，直接改等于让现有密文全部解不开）。

数字项与布尔项写错（如 `OPS_TOKEN_TTL_HOUR=twelve`）会直接报错退出，而不是悄悄用默认值。

---

## 6. 日常运维

- **看状态**：`systemctl status opsone`、`journalctl -u opsone -f`。平台自身的体检在界面「监控告警 → 平台健康」，那一页会列出调度器、内置巡检、通知链路、数据库与录像占用。
- **探活**：`/healthz` 只看进程（常量返回），`/readyz` 会真查数据库与用户表，负载均衡/k8s 探针应该用 `/readyz`。
- **日志**：平台只往 stdout/stderr 写，不自己落文件、不轮转。systemd 交给 journald（`journalctl --vacuum-time=30d` 控制体积），Docker 交给 log driver。
- **重启语义**：收到 SIGTERM 会依次停调度 → 通知并断开 Web 终端 → 关闭转发隧道并落库 → 等在跑的请求收尾（批量执行是同步请求）→ 收尾会话状态 → 关库。超时上限 `OPS_SHUTDOWN_TIMEOUT_SEC`（默认 30s），systemd 的 `TimeoutStopSec` 必须比它大。
- **平台自己挂了谁告警**：平台内置的告警都跑在自己进程里，进程死了没人叫。请用外部监控轮询 `/readyz`（Zabbix/Prometheus blackbox/云监控都行）。

---

## 7. 备份与恢复

备份内容 = SQLite 快照（`VACUUM INTO`，不用停服）+ 录像目录 tar.gz。**不含**配置文件（`opsone.env` 自己纳入配置管理）。

```bash
# 手动备份。--env-file 一定要带：sudo / cron 会清掉环境变量，
# 不带就会按默认值去找当前目录下的 ops.db
sudo -u opsone ops backup --env-file /etc/opsone/opsone.env
sudo -u opsone ops backup --env-file /etc/opsone/opsone.env --out /mnt/nas/opsone --keep 30

# 容器里
docker compose exec opsone ops backup
```

**备份命令会先确认「这确实是 OpsOne 的库」**（能查到 users 表、里面有账号），否则直接报错退出。原因很实际：SQLite 打不开文件时会现场建一个空库，那样会「备份成功」出一个 4KB 的空文件，等真出事才发现没东西可恢复。

自动备份默认开着：`OPS_BACKUP_SPEC=0 3 * * *`，保留 `OPS_BACKUP_KEEP=7` 份。**它刻意早于数据留存清理（`OPS_RETENTION_SPEC=30 3 * * *`）**——清理是真删且不可逆，先备份再清理才有回头路。备份结果（含失败）会出现在「平台健康 → 自动备份」。

想让备份独立于平台进程（平台挂了也要备），用 cron：

```cron
# /etc/cron.d/opsone-backup
0 3 * * * opsone /usr/local/bin/ops backup --env-file /etc/opsone/opsone.env >> /var/log/opsone-backup.log 2>&1
```

用 cron 的话记得把 `OPS_BACKUP_SPEC` 留空，免得一天备两次。

`backups/` 在本机，机器整块坏了就一起没了，**请把它同步到另一台机器或 NAS**（rsync/云同步自行安排）。

**恢复必须停服**（进程握着旧文件句柄，换文件它不会重新加载）：

```bash
sudo systemctl stop opsone
sudo -u opsone ops restore --env-file /etc/opsone/opsone.env \
    --db /var/lib/opsone/backups/opsone-20260921-030000.db \
    --recordings /var/lib/opsone/backups/opsone-20260921-030000-recordings.tar.gz
sudo systemctl start opsone
curl -fsS http://127.0.0.1:8080/readyz
```

`restore` 不会删掉现有库：它先把原文件改名成 `ops.db.before-restore-<时间戳>` 再写入，恢复错了还能退回去。

---

## 8. 升级

表结构是 **AutoMigrate 负责「加表加列」+ 一套版本化步骤负责「改/删/回填」**。
已应用的步骤记在 `schema_migrations` 表里，`ops migrate status` 能看到；
**没有回滚**（见下），所以「先备份」不是建议而是前提。

```bash
sudo -u opsone ops backup          # 1. 先备份，这步别省
sudo systemctl stop opsone         # 2. 停服
sudo install -m 0755 ops /usr/local/bin/ops     # 3. 换二进制
sudo rm -rf /var/lib/opsone/web && sudo cp -r web /var/lib/opsone/web   # 4. 换前端
sudo chown -R opsone:opsone /var/lib/opsone/web
sudo -u opsone ops migrate status --env-file /etc/opsone/opsone.env   # 5. 看要跑哪些步骤（只读）
sudo systemctl start opsone        # 6. 起服（启动时自动迁移）
ops version && curl -fsS http://127.0.0.1:8080/readyz
```

想把迁移与起服分开（大库、或者要先确认迁移结果）：第 5 步之后先
`ops migrate up --env-file ...`，确认输出没问题再起服。

**回退**：换回旧二进制 + `ops restore` 那份备份。只换二进制不换库**会被拒绝启动** ——
旧程序发现库的结构版本比自己认识的新时直接报错退出，而不是像以前那样看着正常、
等到访问新字段时才崩。

**没有 down / 回滚**是明确的设计选择：SQLite 下很多变更要重建表，
自动生成的回滚往往是错的，而一个能跑但把数据弄坏的回滚比没有回滚更危险。

**陈旧列**：AutoMigrate 不删列，所以改过字段名或删过字段的库里会留下旧列。
`ops migrate status` 会把它们列出来并打印可执行的 `ALTER TABLE ... DROP COLUMN`，
但**平台不自动删** —— 删列不可逆。确认无用后自己先备份再执行。

Docker 方式：`docker compose exec opsone ops backup` → `docker compose pull/build` → `docker compose up -d`。


---

## 9. 已知限制

- **不支持多实例 / 水平扩展**：进程内 cron 无选主；转发隧道与扫码登录 ticket 都是进程内状态；SQLite 是本地文件。**第二个实例默认会拒绝启动**（连同原因一起打在日志里）——以前是不报错然后把所有定时任务各跑一遍。确实要短暂双开（升级时重叠几秒）设 `OPS_ALLOW_MULTI_INSTANCE=true`，那样第二个实例以 standby 起来、**不跑任何调度**；leader 心跳超过 `OPS_INSTANCE_LEASE_SEC`（默认 45 秒）没更新时 standby 会接管调度。**这不是 HA**：流量不会自动切，终端与隧道不会迁移。
- **只支持 SQLite**：代码里写死了驱动，换 MySQL/PG 需要改 `server/internal/db/db.go` 并处理方言差异。
- **不产出 Prometheus 指标、没有 pprof 端点**：平台自身可观测性靠「平台健康」页与日志。
- **日志不结构化、不轮转**：交给 journald / docker log driver。
- **转发隧道端口不做认证**：能连到 `OPS_FORWARD_BIND:端口` 的人等同于能访问被转发的服务，详见 `docs/SECURITY.md` 第 16 节。
- **审计只记写操作、不记请求体**：查得到「谁 PUT 了 /hosts/3」，查不到改了哪个字段（`docs/SECURITY.md` 第 5 节）。

---

## 10. 排障

- **启动即退出，日志写「生产模式安全检查未通过」**：按提示补 `OPS_JWT_SECRET` / `OPS_ADMIN_PASSWORD` / `OPS_ALLOW_ORIGINS`，或关掉 `OPS_DEBUG`。临时要带着这些配置跑，可以把 `OPS_ENV` 设回 `dev`（自负风险）。
- **启动即退出，日志写「配置不合法」**：某个数字/布尔环境变量写错了，错误信息里带变量名。
- **页面能开但接口 401 / CORS 报错**：`OPS_ALLOW_ORIGINS` 与浏览器地址栏不一致（协议、端口都算）。
- **终端连上立刻「连接已关闭」**：反代没透传 WebSocket，或 `OPS_ALLOW_ORIGINS` 里没有当前来源（Origin 校验不通过）。
- **审计里的来源 IP 全是反代地址**：把反代 IP 填进 `OPS_TRUSTED_PROXIES`。
- **主机探测/采集全失败**：先在服务器上手动 `ssh` 试一次；开了 `OPS_SSH_STRICT_HOST_KEY` 且目标机换过主机密钥时会被拒连（这是预期行为）。
- **停服很慢**：正常。有正在跑的批量执行时会等它结束，上限是 `OPS_SHUTDOWN_TIMEOUT_SEC`。
- **`ops restore` 报「文件被占用」**：服务没停干净，确认 `systemctl stop opsone` 已完成。
