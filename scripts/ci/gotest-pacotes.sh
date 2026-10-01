#!/usr/bin/env bash
# gotest-pacotes.sh — o veredicto POR PACOTE de uma execução de `go test -v` (AOS-452).
#
# Biblioteca: faz-se `source` dela, não se corre. Vive à parte do `nats.sh` para que o
# `selftest.sh` exercite o MESMO classificador sobre saída real de `go test`, sem cluster —
# o molde do `sca_decide` (§C) e do `coverage_meets_min` (§I).
#
# ─── O DEFEITO QUE FECHA ───────────────────────────────────────────────────────────────────
#
# O `nats.sh` contava falhas por módulo a partir das linhas `--- FAIL`, e o `rc` do `go test`
# só servia para escrever «(vermelho)» na tabela. Três modos de terminar um pacote em FAIL
# NÃO escrevem `--- FAIL` nenhum, e em todos eles o gate saía verde:
#
#   · timeout: `panic: test timed out after 10m0s` — o teste em curso é morto sem veredicto.
#     Foi assim que se viu (AOS-432, 2026-09-26): o `cmd/aos-orq` rebentou aos 10 min, a tabela
#     disse FAIL=0 (vermelho), e o gate saiu 0;
#   · `FAIL <pacote> [build failed]` — o pacote nem compilou; zero testes, zero `--- FAIL`;
#   · `os.Exit`/`log.Fatal` fora de um teste (num `TestMain`, numa goroutine) — `exit status 1`.
#
# E um quarto que ESCREVE `--- FAIL` mas não pode ser explicado por ele: um `panic` num teste
# aborta o binário do pacote, e todos os testes que vinham a seguir NÃO CORRERAM. Se o teste que
# entrou em pânico estiver declarado como falha conhecida, a contagem por nome dizia «falha
# declarada» e calava-se sobre os que ficaram por medir. Tolera-se uma falha declarada; não se
# tolera um aborto.
#
# ─── PORQUE É POR PACOTE E NÃO POR MÓDULO ──────────────────────────────────────────────────
#
# O gate corre `go test ./...` por módulo, e um módulo tem vários pacotes. «O módulo saiu ≠ 0 e
# há falhas declaradas» não chega: a falha declarada num pacote explicava o timeout de OUTRO, e
# o buraco reabria-se pela porta do lado. O `go test` imprime cada pacote em bloco contíguo,
# fechado por uma linha `ok  \t<pkg>`, `FAIL\t<pkg>` ou `?   \t<pkg>` — e é essa linha que dá o
# veredicto de cada um.

# gotest_pacotes_corre <dir> <alvos> <timeout> <saida>
#   Corre `go test <alvos> -count=1 -v -timeout=<timeout>` em <dir>, com stdout+stderr em
#   <saida>, e devolve o rc do `go`. É o comando do gate `nats`; o `selftest.sh` chama-o sobre
#   pacotes sintéticos para que o que ele prova seja sobre os flags reais, não sobre uma cópia.
#   `-v` porque a contagem lê `--- PASS/FAIL/SKIP`; `<alvos>` passa por `eval` porque é uma
#   lista de padrões (`./jetstream/ ./natsjs/`).
gotest_pacotes_corre() {
  local dir="$1" alvos="$2" timeout="$3" saida="$4"
  (cd "$dir" && eval "go test $alvos -count=1 -v -timeout=$timeout") >"$saida" 2>&1
}

# gotest_pacotes_inexplicados <saida> <rc_go>
#   Imprime, uma por linha, `<pacote>\t<causa>` para cada pacote que terminou em FAIL sem que
#   um `--- FAIL` limpo o explique. Devolve 1 se houver algum, 0 se não.
#
#   Um pacote em FAIL está EXPLICADO sse o seu bloco tem pelo menos um `--- FAIL` de topo e
#   nenhum sinal de aborto (timeout, panic, fatal error do runtime). Se essas falhas são novas
#   ou declaradas decide-o o chamador pelo nome, como antes — isto só responde a «o `rc≠0`
#   deste pacote vem de testes que falharam, ou de algo que impediu os testes de correr?».
#
#   Se o `go test` saiu ≠ 0 e NENHUM pacote se declarou em FAIL, o próprio `go` falhou antes de
#   correr testes (módulo em falta, flag inválida, `go.mod` desactualizado): reporta-se com o
#   pacote `(go test)`. Um `-timeout=0` mal escrito, por exemplo, cai aqui.
gotest_pacotes_inexplicados() {
  local saida="$1" rc_go="$2" achados
  achados="$(awk -v rc_go="$rc_go" '
    BEGIN { nfail = 0; timeout = 0; panico = 0; fatal = 0; pkgs_em_fail = 0 }
    /^--- FAIL/                     { nfail++ }
    /^panic: test timed out after / { timeout = 1 }
    /^panic: /                      { panico = 1 }
    /^fatal error: /                { fatal = 1 }
    /^(ok  |FAIL|\?   )\t/ {
      split($0, campo, "\t"); pkg = campo[2]; sub(/ .*/, "", pkg)
      if ($0 ~ /^FAIL\t/) {
        pkgs_em_fail++
        causa = ""
        if ($0 ~ /\[build failed\]/)      causa = "NAO COMPILOU (build failed) — nenhum teste correu"
        else if ($0 ~ /\[setup failed\]/) causa = "setup failed — nenhum teste correu"
        else if (timeout)                 causa = "TIMEOUT — o teste em curso foi morto sem veredicto e os seguintes nao correram"
        else if (panico)                  causa = "PANIC — o binario do pacote abortou e os testes seguintes nao correram"
        else if (fatal)                   causa = "fatal error do runtime — o binario do pacote abortou"
        else if (nfail == 0)              causa = "saiu != 0 sem nenhum --- FAIL (os.Exit/log.Fatal fora de um teste?)"
        if (causa != "") print pkg "\t" causa
      }
      nfail = 0; timeout = 0; panico = 0; fatal = 0
    }
    END {
      if (rc_go != 0 && pkgs_em_fail == 0)
        print "(go test)\to go test saiu rc=" rc_go " e nenhum pacote se declarou em FAIL — o go falhou antes de correr os testes"
    }
  ' "$saida")"
  [ -n "$achados" ] || return 0
  printf '%s\n' "$achados"
  return 1
}

# gotest_saida_do_teste <saida> <NomeDoTeste> [max_linhas]
#   Imprime as linhas das execuções de um teste que NÃO passaram (e dos seus subtestes): o
#   cabeçalho, o que ele escreveu por `t.Logf`/`t.Errorf`/`t.Fatalf`, e a linha do veredicto. É
#   o que o log do gate tem de mostrar quando um teste falha (AOS-455).
#
#   O DEFEITO QUE FECHA. O gate imprimia `grep -A8` a partir do `--- FAIL`. Em `go test -v` as
#   linhas da asserção saem ANTES dessa linha — depois do `=== RUN`/`=== CONT`/`=== NAME` do
#   teste — e o ficheiro completo é um `mktemp` que não é publicado. Em três vermelhos do gate
#   `nats` (AOS-455) ninguém viu as contagens: o log mostrava o `--- FAIL` e os `--- PASS` dos
#   testes SEGUINTES, que é ruído. Sem `-v` (a passagem da cobertura) as linhas saem DEPOIS do
#   `--- FAIL`; a mesma regra serve os dois formatos.
#
#   A regra: cada linha pertence ao último teste anunciado por um cabeçalho `=== …` ou por uma
#   linha de veredicto `--- …`; uma linha de pacote (`FAIL`, `ok`, `PASS`, `exit status`) não
#   pertence a teste nenhum. Testes paralelos intercalam, mas o `go test -v` re-anuncia o dono
#   (`=== CONT`/`=== NAME`) antes de cada bloco de output — e é isso que a regra segue.
#
#   POR EXECUÇÃO, e só as que não passaram. Com `-count=N` (o bloco de repetição do `nats.sh`) o
#   mesmo nome corre N vezes; cada `=== RUN <nome>` abre uma execução, e com `-v` o veredicto
#   fecha-a. Imprimem-se as execuções com `--- FAIL`, com `--- SKIP` ou SEM veredicto (o binário
#   abortou a meio dela); as que passaram são ruído — e era esse ruído que, cortado ANTES de se
#   filtrar, podia empurrar a asserção de uma repetição tardia para fora do log (revisão do
#   AOS-455). `max_linhas` (omissão 150) conta só o que se imprime; acima dele corta-se o MEIO
#   (ficam o cabeçalho e o fim, onde estão o `t.Fatalf` e o veredicto), e o corte é declarado.
#
#   LIMITE CONHECIDO: o stdout CRU de um teste (`fmt.Println`, e não `t.Log`) não traz o nome
#   dele, e o `go test -v` não o re-anuncia; num teste paralelo fica atribuído ao último teste
#   anunciado — o outro. As asserções (`t.Errorf`/`t.Fatalf`) e o `t.Logf` não têm este limite.
gotest_saida_do_teste() {
  local saida="$1" nome="$2" max="${3:-150}"
  awk -v nome="$nome" -v max="$max" '
    function deste(t) { return t == nome || index(t, nome "/") == 1 }
    function guarda(l) { seg[++nl] = l }
    function fecha(   i) {
      if (nl > 0 && veredicto != "PASS")
        for (i = 1; i <= nl; i++) out[++n] = seg[i]
      nl = 0; veredicto = ""
    }
    /^=== (RUN|CONT|PAUSE|NAME)[[:space:]]/ {
      dono = $3
      if ($2 == "RUN" && dono == nome) fecha()
      if (deste(dono)) guarda($0)
      next
    }
    /^[[:space:]]*--- (FAIL|PASS|SKIP): / {
      t = $0; sub(/^[[:space:]]*--- (FAIL|PASS|SKIP): /, "", t); sub(/ .*/, "", t)
      dono = t
      if (t == nome && $0 ~ /^---/) {
        # Sem `-v` o veredicto ABRE a execução (as linhas vêm depois dele): um segundo veredicto
        # de topo sem `=== RUN` entre os dois é uma execução nova.
        if (veredicto != "") fecha()
        veredicto = $2; sub(/:$/, "", veredicto)
      }
      if (deste(dono)) guarda($0)
      next
    }
    /^(FAIL|PASS|ok  |\?   |exit status )/ { dono = ""; next }
    deste(dono) { guarda($0) }
    END {
      fecha()
      if (n <= max) { for (i = 1; i <= n; i++) print out[i]; exit }
      # Corta-se o MEIO: o cabeçalho fica, e o fim também — é lá que estão o `t.Fatalf` e o
      # veredicto da execução que falhou.
      cab = int(max / 2); cauda = max - cab
      for (i = 1; i <= cab; i++) print out[i]
      printf "       … %d linha(s) do teste %s cortadas aqui (max_linhas=%d)\n", n - max, nome, max
      for (i = n - cauda + 1; i <= n; i++) print out[i]
    }
  ' "$saida"
}

# gotest_pacotes_diagnostico <saida>
#   As linhas que dizem PORQUÊ, para o log do gate: a mensagem do panic, os testes que estavam
#   a correr quando o timeout disparou, e os erros de compilação. Sem isto o gate dizia «vermelho»
#   e mandava quem lê abrir o artefacto — que em CI não existe, porque a saída é um `mktemp`.
gotest_pacotes_diagnostico() {
  grep -E '^(panic: |fatal error: |# )|^[[:space:]]+running tests:|^[[:space:]]+Test[^ ]+ \([0-9.hms]+\)$|\.go:[0-9]+:[0-9]+: |^exit status ' "$1" \
    | head -16 | sed 's/^/       /' || true
}
