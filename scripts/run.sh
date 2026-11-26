#!/data/data/com.termux/files/usr/bin/bash
# Roda um comando do ideias a partir do cron, no Termux:
#   - segura o wake lock durante a execução e SEMPRE o solta no fim (mesmo
#     com erro, Ctrl+C ou SIGTERM do Android);
#   - impede duas execuções ao mesmo tempo;
#   - grava tudo em ~/.local/state/ideias/ideias.log, rotacionado;
#   - depois de cada collect, roda o "check" (alertas pelo Telegram).
#
# Uso: scripts/run.sh collect|report|check|backup [opções do ideias]
#
# Variáveis opcionais (úteis em teste):
#   IDEIAS_BIN        binário (padrão: <repo>/bin/ideias)
#   IDEIAS_STATE_DIR  pasta do log e da trava (padrão: ~/.local/state/ideias)
#   IDEIAS_LOG_MAX    tamanho máximo do log antes de rotacionar, em bytes (padrão: 1 MiB)
#   IDEIAS_LOG_KEEP   quantos logs antigos manter (padrão: 5)

set -u

# O cron roda com PATH mínimo: garante os binários do Termux.
PREFIX="${PREFIX:-/data/data/com.termux/files/usr}"
export PATH="$PREFIX/bin:$PATH"

REPO="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${IDEIAS_BIN:-$REPO/bin/ideias}"
STATE="${IDEIAS_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/ideias}"
LOG="$STATE/ideias.log"
LOG_MAX="${IDEIAS_LOG_MAX:-1048576}"
LOG_KEEP="${IDEIAS_LOG_KEEP:-5}"
LOCK="$STATE/run.lock"

if [ $# -eq 0 ]; then
	echo "uso: $0 collect|report|check|backup [opções]" >&2
	exit 2
fi

mkdir -p "$STATE"

log() {
	printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >>"$LOG"
}

rotate_log() {
	[ -f "$LOG" ] || return 0
	local size
	size=$(wc -c <"$LOG" 2>/dev/null || echo 0)
	[ "$size" -lt "$LOG_MAX" ] && return 0
	local i
	for ((i = LOG_KEEP - 1; i >= 1; i--)); do
		[ -f "$LOG.$i" ] && mv -f "$LOG.$i" "$LOG.$((i + 1))"
	done
	mv -f "$LOG" "$LOG.1"
	rm -f "$LOG.$((LOG_KEEP + 1))"
}

# Trava simples com mkdir (atômico). Uma trava de processo que já morreu é
# removida.
acquire_lock() {
	if mkdir "$LOCK" 2>/dev/null; then
		echo $$ >"$LOCK/pid"
		return 0
	fi
	local pid
	pid=$(cat "$LOCK/pid" 2>/dev/null || echo "")
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		return 1
	fi
	rm -rf "$LOCK"
	mkdir "$LOCK" 2>/dev/null && echo $$ >"$LOCK/pid"
}

WAKELOCK=0
cleanup() {
	if [ "$WAKELOCK" = 1 ] && command -v termux-wake-unlock >/dev/null 2>&1; then
		termux-wake-unlock >/dev/null 2>&1
		WAKELOCK=0
	fi
	[ -f "$LOCK/pid" ] && [ "$(cat "$LOCK/pid" 2>/dev/null)" = "$$" ] && rm -rf "$LOCK"
}
trap cleanup EXIT
trap 'log "interrompido por sinal"; exit 143' INT TERM HUP

rotate_log

if ! acquire_lock; then
	log "ignorado: outra execução ainda está rodando (ideias $*)"
	exit 0
fi

if command -v termux-wake-lock >/dev/null 2>&1; then
	termux-wake-lock >/dev/null 2>&1 && WAKELOCK=1
fi

# Os comandos rodam na raiz do repositório, para reports/ e export/ ficarem lá.
cd "$REPO" || exit 1

if [ ! -x "$BIN" ] && [ ! -x "$BIN.exe" ]; then
	log "erro: binário não encontrado em $BIN (rode: make build)"
	exit 1
fi

log "início: ideias $*"
"$BIN" "$@" >>"$LOG" 2>&1
status=$?
log "fim: ideias $* (código $status)"

if [ "$1" = "collect" ]; then
	"$BIN" check >>"$LOG" 2>&1
	log "fim: ideias check (código $?)"
fi

exit "$status"
