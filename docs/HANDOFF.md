# 交接说明（HANDOFF）

> 给接手者的第一份文件。读完这页再动代码。
> 最后更新：2026-11-03 · 最后提交：`4eeb700`

---

## 1. 30 秒了解

**frpc-qnap** 是把 frp 的客户端（`frpc`）封装成 QNAP 原生 QPKG 的项目。核心卖点是：**管理界面嵌在 NAS 网页端里**（`NAS:8080/frpc-qnap/`），不额外开放端口。

- 上游内核：frp v0.71.1，**不修改源码**，直接用官方 release 二进制
- 目标架构：`arm_64`（用户只有 ARM 机型）；`x86_64` 构建链已就绪但未实机验证
- 当前状态：**原型可运行、已通过 23 项自动化回归，但还没在真 NAS 上装过**

**两件没有任何证据的事**（接手后优先消掉）：

1. `qbuild` 打包从未真正执行过（开发机是 macOS，没有 Linux/QDK）
2. QTS 反向代理的前缀行为没实测过（后端两种都兼容，但没验证过）

---

## 2. 你的环境（Windows）

| 能做 | 怎么做 |
|---|---|
| 跑 Go 构建 / 测试 | WSL Ubuntu-24.04（用户已有）。Windows 原生也能 `go build`，但路径和脚本是 POSIX 风格 |
| 出 `.qpkg` | **只能 WSL**：装 QDK（仅 Linux x86_64 deb），`make qpkg` |
| 改前端 | 直接改 `internal/webui/assets/`，`make run` 后刷新即生效，无需构建 |
| 连 NAS 调试 | WSL 里 `ssh admin@NAS`；或 Windows 用 WinSCP/PuTTY |

注意：
- 仓库放在 `/mnt/d/...` 时 WSL 的 IO 很慢，建议 clone 到 WSL 家目录（`~/frpc-qnap`）
- Windows 的 `make` 可能是 Git Bash 的旧版本，**统一在 WSL 里执行 Makefile**
- 打包出来的 `.qpkg` 在 WSL 的 `dist/` 下，用 Windows 资源管理器走 `\\wsl$\Ubuntu-24.04\home\...` 取
- WSL2 里 `make run` 后，Windows 浏览器直接开 `http://localhost:7911` 即可（WSL2 会自动转发回环端口）；若不通，用 `hostname -I` 拿 WSL 的 IP 再访问

---

## 3. 代码地图

按重要性排序，改代码前先看这里。

| 路径 | 职责 | 什么时候改 |
|---|---|---|
| `internal/config/config.go` | 配置模型、默认值、**校验**、**TOML 生成**、原子写盘 | 加字段、改 frpc 配置映射、改校验规则 |
| `internal/server/server.go` | 全部 HTTP 路由、认证中间件、**配置生效链路**（verify → 落盘 → reload/restart） | 加接口、改生效策略、改 QTS 前缀兼容 |
| `internal/supervisor/supervisor.go` | frpc 子进程启停、崩溃退避重启（上限 10 次） | 改重启策略 |
| `internal/frpc/api.go` | frpc 管理 API 客户端（`/api/status`、`/api/reload`） | frp 升级后接口变了 |
| `internal/server/auth.go` | PBKDF2 口令、内存会话、登录限流 | 改认证 |
| `internal/webui/assets/app.js` | 前端全部逻辑（路由/渲染/表单/轮询） | 改界面 |
| `internal/webui/assets/index.html` · `app.css` | 结构与样式（QTS 风格，跟随暗色） | 改布局 |
| `package/qpkg.cfg` | QPKG 元数据（**QTS 代理与桌面窗口全靠这里**） | 改集成方式、版本号 |
| `package/package_routines` | 安装/升级/卸载钩子 | 改安装期行为 |
| `package/shared/frpc-qnap.sh` | QTS 调用的启停脚本（只管 manager） | 改启停 |
| `scripts/e2e.sh` | 端到端回归（23 项断言） | **改完任何逻辑都跑一遍** |
| `docs/frpc-qnap-方案评估.md` | 技术方案与可行性论证（§12 是实现状态） | 决策变更时更新 |

---

## 4. 当前进度

### 已完成

- [x] Go 管理器（配置/校验/TOML/守护/API/认证/CSRF）
- [x] 前端四页（概览 / 隧道 / 设置 / 日志），字段对齐 luci-app-frpc
- [x] QPKG 打包源（qpkg.cfg、钩子、启停脚本、图标、默认配置）
- [x] 交叉编译 ARM64（manager 6.3 MB + frpc 14 MB，静态链接）
- [x] e2e 回归脚本，连续两轮 23/23 通过
- [x] UI 用无头 Chromium 实际登录验证，控制台零报错

### 未完成（按优先级）

1. **真机 Phase 0**（见第 5 节）—— 阻塞一切发布
2. `make qpkg` 首次真机打包验证（qbuild 目录约定可能报错）
3. 管理员改密页面（后端 PBKDF2 已就绪，缺 UI；口令只能靠 `initial-password.txt`）
4. `[[visitors]]` 支持（stcp/xtcp/sudp 的**访问端**，目前只支持服务端）
5. manager 监听端口（7911）冲突时自动顺延（现在只有 frpc 管理端口会顺延）

---

## 5. 下一步任务（含验收标准）

### 任务 1：真机 Phase 0（最高优先级）

先 `make qpkg` 出包，装到 NAS，然后逐条验证：

| # | 验证项 | 命令 / 动作 | 通过标准 |
|---|---|---|---|
| 1 | 未签名包能装 | App Center 开「允许安装不具数字签名应用」→ 手动安装 | 装完主菜单出现 frpc |
| 2 | 代理路径通 | `curl -sv http://127.0.0.1:8080/frpc-qnap/` | 返回 200 + HTML |
| 3 | **前缀行为** | `grep -rn "frpc-qnap" /etc/config/apache/` | 确认是保留还是剥离前缀，记录到 docs |
| 4 | 桌面窗口 | 点主菜单 frpc | QTS 桌面内弹窗，不是新标签页，无白屏 |
| 5 | 重启自愈 | `/etc/init.d/Qthttpd.sh restart` + 重启 NAS | 代理仍可访问，服务自动起来 |
| 6 | 端口占用 | `netstat -lntp \| grep frpc` | 只有 127.0.0.1:7911 和 127.0.0.1:74xx，无 0.0.0.0 |

验收后请把第 3 条的实测结果写进 `docs/frpc-qnap-方案评估.md` §12，并更新 README 的「待实机验证」。

### 任务 2：改密页面

- 后端已有 `Auth.SetPassword`（PBKDF2 120k 轮），`state.json` 存 hash
- 需要：`POST /api/password`（旧密码 + 新密码）+ 设置页一个卡片
- 验收：改密后旧会话失效、新口令能登录、重启后仍生效

### 任务 3：visitors

- 现在 `Proxy` 结构只映射 `[[proxies]]`，需要区分 role
- frp 的访问端是 `[[visitors]]`：`name/type/serverName/secretKey/bindAddr/bindPort`
- 验收：UI 能选"访问端"，生成正确 TOML，e2e 补一条断言

---

## 6. 关键设计决策（别轻易推翻）

| 决策 | 理由 |
|---|---|
| **不 fork frp** | QNAP 外壳对上游零价值；升级只需换二进制。只有必须改内核时才用 `patches/` |
| **官方 frpc 二进制 sidecar** | 与上游行为完全一致；Go 内嵌（`import client`）留作 v2，用 build tag 隔离 |
| **manager.json 是唯一真相源** | `frpc.toml` 是生成物，每次保存重写（旧文件备份 `.bak`）。所以后端不需要 TOML 解析器，零第三方依赖 |
| **代理变更 reload、公共段变更 restart** | frp 的热重载只支持代理段，公共段改了必须重启，UI 会提示 |
| **原生前端，无构建** | 四页规模用不上框架；打包链不需要 Node；改文件即生效 |
| **每架构一个包（固定）** | 用户明确要求，不要改成单包多架构 |
| **`QPKG_USE_PROXY=1` + `DESKTOP_APP=1`** | 这是"不开放额外端口"的唯一正解，别自己写 Apache conf（会随固件升级被清） |
| **`QPKG_NAME=frpc-qnap` 不能改** | App Center 按名字判重，改成 `frpc` 会与 2019 年民间包互相覆盖 |

---

## 7. 踩过的坑（别重复踩）

1. **frpc 的 `/api/reload` 成功时返回 `200` + 空 body**。按 JSON 解析会误判失败 → 触发多余重启。现实现不解析响应体。
2. **`/` 改写成 `/index.html` 交给 `http.FileServer` 会 301 回 `/`**，死循环。用 `http.ServeFileFS(w, r, fsys, "index.html")`。
3. **frp 默认 `loginFailExit=true`**：frps 不可达时 frpc 直接退出，靠守护进程反复拉起（日志刷屏、重启计数飙升）。项目默认改成 `false`。
4. **frpc 状态值不止 `error`**，还有 `start error`（remotePort 被占、frps 没配 vhost）。统计用 `strings.Contains(status, "error")`，UI 同理。
5. **macOS AirPlay 占用 7000 端口**，本地起 frps 会静默失败。e2e 已改用 17000 / 28080 / 28022。
6. **e2e 与本机演示环境抢远程端口**：演示配置占着 18080，e2e 必须用不同端口，否则 `port unavailable`。
7. **QDK 的图标目录是 `package/icons/`，不是 `package/shared/icons/`**；`package/config/` 放升级要保留的默认配置。
8. **`http.ServeMux` 的 `GET /api/xxx` 模式要 Go ≥ 1.22**，`r.PathValue` 同理，`go.mod` 已锁 1.22。

---

## 8. 常用命令

```bash
# 本机开发（需自己放一个 frpc 到 devdata/frpc）
make build
make run                      # http://127.0.0.1:7911，口令见 devdata/initial-password.txt

# 回归
FRP_DIR=/path/to/frp_x.y.z_<os>_<arch> make build && \
FRP_DIR=/path/to/frp_x.y.z_<os>_<arch> bash scripts/e2e.sh

# 交叉编译（已存在 frpc 时跳过下载）
make arm64
make x86_64

# 打包（仅 Linux + QDK）
sudo apt install ./qdk_2.5.3_amd64.deb
make qpkg                     # → dist/frpc-qnap_1.0.0_arm_64.qpkg
```

`FRP_DIR` 必须同时含 `frpc` 和 `frps`（e2e 要起服务端）。

---

## 9. 调试入口

| 东西 | 位置 |
|---|---|
| 配置真相源 | `<data>/config/manager.json`（QTS 上是 `/share/CACHEDEV1_DATA/.qpkg/frpc-qnap/config/`） |
| 生成物 | `<data>/config/frpc.toml`（上一版 `.bak`） |
| 内部状态（含口令 hash、frpc 管理端口/口令） | `<data>/config/state.json` |
| 日志 | `<data>/logs/frpc.log`、`<data>/logs/manager.log`、`<data>/logs/manager.out` |
| 初始口令 | `<data>/initial-password.txt` |
| frpc 管理 API | `curl -u <adminUser>:<adminPassword> http://127.0.0.1:74xx/api/status`（口令在 state.json） |

前台手动跑（排查启动问题最快）：

```bash
/share/CACHEDEV1_DATA/.qpkg/frpc-qnap/frpc-qnap-manager \
  -data /share/CACHEDEV1_DATA/.qpkg/frpc-qnap \
  -frpc /share/CACHEDEV1_DATA/.qpkg/frpc-qnap/frpc \
  -listen 127.0.0.1:7911
```

---

## 10. 禁忌

- **不要 `rm -rf .workbuddy`**（项目记忆）
- 不要把 `auth.token` / `secretKey` / `adminPassword` 写进日志或返回给前端（现有代码已做掩码，别破坏）
- 不要把 `devdata/`、`build/`、`dist/`、`package/*/frpc*` 提交进仓库（`.gitignore` 已覆盖）
- 不要改 `QPKG_NAME`
- 不要把二进制内嵌进 git（frpc 14 MB，靠 `scripts/fetch-frp.sh` 拉取）
- 提交前必须跑 `scripts/e2e.sh`，全绿再推
- 后台进程不要用 `nohup` / `&` 起（会被工具回收）；调试时用前台阻塞运行

---

## 11. 待用户确认的产品问题

1. 要不要发布到 QNAPclub / 论坛（需要英文 README + 截图）
2. 是否需要 `visitors`（如果只用「把 NAS 服务暴露出去」，不需要）
3. 是否需要自定义 UI 主题色（目前跟随系统亮/暗）
4. 托盘/通知：要不要断线时推 QTS 通知中心（`qnotification`）

---

## 12. 相关文档

- `docs/frpc-qnap-方案评估.md` —— 可行性论证、QTS 集成机制、风险清单、§12 实现状态
- `docs/records/2026-11-03-worklog.md` —— 本次开发的时间线、决策依据、验证数据
- `README.md` —— 面向使用者的构建与安装说明
- `handoff/` —— 源码快照压缩包（离线备份，非必需）
