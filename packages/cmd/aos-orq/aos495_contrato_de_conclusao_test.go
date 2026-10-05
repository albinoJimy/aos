package main

// AOS-495 — O `aos-orq` DECLARA O CONTRATO DE CONCLUSÃO E NÃO PUBLICA SAÍDAS SEM EVIDÊNCIA.
//
// Os testes de processo correm o `aos-orq consume` REAL contra um nó `aos` falso. O que o nó
// falso responde no `GET /runs/{id}` não é inventado aqui: são os ficheiros
// `packages/cmd/aos/testdata/aos494_fio/`, que o teste do nó (`TestAOS494_Fio_…`) exige que o
// nó REAL responda, byte a byte, às duas respostas de produção de 2026-10-04 e à resposta boa,
// com o modelo falso. Este módulo não pode importar o pacote do nó; é por esses ficheiros que
// os dois lados do fio ficam presos.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos495Fio lê uma resposta do `GET /runs/{id}` do nó real, gravada pelo teste do AOS-494.
func aos495Fio(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "aos", "testdata", "aos494_fio", nome+".json"))
	if err != nil || len(bytes.TrimSpace(cru)) == 0 {
		t.Fatalf("ficheiro do fio %s: err=%v bytes=%d", nome, err, len(cru))
	}
	return cru
}

// aos495Planos são os dois planos de produção de 2026-10-04 que saíram verdes sem cumprir.
var aos495Planos = []string{"plan-e2e-v0145-1791115918", "plan-e2e-v0145-1791117087"}

// camposDoPostRunsAnterior são os campos que o `POST /runs` de um nó ANTERIOR ao AOS-494
// aceita. O decoder dele é estrito: qualquer outro campo dá 400.
var camposDoPostRunsAnterior = []string{"run_id", "objective", "principal_nhi", "credential", "scope", "system", "max_turns", "tools", "inputs", "plan_request"}

// aos495No é o nó `aos` falso destes testes.
type aos495No struct {
	// anuncio é o `completion_contract.mode` do `GET /tools`; vazio ⇒ o nó NÃO anuncia (um nó
	// anterior ao AOS-494).
	anuncio string
	// estrito ⇒ o `POST /runs` recusa com 400 um campo fora de [camposDoPostRunsAnterior], como
	// o decoder de um nó anterior.
	estrito bool
	// respostas é o corpo do `GET /runs/{id}` por nó do plano (`n1`, `n2`). Sem entrada, o run
	// conclui com o texto `feito: <id>`.
	respostas map[string][]byte

	mu        sync.Mutex
	ofertas   []pedidoReclamado
	desfechos []map[string]any
	corpos    map[string]map[string]any
	recusados int
}

func (f *aos495No) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	f.corpos = map[string]map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plans/claim", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.ofertas) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		p := f.ofertas[0]
		f.ofertas = f.ofertas[1:]
		_ = json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("POST /plans/outcome", func(w http.ResponseWriter, r *http.Request) {
		var d map[string]any
		_ = json.NewDecoder(r.Body).Decode(&d)
		f.mu.Lock()
		f.desfechos = append(f.desfechos, d)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		var corpo map[string]any
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		id, _ := corpo["run_id"].(string)
		if f.estrito {
			for campo := range corpo {
				if !slices.Contains(camposDoPostRunsAnterior, campo) {
					f.mu.Lock()
					f.recusados++
					f.mu.Unlock()
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"corpo invalido"}`))
					return
				}
			}
		}
		f.mu.Lock()
		_, repetida := f.corpos[id]
		f.corpos[id] = corpo
		f.mu.Unlock()
		if repetida {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		_, existe := f.corpos[id]
		f.mu.Unlock()
		if !existe {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if cru, ok := f.respostas[id[strings.LastIndex(id, "~")+1:]]; ok {
			_, _ = w.Write(cru)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "completed", "terminated": true, "final_text": "feito: " + id})
	})
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		cat := strings.TrimSpace(aos441CatalogoDoSnapshotComPerigo)
		if f.anuncio != "" {
			cat = strings.TrimSuffix(cat, "}") + `,"completion_contract":{"mode":"` + f.anuncio + `"}}`
		}
		_, _ = w.Write([]byte(cat))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// corpo devolve o corpo do `POST /runs` do nó do plano `no`, ou nil se não foi submetido.
func (f *aos495No) corpo(run, no string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.corpos[run+"~"+no]
}

// aos495Desfecho é o que UMA drenagem deixou: o que o nó recebeu e o que o processo imprimiu.
type aos495Desfecho struct {
	codigo  int
	classe  string
	detalhe string
	stdout  string
}

// aos495Consumir oferece um pedido de plano e corre UMA drenagem do `aos-orq consume` real, com
// o plano de leitura e resumo do AOS-484 a fazer de decomposição.
func aos495Consumir(t *testing.T, bin string, f *aos495No, run string) aos495Desfecho {
	t.Helper()
	srv := f.servidor(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, aos484PlanoLerEResumir)
	f.mu.Lock()
	f.ofertas = append(f.ofertas, pedidoReclamado{RunID: run, Objective: "ler o documento notes e resumi-lo", Geracao: 1})
	f.mu.Unlock()

	env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	r := correrComEnv(t, env, bin, "consume", "--wal", filepath.Join(dir, "consume.wal"), "--snapshot", snap,
		"--decompose-fixture", fix, "--poll-interval", "20ms", "--max", "1", "--plan-timeout", "30s")
	if r.code != exitOK {
		t.Fatalf("o consume em si tinha de sair 0 (o desfecho vai para o no), saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.desfechos) != 1 {
		t.Fatalf("queria um desfecho reportado ao no, vieram %d\n%s\n%s", len(f.desfechos), r.stdout, r.stderr)
	}
	d := f.desfechos[0]
	codigo, _ := d["codigo_saida"].(float64)
	classe, _ := d["classe"].(string)
	detalhe, _ := d["detalhe"].(string)
	return aos495Desfecho{codigo: int(codigo), classe: classe, detalhe: detalhe, stdout: r.stdout}
}

// contratoEnviado lê o `completion_requires` do corpo de um `POST /runs`.
func contratoEnviado(corpo map[string]any) (nomes []string, presente bool) {
	cru, ok := corpo["completion_requires"]
	if !ok {
		return nil, false
	}
	lista, _ := cru.([]any)
	for _, v := range lista {
		s, _ := v.(string)
		nomes = append(nomes, s)
	}
	return nomes, true
}

// TestAOS495ComOBinarioReal corre os cenários de processo sobre UM só binário.
func TestAOS495ComOBinarioReal(t *testing.T) {
	bin := construir(t)

	// (1) AS DUAS RESPOSTAS DE PRODUÇÃO, COM O NÓ EM `enforce`: o run filho vem `failed` por
	// contrato, o nó do plano fica `failed`, e o plano sai 13 com a razão.
	for _, plano := range aos495Planos {
		t.Run("Producao_NoEmEnforce_Sai13ComARazao/"+plano, func(t *testing.T) {
			const run = "plan-aos495-enforce"
			f := &aos495No{anuncio: "enforce", respostas: map[string][]byte{"n1": aos495Fio(t, plano+"-enforce")}}
			d := aos495Consumir(t, bin, f, run)

			if d.classe != "terminal" || d.codigo != exitNosFalhados {
				t.Fatalf("o plano tinha de sair terminal/13; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
			}
			// A razão no `detail` do desfecho — o que o `GET /plans/{id}` serve —, em vocabulário
			// fechado: a do veredicto do kernel para o n1, e a do n2, que não correu.
			if !strings.HasSuffix(d.detalhe, " erro=nos_falhados causa=contract_unmet_no_call:1,entrada_por_cumprir:1") {
				t.Fatalf("o detail tinha de levar a razao: %q", d.detalhe)
			}
			// E no log da drenagem: a linha do nó, a do desfecho e a do aviso.
			for _, quer := range []string{
				"execucao: no n1 leva contrato de conclusao: fs.read\n",
				"execucao: no n1 failed (run " + run + "~n1) causa=contract_unmet_no_call vector [",
				"execucao: n1=failed n2=failed",
				"desfecho: run=" + run + " codigo=13 classe=terminal ",
				" erro=nos_falhados causa=contract_unmet_no_call:1,entrada_por_cumprir:1\n",
				linhaDoAvisoComCausa(run, 1, "terminal", exitNosFalhados, causaConclusaoNaoCumprida) + "\n",
			} {
				if !strings.Contains(d.stdout, quer) {
					t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
				}
			}
			// O contrato foi declarado com as tools atribuídas ao n1.
			if c, ok := contratoEnviado(f.corpo(run, "n1")); !ok || !slices.Equal(c, []string{"fs.read"}) {
				t.Fatalf("o n1 tinha de levar o contrato [fs.read]; levou %v (presente=%v)", c, ok)
			}
			// Nada foi publicado, e o consumidor não correu.
			if strings.Contains(d.stdout, "execucao: payload ") || f.corpo(run, "n2") != nil {
				t.Fatalf("um no failed nao publica saida, e o consumidor nao corre:\n%s", d.stdout)
			}
		})
	}

	// (2) A RESPOSTA BOA: o modelo chamou a tool, o contrato está cumprido, e o plano sai 0.
	t.Run("RespostaBoa_Sai0", func(t *testing.T) {
		const run = "plan-aos495-boa"
		f := &aos495No{anuncio: "enforce", respostas: map[string][]byte{"n1": aos495Fio(t, "boa-enforce")}}
		d := aos495Consumir(t, bin, f, run)
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("a resposta boa tinha de sair terminal/0; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
		}
		if strings.Contains(d.detalhe, "causa=") || strings.Contains(d.stdout, " causa=") || strings.Contains(d.stdout, "VEREDICTO OBSERVADO") {
			t.Fatalf("um plano que correu bem nao leva causa nem veredicto observado: %q\n%s", d.detalhe, d.stdout)
		}
		if !strings.Contains(d.stdout, linhaDoAviso(run, 1, "terminal", exitOK)+"\n") {
			t.Fatalf("o aviso de um plano ok e a linha de sempre:\n%s", d.stdout)
		}
		entradas, _ := f.corpo(run, "n2")["inputs"].([]any)
		if len(entradas) != 1 || entradas[0].(map[string]any)["content"] != "O documento notes diz: conteudo do documento notes" {
			t.Fatalf("o consumidor tinha de receber o que o produtor escreveu: %v", entradas)
		}
		// O n2 não tem tools: não é elegível e não leva o campo.
		if _, ok := contratoEnviado(f.corpo(run, "n2")); ok {
			t.Fatal("um no sem tools atribuidas nao leva contrato")
		}
	})

	// (3) AS DUAS RESPOSTAS DE PRODUÇÃO, COM O NÓ EM `observe`: o nó calcula o veredicto e não
	// fecha o run. O `aos-orq` porta-se como antes — nó `complete`, saída publicada, plano 0 — e
	// regista o veredicto observado. A RAZÃO NÃO É O CRITÉRIO: vem com um run `terminated`.
	for _, plano := range aos495Planos {
		t.Run("Producao_NoEmObserve_Sai0ERegistaOVeredicto/"+plano, func(t *testing.T) {
			const run = "plan-aos495-observe"
			fio := aos495Fio(t, plano+"-observe")
			f := &aos495No{anuncio: "observe", respostas: map[string][]byte{"n1": fio}}
			d := aos495Consumir(t, bin, f, run)
			if d.classe != "terminal" || d.codigo != exitOK {
				t.Fatalf("em observacao o plano sai como hoje, terminal/0; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
			}
			for _, quer := range []string{
				"execucao: no n1 leva contrato de conclusao: fs.read\n",
				"execucao: no n1 VEREDICTO OBSERVADO contract_unmet_no_call (modo observe), vector [",
				"execucao: payload n1/conteudo publicado (record)",
				"execucao: n1=complete n2=complete",
			} {
				if !strings.Contains(d.stdout, quer) {
					t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
				}
			}
			var st estadoDoRun
			if err := json.Unmarshal(fio, &st); err != nil || st.OutcomeReason == "" || !st.Terminated {
				t.Fatalf("pre-condicao: o fio de observacao traz razao E terminated (%+v, %v)", st, err)
			}
			entradas, _ := f.corpo(run, "n2")["inputs"].([]any)
			if len(entradas) != 1 || entradas[0].(map[string]any)["content"] != st.FinalText {
				t.Fatalf("em observacao o consumidor recebe o texto, como hoje: %v", entradas)
			}
			if strings.Contains(d.detalhe, "causa=") {
				t.Fatalf("um plano que saiu 0 nao leva causa no detail: %q", d.detalhe)
			}
		})
	}

	// (4) `aos-orq` NOVO CONTRA UM NÓ ANTERIOR. O nó não anuncia o contrato e o seu `POST /runs`
	// recusa campos que não conhece. O `aos-orq` não envia o campo, submete como hoje, e diz que
	// o contrato não foi aplicado.
	t.Run("NoAnterior_NaoLeva400", func(t *testing.T) {
		const run = "plan-aos495-no-anterior"
		f := &aos495No{estrito: true}
		d := aos495Consumir(t, bin, f, run)
		if f.recusados != 0 {
			t.Fatalf("o no anterior recusou %d submissao(oes) com 400: o aos-orq enviou um campo que ele nao conhece\n%s", f.recusados, d.stdout)
		}
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("contra um no anterior o plano corre como hoje, terminal/0; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
		}
		for _, no := range []string{"n1", "n2"} {
			if _, ok := contratoEnviado(f.corpo(run, no)); ok || f.corpo(run, no) == nil {
				t.Fatalf("o %s tinha de ser submetido, e sem contrato: %v", no, f.corpo(run, no))
			}
		}
		for _, quer := range []string{
			"contrato de conclusao (AOS-495): NAO APLICADO — o no nao anuncia o suporte",
			"execucao: no n1 elegivel para contrato de conclusao, NAO aplicado",
		} {
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("o log tinha de dizer que o contrato nao foi aplicado (%q):\n%s", quer, d.stdout)
			}
		}
	})

	// (5) UMA SAÍDA VAZIA NUNCA É PUBLICADA: o produtor fica `failed`, com razão própria.
	for _, c := range []struct {
		nome, resposta, causa string
	}{
		{"SemTexto", `{"run_id":"x","status":"completed","terminated":true}`, causaSaidaVazia},
		{"SoEspacos", `{"run_id":"x","status":"completed","terminated":true,"final_text":"  \n\t"}`, causaSaidaVazia},
		// O ramo durável do nó depois de um reinício, quando a captura não se lê (AOS-494).
		{"SaidaIndisponivelNoNo", `{"run_id":"x","status":"completed","terminated":true,"output_unavailable":true}`, causaSaidaIndisponivel},
	} {
		t.Run("SaidaVaziaNaoSePublica/"+c.nome, func(t *testing.T) {
			const run = "plan-aos495-vazia"
			f := &aos495No{anuncio: "observe", respostas: map[string][]byte{"n1": []byte(c.resposta)}}
			d := aos495Consumir(t, bin, f, run)
			if d.classe != "terminal" || d.codigo != exitNosFalhados {
				t.Fatalf("o produtor de uma saida vazia fica failed e o plano sai 13; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
			}
			if !strings.HasSuffix(d.detalhe, " erro=nos_falhados causa=entrada_por_cumprir:1,"+c.causa+":1") {
				t.Fatalf("o detail tinha de dizer %s: %q", c.causa, d.detalhe)
			}
			if strings.Contains(d.stdout, "execucao: payload ") || f.corpo(run, "n2") != nil {
				t.Fatalf("a saida vazia foi publicada, ou o consumidor correu com ela:\n%s", d.stdout)
			}
			if !strings.Contains(d.stdout, "execucao: no n1 failed (run "+run+"~n1) causa="+c.causa+" ") ||
				!strings.Contains(d.stdout, linhaDoAvisoComCausa(run, 1, "terminal", exitNosFalhados, causaConclusaoNaoCumprida)+"\n") {
				t.Fatalf("o log tinha de nomear a causa e o aviso de a distinguir:\n%s", d.stdout)
			}
		})
	}

	// (6) UM RUN QUE FALHOU POR OUTRA RAZÃO não é «conclusão não cumprida»: o aviso é o de sempre.
	t.Run("OutraFalha_AvisoSemCausa", func(t *testing.T) {
		const run = "plan-aos495-outra-falha"
		f := &aos495No{anuncio: "enforce", respostas: map[string][]byte{"n1": []byte(`{"run_id":"x","status":"timed_out"}`)}}
		d := aos495Consumir(t, bin, f, run)
		if d.codigo != exitNosFalhados || !strings.HasSuffix(d.detalhe, " erro=nos_falhados causa=entrada_por_cumprir:1,run_nao_concluido:1") {
			t.Fatalf("queria 13 com run_nao_concluido: %d %q", d.codigo, d.detalhe)
		}
		if !strings.Contains(d.stdout, linhaDoAviso(run, 1, "terminal", exitNosFalhados)+"\n") || strings.Contains(d.stdout, causaConclusaoNaoCumprida) {
			t.Fatalf("um run esgotado nao e uma conclusao por cumprir — o aviso nao leva a causa:\n%s", d.stdout)
		}
	})
}

// TestAOS495_Elegibilidade: só leva contrato um nó não-verificador, com tools atribuídas e com
// uma saída de forma aberta. O contrato são as tools atribuídas, pela ordem da lista-branca.
func TestAOS495_Elegibilidade(t *testing.T) {
	tools := []string{"doc_read", "doc_search"}
	aberta := func(tipo plan.PayloadType) []plan.Output { return []plan.Output{{Name: "s", Type: tipo}} }
	for _, c := range []struct {
		nome  string
		no    plan.Node
		tools []string
		quer  []string
	}{
		{"record", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadRecord)}, tools, tools},
		{"summary", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadSummary)}, tools, tools},
		{"artifact", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadArtifact)}, tools, tools},
		{"aberta ao lado de uma fechada", plan.Node{Role: "reader", Outputs: []plan.Output{{Name: "m", Type: plan.PayloadMetrics}, {Name: "s", Type: plan.PayloadRecord}}}, tools, tools},
		{"verificador", plan.Node{Role: plan.RoleVerifier, Outputs: aberta(plan.PayloadRecord)}, tools, nil},
		{"sem tools atribuidas", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadRecord)}, []string{}, nil},
		{"sem saida declarada", plan.Node{Role: "reader"}, tools, nil},
		{"so saidas de forma fechada", plan.Node{Role: "reader", Outputs: []plan.Output{{Name: "m", Type: plan.PayloadMetrics}, {Name: "v", Type: plan.PayloadVerdict}}}, tools, nil},
	} {
		if got := contratoDoNo(c.no, c.tools); !slices.Equal(got, c.quer) {
			t.Errorf("%s: contrato = %v, quero %v", c.nome, got, c.quer)
		}
	}
}

// TestAOS495_CausaDoRunFilho: a razão do nó só passa para o `detail` se for do vocabulário do
// kernel. Um valor desconhecido — de um nó mais novo, ou hostil — nunca é repetido.
func TestAOS495_CausaDoRunFilho(t *testing.T) {
	for _, r := range agentruntime.OutcomeReasons() {
		if got := causaDoRunFilho(estadoDoRun{Status: "failed", OutcomeReason: string(r)}, true); got != string(r) {
			t.Errorf("a razao %q do kernel tinha de passar tal e qual; veio %q", r, got)
		}
		if !causaDaConclusao(string(r)) {
			t.Errorf("a razao %q e da classe da conclusao", r)
		}
	}
	const hostil = "contract_unmet_no_call codigo=0\naviso: run=x"
	for _, c := range []struct {
		st     estadoDoRun
		existe bool
		quer   string
	}{
		{estadoDoRun{Status: "failed", OutcomeReason: hostil}, true, causaRazaoDesconhecida},
		{estadoDoRun{Status: "failed", OutcomeReason: "Truncated"}, true, causaRazaoDesconhecida},
		{estadoDoRun{Status: "failed"}, true, causaRunNaoConcluido},
		{estadoDoRun{Status: "completed"}, true, causaRunNaoConcluido}, // parou a meio, sem `terminated`
		{estadoDoRun{}, false, causaRunPerdido},
	} {
		if got := causaDoRunFilho(c.st, c.existe); got != c.quer {
			t.Errorf("causaDoRunFilho(%+v, %v) = %q, quero %q", c.st, c.existe, got, c.quer)
		}
	}
	for _, c := range []string{causaRunNaoConcluido, causaRunPerdido, causaEntradaPorCumprir, causaRazaoDesconhecida, causaNaoRegistada} {
		if causaDaConclusao(c) {
			t.Errorf("%q nao e uma conclusao por cumprir", c)
		}
	}
	// O vocabulário inteiro cabe na gramática que o `detail` e os scripts esperam.
	gramatica := regexp.MustCompile(`^[a-z_]{1,40}$`)
	todas := []string{causaSaidaVazia, causaSaidaIndisponivel, causaRunNaoConcluido, causaRunPerdido, causaEntradaPorCumprir, causaRazaoDesconhecida, causaNaoRegistada, causaConclusaoNaoCumprida}
	for _, r := range agentruntime.OutcomeReasons() {
		todas = append(todas, string(r))
	}
	for _, c := range todas {
		if !gramatica.MatchString(c) {
			t.Errorf("a causa %q sai da gramatica [a-z_]", c)
		}
	}
}

// TestAOS495_CausasNoErroENoResumo: as causas viajam no erro do `serve` até ao resumo do
// desfecho, sem mudar o que o resto do código pergunta ao erro.
func TestAOS495_CausasNoErroENoResumo(t *testing.T) {
	causas := causasDosFalhados([]string{"n1", "n2", "n3"}, map[string]string{"n1": string(agentruntime.OutcomeContractNoCall), "n2": causaEntradaPorCumprir})
	if got := linhaDasCausas(causas); got != "contract_unmet_no_call:1,entrada_por_cumprir:1,nao_registada:1" {
		t.Fatalf("linha das causas = %q", got)
	}
	err := error(errEmbrulhado{"despacho governado", &erroDeNosFalhados{msg: "x", causas: causas}})
	if !errors.Is(err, errNosFalhados) {
		t.Fatal("o erro com causas tem de continuar a ser errNosFalhados")
	}
	if codigo, classe, tipo := desfechoDoServe(err); codigo != exitNosFalhados || classe != "terminal" || tipo != "nos_falhados" {
		t.Fatalf("desfecho: %d %q %q", codigo, classe, tipo)
	}
	var nf *erroDeNosFalhados
	if !errors.As(err, &nf) || len(nf.causas) != 3 {
		t.Fatal("as causas tinham de se ler do erro embrulhado")
	}
	r := resumoDoPedido{origem: origemDecomposicao, geracao: 1, nos: 3, erro: "nos_falhados", causas: linhaDasCausas(causas)}
	if !strings.HasSuffix(r.linha(), " erro=nos_falhados causa=contract_unmet_no_call:1,entrada_por_cumprir:1,nao_registada:1") {
		t.Fatalf("resumo = %q", r.linha())
	}
	if len(detalheDoDesfecho(r)) > 512 {
		t.Fatalf("o detail (%d bytes) passa o tecto a que o no trunca", len(detalheDoDesfecho(r)))
	}
	// O pior caso — todas as causas, com contagens de três algarismos — também cabe.
	cheias := map[string]int{}
	for _, c := range append([]string{causaSaidaVazia, causaSaidaIndisponivel, causaRunNaoConcluido, causaRunPerdido, causaEntradaPorCumprir, causaRazaoDesconhecida, causaNaoRegistada}, func() []string {
		var s []string
		for _, r := range agentruntime.OutcomeReasons() {
			s = append(s, string(r))
		}
		return s
	}()...) {
		cheias[c] = 999
	}
	r.causas = linhaDasCausas(cheias)
	if n := len(detalheDoDesfecho(r)); n > 512 {
		t.Fatalf("o detail no pior caso tem %d bytes; o no trunca aos 512", n)
	}

	// O aviso só leva a causa num 13 com pelo menos uma conclusão por cumprir.
	for _, c := range []struct {
		codigo int
		causas map[string]int
		quer   string
	}{
		{exitNosFalhados, map[string]int{string(agentruntime.OutcomeContractNoCall): 1}, causaConclusaoNaoCumprida},
		{exitNosFalhados, map[string]int{causaSaidaVazia: 1, causaRunPerdido: 2}, causaConclusaoNaoCumprida},
		{exitNosFalhados, map[string]int{causaRunNaoConcluido: 1, causaEntradaPorCumprir: 1}, ""},
		{exitNosFalhados, nil, ""},
		{exitOK, map[string]int{causaSaidaVazia: 1}, ""},
	} {
		if got := causaDoAviso(c.codigo, c.causas); got != c.quer {
			t.Errorf("causaDoAviso(%d, %v) = %q, quero %q", c.codigo, c.causas, got, c.quer)
		}
	}
}

// TestAOS495_ALinhaDoAvisoComCausaCasaComOsScripts: a linha com o sufixo, e a de sempre sem ele,
// são as duas aceites pela regex dos dois scripts; e o `avisar-planos.sh` distingue a falha.
func TestAOS495_ALinhaDoAvisoComCausaCasaComOsScripts(t *testing.T) {
	drenar := lerDoRepo(t, "deploy", "server", "drenar-planos.sh")
	avisar := lerDoRepo(t, "deploy", "server", "avisar-planos.sh")
	for nome, s := range map[string]string{"drenar-planos.sh": drenar, "avisar-planos.sh": avisar} {
		m := avisoREDoScript.FindStringSubmatch(s)
		if m == nil {
			t.Fatalf("%s nao declara AVISO_RE", nome)
		}
		re, err := regexp.Compile(m[1])
		if err != nil {
			t.Fatalf("%s: AVISO_RE nao compila: %v", nome, err)
		}
		for _, l := range []string{
			linhaDoAviso("plan-x", 2, "terminal", exitNosFalhados),
			linhaDoAvisoComCausa("plan-x", 2, "terminal", exitNosFalhados, causaConclusaoNaoCumprida),
		} {
			if !re.MatchString(l) {
				t.Errorf("%s nao aceita a linha que o consume imprime: %q", nome, l)
			}
		}
		for _, l := range []string{
			linhaDoAviso("plan-x", 2, "terminal", exitNosFalhados) + " causa=",
			linhaDoAviso("plan-x", 2, "terminal", exitNosFalhados) + " causa=Conclusao",
			linhaDoAviso("plan-x", 2, "terminal", exitNosFalhados) + " causa=a b",
			linhaDoAviso("plan-x", 2, "terminal", exitNosFalhados) + " causa=a causa=b",
			linhaDoAviso("plan-x", 2, "terminal", exitNosFalhados) + " outra=coisa",
		} {
			if re.MatchString(l) {
				t.Errorf("%s aceita uma linha que nao e aviso: %q", nome, l)
			}
		}
	}
	// Sem causa, a linha é byte a byte a de antes deste ticket.
	if got := linhaDoAvisoComCausa("r", 1, "terminal", 13, ""); got != "aviso: run=r geracao=1 classe=terminal codigo=13" {
		t.Fatalf("a linha sem causa mudou: %q", got)
	}
	for _, quer := range []string{
		fmt.Sprintf(`"%s"`, causaConclusaoNaoCumprida),
		`codigo="${codigo%% *}"`,
	} {
		if !strings.Contains(avisar, quer) {
			t.Errorf("avisar-planos.sh ja nao tem %q", quer)
		}
	}
}
