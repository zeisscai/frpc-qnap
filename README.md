# frpc-qnap

> **接手开发？先读 [`docs/HANDOFF.md`](docs/HANDOFF.md)** —— 环境要求、代码地图、当前进度、下一步任务都在那里。
> 开发过程与决策依据见 [`docs/records/2026-11-03-worklog.md`](docs/records/2026-11-03-worklog.md)。

QNAP 原生的 frpc 客户端：**管理界面直接嵌在 NAS 网页端里**，不额外开放端口。

- 支持架构：`arm_64`（当前），`x86_64`（构建脚本已就绪，未实机验证）
- 上游内核：`frp` v0.71.1（Apache-2.0），本项目**不修改 frp 源码**
- 管理界面参考 OpenWrt `luci-app-frpc` 的字段组织：服务器 / 传输 / 隧道规则 / 状态

```
浏览器 ──> QTS Apache (8080/443) ──ProxyPass──> 127.0.0.1:7911
                                                    │
                                            frpc-qnap-manager
                                             ├─ 静态前端 + REST API
                                             ├─ 会话认证 / CSRF
                                             └─ 守护子进程
                                                    │
                                              frpc (127.0.0.1:7400)
                                                    │
                                               出站 → frps
```

对外只有 `NAS:8080/frpc-qnap/` 这一个入口，`netstat` 上不会出现新监听端口。

## 它解决什么问题

| 现有做法 | 问题 |
|---|---|
| Container Station 跑 frpc 镜像 | 吃内存、配置靠挂载文件、升级要重建容器 |
| 民间 frpc QPKG（2019） | 图标打开的是 frpc 自带 AdminUI 的 7400 端口（独立页面 + 独立端口）、默认无认证、改配置会丢 token |
| SSH 手写启动脚本 | 不自启、不守护、固件升级容易丢 |

本项目的差异点：QTS 内嵌窗口、进程自动守护、配置热重载、Token 不回显、保留 QPKG 升级配置。

## 目录结构

```
cmd/frpc-qnap-manager/     入口：flag 解析、信号处理、日志
internal/config/           配置模型、校验、TOML 生成（唯一真相源 = manager.json）
internal/supervisor/       frpc 子进程守护（退避重启）
internal/frpc/             frpc 管理 API 客户端（/api/status、/api/reload）
internal/server/           HTTP 服务、会话认证、REST API、QTS 前缀兼容
internal/webui/assets/     前端（原生 ES 模块，无构建步骤）
package/                   QPKG 打包源（qpkg.cfg、安装钩子、启停脚本、图标、默认配置）
scripts/                   fetch-frp.sh / build-qpkg.sh / e2e.sh / make-icon.py
                           preflight-qpkg.sh（打包源预检）/ wsl-setup.sh（WSL 一键装环境）
docs/                      方案评估文档
```

## 本机开发

需要 Go 1.22+。前端不需要 Node。

```bash
make build                 # 编译到 build/
make run                   # 开发模式：前端读磁盘，数据目录 devdata/
```

`make run` 后打开 http://127.0.0.1:7911 ，初始口令在 `devdata/initial-password.txt`。

本机没有 frpc 时，先放一个对应平台的二进制到 `devdata/frpc`（macOS 可直接用 frp 官方 `darwin_arm64` 包里的 `frpc`），否则只能看界面、不能真正建隧道。

## 构建 ARM64 安装包

```bash
make arm64        # 交叉编译 manager + 下载并校验 frpc（linux/arm64）
make qpkg         # 需要 Linux + QDK，产出 dist/frpc-qnap_*.qpkg
```

`make qpkg` 只能在 Linux 上跑（QNAP QDK 只提供 Linux x86_64 版）。WSL 里最短路径：

```bash
make setup        # 装依赖（rsync/file/make）+ Go(>=1.22) + QDK 2.5.3，重复执行也安全
make arm64        # 交叉编译 manager + 下载 frpc（linux/arm64）
make check        # 可选：只做打包源预检，不调 qbuild
make qpkg         # 预检 + qbuild → dist/frpc-qnap_*.qpkg
```

不想用脚本时，等价于：

```bash
sudo apt install -y rsync curl file make
sudo apt-get update            # 装 QDK 前必须，否则依赖会 404
wget https://github.com/qnap-dev/QDK/releases/download/v2.5.3/qdk_2.5.3_amd64.deb
sudo apt install ./qdk_2.5.3_amd64.deb
make qpkg
```

> 依赖 `rsync` 是硬要求：qbuild 用它组装数据包，Ubuntu 最小安装不预装。
> 仓库建议放在 WSL 家目录（`~/frpc-qnap`）而不是 `/mnt/c`，9p 文件系统会显著拖慢编译与打包；
> 从 `/mnt/c` 复制进去的文件权限一律是 777，`make qpkg` 会在打包前统一归一化。

已验证的产物（Ubuntu 24.04 + Go 1.27.3 + QDK 2.5.3）：

| 项 | 值 |
|---|---|
| 文件 | `dist/frpc-qnap_1.0.0_arm_64.qpkg` |
| 大小 | 8 705 163 字节 |
| md5 | `4bb440639025731240fcfa42a6a72630` |

包内结构：`frpc`、`frpc-qnap-manager`、`frpc-qnap.sh`、`config/manager.json`、`config/state.json`、`.qpkg_icon.gif`
（前端已内嵌进 manager，包里不再有 `web/` 副本）。

## 在 NAS 上安装

1. 打开 App Center → 右上角设置 → 勾选 **「允许安装与执行不具数字签名的应用程序」**
   （QTS 5.0.1 起该开关默认关闭；自签名无法通过 QNAP 信任链，个人开发者绕不过去）
2. App Center → 右上角 `+` → **手动安装** → 选择 `dist/frpc-qnap_x.y.z_arm_64.qpkg`
3. 装好后主菜单里出现 **frpc**，点开就是管理窗口

首次登录：

```bash
cat /share/CACHEDEV1_DATA/.qpkg/frpc-qnap/initial-password.txt
```

（ZFS 卷机型路径为 `/share/ZFS1_DATA/.qpkg/frpc-qnap/`）

进去先填服务器地址、端口、Token，保存后 frpc 自动启动。

## 配置是怎么生效的

`manager.json` 是唯一真相源，`frpc.toml` 每次保存时重新生成（旧文件备份为 `.bak`）。

| 改动内容 | 生效方式 |
|---|---|
| 隧道增删改 | `GET /api/reload` 热重载，其他隧道不中断 |
| 服务器地址 / 端口 / Token / 协议 / TLS / 日志 | 重启 frpc 进程（frp 的公共段不支持热重载） |

保存前会调用 `frpc verify -c` 做一次权威校验，配置写错不会把正在跑的隧道弄挂。

## 安全设计

- manager 只绑 `127.0.0.1`，frpc 管理接口也只绑 `127.0.0.1` 且带随机 Basic Auth
- 口令用 PBKDF2-HMAC-SHA256（12 万轮）加盐存储，会话 12 小时过期，重启即失效
- 登录失败 5 次锁定 5 分钟
- 写操作要求 `X-Requested-With: frpc-qnap`，配合 SameSite Cookie 防 CSRF
- Token / secretKey / HTTP 口令不回显给浏览器，留空表示"保持不变"
- 响应头带 `X-Frame-Options: SAMEORIGIN`，只允许 QTS 桌面同源嵌窗

## 待实机验证（第一优先级）

以下几点只能在你那台 ARM NAS 上确认，属于方案里标注的 Phase 0：

1. **QTS 代理是否剥离 `/frpc-qnap` 前缀** —— 后端已做两种兼容（`stripPrefix`），但要看实测
2. QTS 生成的 Apache 规则落在哪里：`grep -rn "frpc-qnap" /etc/config/apache/`
3. 重启 Qthttpd / 重启 NAS 后代理是否自愈
4. `QPKG_DESKTOP_APP=1` 的窗口在 QTS 桌面里是否正常（有无 CSP 拦截）
5. 未签名包的安装流程与提示文案
6. 7400 / 7911 端口被占用时的表现（frpc 管理端口会自动顺延；manager 监听端口目前固定）

## 已知限制

- 只支持 frpc 的 `tcp/udp/http/https/stcp/xtcp/sudp` **服务端**配置，暂不支持 `[[visitors]]`（访问端）
- 管理员口令修改入口尚未做到 UI 里（接口已支持 PBKDF2 存储，缺改密页面）
- 不做 frps，服务端请部署在 VPS 上
- 不支持 32 位 ARM（`arm-x19` / `arm-x31` / `arm-x41`）机型
- QPKG 未做 GPG 签名（即使签了也过不了 QNAP 信任链）

## 回归测试

```bash
# 用本机 frp 二进制跑一遍全链路（起 frps + manager，断言 23 项）
FRP_DIR=/path/to/frp_0.71.1_darwin_arm64 make build && \
FRP_DIR=/path/to/frp_0.71.1_darwin_arm64 bash scripts/e2e.sh
```

覆盖：健康检查、登录、401/CSRF、配置落盘、TOML 生成、隧道 running、热重载、重启、非法配置拒绝、日志、QTS 前缀兼容、启停。

## 许可

本项目代码：Apache-2.0。
分发的 `frpc` 二进制来自 [fatedier/frp](https://github.com/fatedier/frp)，同为 Apache-2.0，打包时保留其 LICENSE 与版权声明。
