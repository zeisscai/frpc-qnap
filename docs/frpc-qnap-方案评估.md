# frpc-QNAP 方案评估

> 目标：基于 frp 的 `frpc` 做一个 QNAP 原生 QPKG，支持 x86_64 与 arm_64，管理界面嵌在 NAS 网页端（不新增对外端口）。
> 版本：v0.1（评估稿） 日期：2026-11-03

---

## 0. 结论速览

| 问题 | 结论 |
|---|---|
| 整体是否可行 | **可行，难度中等偏低**。核心机制（QTS 内嵌 Web UI、多架构打包、frpc 管理 API）全部有官方支持，不需要逆向或改内核 |
| fork frp 还是独立仓库 | **独立仓库**。不 fork、不改 frp 源码；把 frp 当"上游二进制/Go 模块"消费 |
| 「不开额外端口」怎么实现 | `QPKG_USE_PROXY=1` + `QPKG_PROXY_PATH`，由 QTS 自带的 Apache 反向代理到本地回环端口；再用 `QPKG_DESKTOP_APP=1` 让它在 QTS 桌面里以窗口打开 |
| 最大的技术不确定性 | QTS 代理**是否剥离路径前缀**、固件升级后代理配置是否自愈 → 必须在真机上做 Phase 0 尖刺验证 |
| 最大的产品不确定性 | QTS 5.0.1 起 App Center 默认**禁止安装无数字签名 QPKG**，用户需手动开开关（自签名也过不了 QNAP 信任链） |
| 建议节奏 | Phase 0 尖刺（1–2 天，一个 hello-world QPKG 打通代理）→ MVP（约 3 周全职） |

**一句话方案**：一个 QPKG 里塞三个东西 —— 官方 `frpc` 二进制 + 自研 `frpc-qnap-manager`（Go 单文件，含 HTTP API 与静态前端）+ QTS 生命周期脚本。管理器监听 `127.0.0.1:7911`，靠 QTS 代理暴露成 `http://NAS:8080/frpc-qnap/`，管理器的职责是"生成 frpc.toml + 守护进程 + 转发状态"。

---

## 1. 需求拆解与验收标准

| # | 需求 | 验收方式 |
|---|---|---|
| R1 | 支持 x86_64（QTS 5.x 主流 x86 机型） | 真机装包、隧道起来、UI 可开 |
| R2 | 支持 arm_64（aarch64 机型） | 真机或 qemu-aarch64 验证二进制可执行 |
| R3 | 管理界面在 NAS 网页端内使用，**不额外开端口** | 浏览器只访问 `NAS:8080/frpc-qnap/` 即可完成全部操作；`netstat` 上不出现 LAN 监听的新端口 |
| R4 | 能在 QTS 桌面里以窗口形式打开 | `QPKG_DESKTOP_APP=1`，点 App Center/主菜单图标弹窗 |
| R5 | 隧道配置可视化编辑（服务端地址/端口/token/代理列表） | UI 增删改代理，保存后生效 |
| R6 | 运行状态可见（连接状态、每条代理的 running/error） | 复用 frpc `/api/status` |
| R7 | 随 NAS 开关机自启停、可升级、可卸载 | 重启 NAS 后自动连上；升级保留配置 |
| R8 | 日志可看（不要求 SSH） | 管理器读取日志文件尾部并分页展示 |

非目标（v1 不做）：frps 服务端、多实例、多用户权限体系、流量统计图表（frpc 侧无历史数据）、自动申请证书。

---

## 2. 现状与差异化（为什么值得做）

现在 QNAP 上跑 frpc 只有三条路，都有明显缺陷：

| 方案 | 问题 |
|---|---|
| Container Station 跑 `frpc` 镜像 | 吃内存/CPU；配置靠挂载文件；每次升级要删容器重建；需要装 Container Station |
| 2019 年民间的 frpc QPKG（nasyun 帖） | 直接把 frpc 官方二进制包一层；图标打开的是 **frpc 自带 AdminUI 的 7400 端口**（独立页面、独立端口）；AdminUI 默认无密码；有"改配置丢 token"的已知 bug；只能 SSH 改 ini；无 arm64 |
| SSH 手搓 systemd 式启动脚本 | 不会自启、不会守护、固件升级容易丢 |

我们要做的差异化很清晰：**原生 QPKG + QTS 内嵌 UI + 双架构 + 配置不裸奔**。

---

## 3. 可行性逐项论证

### 3.1 「不开放额外端口」是官方支持的，不是 hack

QDK（QNAP 官方 QPKG 开发套件）在 `qpkg.cfg` 里提供了这几个字段：

```
QPKG_WEBUI="/frpc-qnap"        # 相对路径
QPKG_WEB_PORT="7911"           # 你的服务实际监听端口（仅回环）
QPKG_USE_PROXY="1"             # 关键：走 QTS HTTP Proxy
QPKG_PROXY_PATH="/frpc-qnap"   # 代理路径
QPKG_DESKTOP_APP="1"           # 关键：在 QTS 桌面里以窗口打开，而不是新标签页
QPKG_DESKTOP_APP_WIN_WIDTH="1100"
QPKG_DESKTOP_APP_WIN_HEIGHT="600"
QPKG_VISIBLE="1"               # 主菜单只对管理员可见
```

工作机制：QTS 把 `QPKG_WEB_PORT` 写进 `/etc/config/qpkg.conf` 的 `Web_Port` 字段，由 Qthttpd（QTS 自带的 Apache）生成反向代理规则（配置落在 `/etc/config/apache/extra/` 一带），于是你的服务通过 QTS 自己的 8080/443 暴露。

**效果**：用户视角只有 `NAS:8080/frpc-qnap/`，和 File Station、App Center 完全同源。同源还顺带解决了 iframe 的 Same Origin Policy 问题——这正是 2017 年那个经典坑（QTS 8080 里 iframe 嵌 80 端口的页面会被浏览器拦）的根因，而 `QPKG_USE_PROXY=1` 正是官方给的解药。

三层集成递进：

| 层级 | 配置 | 用户感知 |
|---|---|---|
| L1 能被 8080 代理访问 | `WEBUI` + `WEB_PORT` + `USE_PROXY` + `PROXY_PATH` | 地址栏访问 `NAS:8080/frpc-qnap/` |
| L2 在 QTS 桌面内弹窗 | `DESKTOP_APP=1` | 点图标 → 桌面里出现一个应用窗口 |
| L3 进主菜单 | `VISIBLE=1` | 左上角主菜单里能搜到 |

L3 就是我们说的"在 NAS 网页端里使用"的最终形态。

> ⚠️ 待实测（Phase 0 的核心）：QTS 生成的 `ProxyPass` 是 `ProxyPass /frpc-qnap http://127.0.0.1:7911/frpc-qnap`（保留前缀）还是 `.../`（剥离前缀）。这决定后端要不要剥前缀。设计上按"两种都兼容"做：前端资源全用相对路径、路由用 hash 模式、后端统一归一化请求路径。

### 3.2 QPKG 生命周期是成熟的

QDK 2.5.3（2026-06 仍在维护）提供：`qbuild` 打包、`qinstall.sh` 安装、`package_routines` 钩子、`/etc/init.d` 服务注册、`QPKG_CONFIG` 升级保留配置文件、App Center 启停控制、QuLog 系统日志、Notification Center 通知。

我们只写：`qpkg.cfg` + `package_routines`（3 个非空钩子）+ 一个 `start/stop/restart/status` 脚本。这是纯体力活，风险低。

### 3.3 双架构是 qbuild 原生能力

`qbuild --build-arch` 支持的取值里明确包含 `x86_64` 和 `arm_64`，而且可以重复传入做**单包多架构**：

```
qbuild --build-arch x86_64 --build-arch arm_64
```

安装时 `qinstall.sh` 探测 `uname -m`：`x86_64 → x86_64`，`aarch64 → arm_64`，不匹配则拒绝安装。

Go 的交叉编译天然适配（`CGO_ENABLED=0`，纯静态，无 glibc 依赖）——这一点很关键：QNAP 各机型 glibc 版本差异大，静态二进制是唯一省心解。

> 覆盖范围说明：QNAP 的 32 位 ARM（`arm-x19` / `arm-x31` / `arm-x41`，如 TS-431P、TS-231P）不在本次范围。用户若要求，加一个 `arm-x41` 目标即可，成本很低（同一次交叉编译换个 GOARM），但测试机型不好找。

### 3.4 frpc 内核怎么复用：三条路，选最省的那条

| 方式 | 做法 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A. 官方二进制 sidecar** | 下载 GitHub Release 的 `frp_0.71.1_linux_{amd64,arm64}.tar.gz`，只取 `frpc` | 零改上游；升级=换文件；与官方行为完全一致；CI 只编自己的代码 | 两个进程，管理器要做进程守护 | ✅ **v1 采用** |
| B. Go 模块内嵌 | `import "github.com/fatedier/frp/client"`，`client.NewService(...)` 起在进程内 | 单进程、单二进制；可直接 `svc.Status()` 拿状态，连 7400 都不需要；启停原子 | 与 frp 版本强耦合（`v1.ClientCommonConfig` 这类结构体随版本变）；构建慢；排查问题要懂 frp 内部 | 🔶 v2 可选，用 build tag 隔离 |
| C. fork + 打补丁 | fork 一份改内核 | 能改任何行为 | frp 迭代快（0.71.1 刚发），每次 rebase；QNAP 逻辑对上游零价值，PR 不会被接受 | ❌ 除非被迫，不做 |

**关键判断：我们真的不需要改 frpc。** 因为 frpc 已经暴露了完整的管理面：

| 能力 | 接口 | 用途 |
|---|---|---|
| 代理状态 | `GET /api/status` | UI 的"运行中/错误"列表 |
| 读配置 | `GET /api/config` | 首次导入现成配置 |
| 写配置 | `PUT /api/config` | 高级模式直接编辑 TOML |
| 热重载 | `GET /api/reload` | 改代理不中断其他隧道 |
| 优雅停止 | `POST /api/stop` | 停止服务 |
| 动态代理 CRUD | `/api/store/proxies` | （可选）不落盘地增删代理 |
| 配置静态校验 | `frpc verify -c x.toml` | 保存前校验，避免把隧道改挂 |

前提是 `frpc.toml` 里配了 `webServer`（否则 `reload`/`status` 全部报 `web server port should be set`）。我们把它绑在 `127.0.0.1:7400` 并加 Basic Auth，只有管理器能访问。

> 另一个必须自研 UI 的原因：frpc 自带 dashboard 是 Vue SPA，资源引用是绝对路径（`/static/...`），直接反代到子路径 `/<prefix>/` 必然 404。想在子路径用只能改写 HTML/JS，得不偿失——不如自己写一套贴合 QTS 观感的界面。

### 3.5 依赖与资源开销

| 项 | 评估 |
|---|---|
| 运行时依赖 | **零**。Go 静态二进制，不依赖 Python/Node/Container Station |
| 磁盘 | frpc ≈ 25 MB + manager ≈ 10 MB + 前端静态资源 ≈ 1 MB，每架构一包 ≈ 36 MB |
| 内存 | frpc 常驻 ≈ 15–25 MB，manager ≈ 10–15 MB，合计 < 50 MB |
| CPU | 空闲接近 0；arm64 低端机型（如 TS-433，Cortex-A55）无压力 |
| 权限 | QPKG 由 root 启动；frpc 不需要 root，但管理器要写 `/etc/init.d` 之外的路径，root 最省事 |

---

## 4. 仓库策略：独立仓库，不 fork

### 4.1 结论

**新建独立仓库 `frpc-qnap`，把 frp 作为上游依赖消费。** 只有一种情况才考虑 fork，见 4.3。

### 4.2 对比

| 维度 | fork frp | 独立仓库（推荐） |
|---|---|---|
| 代码归属 | QNAP 打包脚本、启动脚本、前端混进 frp 仓库 | 职责清晰：本仓库只放"QNAP 外壳" |
| 升级上游 | 每次 release 要 rebase/merge，冲突集中在 `cmd/frpc`、`conf/` | 改一行 `FRP_VERSION=` + 校验 sha256，重跑 CI |
| 上游接受度 | QNAP 相关代码不可能被 merge，永远背着 diff | 零 diff |
| CI | frp 的 Go CI 与 QDK 的 Linux 打包链互相干扰 | 各管各的；release 流程独立 |
| 版本语义 | 混乱（跟 frp 的 tag 走还是跟 QPKG 走？） | 自己的 `1.0.0+frp0.71.1`，一目了然 |
| 许可证 | Apache-2.0，fork 后修改需标注 | Apache-2.0，分发二进制保留 LICENSE/NOTICE 即可 |
| 心智负担 | 高：要持续关注上游 commit | 低：只看 changelog |

frp 是 Apache-2.0（110k star，最新 v0.71.1 / 2026-10-14），商用、闭源分发、二次打包都允许，唯一义务是保留版权与许可声明，并在修改过的文件里注明。我们**不改源码**，义务最轻。

### 4.3 什么情况下才 fork

只有当出现"不改内核就做不了"的需求，例如：
- 需要在 frpc 内部直接暴露 QTS 会话身份（目前管理面已经够用，不太可能）；
- 上游某个 bug 阻塞发版（临时方案：不是 fork，而是在 `patches/` 放一个 patch，构建时 `git apply`）。

即使到那一步，也建议：**fork 只作为构建源，patch 控制在个位数，同时向上游提 PR**。不要长期漂移。

### 4.4 依赖注入方式（三选一，建议第 1 种）

1. **官方 release 二进制 + sha256 校验**（v1）：`scripts/fetch-frp.sh` 下载、校验、解包出 `frpc`。可复现、最快。
2. **Go module 依赖**（v2 若要走内嵌）：`require github.com/fatedier/frp v0.71.1`，必要时 `replace` 到自己的 fork。
3. **git submodule**：不推荐。frp 仓库大，clone 慢，且我们不需要它的源码树。

### 4.5 建议仓库结构

```
frpc-qnap/
├── VERSION                      # 1.0.0+frp0.71.1
├── Makefile                     # fetch / build / package / verify 一键
├── go.mod
├── cmd/
│   └── frpc-qnap-manager/
│       └── main.go
├── internal/
│   ├── config/                  # 配置模型 + TOML 生成 + 校验
│   ├── supervisor/              # frpc 子进程守护（退避重启）
│   ├── frpapi/                  # 封装 /api/status /reload /config
│   ├── httpapi/                 # 路由、会话、CSRF
│   └── qnap/                    # QTS 相关：系统日志、路径探测
├── web/                         # Vue3 + Vite，base:'./'，hash 路由
│   └── dist/                    # 构建产物，提交进包
├── package/
│   ├── qpkg.cfg
│   ├── package_routines
│   ├── shared/
│   │   ├── frpc-qnap.sh         # 启停脚本
│   │   ├── icons/
│   │   └── web/                 # 由 make 从 web/dist 拷入
│   ├── x86_64/                  # 架构专属文件（frpc、manager）
│   └── arm_64/
├── scripts/
│   ├── fetch-frp.sh
│   ├── build-qpkg.sh
│   └── verify.sh
├── docs/
└── .github/workflows/release.yml
```

---

## 5. 系统设计

### 5.1 进程模型

```
浏览器 ──HTTPS/443 或 HTTP/8080──> QTS Apache ──ProxyPass──> 127.0.0.1:7911
                                      │                        │
                                      │                    frpc-qnap-manager
                                      │                     ├─ 静态前端
                                      │                     ├─ REST API
                                      │                     └─ 守护子进程
                                      │                            │
                                      │                        frpc (127.0.0.1:7400 admin)
                                      │                            │
                                      └── 出站 7000 ────────> frps（公网）
```

要点：
- **只有 manager 一个进程对 QTS 可见**，frpc 是它的子进程。QTS 启停脚本只管 manager，manager 负责 frpc 的 restarts（指数退避，最多 N 次后进 crash 状态并在 UI 报警）。
- frpc 的 `webServer` 绑 `127.0.0.1`，带随机 Basic Auth，**不作为用户入口**（只用于状态/热重载）。想调试可以 SSH 端口转发。
- 所有监听都在回环，`netstat -lntp` 上不出现 0.0.0.0 的新端口 —— R3 的硬指标。

### 5.2 目录布局（安装后）

```
/share/CACHEDEV1_DATA/.qpkg/frpc-qnap/
├── frpc                        # 上游二进制（执行位）
├── frpc-qnap-manager
├── web/                        # 前端静态资源
├── config/
│   ├── frpc.toml               # 生成物，0600
│   └── manager.json            # 自身配置（监听端口、口令 hash），0600
├── logs/
│   ├── frpc.log
│   └── manager.log
└── .uninstall.sh
```

`QPKG_CONFIG` 里登记 `config/manager.json`（用户配置，升级要留）与 `config/frpc.toml`；`web/`、二进制每次覆盖。

### 5.3 端口规划

| 端口 | 绑定 | 用途 | 是否对外 |
|---|---|---|---|
| 7911 | 127.0.0.1 | manager HTTP（QTS 代理目标） | 经 QTS 8080/443 |
| 7400 | 127.0.0.1 | frpc admin API（随机口令） | 否 |
| 7000+ | 出站 | 连 frps | 出站 |

7911 选一个冷门值；首次安装时若被占用，`package_routines` 里探测 7900–7920 取第一个空闲并写入 `qpkg.cfg`/`manager.json`（QTS 代理端口必须安装期固定，运行期不能变）。

### 5.4 配置模型

UI 编辑的字段映射到 `frpc.toml`：

```toml
serverAddr = "frps.example.com"
serverPort = 7000

auth.method = "token"
auth.token = "<加密保存，前端只回 masked>"

transport.protocol = "tcp"        # tcp / kcp / quic / websocket / wss
transport.tls.enable = true

log.to = "/share/CACHEDEV1_DATA/.qpkg/frpc-qnap/logs/frpc.log"
log.level = "info"
log.maxDays = 3

webServer.addr = "127.0.0.1"
webServer.port = 7400
webServer.user = "admin"
webServer.password = "<32 位随机，安装时生成>"

[[proxies]]
name = "qts-web"
type = "tcp"
localIP = "127.0.0.1"
localPort = 8080
remotePort = 18080
```

策略：
- **普通模式**：表单（服务端、认证、传输、代理列表）。
- **高级模式**：直接编辑原始 TOML（危险操作，保存前必须 `frpc verify` 通过）。
- 保存链路：`verify` → 写临时文件 → 原子 `rename` → 判定变更类型 → `reload` 或 `restart`。
- **什么时候 reload、什么时候 restart**：`[[proxies]]` 变化 → `GET /api/reload`（不断其他隧道）；`serverAddr/serverPort/auth/transport` 变化 → 重启 frpc（这些属公共段，热重载不支持）。UI 上要明确提示"将短暂断线"。
- token 落盘用 `0600`，API 返回时一律 `******`；前端提交空值表示"不修改"。

### 5.5 管理器 API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 探活，无需认证 |
| POST | `/api/login` | 口令 → HttpOnly Cookie + CSRF token |
| POST | `/api/logout` | |
| GET | `/api/overview` | frpc 版本、进程状态、uptime、连接状态、当前配置摘要 |
| GET | `/api/proxies` | 透传 `/api/status` 并规整字段 |
| GET/PUT | `/api/config` | 结构化配置读写（GET 掩码） |
| GET/PUT | `/api/config/raw` | 原始 TOML 读写（高级模式） |
| POST | `/api/service/{start,stop,restart}` | 控制 frpc 子进程 |
| POST | `/api/reload` | 显式热重载 |
| GET | `/api/logs?file=frpc&lines=200` | 日志尾部（上限 2000 行，防内存） |
| POST | `/api/setup` | 首次设置口令（需 PIN） |

### 5.6 前端

- Vue 3 + Vite，`base: './'`，**hash 路由**（`#/proxies`），彻底规避子路径问题。
- 资源全部相对路径，不引 CDN（NAS 常常无外网，且要过 CSP）。
- 体积目标 < 300 KB gzip。
- 三个页面足够：概览（状态灯 + 代理列表）、配置（表单/高级 TOML 双 Tab）、日志。
- 观感对齐 QTS 5（浅色卡片、圆角、蓝色主色），但**跟随系统暗色**——QTS 桌面可切主题。

### 5.7 与 QTS 的其它集成点（v2 加分项）

| 能力 | 实现 |
|---|---|
| 断线推送到 Notification Center | `qnotification` / QDK 的通知函数 |
| 日志进 QuLog Center | 走 QDK 的 `err_log` / `info_log` |
| 资源监视器能看到 frpc 子进程 | 启动脚本里 `export QNAP_QPKG=frpc-qnap`（QDK 2.2.15 起的机制） |
| 一键把 NAS 自身服务加成隧道 | 预置模板：QTS 8080、File Station、Container Station、qBittorrent 等 |
| 展示 myQNAPcloud DDNS 域名 | 读 `/etc/config/...` 或调用 QTS CLI |

---

## 6. QPKG 打包要点

### 6.1 qpkg.cfg

```sh
QPKG_NAME="frpc-qnap"                 # 不要用 "frpc"：App Center 按名字判重，会和民间老包互相覆盖
QPKG_DISPLAY_NAME="frpc Client"
QPKG_VER="1.0.0"
QPKG_AUTHOR="..."
QPKG_LICENSE="Apache-2.0"
QPKG_SUMMARY="FRP client with an integrated QTS management UI"
QPKG_RC_NUM="150"
QPKG_SERVICE_PROGRAM="frpc-qnap.sh"
QPKG_SERVICE_PORT="7911"
QTS_MINI_VERSION="4.4.0"

QPKG_WEBUI="/frpc-qnap"
QPKG_WEB_PORT="7911"
QPKG_USE_PROXY="1"
QPKG_PROXY_PATH="/frpc-qnap"
QPKG_DESKTOP_APP="1"
QPKG_DESKTOP_APP_WIN_WIDTH="1100"
QPKG_DESKTOP_APP_WIN_HEIGHT="600"
QPKG_VISIBLE="1"

QPKG_CONFIG="config/manager.json config/frpc.toml"
QPKG_VOLUME_SELECT="3"
QPKG_TIMEOUT="30,30"
```

### 6.2 package_routines 三个非空钩子

| 钩子 | 要做的事 |
|---|---|
| `pkg_check_requirement` | 检查 `uname -m` 是否 `x86_64`/`aarch64`；检查 7911 是否占用（占用则换端口）；检查磁盘空间 |
| `pkg_install` / `pkg_post_install` | 生成 `manager.json`（随机口令 hash、frpc admin 随机口令、首次设置 PIN）；`chmod 600`；把 PIN 写一条 QuLog 系统日志；初始化空的 `frpc.toml` 模板 |
| `pkg_pre_remove` | 停服务、保留用户配置到 `.qpkg` 外（可选导出） |

其它钩子留空即可。

### 6.3 启停脚本 `frpc-qnap.sh`

```sh
start:  确保 config 存在 → 后台起 manager → 写 pid → 等 /api/health 就绪（最多 10s）
stop:   读 pid → SIGTERM → 等 15s → 仍在则 SIGKILL → 清 pid
status: pid 存活 + health 通 → 返回码 0
```

注意 QTS 会在关机时调用 `stop`，超时默认 30s，脚本必须能被快速杀死（manager 收到 SIGTERM 先停 frpc 子进程，最多等 5s）。

### 6.4 打包与产物

```
./scripts/fetch-frp.sh 0.71.1     # 下载 + sha256 校验 + 解包 frpc
make web                          # 前端构建
make package                      # qbuild --build-arch x86_64 → frpc-qnap_1.0.0_x86_64.qpkg
                                  # qbuild --build-arch arm_64 → frpc-qnap_1.0.0_arm_64.qpkg
```

两个独立包（v1 稳妥）。**可选优化**：`qbuild --build-arch x86_64 --build-arch arm_64` 产出单包多架构（把架构专属文件放 `package/x86_64/`、`package/arm_64/`），用户在 App Center 只需下载一个包。这个能力 qbuild 文档明确支持，但需要验证 QTS 安装时的取用逻辑，放到 Phase 3 做。

签名：`qbuild --sign --gpg-name <KEY>` 用自建 GPG 密钥做完整性签名。**注意**：自签名过不了 QNAP 的信任链，App Center 仍视作"未签名应用"，用户必须开启"允许安装与执行不具数字签名的应用程序"（QTS 5.0.1 起该开关默认关闭）。要真正免开关，只有走 QNAP 的合作方签名（`codesigning.qnap.com:5001`，需 QNAP 内部账号）——个人开发者基本拿不到，别在这上面花时间。

---

## 7. 安全设计

| 威胁 | 对策 |
|---|---|
| 8080 上的代理路径可能不带 QTS 登录态（待实测） | 管理器强制自己的会话认证；未登录一律 401，不依赖 QTS |
| 首次上手被别人抢先设密码 | 首次设置要求输入安装期写在 QuLog 系统日志里的 6 位 PIN（仅管理员可见） |
| 暴力破解 | bcrypt 存口令；登录失败 5 次锁 5 分钟（内存计数 + 日志） |
| CSRF | 双提交 Cookie + `X-CSRF-Token` 头；写操作全部 POST/PUT |
| token 泄漏 | 落盘 0600；API 掩码；日志里禁止打印 `auth.token`（生成配置时过滤） |
| iframe 被外部站点嵌套 | 不发 `X-Frame-Options`（QTS 自己会加同源限制）；如需可设 `SAMEORIGIN` |
| frpc admin 端口被滥用 | 仅 `127.0.0.1` + 随机 32 位 Basic Auth 口令；不提供 UI 入口 |
| 配置注入（TOML 里写 `log.to=/etc/passwd`） | 高级模式做路径白名单校验，只允许写包内目录 |
| 提权 | 进程以 root 跑，但不对文件系统做用户可控路径的写操作；所有路径先 `filepath.Clean` + 前缀校验 |

---

## 8. 风险清单

| # | 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|---|
| 1 | QTS 代理路径前缀行为与预期不符（保留/剥离） | 中 | 高 | Phase 0 实测；后端兼容两种；前端 hash 路由 + 相对路径 |
| 2 | 固件升级后 `extra/*.conf` 被重建，代理失效 | 中 | 中 | 走 `QPKG_USE_PROXY` 官方机制（由 QTS 依据 `qpkg.conf` 重建），而不是自己写 Apache conf；升级后自检 `/api/health` |
| 3 | 未签名安装开关，普通用户被吓退 | 高 | 中 | 文档写清楚"为什么需要开、开在哪里、包内容可解包自查"；提供 `sha256` |
| 4 | 7911 端口被别的应用占用 | 低 | 中 | 安装期探测并落到空闲端口 |
| 5 | arm64 机型实机测试缺设备 | 中 | 中 | qemu-user 静态验证 + 找一位 TS-433/TS-233 用户当 beta |
| 6 | frpc 版本升级导致 admin API 行为变化 | 低 | 中 | 锁定版本；升级前跑 e2e 脚本（起 frpc + 打 `/api/status`） |
| 7 | QTS 5 的 CSP 或 iframe 策略限制桌面窗口 | 低 | 中 | Phase 0 用 hello-world 验证 `DESKTOP_APP=1` |
| 8 | 用户把 NAS 8080 改成非默认端口 | 低 | 低 | 代理跟随 QTS 端口，无需关心；文档只写"用你 NAS 的访问地址" |
| 9 | 权限：QPKG 跑在 `/share/CACHEDEV*_DATA`，卷未挂载时启动失败 | 低 | 高 | `QPKG_VOLUME_SELECT=3`；启动脚本检查卷挂载再起 |

---

## 9. 里程碑与工作量

| 阶段 | 内容 | 产出 | 工作量 |
|---|---|---|---|
| **P0 尖刺** | hello-world QPKG 打通 QTS 代理；验证前缀行为、桌面弹窗、unsigned 安装 | 一页实测结论（含截图/命令输出） | 1–2 人日 |
| **P1 骨架** | QPKG 内跑 frpc + manager 空壳；启停脚本；App Center 可装可卸 | 能装、能连上 frps、能看到静态页 | 2–3 人日 |
| **P2 后端** | 配置读写、verify、reload/restart、状态、日志、进程守护、认证 | 完整 REST API + 单测 | 4–5 人日 |
| **P3 前端** | 概览/配置/日志三页，QTS 观感，暗色适配 | 可用 UI | 5–7 人日 |
| **P4 打包** | 双架构产物、CI、GPG 签名、图标、README | 两个 `.qpkg` | 2–3 人日 |
| **P5 加固** | 安全项、异常路径（frps 不可达、配置写坏、磁盘满）、真机回归 | RC | 3–5 人日 |

全职约 3 周；业余节奏约 1.5–2 个月。**P0 只花 1–2 天就能把最大的不确定性消掉，强烈建议先做，不要跳过直接写代码。**

---

## 10. Phase 0 验证清单（真机，逐条打勾）

```bash
# 0. 环境信息
uname -m                      # 期望 x86_64 / aarch64
cat /etc/version              # QTS 版本
grep -A8 "\[frpc" /etc/config/qpkg.conf 2>/dev/null

# 1. 做一个最小 QPKG：qpkg.cfg 只配
#    QPKG_WEBUI="/hello" QPKG_WEB_PORT="7911" QPKG_USE_PROXY="1"
#    QPKG_PROXY_PATH="/hello" QPKG_DESKTOP_APP="1"
#    启动脚本用 busybox httpd -p 7911 -h <web目录>

# 2. 装包（先开 App Center「允许安装不具数字签名的应用」）

# 3. 关键验证：代理是否通
curl -sv http://127.0.0.1:8080/hello/ 2>&1 | head -30

# 4. 前缀行为：请求 /hello/ 时后端收到的路径是什么
#    在 web 目录放个 cgi 或看 httpd 访问日志，确认是 "/hello/" 还是 "/"

# 5. QTS 生成的代理配置长什么样
grep -rn "hello\|7911" /etc/config/apache/ | head

# 6. 桌面弹窗是否正常（点图标，看是否在 QTS 桌面里开窗、有无白屏）

# 7. 重启 Qthttpd 后是否仍是通的
/etc/init.d/Qthttpd.sh restart && curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/hello/

# 8. 重启 NAS 后是否自愈（代理 + 服务）

# 9. 断网/无外网环境下 iframe 是否正常（排除 CSP 拦截）

# 10. arm64 机型重复 1–9（若手头没有，记 TODO）
```

验收：3、6、7、8 全部通过 → 方案的技术地基成立，可以进入 P1。

---

## 11. 需要你拍板的决策点

| # | 决策 | 选项 | 我的建议 |
|---|---|---|---|
| D1 | 仓库 | fork frp / 独立仓库 | **独立仓库**（理由见 §4） |
| D2 | frpc 复用 | 官方二进制 sidecar / Go 内嵌 | **先 sidecar**，内嵌留到 v2 且用 build tag 隔离 |
| D3 | 前端技术栈 | Vue3+Vite / 原生 JS | **Vue3+Vite**（三个页面 + 表单校验，原生会写成意大利面） |
| D4 | 单包多架构 / 双包 | 统一包 / 每架构一包 | **v1 双包**，P4 再试统一包 |
| D5 | 配置编辑形态 | 表单 / 原始 TOML / 两者 | **两者**（普通用户要表单，老手要 TOML） |
| D6 | 是否要做 frps | 只做 client / 服务端也做 | **只做 client**，服务端该跑在 VPS 上 |
| D7 | 首个测试机型 | x86 机型型号？arm64 有无？ | 需要你的机型清单来做兼容矩阵 |

---

## 附：参考资料

- QDK 官方仓库与配置参数：https://github.com/qnap-dev/QDK （`qpkg.cfg`、`--build-arch` 支持 `x86_64`/`arm_64`）
- QTS 手动安装与未签名应用开关：QNAP 官方文档（QTS 4.4/4.5 "启用安装没有数字签章的应用程式"）；QTS 5.0.1 release notes 明确该开关默认关闭
- frp 客户端热重载与管理 API：https://gofrp.org/zh-cn/docs/features/common/client/
- frp 最新版本：v0.71.1（2026-10-14），Apache-2.0
- 历史同类 QPKG（2019，nasyun）：直接用 7400 端口开 AdminUI，无认证、丢 token —— 本方案主要要解决的问题
