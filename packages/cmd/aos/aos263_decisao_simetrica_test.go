package main

// AOS-263 (REMEDIAÇÃO) — AS DUAS METADES DA PERGUNTA, E O RUN QUE NUNCA FICA SEM VIA.
//
// A parte 3 provou o `abort` pela rota. Faltavam as provas da OUTRA metade e das duas maneiras
// de o run ficar preso — os achados que a auditoria isolou:
//
//  1. `continue` é uma DECISÃO, com a mesma autoridade do `abort` (assinatura de operador
//     pinado + nonce durável + selo WORM), e é ela que destranca a retoma. Sem ela, «deixar
//     queimar orçamento acima do limiar» seria a única resposta que dispensava assinatura e
//     registo — a decisão arriscada mais barata do que a segura;
//  2. a RETOMA fica barrada enquanto a pergunta estiver por responder, com a escotilha do TTL
//     para o travão nunca virar prisão;
//  3. duas perguntas do MESMO run não colidem (o contador de turnos reinicia por incarnação),
//     e uma colisão, se acontecesse, falharia ALTO em vez de deixar uma pergunta invisível;
//  4. um `abort` selado cujo efeito falha ganha registo COMPENSATÓRIO na mesma cadeia — a
//     desmentida não vive fora do não-repúdio.
//
// Como em aos263_exhaustion_decision_test.go, tudo o que envolve autoridade passa PELA ROTA.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------
// (1) A metade «continuar» tem a MESMA autoridade — e é ela que destranca a retoma
// ---------------------------------------------------------------------------

// TestAOS263_ContinuePelaRotaSelaEDestrancaARetoma tem o contraste nas pontas: ANTES da
// decisão o `POST /resume` é RECUSADO (409) apesar de o run estar suspenso e reconstituível;
// DEPOIS é aceite. É esse travão que impede a metade arriscada da decisão de ser tomada por
// quem só tem uma credencial NHI — a mesma classe de credencial com que o run já corria.
func TestAOS263_ContinuePelaRotaSelaEDestrancaARetoma(t *testing.T) {
	node, svc, h, opPriv := aos263Node(t)
	const run = "run-263-continue"
	prompt := aos263Suspende(t, node, run, 4, 820, 1000)
	aos263TornaRetomavel(t, node, svc, run)

	// --- ANTES: a retoma está BARRADA pela pergunta por responder ---
	rec := postJSON(h, "POST", "/runs/"+run+"/resume", map[string]any{"credential": "cred-fresca"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("com a pergunta POR RESPONDER a retoma tem de recusar com 409, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), exhaustionDecisionRoute) {
		t.Fatalf("a recusa tem de NOMEAR a rota que destranca o run: %s", rec.Body.String())
	}
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("a recusa nao pode mexer no run; esta em %q", st)
	}

	// --- A DECISÃO, PELA ROTA ---
	em := aos263Assina(opPriv, run, exhaustionOptionContinue, prompt.StepID, aos263Nonce(t), tnClock()())
	rec = aos263Post(h, run, aos263Body(em, exhaustionOptionContinue, prompt.StepID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("continue assinado por operador pinado devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta 200 nao descodifica: %v", err)
	}
	if resp["status"] != "resumable" || resp["state"] != string(state.WaitingOnHuman) || resp["principal"] != aos263OperatorID {
		t.Fatalf("a resposta tem de dizer que o run continua SUSPENSO e quem decidiu: %+v", resp)
	}
	if resp["next"] != exhaustionResumeRoute {
		t.Fatalf("a resposta tem de nomear a execucao que se segue a decisao: %+v", resp)
	}

	// --- DEPOIS: o selo, com razão e capability PRÓPRIAS ---
	selos := aos263Selos(t, node)
	if len(selos) != 1 {
		t.Fatalf("o continue tinha de selar EXACTAMENTE 1 registo; selou %d", len(selos))
	}
	selo := selos[0]
	if selo.Principal.NHIID != aos263OperatorID || selo.RunID != run || selo.StepID != prompt.StepID {
		t.Fatalf("o selo tem de amarrar quem decidiu, o run e a pergunta: %+v", selo)
	}
	if selo.Reason != reasonExhaustionContinue || selo.Capability != capExhaustionContinue {
		t.Fatalf("«quem deixou correr» tem de ser auditavel por nome PROPRIO, nao pela ausencia de um abort: reason=%q cap=%q", selo.Reason, selo.Capability)
	}
	var params map[string]string
	for _, ob := range selo.Obligations {
		if ob.Type == exhaustionDecisionObl {
			params = ob.Params
		}
	}
	if params["decision"] != exhaustionOptionContinue || params["consumed_tokens"] != "820" || params["limit_tokens"] != "1000" {
		t.Fatalf("o selo tem de trazer a decisao e o montante do aviso (nunca um recalculo): %+v", params)
	}

	// --- DEPOIS: o estado NÃO mudou, a pergunta saiu por DECISÃO, e a retoma passa ---
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("o continue NAO transita o run (a re-hospedagem e um acto a parte); esta em %q", st)
	}
	if _, ainda := aos263Prompt(t, node, run); ainda {
		t.Fatal("uma pergunta RESPONDIDA nao pode continuar na lista do operador")
	}
	expiraveis, err := node.PendingApprovals.ListExpirable(context.Background(), time.Now().Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	for _, e := range expiraveis {
		if e.RunID == run {
			t.Fatal("o varrimento NAO pode anunciar «expirado sem decisao» sobre uma pergunta respondida")
		}
	}
	// A retoma DEIXOU de estar barrada. O que se afirma é exactamente isto — que o travão de
	// AOS-263 abriu —, e não que a re-hospedagem conclua: este run nunca correu um turno de
	// modelo a sério, pelo que não tem capturas para reproduzir e a retoma acaba por falhar
	// mais à frente, no plano de replay. Afirmar 202 aqui provaria o replay, não o travão.
	rec = postJSON(h, "POST", "/runs/"+run+"/resume", map[string]any{"credential": "cred-fresca"})
	if rec.Code == http.StatusConflict {
		t.Fatalf("depois do continue selado a retoma NAO pode continuar barrada: %d (%s)", rec.Code, rec.Body.String())
	}
	if err := svc.Resume(context.Background(), run, "cred-fresca"); errors.Is(err, ErrExhaustionPromptUnanswered) {
		t.Fatalf("a pergunta esta respondida — o travao tinha de abrir; deu %v", err)
	}
}

// TestAOS263_ContinueSemAssinaturaValidaERecusado: a metade arriscada não é a metade fácil. As
// mesmas recusas do abort valem aqui — e a que interessa é a última: uma assinatura de `abort`
// legítima NÃO vale como `continue`, porque a decisão entra no payload canónico assinado.
func TestAOS263_ContinueSemAssinaturaValidaERecusado(t *testing.T) {
	node, _, h, opPriv := aos263Node(t)
	const run = "run-263-continue-authn"
	prompt := aos263Suspende(t, node, run, 2, 850, 1000)
	step := prompt.StepID

	adulterada := aos263Assina(opPriv, run, exhaustionOptionContinue, step, aos263Nonce(t), tnClock()())
	adulterada.Signature = append([]byte(nil), adulterada.Signature...)
	adulterada.Signature[0] ^= 0xFF
	comoAbort := aos263Assina(opPriv, run, exhaustionOptionAbort, step, aos263Nonce(t), tnClock()())

	for _, c := range []struct {
		nome string
		em   control.Emitter
	}{
		{"assinatura adulterada", adulterada},
		{"assinatura de um ABORT submetida como continue", comoAbort},
	} {
		t.Run(c.nome, func(t *testing.T) {
			rec := aos263Post(h, run, aos263Body(c.em, exhaustionOptionContinue, step), nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s devia dar 403, veio %d (%s)", c.nome, rec.Code, rec.Body.String())
			}
		})
	}

	// NADA aconteceu: a pergunta continua por responder e o WORM vazio.
	if _, ainda := aos263Prompt(t, node, run); !ainda {
		t.Fatal("nenhuma tentativa recusada pode retirar a pergunta da lista")
	}
	if n := len(aos263Selos(t, node)); n != 0 {
		t.Fatalf("uma decisao recusada NAO e uma decisao: selou %d", n)
	}
	// NÃO-VACUIDADE: a mesma cerimónia, com a assinatura certa, executa.
	em := aos263Assina(opPriv, run, exhaustionOptionContinue, step, aos263Nonce(t), tnClock()())
	if rec := aos263Post(h, run, aos263Body(em, exhaustionOptionContinue, step), nil); rec.Code != http.StatusOK {
		t.Fatalf("apos as recusas, o continue LEGITIMO devia executar (200), veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// (2) O travão da retoma, e a escotilha que o impede de ser uma prisão
// ---------------------------------------------------------------------------

// TestAOS263_RetomaBarradaAteDecisaoOuTTL sela as duas saídas do travão:
//
//   - com a pergunta POR RESPONDER ⇒ [ErrExhaustionPromptUnanswered];
//   - passado o TTL, o MESMO varrimento de pendentes de sempre expira a pergunta e a retoma
//     volta a ser aceite, sem decisão nenhuma — um nó que perdesse os operadores não deixaria
//     o run irrecuperável para sempre.
//
// E a não-vacuidade em cima: uma APROVAÇÃO pendente NÃO barra a retoma (é o caminho normal de
// AOS-021, onde retomar é precisamente o que se faz depois de aprovar).
func TestAOS263_RetomaBarradaAteDecisaoOuTTL(t *testing.T) {
	svc, pend := newSweeperHarness(t, 15*time.Minute)
	ctx := context.Background()
	const run = "run-263-barrado"

	if err := pend.Put(ctx, integration.PendingRecord{
		RunID: run, StepID: "s1-tool-1", Turn: 1, ToolID: "web_post", Capability: "cap:http.post",
		Preview: []byte{0x09}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("Put(aprovacao): %v", err)
	}
	if err := svc.exhaustionPromptPorResponder(ctx, run); err != nil {
		t.Fatalf("uma aprovacao pendente NAO pode barrar a retoma (e o caminho de AOS-021): %v", err)
	}

	passo := exhaustionStepID(4, time.Now(), "a1b2c3d4e5f60718")
	if err := pend.Put(ctx, integration.PendingRecord{
		Kind: integration.PendingKindExhaustion, RunID: run, StepID: passo, Turn: 4,
		Threshold: 0.8, ConsumedTokens: 800, LimitTokens: 1000,
		CreatedAt: time.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("Put(exaustao): %v", err)
	}
	err := svc.exhaustionPromptPorResponder(ctx, run)
	if !errors.Is(err, ErrExhaustionPromptUnanswered) {
		t.Fatalf("com a pergunta por responder a retoma tem de recusar; deu %v", err)
	}
	if !strings.Contains(err.Error(), passo) {
		t.Fatalf("o erro tem de nomear o passo a que se responde: %v", err)
	}

	// A ESCOTILHA: passado o TTL, o varrimento EXISTENTE expira a pergunta e o travão abre.
	svc.SweepApprovalsNow(ctx)
	if err := svc.exhaustionPromptPorResponder(ctx, run); err != nil {
		t.Fatalf("expirada a pergunta, a retoma volta a ser aceite (sem isto o travao seria uma prisao): %v", err)
	}
}

// TestAOS263_LeituraDePendentesFalhadaBarraARetoma: fail-closed do travão. Degradar para «não
// há pergunta» quando o substrato não responde transformaria uma falha transitória na via de
// contorno exacta que o travão fecha.
func TestAOS263_LeituraDePendentesFalhadaBarraARetoma(t *testing.T) {
	falha := errors.New("substrato indisponivel")
	pend, err := integration.NewPendingApprovals(&storeSemLeitura{falha: falha})
	if err != nil {
		t.Fatalf("NewPendingApprovals: %v", err)
	}
	svc := &NodeService{node: &Node{PendingApprovals: pend}, logw: io.Discard}
	if err := svc.exhaustionPromptPorResponder(context.Background(), "run-263-cego"); !errors.Is(err, falha) {
		t.Fatalf("uma leitura falhada NAO pode ler-se como «nao ha pergunta»; deu %v", err)
	}
}

// ---------------------------------------------------------------------------
// (3) Duas perguntas do mesmo run são duas perguntas
// ---------------------------------------------------------------------------

// TestAOS263_SegundaPerguntaDoMesmoRunNaoColideComAPrimeira é o cenário do run preso, corrido
// ponta a ponta: o contador de turnos REINICIA em cada re-hospedagem (o loop conta de 1) mas o
// ledger de burn-down é cumulativo, pelo que um run retomado volta a cruzar o limiar no turno
// 1 — o MESMO número de turno da pergunta anterior.
//
// Com uma chave só-turno, o `Put` da segunda pergunta caía na deduplicação do Event Store, o
// facto de retirada da primeira continuava a valer, e o run ficava suspenso com uma pergunta
// invisível: ausente de `GET /runs/{id}`, 404 na rota de decisão, já expirada para o
// varrimento. Nem abortável nem progredível. A âncora de ocorrência fecha-o.
func TestAOS263_SegundaPerguntaDoMesmoRunNaoColideComAPrimeira(t *testing.T) {
	node, svc, h, opPriv := aos263Node(t)
	ctx := context.Background()
	const run = "run-263-duas-perguntas"

	primeira := aos263Suspende(t, node, run, 1, 810, 1000)
	// A primeira pergunta SAI da lista sem decisão — o TTL, o caminho documentado.
	if err := node.PendingApprovals.ExpireKind(ctx, integration.PendingKindExhaustion, run, primeira.StepID); err != nil {
		t.Fatalf("ExpireKind: %v", err)
	}
	if _, ainda := aos263Prompt(t, node, run); ainda {
		t.Fatal("a primeira pergunta tinha de sair da lista pelo TTL")
	}

	// Nova incarnação: o run é retomado e volta a cruzar o limiar NO TURNO 1.
	if err := node.stateGates.Open(ctx, run, state.Uint64Token(2)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := node.stateGates.resolveGate(run).ResumeFromHuman(ctx, "retoma apos o TTL"); err != nil {
		t.Fatalf("ResumeFromHuman: %v", err)
	}
	segunda := aos263Suspende(t, node, run, 1, 990, 1000)

	if segunda.StepID == primeira.StepID {
		t.Fatalf("duas perguntas distintas NAO podem partilhar a chave (%q) — a segunda seria deduplicada contra a primeira", segunda.StepID)
	}
	// VISÍVEL: pela rota, não por leitura in-process.
	rec := postJSON(h, "GET", "/runs/"+run, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), segunda.StepID) {
		t.Fatalf("a segunda pergunta TEM de aparecer em GET /runs/{id}: %d %s", rec.Code, rec.Body.String())
	}
	// DECIDÍVEL: e o run acaba parado por decisão, não preso.
	svc.mu.Lock()
	svc.suspended[run] = &runState{runID: run, suspended: true, done: make(chan struct{})}
	svc.mu.Unlock()
	em := aos263Assina(opPriv, run, exhaustionOptionAbort, segunda.StepID, aos263Nonce(t), tnClock()())
	if rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, segunda.StepID), nil); rec.Code != http.StatusOK {
		t.Fatalf("a segunda pergunta tem de ser DECIDIVEL, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if st := aos263Estado(t, node, run); st != state.Killed {
		t.Fatalf("o run tinha de acabar parado por decisao; esta em %q", st)
	}
}

// TestAOS263_ColisaoDeChaveEFalhaFechada é a defesa em profundidade. Se alguma vez duas
// perguntas partilharem chave, o registo NÃO é engolido em silêncio: o `Put` recusa, o erro
// sobe por [exhaustionPrompt.raise] e o run aborta como FALHADO (visível) em vez de ficar
// suspenso à espera de uma pergunta que ninguém vê. E o tipo APROVAÇÃO continua idempotente —
// re-escalar a MESMA tool call é normal e não pode partir.
func TestAOS263_ColisaoDeChaveEFalhaFechada(t *testing.T) {
	node, _, _, _ := aos263Node(t)
	ctx := context.Background()
	const run = "run-263-colisao"

	rec := integration.PendingRecord{
		Kind: integration.PendingKindExhaustion, RunID: run, StepID: exhaustionStepID(1, time.Now(), "0f0e0d0c0b0a0908"), Turn: 1,
		Threshold: 0.8, ConsumedTokens: 800, LimitTokens: 1000,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := node.PendingApprovals.Put(ctx, rec); err != nil {
		t.Fatalf("primeiro Put: %v", err)
	}
	if err := node.PendingApprovals.Put(ctx, rec); !errors.Is(err, integration.ErrExhaustionPromptColide) {
		t.Fatalf("um duplicado de EXAUSTAO tem de ser recusado fail-closed; deu %v", err)
	}
	aprov := integration.PendingRecord{
		RunID: run, StepID: "s1-tool-1", Turn: 1, ToolID: "web_post", Capability: "cap:http.post",
		Preview: []byte{0x01}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := node.PendingApprovals.Put(ctx, aprov); err != nil {
		t.Fatalf("Put(aprovacao): %v", err)
	}
	if err := node.PendingApprovals.Put(ctx, aprov); err != nil {
		t.Fatalf("re-escalar a MESMA tool call continua a ser idempotente (nao se pode partir AOS-021): %v", err)
	}
}

// ---------------------------------------------------------------------------
// (4) A cadeia não afirma um abort que não aconteceu
// ---------------------------------------------------------------------------

// TestAOS263_AbortFalhadoAposOSeloEscreveRegistoCompensatorio: o selo é pré-condição do efeito
// e é escrito ANTES dele — a ordem certa. Mas se o efeito falhar (aqui: o run é RETOMADO na
// janela entre a leitura do estado e a transição), a cadeia ficaria a afirmar «este principal
// abortou este run» sobre um run vivo. A desmentida tem de viver DENTRO da cadeia, não só no
// log do nó, que não é superfície de não-repúdio.
func TestAOS263_AbortFalhadoAposOSeloEscreveRegistoCompensatorio(t *testing.T) {
	node, _, _, opPriv := aos263Node(t)
	ctx := context.Background()
	const run = "run-263-compensa"
	prompt := aos263Suspende(t, node, run, 3, 900, 1000)

	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	body := aos263Body(em, exhaustionOptionAbort, prompt.StepID)

	// A CORRIDA, de forma determinista: o handler lê o estado durável em (6) e sela em (7); se
	// o run for RETOMADO no meio, a tabela declarativa de AOS-017 recusa `running → killed` e o
	// abort falha DEPOIS do selo. O gancho vai no Append do WORM, que é exactamente o (7).
	if err := node.stateGates.Open(ctx, run, state.Uint64Token(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	gate := node.stateGates.resolveGate(run)
	mutado := *node
	mutado.WORM = &wormQueRetoma{Store: node.WORM, retoma: func() {
		if err := gate.ResumeFromHuman(ctx, "retoma concorrente"); err != nil {
			t.Errorf("ResumeFromHuman: %v", err)
		}
	}}
	_, hm := newAPI(t, &mutado)

	rec := aos263Post(hm, run, body, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("o abort tinha de falhar por corrida (409), veio %d (%s)", rec.Code, rec.Body.String())
	}
	if st := aos263Estado(t, node, run); st != state.Running {
		t.Fatalf("o run retomado NAO podia ser morto; esta em %q", st)
	}

	// A CADEIA tem as DUAS linhas: a decisão selada e a desmentida que a corrige.
	selos := aos263Selos(t, node)
	if len(selos) != 2 {
		t.Fatalf("a cadeia tem de ter a decisao E o registo compensatorio; tem %d: %+v", len(selos), selos)
	}
	decisao, compensa := selos[0], selos[1]
	if decisao.Reason != reasonExhaustionAbort || decisao.Decision != audit.DecisionAllow {
		t.Fatalf("a primeira linha e a decisao selada: %+v", decisao)
	}
	if compensa.Reason != reasonExhaustionAbortFailed || compensa.Decision != audit.DecisionDeny {
		t.Fatalf("a segunda linha tem de DESMENTIR o efeito: %+v", compensa)
	}
	if compensa.RunID != run || compensa.Principal.NHIID != aos263OperatorID {
		t.Fatalf("a desmentida tem de amarrar o mesmo run e o mesmo principal: %+v", compensa)
	}
	var params map[string]string
	for _, ob := range compensa.Obligations {
		if ob.Type == exhaustionDecisionObl {
			params = ob.Params
		}
	}
	if params["corrects_audit_seq"] == "" {
		t.Fatalf("sem a referencia ao selo corrigido as duas linhas ficam soltas: %+v", params)
	}
	if params["failure"] == "" || strings.Contains(params["failure"], "aos:") {
		t.Fatalf("a classe de falha e vocabulario FECHADO, nunca o erro cru: %+v", params)
	}
}

// wormQueRetoma corre um efeito colateral no PRIMEIRO Append — a forma determinista de meter
// uma corrida real entre a leitura do estado e a transição.
type wormQueRetoma struct {
	audit.Store
	retoma func()
	feito  bool
}

func (w *wormQueRetoma) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	out, err := w.Store.Append(ctx, rec)
	if !w.feito && w.retoma != nil {
		w.feito = true
		w.retoma()
	}
	return out, err
}

// aos263TornaRetomavel dá ao run o que a suspensão real lhe dá: registo de retoma persistido e
// entrada no balde de suspensos. Sem isto a retoma falharia por OUTRA razão (sem registo,
// 409) e os testes do travão não distinguiriam as duas recusas.
func aos263TornaRetomavel(t *testing.T, node *Node, svc *NodeService, runID string) {
	t.Helper()
	goal := agentruntime.Goal{
		RunID:     runID,
		Objective: "trabalho do run suspenso",
		Principal: referencemonitor.Principal{NHIID: "nhi:agente-263"},
	}
	if err := node.ResumeRecords.Put(context.Background(), resumeRecordFromGoal(goal)); err != nil {
		t.Fatalf("ResumeRecords.Put: %v", err)
	}
	svc.mu.Lock()
	svc.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
	svc.mu.Unlock()
}
