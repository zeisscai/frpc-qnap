#!/bin/sh
# 在 WSL（或任意 Debian/Ubuntu）里一次装好打包所需的环境。
#
#   sh scripts/wsl-setup.sh            # 装依赖 + Go + QDK
#   sh scripts/wsl-setup.sh --no-qdk   # 只装依赖与 Go（不想装 QDK）
#   QDK_VERSION=2.5.3 GO_VERSION=1.25.3 sh scripts/wsl-setup.sh
#
# 幂等：已经满足的步骤会跳过。全部只动系统包与 /usr/local/go，不碰仓库内容。
set -eu

QDK_VERSION="${QDK_VERSION:-2.5.3}"
GO_MIN_MINOR=22          # 项目需要 Go >= 1.22（http.ServeMux 的 "GET /path" 与 r.PathValue）
GO_VERSION="${GO_VERSION:-}"   # 留空则取 go.dev 上的最新稳定版
WANT_QDK=1
[ "${1:-}" = "--no-qdk" ] && WANT_QDK=0

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok()  { printf '  [ok]   %s\n' "$*"; }
die() { printf '  [FAIL] %s\n' "$*" >&2; exit 1; }

# ------------------------------------------------------------------ 0. 基本判断
say "环境检查"
[ "$(uname -s)" = "Linux" ] || die "这个脚本只能在 Linux/WSL 里跑，当前：$(uname -s)"

case "$(uname -r)" in
	*microsoft*|*Microsoft*|*WSL*) ok "运行在 WSL 中（$(uname -r)）" ;;
	*) ok "运行在 Linux 中（$(uname -r)），非 WSL 也可继续" ;;
esac

[ -r /etc/os-release ] || die "缺少 /etc/os-release，无法判断发行版"
. /etc/os-release
case "${ID:-}${ID_LIKE:-}" in
	*debian*|*ubuntu*) ok "发行版：${PRETTY_NAME:-unknown}" ;;
	*) die "本脚本按 Debian/Ubuntu 写（用 apt）。当前是 ${PRETTY_NAME:-unknown}，请手动安装 qbuild 与 rsync。" ;;
esac

if [ "$(id -u)" -eq 0 ]; then
	SUDO=""
elif command -v sudo >/dev/null 2>&1; then
	SUDO="sudo"
else
	die "既不是 root 也没有 sudo，无法安装系统包"
fi

# 仓库在 /mnt/c 或 /mnt/d 下时，WSL 的 9p 文件系统会让 go build 慢好几倍，提前说清楚。
case "${ROOT}" in
	/mnt/*)
		printf '  [warn] 仓库位于 %s（Windows 盘）。\n' "${ROOT}"
		printf '         WSL 访问 /mnt/* 走 9p，go build 与 rsync 会明显变慢，建议：\n'
		printf '           cp -a "%s" ~/frpc-qnap && cd ~/frpc-qnap\n' "${ROOT}"
		;;
	*) ok "仓库路径：${ROOT}" ;;
esac

# ------------------------------------------------------------------ 1. 系统依赖
say "安装系统依赖（rsync / curl / file / make）"
# qbuild 内部用 rsync 组装数据包、md5sum 记录配置校验值；缺 rsync 是 WSL 上最常见的坑。
MISSING=""
for pkg in rsync curl ca-certificates file make; do
	dpkg -s "${pkg}" >/dev/null 2>&1 || MISSING="${MISSING} ${pkg}"
done
if [ -n "${MISSING}" ]; then
	${SUDO} env DEBIAN_FRONTEND=noninteractive apt-get update -qq
	${SUDO} env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ${MISSING}
	ok "已安装：${MISSING# }"
else
	ok "依赖齐全"
fi
command -v rsync >/dev/null 2>&1 || die "rsync 仍不可用，qbuild 无法组装数据包"

# ------------------------------------------------------------------ 2. Go
say "检查 Go（需要 >= 1.${GO_MIN_MINOR}）"
go_ok() {
	command -v go >/dev/null 2>&1 || return 1
	# 输出形如 go version go1.24.2 linux/amd64
	ver=$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')
	major=${ver%%.*}
	rest=${ver#*.}
	minor=${rest%%.*}
	[ "${major}" -gt 1 ] && return 0
	[ "${major}" -eq 1 ] && [ "${minor}" -ge "${GO_MIN_MINOR}" ]
}

if go_ok; then
	ok "已满足：$(go version)"
else
	if [ -z "${GO_VERSION}" ]; then
		say "查询 go.dev 上的最新稳定版"
		GO_VERSION=$(curl -fsSL --retry 3 --connect-timeout 15 'https://go.dev/VERSION?m=text' | head -n1 | sed 's/^go//')
		[ -n "${GO_VERSION}" ] || die "查询 Go 版本失败；可显式指定：GO_VERSION=1.25.3 sh scripts/wsl-setup.sh"
	fi
	arch=$(dpkg --print-architecture)   # amd64 / arm64
	tarball="go${GO_VERSION}.linux-${arch}.tar.gz"
	say "安装 Go ${GO_VERSION}（${arch}）到 /usr/local/go"
	tmp=$(mktemp -d)
	trap 'rm -rf "${tmp}"' EXIT
	curl -fL --retry 3 --connect-timeout 15 -o "${tmp}/${tarball}" "https://go.dev/dl/${tarball}"
	${SUDO} rm -rf /usr/local/go
	${SUDO} tar -C /usr/local -xzf "${tmp}/${tarball}"
	# 写进 /etc/profile.d，交互式登录与 `wsl -e` 都能拿到
	if ! grep -qs '/usr/local/go/bin' /etc/profile.d/go.sh 2>/dev/null; then
		printf 'export PATH=$PATH:/usr/local/go/bin\n' | ${SUDO} tee /etc/profile.d/go.sh >/dev/null
	fi
	PATH="${PATH}:/usr/local/go/bin"
	export PATH
	go_ok || die "Go 安装后仍不可用，手动检查 /usr/local/go/bin/go"
	ok "已安装：$(go version)"
fi
printf '         提示：非登录 shell 若找不到 go，执行 export PATH=$PATH:/usr/local/go/bin\n'

# ------------------------------------------------------------------ 3. QDK
if [ "${WANT_QDK}" -eq 1 ]; then
	say "安装 QNAP QDK ${QDK_VERSION}"
	if command -v qbuild >/dev/null 2>&1; then
		ok "已安装：$(command -v qbuild)"
	else
		arch=$(dpkg --print-architecture)
		if [ "${arch}" != "amd64" ]; then
			# QDK 官方只发 x86_64 的 deb；ARM 版 WSL 跑不起来
			die "QDK 只提供 amd64 包，当前 WSL 是 ${arch}。请换 x86_64 的机器/WSL，或用 x86 虚拟机打包。"
		fi
		deb="qdk_${QDK_VERSION}_amd64.deb"
		tmp=$(mktemp -d)
		trap 'rm -rf "${tmp}"' EXIT
		URL="https://github.com/qnap-dev/QDK/releases/download/v${QDK_VERSION}/${deb}"
		say "下载 ${URL}"
		curl -fL --retry 3 --connect-timeout 15 -o "${tmp}/${deb}" "${URL}"
		${SUDO} env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${tmp}/${deb}"
		hash -r 2>/dev/null || true
		command -v qbuild >/dev/null 2>&1 || die "QDK 装完了但找不到 qbuild，检查 /usr/bin/qbuild 是否是指向 /usr/share/QDK/bin/qbuild 的软链"
		ok "已安装：$(command -v qbuild)"
	fi
else
	say "跳过 QDK（--no-qdk）"
fi

# ------------------------------------------------------------------ 4. 自查
say "自查"
for tool in go rsync md5sum; do
	command -v "${tool}" >/dev/null 2>&1 && ok "${tool}" || die "缺少 ${tool}"
done
if [ "${WANT_QDK}" -eq 1 ]; then
	qbuild --version 2>&1 | head -n1 || true
fi

printf '\n下一步：\n'
printf '  cd "%s"\n' "${ROOT}"
printf '  make arm64          # 交叉编译 manager + 下载 frpc（linux/arm64）\n'
printf '  make qpkg           # 预检 + qbuild，产物在 dist/\n'
