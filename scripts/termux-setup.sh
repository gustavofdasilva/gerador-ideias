#!/data/data/com.termux/files/usr/bin/bash
# Prepara o Termux para rodar o ideias. Pode ser rodado quantas vezes quiser.
#
# Uso:
#   scripts/termux-setup.sh            # instala, configura e compila
#   scripts/termux-setup.sh --cron     # idem + instala o crontab do ideias
#
# Horários do crontab (hora local do celular, formato do cron "minuto hora"):
#   COLLECT_AT="30 3"   coleta diária          (padrão: 03:30)
#   REPORT_AT="0 9"     relatório de domingo   (padrão: 09:00)
#   CHECK_EVERY=6       verificação de saúde a cada N horas (padrão: 6)
# Ex.: COLLECT_AT="15 4" scripts/termux-setup.sh --cron
#
# O OmniRoute já roda no Termux e NÃO é tocado por este script.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
CONF="$HOME/.config/ideias"
INSTALL_CRON=0
[ "${1:-}" = "--cron" ] && INSTALL_CRON=1

step() { printf '\n==> %s\n' "$*"; }

step "Pacotes"
pkg install -y golang git make cronie termux-services termux-api

step "Armazenamento compartilhado (para o backup em ~/storage/shared)"
if [ ! -d "$HOME/storage/shared" ]; then
	echo "Pedindo permissão de armazenamento: aceite no diálogo do Android."
	termux-setup-storage || true
	sleep 3
fi
[ -d "$HOME/storage/shared" ] && echo "ok: $HOME/storage/shared" ||
	echo "aviso: sem acesso ao armazenamento; rode termux-setup-storage depois."

step "Serviço do cron"
if command -v sv-enable >/dev/null 2>&1 && [ -d "${PREFIX:-/data/data/com.termux/files/usr}/var/service/crond" ]; then
	sv-enable crond && echo "ok: crond habilitado"
else
	echo "aviso: o termux-services acabou de ser instalado. Feche e abra o Termux"
	echo "       e rode este script de novo para habilitar o crond."
fi

step "Pastas e configuração"
mkdir -p "$CONF" "$HOME/.local/state/ideias" "$HOME/.local/share/ideias"
if [ ! -f "$CONF/config.toml" ]; then
	cp "$REPO/config.example.toml" "$CONF/config.toml"
	echo "criado: $CONF/config.toml (revise ai.group_model e ai.score_model)"
else
	echo "mantido: $CONF/config.toml"
fi
if [ ! -f "$CONF/env" ]; then
	cp "$REPO/env.example" "$CONF/env"
	echo "criado: $CONF/env (preencha os tokens)"
else
	echo "mantido: $CONF/env"
fi
chmod 600 "$CONF/env"

step "Compilação"
chmod +x "$REPO/scripts/"*.sh
make -C "$REPO" build
"$REPO/bin/ideias" migrate

step "Crontab"
COLLECT_AT="${COLLECT_AT:-30 3}"
REPORT_AT="${REPORT_AT:-0 9}"
CHECK_EVERY="${CHECK_EVERY:-6}"
CRON_LINES="$COLLECT_AT * * * $REPO/scripts/run.sh collect # ideias
$REPORT_AT * * 0 $REPO/scripts/run.sh report # ideias
0 */$CHECK_EVERY * * * $REPO/scripts/run.sh check # ideias"
if [ "$INSTALL_CRON" = 1 ]; then
	# Troca só as linhas marcadas com "# ideias"; o resto do crontab fica.
	{ crontab -l 2>/dev/null | grep -v '# ideias$' || true; echo "$CRON_LINES"; } | crontab -
	echo "crontab instalado:"
	crontab -l | grep '# ideias$'
else
	echo "Crontab sugerido (instale com: scripts/termux-setup.sh --cron):"
	echo "$CRON_LINES"
fi

step "Pronto"
cat <<EOF
Próximos passos:
  1. Preencha $CONF/env (Bluesky, GitHub, Telegram; OmniRoute se não for o padrão).
  2. Revise $CONF/config.toml, principalmente [ai] group_model/score_model e [collect] sources.
  3. Teste: $REPO/scripts/run.sh collect && tail -n 30 ~/.local/state/ideias/ideias.log
  4. Desative a otimização de bateria do Termux (Configurações do Android > Apps > Termux > Bateria > Sem restrição).
  5. Instale o app Termux:API (mesma loja do Termux) para o wake lock funcionar.
EOF
