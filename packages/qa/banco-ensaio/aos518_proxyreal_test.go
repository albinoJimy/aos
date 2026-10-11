package bancoensaio

// AOS-518 — A QUALIFICAÇÃO ATRÁS DO PROXY REAL.
//
// A regra do veredicto não depende de correr sem proxy: com a imagem de produção do proxy (fixada
// pelo digest) entre o nó de ensaio e o provider falso, o falso «bom» sai `qualificado` e os falsos
// «maus» saem `recusado`, com as mesmas razões do modo falso. Atrás do proxy quem declara o modelo
// servido é o PROXY — por isso o falso que troca o modelo se ensaia aqui com um perfil que espera
// outro nome: é o proxy a dizer que serviu um modelo que o perfil não espera.
//
// PRECISA DE DOCKER e da imagem já descarregada. Só corre a pedido:
//
//	bash scripts/ci/banco-ensaio-proxy.sh
//
// O QUE NÃO PROVA: nada sobre um modelo real — o fornecedor é o provider falso.

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAOS518_ProxyReal_AQualificacaoAtrasDoProxy(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 — a qualificacao atras do proxy precisa de Docker e da imagem de producao do proxy; " +
			"correr com `bash scripts/ci/banco-ensaio-proxy.sh`. POR VERIFICAR nesta execucao: que o veredicto e o mesmo com o proxy real no meio. " +
			"A mesma qualificacao, sem proxy, correu (TestAOS518_Falso_OBomSaiQualificado e TestAOS518_Falso_OsCincoMausSaemRecusadosCadaUmComASuaRazao).")
	}
	outroModelo := strings.Replace(aos518PerfilSimples, "openai/modelo-falso-do-banco", "openai/um-modelo-que-o-proxy-nao-serve", 1)
	cenarios := []struct {
		nome, perfil, roteiro string
		codigo                int
		veredicto             string
		razoes                []string
	}{
		{"bom", aos518PerfilSimples, "cumpre", SaidaQualificadoEmEnsaio, VeredictoQualificado, []string{}},
		{"nao chama a tool", aos518PerfilSimples, "texto", SaidaModeloRecusado, VeredictoRecusado, []string{RazaoModeloNaoCumpridos, RazaoModeloSemToolCall, RazaoAmostraIncompleta}},
		{"recusa o segundo turno", aos518PerfilSimples, "rejeita_segundo", SaidaModeloRecusado, VeredictoRecusado, []string{RazaoModeloNaoCumpridos, RazaoModelo4xxNoSegundo, RazaoAmostraIncompleta}},
		{"responde vazio", aos518PerfilSimples, "vazia", SaidaModeloRecusado, VeredictoRecusado, []string{RazaoModeloNaoCumpridos, RazaoModeloRespostasVazias, RazaoSemToolsNaoCumpridos, RazaoAmostraIncompleta}},
		{"o proxy serve outro modelo", outroModelo, "cumpre", SaidaModeloRecusado, VeredictoRecusado, []string{RazaoModeloServidoDiferente}},
	}
	medidas := map[string]any{"imagem": ImagemDoProxy}
	for _, c := range cenarios {
		saida := t.TempDir()
		e := executar(t, Ambiente{}, "proxy", "--binario-do-falso", binario, "--experiencia", "qualificacao",
			"--perfil", aos516Ficheiro(t, c.perfil), "--roteiro", c.roteiro, "--saida", saida, "--silencioso")
		if e.codigo != c.codigo {
			t.Errorf("%s: codigo %d, quer %d\n%s", c.nome, e.codigo, c.codigo, e.stderr)
			continue
		}
		r, cru := aos518LerRelatorio(t, aos518Relatorios(t, saida)[0])
		q := aos518Veredicto(t, r)
		if q.Veredicto != c.veredicto || !reflect.DeepEqual(q.Razoes, c.razoes) {
			t.Errorf("%s: %s %v, quer %s %v", c.nome, q.Veredicto, q.Razoes, c.veredicto, c.razoes)
		}
		if r.Modo != ModoProxy || q.Rota.ImagemDoProxy != ImagemDoProxy || q.Contagens.NosComToolsNaAmostra != 40 || q.Contagens.TurnosSemModeloReportado != 0 {
			t.Errorf("%s: modo %s, imagem %s, amostra %d, turnos sem modelo reportado %d", c.nome, r.Modo, q.Rota.ImagemDoProxy, q.Contagens.NosComToolsNaAmostra, q.Contagens.TurnosSemModeloReportado)
		}
		if _, err := VerificarDigestDoRelatorio(cru); err != nil {
			t.Errorf("%s: o digest do relatorio nao confere: %v", c.nome, err)
		}
		verSemFugas(t, "qualificacao atras do proxy: "+c.nome, tudoOQueFoiEscrito(t, e, saida), append(proibidosDeTexto(t), "sk-falso-", "sk-ensaio-", "Bearer "))
		medidas[c.nome] = map[string]any{
			"veredicto": q.Veredicto, "razoes": q.Razoes, "pedidos": r.Pedidos.Enviados, "http": r.Taxas.HTTP,
			"cumpridos": q.Contagens.NosComToolsCumpridos, "turnos": q.Contagens.Turnos, "modelo_igual": q.Contagens.TurnosComModeloIgual,
			"modelo_diferente": q.Contagens.TurnosComModeloDiferente, "4xx_nos_segundos_turnos": q.Contagens.QuatroXXComMensagemTool,
		}
	}
	// O QUE CHEGA AO NO quando o fornecedor fecha um turno sem tool call com um motivo que nao e
	// `stop` (a revisao adversarial nao o conseguiu medir sem o proxy): uma passagem da bateria por
	// comportamento, e os motivos de paragem que o runtime registou.
	motivos := map[string]any{}
	for _, c := range []Comportamento{ComportamentoSemToolEndTurn, ComportamentoSemToolContentFilter, ComportamentoSemToolSemMotivo} {
		saida := t.TempDir()
		e := executar(t, Ambiente{}, "proxy", "--binario-do-falso", binario, "--roteiro", string(c), "--saida", saida, "--silencioso")
		achados := aos518Relatorios(t, saida)
		if len(achados) != 1 {
			t.Errorf("%s atras do proxy: codigo %d, relatorios %v\n%s", c, e.codigo, achados, e.stderr)
			continue
		}
		r, _ := aos518LerRelatorio(t, achados[0])
		k := contarParaQualificacao(r.Terminou, 5, 2, r.Observacoes)
		if k.SemToolCallAPrimeira != 5 {
			t.Errorf("%s atras do proxy: L2 contou %d de %d primeiras tentativas; quer 5", c, k.SemToolCallAPrimeira, k.PrimeirasTentativasComTools)
		}
		motivos[string(c)] = map[string]any{"motivos_de_paragem": r.Taxas.MotivosDeParagem, "desfechos": r.Taxas.Desfechos, "http": r.Taxas.HTTP, "sem_tool_call_a_primeira": k.SemToolCallAPrimeira}
	}
	medidas["motivos_de_paragem_atras_do_proxy"] = motivos
	medidas["pass"] = !t.Failed()
	resumo, _ := json.Marshal(medidas)
	t.Logf("AOS_BANCO_QUALIFICACAO_REPORT %s", resumo)
}
