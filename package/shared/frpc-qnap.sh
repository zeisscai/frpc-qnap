#!/bin/sh
# frpc-qnap 服务脚本（由 QTS 在开机 / 启停 App 时调用）
#
# 职责：只负责 manager 进程的启停。frpc 子进程由 manager 自己守护，
# 这样 frpc 崩溃时不需要惊动 QTS，也不依赖 App Center 的按钮。

CONF=/etc/config/qpkg.conf
QPKG_NAME="frpc-qnap"
QPKG_DIR=$(/sbin/getcfg "${QPKG_NAME}" Install_Path -f "${CONF}")

PIDFILE="${QPKG_DIR}/frpc-qnap.pid"
LOGDIR="${QPKG_DIR}/logs"
MANAGER="${QPKG_DIR}/frpc-qnap-manager"
FRPC="${QPKG_DIR}/frpc"
LISTEN="127.0.0.1:7911"

is_running() {
	[ -f "${PIDFILE}" ] || return 1
	PID=$(cat "${PIDFILE}" 2>/dev/null)
	[ -n "${PID}" ] || return 1
	kill -0 "${PID}" 2>/dev/null
}

start_service() {
	ENABLED=$(/sbin/getcfg "${QPKG_NAME}" Enable -u -d FALSE -f "${CONF}")
	if [ "${ENABLED}" != "TRUE" ]; then
		echo "${QPKG_NAME} is disabled."
		exit 1
	fi

	if is_running; then
		echo "${QPKG_NAME} is already running (pid $(cat "${PIDFILE}"))."
		exit 0
	fi

	if [ ! -x "${MANAGER}" ]; then
		echo "manager binary not found: ${MANAGER}" >&2
		exit 1
	fi

	/bin/mkdir -p "${LOGDIR}"
	/bin/chmod 700 "${LOGDIR}" 2>/dev/null

	# 让 QTS 资源监视器把 frpc 子进程归到这个 App 名下。
	export QNAP_QPKG="${QPKG_NAME}"

	cd "${QPKG_DIR}" || exit 1
	"${MANAGER}" -data "${QPKG_DIR}" -frpc "${FRPC}" -listen "${LISTEN}" \
		>>"${LOGDIR}/manager.out" 2>&1 &
	echo $! >"${PIDFILE}"

	# 等接口就绪，最多 10 秒；失败不算致命错误，日志里能看到原因。
	i=0
	while [ "${i}" -lt 20 ]; do
		if [ -x /usr/bin/curl ] && /usr/bin/curl -sf -o /dev/null "http://${LISTEN}/api/health"; then
			break
		fi
		sleep 0.5
		i=$((i + 1))
	done

	echo "${QPKG_NAME} started (pid $(cat "${PIDFILE}"))."
	exit 0
}

stop_service() {
	if ! is_running; then
		echo "${QPKG_NAME} is not running."
		/bin/rm -f "${PIDFILE}"
		exit 0
	fi

	PID=$(cat "${PIDFILE}")
	kill "${PID}" 2>/dev/null

	i=0
	while [ "${i}" -lt 20 ]; do
		kill -0 "${PID}" 2>/dev/null || break
		sleep 1
		i=$((i + 1))
	done

	if kill -0 "${PID}" 2>/dev/null; then
		echo "${QPKG_NAME} did not exit in time, killing."
		kill -9 "${PID}" 2>/dev/null
	fi

	/bin/rm -f "${PIDFILE}"
	echo "${QPKG_NAME} stopped."
	exit 0
}

case "$1" in
	start)
		start_service
		;;
	stop)
		stop_service
		;;
	restart)
		stop_service
		sleep 1
		start_service
		;;
	status)
		if is_running; then
			echo "${QPKG_NAME} is running (pid $(cat "${PIDFILE}"))."
			exit 0
		fi
		echo "${QPKG_NAME} is not running."
		exit 1
		;;
	*)
		echo "Usage: $0 {start|stop|restart|status}"
		exit 1
		;;
esac
