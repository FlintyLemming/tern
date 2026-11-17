#!/bin/sh
# tern's service script on QTS. App Center and the NAS's start-up and
# shutdown call it with start, stop or restart; uninstalling calls stop and
# remove. "status" says whether tern runs.
#
# tern runs below a small supervisor (this script, "supervise") that starts
# it again if it dies, like the image's restart policy.
# Not the caller's directory: App Center runs this from a temporary
# folder it deletes afterwards, and tern and its tools would inherit it
# (rsync cannot even start in a deleted directory).
cd / || exit 1

CONF=/etc/config/qpkg.conf
QPKG_NAME=tern
QPKG_ROOT=$(/sbin/getcfg $QPKG_NAME Install_Path -f $CONF)
SELF=$QPKG_ROOT/tern.sh
BIN=$QPKG_ROOT/bin/tern
TERN_CONF=$QPKG_ROOT/tern.conf
LOG=$QPKG_ROOT/data/tern.log
LOG_MAX=10485760 # bytes; the log moves to tern.log.1 at start beyond this
RUN=/var/run/tern # supervisor.pid, tern.pid, stopping
APP_PROXY=/etc/app_proxy.conf
STOP_WAIT=115     # tern's shutdown budget is 100 s
START_WAIT=50

write_defaults() {
	cat >"$TERN_CONF" <<'EOF'
# tern on this NAS. Stop and start tern in App Center after a change.
#
# The port tern listens on, on the loopback interface only: QTS forwards
# /tern/ on its own HTTP(S) port to it.
TERN_PORT=17180
#
# Other environment variables for tern and the tools it runs, e.g.
# export HTTPS_PROXY=http://192.168.1.2:3128
EOF
}

# environment sets up what tern sees. tern.conf is sourced first; what
# makes this the QNAP package is not up to it.
environment() {
	TERN_PORT=17180
	[ -f "$TERN_CONF" ] || write_defaults
	# shellcheck source=/dev/null
	. "$TERN_CONF"
	export TERN_LISTEN="127.0.0.1:$TERN_PORT"
	export TERN_BASE_PATH=/tern
	export TERN_PLATFORM=qts
	export TERN_DATA_DIR="$QPKG_ROOT/data"
	export TERN_SECRETS_DIR="$QPKG_ROOT/secrets"
	export TERN_RUNTIME_DIR=/dev/shm/tern
	TZ=$(/sbin/getcfg System "Time Zone" -d UTC)
	export TZ
	# dot files of the tools land on the data volume, not on QTS's RAM disk
	export HOME="$QPKG_ROOT/data/home"
	export PATH="$QPKG_ROOT/bin:/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/usr/local/sbin"
	umask 077
}

# ensure_proxy makes QTS's web server forward /tern to tern. QTS writes the
# line into $APP_PROXY (included by its HTTP and HTTPS front ends) when App
# Center enables the app, in its own format; but not after an install that
# bypassed App Center, not with a port changed in tern.conf, and the file
# lives on the RAM disk. Stale /tern lines go, the reload is graceful.
ensure_proxy() {
	want="ProxyPass \"/tern\" \"http://127.0.0.1:$TERN_PORT/tern/\" retry=0"
	# QTS reads the port from here when it writes the line itself
	/sbin/setcfg $QPKG_NAME Web_Port "$TERN_PORT" -f $CONF
	touch "$APP_PROXY"
	# ours: what QTS writes for /tern, enabled (ProxyPass) or disabled
	# (RewriteRule to its "app disabled" page); fixed strings, no regexes
	ours='index($0, "ProxyPass \"/tern\" ") == 1 || index($0, "ProxyPass \"/tern/\" ") == 1 || index($0, "RewriteRule \"^/tern(") == 1'
	stale=$(awk -v want="$want" "\$0 != want && ($ours)" "$APP_PROXY")
	if [ -z "$stale" ] && grep -qxF "$want" "$APP_PROXY"; then
		return 0
	fi
	awk "!($ours)" "$APP_PROXY" >"$APP_PROXY.tern" && echo "$want" >>"$APP_PROXY.tern" &&
		cat "$APP_PROXY.tern" >"$APP_PROXY"
	rm -f "$APP_PROXY.tern"
	log "wrote the /tern proxy line into $APP_PROXY"
	/etc/init.d/thttpd.sh reload >/dev/null 2>&1
	/etc/init.d/stunnel.sh reload >/dev/null 2>&1
	return 0
}

# pid_of <supervisor|tern>: the live pid in the pid file, or nothing.
pid_of() {
	[ -f "$RUN/$1.pid" ] || return 1
	read -r p <"$RUN/$1.pid" || return 1
	[ -n "$p" ] && kill -0 "$p" 2>/dev/null || return 1
	echo "$p"
}

healthy() {
	(environment && "$BIN" --healthcheck)
}

# log writes a line in local time: QTS's date reads /etc/localtime, not
# the zone name environment puts in TZ.
log() {
	echo "$(unset TZ; date '+%Y-%m-%d %H:%M:%S') tern.sh: $*" >>"$LOG"
}

supervise() {
	trap '' HUP
	echo $$ >"$RUN/supervisor.pid"
	environment
	while :; do
		"$BIN" >>"$LOG" 2>&1 </dev/null &
		child=$!
		echo "$child" >"$RUN/tern.pid"
		wait "$child"
		code=$?
		[ -f "$RUN/stopping" ] && break
		log "tern exited with status $code; starting it again in 10 s"
		sleep 10
		[ -f "$RUN/stopping" ] && break
	done
	rm -f "$RUN/tern.pid" "$RUN/supervisor.pid"
}

start() {
	if [ "$(/sbin/getcfg $QPKG_NAME Enable -u -d FALSE -f $CONF)" != TRUE ]; then
		echo "$QPKG_NAME is disabled."
		exit 1
	fi
	if pid_of supervisor >/dev/null; then
		echo "tern is already running."
		return 0
	fi
	mkdir -p "$RUN" "$QPKG_ROOT/data/home" "$QPKG_ROOT/secrets"
	chmod 700 "$QPKG_ROOT/data" "$QPKG_ROOT/secrets"
	rm -f "$RUN/stopping" "$RUN/supervisor.pid" "$RUN/tern.pid"
	if [ -f "$LOG" ] && [ "$(stat -c %s "$LOG")" -gt "$LOG_MAX" ]; then
		mv -f "$LOG" "$LOG.1"
	fi
	log "starting"
	(environment && ensure_proxy)
	setsid "$SELF" supervise </dev/null >/dev/null 2>&1 &

	i=0
	while [ $i -lt $START_WAIT ]; do
		sleep 1
		i=$((i + 1))
		if healthy; then
			echo "tern started."
			return 0
		fi
	done
	echo "tern did not answer within ${START_WAIT}s. The end of $LOG:"
	tail -n 20 "$LOG"
	stop >/dev/null
	return 1
}

stop() {
	sup=$(pid_of supervisor)
	child=$(pid_of tern)
	if [ -z "$sup$child" ]; then
		rm -f "$RUN/supervisor.pid" "$RUN/tern.pid" "$RUN/stopping"
		echo "tern is not running."
		return 0
	fi
	mkdir -p "$RUN"
	: >"$RUN/stopping"
	if [ -n "$child" ]; then
		kill -TERM "$child" 2>/dev/null
		i=0
		while kill -0 "$child" 2>/dev/null && [ $i -lt $STOP_WAIT ]; do
			sleep 1
			i=$((i + 1))
		done
		if kill -0 "$child" 2>/dev/null; then
			log "tern did not stop within ${STOP_WAIT}s; killing it"
			kill -KILL "$child" 2>/dev/null
		fi
	fi
	# it may be sleeping before a restart
	[ -n "$sup" ] && kill -TERM "$sup" 2>/dev/null
	rm -f "$RUN/supervisor.pid" "$RUN/tern.pid" "$RUN/stopping"
	log "stopped"
	echo "tern stopped."
}

case "$1" in
start) start ;;
stop) stop ;;
restart)
	stop
	start
	;;
remove) stop ;;
status)
	if pid_of supervisor >/dev/null && healthy; then
		echo "tern is running."
	else
		echo "tern is not running."
		exit 1
	fi
	;;
supervise) supervise ;;
*)
	echo "Usage: $0 {start|stop|restart|status|remove}"
	exit 1
	;;
esac
exit $?
