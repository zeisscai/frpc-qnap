#!/usr/bin/env bash
# 端到端回归：启动本地 frps，验证「配置 → 生成 TOML → 启动 frpc → 状态上报」全链路。
#
# 用法：
#   FRP_DIR=/path/to/frp_0.71.1_darwin_arm64 scripts/e2e.sh
#   FRP_DIR=/path/to/frp_0.71.1_linux_amd64 scripts/e2e.sh   # Linux CI
#
# 依赖：curl、python3（用于解析 JSON）、bash 4+
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
FRP_DIR=${FRP_DIR:-/tmp/frp_0.71.1_darwin_arm64}
MGR_PORT=${MGR_PORT:-17911}
ADMIN_PORT=${ADMIN_PORT:-17400}
FRPS_PORT=${FRPS_PORT:-17000}

TMP=$(mktemp -d)
MGR_PID=""
FRPS_PID=""

cleanup() {
	[ -n "$MGR_PID" ] && kill "$MGR_PID" 2>/dev/null || true
	[ -n "$FRPS_PID" ] && kill "$FRPS_PID" 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT

pass_count=0
fail_count=0

pass() { pass_count=$((pass_count + 1)); echo "  ✓ $1"; }
fail() { fail_count=$((fail_count + 1)); echo "  ✗ $1"; echo "    期望: $2"; echo "    实际: $3"; }

assert_eq() {
	if [ "$1" = "$2" ]; then pass "$3"; else fail "$3" "$2" "$1"; fi
}

assert_contains() {
	case "$1" in
		*"$2"*) pass "$3" ;;
		*) fail "$3" "包含 '$2'" "$1" ;;
	esac
}

jget() {
	python3 -c '
import json, sys
d = json.load(sys.stdin)
for k in sys.argv[1].split("."):
    if k == "":
        continue
    d = d[int(k)] if isinstance(d, list) else d[k]
print(d if d is not None else "")
' "$1"
}

code() { curl -s -o /dev/null -w "%{http_code}" "$@"; }

echo "==> 准备测试环境：$TMP"
[ -x "$FRP_DIR/frpc" ] || { echo "找不到 $FRP_DIR/frpc，请设置 FRP_DIR" >&2; exit 1; }
[ -x "$FRP_DIR/frps" ] || { echo "找不到 $FRP_DIR/frps，请设置 FRP_DIR" >&2; exit 1; }

cp "$FRP_DIR/frpc" "$FRP_DIR/frps" "$TMP/"
cat >"$TMP/frps.toml" <<EOF
bindPort = ${FRPS_PORT}
auth.method = "token"
auth.token = "e2e-token"
EOF

"$TMP/frps" -c "$TMP/frps.toml" >"$TMP/frps.log" 2>&1 &
FRPS_PID=$!

"$ROOT/build/frpc-qnap-manager" \
	-data "$TMP/data" -frpc "$TMP/frpc" \
	-listen "127.0.0.1:${MGR_PORT}" -admin-port "${ADMIN_PORT}" \
	>"$TMP/mgr.log" 2>&1 &
MGR_PID=$!

BASE="http://127.0.0.1:${MGR_PORT}"
CJ="$TMP/cookies.txt"

echo "==> 等待 manager 就绪"
for _ in $(seq 1 40); do
	curl -sf "$BASE/api/health" >/dev/null 2>&1 && break
	sleep 0.25
done

echo "==> 用例 1：健康检查与静态页面"
assert_eq "$(curl -s "$BASE/api/health" | jget status)" "ok" "health 返回 ok"
assert_eq "$(code "$BASE/")" "200" "首页可访问"
assert_contains "$(curl -s "$BASE/")" "frpc" "首页包含应用标识"

echo "==> 用例 2：登录"
PW=$(sed -n 's/.*口令：\([A-Za-z0-9]*\).*/\1/p' "$TMP/data/initial-password.txt")
[ -n "$PW" ] || { echo "未能读取初始口令" >&2; exit 1; }
assert_eq "$(code -X POST -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" "$BASE/api/login")" "200" "初始口令可登录"
assert_eq "$(code -X POST -H 'Content-Type: application/json' -d '{"password":"wrong"}' "$BASE/api/login")" "401" "错误口令被拒绝"

curl -s -c "$CJ" -X POST -H 'Content-Type: application/json' \
	-d "{\"password\":\"$PW\"}" "$BASE/api/login" >/dev/null

echo "==> 用例 3：未登录与 CSRF 防护"
assert_eq "$(code "$BASE/api/overview")" "401" "未登录访问 API 返回 401"
assert_eq "$(code -b "$CJ" -X PUT -H 'Content-Type: application/json' -d '{}' "$BASE/api/config")" "403" "缺少 CSRF 头被拒绝"

echo "==> 用例 4：保存配置并启动 frpc"
cat >"$TMP/cfg.json" <<EOF
{
  "serverAddr": "127.0.0.1",
  "serverPort": ${FRPS_PORT},
  "authToken": "e2e-token",
  "protocol": "tcp",
  "tlsEnable": true,
  "tcpMux": true,
  "logLevel": "info",
  "logMaxDays": 3,
  "loginFailExit": false,
  "proxies": [
    {"name":"qts-web","type":"tcp","localIP":"127.0.0.1","localPort":8080,"remotePort":18080,"remark":"NAS 管理界面"},
    {"name":"nas-ssh","type":"tcp","localIP":"127.0.0.1","localPort":22,"remotePort":18022}
  ]
}
EOF

RESP=$(curl -s -b "$CJ" -X PUT -H 'Content-Type: application/json' -H 'X-Requested-With: frpc-qnap' \
	--data-binary @"$TMP/cfg.json" "$BASE/api/config")
assert_eq "$(echo "$RESP" | jget started)" "True" "首次保存后 frpc 已启动"
assert_eq "$(echo "$RESP" | jget restarted)" "False" "首次保存未触发重启"

assert_contains "$(cat "$TMP/data/config/frpc.toml")" 'remotePort = 18080' "生成的 TOML 含远程端口"
assert_contains "$(cat "$TMP/data/config/frpc.toml")" '[[proxies]]' "生成的 TOML 含代理段"

echo "==> 用例 5：等待隧道 running"
STATUS=""
for _ in $(seq 1 40); do
	STATUS=$(curl -s -b "$CJ" "$BASE/api/proxies" | jget "proxies.0.status")
	[ "$STATUS" = "running" ] && break
	sleep 0.25
done
assert_eq "$STATUS" "running" "隧道状态上报为 running"
assert_eq "$(curl -s -b "$CJ" "$BASE/api/overview" | jget "counts.running")" "2" "概览统计有 2 条运行中"

echo "==> 用例 6：新增隧道走热重载（不重启进程）"
python3 - "$TMP/cfg.json" >"$TMP/cfg2.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
c["proxies"].append({"name": "dns", "type": "udp", "localIP": "127.0.0.1", "localPort": 53, "remotePort": 15353})
print(json.dumps(c))
PY
RESP=$(curl -s -b "$CJ" -X PUT -H 'Content-Type: application/json' -H 'X-Requested-With: frpc-qnap' \
	--data-binary @"$TMP/cfg2.json" "$BASE/api/config")
assert_eq "$(echo "$RESP" | jget reloaded)" "True" "代理变更触发热重载"

echo "==> 用例 7：公共参数变更触发重启"
python3 - "$TMP/cfg2.json" >"$TMP/cfg3.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
c["logLevel"] = "debug"
json.dump(c, sys.stdout)
PY
RESP=$(curl -s -b "$CJ" -X PUT -H 'Content-Type: application/json' -H 'X-Requested-With: frpc-qnap' \
	--data-binary @"$TMP/cfg3.json" "$BASE/api/config")
assert_eq "$(echo "$RESP" | jget restarted)" "True" "修改日志级别触发重启"

echo "==> 用例 8：非法配置被拒绝且不落盘"
BAD='{"serverAddr":"127.0.0.1","serverPort":7000,"protocol":"tcp","logLevel":"info","logMaxDays":3,"authToken":"","proxies":[{"name":"bad","type":"tcp","localIP":"127.0.0.1","localPort":8080,"remotePort":0}]}'
BODY=$(curl -s -b "$CJ" -X PUT -H 'Content-Type: application/json' -H 'X-Requested-With: frpc-qnap' -d "$BAD" "$BASE/api/config")
assert_contains "$BODY" "远程端口" "缺远程端口被拒绝并给出提示"
assert_eq "$(curl -s -b "$CJ" "$BASE/api/config" | jget "proxies.1.name")" "nas-ssh" "非法配置未覆盖已保存配置"

echo "==> 用例 9：日志与 QTS 前缀兼容"
assert_contains "$(curl -s -b "$CJ" "$BASE/api/logs?file=frpc&lines=50" | jget content)" "frpc" "能读到 frpc 日志"
assert_eq "$(code "$BASE/frpc-qnap/api/health")" "200" "兼容保留 /frpc-qnap 前缀的代理"
assert_eq "$(code "$BASE/frpc-qnap/")" "200" "兼容带前缀的首页访问"

echo "==> 用例 10：服务控制"
sleep 0.5
assert_eq "$(echo "$(curl -s -b "$CJ" -X POST -H 'X-Requested-With: frpc-qnap' -d '{}' "$BASE/api/service/stop")" | jget ok)" "True" "停止接口返回 ok"
assert_eq "$(curl -s -b "$CJ" "$BASE/api/overview" | jget "process.running")" "False" "停止后进程状态为未运行"
assert_eq "$(echo "$(curl -s -b "$CJ" -X POST -H 'X-Requested-With: frpc-qnap' -d '{}' "$BASE/api/service/start")" | jget ok)" "True" "启动接口返回 ok"

echo
echo "==> 结果：$pass_count 通过，$fail_count 失败"
[ "$fail_count" -eq 0 ] || {
	echo "--- manager.log ---"
	tail -30 "$TMP/mgr.log" 2>/dev/null || true
	exit 1
}
exit 0
