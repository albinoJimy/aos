package main

// AOS-503 — O `aos-orq` VOLTA A SUBMETER UM NÓ DO PLANO QUE TERMINOU SEM CHAMAR A TOOL (ADR-039).
//
// Os cenários de processo correm o BINÁRIO REAL do `aos-orq consume` contra um nó `aos` falso que
// serve o que o NÓ REAL responde: os ficheiros de `../aos/testdata/aos502_fio/` são gerados pelos
// testes do nó (AOS-502) — o anúncio do `GET /tools` e as respostas do `GET /runs/{id}` de uma
// tentativa falhada (a resposta de produção de 2026-10-04) e de uma recuperada. No sentido
// inverso, os corpos de `POST /runs` que este pacote envia ficam em `testdata/aos503_fio/`, e o
// teste do nó consome-os byte a byte.
//
// Regeneram-se com `AOS494_ACTUALIZAR_FIO=1`, por esta ordem: o nó (o anúncio), este pacote (os
// corpos e os vectores do id), o nó (as respostas), e este pacote outra vez.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// aos503Run é o pedido de plano destes testes; é o mesmo id que o teste do nó submete à fila.
const aos503Run = "plan-aos503-fio"

// aos503Actualizar diz se os ficheiros de fio se regeneram nesta corrida.
func aos503Actualizar() bool { return os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" }

// aos503FioDoNo lê um ficheiro gerado pelo teste do NÓ (AOS-502).
func aos503FioDoNo(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "aos", "testdata", "aos502_fio", nome+".json"))
	if err != nil || len(bytes.TrimSpace(cru)) == 0 {
		if aos503Actualizar() {
			t.Skipf("o ficheiro %s ainda nao foi gerado (%v): corre primeiro o teste do no com AOS494_ACTUALIZAR_FIO=1, e este outra vez", nome, err)
		}
		t.Fatalf("ficheiro do fio %s em falta: err=%v bytes=%d — gera-o no no com AOS494_ACTUALIZAR_FIO=1", nome, err, len(cru))
	}
	return bytes.TrimSpace(cru)
}

// aos503Fio compara (ou, a regenerar, escreve) um ficheiro que o teste do NÓ consome.
func aos503Fio(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos503_fio", nome+".json")
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

// aos503No é o nó `aos` falso destes testes. As respostas e os estados são POR ID DE RUN INTEIRO:
// as tentativas de um nó do plano são runs diferentes.
type aos503No struct {
	// catalogo é o corpo do `GET /tools`.
	catalogo []byte
	// respostas é o corpo do `GET /runs/{id}` de um run ACEITE, pelo id. Sem entrada, o run
	// conclui com o texto `feito: <id>`.
	respostas map[string][]byte
	// estadoDoPost devolve o estado HTTP do n-ésimo `POST /runs` desse id (a contar de 1). Zero ⇒
	// o nó aceita (201), ou responde 409 se já tinha aceite esse id.
	estadoDoPost func(id string, n int) int
	// ilegivelAntes é quantas vezes o `GET /runs/{id}` desse run responde 503 antes do desfecho.
	ilegivelAntes map[string]int

	mu        sync.Mutex
	ofertas   []pedidoReclamado
	desfechos []map[string]any
	crus      map[string][][]byte
	aceites   map[string]bool
	posts     []string
	leituras  map[string]int
}

func (f *aos503No) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	f.crus = map[string][][]byte{}
	f.aceites = map[string]bool{}
	f.leituras = map[string]int{}
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
		var corpo struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(cru, &corpo)
		f.mu.Lock()
		f.crus[corpo.RunID] = append(f.crus[corpo.RunID], cru)
		f.posts = append(f.posts, corpo.RunID)
		n := len(f.crus[corpo.RunID])
		jaAceite := f.aceites[corpo.RunID]
		f.mu.Unlock()
		estado := 0
		if f.estadoDoPost != nil {
			estado = f.estadoDoPost(corpo.RunID, n)
		}
		switch {
		case estado == http.StatusTooManyRequests:
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(estado)
			_, _ = w.Write([]byte(`{"error":"submissao recusada"}`))
		case estado == http.StatusForbidden:
			w.WriteHeader(estado)
			_, _ = w.Write([]byte(`{"error":"nao autorizado"}`))
		case estado != 0:
			w.WriteHeader(estado)
		case jaAceite:
			w.WriteHeader(http.StatusConflict)
		default:
			f.mu.Lock()
			f.aceites[corpo.RunID] = true
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		}
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		aceite := f.aceites[id]
		f.leituras[id]++
		resposta := f.respostas[id]
		ilegivel := aceite && f.ilegivelAntes[id] > 0
		if ilegivel {
			f.ilegivelAntes[id]--
		}
		f.mu.Unlock()
		if !aceite {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ilegivel {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"indisponivel"}`))
			return
		}
		if resposta != nil {
			_, _ = w.Write(resposta)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "completed", "terminated": true, "final_text": "feito: " + id})
	})
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = w.Write(f.catalogo)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// corpos devolve os corpos de `POST /runs` recebidos para esse id, por ordem.
func (f *aos503No) corpos(id string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.crus[id]...)
}

// submetidos devolve os ids de todos os `POST /runs`, por ordem de chegada.
func (f *aos503No) submetidos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

// aceite diz se o nó aceitou (201) um run com esse id.
func (f *aos503No) aceite(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aceites[id]
}

// lido devolve quantas vezes o `GET /runs/{id}` desse run foi pedido.
func (f *aos503No) lido(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leituras[id]
}

// responder troca a resposta do `GET /runs/{id}` de um run.
func (f *aos503No) responder(id string, corpo []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.respostas == nil {
		f.respostas = map[string][]byte{}
	}
	f.respostas[id] = corpo
}

// aos503Sessao é a pasta de trabalho do `consume`, que sobrevive a várias drenagens: o mesmo WAL,
// a mesma pasta de planos e o mesmo nó falso.
type aos503Sessao struct {
	bin, dir, wal, snap, fix string
	env                      []string
	f                        *aos503No
	// semReferencia: o pedido oferecido NÃO leva a referência ao `planrequest.submitted` (um nó
	// anterior ao AOS-477), e os runs filhos não declaram o plano nem o nó.
	semReferencia bool
}

func aos503Abrir(t *testing.T, bin string, f *aos503No, plano, snapshot string) *aos503Sessao {
	t.Helper()
	srv := f.servidor(t)
	dir := t.TempDir()
	s := &aos503Sessao{bin: bin, dir: dir, f: f, wal: filepath.Join(dir, "consume.wal"),
		snap: filepath.Join(dir, "snap.json"), fix: filepath.Join(dir, "plano.json")}
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	escrever(t, s.snap, snapshot)
	escrever(t, s.fix, plano)
	s.env = []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	return s
}

// drenar oferece o pedido na geração dada e corre UMA drenagem. `modo` vazio ⇒ a variável do
// interruptor NÃO é definida. O pedido leva a referência ao `planrequest.submitted`, como o nó de
// produção a entrega: é ela que faz cada run filho declarar o plano e o nó (AOS-477), e sem essa
// declaração não há tentativa.
func (s *aos503Sessao) drenar(t *testing.T, geracao int, modo, prazo string, extra ...string) aos499Drenagem {
	t.Helper()
	s.f.mu.Lock()
	antes := len(s.f.desfechos)
	oferta := pedidoReclamado{RunID: aos503Run, Objective: "ler o documento notes e resumi-lo", Geracao: geracao,
		RequestStream: "_aos/plan-requests", RequestSeq: 7}
	if s.semReferencia {
		oferta.RequestStream, oferta.RequestSeq = "", 0
	}
	s.f.ofertas = append(s.f.ofertas, oferta)
	s.f.mu.Unlock()
	env := append([]string{}, s.env...)
	if modo != "" {
		env = append(env, "AOS_ORQ_NOVA_TENTATIVA="+modo)
	}
	env = append(env, extra...)
	r := correrComEnv(t, env, s.bin, "consume", "--wal", s.wal, "--snapshot", s.snap, "--decompose-fixture", s.fix,
		"--poll-interval", "20ms", "--max", "1", "--plan-timeout", prazo)
	if r.code != exitOK {
		t.Fatalf("o consume em si tinha de sair 0 (o desfecho vai para o no), saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	metricas, err := os.ReadFile(filepath.Join(s.dir, nomeDoFicheiroDeMetricas))
	if err != nil {
		t.Fatalf("a drenagem tinha de escrever o ficheiro de metricas: %v", err)
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if len(s.f.desfechos) != antes+1 {
		t.Fatalf("queria mais um desfecho reportado ao no, vieram %d\n%s\n%s", len(s.f.desfechos)-antes, r.stdout, r.stderr)
	}
	d := s.f.desfechos[len(s.f.desfechos)-1]
	codigo, _ := d["codigo_saida"].(float64)
	classe, _ := d["classe"].(string)
	detalhe, _ := d["detalhe"].(string)
	return aos499Drenagem{wal: s.wal, aos495Desfecho: aos495Desfecho{codigo: int(codigo), classe: classe, detalhe: detalhe,
		stdout: r.stdout, stderr: r.stderr, metricas: string(metricas)}}
}

// aos503Catalogo devolve o `GET /tools` do nó real com o tecto de tentativas dado: o ficheiro de
// fio anuncia 2; os outros tectos trocam só esse número, e zero tira o anúncio (o corpo fica o de
// um nó sem a variável — os mesmos bytes do fio do AOS-494).
func aos503Catalogo(t *testing.T, tecto int) []byte {
	t.Helper()
	fio := aos503FioDoNo(t, "tools-enforce-retry-2")
	const anuncio = `,"run_retry":{"max":2}}`
	if !bytes.HasSuffix(fio, []byte(anuncio)) {
		t.Fatalf("pre-condicao: o GET /tools do no acaba com o anuncio %s; veio %s", anuncio, fio)
	}
	sem := append(bytes.TrimSuffix(append([]byte(nil), fio...), []byte(anuncio)), '}')
	if tecto == 0 {
		return sem
	}
	return append(bytes.TrimSuffix(sem, []byte("}")), []byte(fmt.Sprintf(`,"run_retry":{"max":%d}}`, tecto))...)
}

// aos503Ids são os ids dos runs do nó `read_notes` (as três tentativas) e do consumidor.
var (
	aos503Leitor1  = aos503Run + "~read_notes"
	aos503Leitor2  = aos503Run + "~read_notes~2"
	aos503Leitor3  = aos503Run + "~read_notes~3"
	aos503Resumo   = aos503Run + "~summarize"
	aos503ResumoT2 = aos503Run + "~summarize~2"
)

// aos503Falhado monta, à mão, a resposta de um run `failed` com a razão e o total de tool calls
// pedidas dados. Só para os casos que o nó real não grava num ficheiro de fio.
func aos503Falhado(id, razao string, pedidas int) []byte {
	corpo := `{"run_id":"` + id + `","status":"failed","turns":1`
	if razao != "" {
		corpo += fmt.Sprintf(`,"outcome_reason":%q,"verdict":{"mode":"enforce","fulfilled":false,"reason":%q,"tools":[{"tool":"doc_read","requested":%d,"effective":0,"denied":%d,"failed":0}],"tool_calls_requested":%d}`,
			razao, razao, pedidas, pedidas, pedidas)
	}
	return []byte(corpo + "}")
}

// aos503Factos devolve os factos `plan.node_attempt_started` do log do plano, por ordem.
func aos503Factos(t *testing.T, wal string) []plannerevents.NodeAttemptStartedPayload {
	t.Helper()
	var factos []plannerevents.NodeAttemptStartedPayload
	for _, e := range aos501Eventos(aos499EventosDoPlano(t, wal, aos503Run), plannerevents.EventNodeAttemptStarted) {
		var a plannerevents.NodeAttemptStartedPayload
		if err := json.Unmarshal([]byte(e.Payload), &a); err != nil {
			t.Fatalf("facto ilegivel: %v", err)
		}
		if e.Passo != fmt.Sprintf("planstep:node_attempt_started:%s:%d", a.NodeID, a.Attempt) || e.Stream != aos503Run+"-plan" {
			t.Fatalf("o facto tem o passo por (no, tentativa) e vive no stream do plano: %+v", e)
		}
		factos = append(factos, a)
	}
	return factos
}

// aos503SemVolatil tira do `detail` e das linhas de log o que muda entre corridas (a duração).
func aos503SemVolatil(texto string) string {
	var out []string
	for _, campo := range strings.Fields(texto) {
		if strings.HasPrefix(campo, "duracao_s=") {
			campo = "duracao_s=X"
		}
		out = append(out, campo)
	}
	return strings.Join(out, " ")
}

// aos503Campos decompõe o corpo de um `POST /runs` nos seus campos crus.
func aos503Campos(t *testing.T, cru []byte) map[string]json.RawMessage {
	t.Helper()
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(cru, &campos); err != nil {
		t.Fatalf("corpo de POST /runs ilegivel: %v", err)
	}
	return campos
}

// aos503SoDifereNoIdENaTentativa exige que o corpo da tentativa seja o da primeira com DUAS
// diferenças: o `run_id` e o `plan_request.attempt`. Tudo o resto — objectivo, tools, contrato,
// origem declarada e `inputs` (conteúdo e digests) — é igual BYTE A BYTE.
func aos503SoDifereNoIdENaTentativa(t *testing.T, primeira, tentativa []byte, idDaTentativa string, n int) {
	t.Helper()
	a, b := aos503Campos(t, primeira), aos503Campos(t, tentativa)
	if len(a) != len(b) {
		t.Fatalf("os dois corpos tem de ter os mesmos campos:\n  primeira:  %s\n  tentativa: %s", primeira, tentativa)
	}
	for campo, valor := range a {
		switch campo {
		case "run_id":
			if quer, _ := json.Marshal(idDaTentativa); string(b[campo]) != string(quer) {
				t.Fatalf("o run_id da tentativa e %s; veio %s", quer, b[campo])
			}
		case "plan_request":
			var va, vb map[string]json.RawMessage
			if json.Unmarshal(valor, &va) != nil || json.Unmarshal(b[campo], &vb) != nil {
				t.Fatalf("plan_request ilegivel: %s / %s", valor, b[campo])
			}
			if _, tem := va["attempt"]; tem {
				t.Fatalf("a primeira tentativa NAO leva attempt: %s", valor)
			}
			if string(vb["attempt"]) != fmt.Sprint(n) {
				t.Fatalf("a tentativa leva attempt=%d; veio %s", n, b[campo])
			}
			delete(vb, "attempt")
			if len(va) != len(vb) {
				t.Fatalf("o plan_request so difere em attempt:\n  %s\n  %s", valor, b[campo])
			}
			for k, v := range va {
				if string(vb[k]) != string(v) {
					t.Fatalf("plan_request.%s mudou entre a primeira e a tentativa: %s -> %s", k, v, vb[k])
				}
			}
		default:
			if !bytes.Equal(valor, b[campo]) {
				t.Fatalf("o campo %s do pedido repetido tem de ser igual byte a byte:\n  primeira:  %s\n  tentativa: %s", campo, valor, b[campo])
			}
		}
	}
}

// TestAOS503ComOBinarioReal corre os cenários de processo sobre UM só binário.
func TestAOS503ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	cat2 := aos503Catalogo(t, 2)
	if !bytes.Equal(aos503Catalogo(t, 0), p.catalogo) {
		t.Fatalf("o GET /tools de um no com o tecto a zero tem de ser, byte a byte, o do fio do AOS-494:\n  agora: %s\n  antes: %s", aos503Catalogo(t, 0), p.catalogo)
	}

	// (0) OS CORPOS QUE O NÓ CONSOME. A primeira tentativa falha (resposta montada à mão, só para
	// o processo chegar às tentativas); o que se grava são os corpos de `POST /runs`.
	t.Run("Fio_OsCorposDasTentativas", func(t *testing.T) {
		f := &aos503No{catalogo: cat2, respostas: map[string][]byte{
			aos503Leitor1: aos503Falhado(aos503Leitor1, "contract_unmet_no_call", 0),
			aos503Leitor2: aos503Falhado(aos503Leitor2, "contract_unmet_no_call", 0),
		}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "on", "30s")
		if d.codigo != exitOK {
			t.Fatalf("falha, falha, sucesso: o plano sai 0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		c1, c2, c3 := f.corpos(aos503Leitor1), f.corpos(aos503Leitor2), f.corpos(aos503Leitor3)
		if len(c1) != 1 || len(c2) != 1 || len(c3) != 1 {
			t.Fatalf("tres runs do leitor, um POST cada: %d %d %d", len(c1), len(c2), len(c3))
		}
		aos503Fio(t, "post-runs-read_notes", c1[0])
		aos503Fio(t, "post-runs-read_notes-tentativa-2", c2[0])
		aos503Fio(t, "post-runs-read_notes-tentativa-3", c3[0])
		aos503SoDifereNoIdENaTentativa(t, c1[0], c2[0], aos503Leitor2, 2)
		aos503SoDifereNoIdENaTentativa(t, c1[0], c3[0], aos503Leitor3, 3)
	})

	// (1) FALHA, FALHA, SUCESSO — com as respostas do NÓ REAL (a resposta de produção de
	// 2026-10-04 nas duas primeiras). O plano sai 0, há três runs do leitor, e o consumidor recebe
	// a saída da TERCEIRA.
	t.Run("On_FalhaFalhaSucesso", func(t *testing.T) {
		falhada1, falhada2, recuperada3 := aos503FioDoNo(t, "tentativa-1-falhada"), aos503FioDoNo(t, "tentativa-2-falhada"), aos503FioDoNo(t, "tentativa-3-recuperada")
		var st3 estadoDoRun
		if err := json.Unmarshal(recuperada3, &st3); err != nil || !st3.concluiu() || st3.RunID != aos503Leitor3 || st3.FinalText == "" {
			t.Fatalf("pre-condicao: o fio da tentativa 3 e um run concluido com texto (%v): %s", err, recuperada3)
		}
		f := &aos503No{catalogo: cat2, respostas: map[string][]byte{aos503Leitor1: falhada1, aos503Leitor2: falhada2, aos503Leitor3: recuperada3}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "on", "30s")
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("falha, falha, sucesso: o plano sai terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Leitor2, aos503Leitor3, aos503Resumo}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("tres runs do leitor e depois o consumidor, por esta ordem:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
		}
		// O consumidor recebe a saída da tentativa que teve êxito, e só correu depois dela.
		var resumo struct {
			Inputs []entradaDoNo `json:"inputs"`
		}
		if err := json.Unmarshal(f.corpos(aos503Resumo)[0], &resumo); err != nil || len(resumo.Inputs) != 1 {
			t.Fatalf("o consumidor leva uma entrada: %v %s", err, f.corpos(aos503Resumo)[0])
		}
		if resumo.Inputs[0].Content != st3.FinalText || resumo.Inputs[0].Digest != digestDoConteudo(st3.FinalText) {
			t.Fatalf("o consumidor recebe a saida da TERCEIRA tentativa:\n  quero: %q\n  veio:  %q", st3.FinalText, resumo.Inputs[0].Content)
		}
		// O LOG DO PLANO: dois factos, por ordem, e a publicação aponta para o run da terceira.
		factos := aos503Factos(t, s.wal)
		if len(factos) != 2 || factos[0] != (plannerevents.NodeAttemptStartedPayload{PlanID: aos503Run + "-plan", NodeID: "read_notes", Attempt: 2, RetryOf: aos503Leitor1, Reason: plannerevents.AttemptReasonContractUnmetNoCall}) ||
			factos[1] != (plannerevents.NodeAttemptStartedPayload{PlanID: aos503Run + "-plan", NodeID: "read_notes", Attempt: 3, RetryOf: aos503Leitor2, Reason: plannerevents.AttemptReasonContractUnmetNoCall}) {
			t.Fatalf("dois factos, um por tentativa, com o run anterior de cada uma: %+v", factos)
		}
		publicados := aos501Eventos(aos499EventosDoPlano(t, s.wal, aos503Run), plannerevents.EventPayloadPublished)
		var pub plannerevents.PayloadPublishedPayload
		if len(publicados) != 1 || json.Unmarshal([]byte(publicados[0].Payload), &pub) != nil || pub.Record.Stream != aos503Leitor3 || pub.Record.Digest != digestDoConteudo(st3.FinalText) {
			t.Fatalf("o plan.payload_published aponta para o run da tentativa que concluiu: %+v", publicados)
		}
		// O DESFECHO: um 0 depois de recuperação distingue-se de um 0 à primeira.
		if !strings.HasSuffix(d.detalhe, " tentativas=2 recuperados=1") || strings.Contains(d.detalhe, "causa=") || strings.Contains(d.detalhe, "esgotadas") {
			t.Fatalf("o detail leva tentativas= e recuperados=, e mais nada sobre falhas: %q", d.detalhe)
		}
		if !strings.Contains(d.stdout, "execucao: no read_notes complete (run "+aos503Leitor3+") RECUPERADO na tentativa 3\n") ||
			!strings.Contains(d.stdout, "execucao: read_notes=complete summarize=complete\n") {
			t.Fatalf("o log diz em que tentativa o no recuperou e os estados finais:\n%s", d.stdout)
		}
		if strings.Contains(d.stdout, "aviso:") && !strings.Contains(d.stdout, "aviso: run="+aos503Run+" geracao=1 classe=terminal codigo=0\n") {
			t.Fatalf("o aviso de um plano recuperado e o de um plano que correu bem:\n%s", d.stdout)
		}
		for chave, valor := range map[string]int{
			serie(metricaPrimeirasFalhas, "com_consumes", "false"):                                                  1,
			serie(metricaTentativas, "tentativa", "2", "desfecho", tentativaVoltouAFalhar, "com_consumes", "false"): 1,
			serie(metricaTentativas, "tentativa", "3", "desfecho", tentativaRecuperou, "com_consumes", "false"):     1,
			serie(metricaNosRecuperados, "com_consumes", "false"):                                                   1,
			metricaPlanosRecuperados:                                     1,
			serie(metricaTentativasPorPlano, "tentativas", "2"):          1,
			serie(metricaDesfechos, "classe", "terminal", "codigo", "0"): 1,
		} {
			if !temSerie(d.metricas, chave, valor) {
				t.Fatalf("faltou a serie %s %d:\n%s", chave, valor, d.metricas)
			}
		}
		for _, proibida := range []string{metricaPlanosEsgotados, metricaTentativasRecusadas, metricaTentativasEmObservacao} {
			if strings.Contains(d.metricas, proibida) {
				t.Fatalf("a serie %s nao tem valor neste plano:\n%s", proibida, d.metricas)
			}
		}
		// SEM CONTEÚDO: o texto da resposta de produção (das tentativas falhadas, que o nó nem
		// devolve) e o da tentativa recuperada não aparecem no log, nas métricas nem no detail.
		for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": aos501MetricasSemRelogio(d.metricas), "detalhe": d.detalhe} {
			if levaProibido(texto, st3.FinalText) {
				t.Fatalf("o texto final da tentativa recuperada aparece em %s", onde)
			}
		}
	})

	// (2) FALHA TRÊS VEZES: o plano sai 13 com a causa do run e as tentativas esgotadas; o
	// consumidor não corre; nada se publica.
	t.Run("On_FalhaTresVezes_Esgota", func(t *testing.T) {
		f := &aos503No{catalogo: cat2, respostas: map[string][]byte{
			aos503Leitor1: aos503FioDoNo(t, "tentativa-1-falhada"), aos503Leitor2: aos503FioDoNo(t, "tentativa-2-falhada"), aos503Leitor3: aos503FioDoNo(t, "tentativa-3-falhada")}}
		s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
		d := s.drenar(t, 1, "on", "30s")
		if d.classe != "terminal" || d.codigo != exitNosFalhados {
			t.Fatalf("tres falhas: o plano sai terminal/13; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if quer := []string{aos503Leitor1, aos503Leitor2, aos503Leitor3}; fmt.Sprint(f.submetidos()) != fmt.Sprint(quer) {
			t.Fatalf("EXACTAMENTE tres runs do leitor — nem uma quarta tentativa, nem o consumidor:\n  quero: %v\n  veio:  %v", quer, f.submetidos())
		}
		if !strings.HasSuffix(d.detalhe, " erro=nos_falhados causa=contract_unmet_no_call:1,entrada_por_cumprir:1 tentativas=2 recuperados=0 tentativas_esgotadas=1") {
			t.Fatalf("o detail leva a causa do run e as tentativas esgotadas: %q", d.detalhe)
		}
		if !strings.Contains(d.stdout, "execucao: no read_notes tentativas_esgotadas tentativas=3") ||
			!strings.Contains(d.stdout, "(run "+aos503Leitor3+") causa=contract_unmet_no_call vector [doc_read pedidas=0 efectivas=0 negadas=0 falhadas=0] tentativas=3 tentativas_esgotadas\n") {
			t.Fatalf("o log da drenagem diz que as tentativas se esgotaram:\n%s", d.stdout)
		}
		if !strings.Contains(d.stdout, "aviso: run="+aos503Run+" geracao=1 classe=terminal codigo=13 causa="+causaConclusaoNaoCumprida+"\n") {
			t.Fatalf("o aviso mantem o valor fixo de hoje:\n%s", d.stdout)
		}
		if len(aos503Factos(t, s.wal)) != 2 || len(aos501Eventos(aos499EventosDoPlano(t, s.wal, aos503Run), plannerevents.EventPayloadPublished)) != 0 {
			t.Fatal("dois factos de tentativa e nenhuma publicacao")
		}
		for chave, valor := range map[string]int{
			serie(metricaPrimeirasFalhas, "com_consumes", "false"):                                                  1,
			serie(metricaTentativas, "tentativa", "2", "desfecho", tentativaVoltouAFalhar, "com_consumes", "false"): 1,
			serie(metricaTentativas, "tentativa", "3", "desfecho", tentativaVoltouAFalhar, "com_consumes", "false"): 1,
			metricaPlanosEsgotados:                                        1,
			serie(metricaTentativasPorPlano, "tentativas", "2"):           1,
			serie(metricaDesfechos, "classe", "terminal", "codigo", "13"): 1,
		} {
			if !temSerie(d.metricas, chave, valor) {
				t.Fatalf("faltou a serie %s %d:\n%s", chave, valor, d.metricas)
			}
		}
		if strings.Contains(d.metricas, metricaPlanosRecuperados) || strings.Contains(d.metricas, metricaNosRecuperados) {
			t.Fatalf("nada se recuperou:\n%s", d.metricas)
		}
	})

	// (3) SUCESSO À PRIMEIRA: nenhuma tentativa, e tudo igual a `off` — os mesmos POST, os mesmos
	// eventos do plano e o mesmo detail. Só a série das tentativas por plano (zero) é nova.
	t.Run("On_SucessoAPrimeira_NenhumaTentativa", func(t *testing.T) {
		correr := func(modo string) (aos499Drenagem, *aos503No, []aos499Evento) {
			f := &aos503No{catalogo: cat2}
			s := aos503Abrir(t, bin, f, p.plano, p.snapshot)
			d := s.drenar(t, 1, modo, "30s")
			return d, f, aos499EventosDoPlano(t, s.wal, aos503Run)
		}
		dOff, fOff, evOff := correr("")
		dOn, fOn, evOn := correr("on")
		if dOn.codigo != exitOK || dOff.codigo != exitOK {
			t.Fatalf("os dois planos saem 0: %d %d", dOn.codigo, dOff.codigo)
		}
		if fmt.Sprint(fOn.submetidos()) != fmt.Sprint(fOff.submetidos()) || len(fOn.submetidos()) != 2 {
			t.Fatalf("os mesmos dois POST /runs: %v / %v", fOn.submetidos(), fOff.submetidos())
		}
		for _, id := range fOn.submetidos() {
			if !bytes.Equal(fOn.corpos(id)[0], fOff.corpos(id)[0]) {
				t.Fatalf("o corpo de %s e igual em on e em off:\n  on:  %s\n  off: %s", id, fOn.corpos(id)[0], fOff.corpos(id)[0])
			}
		}
		if fmt.Sprint(evOn) != fmt.Sprint(evOff) || len(aos501Eventos(evOn, plannerevents.EventNodeAttemptStarted)) != 0 {
			t.Fatal("os eventos do plano sao os de off, e nao ha factos de tentativa")
		}
		if aos503SemVolatil(dOn.detalhe) != aos503SemVolatil(dOff.detalhe) || strings.Contains(dOn.detalhe, "tentativas") {
			t.Fatalf("o detail de um plano que correu bem a primeira e o de sempre:\n  on:  %q\n  off: %q", dOn.detalhe, dOff.detalhe)
		}
		if !temSerie(dOn.metricas, serie(metricaTentativasPorPlano, "tentativas", "0"), 1) {
			t.Fatalf("em on, um plano sem tentativas conta na classe 0:\n%s", dOn.metricas)
		}
		for _, proibida := range []string{metricaPrimeirasFalhas, metricaTentativas + "{", metricaPlanosRecuperados} {
			if strings.Contains(dOn.metricas, proibida) {
				t.Fatalf("sem primeira falha nao ha a serie %s:\n%s", proibida, dOn.metricas)
			}
		}
		if strings.Contains(dOff.metricas, "tentativa") || strings.Contains(dOff.metricas, "primeiras_falhas") || strings.Contains(dOff.metricas, "recuperados") {
			t.Fatalf("em off o ficheiro de metricas nao tem series da nova tentativa:\n%s", dOff.metricas)
		}
		if strings.Contains(dOff.stdout, "nova tentativa") || strings.Contains(dOff.stdout, "NOVA TENTATIVA") {
			t.Fatalf("em off o log nao fala da nova tentativa:\n%s", dOff.stdout)
		}
	})
}
