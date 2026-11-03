#!/bin/sh
# 在 Linux（含 WSL）上用 QDK 打包 QPKG。
#
# 前置条件：
#   1. 安装 QDK：https://github.com/qnap-dev/QDK/releases
#      sudo apt install ./qdk_2.5.3_amd64.deb
#   2. 已执行 make arm64（生成 manager 与 frpc 二进制）
#
# 用法：scripts/build-qpkg.sh [arm_64]
set -eu

ARCH="${1:-arm_64}"
VERSION="$(cat VERSION 2>/dev/null || echo 1.0.0)"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if ! command -v qbuild >/dev/null 2>&1; then
	echo "找不到 qbuild。请先安装 QNAP QDK（仅支持 Linux x86_64）。" >&2
	exit 1
fi

if [ ! -f "${ROOT}/package/${ARCH}/frpc-qnap-manager" ]; then
	echo "缺少 package/${ARCH}/frpc-qnap-manager，请先运行：make ${ARCH%%_*}" >&2
	exit 1
fi
if [ ! -f "${ROOT}/package/${ARCH}/frpc" ]; then
	echo "缺少 package/${ARCH}/frpc，请先运行：make fetch-frp" >&2
	exit 1
fi

echo "==> 同步前端资源到 package/shared/web"
rm -rf "${ROOT}/package/shared/web"
mkdir -p "${ROOT}/package/shared/web"
cp -R "${ROOT}/internal/webui/assets/." "${ROOT}/package/shared/web/"

mkdir -p "${ROOT}/dist"
cd "${ROOT}/package"
echo "==> qbuild --build-arch ${ARCH} --build-version ${VERSION}"
qbuild --build-arch "${ARCH}" --build-version "${VERSION}" --build-dir "${ROOT}/dist"

echo "==> 产物："
ls -lh "${ROOT}/dist"
