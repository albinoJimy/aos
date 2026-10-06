package main

// AOS-503 — os cenários de processo da nova tentativa que NÃO são o caminho feliz: o modo de
// observação, cada desfecho que não admite tentativa, os tectos, as recusas do nó, a ordem do
// facto, a retoma por outro processo, o âmbito largo (nó com `consumes`), a entrega por
// referência sobre a tentativa que teve êxito, e o rollback pelo interruptor.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos503ComId devolve a resposta de fio de um run com o `run_id` trocado pelo dado: o resto dos
// bytes — o desfecho, o vector, a âncora, os bytes designados — é o que o nó real respondeu.
func aos503ComId(t *testing.T, cru []byte, de, para string) []byte {
	t.Helper()
	velho := []byte(`{"run_id":"` + de + `",`)
	if !bytes.HasPrefix(cru, velho) {
		t.Fatalf("pre-condicao: a resposta comeca pelo run_id %s; veio %s", de, cru[:min(len(cru), 80)])
	}
	return append([]byte(`{"run_id":"`+para+`",`), cru[len(velho):]...)
}

// aos503NaTentativa devolve a resposta de fio de uma tentativa com o numero da tentativa que o no
// declara (`plan_attempt.attempt`) trocado: e o que faz de uma resposta gravada para a tentativa
// `de` a resposta da tentativa `para`. Sem isto, uma retoma que confere a origem recusava-a — e
// bem: o run diria ser outra tentativa.
func aos503NaTentativa(t *testing.T, cru []byte, de, para int) []byte {
	t.Helper()
	velho := []byte(fmt.Sprintf(`"attempt":%d}}`, de))
	if !bytes.HasSuffix(cru, velho) {
		t.Fatalf("pre-condicao: a resposta de uma tentativa acaba com o plan_attempt que o no declara (%s); veio ...%s", velho, cru[max(0, len(cru)-80):])
	}
	return append(append([]byte(nil), cru[:len(cru)-len(velho)]...), []byte(fmt.Sprintf(`"attempt":%d}}`, para))...)
}

// aos503EsperarAPosse espera que a posse de um `serve` que ABORTOU expire. Um `serve` que sai com
// um erro verdadeiro não larga a posse (só a larga no fim do trabalho): o lease a expirar é a
// informação de que alguém caiu a meio, e o processo seguinte tem de esperar por ele. É o que
// acontece em produção entre duas drenagens; aqui espera-se o TTL.
func aos503EsperarAPosse() { time.Sleep(leaseTTL + time.Second) }

// aos503TemTentativa diz se algum `POST /runs` foi o de uma tentativa a mais.
func aos503TemTentativa(f *aos503No) bool {
	for _, id := range f.submetidos() {
		if strings.Count(strings.TrimPrefix(id, aos503Run+"~"), "~") > 0 {
			return true
		}
		for _, cru := range f.corpos(id) {
			if bytes.Contains(cru, []byte(`"attempt"`)) {
				return true
			}
		}
	}
	return false
}

func TestAOS503_CenariosComOBinarioReal(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	cat2 := aos503Catalogo(t, 2)
	falhada1, falhada2 := aos503FioDoNo(t, "tentativa-1-falhada"), aos503FioDoNo(t, "tentativa-2-falhada")
	recuperada2 := aos503NaTentativa(t, aos503ComId(t, aos503FioDoNo(t, "tentativa-3-recuperada"), aos503Leitor3, aos503Leitor2), 3, 2)

	// offDe corre o plano em `off` contra as mesmas respostas: é a referência de «como hoje».
	offDe := func(t *testing.T, catalogo []byte, respostas map[string][]byte) (aos499Drenagem, *aos503No, []aos499Evento) {
		t.Helper()
		f := &aos503No{catalogo: catalogo, respostas: respostas}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "", "30s")
		return d, f, aos499EventosDoPlano(t, s.wal, aos503Run)
	}
	// igualAOff exige os mesmos POST (ids e bytes), os mesmos eventos do plano, o mesmo código e
	// o mesmo detail — a menos dos campos que o cenário diz que o detail ganha.
	igualAOff := func(t *testing.T, d aos499Drenagem, f *aos503No, eventos []aos499Evento, catalogo []byte, respostas map[string][]byte, sufixoDoDetail string) {
		t.Helper()
		dOff, fOff, evOff := offDe(t, catalogo, respostas)
		if d.codigo != dOff.codigo || d.classe != dOff.classe {
			t.Fatalf("o desfecho tinha de ser o de off (%s/%d); saiu %s/%d\n%s", dOff.classe, dOff.codigo, d.classe, d.codigo, d.stdout)
		}
		if fmt.Sprint(f.submetidos()) != fmt.Sprint(fOff.submetidos()) {
			t.Fatalf("os POST /runs tinham de ser os de off:\n  off:   %v\n  agora: %v", fOff.submetidos(), f.submetidos())
		}
		for _, id := range fOff.submetidos() {
			if !bytes.Equal(f.corpos(id)[0], fOff.corpos(id)[0]) {
				t.Fatalf("o corpo de %s tinha de ser o de off:\n  off:   %s\n  agora: %s", id, fOff.corpos(id)[0], f.corpos(id)[0])
			}
		}
		if fmt.Sprint(eventos) != fmt.Sprint(evOff) {
			t.Fatalf("os eventos do plano tinham de ser os de off:\n  off:   %v\n  agora: %v", evOff, eventos)
		}
		if aos503SemVolatil(d.detalhe) != aos503SemVolatil(dOff.detalhe)+sufixoDoDetail {
			t.Fatalf("o detail tinha de ser o de off mais %q:\n  off:   %q\n  agora: %q", sufixoDoDetail, dOff.detalhe, d.detalhe)
		}
		if strings.Contains(dOff.metricas, "tentativa") || strings.Contains(dOff.stdout, "TENTATIVA") {
			t.Fatalf("pre-condicao: em off nada fala de tentativas:\n%s\n%s", dOff.metricas, dOff.stdout)
		}
	}

	// (1) OBSERVE: conta e diz o que tentaria; não tenta.
	t.Run("Observe_ContaENaoTenta", func(t *testing.T) {
		respostas := map[string][]byte{aos503Leitor1: falhada1}
		f := &aos503No{catalogo: cat2, respostas: respostas}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "observe", "30s")
		if d.codigo != exitNosFalhados || aos503TemTentativa(f) || len(aos503Factos(t, s.wal)) != 0 {
			t.Fatalf("em observe o no fecha failed e nao ha tentativa nem facto; saiu %s/%d, POST %v\n%s", d.classe, d.codigo, f.submetidos(), d.stdout)
		}
		igualAOff(t, d, f, aos499EventosDoPlano(t, s.wal, aos503Run), cat2, respostas, "")
		if !strings.Contains(d.stdout, "execucao: no read_notes NOVA TENTATIVA EM OBSERVACAO — tentaria outra vez (causa=contract_unmet_no_call, zero tool calls pedidas, com_consumes=false); nao tenta: o no fecha como em off\n") ||
			!strings.Contains(d.stdout, "nova tentativa (AOS-503, ADR-039): EM OBSERVACAO") {
			t.Fatalf("o log diz o que tentaria, com o no e a causa:\n%s", d.stdout)
		}
		for chave, valor := range map[string]int{
			serie(metricaTentativasEmObservacao, "com_consumes", "false"): 1,
			serie(metricaPrimeirasFalhas, "com_consumes", "false"):        1,
		} {
			if !temSerie(d.metricas, chave, valor) {
				t.Fatalf("faltou a serie %s %d:\n%s", chave, valor, d.metricas)
			}
		}
		for _, proibida := range []string{metricaTentativas + "{", metricaTentativasPorPlano, metricaPlanosRecuperados, metricaTentativasRecusadas} {
			if strings.Contains(d.metricas, proibida) {
				t.Fatalf("em observe nao se tenta: a serie %s nao existe:\n%s", proibida, d.metricas)
			}
		}
	})

	// (2) O QUE NÃO ADMITE NOVA TENTATIVA. Um caso por desfecho; em todos, em `on` e contra um nó
	// que anuncia, o `aos-orq` não pede tentativa nenhuma e porta-se como em `off`.
	naoConcluiu := []byte(`{"run_id":"` + aos503Leitor1 + `","status":"completed","turns":8}`)
	for nome, resposta := range map[string][]byte{
		"depois-de-uma-tool-call-negada":            aos503Falhado(aos503Leitor1, string(agentruntime.OutcomeContractAfterDenial), 1),
		"depois-de-uma-tool-call-falhada":           aos503Falhado(aos503Leitor1, string(agentruntime.OutcomeContractAfterToolError), 1),
		"resposta-truncada":                         aos503Falhado(aos503Leitor1, string(agentruntime.OutcomeTruncated), 0),
		"texto-vazio":                               aos503Falhado(aos503Leitor1, string(agentruntime.OutcomeEmptyOutput), 0),
		"no-call-COM-uma-tool-call-pedida":          aos503Falhado(aos503Leitor1, string(agentruntime.OutcomeContractNoCall), 1),
		"failed-sem-razao":                          aos503Falhado(aos503Leitor1, "", 0),
		"razao-desconhecida":                        aos503Falhado(aos503Leitor1, "uma_razao_que_este_binario_nao_conhece", 0),
		"timed-out":                                 []byte(`{"run_id":"` + aos503Leitor1 + `","status":"timed_out","turns":1}`),
		"killed":                                    []byte(`{"run_id":"` + aos503Leitor1 + `","status":"killed"}`),
		"run-que-nao-concluiu":                      naoConcluiu,
		"no-call-na-resposta-de-OUTRO-run":          aos503ComId(t, falhada1, aos503Leitor1, aos503Run+"~outro"),
		"no-call-observado-num-run-que-concluiu":    []byte(`{"run_id":"` + aos503Leitor1 + `","status":"completed","terminated":true,"final_text":"um texto","turns":1,"outcome_reason":"contract_unmet_no_call","verdict":{"mode":"observe","fulfilled":false,"reason":"contract_unmet_no_call","tool_calls_requested":0}}`),
		"saida-vazia-de-um-run-que-concluiu":        []byte(`{"run_id":"` + aos503Leitor1 + `","status":"completed","terminated":true,"final_text":"","turns":2}`),
		"saida-indisponivel-de-um-run-que-concluiu": []byte(`{"run_id":"` + aos503Leitor1 + `","status":"completed","terminated":true,"output_unavailable":true,"turns":2}`),
	} {
		t.Run("On_NaoTenta/"+nome, func(t *testing.T) {
			respostas := map[string][]byte{aos503Leitor1: resposta}
			f := &aos503No{catalogo: cat2, respostas: respostas}
			s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
			d := s.drenar(t, 1, "on", "30s")
			if aos503TemTentativa(f) || len(aos503Factos(t, s.wal)) != 0 {
				t.Fatalf("NAO pode haver nova tentativa; POST %v\n%s", f.submetidos(), d.stdout)
			}
			igualAOff(t, d, f, aos499EventosDoPlano(t, s.wal, aos503Run), cat2, respostas, "")
			if strings.Contains(d.stdout, "NOVA TENTATIVA") || strings.Contains(d.metricas, metricaPrimeirasFalhas) || strings.Contains(d.metricas, metricaTentativas+"{") {
				t.Fatalf("um desfecho que nao e elegivel nao conta como primeira falha nem como tentativa:\n%s\n%s", d.stdout, d.metricas)
			}
		})
	}

	// (2-bis) O 503 DO NÓ não é desfecho nenhum: volta-se a ler, e não se tenta.
	t.Run("On_NaoTenta/503-do-no", func(t *testing.T) {
		f := &aos503No{catalogo: cat2, ilegivelAntes: map[string]int{aos503Leitor1: 3}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "on", "30s")
		if d.codigo != exitOK || aos503TemTentativa(f) || len(aos503Factos(t, s.wal)) != 0 {
			t.Fatalf("depois de tres 503 o plano conclui, sem tentativa nenhuma; saiu %s/%d POST %v", d.classe, d.codigo, f.submetidos())
		}
	})

	// (2-ter) UM NÓ SEM TOOLS: a mesma razão, se o nó a devolvesse, não dá tentativa — não há
	// sinal estrutural de que repetir ajude, e não se julga o texto.
	t.Run("On_NaoTenta/no-sem-tools", func(t *testing.T) {
		respostas := map[string][]byte{aos503Resumo: aos503ComId(t, falhada1, aos503Leitor1, aos503Resumo)}
		f := &aos503No{catalogo: cat2, respostas: respostas}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "on", "30s")
		if d.codigo != exitNosFalhados || aos503TemTentativa(f) || len(aos503Factos(t, s.wal)) != 0 {
			t.Fatalf("o no de resumo (sem tools) fecha failed sem tentativa; saiu %s/%d POST %v\n%s", d.classe, d.codigo, f.submetidos(), d.stdout)
		}
		igualAOff(t, d, f, aos499EventosDoPlano(t, s.wal, aos503Run), cat2, respostas, "")
	})

	// (3) O NÓ NÃO ANUNCIA (um nó anterior ao AOS-502, ou com o tecto a zero): nenhuma tentativa,
	// nenhum campo novo no fio, e os desfechos de hoje. A causa conta como `nao_anunciado`.
	t.Run("On_NoQueNaoAnuncia_ComoHoje", func(t *testing.T) {
		respostas := map[string][]byte{aos503Leitor1: falhada1}
		f := &aos503No{catalogo: p.catalogo, respostas: respostas}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "on", "30s")
		if aos503TemTentativa(f) || len(aos503Factos(t, s.wal)) != 0 {
			t.Fatalf("contra um no que nao anuncia nao ha tentativa nem campo attempt; POST %v", f.submetidos())
		}
		igualAOff(t, d, f, aos499EventosDoPlano(t, s.wal, aos503Run), p.catalogo, respostas, " tentativa_recusada=nao_anunciado")
		if !temSerie(d.metricas, serie(metricaTentativasRecusadas, "causa", recusaNaoAnunciado), 1) ||
			!strings.Contains(d.stdout, "nova tentativa (AOS-503, ADR-039): LIGADA E NAO APLICADA") ||
			!strings.Contains(d.stdout, "causa=contract_unmet_no_call vector [doc_read pedidas=0 efectivas=0 negadas=0 falhadas=0] tentativa_recusada=nao_anunciado\n") {
			t.Fatalf("a causa conta como nao_anunciado e o banner di-lo:\n%s\n%s", d.stdout, d.metricas)
		}
	})

	// (3-bis) SEM A REFERÊNCIA AO PEDIDO (um nó anterior ao AOS-477) os runs filhos não declaram o
	// plano nem o nó, e sem isso o nó `aos` não tem de que derivar o id da tentativa: não se tenta,
	// não se grava facto e não se conta nada — mesmo com o nó a anunciar.
	t.Run("On_SemAReferenciaAoPedido_NaoTenta", func(t *testing.T) {
		f := &aos503No{catalogo: cat2, respostas: map[string][]byte{aos503Leitor1: falhada1}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		s.semReferencia = true
		d := s.drenar(t, 1, "on", "30s")
		if d.codigo != exitNosFalhados || aos503TemTentativa(f) || len(aos503Factos(t, s.wal)) != 0 {
			t.Fatalf("sem a referencia ao pedido nao ha tentativa; saiu %s/%d POST %v\n%s", d.classe, d.codigo, f.submetidos(), d.stdout)
		}
		if bytes.Contains(f.corpos(aos503Leitor1)[0], []byte(`"node_id"`)) {
			t.Fatalf("pre-condicao: o run filho nao declara o no: %s", f.corpos(aos503Leitor1)[0])
		}
		if strings.Contains(d.detalhe, "tentativa") || strings.Contains(d.metricas, metricaPrimeirasFalhas) || strings.Contains(d.stdout, "NOVA TENTATIVA") {
			t.Fatalf("nada se conta nem se diz sobre uma tentativa que nao pode existir: %q\n%s", d.detalhe, d.metricas)
		}
	})

	// (4) OS TECTOS. O do nó (anuncia 1) e o do plano (variável) vencem o do `aos-orq`.
	for _, c := range []struct {
		nome     string
		catalogo []byte
		env      []string
		runs     []string
		causa    string
		factos   int
	}{
		{"TectoDoNoA1", aos503Catalogo(t, 1), nil, []string{aos503Leitor1, aos503Leitor2}, recusaTectoDoNo, 1},
		{"TectoDoPlanoA1", cat2, []string{"AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO=1"}, []string{aos503Leitor1, aos503Leitor2}, recusaTectoDoPlano, 1},
		{"TectoDoPlanoA0", cat2, []string{"AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO=0"}, []string{aos503Leitor1}, recusaTectoDoPlano, 0},
	} {
		t.Run("On_"+c.nome, func(t *testing.T) {
			f := &aos503No{catalogo: c.catalogo, respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: falhada2}}
			s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
			d := s.drenar(t, 1, "on", "30s", c.env...)
			if d.codigo != exitNosFalhados || fmt.Sprint(f.submetidos()) != fmt.Sprint(c.runs) || len(aos503Factos(t, s.wal)) != c.factos {
				t.Fatalf("o tecto corta as tentativas: queria os runs %v e %d facto(s); saiu %s/%d com %v\n%s", c.runs, c.factos, d.classe, d.codigo, f.submetidos(), d.stdout)
			}
			if !strings.Contains(d.detalhe, " causa=contract_unmet_no_call:1,entrada_por_cumprir:1") || !strings.HasSuffix(d.detalhe, " tentativa_recusada="+c.causa) ||
				!temSerie(d.metricas, serie(metricaTentativasRecusadas, "causa", c.causa), 1) || strings.Contains(d.detalhe, "esgotadas") {
				t.Fatalf("o no fecha com a causa do run e a tentativa recusada por %s: %q\n%s", c.causa, d.detalhe, d.metricas)
			}
		})
	}

	// (5) O NÓ RECUSA A SUBMISSÃO DA TENTATIVA. 429 (a quota de quem pediu): o nó do plano fecha
	// `failed` com a causa original e o plano sai 13 — nunca 8, que reabria o pedido na fila. 403:
	// a prova do nó não passou. Em nenhum dos dois se insiste.
	for estado, causa := range map[int]string{http.StatusTooManyRequests: recusaQuota, http.StatusForbidden: recusaPeloNo} {
		t.Run(fmt.Sprintf("On_ONoRecusaATentativa_%d", estado), func(t *testing.T) {
			f := &aos503No{catalogo: cat2, respostas: map[string][]byte{aos503Leitor1: falhada1},
				estadoDoPost: func(id string, _ int) int {
					if id == aos503Leitor2 {
						return estado
					}
					return 0
				}}
			s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
			d := s.drenar(t, 1, "on", "30s")
			if d.classe != "terminal" || d.codigo != exitNosFalhados {
				t.Fatalf("uma tentativa recusada fecha o no com a causa original e o plano sai terminal/13; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			if quer := []string{aos503Leitor1, aos503Leitor2}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
				t.Fatalf("UM pedido da tentativa, e nao se insiste nem corre o consumidor: %v", f.submetidos())
			}
			if !strings.Contains(d.detalhe, " causa=contract_unmet_no_call:1,entrada_por_cumprir:1 tentativas=1 recuperados=0 tentativa_recusada="+causa) ||
				!temSerie(d.metricas, serie(metricaTentativasRecusadas, "causa", causa), 1) {
				t.Fatalf("o detail e a metrica dizem a causa original e o motivo %s: %q\n%s", causa, d.detalhe, d.metricas)
			}
			if !strings.Contains(d.stdout, "(run "+aos503Leitor1+") causa=contract_unmet_no_call") {
				t.Fatalf("o no fecha sobre o run que falhou, e nao sobre a tentativa que o no recusou:\n%s", d.stdout)
			}
		})
	}

	// (6) O FACTO FICA NO LOG ANTES DO PEDIDO AO NÓ, E A RETOMA CONTINUA DELE. O `POST /runs` da
	// tentativa falha (500) e o `serve` aborta: o facto da tentativa 2 JÁ ESTÁ no log, sem run. O
	// processo seguinte lê o estado do id da tentativa, vê 404 e submete-a — sem gravar outro
	// facto e sem saltar para a 3.
	t.Run("On_OFactoAntesDoPedido_ERetomaEntreOFactoEASubmissao", func(t *testing.T) {
		var falhar atomic.Bool
		falhar.Store(true)
		f := &aos503No{catalogo: cat2, respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: recuperada2},
			estadoDoPost: func(id string, _ int) int {
				if id == aos503Leitor2 && falhar.Load() {
					return http.StatusInternalServerError
				}
				return 0
			}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d1 := s.drenar(t, 1, "on", "30s")
		if d1.codigo == exitOK || d1.codigo == exitNosFalhados || d1.classe == "terminal" {
			t.Fatalf("com o POST da tentativa a dar 500 o serve aborta sem fechar o no; saiu %s/%d %q\n%s", d1.classe, d1.codigo, d1.detalhe, d1.stdout)
		}
		factos := aos503Factos(t, s.wal)
		if len(factos) != 1 || factos[0].Attempt != 2 || factos[0].RetryOf != aos503Leitor1 || f.aceite(aos503Leitor2) {
			t.Fatalf("o facto da tentativa 2 tinha de estar no log ANTES de o no aceitar o run: factos=%+v aceite=%v\n%s", factos, f.aceite(aos503Leitor2), d1.stdout)
		}
		falhar.Store(false)
		aos503EsperarAPosse()
		d2 := s.drenar(t, 2, "on", "30s")
		if d2.classe != "terminal" || d2.codigo != exitOK {
			t.Fatalf("a retoma submete a tentativa que o log regista e o plano conclui; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Leitor2, aos503Leitor2, aos503Resumo}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("a retoma submete a MESMA tentativa (a 2), uma vez, e nunca a 3:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
		}
		if depois := aos503Factos(t, s.wal); len(depois) != 1 {
			t.Fatalf("a retoma nao grava outro facto nem gasta outra tentativa: %+v", depois)
		}
		if !strings.Contains(d2.stdout, "execucao: no read_notes RETOMA da tentativa 2") || !strings.HasSuffix(d2.detalhe, " tentativas=1 recuperados=1") {
			t.Fatalf("o log e o detail da retoma:\n%s\n%q", d2.stdout, d2.detalhe)
		}
		// O corpo que a retoma envia é o da tentativa 2, na geração NOVA da reclamação.
		var corpo struct {
			PlanRequest vinculoAoPedido `json:"plan_request"`
		}
		if err := json.Unmarshal(f.corpos(aos503Leitor2)[1], &corpo); err != nil || corpo.PlanRequest.Attempt != 2 || corpo.PlanRequest.Geracao != 2 {
			t.Fatalf("a tentativa retomada leva attempt=2 e a geracao viva: %+v (%v)", corpo.PlanRequest, err)
		}
	})

	// (7) RETOMA ENTRE A SUBMISSÃO E A RECOLHA. A tentativa 2 foi aceite e ainda corre quando o
	// primeiro `serve` sai (8). O seguinte lê o estado do id da tentativa, vê que existe e segue-o:
	// não a volta a submeter.
	t.Run("On_RetomaEntreASubmissaoEARecolha", func(t *testing.T) {
		aCorrer := []byte(`{"run_id":"` + aos503Leitor2 + `","status":"in_progress"}`)
		f := &aos503No{catalogo: cat2, respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: aCorrer}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d1 := s.drenar(t, 1, "on", "1s")
		if d1.codigo != exitNosEmVoo {
			t.Fatalf("a primeira drenagem sai com a tentativa em voo (8); saiu %s/%d %q\n%s", d1.classe, d1.codigo, d1.detalhe, d1.stdout)
		}
		f.responder(aos503Leitor2, recuperada2)
		d2 := s.drenar(t, 2, "on", "30s")
		if d2.classe != "terminal" || d2.codigo != exitOK {
			t.Fatalf("a retoma recolhe a tentativa e conclui; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Leitor2, aos503Resumo}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("a tentativa foi submetida UMA vez; a retoma segue o run que existe:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
		}
		if len(aos503Factos(t, s.wal)) != 1 || strings.Contains(d2.stdout, "RETOMA da tentativa") {
			t.Fatalf("um facto, e nenhuma re-submissao:\n%s", d2.stdout)
		}
	})

	// (8) ROLLBACK PELO INTERRUPTOR. Uma tentativa ficou em voo; o operador põe o `aos-orq` em
	// `off`. O processo seguinte continua a SEGUIR a tentativa que o log regista (não a perde de
	// vista, nem volta ao run que falhou) e não começa mais nenhuma.
	t.Run("Off_ComUmaTentativaNoLog_SegueANaoComecaOutra", func(t *testing.T) {
		aCorrer := []byte(`{"run_id":"` + aos503Leitor2 + `","status":"in_progress"}`)
		f := &aos503No{catalogo: cat2, respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: aCorrer}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		if d1 := s.drenar(t, 1, "on", "1s"); d1.codigo != exitNosEmVoo {
			t.Fatalf("pre-condicao: a tentativa fica em voo (8); saiu %d", d1.codigo)
		}
		f.responder(aos503Leitor2, falhada2)
		d2 := s.drenar(t, 2, "", "30s")
		if d2.classe != "terminal" || d2.codigo != exitNosFalhados {
			t.Fatalf("em off a tentativa que voltou a falhar fecha o no; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Leitor2}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) || len(aos503Factos(t, s.wal)) != 1 {
			t.Fatalf("em off nao comeca a tentativa 3: %v", f.submetidos())
		}
		if !strings.Contains(d2.stdout, "(run "+aos503Leitor2+") causa=contract_unmet_no_call") {
			t.Fatalf("o no fecha sobre o run da tentativa que o log regista:\n%s", d2.stdout)
		}
		// Revisão adversarial, M3: a tentativa que voltou a falhar do mesmo modo conta como
		// recorrência também em off — e não como `outra_causa`.
		if !temSerie(d2.metricas, serie(metricaTentativas, "tentativa", "2", "desfecho", tentativaVoltouAFalhar, "com_consumes", "false"), 1) ||
			strings.Contains(d2.metricas, `desfecho="`+tentativaOutraCausa+`"`) {
			t.Fatalf("em off a tentativa 2 que voltou a falhar sem chamar a tool conta voltou_a_falhar:\n%s", d2.metricas)
		}
	})
}

// TestAOS503_AmbitoLargoComOBinarioReal é a decisão 3 do dono: um nó com tools E com `consumes`
// cujo modelo nunca chama a tool. Gasta EXACTAMENTE o tecto, a tentativa leva os mesmos `inputs`,
// o nó fecha `failed`, o plano sai 13, e as métricas separam esta classe.
func TestAOS503_AmbitoLargoComOBinarioReal(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	// O nó de resumo passa a ter a tool de leitura: é um nó com tools e com `consumes`.
	const semTools = `"tools":[],`
	if strings.Count(p.plano, semTools) != 1 {
		t.Fatalf("pre-condicao: o plano de producao tem um no sem tools:\n%s", p.plano)
	}
	var doc plan.PlanDocument
	if err := json.Unmarshal([]byte(p.plano), &doc); err != nil || len(doc.Nodes) != 2 || len(doc.Nodes[0].Tools) != 1 {
		t.Fatalf("pre-condicao: o plano tem dois nos e o leitor tem uma tool (%v)", err)
	}
	pin, err := json.Marshal(doc.Nodes[0].Tools)
	if err != nil {
		t.Fatal(err)
	}
	plano := strings.Replace(p.plano, semTools, `"tools":`+string(pin)+`,`, 1)

	respostas := map[string][]byte{}
	for _, id := range []string{aos503Resumo, aos503ResumoT2, aos503Run + "~summarize~3"} {
		respostas[id] = aos503Falhado(id, string(agentruntime.OutcomeContractNoCall), 0)
	}
	f := &aos503No{catalogo: aos503Catalogo(t, 2), respostas: respostas}
	s := aos503Abrir(t, bin, f, plano, p.snapshot)
	d := s.drenar(t, 1, "on", "30s")
	if d.classe != "terminal" || d.codigo != exitNosFalhados {
		t.Fatalf("o no com consumes que nunca chama a tool fecha failed e o plano sai 13; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
	}
	if quer := []string{aos503Leitor1, aos503Resumo, aos503ResumoT2, aos503Run + "~summarize~3"}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
		t.Fatalf("EXACTAMENTE o tecto: tres runs do consumidor, e nem mais um:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
	}
	// A TENTATIVA LEVA OS MESMOS INPUTS: o conteúdo e os digests são iguais byte a byte.
	primeira := f.corpos(aos503Resumo)[0]
	var corpo struct {
		Inputs []entradaDoNo `json:"inputs"`
	}
	if err := json.Unmarshal(primeira, &corpo); err != nil || len(corpo.Inputs) != 1 || corpo.Inputs[0].Content != "feito: "+aos503Leitor1 || corpo.Inputs[0].From != "read_notes" {
		t.Fatalf("pre-condicao: o consumidor leva a saida do leitor nos inputs: %s (%v)", primeira, err)
	}
	aos503SoDifereNoIdENaTentativa(t, primeira, f.corpos(aos503ResumoT2)[0], aos503ResumoT2, 2)
	aos503SoDifereNoIdENaTentativa(t, primeira, f.corpos(aos503Run + "~summarize~3")[0], aos503Run+"~summarize~3", 3)
	if !strings.HasSuffix(d.detalhe, " causa=contract_unmet_no_call:1 tentativas=2 recuperados=0 tentativas_esgotadas=1") {
		t.Fatalf("o detail: a causa do run e as tentativas esgotadas: %q", d.detalhe)
	}
	for chave, valor := range map[string]int{
		serie(metricaPrimeirasFalhas, "com_consumes", "true"):                                                  1,
		serie(metricaTentativas, "tentativa", "2", "desfecho", tentativaVoltouAFalhar, "com_consumes", "true"): 1,
		serie(metricaTentativas, "tentativa", "3", "desfecho", tentativaVoltouAFalhar, "com_consumes", "true"): 1,
		metricaPlanosEsgotados: 1,
	} {
		if !temSerie(d.metricas, chave, valor) {
			t.Fatalf("faltou a serie %s %d:\n%s", chave, valor, d.metricas)
		}
	}
	if strings.Contains(d.metricas, `com_consumes="false"`) {
		t.Fatalf("a classe com consumes nao se mistura com a outra:\n%s", d.metricas)
	}
	// SEM CONTEÚDO: o material untrusted que o consumidor recebeu não aparece no log, nas métricas
	// nem no detail.
	for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": aos501MetricasSemRelogio(d.metricas), "detalhe": d.detalhe} {
		if levaProibido(texto, corpo.Inputs[0].Content) {
			t.Fatalf("o conteudo entregue ao consumidor aparece em %s", onde)
		}
	}
}

// TestAOS503_EntregaPorReferenciaComOBinarioReal: com a entrega por referência ligada (AOS-501),
// a primeira tentativa do nó que declara a origem não chama a tool e a segunda chama. A âncora, o
// facto e os bytes entregues são os da tentativa que teve êxito — a resposta é a do NÓ REAL com a
// TOOL REAL DA SANDBOX (o ficheiro de fio do AOS-501), com o `run_id` da tentativa.
func TestAOS503_EntregaPorReferenciaComOBinarioReal(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	comOrigem := aos501Plano(t, p)
	falhada1 := aos503FioDoNo(t, "tentativa-1-falhada")
	daSandbox := aos503ComId(t, aos501FioDoNo(t, "sandbox-binding-resumo"), aos501Run+"~read_notes", aos503Leitor2)
	var st estadoDoRun
	if err := json.Unmarshal(daSandbox, &st); err != nil || !st.concluiu() || st.Output == nil || st.OutputSource == nil || st.RunID != aos503Leitor2 {
		t.Fatalf("pre-condicao: a resposta da tentativa 2 traz a ancora e os bytes da sandbox (%v)", err)
	}
	documento, forma := desembrulharEnvelope(*st.Output)
	if forma != formaEnvelope || !strings.Contains(documento, "1250") || strings.Contains(st.FinalText, "1250") {
		t.Fatalf("pre-condicao: os bytes designados sao o envelope da sandbox e o resumo do modelo perde o numero (forma=%s)", forma)
	}
	entregaLigada := "AOS_ORQ_SAIDA_POR_REFERENCIA=on"

	// (1) Num só processo: falha, e a segunda tentativa entrega por referência.
	t.Run("AAncoraEOsBytesSaoOsDaTentativaQueTeveExito", func(t *testing.T) {
		f := &aos503No{catalogo: aos503Catalogo(t, 2), respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: daSandbox}}
		s := aos503Abrir(t, bin, f, comOrigem, p.snapshot)
		d := s.drenar(t, 1, "on", "30s", entregaLigada)
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("o plano recupera e entrega por referencia; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Leitor2, aos503Resumo}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("o consumidor so corre depois da tentativa bem-sucedida:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
		}
		// A tentativa leva a MESMA declaração de origem e o mesmo vínculo.
		aos503SoDifereNoIdENaTentativa(t, f.corpos(aos503Leitor1)[0], f.corpos(aos503Leitor2)[0], aos503Leitor2, 2)
		var tentativa map[string]any
		if err := json.Unmarshal(f.corpos(aos503Leitor2)[0], &tentativa); err != nil {
			t.Fatal(err)
		}
		if tool, vinculo, presente := origemEnviada(tentativa); !presente || tool != "doc_read" || vinculo != "binding" {
			t.Fatalf("a tentativa declara a origem com o vinculo binding: %q/%q (%v)", tool, vinculo, presente)
		}
		// O QUE O CONSUMIDOR RECEBE: o documento que a tool devolveu NA SEGUNDA tentativa.
		var resumo struct {
			Inputs []entradaDoNo `json:"inputs"`
		}
		if err := json.Unmarshal(f.corpos(aos503Resumo)[0], &resumo); err != nil || len(resumo.Inputs) != 1 {
			t.Fatalf("o consumidor leva uma entrada: %v", err)
		}
		if resumo.Inputs[0].Content != documento || resumo.Inputs[0].Digest != digestDoConteudo(documento) {
			t.Fatalf("o consumidor recebe o stdout_text selado no run da SEGUNDA tentativa:\n  quero: %q\n  veio:  %q", documento, resumo.Inputs[0].Content)
		}
		// O LOG: UM facto da declaração, um facto da tentativa, e a publicação aponta para o run
		// da tentativa 2, com a âncora dele.
		eventos := aos499EventosDoPlano(t, s.wal, aos503Run)
		if n := len(aos501Eventos(eventos, plannerevents.EventOutputSourceDeclared)); n != 1 {
			t.Fatalf("o facto da declaracao de origem e um por no, tambem com tentativas: %d", n)
		}
		if factos := aos503Factos(t, s.wal); len(factos) != 1 || factos[0].Attempt != 2 {
			t.Fatalf("um facto de tentativa: %+v", factos)
		}
		publicados := aos501Eventos(eventos, plannerevents.EventPayloadPublished)
		var pub plannerevents.PayloadPublishedPayload
		if len(publicados) != 1 || json.Unmarshal([]byte(publicados[0].Payload), &pub) != nil {
			t.Fatalf("uma publicacao: %+v", publicados)
		}
		if pub.Record.Stream != aos503Leitor2 || pub.Source == nil || pub.Source.StepID != st.OutputSource.StepID ||
			pub.Source.AnchorDigest != st.OutputSource.Digest || pub.Source.AnchorBytes != st.OutputSource.Bytes || pub.Record.Digest != digestDoConteudo(documento) {
			t.Fatalf("a publicacao aponta para o run e a ancora da tentativa que concluiu: %+v source=%+v", pub.Record, pub.Source)
		}
		if !temSerie(d.metricas, serie(metricaEntregaPorReferencia, "resultado", resultadoEntregue), 1) || strings.Contains(d.metricas, `resultado="origem_`) {
			t.Fatalf("a entrega conta uma vez, como entregue:\n%s", d.metricas)
		}
		// O TEXTO DO MODELO — o resumo da tentativa 2 — não se publica nem se entrega.
		for _, id := range f.submetidos() {
			for _, cru := range f.corpos(id) {
				if levaProibido(string(cru), strings.TrimSpace(st.FinalText)) {
					t.Fatalf("o texto final do produtor aparece no POST /runs de %s", id)
				}
			}
		}
		for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": aos501MetricasSemRelogio(d.metricas), "detalhe": d.detalhe, "eventos": fmt.Sprint(eventos)} {
			if levaProibido(texto, strings.TrimSpace(st.FinalText)) {
				t.Fatalf("o texto final do produtor aparece em %s", onde)
			}
		}
	})

	// (2) A REIDRATAÇÃO lê a tentativa que concluiu. O primeiro `serve` recupera o produtor, publica
	// por referência e morre ao submeter o consumidor (500). O seguinte reconstrói o payload DO
	// RUN DA TENTATIVA 2 — o que o log regista como corrente — e entrega-o.
	t.Run("AReidratacaoLeATentativaQueConcluiu", func(t *testing.T) {
		var falhar atomic.Bool
		falhar.Store(true)
		f := &aos503No{catalogo: aos503Catalogo(t, 2), respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: daSandbox},
			estadoDoPost: func(id string, _ int) int {
				if id == aos503Resumo && falhar.Load() {
					return http.StatusInternalServerError
				}
				return 0
			}}
		s := aos503Abrir(t, bin, f, comOrigem, p.snapshot)
		d1 := s.drenar(t, 1, "on", "30s", entregaLigada)
		if d1.codigo == exitOK || d1.codigo == exitNosFalhados {
			t.Fatalf("pre-condicao: o primeiro serve aborta ao submeter o consumidor; saiu %s/%d\n%s", d1.classe, d1.codigo, d1.stdout)
		}
		if n := len(aos501Eventos(aos499EventosDoPlano(t, s.wal, aos503Run), plannerevents.EventPayloadPublished)); n != 1 {
			t.Fatalf("pre-condicao: o produtor publicou por referencia antes de o serve abortar (%d)", n)
		}
		falhar.Store(false)
		aos503EsperarAPosse()
		d2 := s.drenar(t, 2, "on", "30s", entregaLigada)
		if d2.classe != "terminal" || d2.codigo != exitOK {
			t.Fatalf("a retoma reidrata do run da tentativa 2 e conclui; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		if !strings.Contains(d2.stdout, "1 de 1 payload(s) reconstruido(s) do log") {
			t.Fatalf("o payload por referencia foi reconstruido do run da tentativa que concluiu:\n%s", d2.stdout)
		}
		corpos := f.corpos(aos503Resumo)
		var resumo struct {
			Inputs []entradaDoNo `json:"inputs"`
		}
		if err := json.Unmarshal(corpos[len(corpos)-1], &resumo); err != nil || len(resumo.Inputs) != 1 || resumo.Inputs[0].Content != documento {
			t.Fatalf("depois da retoma o consumidor recebe o documento da tentativa 2: %v", err)
		}
		// O estado do run da PRIMEIRA tentativa não foi relido para reidratar: quem serve o payload
		// é o run da tentativa 2.
		if f.lido(aos503Leitor2) == 0 {
			t.Fatal("a reidratacao tinha de ler o run da tentativa 2")
		}
	})
}
