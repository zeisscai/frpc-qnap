#!/bin/sh
# 下载并校验指定版本的 frpc 二进制。
#
# 用法：scripts/fetch-frp.sh [frp版本] [arm64|amd64] [输出目录]
# 例：  scripts/fetch-frp.sh 0.71.1 arm64 package/arm_64
set -eu

VERSION="${1:-0.71.1}"
ARCH="${2:-arm64}"
DEST="${3:-package/arm_64}"

case "${ARCH}" in
	arm64|amd64) ;;
	*)
		echo "不支持的架构：${ARCH}（只接受 arm64 或 amd64）" >&2
		exit 1
		;;
esac

BASE="https://github.com/fatedier/frp/releases/download/v${VERSION}"
TARBALL="frp_${VERSION}_linux_${ARCH}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

echo "==> 下载 ${TARBALL}"
curl -fL --retry 3 --connect-timeout 15 -o "${TMP}/${TARBALL}" "${BASE}/${TARBALL}"
curl -fL --retry 3 --connect-timeout 15 -o "${TMP}/sums.txt" "${BASE}/frp_sha256_checksums.txt"

echo "==> 校验 SHA256"
EXPECTED=$(grep " ${TARBALL}\$" "${TMP}/sums.txt" | awk '{print $1}')
if [ -z "${EXPECTED}" ]; then
	echo "校验文件中找不到 ${TARBALL}，可能版本号有误。" >&2
	exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	ACTUAL=$(sha256sum "${TMP}/${TARBALL}" | awk '{print $1}')
else
	ACTUAL=$(shasum -a 256 "${TMP}/${TARBALL}" | awk '{print $1}')
fi
if [ "${EXPECTED}" != "${ACTUAL}" ]; then
	echo "校验失败：期望 ${EXPECTED}，实际 ${ACTUAL}" >&2
	exit 1
fi

echo "==> 解包"
tar -xzf "${TMP}/${TARBALL}" -C "${TMP}"
mkdir -p "${DEST}"
install -m 755 "${TMP}/frp_${VERSION}_linux_${ARCH}/frpc" "${DEST}/frpc"
# Apache-2.0 合规：分发二进制时一并提供上游许可与版权声明。
install -m 644 "${TMP}/frp_${VERSION}_linux_${ARCH}/LICENSE" "${DEST}/LICENSE.frp"

echo "==> 完成：${DEST}/frpc（frp ${VERSION} / linux_${ARCH}）"
"${DEST}/frpc" -v 2>/dev/null || true
