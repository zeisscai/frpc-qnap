#!/bin/sh
# QPKG 打包源预检：在 qbuild 之前把关，把 qbuild 的晦涩报错前置成可读检查。
#
# 用法：scripts/preflight-qpkg.sh [arm_64]
# 退出码：0 = 可以打包；1 = 有致命问题（必须修）；2 = 只有警告
#
# 检查项全部来自 QDK 2.5.3 的实际行为（shared/bin/qbuild、shared/scripts/qinstall.sh），
# 不是推测。见 docs/records/2026-11-03-wsl-env.md。
set -u

ARCH="${1:-arm_64}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}/package" || exit 1

fatal=0
warn=0

ok()   { printf '  [ok]   %s\n' "$*"; }
bad()  { printf '  [FAIL] %s\n' "$*"; fatal=$((fatal + 1)); }
warn() { printf '  [warn] %s\n' "$*"; warn=$((warn + 1)); }

has_crlf() {
	# package_routines / *.sh 会被 QTS 用 busybox sh 执行，CRLF 会让安装直接失败。
	LC_ALL=C grep -q "$(printf '\r')" "$1" 2>/dev/null
}

echo "==> 预检 QPKG 打包源（root=${ROOT}/package, arch=${ARCH}）"

# ---------------------------------------------------------------- 构建依赖
echo "--- 构建依赖"
for tool in rsync md5sum cp tar gzip sed awk; do
	if command -v "${tool}" >/dev/null 2>&1; then
		ok "命令存在：${tool}"
	else
		bad "缺少命令：${tool}（qbuild 依赖它；Debian/Ubuntu: sudo apt install -y ${tool}）"
	fi
done
if command -v qbuild >/dev/null 2>&1; then
	ok "qbuild：$(command -v qbuild)"
else
	bad "找不到 qbuild，先装 QDK（scripts/wsl-setup.sh）"
fi

# ---------------------------------------------------------------- 元数据
echo "--- 元数据"
[ -f qpkg.cfg ] && ok "qpkg.cfg" || bad "缺少 qpkg.cfg"
[ -f package_routines ] && ok "package_routines" || bad "缺少 package_routines"
[ -f package_routines ] && has_crlf package_routines && bad "package_routines 含 CRLF，QTS 会执行失败（见 .gitattributes）"

for f in qpkg.cfg package_routines; do
	[ -f "$f" ] || continue
	has_crlf "$f" && bad "$f 含 CRLF（应统一 LF）"
done

if [ -f qpkg.cfg ]; then
	# qbuild 只 sed 形如 QPKG_NAME="xxx" 的单行定义
	for field in QPKG_NAME QPKG_VER QPKG_AUTHOR QPKG_SERVICE_PROGRAM; do
		val=$(sed -n "s/^${field}=\"\(.*\)\"/\1/p" qpkg.cfg)
		if [ -n "${val}" ]; then
			ok "${field}=\"${val}\""
		else
			bad "qpkg.cfg 里 ${field} 未赋值或格式不是 ${field}=\"value\""
		fi
	done
fi

QPKG_NAME=$(sed -n 's/^QPKG_NAME="\(.*\)"/\1/p' qpkg.cfg 2>/dev/null)
QPKG_SERVICE_PROGRAM=$(sed -n 's/^QPKG_SERVICE_PROGRAM="\(.*\)"/\1/p' qpkg.cfg 2>/dev/null)

# ---------------------------------------------------------------- 架构目录
echo "--- 架构文件（package/${ARCH}/）"
if [ -d "${ARCH}" ]; then
	ok "目录存在：${ARCH}/"
else
	bad "缺少目录 package/${ARCH}/，先跑 make ${ARCH}"
fi
for f in frpc-qnap-manager frpc; do
	if [ -f "${ARCH}/${f}" ]; then
		ok "${ARCH}/${f}"
	else
		bad "缺少 ${ARCH}/${f}（make ${ARCH} 会生成/下载）"
	fi
done

# 架构必须与包的 arch 对得上，否则装到 NAS 上是 exec format error。
# 注意 GNU file 对 Go 产物的措辞：linux/arm64 → "ARM aarch64"，linux/amd64 → "x86-64"，
# 大小写不统一，所以统一转小写再匹配。
expect_machine() {
	case "$1" in
		arm_64) echo "aarch64" ;;
		x86_64) echo "x86-64" ;;
		*) echo "" ;;
	esac
}
want="$(expect_machine "${ARCH}")"
if command -v file >/dev/null 2>&1; then
	for f in frpc-qnap-manager frpc; do
		[ -f "${ARCH}/${f}" ] || continue
		desc=$(file -b "${ARCH}/${f}")
		low=$(printf '%s' "${desc}" | tr 'A-Z' 'a-z')
		case "${low}" in
			*elf*) : ;;
			*) bad "${ARCH}/${f} 不是 ELF（${desc}）"; continue ;;
		esac
		case "${low}" in
			*"${want}"*) ok "${ARCH}/${f} 架构匹配（${want}）" ;;
			*) bad "${ARCH}/${f} 架构不匹配，期望 ${want}，实际：${desc}" ;;
		esac
	done
else
	warn "没有 file 命令，跳过 ELF 架构校验（sudo apt install -y file）"
fi

# ---------------------------------------------------------------- 公共文件
echo "--- 公共文件（package/shared/）"
if [ -n "${QPKG_SERVICE_PROGRAM}" ]; then
	if [ -f "shared/${QPKG_SERVICE_PROGRAM}" ]; then
		ok "服务脚本：shared/${QPKG_SERVICE_PROGRAM}"
		has_crlf "shared/${QPKG_SERVICE_PROGRAM}" && bad "shared/${QPKG_SERVICE_PROGRAM} 含 CRLF，NAS 上启停会失败"
	else
		bad "QPKG_SERVICE_PROGRAM=\"${QPKG_SERVICE_PROGRAM}\" 但 shared/${QPKG_SERVICE_PROGRAM} 不存在"
	fi
fi

# ---------------------------------------------------------------- 配置保留
# qbuild 的 add_config_data：相对路径要求数据包里已有同名文件（来自 shared/ 或 <arch>/），
# 否则 err_msg 直接 exit 1；绝对路径则要求 package/config/ 下有对应文件。
echo "--- QPKG_CONFIG 路径约定"
qpkg_config=$(sed -n 's/^QPKG_CONFIG="\(.*\)"/\1/p' qpkg.cfg 2>/dev/null)
if [ -z "${qpkg_config}" ]; then
	warn "qpkg.cfg 未声明 QPKG_CONFIG，升级时用户配置不会被保留"
else
	for f in ${qpkg_config}; do
		case "${f}" in
			/*)
				src="config/${f#/}"
				if [ -f "${src}" ]; then
					ok "绝对路径 ${f} ← package/${src}"
				else
					bad "QPKG_CONFIG=\"${f}\"：qbuild 要求默认文件在 package/${src}"
				fi
				;;
			*)
				if [ -f "config/${f}" ]; then
					ok "${f} ← package/config/${f}（qbuild 会拷到安装根目录）"
				elif [ -f "shared/${f}" ] || [ -f "${ARCH}/${f}" ]; then
					ok "${f} ← 数据包内已存在（shared/ 或 ${ARCH}/）"
				else
					bad "QPKG_CONFIG=\"${f}\" 找不到默认文件：既不在 package/config/${f}，也不在 package/shared/${f} 或 package/${ARCH}/${f}（qbuild 会报 'no such file; check your QPKG_CONFIG settings' 并中止）"
				fi
				;;
		esac
	done
fi

# ---------------------------------------------------------------- 图标
# qbuild 只认 ${QPKG_NAME}.gif/png、${QPKG_NAME}_80.*、${QPKG_NAME}_gray.*
echo "--- 图标"
if [ -d icons ]; then
	found=0
	for ext in png gif; do
		[ -f "icons/${QPKG_NAME}.${ext}" ] && { ok "icons/${QPKG_NAME}.${ext}"; found=1; }
	done
	[ "${found}" -eq 0 ] && warn "icons/ 里没有 ${QPKG_NAME}.png|gif，App Center 会显示默认图标"
	for f in icons/*; do
		[ -f "$f" ] || continue
		base=$(basename "$f")
		case "${base}" in
			"${QPKG_NAME}.png"|"${QPKG_NAME}.gif"|"${QPKG_NAME}_80.png"|"${QPKG_NAME}_80.gif"|"${QPKG_NAME}_gray.png"|"${QPKG_NAME}_gray.gif") : ;;
			*) warn "icons/${base} 命名不符合 QDK 约定（必须是 ${QPKG_NAME}.png 等），打包时会被忽略" ;;
		esac
	done
else
	warn "没有 package/icons/ 目录"
fi

# ---------------------------------------------------------------- 结论
echo
if [ "${fatal}" -gt 0 ]; then
	echo "==> 预检失败：${fatal} 个致命问题，${warn} 个警告。修完再打包。"
	exit 1
fi
if [ "${warn}" -gt 0 ]; then
	echo "==> 预检通过（${warn} 个警告）。"
	exit 2
fi
echo "==> 预检通过。"
exit 0
