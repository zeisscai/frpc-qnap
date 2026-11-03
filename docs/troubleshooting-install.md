# 安装失败排查：QTS 提示「无法安装程序包」，没有其他提示、日志里也没有

> 适用：App Center → 右上角 `+` → 手动安装，选完 `.qpkg` 后只弹一句失败，没有细节。
> 本文按「先定性、再定位」的顺序组织，每一步都给出判据，避免靠猜。

---

## 0. 先分清两种失败

QPKG 的安装分两个阶段，失败形态完全不同：

| 阶段 | 发生在哪 | 是否执行 `package_routines` | 典型表现 |
|---|---|---|---|
| **A. 前置校验** | App Center 内部（C 程序） | 否 | 只给通用文案，系统日志常常是空的 |
| **B. 安装执行** | QTS 调用包内 `qinstall.sh` → 我们的钩子 | 是 | 通常会有系统日志；钩子里的 `err_log` 会写 `log_tool` |

**判据**：手动安装失败后，用管理员 SSH 登录 NAS 执行

```bash
ls -l /tmp/frpc-qnap-install.log && cat /tmp/frpc-qnap-install.log
```

- 文件**存在** → 属于 B，问题在钩子或解包，日志里有 hook 打印的型号/架构/固件版本
- 文件**不存在** → 属于 A，根本没走到安装脚本，往下看第 1–4 条
- （该文件从 1.0.1 起由 `pkg_init` 写入；1.0.0 没有，所以 1.0.0 的失败无法用这个判据）

---

## 1. 最常见：未开启「允许安装不具备有效数字签名的应用程序」

第三方 QPKG 没有 QNAP 签名，QTS 5.x 默认拒绝安装，且错误文案恰好就是一句
`Unable to install the Package` / 「无法安装程序包」。

**操作**：App Center → 右上角齿轮 **设置** → **常规** 标签 → 勾选
**「允许安装不具备有效数字签名的应用程序」** → **应用**。

依据：QNAP 官方 FAQ《What to do if an app does not have a digital QNAP signature?》；
第三方厂商支持文档也把「出现 Unable to install the Package 就是没开这个开关」列为固定结论。

> 注意：该开关在 QTS 5.0.1 起默认关闭；某些固件把它放在「App Center → 设置 → 一般」，
> 或需要先登录 `admin` 组账号才可见。

---

## 2. 文件在传输中损坏

`.qpkg` 尾部 100 字节里带了一段由 `qpkg_encrypt` 写入的数值，包内容一变就对不上；
经 SMB/浏览器下载再上传尤其容易出问题（截断、被安全软件改写）。

**在 NAS 上校验**（把路径换成实际位置）：

```bash
md5sum /share/Public/frpc-qnap_1.0.1_arm_64.qpkg
```

应与发布值一致：`f91945c48a12fbc77f845fdb209b9637`（1.0.1，8 705 176 字节）。不一致就重新传输，不要继续往下排查。

---

## 3. 架构与型号

TS-428 是 Realtek RTD1296 / Cortex-A53，**64 位 ARM**，对应 `arm_64` 包，选型正确。

**在 NAS 上确认**：

```bash
uname -m          # 应为 aarch64
/sbin/getcfg System Model
/sbin/getcfg System Version
```

- `uname -m` 输出 `aarch64` → 架构没问题
- 输出 `armv7l` → 说明这台机器跑的是 32 位用户态，`arm_64` 包装不上（本项目不支持 32 位 ARM）

---

## 4. 系统侧的「装不上且无报错」

QNAP 官方 FAQ《What to do if the APPs can't be installed and update in App center (no error messages)》
列了三种成因，都属于 NAS 环境而非包本身：

1. NAS 被某个进程锁住 → **重启 NAS**（App Center 与安装锁的常见解法）
2. QTS 固件里的库缺失或损坏 → **重装/升级同版本固件**
3. App Center 里有异常应用、未安装 Malware Remover → 装上 Malware Remover 后重试

如果第 1–3 条都排除了仍然失败，按这条走。

---

## 5. 拿到真实错误输出

App Center 的 GUI 会吞掉细节。要看到 stdout/stderr：

```bash
# 1) 把包放到 NAS 的公共目录，SSH 上去
cd /share/Public

# 2) 直接执行（QPKG 本质是自解压 shell 脚本，会打印安装过程）
sudo sh frpc-qnap_1.0.1_arm_64.qpkg
```

终端里会看到 `qinstall.sh` 的每一步输出。**这是最快定位 B 类失败的方式**，
比翻日志快得多（QNAP 官方不建议日常这样装，但用于诊断完全够用）。

其他日志入口：

| 位置 | 看什么 |
|---|---|
| QuLog Center → 系统事件日志 | 筛选来源 `App Center` / `System`，按安装时间点找 |
| `/var/log/messages` | `tail -f` 后重试安装，实时看 |
| `/tmp/frpc-qnap-install.log` | 我们钩子写的轨迹（1.0.1 起） |

---

## 6. 对照实验：用官方模板包判断「是不是我们的包的问题」

`dist/control-demo_0.1_arm_64.qpkg` 是用 **QDK 2.5.3 官方模板**原样生成的最小包
（25 KB，无前端、无二进制、只有一个空的服务脚本），签名状态与我们相同（未签名）。

| 现象 | 结论 |
|---|---|
| demo 也装不上 | 与我们的包无关：签名开关（第 1 条）或 NAS 环境（第 4 条） |
| demo 能装、frpc-qnap 装不上 | 问题在我们的元数据或包内容，进 §6.1 的阶梯 |

装完 demo 可以直接在 App Center 卸载，无残留。

### 6.1 诊断包阶梯（demo 能装、我们的不能装时用）

已实测：`control-demo` 能装、`frpc-qnap` 不能装，说明签名策略、文件完整性、架构都已排除，
差异只剩「元数据字段」和「载荷内容」两块。下面四个变体把这两块拆开，按顺序试，**试到能装就停**：

| 顺序 | 变体 | 与正式包的差异 | 装上了说明什么 |
|---|---|---|---|
| 1 | `dist/diag/C1/` | 仅删除 `QPKG_VOLUME_SELECT="3"` | **这就是根因**（该字段值 3 在 QDK 官方 GitBook 里标注为 not ready） |
| 2 | `dist/diag/C2/` | C1 + `QPKG_WEBUI` 补尾斜杠 `/frpc-qnap/` | 根因是 WEBUI 路径格式 |
| 3 | `dist/diag/A/` | 元数据精简成模板同款 6 个字段，载荷不变 | 元数据里还有其他字段不被接受 |
| 4 | `dist/diag/B/` | A 再去掉图标 | 图标（PNG 改名成 .gif）是根因 |

全都不行 → 问题在载荷（22 MB 二进制或 `config/` 目录），与元数据无关。

> **安装时不要改文件名**。App Center 从文件名解析 `<名字>_<版本>_<架构>`，
> 改成 `diag-C1.qpkg` 会让它认不出架构。每个变体都放在独立目录里，文件名都是
> `frpc-qnap_1.0.2_arm_64.qpkg`，直接用原文件名安装。

四个变体都由 `qbuild` 用同一套 QDK 重新生成，内部版本号 1.0.2，`QPKG_NAME` 仍是 `frpc-qnap`，
装成功后可以正常在 App Center 卸载。

复现命令（在 WSL 里，以 C1 为例）：

```bash
cp -a ~/frpc-qnap/package /tmp/C1 && cd /tmp/C1
sed -i '/^QPKG_VOLUME_SELECT=/d' qpkg.cfg
find . -type d -exec chmod 755 {} +; find . -type f -exec chmod 644 {} +
chmod 755 arm_64/frpc arm_64/frpc-qnap-manager shared/frpc-qnap.sh
qbuild --build-arch arm_64 --build-version 1.0.2 --build-dir /tmp/out
```

---

## 7. 反馈时请附上

1. `md5sum` 结果
2. `uname -m`、`getcfg System Model`、`getcfg System Version` 的输出
3. `/tmp/frpc-qnap-install.log` 是否存在及内容
4. 第 5 条终端安装的完整输出
5. 是否已勾选签名开关、是否重启过 NAS

有这五项基本可以一次定位。
