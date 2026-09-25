package main

// AOS-442 — A RETOMA DE UM PLANO CORRE PELO DOCUMENTO VALIDADO, E UM PLANO À ESPERA DE HUMANO
// VOLTA A CORRER DEPOIS DA DECISÃO.
//
// Medido em produção (`plan-e2e-437-1790336067`): a retoma do `consume` corria `serve --goal`
// outra vez, o modelo re-decompunha, saía outro organigrama, e o gate recusava-o — saída 7,
// terminal. E um pedido que saía `aguarda_humano` ficava parado para sempre depois da decisão.
//
// Os testes correm o `consume` REAL (o binário) contra um nó falso que faz de fila. O planeador é
// o fixture do decompositor, e a prova de que não re-decompõe é dupla: a linha `decomposto:` que
// o `serve` imprime por cada decomposição é CONTADA, e a segunda tentativa recebe um fixture com
// OUTRO organigrama — se decompusesse, o gate recusava-o (7) e o teste via-o.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/substrate/eventstore"
)

// aos442No é o nó `aos` falso: a FILA (uma oferta por reclamação, pela ordem dada, e os
// desfechos gravados) e os runs dos nós do plano.
type aos442No struct {
	mu         sync.Mutex
	ofertas    []pedidoReclamado
	desfechos  []pedidoDeDesfechoVisto
	submissoes map[string]bool
	nuncaAcaba bool
	saidaVerif string
}

type pedidoDeDesfechoVisto struct {
	RunID   string `json:"run_id"`
	Geracao int    `json:"generation"`
	Classe  string `json:"classe"`
	Codigo  int    `json:"codigo_saida"`
}

func (f *aos442No) oferecer(p pedidoReclamado) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ofertas = append(f.ofertas, p)
}

func (f *aos442No) vistos() []pedidoDeDesfechoVisto {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pedidoDeDesfechoVisto(nil), f.desfechos...)
}

func (f *aos442No) submetidos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.submissoes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (f *aos442No) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	f.submissoes = map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plans/claim", func(w http.ResponseWriter, r *http.Request) {
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
		var d pedidoDeDesfechoVisto
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
		f.mu.Lock()
		repetida := f.submissoes[id]
		f.submissoes[id] = true
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
		existe, nunca, verif := f.submissoes[id], f.nuncaAcaba, f.saidaVerif
		f.mu.Unlock()
		if !existe {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if nunca {
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "in_progress"})
			return
		}
		saida := "feito: " + id
		if strings.HasSuffix(id, "~n2") {
			saida = verif
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": id, "status": "completed", "terminated": true, "final_text": saida})
	})
	// AOS-441: o consume confere o snapshot com o catálogo do nó ANTES de reclamar. O nó falso tem
	// as tools do snapshot destes testes (aos408SnapshotComPerigo), tal e qual.
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(aos441CatalogoDoSnapshotComPerigo))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// aos442Ambiente monta o nó falso, o binário e os ficheiros comuns.
type aos442Ambiente struct {
	no                  *aos442No
	env                 []string
	bin, dir, wal, snap string
}

func novoAmbiente442(t *testing.T, bin string, no *aos442No) aos442Ambiente {
	t.Helper()
	srv := no.servidor(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	return aos442Ambiente{
		no:   no,
		env:  []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="},
		bin:  bin,
		dir:  dir,
		wal:  filepath.Join(dir, "consume.wal"),
		snap: snap,
	}
}

// consumir corre UMA drenagem de um pedido, com o fixture dado a fazer de modelo.
func (a aos442Ambiente) consumir(t *testing.T, fixture string, extra ...string) resultado {
	t.Helper()
	fix := filepath.Join(a.dir, "fixture-"+strings.ReplaceAll(t.Name(), "/", "_")+".json")
	escrever(t, fix, fixture)
	args := append([]string{"consume", "--wal", a.wal, "--snapshot", a.snap, "--decompose-fixture", fix,
		"--poll-interval", "20ms", "--max", "1"}, extra...)
	r := correrComEnv(t, a.env, a.bin, args...)
	if r.code != exitOK {
		t.Fatalf("o consume em si tinha de sair 0 (o desfecho vai para o nó), saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	return r
}

// documentoDe é o ficheiro onde o `consume` guarda o documento do plano do run (por omissão,
// `planos/` ao lado do WAL).
func (a aos442Ambiente) documentoDe(t *testing.T, run string) string {
	t.Helper()
	doc, err := caminhoDoDocumento(filepath.Join(a.dir, "planos"), run)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func aos442Redecomposto(plano string) string {
	return strings.Replace(plano, `"objective": "`, `"objective": "SEGUNDA DECOMPOSICAO — `, 1)
}

func exigirDesfechos(t *testing.T, vistos []pedidoDeDesfechoVisto, esperados ...pedidoDeDesfechoVisto) {
	t.Helper()
	if !slices.Equal(vistos, esperados) {
		t.Fatalf("desfechos reportados ao nó:\n  veio     %+v\n  esperava %+v", vistos, esperados)
	}
}

// TestAOS442ComOBinarioReal corre os cenários que precisam do `aos-orq` como processo — o
// `consume` e o `serve` reais — sobre UM só binário. Compilá-lo por cenário custava minutos à
// suite, e o `go test` da CI corre com o prazo por omissão de 10 min por pacote.
func TestAOS442ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	t.Run("RetomaPosAprovacaoNaoRedecompoe", func(t *testing.T) { aos442RetomaPosAprovacaoNaoRedecompoe(t, bin) })
	t.Run("AprovacaoHumanaLevaOPedidoACorrer", func(t *testing.T) { aos442AprovacaoHumanaLevaOPedidoACorrer(t, bin) })
	t.Run("ValidadoSemDocumentoNaoPagaAoModelo", func(t *testing.T) { aos442ValidadoSemDocumentoNaoPagaAoModelo(t, bin) })
	t.Run("DocumentoAntesDosFactos", func(t *testing.T) { aos442DocumentoAntesDosFactos(t, bin) })
	t.Run("SnapshotTrocadoEntreTentativasETerminal", func(t *testing.T) { aos442SnapshotTrocadoETerminal(t, bin) })
	t.Run("DocumentoTruncadoETerminal", func(t *testing.T) { aos442DocumentoTruncadoETerminal(t, bin) })
	t.Run("DocumentoPlantadoNumRunNovoNaoEUsado", func(t *testing.T) { aos442DocumentoPlantadoNaoEUsado(t, bin) })
	t.Run("PlanDocBenignoNaoContornaOPendente", func(t *testing.T) { aos442PlanDocBenignoNaoContornaOPendente(t, bin) })
	t.Run("PlanDocTruncadoSai10", func(t *testing.T) { aos442PlanDocTruncadoSai10(t, bin) })
}

// aos442RetomaPosAprovacaoNaoRedecompoe — o defeito medido em produção.
//
// Geração 1: o plano sem risco auto-aprova, é despachado, e o prazo acaba com os nós a correr
// (saída 8, transitório). Geração 2: a retoma. FALHA-ANTES: corria `--goal`, o modelo devolvia
// OUTRO organigrama, e o gate recusava-o — desfecho `7`, terminal, e um plano que correu metade
// perdido.
func aos442RetomaPosAprovacaoNaoRedecompoe(t *testing.T, bin string) {
	no := &aos442No{nuncaAcaba: true}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos442-retoma"

	no.oferecer(pedidoReclamado{RunID: run, Objective: "recolher e analisar", Geracao: 1})
	r1 := a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--plan-timeout", "300ms")
	if !strings.Contains(r1.stdout, "gate de plano: APROVADO sem humano") {
		t.Fatalf("a 1.ª geração tinha de auto-aprovar:\n%s", r1.stdout)
	}
	if _, err := os.Stat(a.documentoDe(t, run)); err != nil {
		t.Fatalf("o documento do plano AUTO-APROVADO tinha de ficar guardado para a retoma: %v\n%s", err, r1.stdout)
	}

	// A retoma: os nós acabaram entretanto, e o «modelo» devolveria agora outro organigrama.
	no.mu.Lock()
	no.nuncaAcaba = false
	no.mu.Unlock()
	no.oferecer(pedidoReclamado{RunID: run, Objective: "recolher e analisar", Geracao: 2})
	r2 := a.consumir(t, aos442Redecomposto(planoFixtureDuasFolhasComSnapshotAOS408), "--plan-timeout", "30s")

	if n := strings.Count(r1.stdout+r2.stdout, "decomposto:"); n != 1 {
		t.Fatalf("o planeador foi chamado %d vezes; a retoma de um plano aprovado não decompõe\n--- 1.ª\n%s\n--- 2.ª\n%s", n, r1.stdout, r2.stdout)
	}
	if !strings.Contains(r2.stdout, "origem do plano: run="+run+" documento=") {
		t.Fatalf("a retoma tinha de correr pelo documento validado:\n%s", r2.stdout)
	}
	if !strings.Contains(r2.stdout, "gate de plano: ja APROVADO") {
		t.Fatalf("a retoma tinha de passar pelo gate e reconhecer a decisão do log:\n%s", r2.stdout)
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "transitorio", Codigo: exitNosEmVoo},
		pedidoDeDesfechoVisto{RunID: run, Geracao: 2, Classe: "terminal", Codigo: exitOK},
	)
	exigirDocumentoApagado(t, a.documentoDe(t, run))
}

// exigirDocumentoApagado: depois de um desfecho TERMINAL reportado, a cópia em claro do documento
// sai do disco (M4 da revisão do AOS-442).
func exigirDocumentoApagado(t *testing.T, doc string) {
	t.Helper()
	if _, err := os.Stat(doc); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("o documento de um pedido FECHADO tinha de ter sido apagado (%v): %s", err, doc)
	}
}

// aos442AprovacaoHumanaLevaOPedidoACorrer — o segundo buraco.
//
// Geração 1: plano de risco, fica pendente (`aguarda_humano`). Geração 2: o nó re-oferece-o antes
// da decisão; a re-verificação lê o log e NÃO corre o `serve` (sem posse, sem modelo), e continua à espera. O humano
// decide sobre o documento que o `consume` guardou. Geração 3: corre até ao fim.
// FALHA-ANTES: o `consume` não guardava o documento (o humano não tinha o que decidir), a
// re-verificação decompunha outra vez e o gate recusava o organigrama novo.
func aos442AprovacaoHumanaLevaOPedidoACorrer(t *testing.T, bin string) {
	no := &aos442No{saidaVerif: `{"outcome":"pass","reasons":[]}`}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos442-humano"

	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 1})
	r1 := a.consumir(t, aos413PlanoDoModeloVivo)
	if !strings.Contains(r1.stdout, "pendente de aprovacao humana") {
		t.Fatalf("o plano com n3 danger tinha de ficar pendente:\n%s", r1.stdout)
	}
	doc := a.documentoDe(t, run)
	if _, err := os.Stat(doc); err != nil {
		t.Fatalf("o documento pendente tinha de ficar guardado para o humano decidir: %v", err)
	}

	// Re-oferta ANTES da decisão: re-verifica, sem modelo e sem gastar o `--max`.
	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 2})
	r2 := a.consumir(t, aos442Redecomposto(aos413PlanoDoModeloVivo))
	if strings.Contains(r2.stdout, "decomposto:") || strings.Contains(r2.stdout, "posse:") {
		t.Fatalf("a re-verificação de um plano à espera de humano não corre o serve (nem modelo, nem posse):\n%s", r2.stdout)
	}
	if !strings.Contains(r2.stdout, "0 pedido(s) consumido(s), 1 re-verificado(s)") {
		t.Fatalf("a re-verificação não gasta o --max:\n%s", r2.stdout)
	}
	if ids := no.submetidos(); len(ids) != 0 {
		t.Fatalf("um plano PENDENTE não executa nada no nó, e submeteu %v", ids)
	}

	// A cerimónia, sobre o WAL do consume e o documento que ele guardou.
	pl := correr(t, a.bin, "plans", "--wal", a.wal, "--run", run)
	m := reRequestID.FindStringSubmatch(pl.stdout)
	if m == nil {
		t.Fatalf("sem request_id:\n%s", pl.stdout)
	}
	priv, aprovadores := aos408Aprovador(t, a.dir, "human:alice")
	aprovacao := aos408Assinar(t, a.dir, "aprovacao.json", m[1], "human:alice", priv, true)
	if d := correr(t, a.bin, "decide", "--wal", a.wal, "--run", run, "--plan-doc", doc, "--snapshot", a.snap,
		"--decision", "approve", "--approval", aprovacao, "--approvers", aprovadores); d.code != exitOK {
		t.Fatalf("aprovação: %d\n%s\n%s", d.code, d.stdout, d.stderr)
	}

	// Re-oferta DEPOIS da decisão: corre.
	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 3})
	r3 := a.consumir(t, aos442Redecomposto(aos413PlanoDoModeloVivo), "--plan-timeout", "30s")
	if n := strings.Count(r1.stdout+r2.stdout+r3.stdout, "decomposto:"); n != 1 {
		t.Fatalf("o planeador foi chamado %d vezes, esperava 1", n)
	}
	for _, quer := range []string{"gate de plano: APROVADO por humano", "execucao: n1=complete n2=complete n3=complete"} {
		if !strings.Contains(r3.stdout, quer) {
			t.Fatalf("faltou %q depois da aprovação:\n%s\n%s", quer, r3.stdout, r3.stderr)
		}
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "aguarda_humano", Codigo: exitPendenteDeAprovacao},
		pedidoDeDesfechoVisto{RunID: run, Geracao: 2, Classe: "aguarda_humano", Codigo: exitPendenteDeAprovacao},
		pedidoDeDesfechoVisto{RunID: run, Geracao: 3, Classe: "terminal", Codigo: exitOK},
	)
	exigirDocumentoApagado(t, doc)
}

// aos442ValidadoSemDocumentoNaoPagaAoModelo: um plano já validado cujo documento se perdeu
// não tem retoma possível — uma decomposição nova daria outro organigrama, e o gate recusá-lo-ia.
// O `consume` reporta essa recusa SEM correr o `serve`: nem modelo, nem posse.
func aos442ValidadoSemDocumentoNaoPagaAoModelo(t *testing.T, bin string) {
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos442-sem-doc"

	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 1})
	a.consumir(t, aos413PlanoDoModeloVivo)
	if err := os.Remove(a.documentoDe(t, run)); err != nil {
		t.Fatal(err)
	}

	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 2})
	r := a.consumir(t, aos442Redecomposto(aos413PlanoDoModeloVivo))
	if strings.Contains(r.stdout, "decomposto:") || strings.Contains(r.stdout, "posse:") {
		t.Fatalf("sem documento, o serve não podia ter corrido:\n%s", r.stdout)
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "aguarda_humano", Codigo: exitPendenteDeAprovacao},
		pedidoDeDesfechoVisto{RunID: run, Geracao: 2, Classe: "terminal", Codigo: exitDecisaoRecusada},
	)
}

// aos442DocumentoAntesDosFactos: o documento escreve-se ANTES de o `plan.validated` entrar no
// log. Com a ordem inversa, uma falha a escrever deixava o plano ancorado a um hash cujo documento
// não existe em lado nenhum — e nenhuma retoma o voltava a produzir. Nesta ordem, a falha aborta
// SEM factos, e a tentativa seguinte decompõe como se nada fosse.
func aos442DocumentoAntesDosFactos(t *testing.T, bin string) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "es.wal")
	const run = "run-aos442-ordem"

	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher-e-analisar", "--snapshot", snap,
		"--decompose-fixture", fix, "--plan-out", filepath.Join(dir, "nao-existe", "doc.json"), "--worker", "p1")
	if r.code == exitOK {
		t.Fatalf("sem onde escrever o documento, o serve tinha de falhar:\n%s", r.stdout)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", run)
	if !strings.Contains(pl.stdout, "SEM-PROPOSTA-VALIDADA") {
		t.Fatalf("a falha a escrever o documento deixou factos no log — o plano ficou ancorado sem documento:\n%s", pl.stdout)
	}
}

// aos442SnapshotTrocadoETerminal — A1 da revisão. O snapshot muda entre tentativas (mesmo rótulo,
// conteúdo diferente): a retoma pelo documento é recusada pelo gate, e a recusa é DETERMINISTA.
// A troca muda só a VERSÃO de uma tool — o que o AOS-441 não confere e não muda o risco de nenhum
// nó —, para que seja o selo do snapshot (`ErrSnapshotDiferenteDoSelado`) a recusar. Mudar os
// eixos não serve: descê-los abaixo do nó é recusado pelo AOS-441 antes de reclamar, e subi-los
// torna perigoso um nó que a decisão gravada auto-aprovou, e o gate recusa-o por aí (7).
// FALHA-ANTES: `ErrSnapshotDiferenteDoSelado` caía no código genérico 1 ⇒ transitório, e o pedido
// voltava à cabeça da fila para sempre, a comer o `--max` de todas as drenagens.
func aos442SnapshotTrocadoETerminal(t *testing.T, bin string) {
	no := &aos442No{nuncaAcaba: true}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos442-snap-trocado"

	no.oferecer(pedidoReclamado{RunID: run, Objective: "recolher e analisar", Geracao: 1})
	a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--plan-timeout", "300ms")
	escrever(t, a.snap, strings.Replace(aos408SnapshotComPerigo,
		`"name":"http.post","version":"2.0.0"`, `"name":"http.post","version":"2.0.1"`, 1))

	no.oferecer(pedidoReclamado{RunID: run, Objective: "recolher e analisar", Geracao: 2})
	r := a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--plan-timeout", "30s")
	if strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("sob outro catálogo nada podia materializar:\n%s", r.stdout)
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "transitorio", Codigo: exitNosEmVoo},
		pedidoDeDesfechoVisto{RunID: run, Geracao: 2, Classe: "terminal", Codigo: exitDocumentoRecusado},
	)
	exigirDocumentoApagado(t, a.documentoDe(t, run))
}

// aos442DocumentoTruncadoETerminal — A1. O documento guardado de um plano pendente está
// truncado: a re-verificação recusa-o, sem `serve`, e o pedido FECHA (10). FALHA-ANTES: a leitura
// falhava como erro genérico, transitório, para sempre.
func aos442DocumentoTruncadoETerminal(t *testing.T, bin string) {
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos442-truncado"

	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 1})
	a.consumir(t, aos413PlanoDoModeloVivo)
	doc := a.documentoDe(t, run)
	inteiro := ler(t, doc)
	escrever(t, doc, inteiro[:len(inteiro)/2])

	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 2})
	r := a.consumir(t, aos413PlanoDoModeloVivo)
	if strings.Contains(r.stdout, "posse:") || strings.Contains(r.stdout, "decomposto:") {
		t.Fatalf("um documento truncado não corre o serve:\n%s", r.stdout)
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "aguarda_humano", Codigo: exitPendenteDeAprovacao},
		pedidoDeDesfechoVisto{RunID: run, Geracao: 2, Classe: "terminal", Codigo: exitDocumentoRecusado},
	)
	exigirDocumentoApagado(t, doc)
}

// aos442DocumentoPlantadoNaoEUsado — M1. Um documento que aparece na pasta para um run que o log
// nunca validou não tem âncora contra a qual o confrontar: não é usado, decompõe-se, e o que a
// decomposição validar substitui-o. FALHA-ANTES: o `consume` corria-o por `--plan-doc`, e um plano
// sem risco plantado ali era auto-aprovado no lugar da decomposição.
func aos442DocumentoPlantadoNaoEUsado(t *testing.T, bin string) {
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	const run = "plan-aos442-plantado"
	doc := a.documentoDe(t, run)
	if err := os.MkdirAll(filepath.Dir(doc), 0o700); err != nil {
		t.Fatal(err)
	}
	escrever(t, doc, planoFixtureDuasFolhasComSnapshotAOS408)

	no.oferecer(pedidoReclamado{RunID: run, Objective: "ler e publicar", Geracao: 1})
	r := a.consumir(t, aos413PlanoDoModeloVivo)
	if strings.Count(r.stdout, "decomposto:") != 1 || strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("o documento plantado não podia ser usado — tinha de decompor, e o plano de risco ficar pendente:\n%s", r.stdout)
	}
	if !strings.Contains(ler(t, doc), `"n3"`) {
		t.Fatal("o documento plantado tinha de ser substituído pelo que a decomposição validou")
	}
	exigirDesfechos(t, no.vistos(),
		pedidoDeDesfechoVisto{RunID: run, Geracao: 1, Classe: "aguarda_humano", Codigo: exitPendenteDeAprovacao},
	)
}

// aos442PlanDocBenignoNaoContornaOPendente — M1, no `serve`. Um plano de risco está pendente; um
// `--plan-doc` com OUTRO organigrama, sem risco e com o mesmo `capabilities_hash`, caía no ramo sem
// risco e era auto-aprovado — contornando a revisão humana do plano que está de facto pendente.
func aos442PlanDocBenignoNaoContornaOPendente(t *testing.T, bin string) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	fix := filepath.Join(dir, "risco.json")
	escrever(t, fix, aos408PlanoRiscoIndependente)
	benigno := filepath.Join(dir, "benigno.json")
	escrever(t, benigno, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "es.wal")
	const run = "run-aos442-benigno"

	if r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher-e-publicar", "--snapshot", snap,
		"--decompose-fixture", fix, "--worker", "p1"); r.code != exitPendenteDeAprovacao {
		t.Fatalf("o plano de risco tinha de ficar pendente, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--plan-doc", benigno, "--snapshot", snap, "--worker", "p2")
	if r.code != exitDocumentoRecusado || strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("um organigrama que não é o validado tinha de ser recusado (%d), saiu %d\n%s\n%s",
			exitDocumentoRecusado, r.code, r.stdout, r.stderr)
	}
	if pl := correr(t, bin, "plans", "--wal", wal, "--run", run); !strings.Contains(pl.stdout, "estado=PENDENTE") {
		t.Fatalf("o plano de risco tinha de continuar PENDENTE, sem decisão:\n%s", pl.stdout)
	}
}

// aos442PlanDocTruncadoSai10 — A1, no `serve`: um documento que não descodifica é recusa
// determinista, com código próprio.
func aos442PlanDocTruncadoSai10(t *testing.T, bin string) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	doc := filepath.Join(dir, "truncado.json")
	escrever(t, doc, planoFixtureDuasFolhasComSnapshotAOS408[:60])
	r := correr(t, bin, "serve", "--wal", filepath.Join(dir, "es.wal"), "--run", "run-aos442-trunc",
		"--plan-doc", doc, "--snapshot", snap, "--worker", "p1")
	if r.code != exitDocumentoRecusado {
		t.Fatalf("um documento truncado tinha de sair %d, saiu %d\n%s", exitDocumentoRecusado, r.code, r.stderr)
	}
}

// A regra da origem, sem I/O — decidida pelo LOG do run.
func TestAOS442EscolherOrigem(t *testing.T) {
	const doc = "/v/planos/r.plan.json"
	nunca := func() (bool, error) { t.Fatal("exigeHumano não devia ter sido chamado"); return false, nil }
	sim := func() (bool, error) { return true, nil }
	nao := func() (bool, error) { return false, nil }
	ilegivel := func() (bool, error) { return false, errDocumentoDoPlanoRecusado }

	casos := []struct {
		nome        string
		r           retratoDoPedido
		exigeHumano func() (bool, error)
		porDoc      bool
		fixture     string
		erro        error
	}{
		{"run novo: decompõe", retratoDoPedido{}, nunca, false, "fix", nil},
		{"run novo com documento plantado: NÃO o usa, decompõe (M1)", retratoDoPedido{haDocumento: true}, nunca, false, "fix", nil},
		{"validado sem documento: 7 sem serve", retratoDoPedido{validado: true}, nunca, false, "", errDecisaoRecusada},
		{"decidido: pelo documento, e o gate decide", retratoDoPedido{validado: true, haDocumento: true, decidido: true}, nunca, true, "", nil},
		{"decidido e fora do prazo: a decisão manda", retratoDoPedido{validado: true, haDocumento: true, decidido: true, expirado: true}, nunca, true, "", nil},
		{"sem decisão e fora do prazo: 7 sem serve", retratoDoPedido{validado: true, haDocumento: true, expirado: true}, nunca, false, "", errDecisaoRecusada},
		{"sem decisão e exige humano: continua à espera, sem serve", retratoDoPedido{validado: true, haDocumento: true}, sim, false, "", errPlanoPendente},
		{"sem decisão e sem risco: a auto-aprovação ficou a meio", retratoDoPedido{validado: true, haDocumento: true}, nao, true, "", nil},
		{"documento que não descodifica: 10", retratoDoPedido{validado: true, haDocumento: true}, ilegivel, false, "", errDocumentoDoPlanoRecusado},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			o, err := escolherOrigem(doc, c.r, "fix", c.exigeHumano)
			if c.erro != nil {
				if !errors.Is(err, c.erro) {
					t.Fatalf("esperava %v, veio %v", c.erro, err)
				}
			} else if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if o.porDocumento != c.porDoc || o.decomposeFixture != c.fixture || o.documento != doc || o.jaValidado != c.r.validado {
				t.Fatalf("origem %+v", o)
			}
		})
	}
}

// O `serve` recebe UMA das duas origens, nunca as duas — o `--goal` e o `--plan-doc` excluem-se.
func TestAOS442ArgsDoServePorOrigem(t *testing.T) {
	p := pedidoReclamado{RunID: "r", Objective: "o"}
	sub := substrato{wal: "/v/consume.wal"}
	porDoc := argsDoServe("/s.json", p, sub, time.Minute, time.Second, "", origemDoPlano{documento: "/v/d.json", porDocumento: true, decomposeFixture: "f"})
	for _, proibida := range []string{"--goal", "--plan-out", "--decompose-fixture"} {
		if slices.Contains(porDoc, proibida) {
			t.Errorf("a retoma por documento não pode levar %s: %v", proibida, porDoc)
		}
	}
	if i := slices.Index(porDoc, "--plan-doc"); i < 0 || porDoc[i+1] != "/v/d.json" || !slices.Contains(porDoc, "--release") {
		t.Errorf("a retoma por documento leva --plan-doc e --release: %v", porDoc)
	}
	porGoal := argsDoServe("/s.json", p, sub, time.Minute, time.Second, "", origemDoPlano{documento: "/v/d.json"})
	if slices.Contains(porGoal, "--plan-doc") {
		t.Errorf("a decomposição não leva --plan-doc: %v", porGoal)
	}
	if i := slices.Index(porGoal, "--plan-out"); i < 0 || porGoal[i+1] != "/v/d.json" {
		t.Errorf("a decomposição guarda o documento com --plan-out: %v", porGoal)
	}
}

// O nome do documento vem do `run_id`, que vem do nó: tem de ficar DENTRO da pasta, e dois runs
// distintos não podem partilhar ficheiro.
func TestAOS442CaminhoDoDocumentoFicaNaPasta(t *testing.T) {
	pasta := filepath.Join("v", "planos")
	vistos := map[string]string{}
	longo := strings.Repeat("%/", 400) // escapado, passava os 255 bytes de um nome de ficheiro
	for _, run := range []string{"plan-a", "a/../../etc/passwd", "a:b", "a%2Fb", "a/b", `a\b`, "..", "Ω", longo} {
		doc, err := caminhoDoDocumento(pasta, run)
		if err != nil {
			t.Fatalf("%q: %v", run, err)
		}
		if filepath.Dir(doc) != pasta {
			t.Fatalf("o documento de %q saiu da pasta: %s", run, doc)
		}
		if n := len(filepath.Base(doc)); n > 100 {
			t.Fatalf("nome com %d bytes para o run de %d: ENAMETOOLONG à espera de acontecer", n, len(run))
		}
		if outro, dup := vistos[doc]; dup {
			t.Fatalf("%q e %q partilham o ficheiro %s", run, outro, doc)
		}
		vistos[doc] = run
	}
	if _, err := caminhoDoDocumento(pasta, ""); err == nil {
		t.Fatal("run_id vazio tinha de recusar")
	}
}

// Sobre o substrato REPLICADO não há WAL de onde derivar a pasta: exige-se, antes de reclamar.
func TestAOS442PastaDosPlanos(t *testing.T) {
	if p, err := pastaDosPlanos("", substrato{wal: filepath.Join("v", "consume.wal")}); err != nil || p != filepath.Join("v", "planos") {
		t.Fatalf("por omissão, planos/ ao lado do WAL: %q %v", p, err)
	}
	if p, _ := pastaDosPlanos("/x", substrato{nats: "h:4222"}); p != "/x" {
		t.Fatalf("a pasta explícita manda: %q", p)
	}
	if _, err := pastaDosPlanos("", substrato{nats: "h:4222"}); err == nil {
		t.Fatal("sobre --nats sem --plan-dir tinha de recusar")
	}
}

// storeDoPrazo devolve um `plan.validated` com o instante dado — o relógio do Event Store não se
// injecta, e o prazo da política é de um dia.
type storeDoPrazo struct{ validadoEm time.Time }

func (s storeDoPrazo) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	ts := "carimbo-ilegivel"
	if !s.validadoEm.IsZero() {
		ts = s.validadoEm.Format(time.RFC3339Nano)
	}
	return []eventstore.Event{{
		Seq: 1, Type: plannerevents.EventValidated, Ts: ts,
		Payload: json.RawMessage(`{"plan_id":"r-plan","plan_hash":"sha256:h"}`),
	}}, nil
}

func (storeDoPrazo) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	panic("ler o prazo não escreve")
}

// O `serve` recusa um pendente fora do prazo, como o `decide`. FALHA-ANTES: dizia «pendente» para
// sempre — e, com a fila a re-oferecer o pedido, a re-verificação nunca acabava.
func TestAOS442PendenteForaDoPrazoERecusado(t *testing.T) {
	agora := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		validadoEm time.Time
		recusa     bool
	}{
		{agora.Add(-time.Hour), false},
		{agora.Add(-ttlPendentePorOmissao - time.Minute), true},
		// B2 da revisão: sem prazo legível a re-oferta nunca acabava — conta como expirado.
		{time.Time{}, true},
	} {
		leitor, err := runlifecycle.NewPlanDecisionReader(storeDoPrazo{c.validadoEm}, "r-plan")
		if err != nil {
			t.Fatal(err)
		}
		estado, err := leitor.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = recusarPendenteExpirado(estado, agora)
		if c.recusa != errors.Is(err, errDecisaoRecusada) || (!c.recusa && err != nil) {
			t.Fatalf("validado em %s: recusa=%v, veio %v", c.validadoEm, c.recusa, err)
		}
	}
}
