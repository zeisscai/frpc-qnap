#!/bin/sh
# 在 Linux（含 WSL）上用 QDK 打包 QPKG。
#
# 前置条件：
#   1. 安装 QDK：https://github.com/qnap-dev/QDK/releases
#      scripts/wsl-setup.sh 会一并装好依赖与 QDK
#   2. 已执行 make arm64（生成 manager 与 frpc 二进制）
#
# 用法：scripts/build-qpkg.sh [arm_64]
set -eu

ARCH="${1:-arm_64}"
VERSION="$(cat VERSION 2>/dev/null || echo 1.0.0)"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# 不同 ARCH 对应的 make 目标名（arm_64 → make arm64）
case "${ARCH}" in
	arm_64) make_target="arm64" ;;
	x86_64) make_target="x86_64" ;;
	*) echo "ARCH 只支持 arm_64 / x86_64，收到：${ARCH}" >&2; exit 1 ;;
esac

if ! command -v qbuild >/dev/null 2>&1; then
	echo "找不到 qbuild。请先安装 QNAP QDK（仅支持 Linux x86_64）：" >&2
	echo "  sh scripts/wsl-setup.sh" >&2
	exit 1
fi

if [ ! -f "${ROOT}/package/${ARCH}/frpc-qnap-manager" ] || [ ! -f "${ROOT}/package/${ARCH}/frpc" ]; then
	echo "缺少 package/${ARCH}/ 下的二进制，请先运行：make ${make_target}" >&2
	exit 1
fi

# 包内权限必须由构建决定，不能继承检出/复制时的权限。
# 典型场景：仓库从 /mnt/c 拷进 WSL 时 drvfs 会把所有文件标成 777，
# 于是 NAS 上出现 world-writable 的可执行文件与配置文件。
echo "==> 归一化包内文件权限（644，可执行 755，目录 755）"
find "${ROOT}/package/${ARCH}" -type d -exec chmod 755 {} +
find "${ROOT}/package/${ARCH}" -type f -exec chmod 644 {} +
chmod 755 "${ROOT}/package/${ARCH}/frpc-qnap-manager" "${ROOT}/package/${ARCH}/frpc"
find "${ROOT}/package/shared" -type d -exec chmod 755 {} +
find "${ROOT}/package/shared" -type f -exec chmod 644 {} +
find "${ROOT}/package/shared" -type f -name '*.sh' -exec chmod 755 {} +
chmod 644 "${ROOT}/package/qpkg.cfg" "${ROOT}/package/package_routines"
if [ -d "${ROOT}/package/icons" ]; then
	find "${ROOT}/package/icons" -type d -exec chmod 755 {} +
	find "${ROOT}/package/icons" -type f -exec chmod 644 {} +
fi

# 注意：前端资源已用 //go:embed 编进 manager（internal/webui/assets），
# 非 dev 模式不读磁盘上的 web/ 目录，所以不再往包里放一份副本。

# qbuild 的报错信息很少（配置路径写错只说 "no such file"），先在前面把关。
# 退出码 2 = 只有警告，可以继续。
status=0
sh "${ROOT}/scripts/preflight-qpkg.sh" "${ARCH}" || status=$?
if [ "${status}" -eq 1 ]; then
	echo "预检未通过，已中止打包。" >&2
	exit 1
fi

mkdir -p "${ROOT}/dist"
cd "${ROOT}/package"
echo "==> qbuild --build-arch ${ARCH} --build-version ${VERSION}"
qbuild --build-arch "${ARCH}" --build-version "${VERSION}" --build-dir "${ROOT}/dist"

echo "==> 产物："
ls -lh "${ROOT}/dist"
