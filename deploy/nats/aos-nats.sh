#!/usr/bin/env bash
# aos-nats.sh — o cluster NATS JetStream de PRODUÇÃO do Event Store, entre hosts (AOS-469).
#
# Um só ficheiro de especificação (`cluster.conf`, ver `cluster.conf.example`) descreve o
# cluster inteiro; cada host corre este script com o SEU nome e materializa só a sua parte:
#
#   sudo bash aos-nats.sh chave                          # 1x por host: gera a chave WG, imprime a pública
#   sudo bash aos-nats.sh aplicar /etc/aos-nats/cluster.conf <host>   # WireGuard + firewall + nats-server
#        bash aos-nats.sh estado  /etc/aos-nats/cluster.conf <host>   # túnel, rotas, meta-leader, URL do cliente
#        bash aos-nats.sh provar  /etc/aos-nats/cluster.conf <host>   # cria e apaga um stream R3 com a placement do AOS
#        bash aos-nats.sh gerar   <cluster.conf> <host> <dir>          # só escreve ficheiros (sem root, sem efeitos)
#
# # PORQUE HÁ UM TÚNEL, E PORQUE O NATS SÓ ESCUTA NELE
#
# O cliente do AOS (`packages/substrate/eventstore/natsjs`) fala NATS em TCP simples:
# `"tls_required":false` e CONNECT sem utilizador, palavra-passe, token nem nkey. Quem chegar
# à porta de cliente escreve no log de produção. Por isso:
#   - o tráfego entre hosts (rotas Raft E cliente) atravessa o WireGuard, cifrado e com par
#     autenticado pela chave — nunca a internet pública em claro;
#   - cada nats-server escuta SÓ no IP WireGuard do seu host: a porta não existe no IP público;
#   - as regras do firewall vêm e vão com a interface (PostUp/PostDown): dentro do túnel só os
#     pares; a partir de contentores locais só a sub-rede Docker declarada (a do nó `aos`).
#     Os contentores órfãos e os pods do cluster k8s vizinho no mesmo host NÃO chegam lá.
# O que isto NÃO fecha: root num dos hosts escreve no log. Fechar isso é autenticação no
# cliente `natsjs`, que é código e não infraestrutura (ver README, «O que fica por fazer»).
#
# # A CHAVE PRIVADA NUNCA SAI DO HOST
#
# É gerada no próprio host (`chave`), vive em /etc/wireguard/aos-es.key (0600) e é carregada
# por `wg set … private-key <ficheiro>` no PostUp. Nenhum ficheiro gerado a contém; o
# `cluster.conf` só leva chaves PÚBLICAS e pode ser copiado entre hosts sem cuidado especial.
set -euo pipefail

IFACE="aos-es"
CHAVE_PRIVADA="/etc/wireguard/${IFACE}.key"
DIR_SISTEMA="/etc/aos-nats"
PROJECTO_COMPOSE="aos-nats"
# A região é anunciada como `region:<valor>`: é a constante TagDeRegiao de
# packages/substrate/eventstore/jetstream/soberania.go. Se mudar lá, muda aqui (e em
# infra/modules/eventstore/main.tf), ou a placement passa a pedir uma tag que ninguém anuncia.
PREFIXO_TAG_REGIAO="region:"

die() { printf 'aos-nats: %s\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*" >&2; }

# ---------------------------------------------------------------------------------------------
# Leitura e validação da especificação
# ---------------------------------------------------------------------------------------------

# Estado lido do cluster.conf (preenchido por ler_spec).
REGIAO="" REDE_WG="" PORTA_WG="" IMAGEM="" IMAGEM_BOX="" NOME_CLUSTER="" MAX_FILE_STORE=""
declare -a H_NOME=() H_PUB=() H_WG=() H_CHAVE=() H_DOCKER=()
declare -a N_NOME=() N_HOST=() N_CLI=() N_ROTA=() N_MON=()

# Octetos sem zeros à esquerda: «010» seria octal dentro de $(( )) em ip_para_int.
eh_ipv4() {
	local octeto='(0|[1-9][0-9]{0,2})' o
	[[ "$1" =~ ^$octeto\.$octeto\.$octeto\.$octeto$ ]] || return 1
	for o in "${BASH_REMATCH[@]:1}"; do [ "$o" -le 255 ] || return 1; done
}

eh_cidr() {
	local ip="${1%/*}" bits="${1#*/}"
	[[ "$1" == */* && "$bits" =~ ^[0-9]{1,2}$ ]] && [ "$bits" -le 32 ] && eh_ipv4 "$ip"
}

eh_porta() { [[ "$1" =~ ^[0-9]{1,5}$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }

# dentro_de IP CIDR — verdadeiro se o IP pertence à rede.
dentro_de() {
	local ip="$1" rede="${2%/*}" bits="${2#*/}" a b
	a=$(ip_para_int "$ip"); b=$(ip_para_int "$rede")
	local mascara=$(( bits == 0 ? 0 : (0xFFFFFFFF << (32 - bits)) & 0xFFFFFFFF ))
	[ $(( a & mascara )) -eq $(( b & mascara )) ]
}

ip_para_int() {
	local IFS=. o1 o2 o3 o4
	read -r o1 o2 o3 o4 <<<"$1"
	printf '%d' $(( (o1 << 24) | (o2 << 16) | (o3 << 8) | o4 ))
}

indice_do_host() {
	local i
	for i in "${!H_NOME[@]}"; do [ "${H_NOME[$i]}" = "$1" ] && { printf '%s' "$i"; return 0; }; done
	return 1
}

ler_spec() {
	local spec="$1" linha n=0 tipo resto
	[ -r "$spec" ] || die "especificação ilegível: $spec"
	# Recomeça do zero: o `aplicar` lê a especificação e depois chama o `gerar`, que a relê.
	REGIAO="" REDE_WG="" PORTA_WG="" IMAGEM="" IMAGEM_BOX="" NOME_CLUSTER="" MAX_FILE_STORE=""
	H_NOME=() H_PUB=() H_WG=() H_CHAVE=() H_DOCKER=()
	N_NOME=() N_HOST=() N_CLI=() N_ROTA=() N_MON=()
	while IFS= read -r linha || [ -n "$linha" ]; do
		n=$((n + 1))
		linha="${linha%%#*}"
		linha="$(printf '%s' "$linha" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
		[ -z "$linha" ] && continue
		case "$linha" in
		REGIAO=*) REGIAO="${linha#*=}" ;;
		REDE_WG=*) REDE_WG="${linha#*=}" ;;
		PORTA_WG=*) PORTA_WG="${linha#*=}" ;;
		IMAGEM=*) IMAGEM="${linha#*=}" ;;
		IMAGEM_BOX=*) IMAGEM_BOX="${linha#*=}" ;;
		NOME_CLUSTER=*) NOME_CLUSTER="${linha#*=}" ;;
		MAX_FILE_STORE=*) MAX_FILE_STORE="${linha#*=}" ;;
		host\ * | no\ *)
			read -r tipo resto <<<"$linha"
			# shellcheck disable=SC2086 # a separação por espaços é o formato
			set -- $resto
			if [ "$tipo" = host ]; then
				[ $# -eq 4 ] || [ $# -eq 5 ] || die "$spec:$n: 'host' leva 4 ou 5 campos: <nome> <ip-publico> <ip-wg> <chave-publica-wg> [subrede-docker-permitida]"
				H_NOME+=("$1"); H_PUB+=("$2"); H_WG+=("$3"); H_CHAVE+=("$4"); H_DOCKER+=("${5:-}")
			else
				[ $# -eq 5 ] || die "$spec:$n: 'no' leva 5 campos: <nome> <host> <porta-cliente> <porta-rota> <porta-monitor>"
				N_NOME+=("$1"); N_HOST+=("$2"); N_CLI+=("$3"); N_ROTA+=("$4"); N_MON+=("$5")
			fi
			;;
		*) die "$spec:$n: linha não reconhecida: $linha" ;;
		esac
	done <"$spec"
	validar_spec "$spec"
}

validar_spec() {
	local spec="$1" i j h chave
	# Normalização IGUAL à de normalizarRegiao() em soberania.go: minúsculas, sem espaços.
	REGIAO="$(printf '%s' "$REGIAO" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
	[ -n "$REGIAO" ] || die "$spec: REGIAO vazia — sem tag de região a placement do AOS não tem par elegível (err_code=10005)"
	case "$REGIAO" in "$PREFIXO_TAG_REGIAO"*) die "$spec: REGIAO é a região NUA (ex.: eu-west), sem o prefixo '$PREFIXO_TAG_REGIAO'" ;; esac
	eh_cidr "$REDE_WG" || die "$spec: REDE_WG não é um CIDR IPv4: '$REDE_WG'"
	eh_porta "$PORTA_WG" || die "$spec: PORTA_WG inválida: '$PORTA_WG'"
	[[ "$IMAGEM" == *@sha256:* ]] || die "$spec: IMAGEM tem de vir pinada por digest (nome:tag@sha256:…) — uma tag move-se sem ninguém mudar o ficheiro"
	[[ "$IMAGEM_BOX" == *@sha256:* ]] || die "$spec: IMAGEM_BOX tem de vir pinada por digest"
	[[ "$NOME_CLUSTER" =~ ^[A-Za-z0-9_-]+$ ]] || die "$spec: NOME_CLUSTER inválido: '$NOME_CLUSTER'"
	[[ "$MAX_FILE_STORE" =~ ^[0-9]+[KMGT]?$ ]] || die "$spec: MAX_FILE_STORE inválido (ex.: 20G): '$MAX_FILE_STORE'"

	[ "${#H_NOME[@]}" -ge 1 ] || die "$spec: nenhum 'host' declarado"
	for i in "${!H_NOME[@]}"; do
		[[ "${H_NOME[$i]}" =~ ^[a-z0-9-]+$ ]] || die "$spec: nome de host inválido: '${H_NOME[$i]}'"
		eh_ipv4 "${H_PUB[$i]}" || die "$spec: host ${H_NOME[$i]}: ip-publico inválido: '${H_PUB[$i]}'"
		eh_ipv4 "${H_WG[$i]}" || die "$spec: host ${H_NOME[$i]}: ip-wg inválido: '${H_WG[$i]}'"
		dentro_de "${H_WG[$i]}" "$REDE_WG" || die "$spec: host ${H_NOME[$i]}: ${H_WG[$i]} não está em REDE_WG=$REDE_WG"
		chave="${H_CHAVE[$i]}"
		[[ "$chave" =~ ^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$ ]] || die "$spec: host ${H_NOME[$i]}: chave pública WireGuard inválida ('$chave') — cola a saída de 'aos-nats.sh chave' desse host"
		if [ -n "${H_DOCKER[$i]}" ]; then
			eh_cidr "${H_DOCKER[$i]}" || die "$spec: host ${H_NOME[$i]}: subrede-docker-permitida não é CIDR: '${H_DOCKER[$i]}'"
		fi
		for j in "${!H_NOME[@]}"; do
			[ "$i" -lt "$j" ] || continue
			[ "${H_NOME[$i]}" != "${H_NOME[$j]}" ] || die "$spec: host repetido: ${H_NOME[$i]}"
			[ "${H_WG[$i]}" != "${H_WG[$j]}" ] || die "$spec: ip-wg repetido: ${H_WG[$i]}"
			[ "${H_CHAVE[$i]}" != "${H_CHAVE[$j]}" ] || die "$spec: a mesma chave WG em ${H_NOME[$i]} e ${H_NOME[$j]}"
		done
	done

	# Factores de replicação que o nó aceita: 3 ou 5 (main.go, ErrBadEventStoreReplicas; 1 é dev).
	case "${#N_NOME[@]}" in
	3 | 5) ;;
	*) die "$spec: ${#N_NOME[@]} nó(s) NATS — o stream do AOS é R3 (ou R5) com TODAS as réplicas na região; declara exactamente 3 ou 5" ;;
	esac
	for i in "${!N_NOME[@]}"; do
		[[ "${N_NOME[$i]}" =~ ^[a-z0-9-]+$ ]] || die "$spec: nome de nó inválido: '${N_NOME[$i]}'"
		indice_do_host "${N_HOST[$i]}" >/dev/null || die "$spec: nó ${N_NOME[$i]}: host '${N_HOST[$i]}' não declarado"
		for p in "${N_CLI[$i]}" "${N_ROTA[$i]}" "${N_MON[$i]}"; do
			eh_porta "$p" || die "$spec: nó ${N_NOME[$i]}: porta inválida '$p'"
		done
		if [ "${N_CLI[$i]}" = "${N_ROTA[$i]}" ] || [ "${N_CLI[$i]}" = "${N_MON[$i]}" ] || [ "${N_ROTA[$i]}" = "${N_MON[$i]}" ]; then
			die "$spec: nó ${N_NOME[$i]}: as três portas têm de ser distintas"
		fi
		for j in "${!N_NOME[@]}"; do
			[ "$i" -lt "$j" ] || continue
			[ "${N_NOME[$i]}" != "${N_NOME[$j]}" ] || die "$spec: nó repetido: ${N_NOME[$i]}"
			if [ "${N_HOST[$i]}" = "${N_HOST[$j]}" ]; then
				local pi pj
				for pi in "${N_CLI[$i]}" "${N_ROTA[$i]}" "${N_MON[$i]}"; do
					for pj in "${N_CLI[$j]}" "${N_ROTA[$j]}" "${N_MON[$j]}"; do
						[ "$pi" != "$pj" ] || die "$spec: nós ${N_NOME[$i]} e ${N_NOME[$j]} partilham a porta $pi no host ${N_HOST[$i]}"
					done
				done
			fi
		done
	done
	for h in "${H_NOME[@]}"; do
		local tem=0
		for i in "${!N_NOME[@]}"; do [ "${N_HOST[$i]}" = "$h" ] && tem=1; done
		[ "$tem" -eq 1 ] || die "$spec: host '$h' não corre nenhum nó — retira-o, ou é um par WG sem razão de existir"
	done
	avisar_dominio_de_falha
}

# avisar_dominio_de_falha diz em voz alta o que a topologia aguenta. Não recusa: com dois
# hosts, 2+1 é a única forma de ter R3, e é uma escolha legítima — desde que seja consciente.
avisar_dominio_de_falha() {
	local total="${#N_NOME[@]}" quorum=$(( ${#N_NOME[@]} / 2 + 1 )) h i c
	for h in "${H_NOME[@]}"; do
		c=0
		for i in "${!N_NOME[@]}"; do [ "${N_HOST[$i]}" = "$h" ] && c=$((c + 1)); done
		if [ $((total - c)) -lt "$quorum" ]; then
			log "AVISO: o host '$h' corre $c de $total nós. Se ele cair, sobram $((total - c)) < quórum $quorum:"
			log "       o Event Store PÁRA (sem líder não há escrita). E o quórum de commit pode formar-se"
			log "       só dentro dele — a cópia fora dele pode ficar atrás. Um 3.º host fecha os dois."
		fi
	done
}

# ---------------------------------------------------------------------------------------------
# Geração (pura: só escreve em <dir>)
# ---------------------------------------------------------------------------------------------

portas_do_host() {
	local h="$1" i out=()
	for i in "${!N_NOME[@]}"; do
		[ "${N_HOST[$i]}" = "$h" ] && out+=("${N_CLI[$i]}" "${N_ROTA[$i]}" "${N_MON[$i]}")
	done
	local IFS=,
	printf '%s' "${out[*]}"
}

ip_wg_do_no() { local hi; hi=$(indice_do_host "${N_HOST[$1]}"); printf '%s' "${H_WG[$hi]}"; }

gerar_conf_no() {
	local i="$1" ip j rotas=""
	ip=$(ip_wg_do_no "$i")
	for j in "${!N_NOME[@]}"; do
		[ "$j" = "$i" ] && continue
		rotas+="    nats-route://$(ip_wg_do_no "$j"):${N_ROTA[$j]}"$'\n'
	done
	cat <<EOF
# Gerado por deploy/nats/aos-nats.sh (AOS-469) — NÃO editar à mão; muda o cluster.conf e reaplica.
server_name: ${N_NOME[$i]}

# Só o IP WireGuard: o cliente do AOS não tem TLS nem autenticação (natsjs), e a porta não
# pode existir no IP público.
listen: ${ip}:${N_CLI[$i]}
http: ${ip}:${N_MON[$i]}

# Elegibilidade para a placement do stream (ADR-011, AOS-100 AC5).
server_tags: ["${PREFIXO_TAG_REGIAO}${REGIAO}"]

jetstream {
  store_dir: "/data"
  max_file_store: ${MAX_FILE_STORE}
  max_memory_store: 256M
}

cluster {
  name: ${NOME_CLUSTER}
  listen: ${ip}:${N_ROTA[$i]}
  routes: [
${rotas}  ]
}

max_payload: 1MB
write_deadline: "10s"
EOF
}

gerar_compose() {
	local h="$1" i
	cat <<EOF
# Gerado por deploy/nats/aos-nats.sh (AOS-469) para o host '${h}' — NÃO editar à mão.
name: ${PROJECTO_COMPOSE}

services:
EOF
	for i in "${!N_NOME[@]}"; do
		[ "${N_HOST[$i]}" = "$h" ] || continue
		cat <<EOF
  ${N_NOME[$i]}:
    image: ${IMAGEM}
    # network_mode host: o servidor escuta no IP WireGuard do host, e as rotas para os outros
    # hosts saem pela interface ${IFACE}. Uma rede bridge poria um NAT entre o Raft e o túnel.
    network_mode: host
    restart: unless-stopped
    command: ["-c", "/etc/nats/nats.conf"]
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    pids_limit: 512
    init: true
    stop_grace_period: 30s
    volumes:
      - ${DIR_SISTEMA}/${N_NOME[$i]}.conf:/etc/nats/nats.conf:ro
      - ${N_NOME[$i]}-data:/data
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://$(ip_wg_do_no "$i"):${N_MON[$i]}/healthz?js-enabled-only=true"]
      interval: 10s
      timeout: 3s
      retries: 6
EOF
	done
	printf '\nvolumes:\n'
	for i in "${!N_NOME[@]}"; do
		[ "${N_HOST[$i]}" = "$h" ] && printf '  %s-data:\n' "${N_NOME[$i]}"
	done
	return 0
}

gerar_wg() {
	local h="$1" hi j portas docker_sub
	hi=$(indice_do_host "$h")
	portas=$(portas_do_host "$h")
	docker_sub="${H_DOCKER[$hi]}"
	local mascara="${REDE_WG#*/}"
	cat <<EOF
# Gerado por deploy/nats/aos-nats.sh (AOS-469) para o host '${h}' — NÃO editar à mão.
# A chave privada NÃO está aqui: é carregada de ${CHAVE_PRIVADA} no PostUp.
[Interface]
Address = ${H_WG[$hi]}/${mascara}
ListenPort = ${PORTA_WG}
PostUp = wg set %i private-key ${CHAVE_PRIVADA}

# --- firewall: vem e vai com a interface ---------------------------------------------------
# Entrada nas portas NATS deste host (só existem no IP WG): pares do túnel, o próprio host e,
# se declarada, a sub-rede Docker do nó aos. Tudo o resto — incluindo outros contentores e
# pods deste host — cai.
PostUp = iptables -N AOS-ES-IN 2>/dev/null || true; iptables -F AOS-ES-IN
PostUp = iptables -A AOS-ES-IN -i %i -s ${REDE_WG} -j ACCEPT
PostUp = iptables -A AOS-ES-IN -i lo -j ACCEPT
EOF
	[ -n "$docker_sub" ] && printf 'PostUp = iptables -A AOS-ES-IN -s %s -j ACCEPT\n' "$docker_sub"
	cat <<EOF
PostUp = iptables -A AOS-ES-IN -j DROP
PostUp = while iptables -D INPUT -d ${H_WG[$hi]} -p tcp -m multiport --dports ${portas} -j AOS-ES-IN 2>/dev/null; do :; done; iptables -I INPUT -d ${H_WG[$hi]} -p tcp -m multiport --dports ${portas} -j AOS-ES-IN
# Encaminhamento através do túnel: só respostas e, para fora, só a sub-rede do nó aos. Um par
# não usa este host como router, e nenhum contentor que não o nó aos chega aos outros hosts.
PostUp = iptables -N AOS-ES-FWD 2>/dev/null || true; iptables -F AOS-ES-FWD
PostUp = iptables -A AOS-ES-FWD -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
EOF
	[ -n "$docker_sub" ] && printf 'PostUp = iptables -A AOS-ES-FWD -o %%i -s %s -d %s -j RETURN\n' "$docker_sub" "$REDE_WG"
	cat <<EOF
PostUp = iptables -A AOS-ES-FWD -j DROP
PostUp = while iptables -D FORWARD -i %i -j AOS-ES-FWD 2>/dev/null; do :; done; iptables -I FORWARD -i %i -j AOS-ES-FWD
PostUp = while iptables -D FORWARD -o %i -j AOS-ES-FWD 2>/dev/null; do :; done; iptables -I FORWARD -o %i -j AOS-ES-FWD
# Idempotente nos dois sentidos: um PostUp repetido (interface que caiu sem PostDown) não
# duplica saltos, e o PostDown remove TODAS as cópias — senão a cadeia não se apagaria.
PostDown = while iptables -D INPUT -d ${H_WG[$hi]} -p tcp -m multiport --dports ${portas} -j AOS-ES-IN 2>/dev/null; do :; done
PostDown = while iptables -D FORWARD -i %i -j AOS-ES-FWD 2>/dev/null; do :; done
PostDown = while iptables -D FORWARD -o %i -j AOS-ES-FWD 2>/dev/null; do :; done
PostDown = iptables -F AOS-ES-IN || true; iptables -X AOS-ES-IN || true
PostDown = iptables -F AOS-ES-FWD || true; iptables -X AOS-ES-FWD || true
EOF
	for j in "${!H_NOME[@]}"; do
		[ "$j" = "$hi" ] && continue
		cat <<EOF

[Peer]
# ${H_NOME[$j]}
PublicKey = ${H_CHAVE[$j]}
Endpoint = ${H_PUB[$j]}:${PORTA_WG}
AllowedIPs = ${H_WG[$j]}/32
PersistentKeepalive = 25
EOF
	done
}

url_cliente() {
	local i out=()
	for i in "${!N_NOME[@]}"; do out+=("$(ip_wg_do_no "$i"):${N_CLI[$i]}"); done
	local IFS=,
	printf '%s' "${out[*]}"
}

cmd_gerar() {
	[ $# -eq 3 ] || die "uso: gerar <cluster.conf> <host> <dir>"
	ler_spec "$1"
	local h="$2" dir="$3" i
	indice_do_host "$h" >/dev/null || die "host '$h' não está no cluster.conf (hosts: ${H_NOME[*]})"
	mkdir -p "$dir"
	for i in "${!N_NOME[@]}"; do
		[ "${N_HOST[$i]}" = "$h" ] || continue
		gerar_conf_no "$i" >"$dir/${N_NOME[$i]}.conf"
	done
	gerar_compose "$h" >"$dir/docker-compose.yml"
	(umask 077 && gerar_wg "$h" >"$dir/${IFACE}.conf")
	log "gerado em $dir para o host '$h'"
}

# ---------------------------------------------------------------------------------------------
# Comandos com efeitos (root)
# ---------------------------------------------------------------------------------------------

exigir_root() { [ "$(id -u)" -eq 0 ] || die "este comando corre como root (sudo)"; }

cmd_chave() {
	exigir_root
	if ! command -v wg >/dev/null 2>&1; then
		command -v apt-get >/dev/null 2>&1 || die "wireguard-tools em falta e não há apt-get — instala-o à mão"
		log "a instalar wireguard-tools…"
		DEBIAN_FRONTEND=noninteractive apt-get install -y wireguard-tools >/dev/null
	fi
	install -d -m 0700 /etc/wireguard
	if [ ! -s "$CHAVE_PRIVADA" ]; then
		(umask 077 && wg genkey >"$CHAVE_PRIVADA")
		log "chave privada gerada em $CHAVE_PRIVADA (não sai deste host)"
	else
		log "chave privada já existe em $CHAVE_PRIVADA — reutilizada"
	fi
	chmod 0600 "$CHAVE_PRIVADA"
	log "chave PÚBLICA deste host (vai para a linha 'host' do cluster.conf):"
	wg pubkey <"$CHAVE_PRIVADA"
}

# porta_ocupada_por_outro devolve verdadeiro se alguém que não um nats-server escuta na porta.
porta_ocupada_por_outro() {
	local linhas
	linhas="$(ss -Hltnp "sport = :$1" 2>/dev/null || true)"
	[ -n "$linhas" ] && ! grep -q 'nats-server' <<<"$linhas"
}

cmd_aplicar() {
	[ $# -eq 2 ] || die "uso: aplicar <cluster.conf> <host>"
	exigir_root
	local spec="$1" h="$2" hi p pub
	ler_spec "$spec"
	hi=$(indice_do_host "$h") || die "host '$h' não está no cluster.conf (hosts: ${H_NOME[*]})"

	[ -s "$CHAVE_PRIVADA" ] || die "falta $CHAVE_PRIVADA — corre primeiro 'aos-nats.sh chave' neste host"
	command -v wg-quick >/dev/null || die "wg-quick em falta — corre 'aos-nats.sh chave'"
	command -v docker >/dev/null || die "docker em falta neste host"
	docker compose version >/dev/null 2>&1 || die "o plugin 'docker compose' (v2) está em falta neste host"
	command -v iptables >/dev/null || die "iptables em falta"
	pub="$(wg pubkey <"$CHAVE_PRIVADA")"
	[ "$pub" = "${H_CHAVE[$hi]}" ] || die "a chave pública declarada para '$h' no cluster.conf não é a deste host ($pub) — host errado, ou chave regenerada"
	for p in $(portas_do_host "$h" | tr , ' '); do
		porta_ocupada_por_outro "$p" && die "a porta $p já está ocupada por outro processo neste host: $(ss -Hltnp "sport = :$p" | head -1)"
	done
	if [ -n "${H_DOCKER[$hi]}" ]; then
		docker network ls -q | xargs -r docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null |
			tr ' ' '\n' | grep -qxF "${H_DOCKER[$hi]}" ||
			log "AVISO: nenhuma rede Docker deste host tem a sub-rede ${H_DOCKER[$hi]} — o nó aos não vai chegar ao NATS"
	fi

	install -d -m 0755 "$DIR_SISTEMA"
	install -m 0644 "$spec" "$DIR_SISTEMA/cluster.conf.aplicado"
	cmd_gerar "$spec" "$h" "$DIR_SISTEMA"
	install -m 0600 "$DIR_SISTEMA/${IFACE}.conf" "/etc/wireguard/${IFACE}.conf"
	rm -f "$DIR_SISTEMA/${IFACE}.conf"

	# Reaplicar: derrubar e reerguer recarrega peers E regras (o PostDown limpa o que o PostUp pôs).
	if ip link show "$IFACE" >/dev/null 2>&1; then
		systemctl restart "wg-quick@${IFACE}"
	else
		systemctl enable --now "wg-quick@${IFACE}"
	fi
	log "túnel ${IFACE} de pé em ${H_WG[$hi]}"

	docker compose -p "$PROJECTO_COMPOSE" -f "$DIR_SISTEMA/docker-compose.yml" up -d --remove-orphans
	log ""
	log "aplicado. Quando TODOS os hosts estiverem aplicados:"
	log "  bash $0 estado $DIR_SISTEMA/cluster.conf.aplicado $h"
	log "  bash $0 provar $DIR_SISTEMA/cluster.conf.aplicado $h"
}

cmd_estado() {
	[ $# -eq 2 ] || die "uso: estado <cluster.conf> <host>"
	local h="$2" i ip jsz falhas=0
	ler_spec "$1"
	indice_do_host "$h" >/dev/null || die "host '$h' não está no cluster.conf"
	command -v wget >/dev/null || die "wget em falta neste host (apt-get install -y wget)"
	if command -v wg >/dev/null && [ "$(id -u)" -eq 0 ]; then
		echo "== túnel"
		local hs par ts
		if ! hs="$(wg show "$IFACE" latest-handshakes 2>/dev/null)"; then
			echo "  interface $IFACE AUSENTE"; falhas=$((falhas + 1))
		fi
		while read -r par ts; do
			[ -n "$par" ] || continue
			if [ "$ts" = 0 ]; then
				echo "  par ${par:0:8}…: SEM handshake"; falhas=$((falhas + 1))
			else
				echo "  par ${par:0:8}…: handshake há $(( $(date +%s) - ts ))s"
			fi
		done <<<"$hs"
	fi
	echo "== nós deste host"
	for i in "${!N_NOME[@]}"; do
		[ "${N_HOST[$i]}" = "$h" ] || continue
		ip=$(ip_wg_do_no "$i")
		if ! wget -qO- "http://${ip}:${N_MON[$i]}/healthz?js-enabled-only=true" >/dev/null 2>&1; then
			echo "  ${N_NOME[$i]}: NÃO saudável"; falhas=$((falhas + 1)); continue
		fi
		jsz="$(wget -qO- "http://${ip}:${N_MON[$i]}/jsz" 2>/dev/null || true)"
		local rotas
		# Pares DISTINTOS, e não num_routes: o 2.10 abre um pool de ligações por par.
		rotas="$(wget -qO- "http://${ip}:${N_MON[$i]}/routez" 2>/dev/null | grep -o '"remote_name": *"[^"]*"' | sort -u | wc -l)"
		rotas="${rotas// /}/$(( ${#N_NOME[@]} - 1 ))"
		case "$jsz" in
		*'"meta_cluster"'*'"leader"'*)
			echo "  ${N_NOME[$i]}: saudável, pares=$rotas, meta-leader=$(grep -o '"leader": *"[^"]*"' <<<"$jsz" | head -1 | cut -d'"' -f4)" ;;
		*) echo "  ${N_NOME[$i]}: saudável, pares=$rotas, SEM meta-leader (cluster por formar)"; falhas=$((falhas + 1)) ;;
		esac
	done
	echo "== para o nó aos (deploy/server/.env)"
	echo "  AOS_EVENTSTORE_NATS=$(url_cliente)"
	echo "  AOS_EVENTSTORE_NATS_REGION=$REGIAO"
	echo "  AOS_EVENTSTORE_NATS_REPLICAS=${#N_NOME[@]}"
	[ "$falhas" -eq 0 ]
}

cmd_provar() {
	[ $# -eq 2 ] || die "uso: provar <cluster.conf> <host>"
	local h="$2" stream
	ler_spec "$1"
	indice_do_host "$h" >/dev/null || die "host '$h' não está no cluster.conf"
	stream="AOS_469_PROVA_$(date +%s)"
	local srv
	srv="nats://$(url_cliente | sed 's/,/,nats:\/\//g')"
	nbox() { docker run --rm --network host "$IMAGEM_BOX" nats --server "$srv" "$@"; }
	log "a criar $stream: R${#N_NOME[@]}, placement tag ${PREFIXO_TAG_REGIAO}${REGIAO} (a mesma forma que o AOS pede)…"
	nbox stream add "$stream" --subjects "aos469.prova.$stream" --storage file \
		--replicas "${#N_NOME[@]}" --tag "${PREFIXO_TAG_REGIAO}${REGIAO}" --max-age 1h --defaults >/dev/null
	nbox pub "aos469.prova.$stream" "prova" >/dev/null 2>&1
	local info
	info="$(nbox stream info "$stream" --json)"
	nbox stream rm -f "$stream" >/dev/null
	local atuais
	atuais=$(grep -o '"current": *true' <<<"$info" | wc -l)
	echo "líder: $(grep -o '"leader": *"[^"]*"' <<<"$info" | head -1 | cut -d'"' -f4), réplicas em dia: $atuais de $(( ${#N_NOME[@]} - 1 ))"
	[ "$atuais" -eq $(( ${#N_NOME[@]} - 1 )) ] || die "nem todas as réplicas estão em dia — ver 'estado'"
	log "prova OK (stream apagado)"
}

case "${1:-}" in
gerar) shift; cmd_gerar "$@" ;;
chave) shift; cmd_chave "$@" ;;
aplicar) shift; cmd_aplicar "$@" ;;
estado) shift; cmd_estado "$@" ;;
provar) shift; cmd_provar "$@" ;;
*) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
