package main

// AOS-511 — O `aos-orq` VOLTA A SUBMETER UM NÓ DO PLANO QUE FECHOU `empty_output` (ADR-039).
//
// Os cenários correm o BINÁRIO REAL (`consume`) contra um nó `aos` falso que responde, sempre que
// há ficheiro, o que o NÓ REAL respondeu (`../aos/testdata/aos510_fio`). A forma é a de produção:
// `read_notes` (com a tool `doc_read`) e `summarize` (sem tools, com `consumes`) — o nó que, em
// 3 de 140 planos, respondeu vazio.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// aos511FioDoNo lê um ficheiro gerado pelo teste do NÓ (AOS-510).
func aos511FioDoNo(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "aos", "testdata", "aos510_fio", nome+".json"))
	if err != nil || len(bytes.TrimSpace(cru)) == 0 {
		if aos503Actualizar() {
			t.Skipf("o ficheiro %s ainda nao foi gerado (%v): corre primeiro o teste do no com AOS494_ACTUALIZAR_FIO=1, e este outra vez", nome, err)
		}
		t.Fatalf("ficheiro do fio %s em falta: err=%v bytes=%d — gera-o no no com AOS494_ACTUALIZAR_FIO=1", nome, err, len(cru))
	}
	return bytes.TrimSpace(cru)
}

// aos511Fio compara (ou, a regenerar, escreve) um ficheiro que o teste do NÓ consome.
func aos511Fio(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos511_fio", nome+".json")
	cru = bytes.TrimSpace(cru)
	if aos503Actualizar() {
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatalf("criar a pasta do fio: %v", err)
		}
		if err := os.WriteFile(caminho, append(cru, '\n'), 0o644); err != nil {
			t.Fatalf("escrever %s: %v", caminho, err)
		}
		return
	}
	quer, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o com AOS494_ACTUALIZAR_FIO=1", err)
	}
	if !bytes.Equal(bytes.TrimSpace(quer), cru) {
		t.Fatalf("o aos-orq deixou de enviar o que o no aceita nos testes dele (%s):\n  fio:   %s\n  agora: %s", caminho, bytes.TrimSpace(quer), cru)
	}
}

// aos503ResumoT3 é o id da terceira tentativa do nó de resumo.
var aos503ResumoT3 = aos503Run + "~summarize~3"

// aos511MarcadorDoRunFalhado é um texto que só existe na resposta montada à mão de um run que
// respondeu vazio — o nó real nem o devolve. Não pode aparecer em lado nenhum.
const aos511MarcadorDoRunFalhado = "MARCADOR-511-RACIOCINIO-DO-RUN-FALHADO"

// aos511RespostaVazia monta, à mão, a resposta de um run `failed` por `empty_output` com o total
// de tool calls pedidas dado, COM um texto final hostil (que o nó real não devolve): a decisão
// não o lê, e ele não sai do processo.
func aos511RespostaVazia(id string, pedidas int) []byte {
	return []byte(fmt.Sprintf(`{"run_id":%q,"status":"failed","turns":1,"final_text":%q,"outcome_reason":"empty_output","verdict":{"mode":"enforce","fulfilled":false,"reason":"empty_output","tool_calls_requested":%d}}`,
		id, aos511MarcadorDoRunFalhado, pedidas))
}

// aos511Concluido monta a resposta de um run de tentativa do nó de resumo que concluiu, com a
// origem que o nó `aos` declara.
func aos511Concluido(id string, tentativa int) []byte {
	return []byte(fmt.Sprintf(`{"run_id":%q,"status":"completed","terminated":true,"final_text":"Resumo: tres notas.","turns":1,"plan_attempt":{"plan_request":%q,"generation":1,"plan_id":%q,"node_id":"summarize","attempt":%d}}`,
		id, aos503Run, aos503Run+"-plan", tentativa))
}

const (
	aos511On      = "AOS_ORQ_NOVA_TENTATIVA_VAZIA=on"
	aos511Observe = "AOS_ORQ_NOVA_TENTATIVA_VAZIA=observe"
	aos511Off     = "AOS_ORQ_NOVA_TENTATIVA_VAZIA=off"
)

// aos511SemSeriesDoAOS503 exige que o ficheiro de métricas não tenha nenhuma série da nova
// tentativa do AOS-503: esta classe conta nas suas.
func aos511SemSeriesDoAOS503(t *testing.T, metricas string) {
	t.Helper()
	for _, proibida := range []string{metricaPrimeirasFalhas, metricaTentativas + "{", metricaTentativas + " ", metricaNosRecuperados,
		metricaPlanosRecuperados + " ", metricaPlanosEsgotados + " ", metricaTentativasPorPlano, metricaTentativasRecusadas, metricaTentativasEmObservacao} {
		if strings.Contains(metricas, proibida) {
			t.Fatalf("a serie %s e do AOS-503 e nao pode ganhar valores por causa desta classe:\n%s", proibida, metricas)
		}
	}
}

func TestAOS511ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	cat := aos511FioDoNo(t, "tools-enforce-retry-2-empty")
	catSemAClasse := aos503Catalogo(t, 2)

	// (0) OS CORPOS QUE O NÓ CONSOME. As respostas vazias são montadas à mão, só para o processo
	// chegar às tentativas; o que se grava são os corpos de `POST /runs` do nó de resumo.
	t.Run("Fio_OsCorposDasTentativas", func(t *testing.T) {
		f := &aos503No{catalogo: cat, respostas: map[string][]byte{
			aos503Resumo: aos511RespostaVazia(aos503Resumo, 0), aos503ResumoT2: aos511RespostaVazia(aos503ResumoT2, 0),
		}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "", "30s", aos511On)
		if d.codigo != exitOK {
			t.Fatalf("vazio, vazio, sucesso: o plano sai 0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		c1, c2, c3 := f.corpos(aos503Resumo), f.corpos(aos503ResumoT2), f.corpos(aos503ResumoT3)
		if len(c1) != 1 || len(c2) != 1 || len(c3) != 1 {
			t.Fatalf("tres runs do no de resumo, um POST cada: %d %d %d", len(c1), len(c2), len(c3))
		}
		aos511Fio(t, "post-runs-summarize", c1[0])
		aos511Fio(t, "post-runs-summarize-tentativa-2", c2[0])
		aos511Fio(t, "post-runs-summarize-tentativa-3", c3[0])
		// O PEDIDO REPETIDO É O MESMO: só o id e `attempt` mudam; o objectivo e os `inputs`
		// (conteúdo e digests) são iguais byte a byte.
		aos503SoDifereNoIdENaTentativa(t, c1[0], c2[0], aos503ResumoT2, 2)
		aos503SoDifereNoIdENaTentativa(t, c1[0], c3[0], aos503ResumoT3, 3)
		if !bytes.Contains(c1[0], []byte(`"inputs":[{`)) {
			t.Fatalf("pre-condicao: o no de resumo leva inputs: %s", c1[0])
		}
		// O pedido não escolhe a classe: o corpo não nomeia a razão.
		for _, c := range [][]byte{c2[0], c3[0]} {
			if bytes.Contains(c, []byte("empty_output")) || bytes.Contains(c, []byte("reason")) {
				t.Fatalf("o corpo da tentativa nao diz porque se pede: %s", c)
			}
		}
	})

	// (1) VAZIO, VAZIO, SUCESSO — com as respostas do NÓ REAL. O plano sai 0.
	t.Run("On_VazioVazioSucesso", func(t *testing.T) {
		vazia1, vazia2, recuperada3 := aos511FioDoNo(t, "tentativa-vazia-1-falhada"), aos511FioDoNo(t, "tentativa-vazia-2-falhada"), aos511FioDoNo(t, "tentativa-vazia-3-recuperada")
		var st3 estadoDoRun
		if err := json.Unmarshal(recuperada3, &st3); err != nil || !st3.concluiu() || st3.RunID != aos503ResumoT3 || st3.FinalText == "" {
			t.Fatalf("pre-condicao: o fio da tentativa 3 e um run concluido com texto (%v): %s", err, recuperada3)
		}
		f := &aos503No{catalogo: cat, respostas: map[string][]byte{aos503Resumo: vazia1, aos503ResumoT2: vazia2, aos503ResumoT3: recuperada3}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "", "30s", aos511On)
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("vazio, vazio, sucesso: o plano sai terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Resumo, aos503ResumoT2, aos503ResumoT3}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("o leitor e depois tres runs do no de resumo, por esta ordem:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
		}
		factos := aos503Factos(t, s.wal)
		if len(factos) != 2 || factos[0] != (plannerevents.NodeAttemptStartedPayload{PlanID: aos503Run + "-plan", NodeID: "summarize", Attempt: 2, RetryOf: aos503Resumo, Reason: plannerevents.AttemptReasonEmptyOutput}) ||
			factos[1] != (plannerevents.NodeAttemptStartedPayload{PlanID: aos503Run + "-plan", NodeID: "summarize", Attempt: 3, RetryOf: aos503ResumoT2, Reason: plannerevents.AttemptReasonEmptyOutput}) {
			t.Fatalf("dois factos com reason=empty_output, um por tentativa: %+v", factos)
		}
		if !strings.HasSuffix(d.detalhe, " tentativas=2 recuperados=1") || strings.Contains(d.detalhe, "causa=") || strings.Contains(d.detalhe, "esgotadas") {
			t.Fatalf("o detail leva as contagens, e mais nada sobre falhas: %q", d.detalhe)
		}
		if !strings.Contains(d.stdout, "execucao: read_notes=complete summarize=complete\n") || !strings.Contains(d.stdout, "NOVA TENTATIVA 2 POR RESPOSTA VAZIA") {
			t.Fatalf("o log diz as tentativas e os estados finais:\n%s", d.stdout)
		}
		for chave, valor := range map[string]int{
			metricaPrimeirasVazias: 1,
			serie(metricaTentativasVazia, "tentativa", "2", "desfecho", tentativaVoltouAFalhar): 1,
			serie(metricaTentativasVazia, "tentativa", "3", "desfecho", tentativaRecuperou):     1,
			metricaPlanosRecuperadosVazia:                                1,
			serie(metricaDesfechos, "classe", "terminal", "codigo", "0"): 1,
		} {
			if !temSerie(d.metricas, chave, valor) {
				t.Fatalf("faltou a serie %s %d:\n%s", chave, valor, d.metricas)
			}
		}
		aos511SemSeriesDoAOS503(t, d.metricas)
		for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": aos501MetricasSemRelogio(d.metricas), "detalhe": d.detalhe} {
			if levaProibido(texto, st3.FinalText) {
				t.Fatalf("o texto final da tentativa recuperada aparece em %s", onde)
			}
		}
	})

	// (2) VAZIO TRÊS VEZES: as tentativas esgotam-se, o plano sai 13 com a causa e a marca.
	t.Run("On_VazioTresVezes_Esgota", func(t *testing.T) {
		f := &aos503No{catalogo: cat, respostas: map[string][]byte{
			aos503Resumo: aos511FioDoNo(t, "tentativa-vazia-1-falhada"), aos503ResumoT2: aos511FioDoNo(t, "tentativa-vazia-2-falhada"),
			aos503ResumoT3: aos511FioDoNo(t, "tentativa-vazia-3-falhada"),
		}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "", "30s", aos511On)
		if d.classe != "terminal" || d.codigo != exitNosFalhados {
			t.Fatalf("com as tentativas esgotadas o plano sai terminal/13; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Resumo, aos503ResumoT2, aos503ResumoT3}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("no maximo duas tentativas a mais: %v", f.submetidos())
		}
		if !strings.Contains(d.detalhe, "causa=empty_output:1") || !strings.Contains(d.detalhe, " tentativas=2 recuperados=0 tentativas_esgotadas=1") {
			t.Fatalf("o detail leva causa=empty_output e tentativas_esgotadas: %q", d.detalhe)
		}
		if !strings.Contains(d.stdout, "(run "+aos503ResumoT3+") causa=empty_output") || !strings.Contains(d.stdout, " tentativas=3 tentativas_esgotadas") {
			t.Fatalf("o no fecha sobre o run da ultima tentativa, com a causa e a marca:\n%s", d.stdout)
		}
		for chave, valor := range map[string]int{
			metricaPrimeirasVazias: 1,
			serie(metricaTentativasVazia, "tentativa", "2", "desfecho", tentativaVoltouAFalhar): 1,
			serie(metricaTentativasVazia, "tentativa", "3", "desfecho", tentativaVoltouAFalhar): 1,
			metricaPlanosEsgotadosVazia: 1,
		} {
			if !temSerie(d.metricas, chave, valor) {
				t.Fatalf("faltou a serie %s %d:\n%s", chave, valor, d.metricas)
			}
		}
		aos511SemSeriesDoAOS503(t, d.metricas)
	})

	// (3) OFF, OBSERVE E OS CASOS NÃO ELEGÍVEIS: os mesmos POST, os mesmos eventos do plano, o
	// mesmo código e o mesmo detail de um `serve` sem a variável.
	respostasVazias := map[string][]byte{aos503Resumo: aos511RespostaVazia(aos503Resumo, 0)}
	semVariavel := func(t *testing.T, catalogo []byte, respostas map[string][]byte, modo503 string) (aos499Drenagem, *aos503No, []aos499Evento) {
		t.Helper()
		f := &aos503No{catalogo: catalogo, respostas: respostas}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, modo503, "30s")
		return d, f, aos499EventosDoPlano(t, s.wal, aos503Run)
	}
	igualASemVariavel := func(t *testing.T, catalogo []byte, respostas map[string][]byte, modo503 string, extra ...string) aos499Drenagem {
		t.Helper()
		f := &aos503No{catalogo: catalogo, respostas: respostas}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, modo503, "30s", extra...)
		eventos := aos499EventosDoPlano(t, s.wal, aos503Run)
		dRef, fRef, evRef := semVariavel(t, catalogo, respostas, modo503)
		if d.codigo != dRef.codigo || d.classe != dRef.classe || aos503SemVolatil(d.detalhe) != aos503SemVolatil(dRef.detalhe) {
			t.Fatalf("o desfecho tinha de ser o de um serve sem a variavel (%s/%d %q); saiu %s/%d %q\n%s", dRef.classe, dRef.codigo, dRef.detalhe, d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if fmt.Sprint(f.submetidos()) != fmt.Sprint(fRef.submetidos()) {
			t.Fatalf("os POST /runs tinham de ser os mesmos:\n  sem:   %v\n  agora: %v", fRef.submetidos(), f.submetidos())
		}
		for _, id := range fRef.submetidos() {
			if !bytes.Equal(f.corpos(id)[0], fRef.corpos(id)[0]) {
				t.Fatalf("o corpo de %s tinha de ser o mesmo:\n  sem:   %s\n  agora: %s", id, fRef.corpos(id)[0], f.corpos(id)[0])
			}
		}
		if fmt.Sprint(eventos) != fmt.Sprint(evRef) {
			t.Fatalf("os eventos do plano tinham de ser os mesmos:\n  sem:   %v\n  agora: %v", evRef, eventos)
		}
		if strings.Contains(dRef.metricas, "vazia") || strings.Contains(dRef.stdout, "RESPOSTA VAZIA") {
			t.Fatalf("pre-condicao: sem a variavel nada fala desta classe:\n%s\n%s", dRef.metricas, dRef.stdout)
		}
		if len(aos503Factos(t, s.wal)) != 0 {
			t.Fatalf("nenhum facto de tentativa: %+v", aos503Factos(t, s.wal))
		}
		for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": d.metricas, "detalhe": d.detalhe} {
			if levaProibido(texto, aos511MarcadorDoRunFalhado) {
				t.Fatalf("o texto do run falhado aparece em %s", onde)
			}
		}
		return d
	}
	t.Run("Off_EOBinarioDeAntes", func(t *testing.T) {
		d := igualASemVariavel(t, cat, respostasVazias, "", aos511Off)
		if d.codigo != exitNosFalhados || !strings.Contains(d.detalhe, "causa=empty_output:1") {
			t.Fatalf("em off o no que respondeu vazio fecha failed e o plano sai 13, como hoje; saiu %d %q", d.codigo, d.detalhe)
		}
		// As métricas e o log são os de um `serve` sem a variável, linha a linha (a menos do relógio).
		ref, _, _ := semVariavel(t, cat, respostasVazias, "")
		if aos501MetricasSemRelogio(d.metricas) != aos501MetricasSemRelogio(ref.metricas) {
			t.Fatalf("em off o ficheiro de metricas e o de antes:\n  sem:\n%s\n  off:\n%s", ref.metricas, d.metricas)
		}
		if strings.Contains(d.stdout, "RESPOSTA VAZIA") || strings.Contains(d.stdout, "AOS-511") {
			t.Fatalf("em off o log nao fala desta classe:\n%s", d.stdout)
		}
		// E com o interruptor do AOS-503 em `on`: esta classe continua desligada.
		d503 := igualASemVariavel(t, cat, respostasVazias, "on", aos511Off)
		if d503.codigo != exitNosFalhados || strings.Contains(d503.metricas, "vazia") {
			t.Fatalf("o interruptor do AOS-503 nao liga esta classe; saiu %d\n%s", d503.codigo, d503.metricas)
		}
	})
	t.Run("Observe_ContaENaoTenta", func(t *testing.T) {
		d := igualASemVariavel(t, cat, respostasVazias, "", aos511Observe)
		if !temSerie(d.metricas, metricaPrimeirasVazias, 1) || !temSerie(d.metricas, metricaTentativasVaziaEmObservacao, 1) {
			t.Fatalf("em observe conta a primeira resposta vazia e a que tentaria:\n%s", d.metricas)
		}
		if strings.Contains(d.metricas, metricaTentativasVazia+"{") || strings.Contains(d.metricas, metricaTentativasVaziaRecusadas) {
			t.Fatalf("em observe nao ha tentativas nem recusas:\n%s", d.metricas)
		}
		if !strings.Contains(d.stdout, "execucao: no summarize NOVA TENTATIVA POR RESPOSTA VAZIA EM OBSERVACAO") || !strings.Contains(d.stdout, "causa=empty_output") {
			t.Fatalf("em observe o log diz o no e a causa:\n%s", d.stdout)
		}
		aos511SemSeriesDoAOS503(t, d.metricas)
	})
	for nome, respostas := range map[string]map[string][]byte{
		"empty_output-com-tool-calls-pedidas": {aos503Resumo: aos511RespostaVazia(aos503Resumo, 1)},
		"truncated":                           {aos503Resumo: aos503Falhado(aos503Resumo, "truncated", 0)},
		"timed_out":                           {aos503Resumo: []byte(`{"run_id":"` + aos503Resumo + `","status":"timed_out","turns":1}`)},
		"run-que-nao-concluiu":                {aos503Resumo: []byte(`{"run_id":"` + aos503Resumo + `","status":"failed","turns":1}`)},
		"no-com-tools-que-respondeu-vazio":    {aos503Leitor1: aos511RespostaVazia(aos503Leitor1, 0)},
	} {
		t.Run("On_NaoTenta/"+nome, func(t *testing.T) {
			d := igualASemVariavel(t, cat, respostas, "", aos511On)
			if d.codigo != exitNosFalhados || strings.Contains(d.metricas, "vazia") {
				t.Fatalf("um caso nao elegivel fecha como hoje e nao conta nada desta classe; saiu %d\n%s", d.codigo, d.metricas)
			}
		})
	}
	t.Run("On_NaoTenta/503-do-no", func(t *testing.T) {
		// A leitura do estado falha umas vezes e depois diz que o run concluiu: não se tenta nada.
		f := &aos503No{catalogo: cat, ilegivelAntes: map[string]int{aos503Resumo: 2}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "", "30s", aos511On)
		if d.codigo != exitOK || len(aos503Factos(t, s.wal)) != 0 || strings.Contains(d.metricas, "vazia") {
			t.Fatalf("um 503 na leitura nao e uma resposta vazia; saiu %d\n%s", d.codigo, d.stdout)
		}
	})

	// (4) UM NÓ QUE NÃO ANUNCIA A CLASSE (anterior ao AOS-510, ou com AOS_RUN_RETRY_EMPTY
	// desligado): nenhuma tentativa, o desfecho de hoje, e a causa contada como `nao_anunciado`.
	t.Run("On_NoQueNaoAnunciaAClasse_ComoHoje", func(t *testing.T) {
		f := &aos503No{catalogo: catSemAClasse, respostas: respostasVazias}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "", "30s", aos511On)
		if d.codigo != exitNosFalhados || len(aos503Factos(t, s.wal)) != 0 || fmt.Sprint(f.submetidos()) != fmt.Sprint([]string{aos503Leitor1, aos503Resumo}) {
			t.Fatalf("contra um no que nao anuncia a classe nao ha tentativa: saiu %d submetidos=%v", d.codigo, f.submetidos())
		}
		if !temSerie(d.metricas, serie(metricaTentativasVaziaRecusadas, "causa", recusaNaoAnunciado), 1) ||
			!strings.Contains(d.detalhe, "causa=empty_output:1") || !strings.Contains(d.detalhe, "tentativa_recusada="+recusaNaoAnunciado) {
			t.Fatalf("a causa conta como nao_anunciado: %q\n%s", d.detalhe, d.metricas)
		}
		if !strings.Contains(d.stdout, "nova tentativa por resposta vazia (AOS-511, ADR-039): LIGADA E NAO APLICADA") {
			t.Fatalf("o banner diz que a classe nao se aplica contra este no:\n%s", d.stdout)
		}
	})

	// (5) OS TECTOS SÃO PARTILHADOS. As duas classes ligadas e o tecto por plano a 1: o leitor
	// gasta-o (termina sem chamar a tool, e recupera na tentativa 2); o nó de resumo responde
	// vazio e já não tem tentativa — um plano não ganha tentativas por ter as duas classes.
	t.Run("On_OsTectosSaoPartilhadosComOAOS503", func(t *testing.T) {
		recuperada2 := aos503NaTentativa(t, aos503ComId(t, aos503FioDoNo(t, "tentativa-3-recuperada"), aos503Leitor3, aos503Leitor2), 3, 2)
		f := &aos503No{catalogo: cat, respostas: map[string][]byte{
			aos503Leitor1: aos503FioDoNo(t, "tentativa-1-falhada"), aos503Leitor2: recuperada2, aos503Resumo: aos511RespostaVazia(aos503Resumo, 0),
		}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "on", "30s", aos511On, "AOS_ORQ_NOVA_TENTATIVA_MAX_POR_PLANO=1")
		if d.codigo != exitNosFalhados {
			t.Fatalf("o no de resumo ja nao tem tentativa e o plano sai 13; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Leitor2, aos503Resumo}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("uma tentativa no plano inteiro, a do leitor:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
		}
		factos := aos503Factos(t, s.wal)
		if len(factos) != 1 || factos[0].NodeID != "read_notes" || factos[0].Reason != plannerevents.AttemptReasonContractUnmetNoCall {
			t.Fatalf("um so facto, o do leitor: %+v", factos)
		}
		if !temSerie(d.metricas, serie(metricaTentativasVaziaRecusadas, "causa", recusaTectoDoPlano), 1) ||
			!temSerie(d.metricas, serie(metricaTentativas, "tentativa", "2", "desfecho", tentativaRecuperou, "com_consumes", "false"), 1) ||
			!temSerie(d.metricas, serie(metricaTentativasPorPlano, "tentativas", "1"), 1) ||
			strings.Contains(d.metricas, metricaTentativasRecusadas) {
			t.Fatalf("a recusa conta na serie desta classe, e as do AOS-503 contam so o leitor:\n%s", d.metricas)
		}
	})

	// (6) O FACTO FICA NO LOG ANTES DO PEDIDO AO NÓ, E A RETOMA CONTINUA DELE.
	t.Run("On_OFactoAntesDoPedido_ERetomaEntreOFactoEASubmissao", func(t *testing.T) {
		var falhar atomic.Bool
		falhar.Store(true)
		f := &aos503No{catalogo: cat, respostas: map[string][]byte{aos503Resumo: aos511FioDoNo(t, "tentativa-vazia-1-falhada"), aos503ResumoT2: aos511Concluido(aos503ResumoT2, 2)},
			estadoDoPost: func(id string, _ int) int {
				if id == aos503ResumoT2 && falhar.Load() {
					return http.StatusInternalServerError
				}
				return 0
			}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d1 := s.drenar(t, 1, "", "30s", aos511On)
		if d1.codigo == exitOK || d1.codigo == exitNosFalhados || d1.classe == "terminal" {
			t.Fatalf("com o POST da tentativa a dar 500 o serve aborta sem fechar o no; saiu %s/%d %q\n%s", d1.classe, d1.codigo, d1.detalhe, d1.stdout)
		}
		factos := aos503Factos(t, s.wal)
		if len(factos) != 1 || factos[0].Attempt != 2 || factos[0].RetryOf != aos503Resumo || factos[0].Reason != plannerevents.AttemptReasonEmptyOutput || f.aceite(aos503ResumoT2) {
			t.Fatalf("o facto da tentativa 2 tinha de estar no log ANTES de o no aceitar o run: factos=%+v aceite=%v\n%s", factos, f.aceite(aos503ResumoT2), d1.stdout)
		}
		falhar.Store(false)
		aos503EsperarAPosse()
		// A retoma corre SÓ com o interruptor desta classe: é a razão do facto que a liga.
		d2 := s.drenar(t, 2, "", "30s", aos511On)
		if d2.classe != "terminal" || d2.codigo != exitOK {
			t.Fatalf("a retoma submete a tentativa que o log regista e o plano conclui; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		posts := 0
		for _, id := range f.submetidos() {
			if id == aos503ResumoT2 {
				posts++
			}
			if id == aos503ResumoT3 {
				t.Fatalf("a retoma nunca salta para a tentativa 3: %v", f.submetidos())
			}
		}
		if posts != 2 || len(aos503Factos(t, s.wal)) != 1 {
			t.Fatalf("a retoma submete a MESMA tentativa uma vez (o POST que deu 500 e o da retoma), sem outro facto: posts=%d factos=%d", posts, len(aos503Factos(t, s.wal)))
		}
		if !strings.Contains(d2.stdout, "execucao: no summarize RETOMA da tentativa 2") || !strings.HasSuffix(d2.detalhe, " tentativas=1 recuperados=1") ||
			!temSerie(d2.metricas, serie(metricaTentativasVazia, "tentativa", "2", "desfecho", tentativaRecuperou), 1) {
			t.Fatalf("o log, o detail e a serie da retoma:\n%s\n%q\n%s", d2.stdout, d2.detalhe, d2.metricas)
		}
	})

	// (7) RETOMA ENTRE A SUBMISSÃO E A RECOLHA: o run da tentativa existe e o `serve` seguinte não
	// o submeteu. Segue-o com a origem conferida; um run com esse id que NÃO declara ser a
	// tentativa deste nó deste pedido não se segue.
	for nome, c := range map[string]struct {
		depois []byte
		codigo int
	}{
		"com-a-origem-conferida": {aos511Concluido(aos503ResumoT2, 2), exitOK},
		"um-run-alheio-sem-origem": {[]byte(`{"run_id":"` + aos503ResumoT2 + `","status":"completed","terminated":true,"final_text":"` + aos511MarcadorDoRunFalhado + `","turns":1}`),
			exitNosFalhados},
	} {
		t.Run("On_RetomaEntreASubmissaoEARecolha/"+nome, func(t *testing.T) {
			aCorrer := []byte(`{"run_id":"` + aos503ResumoT2 + `","status":"in_progress","plan_attempt":{"plan_request":"` + aos503Run + `","generation":1,"plan_id":"` + aos503Run + `-plan","node_id":"summarize","attempt":2}}`)
			f := &aos503No{catalogo: cat, respostas: map[string][]byte{aos503Resumo: aos511FioDoNo(t, "tentativa-vazia-1-falhada"), aos503ResumoT2: aCorrer}}
			s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
			if d1 := s.drenar(t, 1, "", "1s", aos511On); d1.codigo != exitNosEmVoo {
				t.Fatalf("a primeira drenagem sai com a tentativa em voo (8); saiu %s/%d %q\n%s", d1.classe, d1.codigo, d1.detalhe, d1.stdout)
			}
			f.responder(aos503ResumoT2, c.depois)
			d2 := s.drenar(t, 2, "", "30s", aos511On)
			if d2.classe != "terminal" || d2.codigo != c.codigo {
				t.Fatalf("queria terminal/%d; saiu %s/%d %q\n%s", c.codigo, d2.classe, d2.codigo, d2.detalhe, d2.stdout)
			}
			if quer := []string{aos503Leitor1, aos503Resumo, aos503ResumoT2}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) || len(aos503Factos(t, s.wal)) != 1 {
				t.Fatalf("a tentativa foi submetida UMA vez, e ha um so facto: %v", f.submetidos())
			}
			if c.codigo == exitNosFalhados {
				if !strings.Contains(d2.detalhe, "tentativa_recusada="+recusaRunDeOutraOrigem) ||
					!temSerie(d2.metricas, serie(metricaTentativasVaziaRecusadas, "causa", recusaRunDeOutraOrigem), 1) || strings.Contains(d2.metricas, metricaTentativasRecusadas) {
					t.Fatalf("o run alheio nao se segue, e a recusa conta na serie desta classe: %q\n%s", d2.detalhe, d2.metricas)
				}
				for onde, texto := range map[string]string{"stdout": d2.stdout, "stderr": d2.stderr, "metricas": d2.metricas, "detalhe": d2.detalhe} {
					if levaProibido(texto, aos511MarcadorDoRunFalhado) {
						t.Fatalf("o texto do run alheio aparece em %s", onde)
					}
				}
			}
		})
	}

	// (8) UM `aos-orq` ANTERIOR (ou com esta classe em `off`) RETOMA UM PLANO COM UM FACTO
	// `reason=empty_output` NO LOG. Segue a tentativa que o log regista, não começa outra, e fecha
	// o nó como hoje fecharia: sobre o run dessa tentativa, sem publicar nada do que falhou.
	t.Run("Off_ComUmFactoDestaClasseNoLog_SegueENaoComecaOutra", func(t *testing.T) {
		aCorrer := []byte(`{"run_id":"` + aos503ResumoT2 + `","status":"in_progress","plan_attempt":{"plan_request":"` + aos503Run + `","generation":1,"plan_id":"` + aos503Run + `-plan","node_id":"summarize","attempt":2}}`)
		f := &aos503No{catalogo: cat, respostas: map[string][]byte{aos503Resumo: aos511FioDoNo(t, "tentativa-vazia-1-falhada"), aos503ResumoT2: aCorrer}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		if d1 := s.drenar(t, 1, "", "1s", aos511On); d1.codigo != exitNosEmVoo {
			t.Fatalf("pre-condicao: a tentativa fica em voo (8); saiu %d", d1.codigo)
		}
		f.responder(aos503ResumoT2, aos511FioDoNo(t, "tentativa-vazia-2-falhada"))
		d2 := s.drenar(t, 2, "", "30s")
		if d2.classe != "terminal" || d2.codigo != exitNosFalhados {
			t.Fatalf("sem a variavel, a tentativa que voltou a responder vazio fecha o no; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Resumo, aos503ResumoT2}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) || len(aos503Factos(t, s.wal)) != 1 {
			t.Fatalf("sem a variavel nao comeca a tentativa 3: %v", f.submetidos())
		}
		if !strings.Contains(d2.stdout, "(run "+aos503ResumoT2+") causa=empty_output") {
			t.Fatalf("o no fecha sobre o run da tentativa que o log regista:\n%s", d2.stdout)
		}
		// A recorrência conta na série DESTA classe mesmo com o interruptor desligado — e as do
		// AOS-503 não se mexem.
		if !temSerie(d2.metricas, serie(metricaTentativasVazia, "tentativa", "2", "desfecho", tentativaVoltouAFalhar), 1) {
			t.Fatalf("a tentativa 2 que voltou a responder vazio conta voltou_a_falhar na serie desta classe:\n%s", d2.metricas)
		}
		aos511SemSeriesDoAOS503(t, d2.metricas)
	})
}
