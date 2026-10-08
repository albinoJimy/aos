package bancoensaio

// AOS-512 — A BATERIA ATRÁS DO PROXY REAL.
//
// A MESMA bateria e o MESMO roteiro do provider falso que correm em CI, com a imagem de
// produção do proxy (LiteLLM, fixada pelo digest) no meio: nó de ensaio → proxy → provider
// falso num contentor. É o modo `proxy` da linha de comandos, inteiro.
//
// PRECISA DE DOCKER, da imagem já descarregada (não a descarrega) e de um binário LINUX do
// banco para fazer de provider dentro do contentor. Só corre a pedido:
//
//	bash scripts/ci/banco-ensaio-proxy.sh
//
// O QUE PROVA: que o pedido que o nó de ensaio monta atravessa o proxy de produção e dá os
// mesmos desfechos que sem ele; que a chave da rota chega ao fornecedor pelo ambiente do
// contentor (o provider falso recusa com 401 um pedido sem ela); e que nem ela nem a chave
// mestra do proxy aparecem em saída nenhuma.
//
// O QUE NÃO PROVA: nada sobre um modelo real — o fornecedor é o provider falso.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAOS512_ProxyReal_ABateriaAtrasDoProxy(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 — o modo proxy do banco precisa de Docker e da imagem de producao do proxy; " +
			"correr com `bash scripts/ci/banco-ensaio-proxy.sh`. POR VERIFICAR nesta execucao: que a bateria atravessa o proxy real. " +
			"A mesma bateria, sem proxy, correu (TestAOS512_Falso_BateriaInteira_TaxasExactas).")
	}
	saida := t.TempDir()
	e := executar(t, Ambiente{}, "proxy", "--binario-do-falso", binario, "--amostras", "2", "--saida", saida)
	if e.codigo != SaidaOK {
		t.Fatalf("o modo proxy falhou: codigo %d\n%s", e.codigo, e.stderr)
	}
	achados, _ := filepath.Glob(filepath.Join(saida, "ensaio-proxy-bateria-*.json"))
	if len(achados) != 1 {
		t.Fatalf("relatorios: %v", achados)
	}
	cru, _ := os.ReadFile(achados[0])
	var r Relatorio
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	// Os desfechos são os do modo falso (TestAOS512_Falso_BateriaInteira_TaxasExactas): o proxy
	// não muda o que o loop faz com cada resposta.
	if quer := map[string]int{"cumprido": 10, "empty_output": 2, "truncated": 1, "erro_http": 2, "contract_unmet_no_call": 1}; !reflect.DeepEqual(r.Taxas.Desfechos, quer) {
		t.Errorf("desfechos atras do proxy = %v, quer %v", r.Taxas.Desfechos, quer)
	}
	verTaxa(t, "sem tool call na primeira", r.Taxas.SemToolCallNaPrimeira, 1, 9)
	verTaxa(t, "segundo turno aceite", r.Taxas.SegundoTurnoAceite, 10, 11)
	verTaxa(t, "factos ausentes", r.Taxas.FactosAusentes, 1, 10)
	verTaxa(t, "resposta vazia", r.Taxas.RespostaVazia, 2, 15)
	verTaxa(t, "cortada", r.Taxas.Cortada, 1, 15)
	// Nenhum 401: a chave da rota chegou ao provider falso, que a exige.
	if r.Taxas.HTTP["401"] != 0 || r.Taxas.HTTP["200"] != 25 {
		t.Errorf("codigos HTTP atras do proxy = %v (um 401 seria a chave da rota a nao chegar ao fornecedor)", r.Taxas.HTTP)
	}
	tudo := tudoOQueFoiEscrito(t, e, saida)
	verSemFugas(t, "modo proxy", tudo, append(proibidosDeTexto(t), "sk-falso-", "sk-ensaio-", "Bearer "))
	if r.Modo != ModoProxy || !strings.HasPrefix(r.Digests.Rota, "sha256:") {
		t.Errorf("cabecalho do relatorio do modo proxy mal formado")
	}
	resumo, _ := json.Marshal(map[string]any{"imagem": ImagemDoProxy, "runs": r.Taxas.N, "pedidos": r.Pedidos.Enviados,
		"desfechos": r.Taxas.Desfechos, "http": r.Taxas.HTTP, "fichas": r.Taxas.Fichas, "pass": !t.Failed()})
	t.Logf("AOS_BANCO_PROXY_REPORT %s", resumo)
}
