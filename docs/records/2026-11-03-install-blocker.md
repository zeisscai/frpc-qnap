# 真机安装受阻排查记录 · 2026-11-03

> 目标机器：**TS-428 / QTS 5.2.11.3661**
> 一句话结论：**包结构完全合法，失败发生在 App Center 的前置校验（还没跑到我们的安装脚本）；
> 已把「元数据 vs 载荷」二分到位，四个诊断变体待真机验证。**

---

## 1. 现象

App Center → 右上角 `+` → 手动安装 → 选 `frpc-qnap_*.qpkg` → 只弹一句**「无法安装程序包」**，
没有任何细节，系统事件日志里也没有对应记录。1.0.0、1.0.1 两个版本表现一致。

同一个 NAS 上，用 QDK 2.5.3 官方模板生成的 `demo_0.1_arm_64.qpkg`（25 KB，未签名）
**可以正常安装**。这是整条排查线里最关键的一个对照。

---

## 2. 环境

| 项 | 值 |
|---|---|
| NAS | TS-428（Realtek RTD1296 / Cortex-A53，**64 位 ARM**） |
| 固件 | QTS 5.2.11.3661 |
| 打包环境 | WSL2 Ubuntu 24.04.4 + Go 1.27.3 + QDK 2.5.3 |
| 包 | `frpc-qnap_1.0.0_arm_64.qpkg`、`frpc-qnap_1.0.1_arm_64.qpkg`（均 8.71 MB） |

TS-428 的 CPU 架构已核对 QNAP 官方规格页：**64 位 ARM**，因此 `arm_64` 包选型正确，
「装错架构」不成立。

---

## 3. 做过的尝试（按时间顺序）

### R1 · 出第一版包

`make qpkg` 在 WSL 跑通，产出 `frpc-qnap_1.0.0_arm_64.qpkg`（8 705 163 B）。
真机安装失败，无提示、无日志。

### R2 · 静态核查（六条证据，排除包本身结构问题）

| # | 检查 | 结果 |
|---|---|---|
| 1 | `qbuild --verify` | 退出码 0 |
| 2 | `qbuild --query info` | 正确解析出 Name/Version/Packager/License/Summary |
| 3 | 与 QDK 模板包结构对比 | 两者同为「`#!/bin/sh` 头 + 控制 tar.gz + 数据 tar.gz + 100 字节尾部」，尾部格式 `[MODEL(10)\|RESERVED(40)\|FW_VERSION(10)\|NAME(20)\|VERSION(10)\|FLAG(10)]` 一致 |
| 4 | `qinstall.sh` 对未签名包的处理 | 找不到 `.qcodesigning` 时走「跳过」分支，不会中止 |
| 5 | 安装钩子写法 | 与 QDK 模板一致（`pkg_*` 函数 + `PKG_PRE_REMOVE="{...}"` 字符串） |
| 6 | `err_log` 行为 | 会调 `log_tool -t2` 写系统日志并 `exit 1`，**所以只要钩子跑过就必定留痕** |

由第 6 条推出：**「连日志都没有」本身就说明安装脚本根本没被执行**，拒绝发生在更前面。

### R3 · 加安装轨迹（1.0.1）

改动：
- `pkg_init` 把型号 / `uname -m` / 内核 / 固件版本 / `SYS_QPKG_DIR` 写入 `/tmp/frpc-qnap-install.log`
- `pkg_check_requirement` 放宽为接受 `armv8*`，失败消息带上实测 `uname -m`
- 版本号 1.0.0 → 1.0.1

真机安装**仍然失败**，且该日志文件是否存在尚未确认（关键待验证项）。

### R4 · 用户已排除的两项

- 签名开关：已开启「允许安装不具备有效数字签名的应用程序」
- 文件完整性：包在 NAS 上的 md5 与发布值一致

### R5 · 决定性对照：官方模板包能装

用同一条打包链（同一个 `qbuild`、同样的 arm_64）生成 `demo_0.1_arm_64.qpkg` 交用户测试，
**安装成功**。由此一次性排除：

- 签名策略（demo 同样未签名，能装）
- 文件传输/存储路径（同一个目录、同样的上传方式）
- 架构与机型（同机同架构能装）
- QTS 系统侧问题（进程锁、固件库损坏、Malware Remover 之类，若存在则 demo 也装不上）

**结论：问题在我们这个包的某个具体内容上。**

### R6 · 逐层对比 demo 与我们的包

两边结构完全同构，差异只有三处：

| 层 | demo | frpc-qnap 1.0.1 |
|---|---|---|
| 控制包成员 | `built_info` `qpkg.cfg` `qinstall.sh` `package_routines` | 同上 **+ `md5sum`** |
| qpkg.cfg 字段 | 6 个 | **21 个** |
| 数据包 | 只有 `./demo.sh`（633 B） | 22 MB：`frpc`、`frpc-qnap-manager`、`config/*.json`、`LICENSE.frp`、`.qpkg_icon.gif`、`frpc-qnap.sh` |

### R7 · 找到两个具体疑点

**疑点 A：`QPKG_VOLUME_SELECT="3"`（最高嫌疑）**

QDK 模板注释把这个字段解释为「卷选择」（0/1/2/3），但 **QDK 官方 GitBook 的解释完全不同**：

> QPKG_VOLUME_SELECT Optional. 0. Disable application routing rule. 1. Allow outgoing data packet apply
> to routing rule. 2. Allow incoming data packet bind to network interface. **(not ready)**
> 3. 1 + 2 **(not ready)**

即「应用路由规则」，且 2 与 3 标注为**未实现**。可安装的模板包没有这个字段，我们填了 `3`。

**疑点 B：`QPKG_WEBUI="/frpc-qnap"`（语义用错）**

官方定义：「Relative path to installed application's web interface（相对 web 根目录，通常是 `/share/Web`），
**必须以 `/` 开头**；当 `QPKG_WEB_PORT` 有值且 `QPKG_WEBUI` 为空时，安装期自动补 `/`」。
真实第三方包的写法是 `/owncloud/`、`/NewRelicInfraAgent/` 这种**带尾斜杠的 web 子目录**。
我们写成了 URL 前缀（而 URL 前缀本应由 `QPKG_PROXY_PATH` 承担），且没有尾斜杠。

### R8 · 生成四个二分变体

把「元数据」与「载荷」两条轴拆开，见 §6。

---

## 4. 已排除清单

| 假设 | 排除依据 |
|---|---|
| 包结构/签名格式损坏 | `qbuild --verify` 通过；与模板包逐字节比对同构 |
| 未签名被策略拦截 | 签名开关已开；未签名的 demo 能装 |
| 文件传坏了 | NAS 上 md5 与发布值一致 |
| 架构选错（应为 arm-x41 等） | TS-428 官方规格为 64 位 ARM；同架构 demo 能装 |
| NAS 系统侧问题 | demo 在同机同目录能装 |
| 安装钩子报错 | 钩子报错必经 `err_log` 写系统日志；日志为空 |
| 包名带连字符 `frpc-qnap` | 模板 demo 改名为 `control-demo_0.1_arm_64.qpkg` 后仍能装，说明 App Center 不按文件名与内部名强校验 |

---

## 5. 仍未排除的因素

| 因素 | 说明 |
|---|---|
| `QPKG_VOLUME_SELECT="3"` | 官方文档标注 not ready，语义在模板与 GitBook 之间矛盾 |
| `QPKG_WEBUI` 取值 | 语义是 web 根相对路径，不是 URL 前缀；缺尾斜杠 |
| 其余 16 个字段 | `QPKG_SERVICE_PORT`/`PIDFILE`/`TIMEOUT`/`USE_PROXY`/`PROXY_PATH`/`DESKTOP_APP*`/`VISIBLE`/`CONFIG` 等 |
| `md5sum` 控制成员 | 由 `QPKG_CONFIG` 触发，demo 没有 |
| 图标 | `.qpkg_icon.gif` 实际是 PNG（QDK 对 PNG 的标准做法：改名塞进去），结构校验 CRC 全通过 |
| 载荷大小 | 8.7 MB 包 / 22 MB 解包，demo 仅 25 KB |

---

## 6. 四个诊断变体（待真机验证）

| 顺序 | 变体 | 与正式包(1.0.1)的差异 | 若安装成功则说明 |
|---|---|---|---|
| 1 | **C1** | 仅删除 `QPKG_VOLUME_SELECT` | 该字段是根因 |
| 2 | **C2** | C1 + `QPKG_WEBUI` 补尾斜杠 `/frpc-qnap/` | WEBUI 格式是根因 |
| 3 | **A** | 元数据精简为模板同款 6 字段，载荷不变 | 元数据里还有其他字段不被接受 |
| 4 | **B** | A 再去掉图标 | 图标是根因 |

全都不行 → 问题在载荷（22 MB 二进制或 `config/` 目录），与元数据无关。

本地路径与校验值（`dist/` 已 gitignore，不入库）：

```
dist/diag/C1/frpc-qnap_1.0.2_arm_64.qpkg   8 705 169  md5 a3a15fe6cefd0865ab172cbb426c5a8f
dist/diag/C2/frpc-qnap_1.0.2_arm_64.qpkg   8 705 166  md5 a3ae29b82077e6f7fe49b06b70cee95a
dist/diag/A/frpc-qnap_1.0.2_arm_64.qpkg    8 705 211  md5 5d4d56e37f1cfee5be8e7d05e3837437
dist/diag/B/frpc-qnap_1.0.2_arm_64.qpkg    8 704 512  md5 27c5c6fb76c41fd571aece11fd447c88
```

生成方式见 [`../troubleshooting-install.md`](../troubleshooting-install.md) §6.1。

---

## 7. 下一步

1. 按 C1 → C2 → A → B 顺序试装，**试到能装就停**，把结果反馈
2. 同时确认 `/tmp/frpc-qnap-install.log` 是否存在（区分前置拒绝与安装期失败）
3. 定位后：把修正并回正式包 → 升版本 → 在 `preflight-qpkg.sh` 加一条「元数据字段白名单」检查
4. 之后才进入原本的 Phase 0（代理前缀、桌面窗口、重启自愈、端口占用）

---

## 8. 这轮踩到的坑（操作层面）

1. **QPKG 文件名参与架构解析**：必须保持 `<名字>_<版本>_<架构>.qpkg`。生成诊断包时我曾把它们
   重命名成 `diag-C1.qpkg`，这会直接让 App Center 认不出架构——比原问题更容易误导判断。
   现改为「每个变体一个目录，文件名保持规范」。
2. **QDK 自身的文档互相矛盾**：`QPKG_VOLUME_SELECT` 在 `shared/template/qpkg.cfg` 注释里是「卷选择」，
   在官方 GitBook 里是「路由规则」。遇到这类字段，以「真实可安装的第三方包怎么做」为准更可靠。
3. **qbuild 的 `--build-version` 会回写 `qpkg.cfg`**：`VERSION` 与 `QPKG_VER` 必须同步改，
   否则每次构建都会弄脏工作区。
4. **`qbuild --extract` 只解控制部分**：要看数据包得按 gzip 成员偏移自己切（`data.tar.gz` 是第二个成员）。
5. **无日志的前置拒绝只能二分**：QTS 侧不吐任何信息，唯一有效的办法是拿「已知能装的包」做对照，
   再逐层剥离变量。
