package main

// AOS-495 — O `aos-orq` DECLARA O CONTRATO DE CONCLUSÃO E NÃO PUBLICA SAÍDAS SEM EVIDÊNCIA.
//
// Os testes de processo correm o `aos-orq consume` REAL contra um nó `aos` falso. O que o nó
// falso responde no `GET /runs/{id}` não é inventado aqui: são os ficheiros
// `packages/cmd/aos/testdata/aos494_fio/`, que o teste do nó (`TestAOS494_Fio_…`) exige que o
// nó REAL responda, byte a byte, às duas respostas de produção de 2026-10-04 e à resposta boa,
// com o modelo falso. Este módulo não pode importar o pacote do nó; é por esses ficheiros que
// os dois lados do fio ficam presos.
//
// AS TRÊS DIRECÇÕES DO FIO (revisão adversarial, I5). Cada ficheiro é GERADO por um lado e
// CONSUMIDO pelo outro, e regenera-se só com `AOS494_ACTUALIZAR_FIO=1`:
//
//   - o corpo do `POST /runs`: gerado AQUI (`testdata/aos495_fio/post-runs-read_notes.json`, o
//     que o `aos-orq consume` real enviou), consumido pelo teste do nó (`TestAOS494_Fio_PostRunsDoAosOrq`);
//   - o anúncio do `GET /tools`: gerado pelo nó (`tools-<modo>.json`), consumido aqui;
//   - o `GET /runs/{id}` — em memória, do ramo durável e o `output_unavailable`: gerados pelo
//     nó, consumidos aqui.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	// respostasSemContrato é o corpo do `GET /runs/{id}` de um nó do plano cujo `POST /runs` veio
	// SEM `completion_requires`. Com as duas, o nó falso porta-se como um nó em `enforce`: julga
	// quem lhe declarou um contrato, e conclui quem não declarou.
	respostasSemContrato map[string][]byte
	// catalogo, se dado, é o corpo INTEIRO do `GET /tools` — um ficheiro do fio gerado pelo nó
	// real, com o anúncio lá dentro. Sem ele, o catálogo é o dos outros testes e o anúncio é o
	// campo `anuncio`.
	catalogo []byte
	// toolsNaLeitura decide a n-ésima leitura do `GET /tools` (a contar de 1): status diferente
	// de 0 ⇒ responde esse status com esse corpo, em vez do catálogo.
	toolsNaLeitura func(n int) (status int, corpo []byte)
	// ilegivelAntes é quantas vezes o `GET /runs/{id}` de um nó do plano responde 503 antes de
	// responder o desfecho.
	ilegivelAntes map[string]int

	mu          sync.Mutex
	ofertas     []pedidoReclamado
	desfechos   []map[string]any
	corpos      map[string]map[string]any
	crus        map[string][]byte
	recusados   int
	toolsLidas  int
	estados503  int
	submissoes  int
	comContrato map[string]bool
}

func (f *aos495No) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	f.corpos = map[string]map[string]any{}
	f.crus = map[string][]byte{}
	f.comContrato = map[string]bool{}
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
		cru, _ := io.ReadAll(r.Body)
		var corpo map[string]any
		_ = json.Unmarshal(cru, &corpo)
		id, _ := corpo["run_id"].(string)
		f.mu.Lock()
		f.submissoes++
		f.mu.Unlock()
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
		f.crus[id] = cru
		_, f.comContrato[id] = corpo["completion_requires"]
		f.mu.Unlock()
		if repetida {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		no := id[strings.LastIndex(id, "~")+1:]
		f.mu.Lock()
		_, existe := f.corpos[id]
		comContrato := f.comContrato[id]
		ilegivel := existe && f.ilegivelAntes[no] > 0
		if ilegivel {
			f.ilegivelAntes[no]--
			f.estados503++
		}
		f.mu.Unlock()
		if !existe {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ilegivel {
			// O que o nó real responde com a custódia das KEK fechada (AOS-494).
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"indisponivel"}`))
			return
		}
		if cru, ok := f.respostasSemContrato[no]; ok && !comContrato {
			_, _ = w.Write(cru)
			return
		}
		if cru, ok := f.respostas[no]; ok {
			_, _ = w.Write(cru)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "completed", "terminated": true, "final_text": "feito: " + id})
	})
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.toolsLidas++
		n := f.toolsLidas
		f.mu.Unlock()
		if f.toolsNaLeitura != nil {
			if status, corpo := f.toolsNaLeitura(n); status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write(corpo)
				return
			}
		}
		if f.catalogo != nil {
			_, _ = w.Write(f.catalogo)
			return
		}
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
	stderr  string
	// metricas é o ficheiro de métricas que a drenagem escreveu ao lado do WAL.
	metricas string
}

// aos495Consumir oferece um pedido de plano e corre UMA drenagem do `aos-orq consume` real, com
// o plano de leitura e resumo do AOS-484 a fazer de decomposição.
func aos495Consumir(t *testing.T, bin string, f *aos495No, run string) aos495Desfecho {
	t.Helper()
	return aos495ConsumirCom(t, bin, f, run, aos484PlanoLerEResumir, aos408SnapshotComPerigo)
}

// aos495ConsumirCom é o [aos495Consumir] com o plano e o snapshot dados.
func aos495ConsumirCom(t *testing.T, bin string, f *aos495No, run, plano, snapshot string) aos495Desfecho {
	t.Helper()
	srv := f.servidor(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, snapshot)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, plano)
	f.mu.Lock()
	f.ofertas = append(f.ofertas, pedidoReclamado{RunID: run, Objective: "ler o documento notes e resumi-lo", Geracao: 1})
	f.mu.Unlock()

	env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	r := correrComEnv(t, env, bin, "consume", "--wal", filepath.Join(dir, "consume.wal"), "--snapshot", snap,
		"--decompose-fixture", fix, "--poll-interval", "20ms", "--max", "1", "--plan-timeout", "30s")
	if r.code != exitOK {
		t.Fatalf("o consume em si tinha de sair 0 (o desfecho vai para o no), saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	metricas, err := os.ReadFile(filepath.Join(dir, nomeDoFicheiroDeMetricas))
	if err != nil {
		t.Fatalf("a drenagem tinha de escrever o ficheiro de metricas: %v", err)
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
	return aos495Desfecho{codigo: int(codigo), classe: classe, detalhe: detalhe, stdout: r.stdout, stderr: r.stderr, metricas: string(metricas)}
}

// temSerie diz se o ficheiro de métricas tem a série com esse valor, numa linha inteira.
func temSerie(metricas, chave string, valor int) bool {
	return strings.Contains("\n"+metricas, fmt.Sprintf("\n%s %d\n", chave, valor))
}

// ── A FORMA DE PRODUÇÃO ────────────────────────────────────────────────────────────────────────
//
// O nó do plano chama-se `read_notes`, a tool `doc_read`, a saída é um `record`. O catálogo e o
// anúncio são os que o NÓ REAL responde (ficheiro do fio), e o snapshot e o plano derivam dele —
// o digest é o do contrato que o nó oferece, não um inventado.

// aos495Producao é o ambiente de um plano na forma de produção, contra um nó no modo dado.
type aos495Producao struct {
	catalogo []byte // o `GET /tools` do nó real, com o anúncio
	snapshot string
	plano    string
}

func aos495FormaDeProducao(t *testing.T, modo string) aos495Producao {
	t.Helper()
	cru := aos495Fio(t, "tools-"+modo)
	var corpo struct {
		Tools []toolDoNo `json:"tools"`
	}
	if err := json.Unmarshal(cru, &corpo); err != nil || len(corpo.Tools) != 1 || corpo.Tools[0].Name != "doc_read" {
		t.Fatalf("o anuncio do fio tem de trazer a tool doc_read: err=%v tools=%+v", err, corpo.Tools)
	}
	tool := corpo.Tools[0]
	snapshot := fmt.Sprintf(`{"hash":"sha256:snap-aos408","tools":[{"name":%q,"version":%q,"digest":%q,"admissible":true,"sensitivity":"public","egress":%q,"reversibility":%q,"mutation":%q}]}`,
		tool.Name, tool.Version, tool.Digest, tool.Egress, tool.Reversibility, tool.Mutation)
	plano := aos484PlanoLerEResumir
	for _, troca := range [][2]string{
		{`{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}`, fmt.Sprintf(`{"name":%q,"version":%q,"digest":%q}`, tool.Name, tool.Version, tool.Digest)},
		{`"n1"`, `"read_notes"`},
		{`"n2"`, `"summarize"`},
	} {
		if !strings.Contains(plano, troca[0]) {
			t.Fatalf("pre-condicao: o plano do AOS-484 ja nao tem %s", troca[0])
		}
		plano = strings.ReplaceAll(plano, troca[0], troca[1])
	}
	return aos495Producao{catalogo: bytes.TrimSpace(cru), snapshot: snapshot, plano: plano}
}

// semOutputs devolve o plano de produção SEM `outputs` no leitor e sem `consumes` no resumidor:
// o que o planeador escreve quando nenhum nó consome a saída de outro (regras 7 e 12 do prompt).
func (p aos495Producao) semOutputs(t *testing.T) string {
	t.Helper()
	plano := strings.Replace(p.plano, `,
     "outputs":[{"name":"conteudo","type":"record","taint":"untrusted"}]`, "", 1)
	plano = strings.Replace(plano, strings.ReplaceAll(aos484ConsumesDoN2, `"n1"`, `"read_notes"`), "", 1)
	if strings.Contains(plano, "outputs") || strings.Contains(plano, "consumes") {
		t.Fatalf("pre-condicao: o plano ainda declara outputs ou consumes:\n%s", plano)
	}
	return plano
}

// aos495FioDoPost compara o corpo do `POST /runs` que o `aos-orq` enviou com o ficheiro do fio
// que o teste do NÓ consome. `AOS494_ACTUALIZAR_FIO=1` reescreve-o.
func aos495FioDoPost(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos495_fio", nome+".json")
	cru = bytes.TrimSpace(cru)
	if os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" {
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
		t.Fatalf("o POST /runs deixou de levar o que o no aceita nos testes dele (%s):\n  fio:   %s\n  agora: %s", caminho, bytes.TrimSpace(quer), cru)
	}
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
				"execucao: no n1 contrato de conclusao: classe=com_contrato_saida_aberta tools=fs.read\n",
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
				"execucao: no n1 contrato de conclusao: classe=com_contrato_saida_aberta tools=fs.read\n",
				"execucao: no n2 contrato de conclusao: classe=sem_contrato_sem_tools — NAO leva contrato\n",
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
			// M6: a medição de `observe` está no ficheiro de métricas, e não só no log.
			for chave, valor := range map[string]int{
				serie(metricaVeredictosObservados, "razao", string(agentruntime.OutcomeContractNoCall)): 1,
				serie(metricaNosPorContrato, "classe", classeComContratoSaidaAberta):                    1,
				serie(metricaNosPorContrato, "classe", classeSemContratoSemTools):                       1,
			} {
				if !temSerie(d.metricas, chave, valor) {
					t.Fatalf("faltou nas metricas %s %d:\n%s", chave, valor, d.metricas)
				}
			}
			if strings.Contains(d.metricas, metricaContratoNaoAplicado) {
				t.Fatalf("o contrato foi aplicado: a serie %s nao tinha de existir:\n%s", metricaContratoNaoAplicado, d.metricas)
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
			"execucao: no n1 contrato de conclusao: classe=sem_contrato_no_nao_anuncia — NAO leva contrato\n",
		} {
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("o log tinha de dizer que o contrato nao foi aplicado (%q):\n%s", quer, d.stdout)
			}
		}
		// M6: e conta-se — a execução sem contrato, e o nó que ficou sem ele por essa razão.
		for chave, valor := range map[string]int{
			serie(metricaContratoNaoAplicado, "motivo", motivoNoNaoAnuncia):       1,
			serie(metricaNosPorContrato, "classe", classeSemContratoNaoAnunciado): 1,
			serie(metricaNosPorContrato, "classe", classeSemContratoSemTools):     1,
		} {
			if !temSerie(d.metricas, chave, valor) {
				t.Fatalf("faltou nas metricas %s %d:\n%s", chave, valor, d.metricas)
			}
		}
	})

	// (4b) O ANÚNCIO NÃO SE LEU. Não é o nó a dizer que não: o plano NÃO corre sem contrato. O
	// desfecho é transitório, o pedido volta à fila, e nada foi submetido. As duas primeiras
	// leituras do `GET /tools` são a conferência do snapshot (a do `consume` e a do `serve`); a
	// terceira é a do anúncio.
	for _, c := range []struct {
		nome   string
		status int
		corpo  string
	}{
		{"503", http.StatusServiceUnavailable, `{"error":"indisponivel"}`},
		{"429", http.StatusTooManyRequests, `{"error":"rate limit excedido"}`},
		{"CorpoIlegivel", http.StatusOK, `{"tools":[],"completion_contract":"enforce"}`},
	} {
		t.Run("AnuncioIlegivel_PedidoVoltaAFila/"+c.nome, func(t *testing.T) {
			const run = "plan-aos495-anuncio"
			// Um nó em `enforce`: julga quem declara o contrato, e conclui quem não declara. Se o
			// `aos-orq` seguisse sem contrato, o plano saía 0 — o verde falso.
			f := &aos495No{anuncio: "enforce",
				respostas:            map[string][]byte{"n1": aos495Fio(t, aos495Planos[0]+"-enforce")},
				respostasSemContrato: map[string][]byte{"n1": []byte(`{"run_id":"x","status":"completed","terminated":true,"final_text":"<tool_call>{\"name\":\"fs.read\"}</tool_call>"}`)},
				toolsNaLeitura: func(n int) (int, []byte) {
					if n >= 3 {
						return c.status, []byte(c.corpo)
					}
					return 0, nil
				}}
			d := aos495Consumir(t, bin, f, run)
			if d.classe != "transitorio" || d.codigo != exitErro || !strings.HasSuffix(d.detalhe, " erro=anuncio_ilegivel") {
				t.Fatalf("um anuncio que nao se leu da um desfecho transitorio/1 com erro=anuncio_ilegivel; veio %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			if f.submissoes != 0 {
				t.Fatalf("o plano nao corre sem saber se o no aceita o contrato: houve %d submissao(oes)\n%s", f.submissoes, d.stdout)
			}
			if f.toolsLidas != 3 {
				t.Fatalf("pre-condicao: a leitura que falhou tinha de ser a do anuncio (a 3.a); houve %d", f.toolsLidas)
			}
			for _, proibido := range []string{"contrato de conclusao (AOS-495)", prefixoDoAviso, "posse: run="} {
				if strings.Contains(d.stdout, proibido) {
					t.Fatalf("com o anuncio por ler nao ha banner do contrato, nem aviso, nem posse (%q):\n%s", proibido, d.stdout)
				}
			}
			if !temSerie(d.metricas, serie(metricaContratoNaoAplicado, "motivo", motivoAnuncioIlegivel), 1) ||
				!temSerie(d.metricas, serie(metricaDesfechos, "classe", "transitorio", "codigo", "1"), 1) ||
				strings.Contains(d.metricas, metricaNosPorContrato) {
				t.Fatalf("as metricas tinham de contar o anuncio ilegivel e nenhum no submetido:\n%s", d.metricas)
			}
		})
	}

	// (5) UMA SAÍDA VAZIA NUNCA É PUBLICADA: o produtor fica `failed`, com razão própria.
	for _, c := range []struct {
		nome, resposta, causa string
	}{
		{"SemTexto", `{"run_id":"x","status":"completed","terminated":true}`, causaSaidaVazia},
		{"SoEspacos", `{"run_id":"x","status":"completed","terminated":true,"final_text":"  \n\t"}`, causaSaidaVazia},
		// O ramo durável do nó REAL depois de um reinício, com o titular apagado (AOS-494): os
		// bytes são os do fio.
		{"SaidaIndisponivelNoNo", string(aos495Fio(t, "duravel-saida-indisponivel")), causaSaidaIndisponivel},
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

	// (5b) O RAMO DURÁVEL DO NÓ REAL, depois de um reinício: a saída vem do log, e publica-se.
	t.Run("RamoDuravel_PublicaASaidaDoLog", func(t *testing.T) {
		const run = "plan-aos495-duravel"
		fio := aos495Fio(t, "duravel-boa-enforce")
		f := &aos495No{anuncio: "enforce", respostas: map[string][]byte{"n1": fio}}
		d := aos495Consumir(t, bin, f, run)
		var st estadoDoRun
		if err := json.Unmarshal(fio, &st); err != nil || st.FinalText == "" || st.OutputUnavailable || !st.concluiu() {
			t.Fatalf("pre-condicao: o fio do ramo duravel traz o run concluido com a saida (%+v, %v)", st, err)
		}
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("a resposta do ramo duravel conclui o no; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
		}
		entradas, _ := f.corpo(run, "n2")["inputs"].([]any)
		if len(entradas) != 1 || entradas[0].(map[string]any)["content"] != st.FinalText {
			t.Fatalf("o consumidor tinha de receber a saida lida do log: %v", entradas)
		}
	})

	// (5c) O NÓ RESPONDE 503 AO `GET /runs/{id}` — a custódia das KEK fechada, depois de um
	// reinício (AOS-494). Não é um desfecho: o `aos-orq` não fecha o nó do plano, volta a ler, e
	// quando a custódia volta publica a saída.
	t.Run("Estado503_NaoFechaONo", func(t *testing.T) {
		const run = "plan-aos495-503"
		f := &aos495No{anuncio: "enforce", ilegivelAntes: map[string]int{"n1": 3},
			respostas: map[string][]byte{"n1": aos495Fio(t, "duravel-boa-enforce")}}
		d := aos495Consumir(t, bin, f, run)
		if f.estados503 != 3 {
			t.Fatalf("pre-condicao: o no tinha de responder 503 tres vezes; respondeu %d", f.estados503)
		}
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("um 503 no estado do run nao e um desfecho: o plano tinha de sair terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		for _, quer := range []string{
			"execucao: estado de n1 ilegivel nesta passagem: ",
			"execucao: payload n1/conteudo publicado (record)",
			"execucao: n1=complete n2=complete",
		} {
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
			}
		}
		if strings.Contains(d.stdout, "n1 failed") || strings.Contains(d.stdout, causaSaidaIndisponivel) {
			t.Fatalf("o no do plano nao pode fechar failed por um 503:\n%s", d.stdout)
		}
	})

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

	// (7) A FORMA DE PRODUÇÃO: nó `read_notes`, tool `doc_read`, saída `record`. O anúncio e o
	// catálogo são os bytes do nó real; as respostas do `GET /runs/{id}` são as que o nó real deu
	// ao corpo que ESTE binário enviou (`TestAOS494_Fio_PostRunsDoAosOrq`).
	t.Run("FormaDeProducao", func(t *testing.T) {
		const run = "plan-aos495-fio"
		const vectorSemChamada = "vector [doc_read pedidas=0 efectivas=0 negadas=0 falhadas=0]"

		t.Run("Enforce_Sai13EOCorpoEODoFio", func(t *testing.T) {
			p := aos495FormaDeProducao(t, "enforce")
			// A resposta do no a ESTE corpo so existe depois de o teste do no o consumir. Na
			// primeira geracao do fio ainda nao ha: grava-se o corpo, e o resto fica para a volta.
			resposta, haResposta := []byte(`{"run_id":"x","status":"failed"}`), false
			if _, err := os.Stat(filepath.Join("..", "aos", "testdata", "aos494_fio", "producao-enforce.json")); err == nil || os.Getenv("AOS494_ACTUALIZAR_FIO") != "1" {
				resposta, haResposta = aos495Fio(t, "producao-enforce"), true
			}
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": resposta}}
			d := aos495ConsumirCom(t, bin, f, run, p.plano, p.snapshot)
			// O CORPO DO `POST /runs`, byte a byte: é o ficheiro que o teste do nó consome.
			aos495FioDoPost(t, "post-runs-read_notes", f.crus[run+"~read_notes"])
			if !haResposta {
				t.Skip("o corpo do POST /runs ficou gravado; corre agora o teste do no com AOS494_ACTUALIZAR_FIO=1 (gera a resposta a este corpo), e este outra vez")
			}
			corpo := f.corpo(run, "read_notes")
			tools, _ := corpo["tools"].([]any)
			if c, ok := contratoEnviado(corpo); len(tools) != 1 || tools[0] != "doc_read" || !ok || !slices.Equal(c, []string{"doc_read"}) {
				t.Fatalf("a forma de producao: tools=%v completion_requires=%v (presente=%v)", tools, c, ok)
			}
			if d.classe != "terminal" || d.codigo != exitNosFalhados || !strings.HasSuffix(d.detalhe, " erro=nos_falhados causa=contract_unmet_no_call:1,entrada_por_cumprir:1") {
				t.Fatalf("a resposta de producao em enforce sai terminal/13 com a razao; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			for _, quer := range []string{
				`contrato de conclusao (AOS-495): DECLARADO`, `modo "enforce"`,
				"execucao: no read_notes contrato de conclusao: classe=com_contrato_saida_aberta tools=doc_read\n",
				// O vector nomeia a tool do contrato: o nome é o que este processo enviou.
				"execucao: no read_notes failed (run " + run + "~read_notes) causa=contract_unmet_no_call " + vectorSemChamada + "\n",
				linhaDoAvisoComCausa(run, 1, "terminal", exitNosFalhados, causaConclusaoNaoCumprida) + "\n",
			} {
				if !strings.Contains(d.stdout, quer) {
					t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
				}
			}
		})

		t.Run("Observe_Sai0ERegistaOVeredicto", func(t *testing.T) {
			p := aos495FormaDeProducao(t, "observe")
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": aos495Fio(t, "producao-observe")}}
			d := aos495ConsumirCom(t, bin, f, run, p.plano, p.snapshot)
			if d.classe != "terminal" || d.codigo != exitOK {
				t.Fatalf("em observacao o plano sai terminal/0; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
			}
			for _, quer := range []string{
				`modo "observe"`,
				"execucao: no read_notes VEREDICTO OBSERVADO contract_unmet_no_call (modo observe), " + vectorSemChamada + " — ",
				"execucao: payload read_notes/conteudo publicado (record)",
			} {
				if !strings.Contains(d.stdout, quer) {
					t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
				}
			}
			if !temSerie(d.metricas, serie(metricaVeredictosObservados, "razao", string(agentruntime.OutcomeContractNoCall)), 1) {
				t.Fatalf("o veredicto observado tinha de ficar nas metricas:\n%s", d.metricas)
			}
		})

		t.Run("Boa_Sai0", func(t *testing.T) {
			p := aos495FormaDeProducao(t, "enforce")
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": aos495Fio(t, "producao-boa-enforce")}}
			d := aos495ConsumirCom(t, bin, f, run, p.plano, p.snapshot)
			if d.classe != "terminal" || d.codigo != exitOK || strings.Contains(d.stdout, "VEREDICTO OBSERVADO") || strings.Contains(d.stdout, " causa=") {
				t.Fatalf("a resposta boa sai terminal/0, sem causa nem veredicto observado; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
			}
		})

		// (8) I1 — UM NÓ COM TOOLS E SEM `outputs` LEVA CONTRATO. É o plano de um só nó, o último
		// nó, o nó de escrita: o planeador não lhes declara saída. Ficavam sem contrato, e a
		// resposta de produção dava plano 0 com o nó em `enforce`.
		t.Run("NoComToolsSemOutputs_Enforce_Sai13", func(t *testing.T) {
			p := aos495FormaDeProducao(t, "enforce")
			f := &aos495No{catalogo: p.catalogo,
				respostas:            map[string][]byte{"read_notes": aos495Fio(t, "producao-enforce")},
				respostasSemContrato: map[string][]byte{"read_notes": []byte(`{"run_id":"x","status":"completed","terminated":true,"final_text":"<tool_call name=\"doc_read\">notes</tool_call>"}`)}}
			d := aos495ConsumirCom(t, bin, f, run, p.semOutputs(t), p.snapshot)
			if c, ok := contratoEnviado(f.corpo(run, "read_notes")); !ok || !slices.Equal(c, []string{"doc_read"}) {
				t.Fatalf("um no com tools e sem outputs leva o contrato [doc_read]; levou %v (presente=%v)\n%s", c, ok, d.stdout)
			}
			if d.classe != "terminal" || d.codigo != exitNosFalhados || !strings.HasSuffix(d.detalhe, " erro=nos_falhados causa=contract_unmet_no_call:1") {
				t.Fatalf("o plano tinha de sair terminal/13 com a razao; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			for _, quer := range []string{
				"execucao: no read_notes contrato de conclusao: classe=com_contrato_sem_saida tools=doc_read\n",
				"execucao: no read_notes failed (run " + run + "~read_notes) causa=contract_unmet_no_call " + vectorSemChamada + "\n",
			} {
				if !strings.Contains(d.stdout, quer) {
					t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
				}
			}
			if f.corpo(run, "summarize") != nil {
				t.Fatalf("o no seguinte nao corre depois de um no failed:\n%s", d.stdout)
			}
			if !temSerie(d.metricas, serie(metricaNosPorContrato, "classe", classeComContratoSemSaida), 1) {
				t.Fatalf("a classe alargada tinha de se contar nas metricas:\n%s", d.metricas)
			}
		})

		// (9) M8 — UM NÓ SEM SAÍDA ABERTA PODE CONCLUIR SEM TEXTO. A regra «saída vazia não se
		// publica» é de quem declara uma saída de forma aberta; um nó que não declara nenhuma
		// não tem nada para publicar, e o que diz se trabalhou é o contrato.
		t.Run("NoSemSaidaAberta_ConcluiSemTexto", func(t *testing.T) {
			p := aos495FormaDeProducao(t, "enforce")
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{
				"read_notes": []byte(`{"run_id":"x","status":"completed","terminated":true,"verdict":{"mode":"enforce","fulfilled":true,"tools":[{"tool":"doc_read","requested":1,"effective":1,"denied":0,"failed":0,"last":"effective"}],"tool_calls_requested":1}}`),
				"summarize":  []byte(`{"run_id":"x","status":"completed","terminated":true,"final_text":"   "}`),
			}}
			d := aos495ConsumirCom(t, bin, f, run, p.semOutputs(t), p.snapshot)
			if d.classe != "terminal" || d.codigo != exitOK {
				t.Fatalf("um no sem saida aberta conclui sem texto, e o plano sai terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			if !strings.Contains(d.stdout, "execucao: read_notes=complete summarize=complete") || strings.Contains(d.stdout, causaSaidaVazia) {
				t.Fatalf("os dois nos tinham de concluir, sem saida_vazia:\n%s", d.stdout)
			}
		})
	})
}

// TestAOS495_Anuncio_OClienteLeOFioDoNo: o cliente do `aos-orq` lê o `GET /tools` que o NÓ REAL
// responde (ficheiro do fio) — o anúncio, com o modo, e o catálogo, pela mesma leitura. E separa
// as duas respostas que não se confundem: o nó que responde sem o anúncio, e o anúncio que não
// se leu.
func TestAOS495_Anuncio_OClienteLeOFioDoNo(t *testing.T) {
	servir := func(status int, corpo []byte) *nodeClient {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write(corpo)
		}))
		t.Cleanup(srv.Close)
		return &nodeClient{base: srv.URL, http: &http.Client{Timeout: 5 * time.Second}}
	}
	ctx := context.Background()

	for _, modo := range []string{"enforce", "observe"} {
		cli := servir(http.StatusOK, aos495Fio(t, "tools-"+modo))
		a, err := cli.ContratoDeConclusao(ctx)
		if err != nil || !a.aceita || a.modo != modo {
			t.Fatalf("o anuncio do no real em %s: aceita=%v modo=%q err=%v", modo, a.aceita, a.modo, err)
		}
		cat, err := cli.CatalogoDeTools(ctx)
		if err != nil || len(cat) != 1 || cat[0].Name != "doc_read" || !strings.HasPrefix(cat[0].Digest, "sha256:") {
			t.Fatalf("o catalogo do no real: %+v err=%v", cat, err)
		}
	}

	// (a) O nó RESPONDEU, e não anuncia: sem erro, e não aceita.
	for nome, corpo := range map[string]string{
		"sem o campo": aos441CatalogoDoSnapshotComPerigo,
		"campo null":  `{"tools":[],"completion_contract":null}`,
	} {
		if a, err := servir(http.StatusOK, []byte(corpo)).ContratoDeConclusao(ctx); err != nil || a.aceita {
			t.Errorf("%s: um no que responde sem o anuncio nao aceita, e isso nao e um erro; veio aceita=%v err=%v", nome, a.aceita, err)
		}
	}

	// (b) O anúncio NÃO SE LEU: erro, sempre — nunca «não aceita».
	for nome, cli := range map[string]*nodeClient{
		"503":                          servir(http.StatusServiceUnavailable, []byte(`{"error":"indisponivel"}`)),
		"429":                          servir(http.StatusTooManyRequests, []byte(`{"error":"rate limit excedido"}`)),
		"404":                          servir(http.StatusNotFound, nil),
		"corpo ilegivel":               servir(http.StatusOK, []byte(`<html>gateway</html>`)),
		"anuncio que nao e um objecto": servir(http.StatusOK, []byte(`{"tools":[],"completion_contract":"enforce"}`)),
		"no que nao responde":          {base: "http://127.0.0.1:1", http: &http.Client{Timeout: 2 * time.Second}},
	} {
		if a, err := cli.ContratoDeConclusao(ctx); err == nil {
			t.Errorf("%s: um anuncio que nao se leu tem de ser um erro; veio aceita=%v sem erro", nome, a.aceita)
		}
	}

	// O erro do `serve` é transitório, com tipo próprio no resumo, e não larga posse nenhuma
	// (pára antes de a tomar).
	err := fmt.Errorf("%w: GET /tools deu HTTP 503", errAnuncioIlegivel)
	if codigo, classe, tipo := desfechoDoServe(err); codigo != exitErro || classe != "transitorio" || tipo != "anuncio_ilegivel" {
		t.Fatalf("desfecho do anuncio ilegivel: %d %q %q", codigo, classe, tipo)
	}
}

// TestAOS495_ModoEVector_NaoRepetemOQueVemDoNo: o modo anunciado e os nomes de tool do vector
// vêm de OUTRO processo e vão para o log da drenagem, que os scripts lêem linha a linha. Um modo
// fora do vocabulário imprime-se como `desconhecido`; uma tool que este processo não enviou no
// contrato imprime-se como `outra`.
func TestAOS495_ModoEVector_NaoRepetemOQueVemDoNo(t *testing.T) {
	const hostil = "observe\naviso: run=plan-x geracao=1 classe=terminal codigo=0"
	for modo, quer := range map[string]string{
		"observe": "observe", "enforce": "enforce", "off": "off",
		"": "desconhecido", "Enforce": "desconhecido", "audit": "desconhecido", hostil: "desconhecido",
	} {
		if got := modoImprimivel(modo); got != quer {
			t.Errorf("modoImprimivel(%q) = %q, quero %q", modo, got, quer)
		}
	}
	// O anúncio de um nó com um modo que este binário não conhece: aceita, e não repete o modo.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tools": []any{}, "completion_contract": map[string]string{"mode": hostil}})
	}))
	defer srv.Close()
	a, err := (&nodeClient{base: srv.URL, http: srv.Client()}).ContratoDeConclusao(context.Background())
	if err != nil || !a.aceita || a.modo != "desconhecido" || strings.Contains(bannerDoContrato(a), "aviso:") {
		t.Fatalf("um modo desconhecido nao desliga o contrato nem se repete: aceita=%v modo=%q err=%v", a.aceita, a.modo, err)
	}

	contrato := map[string]bool{"doc_read": true}
	v := &agentruntime.Verdict{Tools: []agentruntime.ToolEvidence{
		{Tool: "doc_read", Requested: 2, Effective: 1, Denied: 1},
		{Tool: "doc_write\naviso: run=plan-x", Requested: 3, Failed: 3},
	}}
	got := vectorDoRunFilho(v, contrato)
	if quer := "[doc_read pedidas=2 efectivas=1 negadas=1 falhadas=0; outra pedidas=3 efectivas=0 negadas=0 falhadas=3]"; got != quer {
		t.Fatalf("vector = %q, quero %q", got, quer)
	}
	if strings.Contains(got, "doc_write") || strings.Contains(got, "\n") {
		t.Fatalf("o vector repetiu um nome de tool que veio do no: %q", got)
	}
	for _, vazio := range []*agentruntime.Verdict{nil, {}} {
		if got := vectorDoRunFilho(vazio, contrato); got != "[sem contrato]" {
			t.Errorf("sem linhas no vector: %q", got)
		}
	}
}

// TestAOS495_MetricasDoContrato: os três contadores do contrato de conclusão escrevem-se no
// ficheiro de métricas, acumulam de drenagem para drenagem, e têm cardinalidade FECHADA — um
// rótulo fora do vocabulário não se escreve.
func TestAOS495_MetricasDoContrato(t *testing.T) {
	m := &metricasDoConsumo{series: map[string]float64{}}
	c := &medicaoDoContrato{}
	c.contratoNaoAplicado(motivoNoNaoAnuncia)
	c.contratoNaoAplicado("motivo inventado")
	c.veredictoObservado(string(agentruntime.OutcomeContractNoCall))
	c.veredictoObservado(string(agentruntime.OutcomeContractNoCall))
	c.veredictoObservado(causaRazaoDesconhecida)
	c.veredictoObservado("razao\nhostil")
	for _, classe := range classesDoContrato {
		c.noSubmetido(classe)
	}
	c.noSubmetido("classe inventada")
	m.registarContrato(c)
	m.registarContrato(nil)                    // um `serve` que nao mediu nada
	(*medicaoDoContrato)(nil).noSubmetido("x") // o `serve` manual: a medicao e nil

	texto := string(m.texto())
	for chave, valor := range map[string]int{
		serie(metricaContratoNaoAplicado, "motivo", motivoNoNaoAnuncia):                         1,
		serie(metricaVeredictosObservados, "razao", string(agentruntime.OutcomeContractNoCall)): 2,
		serie(metricaVeredictosObservados, "razao", causaRazaoDesconhecida):                     1,
	} {
		if !temSerie(texto, chave, valor) {
			t.Errorf("faltou %s %d:\n%s", chave, valor, texto)
		}
	}
	for _, classe := range classesDoContrato {
		if !temSerie(texto, serie(metricaNosPorContrato, "classe", classe), 1) {
			t.Errorf("faltou a classe %s:\n%s", classe, texto)
		}
	}
	for _, proibido := range []string{"inventad", "hostil"} {
		if strings.Contains(texto, proibido) {
			t.Errorf("um rotulo fora do vocabulario fechado foi escrito (%q):\n%s", proibido, texto)
		}
	}
	// As séries possíveis são as das três listas fechadas, e mais nenhuma.
	series := 0
	for chave := range m.series {
		switch nomeDaSerie(chave) {
		case metricaContratoNaoAplicado, metricaVeredictosObservados, metricaNosPorContrato:
			series++
		}
	}
	if teto := len(motivosDoContratoNaoAplicado) + len(razoesObservaveis()) + len(classesDoContrato); series > teto || series != 1+2+len(classesDoContrato) {
		t.Errorf("escreveram-se %d series do contrato; o tecto e %d", series, teto)
	}
	// A razão do veredicto observado é sempre uma do vocabulário fechado, venha o que vier do nó.
	for _, st := range []estadoDoRun{{OutcomeReason: "razao\nhostil"}, {}, {OutcomeReason: string(agentruntime.OutcomeTruncated)}} {
		if r := causaDoRunFilho(st, true); !slices.Contains(razoesObservaveis(), r) {
			t.Errorf("a razao observada %q sai do vocabulario fechado das metricas", r)
		}
	}

	// Acumulam: a drenagem seguinte lê o ficheiro e soma por cima.
	caminho := filepath.Join(t.TempDir(), nomeDoFicheiroDeMetricas)
	if err := escreverMetricas(caminho, m.texto()); err != nil {
		t.Fatal(err)
	}
	m2, err := lerMetricas(caminho)
	if err != nil {
		t.Fatalf("lerMetricas: %v", err)
	}
	c2 := &medicaoDoContrato{}
	c2.contratoNaoAplicado(motivoNoNaoAnuncia)
	c2.contratoNaoAplicado(motivoAnuncioIlegivel)
	m2.registarContrato(c2)
	if texto := string(m2.texto()); !temSerie(texto, serie(metricaContratoNaoAplicado, "motivo", motivoNoNaoAnuncia), 2) ||
		!temSerie(texto, serie(metricaContratoNaoAplicado, "motivo", motivoAnuncioIlegivel), 1) ||
		!temSerie(texto, serie(metricaVeredictosObservados, "razao", string(agentruntime.OutcomeContractNoCall)), 2) {
		t.Fatalf("os contadores nao acumularam:\n%s", texto)
	}
}

// TestAOS495_Elegibilidade: leva contrato TODO o nó não-verificador com tools atribuídas, com ou
// sem `outputs` (o alargamento da revisão adversarial). O contrato são as tools atribuídas, pela
// ordem da lista-branca. E cada nó cai numa classe, que é o que ele diz no log e nas métricas.
func TestAOS495_Elegibilidade(t *testing.T) {
	tools := []string{"doc_read", "doc_search"}
	aberta := func(tipo plan.PayloadType) []plan.Output { return []plan.Output{{Name: "s", Type: tipo}} }
	fechadas := []plan.Output{{Name: "m", Type: plan.PayloadMetrics}, {Name: "v", Type: plan.PayloadVerdict}}
	for _, c := range []struct {
		nome   string
		no     plan.Node
		tools  []string
		quer   []string
		classe string
	}{
		{"record", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadRecord)}, tools, tools, classeComContratoSaidaAberta},
		{"summary", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadSummary)}, tools, tools, classeComContratoSaidaAberta},
		{"artifact", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadArtifact)}, tools, tools, classeComContratoSaidaAberta},
		{"aberta ao lado de uma fechada", plan.Node{Role: "reader", Outputs: []plan.Output{{Name: "m", Type: plan.PayloadMetrics}, {Name: "s", Type: plan.PayloadRecord}}}, tools, tools, classeComContratoSaidaAberta},
		// A classe alargada: o último nó, o plano de um só nó, o nó de escrita.
		{"sem saida declarada", plan.Node{Role: "writer"}, tools, tools, classeComContratoSemSaida},
		{"so saidas de forma fechada", plan.Node{Role: "reader", Outputs: fechadas}, tools, tools, classeComContratoSemSaida},
		// Sem contrato, e porquê.
		{"verificador com saida aberta", plan.Node{Role: plan.RoleVerifier, Outputs: aberta(plan.PayloadRecord)}, tools, nil, classeSemContratoVerificador},
		{"verificador sem tools", plan.Node{Role: plan.RoleVerifier}, []string{}, nil, classeSemContratoVerificador},
		{"sem tools atribuidas", plan.Node{Role: "reader", Outputs: aberta(plan.PayloadRecord)}, []string{}, nil, classeSemContratoSemTools},
		{"sem tools e sem saida", plan.Node{Role: "summarizer"}, []string{}, nil, classeSemContratoSemTools},
	} {
		if got := contratoDoNo(c.no, c.tools); !slices.Equal(got, c.quer) {
			t.Errorf("%s: contrato = %v, quero %v", c.nome, got, c.quer)
		}
		if got := classeDoContrato(c.no, c.tools, true); got != c.classe {
			t.Errorf("%s: classe = %q, quero %q", c.nome, got, c.classe)
		}
		// Contra um nó que não anuncia: quem levava contrato fica na classe própria; os outros
		// ficam onde estavam — a razão deles não é o nó.
		querSemAnuncio := c.classe
		if c.quer != nil {
			querSemAnuncio = classeSemContratoNaoAnunciado
		}
		if got := classeDoContrato(c.no, c.tools, false); got != querSemAnuncio {
			t.Errorf("%s, sem anuncio: classe = %q, quero %q", c.nome, got, querSemAnuncio)
		}
		if !slices.Contains(classesDoContrato, classeDoContrato(c.no, c.tools, true)) {
			t.Errorf("%s: a classe sai da lista fechada", c.nome)
		}
		// Leva contrato quem está numa classe «com contrato», e só esse.
		if (c.quer != nil) != strings.HasPrefix(c.classe, "com_contrato_") {
			t.Errorf("%s: a classe %q e o contrato %v contradizem-se", c.nome, c.classe, c.quer)
		}
	}
	gramatica := regexp.MustCompile(`^[a-z_]{1,40}$`)
	for _, v := range append(append([]string{}, classesDoContrato...), motivosDoContratoNaoAplicado...) {
		if !gramatica.MatchString(v) {
			t.Errorf("o rotulo %q sai da gramatica [a-z_]", v)
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
