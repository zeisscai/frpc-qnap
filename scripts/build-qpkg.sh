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

echo "==> 同步前端资源到 package/shared/web"
rm -rf "${ROOT}/package/shared/web"
mkdir -p "${ROOT}/package/shared/web"
cp -R "${ROOT}/internal/webui/assets/." "${ROOT}/package/shared/web/"

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
