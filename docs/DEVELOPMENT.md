# 开发与构建

> 面向维护者。用户文档见 [`../README.md`](../README.md)，接手交接见 [`HANDOFF.md`](HANDOFF.md)。
> 本文覆盖：仓库结构、本机开发、打包出 `.qpkg`、产物校验、回归测试、发版步骤、待实机验证项。

---

## 1. 环境要求

| 用途 | 需要什么 |
|---|---|
| 编译 / 单元级调试 | Go **1.22+**（`go.mod` 锁 1.22；用到 `http.ServeMux` 的 `GET /path` 与 `r.PathValue`） |
| 打包 `.qpkg` | Linux x86_64 + **QNAP QDK 2.5.3**（官方只发 amd64 deb）+ `rsync` |
| 前端 | 不需要 Node，原生 ES 模块，改完刷新即生效 |
| 在 Windows 上开发 | 建议在 WSL 里构建；仓库放 `~/` 而不是 `/mnt/c`（9p 文件系统会显著拖慢 go build 与 rsync） |

`make setup` 会自动装齐（依赖 + Go + QDK），幂等：

```bash
make setup          # 全部装
make setup --no-qdk # 只装依赖与 Go
```

注意：QDK 只提供 amd64 包，ARM 版 WSL 装不上；`make setup` 会提前判断并给出提示。

---

## 2. 仓库结构

```
cmd/frpc-qnap-manager/     入口：flag 解析、信号处理、日志
internal/config/           配置模型、校验、TOML 生成（唯一真相源 = manager.json）
internal/supervisor/       frpc 子进程守护（退避重启）
internal/frpc/             frpc 管理 API 客户端（/api/status、/api/reload）
internal/server/           HTTP 服务、会话认证、REST API、QTS 前缀兼容
internal/webui/assets/     前端（原生 ES 模块，无构建步骤；编译时 //go:embed 进二进制）
package/                   QPKG 打包源
  ├─ qpkg.cfg              QPKG 元数据（QTS 代理与桌面窗口全靠这里）
  ├─ package_routines      安装/升级/卸载钩子
  ├─ shared/               会拷进安装根目录的公共文件
  │   ├─ frpc-qnap.sh      服务脚本（QTS 调用的启停入口）
  │   └─ config/           升级要保留的默认配置（manager.json / state.json）
  ├─ arm_64/               架构专属文件（frpc、manager，gitignore）
  └─ icons/                App Center 图标
scripts/                   fetch-frp.sh / build-qpkg.sh / e2e.sh / make-icon.py
                           preflight-qpkg.sh（打包源预检）/ wsl-setup.sh（WSL 一键装环境）
docs/                      方案评估、交接说明、排查文档、过程记录
```

按重要性排序的代码地图见 [`HANDOFF.md`](HANDOFF.md) §3。

---

## 3. 本机开发

```bash
make build      # 编译到 build/
make run        # 开发模式：前端读磁盘，数据目录 devdata/
```

`make run` 后打开 <http://127.0.0.1:7911>，初始口令在 `devdata/initial-password.txt`。

本机没有 frpc 时，先放一个对应平台的二进制到 `devdata/frpc`（macOS 可用官方 `darwin_arm64` 包里的 `frpc`），否则只能看界面、不能真正建隧道。

Windows 原生编译 Go 时若报 `build cache is required`，指定缓存目录即可：

```bash
export GOCACHE=/tmp/gocache
```

---

## 4. 打包成 .qpkg

```bash
make arm64      # 交叉编译 manager（linux/arm64）+ 下载并校验 frpc
make check      # 可选：只做打包源预检，不调 qbuild
make qpkg       # 预检 + qbuild → dist/frpc-qnap_<ver>_arm_64.qpkg
```

`make qpkg` 只在 Linux 上可用（QDK 仅提供 Linux 版）。手工等价步骤：

```bash
sudo apt install -y rsync curl file make
sudo apt-get update            # 装 QDK 前必须，否则依赖会被解析到已下架的版本而 404
wget https://github.com/qnap-dev/QDK/releases/download/v2.5.3/qdk_2.5.3_amd64.deb
sudo apt install ./qdk_2.5.3_amd64.deb
```

打包链的几个约束（踩过坑，别改回去）：

- **`QPKG_CONFIG` 的默认文件必须放 `package/shared/config/`**。qbuild 对相对嵌套路径只认「数据包里已有该文件」，放 `package/config/` 会直接 `exit 1`，报错只有一句 `no such file; check your QPKG_CONFIG settings`。
- **打包前会归一化权限**（文件 644 / 可执行 755 / 目录 755）。从 `/mnt/c` 拷进 WSL 的文件一律是 777，不归一会把 world-writable 的二进制带上 NAS。
- **前端不再单独放入 `package/shared/web/`**。资源已 `//go:embed` 进 manager，非 `-dev` 模式不读磁盘。
- `scripts/preflight-qpkg.sh` 会在 qbuild 之前检查依赖、元数据格式、架构与 ELF 匹配、`QPKG_CONFIG` 可达性、图标命名、脚本 CRLF。退出码 2 表示只有警告。

---

## 5. 产物与校验

当前已验证的产物（Ubuntu 24.04 + Go 1.27.3 + QDK 2.5.3）：

| 项 | 值 |
|---|---|
| 文件 | `dist/frpc-qnap_1.0.1_arm_64.qpkg` |
| 大小 | 8 705 176 字节 |
| md5 | `f91945c48a12fbc77f845fdb209b9637` |
| sha256 | `47072cfa36aba83092cb6a2e52287ae289c8d27669497b01b1884f7cce6c203c` |

包体结构：`#!/bin/sh` 引导头 + 控制 tar.gz（`qpkg.cfg`/`package_routines`/`qinstall.sh`/`md5sum`/`built_info`）+ 数据 tar.gz + 100 字节尾部
（`[MODEL(10)|RESERVED(40)|FW_VERSION(10)|NAME(20)|VERSION(10)|FLAG(10)]`，末段为 `QNAPQPKG`）。

安装根目录内容：`frpc`、`frpc-qnap-manager`、`frpc-qnap.sh`、`config/manager.json`、`config/state.json`、`.qpkg_icon.gif`。

```bash
# QDK 自带的元数据查询与结构校验
qbuild --query info dist/frpc-qnap_1.0.1_arm_64.qpkg
qbuild --verify    dist/frpc-qnap_1.0.1_arm_64.qpkg

# 解出控制文件
qbuild --extract dist/frpc-qnap_1.0.1_arm_64.qpkg /tmp/x
```

> `qbuild --extract` 只解控制部分；要看数据包内容需要按上面说的偏移取第二个 gzip 成员。

---

## 6. 回归测试

```bash
FRP_DIR=/path/to/frp_0.71.1_<os>_<arch> make build && \
FRP_DIR=/path/to/frp_0.71.1_<os>_<arch> bash scripts/e2e.sh
```

`FRP_DIR` 必须同时包含 `frpc` 与 `frps`（测试会自己拉起一个 frps）。覆盖 23 项断言：
健康检查与静态页、登录、401 与 CSRF、配置落盘与 TOML 生成、隧道 running、热重载不重启、
公共段变更重启、非法配置拒绝且不覆盖、日志、`/frpc-qnap` 前缀兼容、启停。

e2e 占用 17911 / 17400 / 17000 等冷门端口，避免与本机演示环境冲突。

---

## 7. 发版步骤

1. 同步修改 `VERSION` 与 `package/qpkg.cfg` 的 `QPKG_VER`（`qbuild --build-version` 会回写 qpkg.cfg，两者不一致会弄脏工作区）
2. `make arm64 && make qpkg`
3. 记录产物大小与 md5/sha256，更新本文 §5 与 `README.md` 的安装说明（如涉及）
4. 跑一遍 `scripts/e2e.sh`
5. 提交，并同步 `handoff/frpc-qnap-src-*.zip` 源码快照（`git archive` 生成，排除 `handoff/` 自身）

---

## 8. 待实机验证（Phase 0）

以下只能在真实 ARM NAS 上确认，是发布前必须消掉的不确定性：

| # | 项 | 怎么验 | 通过标准 |
|---|---|---|---|
| 1 | QTS 反向代理是否剥离 `/frpc-qnap` 前缀 | `grep -rn "frpc-qnap" /etc/config/apache/` | 确认是保留还是剥离，记入方案评估 §12 |
| 2 | 未签名包能否安装 | App Center 手动安装 | 装完主菜单出现 frpc |
| 3 | 代理路径可达 | `curl -sv http://127.0.0.1:8080/frpc-qnap/` | 200 + HTML |
| 4 | 桌面内嵌窗口 | 点主菜单 frpc | 桌面内弹窗，不新开标签，无白屏/CSP 拦截 |
| 5 | 重启自愈 | `/etc/init.d/Qthttpd.sh restart` + 重启 NAS | 代理仍可访问，服务自动起来 |
| 6 | 端口占用 | `netstat -lntp \| grep frpc` | 只有 127.0.0.1:7911 与 127.0.0.1:74xx，无 0.0.0.0 |

安装失败（「无法安装程序包」且无提示）的排查见 [`troubleshooting-install.md`](troubleshooting-install.md)。

---

## 9. 已知的文档与实现不一致

维护时注意，避免在用户文档里复述错误表述：

- `internal/server/server.go` 生成 `initial-password.txt` 时写的提示是「登录后请立即在「设置」中修改」，但**界面里并没有改密入口**（后端 `Auth.SetPassword` 已具备，缺 UI）。用户文档已按真实情况写，代码里的这句提示待与改密页一起处理。
- 方案评估稿早期版本里 `QPKG_CONFIG` 含 `config/frpc.toml`、`web/` 也会入包，均已被实现阶段修正，以本文为准。

---

## 10. 相关文档

- [`HANDOFF.md`](HANDOFF.md) —— 交接说明：代码地图、进度、决策、踩坑、调试入口
- [`frpc-qnap-方案评估.md`](frpc-qnap-方案评估.md) —— 可行性论证、QTS 集成机制、风险清单
- [`troubleshooting-install.md`](troubleshooting-install.md) —— 安装失败排查
- [`records/2026-11-03-worklog.md`](records/2026-11-03-worklog.md) —— 第一轮开发时间线与决策依据
- [`records/2026-11-03-wsl-env.md`](records/2026-11-03-wsl-env.md) —— WSL 打包链验证与实跑结果
- [`records/2026-11-03-install-blocker.md`](records/2026-11-03-install-blocker.md) —— 真机安装失败排查全过程（含四个二分变体）
