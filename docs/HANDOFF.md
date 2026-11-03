# 交接说明（HANDOFF）

> 给接手者的第一份文件。读完这页再动代码。
> 最后更新：2026-11-03 · 最后提交：见 `git log -1` · 打包链已在 WSL 实跑通过（见 §4 与 `docs/records/2026-11-03-wsl-env.md`）

---

## 1. 30 秒了解

**frpc-qnap** 是把 frp 的客户端（`frpc`）封装成 QNAP 原生 QPKG 的项目。核心卖点是：**管理界面嵌在 NAS 网页端里**（`NAS:8080/frpc-qnap/`），不额外开放端口。

- 上游内核：frp v0.71.1，**不修改源码**，直接用官方 release 二进制
- 目标架构：`arm_64`（用户只有 ARM 机型）；`x86_64` 构建链已就绪但未实机验证
- 当前状态：**已产出可分发 QPKG（`dist/frpc-qnap_1.0.1_arm_64.qpkg`，8.71 MB），但还没在真 NAS 上装过**

**只剩一件事没有任何证据：装到真 NAS 上的表现**（HANDOFF §5 Phase 0 六条），其中最关键的是
**QTS 反向代理会不会剥离 `/frpc-qnap` 前缀**——后端两种都兼容，但没实测过。
打包侧（`qbuild` 目录约定、包结构、权限、回归）这一轮已经全部拿到证据。



---

## 2. 你的环境（Windows）

| 能做 | 怎么做 |
|---|---|
| 跑 Go 构建 / 测试 | WSL Ubuntu-24.04（用户已有）。Windows 原生也能 `go build` / `go vet`（需 `GOCACHE`，见下） |
| 出 `.qpkg` | **只能 WSL**：装 QDK（仅 Linux x86_64 deb），`make qpkg` |
| 改前端 | 直接改 `internal/webui/assets/`，`make run` 后刷新即生效，无需构建 |
| 连 NAS 调试 | WSL 里 `ssh admin@NAS`；或 Windows 用 WinSCP/PuTTY |

### 2.1 WSL 侧三步（本轮新增，已静态验证、未实跑）

```bash
# 0. 建议放在 WSL 家目录，别放 /mnt/c —— 9p 文件系统会让 go build / rsync 慢好几倍
cp -a /mnt/c/Users/chn/Desktop/workbuddy/frpc-qnap ~/frpc-qnap && cd ~/frpc-qnap

# 1. 一次装好依赖 + Go(>=1.22) + QDK（幂等；--no-qdk 只装依赖与 Go）
make setup

# 2. 交叉编译 + 打包
make arm64
make qpkg        # 内部先跑 scripts/preflight-qpkg.sh，再调 qbuild，产物在 dist/
```

`make check` 只跑打包源预检（不调 qbuild），Windows 的 Git Bash 里也能跑，会明确告诉你缺什么。

注意：

- 仓库放在 `/mnt/d/...` 时 WSL 的 IO 很慢，建议 clone 到 WSL 家目录（`~/frpc-qnap`）
- Windows 的 `make` 可能是 Git Bash 的旧版本，**统一在 WSL 里执行 Makefile**
- 打包出来的 `.qpkg` 在 WSL 的 `dist/` 下，用 Windows 资源管理器走 `\\wsl$\Ubuntu-24.04\home\...` 取
- WSL2 里 `make run` 后，Windows 浏览器直接开 `http://localhost:7911` 即可（WSL2 会自动转发回环端口）；若不通，用 `hostname -I` 拿 WSL 的 IP 再访问
- 如果已经在 Windows 上编好了 `package/arm_64/`（manager + frpc），WSL 里可以只装 QDK + rsync 直接 `make qpkg`，省掉 Go
- Windows 原生跑 `go`：需要 `export GOCACHE=/tmp/gocache`（本机 `%LocalAppData%` 在 Git Bash 里不可见，否则报 `build cache is required`）

### 2.2 已知的环境级阻塞

`wsl.exe` 若被本机安全策略拦截（WorkBuddy → Security Center → Command Security → Program Blacklist），
助手无法代跑任何 WSL 命令，只能由你在终端里执行上面的命令。QDK 相关的一切都要在这一步之后才能继续。


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
| `scripts/preflight-qpkg.sh` | 打包源预检（依赖/元数据/架构/`QPKG_CONFIG` 路径/图标） | 改 `qpkg.cfg`、动 `package/` 布局时扩一条检查 |
| `scripts/wsl-setup.sh` | WSL/Linux 一键装依赖 + Go + QDK | QDK 或 Go 版本变化时改 |
| `docs/frpc-qnap-方案评估.md` | 技术方案与可行性论证（§12 是实现状态） | 决策变更时更新 |

---

## 4. 当前进度

### 已完成

- [x] Go 管理器（配置/校验/TOML/守护/API/认证/CSRF）
- [x] 前端四页（概览 / 隧道 / 设置 / 日志），字段对齐 luci-app-frpc
- [x] QPKG 打包源（qpkg.cfg、钩子、启停脚本、图标、默认配置）
- [x] 交叉编译 ARM64（manager 6.5 MB@Go1.26 / 7.0 MB@Go1.27 + frpc 15 MB，均为静态 ELF aarch64）
- [x] e2e 回归脚本，连续两轮 23/23 通过
- [x] UI 用无头 Chromium 实际登录验证，控制台零报错
- [x] 打包链静态验证：qbuild 参数与目录约定逐条对照 QDK 2.5.3 源码确认，修掉一处会让首次打包必定失败的 `QPKG_CONFIG` 路径问题
- [x] **`make qpkg` 实跑成功**：WSL Ubuntu 24.04 + QDK 2.5.3 产出 `dist/frpc-qnap_1.0.1_arm_64.qpkg`（8 705 176 B，md5 `f91945c48a12fbc77f845fdb209b9637`），包结构/权限/尾部元数据均已解包核对
- [x] e2e 回归在 WSL 里复跑：**23/23 通过**
- [x] `make setup` / `make check`：WSL 环境一键装、打包源预检

### 未完成（按优先级）

1. **真机 Phase 0**（见第 5 节）—— 阻塞一切发布
2. 管理员改密页面（后端 PBKDF2 已就绪，缺 UI；口令只能靠 `initial-password.txt`）
3. `[[visitors]]` 支持（stcp/xtcp/sudp 的**访问端**，目前只支持服务端）
4. manager 监听端口（7911）冲突时自动顺延（现在只有 frpc 管理端口会顺延）

---

## 5. 下一步任务（含验收标准）

### 任务 0：出包 —— ✅ 已完成（2026-11-03）

已完成，命令与结果留档，换机器/换版本时按同样步骤复现：

```bash
cp -a /mnt/c/Users/chn/Desktop/workbuddy/frpc-qnap ~/frpc-qnap && cd ~/frpc-qnap
make setup      # 装 rsync/curl/file/make + Go(>=1.22) + QDK 2.5.3（幂等）
make arm64      # 已有 package/arm_64/frpc 时跳过下载
make qpkg       # 预检 + qbuild
```

实测结果（Ubuntu 24.04.4 / Go 1.27.3 / QDK 2.5.3）：

| 验收项 | 结果 |
|---|---|
| `qbuild` 通过 | ✅ 不再出现 `no such file; check your QPKG_CONFIG settings`（该阻断点已在上一轮修掉） |
| 产物 | ✅ `dist/frpc-qnap_1.0.1_arm_64.qpkg`，8 705 176 B，md5 `f91945c48a12…9637` |
| 包内结构 | ✅ `frpc` / `frpc-qnap-manager` / `frpc-qnap.sh` / `config/manager.json` / `config/state.json` / `.qpkg_icon.gif` |
| 权限 | ✅ 二进制与 `frpc-qnap.sh` 755，配置与许可 644，目录 755（打包时归一化，不继承检出权限） |
| 编码 | ✅ `package_routines`、`frpc-qnap.sh` 均为 LF |
| 尾部元数据 | ✅ `frpc-qnap / 1.0.0 / QNAPQPKG` |
| 回归 | ✅ `FRP_DIR=… bash scripts/e2e.sh` → 23/23 |

详细证据见 `docs/records/2026-11-03-wsl-env.md` §6。若换环境后 `qbuild` 报新错，
把完整输出补进该文档 §6，并同步第 7 节。

### 任务 1：真机 Phase 0（最高优先级）

包已经有了，装到 NAS，然后逐条验证：

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
7. **QDK 的图标目录是 `package/icons/`，不是 `package/shared/icons/`**；升级要保留的默认配置放 `package/shared/config/`（原因见第 9 条，别放回 `package/config/`）。
8. **`http.ServeMux` 的 `GET /api/xxx` 模式要 Go ≥ 1.22**，`r.PathValue` 同理，`go.mod` 已锁 1.22。
9. **`QPKG_CONFIG` 写相对嵌套路径时，默认文件必须放在 `package/shared/config/`**。qbuild 2.5.3 的 `add_config_data()` 对 `config/manager.json` 这种相对路径，只认「数据包里已经有这个文件」（即来自 `shared/` 或 `<arch>/`）或 `package/config/config/manager.json`；放 `package/config/manager.json` 会直接 `exit 1`，报错只有一句 `no such file; check your QPKG_CONFIG settings`。`scripts/preflight-qpkg.sh` 现在会提前拦住。
10. **`QPKG_CONFIG` 只写单层文件名时语义完全不同**：`QPKG_CONFIG="myApp.conf"` 表示 `package/config/myApp.conf` 会被拷到**安装根目录**（不是 root 下的 config/）。
11. **CRLF 会让 QTS 装不上包**：`package_routines` 与 `shared/*.sh` 由 NAS 上的 busybox sh 执行，带 `\r` 会报 `not found` / 语法错。仓库已加 `.gitattributes`（`text=auto eol=lf`），别删；在 Windows 上手工改过脚本后可用 `git check-attr -a <file>` 确认。
12. **WSL 最小安装不自带 `rsync`**，而 qbuild 用 rsync 组装数据包，缺了会在打包中途挂。`make setup` 会装。
13. **`build-qpkg.sh` 旧版提示「请先运行 make arm」是个错误目标名**（`${ARCH%%_*}` 把 `arm_64` 截成 `arm`）。已改成显式映射。
14. **装 QDK 前必须 `apt-get update`**：deb 依赖 `gnupg2`，索引过期时 apt 会去抓已下架的旧版本报 `404 Not Found`，安装中断。`wsl-setup.sh` 已改成无条件先 update。
15. **从 `/mnt/c` 拷进 WSL 的文件一律是 777**（drvfs 不保留 POSIX 权限）。不处理就会把 world-writable 的二进制和配置文件打进包。`build-qpkg.sh` 打包前统一归一化权限。
16. **`package/shared/web/` 是死重量**：前端已 `//go:embed` 进 manager，非 `-dev` 模式从不读磁盘上的 `web/`（`server.go` 的 `staticHandler` 只在 Dev 分支用 `AssetsDir`）。打包流程已不再同步该目录。

---

## 8. 常用命令

完整的构建、打包、发版流程见 [`DEVELOPMENT.md`](DEVELOPMENT.md)，这里只留交接时最常用的几条：

```bash
# 本机开发（需自己放一个 frpc 到 devdata/frpc）
make build
make run                      # http://127.0.0.1:7911，口令见 devdata/initial-password.txt

# 回归（FRP_DIR 必须同时含 frpc 与 frps）
FRP_DIR=/path/to/frp_x.y.z_<os>_<arch> make build && \
FRP_DIR=/path/to/frp_x.y.z_<os>_<arch> bash scripts/e2e.sh

# 出包（仅 Linux + QDK；WSL 里跑）
make setup && make arm64 && make qpkg
```

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

- `.workbuddy/` 是本地助手记忆，**不入库**；仓库内的存档副本在 `docs/records/memory-2026-11-03.md`，别删
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
- `docs/DEVELOPMENT.md` —— **构建/打包/回归/发版手册**（README 里删掉的开发内容都在这里）
- `docs/troubleshooting-install.md` —— **装不上时先看这里**：无提示无日志的排查阶梯、安装轨迹判据、对照实验
- `docs/records/2026-11-03-worklog.md` —— 第一轮的开发时间线、决策依据、验证数据
- `docs/records/2026-11-03-wsl-env.md` —— 第二轮：打包链静态验证、`QPKG_CONFIG` 缺陷的证据与复现、WSL 环境坑
- `docs/records/memory-2026-11-03.md` —— 开发过程中的原始工作记忆（按阶段追加，未整理）
- `README.md` —— 面向使用者的构建与安装说明
- `handoff/` —— 源码快照压缩包（离线备份，非必需）
