package main

// AOS-263 (PARTE 2) — O PROMPT DE EXAUSTÃO É EMITIDO, VISÍVEL, VARRIDO E NÃO MATA O RUN.
//
// Os quatro critérios desta parte, um bloco por cada, mais os dois que a entrega tem de
// negar (fail-open e opção sem executor):
//
//  1. O aviso de burn-down de AOS-262 SUSPENDE o run em `waiting_on_human` e SELA um pendente
//     de exaustão — pelas peças que já existiam, não por um mecanismo novo;
//  2. o prompt aparece em GET /runs/{id}, no seu campo, com a amarra que o justifica;
//  3. o TTL é varrido pelo sweeper de pendentes JÁ EXISTENTE;
//  4. a SUSPENSÃO REPÕE o `enteredAt` — a deliberação humana não pode matar o run pelo
//     wall-clock de AOS-252. Com a metade não-vacuosa: sem a reposição, o run morreria.
//
// E as negações: uma suspensão que FALHA aborta o run (nunca segue em silêncio, porque o
// sinal é uma-vez-por-run), e o prompt NÃO oferece `extend` (não tem executor).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/budget"
	hitl "github.com/aos-ref/control-plane/governance/hitl"
	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// aos263Harness compõe o caminho REAL: ledger de turnos → observador de burn-down → prompt
// de exaustão → registo durável de pendentes + gate de estado do run. Nada é simulado a não
// ser o relógio de parede (que é real) e o tecto.
type aos263Harness struct {
	prog  *runProgress
	rec   *agentruntime.TurnRecorder
	gates *runStateGates
	pend  *integration.PendingApprovals
	logs  *syncBuf
	// rb é o orçamento por-run que o observador lê — exposto porque os testes do eixo $
	// (AOS-260) precisam de o partilhar com a admissão do turno de modelo, que é precisamente
	// a propriedade «um só tecto, dois pontos de admissão».
	rb *integration.RunBudget
}

// novoAOS263Harness monta tudo com o prompt ARMADO. runWallClock alimenta as máquinas de
// estado abertas (é o tecto de AOS-252 cujo `enteredAt` o critério 4 protege).
func novoAOS263Harness(t *testing.T, maxTokens int64, threshold float64, runWallClock time.Duration) *aos263Harness {
	t.Helper()
	rb, err := integration.NewRunBudget(maxTokens)
	if err != nil {
		t.Fatalf("NewRunBudget: %v", err)
	}
	return novoAOS263HarnessSobre(t, rb, threshold, runWallClock)
}

// novoAOS263HarnessSobre é o mesmo harness sobre um orçamento JÁ CONSTRUÍDO — o que permite
// exercer um tecto em DÓLARES ([integration.WithMaxCostMicroUSDPerRun]) sem duplicar a
// composição.
func novoAOS263HarnessSobre(t *testing.T, rb *integration.RunBudget, threshold float64, runWallClock time.Duration) *aos263Harness {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	pend, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	resumeRecords, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	logs := &syncBuf{}
	log := func(format string, args ...any) { fmt.Fprintf(logs, "[aos] "+format+"\n", args...) }
	gates := newRunStateGates(es, nil, runWallClock)
	auth, worm := aos263RotaDeDecisao(t, es)
	prompt, err := newExhaustionPrompt(gates, pend, resumeRecords, auth, worm, log)
	if err != nil {
		t.Fatalf("newExhaustionPrompt: %v", err)
	}
	if prompt == nil {
		t.Fatal("com a maquinaria HITL composta o prompt TEM de armar")
	}
	prog := newRunProgress(gates, rb, newTurnLedgerBurndown(es), otelgenai.NoopTracer{}, threshold, log, prompt)
	if prog == nil {
		t.Fatal("o observador tinha de ser composto")
	}
	return &aos263Harness{prog: prog, rec: agentruntime.NewTurnRecorder(es), gates: gates, pend: pend, logs: logs, rb: rb}
}

// aos263RotaDeDecisao compõe as duas peças da ROTA DE DECISÃO (AOS-263 parte 3) de que o
// prompt agora depende para armar: o autenticador ed25519 COM um operador pinado e um WORM.
// São as mesmas peças reais que o nó compõe — um autenticador vazio ou um WORM ausente
// deixariam o prompt DESARMADO, que é o que [TestAOS263_SemMaquinariaHITLNaoHaPrompt] sela.
func aos263RotaDeDecisao(t *testing.T, es *eventstore.Store) (*integration.Ed25519Authenticator, audit.Store) {
	t.Helper()
	auth, err := integration.NewEd25519Authenticator(hitl.NewEventStoreNonceStore(es), 5*time.Minute)
	if err != nil {
		t.Fatalf("NewEd25519Authenticator: %v", err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	auth.Register("human:operator-harness", pub)
	return auth, audit.NewMemStore()
}

// abreEReclama abre o gate do run e reclama ready→running, que é o que o loop de serviço faz
// no arranque de um run (AOS-251). Sem o claim não haveria de onde suspender.
func (h *aos263Harness) abreEReclama(t *testing.T, runID string) *runGate {
	t.Helper()
	ctx := context.Background()
	if err := h.gates.Open(ctx, runID, state.Uint64Token(1)); err != nil {
		t.Fatalf("gates.Open: %v", err)
	}
	gate := h.gates.resolveGate(runID)
	if gate == nil {
		t.Fatal("gate do run devia existir depois do Open")
	}
	if err := gate.claimRunning(ctx); err != nil {
		t.Fatalf("claimRunning: %v", err)
	}
	return gate
}

// queimaTurno grava UM turno no ledger REAL e observa a fronteira de fim-de-turno, devolvendo
// o erro que o loop veria.
func (h *aos263Harness) queimaTurno(t *testing.T, runID string, turn int, tokens int64) error {
	t.Helper()
	gravaTurno(t, h.rec, runID, fmt.Sprintf("step-%d", turn), turn, tokens, 0, 0)
	return h.prog.ObserveProgress(context.Background(), runID, turn)
}

// exaustoesDe devolve só os pendentes de exaustão do run.
func (h *aos263Harness) exaustoesDe(t *testing.T, runID string) []integration.PendingRecord {
	t.Helper()
	recs, err := h.pend.ListForRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	var out []integration.PendingRecord
	for _, r := range recs {
		if r.Kind.Resolved() == integration.PendingKindExhaustion {
			out = append(out, r)
		}
	}
	return out
}

// --- (1) O aviso passa a ter consequência -------------------------------------------

// TestAOS263_AvisoSuspendeORunESelaOPrompt é o critério central. Abaixo do limiar o run corre
// como sempre (o contraste que impede o teste de ser vacuoso); ao cruzá-lo, o observador
// devolve o sinal de suspensão, a máquina de estados fica em waiting_on_human e o pendente de
// exaustão está selado com a amarra que o justifica — e SEM preview.
func TestAOS263_AvisoSuspendeORunESelaOPrompt(t *testing.T) {
	h := novoAOS263Harness(t, 1000, 0.80, 0)
	const run = "run-263-suspende"
	gate := h.abreEReclama(t, run)

	// Turnos 1-3: 600/1000 = 60% — abaixo do limiar, nada acontece.
	for turn := 1; turn <= 3; turn++ {
		if err := h.queimaTurno(t, run, turn, 200); err != nil {
			t.Fatalf("abaixo do limiar o run NAO pode ser tocado (turno %d): %v", turn, err)
		}
	}
	if got := gate.m.Current(); got != state.Running {
		t.Fatalf("abaixo do limiar o run continua em running; esta em %q", got)
	}
	if n := len(h.exaustoesDe(t, run)); n != 0 {
		t.Fatalf("abaixo do limiar nao ha prompt nenhum; havia %d", n)
	}

	// Turno 4: 800/1000 = 80% — cruza o limiar.
	err := h.queimaTurno(t, run, 4, 200)
	if !errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("ao cruzar o limiar o observador tem de devolver o sinal de SUSPENSAO; deu %v", err)
	}
	if got := gate.m.Current(); got != state.WaitingOnHuman {
		t.Fatalf("o run tinha de suspender em waiting_on_human; esta em %q", got)
	}

	prompts := h.exaustoesDe(t, run)
	if len(prompts) != 1 {
		t.Fatalf("tinha de ficar selado EXACTAMENTE 1 prompt de exaustao; ficaram %d", len(prompts))
	}
	p := prompts[0]
	if p.Turn != 4 || p.Threshold != 0.80 || p.ConsumedTokens != 800 || p.LimitTokens != 1000 {
		t.Fatalf("a amarra do prompt tem de ser a do aviso (turno, limiar, consumido, tecto): %+v", p)
	}
	if len(p.Preview) != 0 {
		t.Fatalf("um prompt de exaustao NAO tem preview (nao ha efeito exibido para assinar): %+v", p)
	}
	if p.CreatedAt == "" {
		t.Fatal("sem CreatedAt o prompt nunca expiraria sozinho — a ancora do TTL e obrigatoria")
	}
	// A linha do log nomeia o run, os números e a ausência de `extend` (o operador lê isto
	// antes de ler qualquer API).
	for _, marcador := range []string{"PROMPT DE EXAUSTAO", run, "800 de 1000", "NAO se oferece extend"} {
		if !strings.Contains(h.logs.String(), marcador) {
			t.Errorf("o log devia conter %q:\n%s", marcador, h.logs.String())
		}
	}
}

// TestAOS263_SegundoPromptNaoSeSobrepoeAoPrimeiro: um run RETOMADO com a pergunta ainda em
// aberto não volta a suspender-se. A memória é o registo DURÁVEL (o mesmo que o operador vê),
// não um latch em memória — por isso resiste ao restart do processo, que é exactamente quando
// o latch do aviso de AOS-262 se perde.
func TestAOS263_SegundoPromptNaoSeSobrepoeAoPrimeiro(t *testing.T) {
	h := novoAOS263Harness(t, 1000, 0.80, 0)
	const run = "run-263-retomado"
	gate := h.abreEReclama(t, run)

	if err := h.queimaTurno(t, run, 1, 900); !errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("primeira travessia devia suspender: %v", err)
	}
	// A retoma: o run volta a running e o latch do aviso é limpo (nova incarnação da
	// superfície), tal como acontece quando o run é re-hospedado.
	if err := gate.ResumeFromHuman(context.Background(), "retoma do operador com a pergunta em aberto"); err != nil {
		t.Fatalf("ResumeFromHuman: %v", err)
	}
	h.prog.forget(run)

	if err := h.queimaTurno(t, run, 2, 50); err != nil {
		t.Fatalf("com a pergunta em aberto o run NAO se volta a suspender: %v", err)
	}
	if got := gate.m.Current(); got != state.Running {
		t.Fatalf("o run devia continuar a correr; esta em %q", got)
	}
	if n := len(h.exaustoesDe(t, run)); n != 1 {
		t.Fatalf("continua a haver UMA pergunta, nao duas; ha %d", n)
	}
	if !strings.Contains(h.logs.String(), "JA existe um por responder") {
		t.Errorf("o log tem de dizer porque nao suspendeu:\n%s", h.logs.String())
	}
}

// --- (2) O prompt aparece em GET /runs/{id} -----------------------------------------

// TestAOS263_PromptApareceNoWireDoRun: a projecção para a superfície de administração. O
// prompt sai no SEU campo (nunca como aprovação, que teria uma preview vazia para assinar) e
// traz só opções COM executor.
func TestAOS263_PromptApareceNoWireDoRun(t *testing.T) {
	h := novoAOS263Harness(t, 1000, 0.80, 0)
	const run = "run-263-wire"
	h.abreEReclama(t, run)
	if err := h.queimaTurno(t, run, 1, 950); !errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("devia suspender: %v", err)
	}
	// E uma APROVAÇÃO pendente no mesmo run, para provar que os dois tipos não se misturam.
	if err := h.pend.Put(context.Background(), integration.PendingRecord{
		RunID: run, StepID: "s1-tool-1", Turn: 1,
		ToolID: "web_post", Capability: "cap:http.post",
		Preview:   []byte{0x01, 0x02},
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("Put(aprovacao): %v", err)
	}

	api := &apiHandler{node: &Node{PendingApprovals: h.pend}, svc: &NodeService{logw: io.Discard}}
	aprovacoes, exaustoes, indisponivel := api.pendingFor(context.Background(), run)
	if indisponivel {
		t.Fatal("a leitura correu bem — nao se pode anunciar indisponibilidade")
	}
	if len(aprovacoes) != 1 || aprovacoes[0].ToolID != "web_post" {
		t.Fatalf("a aprovacao tem de continuar na sua face de wire: %+v", aprovacoes)
	}
	if len(exaustoes) != 1 {
		t.Fatalf("o prompt de exaustao tinha de aparecer no seu campo; deu %+v", exaustoes)
	}
	w := exaustoes[0]
	if w.Turn != 1 || w.Threshold != 0.80 || w.ConsumedTokens != 950 || w.LimitTokens != 1000 {
		t.Fatalf("o wire tem de trazer o PORQUE da pergunta: %+v", w)
	}
	if w.CreatedAt == "" {
		t.Fatalf("o wire tem de trazer a ancora do TTL: %+v", w)
	}

	// A resposta de estado que o GET escreve: o campo existe, tem nome próprio e o prompt
	// não aparece dentro de pending_approvals.
	corpo, err := json.Marshal(runStateResponse{
		RunID: run, Status: "waiting_on_human",
		PendingApprovals: aprovacoes, PendingExhaustion: exaustoes,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var resp struct {
		PendingApprovals  []map[string]any `json:"pending_approvals"`
		PendingExhaustion []struct {
			Threshold float64 `json:"threshold"`
			Options   []struct {
				ID    string `json:"id"`
				Route string `json:"route"`
			} `json:"options"`
		} `json:"pending_exhaustion"`
	}
	if err := json.Unmarshal(corpo, &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(resp.PendingExhaustion) != 1 || len(resp.PendingApprovals) != 1 {
		t.Fatalf("cada tipo no seu campo: %s", corpo)
	}
	if _, tem := resp.PendingApprovals[0]["threshold"]; tem {
		t.Fatalf("a face de wire da aprovacao nao pode ganhar campos de exaustao: %s", corpo)
	}

	// AS OPÇÕES SÃO AS DUAS METADES DA PERGUNTA, E AMBAS NA MESMA ROTA ASSINADA: `continue` e
	// `abort` (AOS-263 parte 3). A simetria é o critério — uma opção «continuar» que apontasse
	// para a retoma (`POST /runs/{id}/resume`, sem assinatura de operador e sem selo) daria a
	// resposta ARRISCADA por menos autoridade do que a segura. `extend` sai por decisão do dono
	// (iii) e `summarize_stop` não tem caminho de resumo no loop — nenhuma pode aparecer.
	opcoes := resp.PendingExhaustion[0].Options
	rotaDe := map[string]string{}
	for _, o := range opcoes {
		rotaDe[o.ID] = o.Route
	}
	if len(opcoes) != 2 || rotaDe[exhaustionOptionContinue] != exhaustionDecisionRoute || rotaDe[exhaustionOptionAbort] != exhaustionDecisionRoute {
		t.Fatalf("as opcoes apresentadas sao EXACTAMENTE as duas decisoes da rota assinada (%s e %s → %s); deu %+v",
			exhaustionOptionContinue, exhaustionOptionAbort, exhaustionDecisionRoute, opcoes)
	}
	if strings.Contains(string(corpo), exhaustionResumeRoute) {
		t.Fatalf("a RETOMA nao e uma opcao do prompt (e a execucao de um continue ja decidido): %s", corpo)
	}
	for _, proibida := range []string{"extend", "summarize_stop"} {
		if strings.Contains(string(corpo), proibida) {
			t.Fatalf("o prompt NAO pode apresentar %q — nao tem executor: %s", proibida, corpo)
		}
	}
	// E a rota apresentada TEM de existir no mux (uma opção cuja rota não está registada é uma
	// promessa, que é exactamente o que AOS-262 se recusou a fazer).
	//
	// Consulta a TABELA DE ROTAS real, e já não o texto do api.go: a verificação textual partiu-se
	// quando o registo passou a ser uma tabela, e teria dito "rota em falta" sobre uma rota que
	// estava lá — o modo de falha errado, porque acusa em vez de avisar.
	registada := false
	for _, rt := range (&apiHandler{}).tabelaDeRotas() {
		if rt.padrao == "POST /runs/{id}/exhaustion" {
			registada = true
			// E tem de ser plano de CONTROLO: a decisão de exaustão muda o destino de um run.
			if rt.plano != planoControlo {
				t.Errorf("a rota de exaustao esta em %v, tem de ser controlo", rt.plano)
			}
		}
	}
	if !registada {
		t.Error("a rota das opcoes apresentadas tem de estar REGISTADA no mux")
	}
}

// TestAOS263_LeituraDePendentesIndisponivelEDeclarada: o fail-open da leitura deixou de ser
// MUDO. Um erro a ler o registo de pendentes já não é indistinguível de «não há nada por
// decidir» — e a distinção importa porque as duas pedem acções opostas (esperar vs.
// investigar) num run que está PARADO à espera de uma resposta.
func TestAOS263_LeituraDePendentesIndisponivelEDeclarada(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	falha := errors.New("substrato indisponivel")
	pend, err := integration.NewPendingApprovals(&storeSemLeitura{falha: falha})
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	logs := &syncBuf{}
	api := &apiHandler{node: &Node{PendingApprovals: pend}, svc: &NodeService{logw: logs}}

	aprov, exaust, indisponivel := api.pendingFor(context.Background(), "run-263-cego")
	if !indisponivel {
		t.Fatal("uma leitura FALHADA tem de ser declarada — senao a ausencia mente")
	}
	if aprov != nil || exaust != nil {
		t.Fatalf("nada se inventa a partir de uma leitura falhada: %+v %+v", aprov, exaust)
	}
	if !strings.Contains(logs.String(), "run-263-cego") {
		t.Errorf("a falha tem de ficar no log do no com o run: %s", logs.String())
	}
	// E o campo viaja na resposta de estado (o operador lê-o sem ter de ir ao log do nó).
	corpo, err := json.Marshal(runStateResponse{RunID: "run-263-cego", Status: "waiting_on_human", PendingUnavailable: indisponivel})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(corpo), `"pending_unavailable":true`) {
		t.Fatalf("a resposta tem de declarar a indisponibilidade: %s", corpo)
	}
	// NÃO-VACUIDADE: no caminho normal o campo NÃO aparece (é omitempty — nada de alarmes
	// permanentes numa resposta saudável).
	limpo, err := json.Marshal(runStateResponse{RunID: "run-263-cego", Status: "waiting_on_human"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(limpo), "pending_unavailable") {
		t.Fatalf("sem falha o campo nao pode aparecer: %s", limpo)
	}
}

// storeSemLeitura recusa LER (e escrever) — o substrato que não serve consultas.
type storeSemLeitura struct{ falha error }

func (s *storeSemLeitura) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, s.falha
}

func (s *storeSemLeitura) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return nil, s.falha
}

// TestAOS263_SemMaquinariaHITLNaoHaPrompt: o prompt SÓ arma onde tudo o que ele implica
// existe. Sem four-eyes não há registo de pendentes nem de retoma (não há a quem perguntar nem
// como re-hospedar); sem ROTA DE DECISÃO composta — operador pinado no autenticador e WORM —
// ninguém poderia responder e a resposta não poderia ser selada, e o prompt suspenderia runs
// para uma pergunta sem via, cuja única saída seria esperar pelo TTL. Em qualquer dos casos
// fica DESARMADO (sem erro) e o nó comporta-se como em AOS-262.
func TestAOS263_SemMaquinariaHITLNaoHaPrompt(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	gates := newRunStateGates(es, nil, 0)
	pend, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	rr, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	auth, worm := aos263RotaDeDecisao(t, es)
	// Autenticador SEM emissores: existe, mas não tem pubkey pinada nenhuma — nenhuma decisão
	// poderia ser autenticada por ele (default-deny de AOS-160).
	semOperadores, err := integration.NewEd25519Authenticator(hitl.NewEventStoreNonceStore(es), 5*time.Minute)
	if err != nil {
		t.Fatalf("NewEd25519Authenticator: %v", err)
	}

	for _, caso := range []struct {
		nome string
		pend *integration.PendingApprovals
		rr   *integration.ResumeRecords
		auth *integration.Ed25519Authenticator
		worm audit.Store
	}{
		{"sem pendentes nem retoma", nil, nil, auth, worm},
		{"sem registo de pendentes", nil, rr, auth, worm},
		{"sem registo de retoma (nao haveria como re-hospedar)", pend, nil, auth, worm},
		{"sem autenticador (ninguem poderia responder)", pend, rr, nil, worm},
		{"com autenticador SEM operadores pinados", pend, rr, semOperadores, worm},
		{"sem WORM (a resposta nao poderia ser selada)", pend, rr, auth, nil},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			p, err := newExhaustionPrompt(gates, caso.pend, caso.rr, caso.auth, caso.worm, nil)
			if err != nil {
				t.Fatalf("a ausencia de uma peca DESARMA, nao e erro: %v", err)
			}
			if p != nil {
				t.Fatal("o prompt nao pode armar sem a maquinaria completa")
			}
			// Desarmado é NIL-SAFE em todo o caminho quente.
			if err := p.raise(context.Background(), "run-x", progressEvalDeAviso(4, 0.8, 800, 1000), "limiar de burn-down cruzado (razao de teste)"); err != nil {
				t.Fatalf("desarmado tem de ser um no-op: %v", err)
			}
		})
	}

	// NÃO-VACUIDADE: com as quatro peças, arma.
	if p, err := newExhaustionPrompt(gates, pend, rr, auth, worm, nil); err != nil || p == nil {
		t.Fatalf("com tudo composto o prompt TEM de armar; p=%v err=%v", p != nil, err)
	}

	// A cablagem INCOERENTE (há a quem perguntar, mas não há como suspender) é ERRO de
	// arranque — desarmar em silêncio esconderia um defeito de composição.
	if _, err := newExhaustionPrompt(nil, pend, rr, auth, worm, nil); !errors.Is(err, ErrExhaustionPromptUnwired) {
		t.Fatalf("sem gates de estado a composicao tem de FALHAR; deu %v", err)
	}
}

// --- (3) O TTL é varrido pelo sweeper JÁ EXISTENTE ----------------------------------

// TestAOS263_TTLVarridoPeloSweeperDePendentes: nenhum varrimento novo. O prompt esquecido
// envelhece e é expirado pelo MESMO [NodeService.sweepApprovalsOnce] das aprovações — e não
// volta a aparecer ao varrimento seguinte (senão o sweeper re-tentaria o mesmo registo para
// sempre).
func TestAOS263_TTLVarridoPeloSweeperDePendentes(t *testing.T) {
	svc, pend := newSweeperHarness(t, 15*time.Minute)
	ctx := context.Background()
	const run = "run-263-ttl"

	velho := integration.PendingRecord{
		Kind: integration.PendingKindExhaustion, RunID: run, StepID: exhaustionStepID(4, time.Now(), "4444444444444444"), Turn: 4,
		Threshold: 0.8, ConsumedTokens: 800, LimitTokens: 1000,
		CreatedAt: time.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339Nano),
	}
	if err := pend.Put(ctx, velho); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if lista, _ := pend.ListForRun(ctx, run); len(lista) != 1 {
		t.Fatalf("antes do varrimento a pergunta esta pendente; n=%d", len(lista))
	}

	svc.SweepApprovalsNow(ctx)

	lista, err := pend.ListForRun(ctx, run)
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(lista) != 0 {
		t.Fatalf("passado o TTL a pergunta tem de EXPIRAR; ficou %+v", lista)
	}
	expiraveis, err := pend.ListExpirable(ctx, time.Now(), 15*time.Minute)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	if len(expiraveis) != 0 {
		t.Fatalf("um prompt expirado nao pode voltar ao varrimento: %+v", expiraveis)
	}
}

// --- (4) A suspensão repõe o enteredAt ----------------------------------------------

// TestAOS263_SuspensaoRepoeEnteredAtEADeliberacaoNaoMataORun é o critério explícito do
// ticket, com as duas metades:
//
//   - NÃO-VACUOSIDADE: um run que fica em `running` além do tecto de wall-clock de AOS-252 é
//     mesmo morto pelo varrimento de deadlines. O tecto é real;
//   - A GARANTIA: um run SUSPENSO pelo prompt atravessa esse mesmo tempo sem morrer — porque
//     a suspensão é uma TRANSIÇÃO DURÁVEL, e cada transição carimba o `enteredAt`. Ao
//     retomar, o relógio do wall-clock recomeça: o run não paga a deliberação humana.
func TestAOS263_SuspensaoRepoeEnteredAtEADeliberacaoNaoMataORun(t *testing.T) {
	const wall = 60 * time.Millisecond
	const delibera = 200 * time.Millisecond // BEM acima do tecto: a deliberação é lenta
	ctx := context.Background()
	h := novoAOS263Harness(t, 1000, 0.80, wall)

	// (a) NÃO-VACUOSIDADE — um run que só corre morre pelo tecto.
	const controlo = "run-263-controlo"
	gateControlo := h.abreEReclama(t, controlo)
	time.Sleep(delibera)
	st, disparou, err := gateControlo.m.CheckDeadlines(ctx)
	if err != nil {
		t.Fatalf("CheckDeadlines(controlo): %v", err)
	}
	if !disparou || st != state.TimedOut {
		t.Fatalf("o tecto de wall-clock TEM de ser real, senao este teste nao prova nada; st=%q disparou=%v", st, disparou)
	}

	// (b) A GARANTIA — o mesmo tempo, mas em deliberação.
	const run = "run-263-entered-at"
	gate := h.abreEReclama(t, run)
	entradaEmRunning := gate.m.EnteredAt()

	// O run CORRE `delibera` antes de cruzar o limiar. Duas razões, e as duas importam:
	//
	//   - o carimbo da suspensão fica separado do de `running` por um intervalo REAL e grande
	//     (200 ms). Sem esta espera os dois carimbos caem no MESMO tick do relógio de parede —
	//     o `enteredAt` é gravado sem componente monotónica (ver state.Machine.EnteredAt) e a
	//     granularidade do wall-clock chega a ser de milissegundos —, e a asserção mediria a
	//     resolução do relógio em vez da garantia. Assim é determinista em qualquer máquina;
	//   - o run chega à suspensão JÁ com o tecto de wall-clock de `running` estourado, que é o
	//     caso interessante: se a transição não repusesse o `enteredAt`, o run suspenso estaria
	//     morto à nascença e a deliberação abaixo mata-lo-ia de imediato.
	time.Sleep(delibera)

	if err := h.queimaTurno(t, run, 1, 900); !errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("devia suspender: %v", err)
	}
	if got := gate.m.Current(); got != state.WaitingOnHuman {
		t.Fatalf("estado depois da suspensao: %q", got)
	}
	entradaEmEspera := gate.m.EnteredAt()
	if !entradaEmEspera.After(entradaEmRunning) {
		t.Fatalf("a suspensao TEM de repor o enteredAt (running=%s, espera=%s)", entradaEmRunning, entradaEmEspera)
	}
	if decorrido := entradaEmEspera.Sub(entradaEmRunning); decorrido < delibera {
		t.Fatalf("o enteredAt da espera tem de ser o instante da SUSPENSAO, nao o de running (decorrido=%s, esperado >= %s)", decorrido, delibera)
	}

	time.Sleep(delibera) // o humano a pensar, muito para lá do tecto de `running`

	st, disparou, err = gate.m.CheckDeadlines(ctx)
	if err != nil {
		t.Fatalf("CheckDeadlines(suspenso): %v", err)
	}
	if disparou || st != state.WaitingOnHuman {
		t.Fatalf("a DELIBERACAO nao pode matar o run pelo wall-clock; st=%q disparou=%v", st, disparou)
	}

	// E a retoma recomeça o relógio: o run não arranca já morto.
	if err := gate.ResumeFromHuman(ctx, "decidido pelo operador"); err != nil {
		t.Fatalf("ResumeFromHuman: %v", err)
	}
	if got := gate.m.EnteredAt(); !got.After(entradaEmEspera) {
		t.Fatalf("a retoma tem de repor o enteredAt outra vez (espera=%s, retoma=%s)", entradaEmEspera, got)
	}
	st, disparou, err = gate.m.CheckDeadlines(ctx)
	if err != nil {
		t.Fatalf("CheckDeadlines(retomado): %v", err)
	}
	if disparou || st != state.Running {
		t.Fatalf("logo apos a retoma o run tem o tecto INTEIRO pela frente; st=%q disparou=%v", st, disparou)
	}
}

// --- Fail-closed: uma suspensão que falha NÃO segue em silêncio ----------------------

// storeSemEscrita deixa LER o stream de pendentes e recusa qualquer escrita — reproduz o
// substrato que ainda serve leituras mas já não aceita appends.
type storeSemEscrita struct {
	inner *eventstore.Store
	falha error
}

func (s *storeSemEscrita) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, s.falha
}

func (s *storeSemEscrita) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return s.inner.Read(ctx, streamID, fromSeq)
}

// TestAOS263_SuspensaoFalhadaAbortaORun sela a resposta à pergunta «e se a suspensão falhar?».
// O sinal que alimenta o prompt é emitido UMA VEZ POR RUN: se a falha fosse engolida — a
// postura natural de um aviso — o run corria até ao fim sem nunca ser perguntado, com o
// operador à espera de um prompt que nunca apareceria. Por isso o erro SOBE (o loop aborta o
// run) e NÃO é o sinal de suspensão, para o serviço o arquivar como falha e não como espera.
func TestAOS263_SuspensaoFalhadaAbortaORun(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	falha := errors.New("substrato recusa escritas")
	pend, err := integration.NewPendingApprovals(&storeSemEscrita{inner: es, falha: falha})
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	rr, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	gates := newRunStateGates(es, nil, 0)
	auth, worm := aos263RotaDeDecisao(t, es)
	prompt, err := newExhaustionPrompt(gates, pend, rr, auth, worm, nil)
	if err != nil {
		t.Fatalf("newExhaustionPrompt: %v", err)
	}
	const run = "run-263-falha"
	if err := gates.Open(context.Background(), run, state.Uint64Token(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = prompt.raise(context.Background(), run, progressEvalDeAviso(4, 0.8, 800, 1000), "limiar de burn-down cruzado (razao de teste)")
	if err == nil {
		t.Fatal("uma suspensao que nao se consegue selar NAO pode devolver nil — seria fail-open com o sinal ja gasto")
	}
	if errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("uma FALHA nao pode passar por suspensao bem sucedida: %v", err)
	}
	if !errors.Is(err, falha) {
		t.Fatalf("o erro tem de preservar a causa: %v", err)
	}
	// E o run NÃO ficou meio-suspenso: sem pendente selado, nada transitou.
	if got := gates.resolveGate(run).m.Current(); got == state.WaitingOnHuman {
		t.Fatal("sem o pendente selado o run NAO pode ficar suspenso — o operador nao teria o que decidir")
	}
}

// TestAOS263_SemGateNaoSuspendeEFalhaFechada: a outra metade do fail-closed — se não há
// máquina de estados aberta, não há suspensão possível e o run não pode seguir como se nada
// tivesse ficado por decidir. Mesma postura do [nodeEscalationSink].
func TestAOS263_SemGateNaoSuspendeEFalhaFechada(t *testing.T) {
	h := novoAOS263Harness(t, 1000, 0.80, 0)
	const run = "run-263-sem-gate" // gate NÃO aberto
	err := h.prog.ObserveProgress(context.Background(), run, 1)
	if err == nil {
		t.Fatal("sem gate nao ha suspensao possivel — tem de falhar fechado")
	}
	// A ausência de ledger dá cegueira (o run aborta na mesma); com ledger, o erro é o do
	// gate ausente. Ambos são fatais, e é isso que se sela.
	gravaTurno(t, h.rec, run, "s1", 1, 900, 0, 0)
	err = h.prog.ObserveProgress(context.Background(), run, 1)
	if !errors.Is(err, ErrNoStateGateForRun) {
		t.Fatalf("devia ser ErrNoStateGateForRun; deu %v", err)
	}
}

// --- A saída do run: suspensão, não falha -------------------------------------------

// TestAOS263_ServicoAbsorveOSinalComoSuspensao: o sinal de suspensão viaja como `error`
// porque a porta do observador só sabe devolver isso — mas o run NÃO é uma falha. O serviço
// converte-o na MESMA contabilidade de AOS-021 (registo de retoma persistido, suspenso=true),
// e um erro qualquer continua a ser um erro.
func TestAOS263_ServicoAbsorveOSinalComoSuspensao(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rr, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	svc := &NodeService{node: &Node{ResumeRecords: rr}, logw: io.Discard}
	goal := agentruntime.Goal{RunID: "run-263-absorve", Objective: "trabalho"}
	ctx := context.Background()

	// (a) O sinal ⇒ SUSPENSO, sem erro retido, e o run fica RETOMÁVEL (há registo).
	sinal := fmt.Errorf("%w: run %q", errExhaustionSuspended, goal.RunID)
	suspenso, retido := svc.absorveSuspensaoPorExaustao(ctx, goal, sinal)
	if !suspenso || retido != nil {
		t.Fatalf("o sinal tem de virar SUSPENSAO sem erro; suspenso=%v err=%v", suspenso, retido)
	}
	if _, ok, gerr := rr.Get(ctx, goal.RunID); gerr != nil || !ok {
		t.Fatalf("sem registo de retoma o run suspenso seria irretomavel; ok=%v err=%v", ok, gerr)
	}

	// (b) Um erro que NÃO é o sinal atravessa intacto.
	outro := errors.New("modelo indisponivel")
	if suspenso, retido := svc.absorveSuspensaoPorExaustao(ctx, goal, outro); suspenso || !errors.Is(retido, outro) {
		t.Fatalf("um erro normal continua a ser falha; suspenso=%v err=%v", suspenso, retido)
	}

	// (c) FAIL-CLOSED: sem registo de retoma composto, um "suspenso" seria irretomável — o
	// run fica FALHADO, com a causa preservada.
	semRetoma := &NodeService{node: &Node{}, logw: io.Discard}
	if suspenso, retido := semRetoma.absorveSuspensaoPorExaustao(ctx, goal, sinal); suspenso || !errors.Is(retido, errExhaustionSuspended) {
		t.Fatalf("sem registo de retoma nao ha suspensao; suspenso=%v err=%v", suspenso, retido)
	}
}

// --- Postura anunciada = postura ligada ----------------------------------------------

// TestAOS263_BannerDeclaraOPromptEDerivaDaComposicao: a linha do banner não pode ser um
// literal optimista, e tem de dizer as cinco coisas que o operador precisa de saber. É a
// mesma disciplina de AOS-248/AOS-262 aplicada a esta entrega.
func TestAOS263_BannerDeclaraOPromptEDerivaDaComposicao(t *testing.T) {
	t.Parallel()

	armado := strings.Join(exhaustionPromptPostureBanner(true, 15*time.Minute), "\n")
	for _, marcador := range []string{
		"ARMADO",
		"waiting_on_human",      // o que acontece
		"pending_exhaustion",    // onde se ve
		exhaustionResumeRoute,   // a opcao COM executor
		"NAO se oferece extend", // a opcao que nao tem
		"repoe o enteredAt",     // a deliberacao nao mata o run
		"15m0s",                 // o TTL REAL, nao um numero inventado
		"SE A SUSPENSAO FALHAR", // o fail-closed
	} {
		if !strings.Contains(armado, marcador) {
			t.Errorf("o banner do prompt ARMADO devia conter %q:\n%s", marcador, armado)
		}
	}
	desarmado := strings.Join(exhaustionPromptPostureBanner(false, 15*time.Minute), "\n")
	for _, marcador := range []string{"NAO ARMADO", "o run CONTINUA", "four-eyes"} {
		if !strings.Contains(desarmado, marcador) {
			t.Errorf("o banner do prompt DESARMADO devia conter %q:\n%s", marcador, desarmado)
		}
	}

	// O argumento do banner é o estado REALMENTE composto — nunca um literal.
	fonte, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("ler service.go: %v", err)
	}
	src := string(fonte)
	if !strings.Contains(src, "exhaustionPromptPostureBanner(node.progress.promptArmed()") {
		t.Error("o banner tem de derivar do prompt REALMENTE composto no observador de burn-down")
	}
	if strings.Contains(src, "exhaustionPromptPostureBanner(true") || strings.Contains(src, "exhaustionPromptPostureBanner(false") {
		t.Error("exhaustionPromptPostureBanner foi chamado com um literal")
	}
	// E o prompt que o banner descreve é o que o observador consulta.
	boot, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("ler bootstrap.go: %v", err)
	}
	if !strings.Contains(string(boot), "progressThreshold, log, exhaustion)") {
		t.Error("o prompt composto tem de ser o entregue ao observador de burn-down")
	}
}

// progressEvalDeAviso fabrica a avaliação que a superfície de burn-down devolve ao cruzar o
// limiar — o SINAL, tal como o observador o recebe. Usada só onde o caminho real do ledger
// não é o objecto do teste.
func progressEvalDeAviso(turn int, threshold float64, consumido, tecto int64) progresssurface.RunEvaluation {
	return progresssurface.RunEvaluation{
		Burndown: progresssurface.Burndown{
			Consumed: budget.Amount{Tokens: consumido},
			Limit:    budget.Amount{Tokens: tecto},
			Fraction: float64(consumido) / float64(tecto),
		},
		Warning: &progresssurface.BudgetWarning{
			RunID: "", Turn: turn, Threshold: threshold,
			Fraction: float64(consumido) / float64(tecto), SpanEmitted: true,
		},
		State: progresssurface.PromptWarned,
	}
}
