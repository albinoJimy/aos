package bancoensaio

// AOS-516 — A DEVOLUÇÃO DO ESTADO ATRÁS DO PROXY REAL, NA FORMA DA OPENROUTER.
//
// O modo `proxy` da linha de comandos com o provider falso do estado na forma da OpenRouter
// (`reasoning` + `reasoning_details` com assinatura): nó de ensaio → a imagem de produção do
// proxy (LiteLLM, fixada pelo digest) → o falso num contentor. Corre pelas DUAS formas de rota
// que o proxy tem para a OpenRouter — `openrouter/<autor>/<modelo>` (o adaptador próprio) e
// `openai/<autor>/<modelo>` (o adaptador genérico com a base da OpenRouter) — e, em cada uma,
// com os dois parâmetros de raciocínio que o perfil de uma rota sabe exprimir.
//
// JULGA a rota escolhida ([PrefixoDaOpenRouter]) pelo veredicto do banco. Com o perfil que não
// devolve estado tem de ser `nao_cumprida`. Com o que devolve, HOJE também é `nao_cumprida`, e o
// teste prende a causa MEDIDA (2026-10-10): o proxy entrega a resposta da OpenRouter com
// `reasoning_details` dentro de `message.provider_specific_fields`; o gateway devolve cada campo
// ao sítio de onde veio (ADR-040 §2.9); e o proxy manda esse saco tal e qual ao fornecedor, que
// só lê `reasoning_details` no topo da mensagem. No dia em que o gateway souber repor o campo no
// topo, este teste fica vermelho de propósito, para se trocar o veredicto esperado.
//
// A outra forma de rota e as MEDIÇÕES DIRECTAS ao proxy (o parâmetro `reasoning` nativo, os
// `reasoning_details` no topo da mensagem e os erros na forma da OpenRouter) ficam no relatório
// do teste — nomes de chaves e códigos, nunca valores.
//
// PRECISA DE DOCKER, da imagem já descarregada e de um binário LINUX do banco. Só a pedido:
//
//	bash scripts/ci/banco-ensaio-proxy.sh
//
// O QUE NÃO PROVA: nada sobre a OpenRouter real. O falso emite a forma DOCUMENTADA dela e
// confere os valores que emitiu, não assinaturas; se a OpenRouter aceita o parâmetro de
// raciocínio na forma em que ele lhe chega só a corrida com o modelo real o diz.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// aos516ModeloOpenRouter é o nome, na OpenRouter, do modelo da rota do ensaio. Tem de ser um
// nome que a imagem fixada do proxy conheça como modelo com raciocínio: é isso que o faz
// deixar passar os parâmetros `thinking` e `reasoning_effort`.
const aos516ModeloOpenRouter = "anthropic/claude-sonnet-4.5"

// aos516PerfilOpenRouter devolve um perfil candidato com a rota e os parâmetros dados.
func aos516PerfilOpenRouter(rota, params, devolver string) string {
	versao := "1.3.0"
	if devolver == "nunca" {
		versao = "1.2.0"
	}
	return `{"requested":"rota-de-ensaio","expected_model":"` + rota + `","wire_class":"openai-chat-completions","capabilities":["tools"],` +
		`"params":` + params + `,"projection_version":"` + versao + `","devolver":"` + devolver + `"}`
}

const (
	aos516ParamsThinking = `{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000}`
	aos516ParamsEffort   = `{"reasoning_effort":"medium","max_tokens":16000}`
)

func TestAOS516_ProxyReal_ADevolucaoPelaOpenRouter(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 — a devolucao atras do proxy real precisa de Docker e da imagem de producao do proxy; " +
			"correr com `bash scripts/ci/banco-ensaio-proxy.sh`. POR VERIFICAR nesta execucao: o que o proxy faz ao estado na forma da OpenRouter. " +
			"O mesmo, sem proxy, correu (TestAOS516_Falso_OpenRouter_ObrigatorioCumpre).")
	}
	medidas := map[string]any{"imagem": ImagemDoProxy, "rota_escolhida": PrefixoDaOpenRouter + "/" + aos516ModeloOpenRouter}
	for _, c := range []struct{ nome, prefixo, params, devolver string }{
		{"openrouter_thinking_obrigatorio", "openrouter", aos516ParamsThinking, "obrigatorio"},
		{"openrouter_effort_obrigatorio", "openrouter", aos516ParamsEffort, "obrigatorio"},
		{"openrouter_effort_nunca", "openrouter", aos516ParamsEffort, "nunca"},
		{"openai_thinking_obrigatorio", "openai", aos516ParamsThinking, "obrigatorio"},
		{"openai_effort_obrigatorio", "openai", aos516ParamsEffort, "obrigatorio"},
	} {
		saida := t.TempDir()
		perfil := aos516Ficheiro(t, aos516PerfilOpenRouter(c.prefixo+"/"+aos516ModeloOpenRouter, c.params, c.devolver))
		e := executar(t, Ambiente{}, "proxy", "--binario-do-falso", binario, "--estado", EstadoExige, "--forma-do-falso", FormaOpenRouter,
			"--turnos-do-falso", "3", "--perfil", perfil, "--amostras", "2", "--saida", saida)
		if e.codigo != SaidaOK {
			t.Fatalf("%s: o modo proxy falhou: codigo %d\n%s", c.nome, e.codigo, e.stderr)
		}
		r := aos516Relatorio(t, saida)
		f := r.FormaNoFornecedor
		if f == nil || f.Wire != WireDeChat || f.Pedidos == 0 {
			t.Fatalf("%s: o proxy nao chegou ao falso pelo wire de chat: %+v", c.nome, f)
		}
		if r.Taxas.HTTP["401"] != 0 {
			t.Errorf("%s: um 401 e a chave da rota a nao chegar ao fornecedor: %v", c.nome, r.Taxas.HTTP)
		}
		if c.prefixo == PrefixoDaOpenRouter {
			// A rota escolhida JULGA-SE pelo veredicto do banco.
			if q := r.Qualificacao; q == nil || q.Veredicto != QualificacaoNaoCumprida {
				t.Errorf("%s: qualificacao = %+v, quer %s (se passou a cumprida, a lacuna da devolucao fechou: troque o veredicto esperado e actualize o README do banco)", c.nome, q, QualificacaoNaoCumprida)
			}
			// O parametro de raciocinio do perfil chega ao fornecedor com o nome que o perfil lhe da.
			parametro := "thinking"
			if c.params == aos516ParamsEffort {
				parametro = "reasoning_effort"
			}
			if f.Parametros[parametro] != f.Pedidos {
				t.Errorf("%s: o parametro %s chegou em %d de %d pedidos", c.nome, parametro, f.Parametros[parametro], f.Pedidos)
			}
			if c.devolver == "obrigatorio" {
				// A LACUNA, presa: a rota provou-se igual e o gateway armou o estado; ele chegou ao
				// fornecedor, mas so dentro do saco do proxy.
				d := r.Taxas.Devolucao
				if d == nil || d.TurnosComRaciocinioCapturado == 0 || d.DecididosADevolver == 0 || len(d.NaoDevolvidoPorCausa) != 0 {
					t.Errorf("%s: o gateway tinha de capturar e armar o estado: %+v", c.nome, d)
				}
				if f.Estado["provider_specific_fields.reasoning_details"] == 0 || f.Estado["reasoning_details.intacto"] != 0 || f.Recusas[RecusaEstadoEmFalta] == 0 {
					t.Errorf("%s: a forma medida mudou: estado %v, recusas %v", c.nome, f.Estado, f.Recusas)
				}
			}
		}
		verSemFugas(t, c.nome, tudoOQueFoiEscrito(t, e, saida), append(append(proibidosDeTexto(t), SentinelasDoEstado()...), "sk-falso-", "sk-ensaio-", "Bearer "))
		medidas[c.nome] = map[string]any{
			"runs": r.Taxas.N, "desfechos": r.Taxas.Desfechos, "http": r.Taxas.HTTP, "tipos_de_erro": r.Taxas.TiposDeErro,
			"devolucao": r.Taxas.Devolucao, "qualificacao": r.Qualificacao, "forma_no_fornecedor": f,
		}
	}
	medidas["medicoes_directas"] = aos516MedicoesDirectas(t, binario)
	medidas["pass"] = !t.Failed()
	resumo, _ := json.Marshal(medidas)
	t.Logf("AOS_BANCO_OPENROUTER_REPORT %s", resumo)
}

// aos516MedicoesDirectas faz pedidos DIRECTAMENTE ao proxy (sem gateway nem runtime), pelas duas
// formas de rota, e devolve o que chegou ao falso em cada um — só nomes e códigos:
//
//   - o parâmetro `reasoning` da OpenRouter na sua forma nativa, que o perfil de uma rota hoje
//     não sabe exprimir;
//   - um segundo turno escrito à mão com os `reasoning_details` no TOPO da mensagem `assistant`
//     (com e sem `reasoning` e `reasoning_content` ao lado), e só dentro do saco do proxy: a
//     primeira é a forma que a devolução teria de ter para a OpenRouter a ler;
//   - os erros 400, 401, 402, 404 e 429 na forma da OpenRouter, e o tipo com que o banco os
//     classifica depois de o proxy os reembrulhar. Estes JULGAM-SE na rota escolhida.
func aos516MedicoesDirectas(t *testing.T, binario string) map[string]any {
	t.Helper()
	const tools = `"tools":[{"type":"function","function":{"name":"arquivo","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]`
	const chamada = `"tool_calls":[{"id":"toolu_Banco00","type":"function","function":{"name":"arquivo","arguments":"{\"path\":\"notas-armazem.txt\"}"}}]`
	segundoTurno := func(estado string) string {
		return `{"model":"` + AliasDaRota + `","max_tokens":4096,` + tools + `,"messages":[{"role":"user","content":"Read notas-armazem.txt"},` +
			`{"role":"assistant","content":"",` + estado + chamada + `},{"role":"tool","tool_call_id":"toolu_Banco00","content":"x"}]}`
	}
	simples := func(texto, extra string) string {
		return `{"model":"` + AliasDaRota + `","max_tokens":4096,"messages":[{"role":"user","content":"` + texto + `"}]` + extra + `}`
	}
	detalhes := `"reasoning_details":` + string(detalhesDoTurno(0)) + `,`
	texto, _ := json.Marshal(pensamentoDoTurno(0))
	pedidos := []struct{ nome, corpo string }{
		{"parametro_reasoning_max_tokens", simples(TextoDaSonda, `,"reasoning":{"max_tokens":2048}`)},
		{"parametro_reasoning_effort", simples(TextoDaSonda, `,"reasoning":{"effort":"medium"}`)},
		{"segundo_turno_detalhes_no_topo", segundoTurno(detalhes)},
		{"segundo_turno_detalhes_e_textos_no_topo", segundoTurno(detalhes + `"reasoning":` + string(texto) + `,"reasoning_content":` + string(texto) + `,`)},
		{"segundo_turno_detalhes_so_no_saco", segundoTurno(`"provider_specific_fields":{` + strings.TrimSuffix(detalhes, ",") + `},`)},
	}
	for _, codigo := range []string{"400", "401", "402", "404", "429"} {
		pedidos = append(pedidos, struct{ nome, corpo string }{"erro_" + codigo, simples(PrefixoDoPedidoDeErro+codigo, "")})
	}
	querTipo := map[string]string{"erro_401": TipoChaveRecusada, "erro_402": TipoSaldoInsuficiente, "erro_429": TipoLimiteDeRitmo, "erro_404": TipoModeloDesconhecido, "erro_400": TipoModeloDesconhecido}

	out := map[string]any{}
	for _, prefixo := range []string{"openrouter", "openai"} {
		ctx, cancelar := context.WithTimeout(context.Background(), 8*time.Minute)
		vivo, err := (&LancadorDocker{Redactor: &Redactor{}}).Lancar(ctx, PedidoDeProxy{
			Prefixo: prefixo, Modelo: aos516ModeloOpenRouter, BinarioDoFalso: binario, Estado: EstadoExige, TurnosDoFalso: 1,
			FormaDoFalso: FormaOpenRouter, Vida: 10 * time.Minute,
		}.ComSegredos("sk-falso-medicoes-directas", ""))
		if err != nil {
			cancelar()
			t.Fatalf("medicoes directas (%s): %v", prefixo, err)
		}
		doPrefixo := map[string]any{}
		antes := &FormaNoFornecedor{}
		for _, p := range pedidos {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, vivo.BaseURL+"/chat/completions", bytes.NewReader([]byte(p.corpo)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+vivo.ChaveMestra())
			medida := map[string]any{}
			if resp, rerr := http.DefaultClient.Do(req); rerr == nil {
				lido, _ := io.ReadAll(io.LimitReader(resp.Body, maxCorpoDeErro))
				_ = resp.Body.Close()
				medida["http"] = resp.StatusCode
				if servido := resp.Header.Get("x-litellm-model-name"); servido != "" {
					doPrefixo["modelo_servido_declarado"] = servido
				}
				if quer, eErro := querTipo[p.nome]; eErro {
					tipo := classificarErro(resp.StatusCode, lido)
					medida["tipo"] = tipo
					if prefixo == PrefixoDaOpenRouter && tipo != quer {
						t.Errorf("medicoes directas (%s): %s classificado %q, quer %q (HTTP %d)", prefixo, p.nome, tipo, quer, resp.StatusCode)
					}
				}
			}
			depois, ferr := LerFormaDoFalso(ctx, vivo.EnderecoDoFalso)
			if ferr != nil {
				vivo.Fechar()
				cancelar()
				t.Fatalf("medicoes directas (%s): %v", prefixo, ferr)
			}
			medida["pedidos_no_fornecedor"] = depois.Pedidos - antes.Pedidos
			medida["parametros"] = aos516Diferenca(antes.Parametros, depois.Parametros)
			medida["estado"] = aos516Diferenca(antes.Estado, depois.Estado)
			medida["assistant"] = aos516Diferenca(antes.Assistant, depois.Assistant)
			medida["recusas"] = aos516Diferenca(antes.Recusas, depois.Recusas)
			doPrefixo[p.nome], antes = medida, depois
		}
		vivo.Fechar()
		cancelar()
		if servido, _ := doPrefixo["modelo_servido_declarado"].(string); servido != prefixo+"/"+aos516ModeloOpenRouter {
			t.Errorf("medicoes directas (%s): o proxy declarou ter servido %q, e o perfil espera %s/%s", prefixo, servido, prefixo, aos516ModeloOpenRouter)
		}
		out[prefixo] = doPrefixo
	}
	return out
}

// aos516Diferenca devolve as contagens que cresceram de antes para depois.
func aos516Diferenca(antes, depois map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range depois {
		if v != antes[k] {
			out[k] = v - antes[k]
		}
	}
	return out
}
