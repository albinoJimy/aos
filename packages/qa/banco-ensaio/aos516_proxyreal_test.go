package bancoensaio

// AOS-516 — A DEVOLUÇÃO DO ESTADO ATRÁS DO PROXY REAL, PELA ROTA `anthropic/…`.
//
// O modo `proxy` da linha de comandos com o provider falso do ESTADO: nó de ensaio → a imagem de
// produção do proxy (LiteLLM, fixada pelo digest) com uma rota `anthropic/…` → o falso num
// contentor, a falar o wire de mensagens da Anthropic. É o caminho de TRADUÇÃO do proxy: o
// gateway envia o estado no wire de chat, e o proxy converte-o em blocos de conteúdo.
//
// É MEDIÇÃO. O teste falha se o ensaio não se montar, se o relatório não sair, se o proxy não
// chegar ao falso pelo wire de mensagens ou se um segredo ou um byte do estado aparecer numa
// saída. O que o proxy faz ao estado — se os blocos chegam, com que forma, e o bloco de texto
// que ele acrescenta quando o `content` do `assistant` é vazio — fica REGISTADO, sem falhar.
//
// PRECISA DE DOCKER, da imagem já descarregada e de um binário LINUX do banco. Só a pedido:
//
//	bash scripts/ci/banco-ensaio-proxy.sh
//
// O QUE NÃO PROVA: nada sobre o fornecedor real. O falso confere os valores que emitiu, não
// assinaturas; se a Anthropic aceita um bloco de texto entre o raciocínio e a tool call só a
// corrida com o modelo real o diz.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// aos516ModeloDoProxy é o nome que a rota anthropic/ do ensaio pede. Tem de ser um nome que a
// imagem fixada do proxy CONHEÇA como modelo com raciocínio: com um nome desconhecido o proxy
// não deixa passar o parâmetro `thinking` (medido no AOS-515).
const aos516ModeloDoProxy = "claude-sonnet-4-5"

func TestAOS516_ProxyReal_ADevolucaoPelaRotaAnthropic(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 — a devolucao atras do proxy real precisa de Docker e da imagem de producao do proxy; " +
			"correr com `bash scripts/ci/banco-ensaio-proxy.sh`. POR VERIFICAR nesta execucao: o que o proxy faz ao estado opaco na rota anthropic/. " +
			"O mesmo, sem proxy e no wire de chat, correu (TestAOS516_Falso_ObrigatorioComOExigente_Passa).")
	}
	com := strings.Replace(aos516PerfilObrigatorio, "modelo-de-ensaio", aos516ModeloDoProxy, 1)
	sem := strings.Replace(aos516PerfilNunca, "modelo-de-ensaio", aos516ModeloDoProxy, 1)
	medidas := map[string]any{"imagem": ImagemDoProxy, "rota": "anthropic/" + aos516ModeloDoProxy}
	for _, c := range []struct{ nome, perfil, estado string }{
		{"obrigatorio_contra_o_que_exige", com, EstadoExige},
		{"nunca_contra_o_que_exige", sem, EstadoExige},
	} {
		saida := t.TempDir()
		e := executar(t, Ambiente{}, "proxy", "--binario-do-falso", binario, "--estado", c.estado, "--turnos-do-falso", "3",
			"--perfil", aos516Ficheiro(t, c.perfil), "--amostras", "2", "--saida", saida)
		if e.codigo != SaidaOK {
			t.Fatalf("%s: o modo proxy falhou: codigo %d\n%s", c.nome, e.codigo, e.stderr)
		}
		r := aos516Relatorio(t, saida)
		f := r.FormaNoFornecedor
		if f == nil || f.Wire != WireDeMensagens || f.Pedidos == 0 {
			t.Fatalf("%s: o proxy nao chegou ao falso pelo wire de mensagens: %+v", c.nome, f)
		}
		if r.Taxas.HTTP["401"] != 0 {
			t.Errorf("%s: um 401 e a chave da rota a nao chegar ao fornecedor: %v", c.nome, r.Taxas.HTTP)
		}
		// A devolucao JULGA-SE: o veredicto do banco, e nao so a forma do que chegou.
		quer := QualificacaoCumprida
		if c.perfil == sem {
			quer = QualificacaoNaoCumprida
		}
		if q := r.Qualificacao; q == nil || q.Veredicto != quer {
			t.Errorf("%s: qualificacao = %+v, quer %s", c.nome, q, quer)
		}
		verSemFugas(t, c.nome, tudoOQueFoiEscrito(t, e, saida), append(append(proibidosDeTexto(t), SentinelasDoEstado()...), "sk-falso-", "sk-ensaio-", "Bearer "))
		medidas[c.nome] = map[string]any{
			"runs": r.Taxas.N, "desfechos": r.Taxas.Desfechos, "http": r.Taxas.HTTP, "tipos_de_erro": r.Taxas.TiposDeErro,
			"devolucao": r.Taxas.Devolucao, "qualificacao": r.Qualificacao, "forma_no_fornecedor": f,
		}
	}
	medidas["pass"] = !t.Failed()
	resumo, _ := json.Marshal(medidas)
	t.Logf("AOS_BANCO_DEVOLUCAO_REPORT %s", resumo)
}
