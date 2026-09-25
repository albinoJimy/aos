#!/usr/bin/env bash
# nats-cluster.sh — levanta (e derruba) o cluster JetStream que as suites dormentes exigem.
#
# # PORQUE É QUE ISTO EXISTE, E PORQUE NÃO É UM `services:` DO GITHUB ACTIONS
#
# O AOS-431 mediu o que acontece com um NATS de UM nó: das 53 funções de teste do pacote
# `jetstream`, 24 FALHAM — e todas pela mesma resposta do servidor:
#
#     replicas > 1 not supported in non-clustered mode (code=500 err_code=10074)
#
# `jetstream.Abrir` cria os streams com `ReplicasPorOmissao = 3` (store.go). Um nó só não é
# um cluster pequeno: é um substrato onde metade do que estas suites medem — arbitragem
# entre escritores, quórum, perda de nó, colocação soberana — deixa de ser observável.
#
# Baixar as réplicas para 1 tornaria os testes verdes e não é o que se faz aqui: seria
# substituir o ambiente por uma fixture e medir a fixture. É a mesma razão pela qual o
# `perda_test.go` prefere FALHAR a medir sem `AOS_KILL_CMD`.
#
# O `services:` do GitHub Actions levanta contentores, mas não os liga entre si numa rota de
# cluster nem lhes injecta ficheiro de configuração — e `server_tags` **só** existe em
# configuração (o `nats-server` não tem flag para ela; ver
# `infra/modules/eventstore/main.tf`, de onde esta receita é copiada deliberadamente).
#
# # A RECEITA É A DA INFRA, DE PROPÓSITO
#
# Imagem, flags e forma da tag vêm de `infra/modules/eventstore/main.tf`. Se divergirem, o CI
# passa a medir um substrato que a produção não tem — que é exactamente a classe de defeito
# que este ticket veio fechar. A normalização da região é a mesma de `normalizarRegiao()` em
# `soberania.go`: minúsculas, sem espaços em redor.
#
# Uso:
#
#     bash scripts/ci/nats-cluster.sh up      # levanta e imprime AOS_NATS_URL=...
#     bash scripts/ci/nats-cluster.sh down    # derruba e limpa a rede
#     bash scripts/ci/nats-cluster.sh env     # só imprime as variáveis (cluster já de pé)
#     bash scripts/ci/nats-cluster.sh restore # reergue nós derrubados e espera pelo cluster
#
# Em CI:  eval "$(bash scripts/ci/nats-cluster.sh up)"
set -euo pipefail

# A IMAGEM É PINADA AO MESMO VALOR DA INFRA (`infra/variables.tf`, `infra/env/*.tfvars`).
# Uma tag flutuante faria o CI medir um servidor diferente do de produção sem ninguém mudar
# uma linha deste repositório.
IMAGEM="${AOS_NATS_IMAGE:-nats:2.10-alpine}"
REDE="${AOS_NATS_NETWORK:-aos-ci-nats}"
PREFIXO="${AOS_NATS_PREFIX:-aos-ci-nats}"
NOME_CLUSTER="${AOS_NATS_CLUSTER_NAME:-aos-ci-es}"

# A REGIÃO É `eu-west` PORQUE OS TESTES A FIXAM.
# `packages/integration/backup_replicado_test.go` tem `regiaoDoCluster = "eu-west"` em código,
# e é também a residência de produção. Mudar aqui sem mudar lá torna os testes de soberania
# ilegíveis — falhariam por desencontro de configuração e o diagnóstico apontaria ao código.
REGIAO="${AOS_NATS_REGION:-eu-west}"

# A SEGUNDA REGIÃO SERVE UM TESTE SÓ, E É A RAZÃO DE HAVER UM QUARTO NÓ.
# `soberania_test.go` salta a prova de que um nó FORA da fronteira não aloja réplicas, a menos
# que `AOS_NODES_OUTRA_REGIAO` nomeie nós elegíveis noutra região. Sem esse nó, a propriedade
# mais forte do ADR-011 — o fail-closed do SERVIDOR à fronteira — continua por provar.
REGIAO_OUTRA="${AOS_NATS_REGION_OUTRA:-us-east}"

# Portas no host. Os três primeiros nós são da região do board; o quarto é o forasteiro.
PORTA_BASE="${AOS_NATS_PORT_BASE:-14225}"

# O Git Bash no Windows converte argumentos que PARECEM caminhos POSIX em caminhos Windows
# antes de o docker os ver — e `--entrypoint /bin/sh` chega ao daemon como
# `C:/Program Files/Git/usr/bin/sh`, que não existe dentro do contentor. Desligar a conversão
# é inerte em Linux (a variável não é lida) e é o que permite correr esta mesma receita nas
# duas máquinas. Descoberto a correr, não suposto.
export MSYS_NO_PATHCONV=1

log() { printf '%s\n' "$*" >&2; }

nome_do_no() { printf '%s-%s' "$PREFIXO" "$1"; }

# regiao_do_no devolve a região que o nó N anuncia. Os nós 1..3 são do board; o 4 é de fora.
regiao_do_no() {
	if [ "$1" -ge 4 ]; then printf '%s' "$REGIAO_OUTRA"; else printf '%s' "$REGIAO"; fi
}

# caminho_deste_script resolve o caminho ABSOLUTO deste ficheiro, porque o `AOS_RESTORE_CMD`
# e corrido pelo `go test` a partir do directorio do PACOTE — nao da raiz do repositorio.
caminho_deste_script() {
	cd "$(dirname "${BASH_SOURCE[0]}")" && printf '%s/%s' "$(pwd)" "$(basename "${BASH_SOURCE[0]}")"
}

# esperar_pelo_lider bloqueia ate o meta-group do Raft ter lider, ou desiste.
#
# ESPERAR PELO META-LEADER, E NAO PELA PORTA. Um socket aberto nao significa cluster formado:
# sem meta-leader eleito, criar um stream R3 falha com `JetStream system temporarily
# unavailable` (10008) — um erro que se le como avaria de configuracao e nao como corrida de
# arranque. A espera e pela propriedade, nao por um `sleep`.
esperar_pelo_lider() {
	local tentativa jsz
	for tentativa in $(seq 1 90); do
		jsz="$(docker exec "$(nome_do_no 1)" wget -qO- http://127.0.0.1:8222/jsz 2>/dev/null || true)"
		case "$jsz" in
		*'"meta_cluster"'*'"leader"'*) return 0 ;;
		esac
		sleep 1
	done
	return 1
}

derrubar() {
	for i in 1 2 3 4; do
		docker rm -f "$(nome_do_no "$i")" >/dev/null 2>&1 || true
	done
	docker network rm "$REDE" >/dev/null 2>&1 || true
}

imprimir_env() {
	# `AOS_NATS_URL` NOMEIA O CLUSTER — os três nós do board, por vírgula. As duas tentativas
	# que isto custou estão escritas porque a escolha não é óbvia em nenhuma direcção:
	#
	# Com a LISTA, os cinco testes de integração do `natsjs` falhavam com «too many colons in
	# address»: esse pacote passa a variável directa ao `natsjs.Connect` → `net.Dial`, e só
	# sabe falar com UM nó. Isso levou-me a exportar um endereço só.
	#
	# Com UM ENDEREÇO, os dois testes de reconexão passaram a falhar — e por uma razão que é o
	# próprio objecto deles: matam o nó a que a ligação aponta e exigem que o cliente reconecte
	# A OUTRO. Com um endereço não há outro, e o teste mede a ausência de alternativa em vez de
	# medir a reconexão.
	#
	# A lista ganha porque a propriedade que ela permite observar — sobreviver à morte do nó
	# ligado — não é observável de outra forma, enquanto a limitação do `natsjs` se resolve
	# onde ela vive: o helper desse pacote toma o PRIMEIRO endereço da lista.
	local enderecos="" i
	for i in 1 2 3; do
		[ -n "$enderecos" ] && enderecos="$enderecos,"
		enderecos="${enderecos}127.0.0.1:$((PORTA_BASE + i - 1))"
	done
	# OS VALORES VÃO ENTRE ASPAS porque dois deles são COMANDOS com espaços. Sem elas,
	# `eval` parte `docker stop aos-ci-nats-3` em três palavras e o `export` recusa a
	# segunda como nome de variável inválido — falha barulhenta, mas só depois de o
	# cluster já estar de pé.
	printf 'export AOS_NATS_URL="%s"\n' "$enderecos"
	printf 'export AOS_NATS_REGION="%s"\n' "$REGIAO"
	# Os nós de fora da fronteira, pelo NOME que o servidor anuncia — é assim que a
	# `placement` os nomeia, não por endereço.
	printf 'export AOS_NODES_OUTRA_REGIAO="%s"\n' "$(nome_do_no 4)"
	# Os dois comandos de falha. Existem porque o cluster é NOSSO: num `services:` do
	# Actions não haveria como derrubar um nó de dentro do job.
	#
	# O `perda_test.go` exige `AOS_KILL_CMD` e FALHA (não salta) se `AOS_NATS_URL` existir
	# sem ele — desenho deliberado dele, para que ninguém meça perda de nó sem perder um nó.
	printf 'export AOS_KILL_CMD="%s"\n' "docker stop $(nome_do_no 3)"
	printf 'export AOS_KILL_CONNECTED_CMD="%s"\n' "docker stop $(nome_do_no 1)"
	# O TERCEIRO É UM PREFIXO, e a razão é o AOS-449: o nó que aloja um consumidor R1 é
	# SORTEADO pelo servidor e só se conhece depois de ele existir. Um comando fixo acertava-lhe
	# uma vez em três — e o defeito que isso escondia lia-se como flake. O teste acrescenta o
	# nome que o servidor anuncia, que é o do contentor (`--server_name`).
	printf 'export AOS_KILL_NODE_CMD="%s"\n' "docker stop"
	# O RESTAURO NÃO É OPCIONAL, E FOI UMA EXECUÇÃO QUE O PROVOU.
	#
	# Sem ele, os testes destrutivos deixam dois nós em baixo e TUDO o que corre a seguir
	# falha com «JetStream system temporarily unavailable» (10008) — porque com dois nós
	# vivos em quatro não há onde colocar um stream R3. Na primeira medição isto produziu
	# 13 falhas cuja causa aparente era soberania e arbitragem, e cuja causa real era o
	# cluster estar em baixo. Um diagnóstico inteiro a apontar para o sítio errado.
	#
	# Os testes já suportam isto (`t.Cleanup` sobre `AOS_RESTORE_CMD`, perda_test.go:74 e
	# reconexao_test.go:38,95); o que faltava era alguém definir a variável.
	#
	# Aponta ao subcomando `restore` deste script, e não a um `docker start` cru, porque
	# reerguer os contentores não é o mesmo que ter cluster: o `docker start` devolve
	# imediatamente e o stream R3 seguinte ainda apanharia o cluster sem meta-leader.
	printf 'export AOS_RESTORE_CMD="%s"\n' "bash $(caminho_deste_script) restore"
}

levantar() {
	command -v docker >/dev/null 2>&1 || { log "FAIL nats-cluster: docker não está disponível"; exit 1; }

	derrubar
	docker network create "$REDE" >/dev/null

	# As rotas nomeiam os QUATRO nós. Um nó que não esteja na lista de rotas não entra no
	# meta-group do Raft e a sua tag nunca é elegível para `placement` — o que faria o teste
	# da outra região passar por ausência em vez de por recusa.
	local rotas="" i
	for i in 1 2 3 4; do
		[ -n "$rotas" ] && rotas="$rotas,"
		rotas="$rotas""nats://$(nome_do_no "$i"):6222"
	done

	for i in 1 2 3 4; do
		local nome porta
		nome="$(nome_do_no "$i")"
		porta=$((PORTA_BASE + i - 1))

		# O fragmento de configuração é ESCRITO NO ARRANQUE, dentro do contentor, porque
		# `server_tags` não tem flag. É ficheiro próprio e não toca no `nats-server.conf` da
		# imagem: as flags aplicam-se por cima do `-c`, que é a ordem que a infra assume.
		docker run -d --name "$nome" --network "$REDE" \
			-p "127.0.0.1:$porta:4222" \
			--entrypoint sh \
			"$IMAGEM" -c "
				mkdir -p /etc/nats /data
				printf 'server_tags: [\"region:%s\"]\n' '$(regiao_do_no "$i")' > /etc/nats/aos-soberania.conf
				exec nats-server \
					-c /etc/nats/aos-soberania.conf \
					-js -sd /data -p 4222 -m 8222 \
					--server_name '$nome' \
					--cluster_name '$NOME_CLUSTER' \
					--cluster nats://0.0.0.0:6222 \
					--routes '$rotas'
			" >/dev/null
	done

	if ! esperar_pelo_lider; then
		log "FAIL nats-cluster: o meta-leader não foi eleito a tempo"
		docker logs "$(nome_do_no 1)" 2>&1 | tail -20 >&2
		exit 1
	fi
	log "OK   nats-cluster: 4 nós de pé ($REGIAO ×3, $REGIAO_OUTRA ×1), meta-leader eleito"
	imprimir_env
}

# restaurar reergue os nós que um teste destrutivo derrubou, e ESPERA pelo cluster.
#
# É idempotente de propósito: `docker start` sobre um contentor já a correr não faz nada, e os
# dois testes destrutivos matam nós DIFERENTES. Um só comando de restauro tem de servir os
# dois, porque `AOS_RESTORE_CMD` é uma variável e não uma por teste.
restaurar() {
	local i
	for i in 1 2 3 4; do
		docker start "$(nome_do_no "$i")" >/dev/null 2>&1 || true
	done
	if ! esperar_pelo_lider; then
		log "FAIL nats-cluster: restauro sem meta-leader — os testes seguintes vão falhar por 10008"
		return 1
	fi
	log "OK   nats-cluster: restaurado"
}

case "${1:-up}" in
up) levantar ;;
down) derrubar; log "OK   nats-cluster: derrubado" ;;
env) imprimir_env ;;
restore) restaurar ;;
*)
	log "uso: $0 [up|down|env|restore]"
	exit 2
	;;
esac
