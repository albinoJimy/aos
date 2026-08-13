package main

// AOS-263 (PARTE 3) — provas FALSIFICÁVEIS da ROTA DE DECISÃO e do `abort`.
//
// TUDO o que aqui se afirma passa por `POST /runs/{id}/exhaustion` — nunca por
// [apiHandler.sealExhaustionDecision] nem por [runStateGates.killFromWaitingOnHuman] em
// directo. É deliberado: o critério do ticket é que a autoridade e o anti-replay valham PELA
// ROTA, e uma prova in-process não distingue «o nó verifica» de «o nó verificaria se alguém
// chamasse a função certa».
//
// Os quatro critérios, e o que cada bloco falsifica se a entrega estiver errada:
//
//  1. a decisão SEM assinatura válida é RECUSADA — e a recusa não é vacuosa: a MESMA cerimónia
//     com a assinatura íntegra executa;
//  2. o REPLAY do nonce é RECUSADO — e prova-se que é o NONCE que recusa, não o estado do run
//     (um nonce fresco sobre o mesmo pedido dá outro código);
//  3. o WORM regista o PRINCIPAL verificado, o run, o montante consumido e a razão;
//  4. o `abort` pára o run de forma DURÁVEL e no vocabulário de AOS-252 — `killed` num run
//     suspenso, e NUNCA um kill a meio de um turno (aí a rota recusa e nomeia a pausa
//     graciosa).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	audit "github.com/aos-ref/platform/audit"
)

const aos263OperatorID = "human:operator-exhaustion"

// aos263Node compõe um nó REAL com (a) um operador ed25519 pinado no canal de controlo — a
// autoridade que a decisão exige — e (b) o four-eyes composto, que é o que faz o nó ter registo
// durável de pendentes e de retoma (as peças que armam o prompt de AOS-263). Devolve também a
// chave PRIVADA do operador, que vive SÓ do lado do emissor: a API é non-signing.
func aos263Node(t *testing.T, opts ...APIOption) (*Node, *NodeService, http.Handler, ed25519.PrivateKey) {
	t.Helper()
	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(operador): %v", err)
	}
	apPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(aprovador): %v", err)
	}

	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.SteerClock = tnClock() // frescura determinística, como no resto do canal de controlo
	cfg.Operators = map[string]ed25519.PublicKey{aos263OperatorID: opPub}
	cfg.Approvers = []ApproverConfig{
		{Principal: "human:alice", PubKey: apPub, Authority: []string{"approve:danger"}},
	}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.PendingApprovals == nil || node.SteerAuth == nil || node.WORM == nil || node.stateGates == nil {
		t.Fatal("o no de teste tem de ter as quatro pecas que a rota exige compostas")
	}
	svc, h := newAPI(t, node, opts...)
	return node, svc, h, opPriv
}

// aos263Suspende leva o run ao estado REAL em que a decisão o encontra: suspenso em
// `waiting_on_human` com um prompt de exaustão selado. Usa o caminho da PARTE 2
// ([exhaustionPrompt.raise]) e não transições à mão — o alvo da decisão tem de ser o que o nó
// produz, não uma imitação dele.
//
// Fecha o gate no fim, como [NodeService.hostRun] faz ao sair: é assim que o abort encontra o
// run no mundo real (sem gate aberto, com a verdade só no log).
func aos263Suspende(t *testing.T, node *Node, runID string, turn int, consumido, tecto int64) integration.PendingRecord {
	t.Helper()
	ctx := context.Background()
	if err := node.stateGates.Open(ctx, runID, state.Uint64Token(1)); err != nil {
		t.Fatalf("gates.Open: %v", err)
	}
	gate := node.stateGates.resolveGate(runID)
	if gate == nil {
		t.Fatal("gate do run devia existir depois do Open")
	}
	if err := gate.claimRunning(ctx); err != nil {
		t.Fatalf("claimRunning: %v", err)
	}
	prompt, err := newExhaustionPrompt(node.stateGates, node.PendingApprovals, node.ResumeRecords, node.SteerAuth, node.WORM, nil)
	if err != nil || prompt == nil {
		t.Fatalf("newExhaustionPrompt: prompt=%v err=%v", prompt != nil, err)
	}
	if err := prompt.raise(ctx, runID, progressEvalDeAviso(turn, 0.80, consumido, tecto), "limiar de burn-down cruzado (razao de teste)"); err == nil {
		t.Fatal("a travessia do limiar tinha de suspender o run")
	}
	node.stateGates.Close(runID) // como na saída do hostRun: a verdade fica no log

	pendente, ok := aos263Prompt(t, node, runID)
	if !ok {
		t.Fatal("a suspensao tinha de deixar um prompt de exaustao por responder")
	}
	if st := aos263Estado(t, node, runID); st != state.WaitingOnHuman {
		t.Fatalf("o run tinha de ficar em waiting_on_human; esta em %q", st)
	}
	return pendente
}

// aos263Prompt devolve o prompt de exaustão POR RESPONDER do run (o que o operador vê).
func aos263Prompt(t *testing.T, node *Node, runID string) (integration.PendingRecord, bool) {
	t.Helper()
	recs, err := node.PendingApprovals.ListForRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	for _, r := range recs {
		if r.Kind.Resolved() == integration.PendingKindExhaustion {
			return r, true
		}
	}
	return integration.PendingRecord{}, false
}

// aos263Estado lê o estado DURÁVEL do run (do log, não de memória).
func aos263Estado(t *testing.T, node *Node, runID string) state.State {
	t.Helper()
	st, err := node.stateGates.currentState(context.Background(), runID)
	if err != nil {
		t.Fatalf("currentState: %v", err)
	}
	return st
}

// aos263Selos devolve os registos selados na cadeia WORM das decisões de exaustão.
func aos263Selos(t *testing.T, node *Node) []audit.AuditRecord {
	t.Helper()
	recs, err := node.WORM.Read(context.Background(), exhaustionDecisionPartition, 1, 1<<62)
	if err != nil {
		t.Fatalf("WORM.Read(%s): %v", exhaustionDecisionPartition, err)
	}
	return recs
}

// aos263Nonce gera um nonce fresco de 32 bytes (bem acima do mínimo de 16 do autenticador).
func aos263Nonce(t *testing.T) []byte {
	t.Helper()
	n := make([]byte, 32)
	if _, err := rand.Read(n); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	return n
}

// aos263Assina produz a decisão ASSINADA tal como o operador a produziria na sua máquina: a
// assinatura cobre (run ‖ kind próprio ‖ decisão+passo ‖ nonce ‖ issued_at). O nó nunca detém
// esta chave privada.
func aos263Assina(priv ed25519.PrivateKey, runID, decision, stepID string, nonce []byte, at time.Time) control.Emitter {
	return integration.SignSignal(priv, aos263OperatorID, runID, exhaustionDecisionSignalKind,
		exhaustionDecisionPayload(decision, stepID), nonce, at)
}

// aos263Body monta o corpo de wire da decisão.
func aos263Body(em control.Emitter, decision, stepID string) map[string]any {
	return map[string]any{
		"decision": decision,
		"step_id":  stepID,
		"emitter": map[string]any{
			"id":        em.ID,
			"signature": base64.StdEncoding.EncodeToString(em.Signature),
			"nonce":     base64.StdEncoding.EncodeToString(em.Nonce),
			"issued_at": em.IssuedAt.Format(time.RFC3339Nano),
		},
	}
}

// aos263Post faz o POST da decisão com o estado TLS dado (nil ⇒ sem certificado de cliente).
func aos263Post(h http.Handler, runID string, body any, cs *tls.ConnectionState) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/runs/"+runID+"/exhaustion", bytes.NewReader(b))
	req.TLS = cs
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// CA — a decisão executa, sela com PRINCIPAL, e pára o run DURAVELMENTE
// ---------------------------------------------------------------------------

// TestAOS263_AbortPelaRotaSelaComPrincipalEParaORun é a prova central da parte 3, com o
// CONTRASTE antes/depois em cada afirmação:
//
//	ANTES  — o run está suspenso, a pergunta está por responder, o WORM não tem selo nenhum e
//	         POST /resume reconhece o run como suspenso (409 por falta de registo de retoma,
//	         não 404) — é isto que torna o «depois» não-vacuoso;
//	DEPOIS — 200; o log durável diz `killed`; o selo existe com o PRINCIPAL VERIFICADO, o run,
//	         o montante consumido e a razão; a pergunta saiu da lista por DECISÃO; e o run
//	         deixou de ser retomável (404).
func TestAOS263_AbortPelaRotaSelaComPrincipalEParaORun(t *testing.T) {
	node, _, h, opPriv := aos263Node(t)
	const run = "run-263-abort"
	prompt := aos263Suspende(t, node, run, 4, 800, 1000)

	// --- ANTES ---
	if n := len(aos263Selos(t, node)); n != 0 {
		t.Fatalf("a cadeia de decisoes tinha de estar vazia antes da decisao; tinha %d", n)
	}
	if rec := postJSON(h, "GET", "/runs/"+run, nil); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "pending_exhaustion") {
		t.Fatalf("antes da decisao o GET tinha de mostrar a pergunta: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(h, "POST", "/runs/"+run+"/resume", map[string]any{"credential": "cred-fresca"}); rec.Code == http.StatusNotFound {
		t.Fatal("antes da decisao o run TEM de ser reconhecido como suspenso (senao o 404 do fim nao prova nada)")
	}

	// --- A DECISÃO, PELA ROTA ---
	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, prompt.StepID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("abort assinado por operador pinado devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta 200 nao descodifica: %v", err)
	}
	if resp["status"] != "aborted" || resp["state"] != string(state.Killed) || resp["principal"] != aos263OperatorID {
		t.Fatalf("a resposta tem de dizer o desfecho DURAVEL e quem decidiu: %+v", resp)
	}

	// --- DEPOIS: o estado durável ---
	if st := aos263Estado(t, node, run); st != state.Killed {
		t.Fatalf("o abort tem de materializar waiting_on_human->killed; o log diz %q", st)
	}

	// --- DEPOIS: o SELO WORM (o critério explícito do ticket) ---
	selos := aos263Selos(t, node)
	if len(selos) != 1 {
		t.Fatalf("a decisao tinha de selar EXACTAMENTE 1 registo proprio; selou %d", len(selos))
	}
	selo := selos[0]
	if selo.Principal.NHIID != aos263OperatorID {
		t.Fatalf("principal selado = %q, quero %q — sem principal a decisao nao e atribuivel", selo.Principal.NHIID, aos263OperatorID)
	}
	if selo.RunID != run || selo.StepID != prompt.StepID {
		t.Fatalf("o selo tem de amarrar o run E a pergunta: run=%q step=%q", selo.RunID, selo.StepID)
	}
	if selo.Reason != reasonExhaustionAbort || selo.Capability != capExhaustionAbort {
		t.Fatalf("o selo tem de trazer a RAZAO e a capability: reason=%q cap=%q", selo.Reason, selo.Capability)
	}
	var params map[string]string
	for _, ob := range selo.Obligations {
		if ob.Type == exhaustionDecisionObl {
			params = ob.Params
		}
	}
	if params == nil {
		t.Fatalf("o selo tem de transportar a amarra do burn-down: %+v", selo.Obligations)
	}
	if params["consumed_tokens"] != "800" || params["limit_tokens"] != "1000" || params["decision"] != exhaustionOptionAbort {
		t.Fatalf("o MONTANTE CONSUMIDO selado tem de ser o do aviso (800/1000), nao um recalculo: %+v", params)
	}

	// --- DEPOIS: a pergunta saiu por DECISÃO, e o run deixou de ser retomável ---
	if _, ainda := aos263Prompt(t, node, run); ainda {
		t.Fatal("uma pergunta RESPONDIDA nao pode continuar na lista do operador")
	}
	expiraveis, err := node.PendingApprovals.ListExpirable(context.Background(), time.Now().Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	for _, e := range expiraveis {
		if e.RunID == run {
			t.Fatal("o varrimento NAO pode voltar a apanhar uma pergunta decidida — expiraria o que ja foi respondido")
		}
	}
	if rec := postJSON(h, "GET", "/runs/"+run, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), string(state.Killed)) {
		t.Fatalf("depois do abort o GET tem de reflectir o desfecho duravel killed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(h, "POST", "/runs/"+run+"/resume", map[string]any{"credential": "cred-fresca"}); rec.Code != http.StatusNotFound {
		t.Fatalf("um run abortado NAO e retomavel (killed e terminal absorvente); /resume deu %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAOS263_AbortRetiraORunDoBaldeDeSuspensos cobre a metade EM MEMÓRIA da paragem, que o
// teste anterior não alcança: numa réplica que hospedou o run, ele está no balde de suspensos —
// e é esse balde (não o log) que [NodeService.Suspended] e [NodeService.Resume] consultam
// PRIMEIRO. Deixá-lo lá depois do abort faria o GET continuar a anunciar `waiting_on_human` e o
// POST /resume RE-HOSPEDAR um run já terminado: o cache a contradizer o log, com efeito real.
func TestAOS263_AbortRetiraORunDoBaldeDeSuspensos(t *testing.T) {
	node, svc, h, opPriv := aos263Node(t)
	const run = "run-263-balde"
	prompt := aos263Suspende(t, node, run, 6, 880, 1000)

	// Reproduz o que [NodeService.finish] faz a um run suspenso: arquiva-o no balde.
	svc.mu.Lock()
	svc.suspended[run] = &runState{runID: run, suspended: true, done: make(chan struct{})}
	svc.mu.Unlock()
	if _, susp := svc.Suspended(context.Background(), run); !susp {
		t.Fatal("antes do abort o servico tem de reconhecer o run como suspenso")
	}

	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	if rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, prompt.StepID), nil); rec.Code != http.StatusOK {
		t.Fatalf("abort devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}

	if _, susp := svc.Suspended(context.Background(), run); susp {
		t.Fatal("depois do abort o run NAO pode continuar a anunciar-se suspenso — o cache estaria a contradizer o log")
	}
	if err := svc.Resume(context.Background(), run, "cred-fresca"); !errors.Is(err, ErrRunNotSuspended) {
		t.Fatalf("um run abortado NAO se retoma; Resume deu %v", err)
	}
}

// ---------------------------------------------------------------------------
// CA — sem assinatura VÁLIDA não há decisão
// ---------------------------------------------------------------------------

// TestAOS263_DecisaoSemAssinaturaValidaERecusada percorre as formas de chegar à rota sem a
// autoridade que ela exige. Cada uma é 403, e NENHUMA toca no run nem no WORM.
//
// O caso que mais importa é o último par: a assinatura é PERFEITA mas cobre OUTRA coisa (outro
// passo, outra decisão). Sem a amarra do payload canónico, uma decisão assinada para a pergunta
// de um turno valeria para qualquer outra do mesmo run — e um `abort` assinado para o run A não
// poderia sequer ser distinguido de um assinado para um passo que o operador nunca viu.
func TestAOS263_DecisaoSemAssinaturaValidaERecusada(t *testing.T) {
	node, _, h, opPriv := aos263Node(t)
	const run = "run-263-authn"
	prompt := aos263Suspende(t, node, run, 4, 900, 1000)
	step := prompt.StepID

	forjada := aos263Assina(opPriv, run, exhaustionOptionAbort, step, aos263Nonce(t), tnClock()())
	forjada.Signature = append([]byte(nil), forjada.Signature...)
	forjada.Signature[0] ^= 0xFF

	_, estranhaPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	estranho := integration.SignSignal(estranhaPriv, "human:estranho", run, exhaustionDecisionSignalKind,
		exhaustionDecisionPayload(exhaustionOptionAbort, step), aos263Nonce(t), tnClock()())

	// Assinatura de OUTRO passo e assinatura de OUTRA decisão: ambas íntegras, ambas para outra
	// coisa. Submetidas como um abort deste passo, têm de cair.
	outroPasso := aos263Assina(opPriv, run, exhaustionOptionAbort, exhaustionStepID(99, time.Now(), "9999999999999999"), aos263Nonce(t), tnClock()())
	outraDecisao := aos263Assina(opPriv, run, exhaustionOptionContinue, step, aos263Nonce(t), tnClock()())

	// E a CONFUSÃO DE SINAL: um `pause` legítimo, perfeitamente assinado, submetido como abort.
	nonceDoPause := aos263Nonce(t)
	pause := integration.SignSignal(opPriv, aos263OperatorID, run, control.SignalPause, nil, nonceDoPause, tnClock()())

	casos := []struct {
		nome   string
		em     control.Emitter
		porque string
	}{
		{"assinatura adulterada", forjada, "um bit trocado deixa de validar contra a pubkey pinada"},
		{"emissor nao pinado", estranho, "quem autentica e a pubkey em AOS_OPERATORS, nao o nome que o corpo declara"},
		{"anonimo", control.Emitter{IssuedAt: tnClock()()}, "um POST de controlo anonimo NUNCA e aceite"},
		{"stale", aos263Assina(opPriv, run, exhaustionOptionAbort, step, aos263Nonce(t), tnClock()().Add(-10*time.Minute)), "fora da janela de frescura"},
		{"assinatura de OUTRO passo", outroPasso, "a assinatura amarra a PERGUNTA concreta"},
		{"assinatura de OUTRA decisao", outraDecisao, "a assinatura amarra a DECISAO tomada"},
		{"pause submetido como abort", pause, "um pause capturado nao pode virar um abort — o kind entra no tuplo assinado"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			rec := aos263Post(h, run, aos263Body(c.em, exhaustionOptionAbort, step), nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s devia dar 403 (%s), veio %d (%s)", c.nome, c.porque, rec.Code, rec.Body.String())
			}
		})
	}

	// NADA aconteceu: o run continua suspenso, a pergunta por responder, o WORM vazio.
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("nenhuma tentativa recusada pode mexer no run; esta em %q", st)
	}
	if _, ainda := aos263Prompt(t, node, run); !ainda {
		t.Fatal("nenhuma tentativa recusada pode retirar a pergunta da lista")
	}
	if n := len(aos263Selos(t, node)); n != 0 {
		t.Fatalf("uma decisao recusada NAO e uma decisao: nada podia ser selado; selou %d", n)
	}

	// NÃO-VACUIDADE (1): o pause era MESMO válido — e continua GASTÁVEL. A tentativa recusada
	// não lhe queimou o nonce, porque a assinatura é verificada ANTES do consumo.
	pauseBody := map[string]any{"emitter": map[string]any{
		"id":        pause.ID,
		"signature": base64.StdEncoding.EncodeToString(pause.Signature),
		"nonce":     base64.StdEncoding.EncodeToString(pause.Nonce),
		"issued_at": pause.IssuedAt.Format(time.RFC3339Nano),
	}}
	if rec := postJSON(h, "POST", "/runs/"+run+"/pause", pauseBody); rec.Code != http.StatusAccepted {
		t.Fatalf("o pause era legitimo e nao foi gasto pela tentativa recusada; /pause deu %d (%s)", rec.Code, rec.Body.String())
	}

	// NÃO-VACUIDADE (2): a MESMA cerimónia, agora com a assinatura certa, EXECUTA.
	em := aos263Assina(opPriv, run, exhaustionOptionAbort, step, aos263Nonce(t), tnClock()())
	if rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, step), nil); rec.Code != http.StatusOK {
		t.Fatalf("apos as recusas, a decisao LEGITIMA devia executar (200), veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// CA — o REPLAY do nonce é recusado PELA ROTA
// ---------------------------------------------------------------------------

// TestAOS263_ReplayDoNonceERecusadoPelaRota prova o anti-replay DURÁVEL sem o confundir com o
// estado do run — que é o erro fácil neste teste: depois de um abort o run está morto, pelo que
// uma re-submissão seria recusada de qualquer maneira e o 403 não provaria nada.
//
// Por isso a prova principal usa um pedido que NÃO muda nada: uma decisão sobre um passo
// inexistente. Autentica (o nonce é CONSUMIDO) e devolve 404. Re-submetida, devolve 403 — e a
// diferença entre os dois códigos é exactamente o anti-replay a morder. A seguir, a mesma
// distinção sobre a decisão REAL: replay ⇒ 403, nonce fresco ⇒ 404.
func TestAOS263_ReplayDoNonceERecusadoPelaRota(t *testing.T) {
	node, _, h, opPriv := aos263Node(t)
	const run = "run-263-replay"
	prompt := aos263Suspende(t, node, run, 3, 850, 1000)

	// (1) Pedido autenticado que não altera nada: passo inexistente.
	fantasma := exhaustionStepID(777, time.Now(), "7777777777777777")
	emF := aos263Assina(opPriv, run, exhaustionOptionAbort, fantasma, aos263Nonce(t), tnClock()())
	bodyF := aos263Body(emF, exhaustionOptionAbort, fantasma)
	if rec := aos263Post(h, run, bodyF, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("decisao autenticada sobre um passo inexistente devia dar 404, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := aos263Post(h, run, bodyF, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("o MESMO nonce re-submetido devia dar 403 (replay), veio %d — o 404 anterior mostra que a autenticacao passou a primeira vez", rec.Code)
	}
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("nenhum dos dois pedidos podia mexer no run; esta em %q", st)
	}

	// (2) A decisão REAL: executa uma vez.
	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	body := aos263Body(em, exhaustionOptionAbort, prompt.StepID)
	if rec := aos263Post(h, run, body, nil); rec.Code != http.StatusOK {
		t.Fatalf("primeira decisao devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// Replay do MESMO corpo ⇒ 403, ANTES de qualquer outra verificação.
	if rec := aos263Post(h, run, body, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("replay da MESMA decisao devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// E com nonce FRESCO o código é OUTRO (404: já não há pergunta). É isto que prova que o 403
	// acima veio do nonce consumido e não do estado do run.
	fresca := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	if rec := aos263Post(h, run, aos263Body(fresca, exhaustionOptionAbort, prompt.StepID), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("com nonce FRESCO a recusa e outra (404, sem pergunta), veio %d (%s) — se fosse 403 o teste do replay seria vacuoso", rec.Code, rec.Body.String())
	}
	// UM selo, não dois: o replay não voltou a selar decisão nenhuma.
	if n := len(aos263Selos(t, node)); n != 1 {
		t.Fatalf("a cadeia devia ter exactamente 1 decisao selada; tem %d", n)
	}
}

// ---------------------------------------------------------------------------
// CA — o `abort` NÃO é um kill novo
// ---------------------------------------------------------------------------

// TestAOS263_AbortNaoMataRunVivoENomeiaAPausaGraciosa sela a decisão de desenho do ticket: o
// abort adapta-se às paragens que já existem em vez de inventar uma. Num run que VOLTOU A
// CORRER com a pergunta em aberto (possível: quem retoma pode fazê-lo sem responder), a rota
// RECUSA — e nomeia a PAUSA GRACIOSA, que pára o run na fronteira de fim-de-turno e o deixa
// RETOMÁVEL, em vez de o matar a meio de um turno.
func TestAOS263_AbortNaoMataRunVivoENomeiaAPausaGraciosa(t *testing.T) {
	node, _, h, opPriv := aos263Node(t)
	const run = "run-263-vivo"
	prompt := aos263Suspende(t, node, run, 2, 800, 1000)

	// O operador RETOMA sem responder: o run volta a `running` com a pergunta em aberto.
	if err := node.stateGates.Open(context.Background(), run, state.Uint64Token(1)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := node.stateGates.resolveGate(run).ResumeFromHuman(context.Background(), "retoma sem responder"); err != nil {
		t.Fatalf("ResumeFromHuman: %v", err)
	}
	if st := aos263Estado(t, node, run); st != state.Running {
		t.Fatalf("o run devia estar a correr; esta em %q", st)
	}

	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, prompt.StepID), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("abort sobre um run VIVO devia dar 409 (nao se mata a meio de um turno), veio %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), exhaustionGracefulPauseRoute) {
		t.Fatalf("a recusa tem de NOMEAR a pausa graciosa — senao o operador fica sem via: %s", rec.Body.String())
	}
	if st := aos263Estado(t, node, run); st != state.Running {
		t.Fatalf("o run vivo NAO podia ser tocado; esta em %q", st)
	}
	if n := len(aos263Selos(t, node)); n != 0 {
		t.Fatalf("uma decisao recusada nao sela nada; selou %d", n)
	}
}

// ---------------------------------------------------------------------------
// CA — MESMA admissão do /approve e do /pause
// ---------------------------------------------------------------------------

// TestAOS263_DecisaoHerdaMTLSDeControlo prova que a rota entrou no plano de CONTROLO e não ao
// lado dele: com o mTLS de controlo composto, um pedido SEM certificado de cliente verificado é
// 403 ANTES de qualquer autenticação (o nonce nem é consumido — o MESMO corpo executa depois,
// com certificado). Se o handler tivesse esquecido [apiHandler.admitControlMTLS] — a maneira
// mais fácil de "inventar um esquema novo" sem dar por isso — a primeira metade daria 200.
func TestAOS263_DecisaoHerdaMTLSDeControlo(t *testing.T) {
	node, _, h, opPriv := aos263Node(t, WithControlMTLS("/mnt/control-ca.pem"))
	const run = "run-263-mtls"
	prompt := aos263Suspende(t, node, run, 5, 950, 1000)

	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	body := aos263Body(em, exhaustionOptionAbort, prompt.StepID)

	if rec := aos263Post(h, run, body, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("sem certificado de cliente verificado devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("recusado no transporte, o pedido nao podia ter chegado a lado nenhum; run em %q", st)
	}
	if rec := aos263Post(h, run, body, verifiedClientTLS()); rec.Code != http.StatusOK {
		t.Fatalf("com certificado verificado + assinatura valida devia dar 200, veio %d (%s) — o nonce nao podia ter sido gasto na recusa de transporte", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Fronteira — vocabulário FECHADO, e o que não tem executor não entra
// ---------------------------------------------------------------------------

// TestAOS263_VocabularioFechadoDeDecisoes prova que a rota só aceita o que EXECUTA. As duas
// opções sem executor (`extend`, decisão do dono (iii); `summarize_stop`, sem caminho de resumo
// no loop) são 400 — e não um 200 que não faz nada, que seria a forma silenciosa de mentir. O
// `resume` é 400 TAMBÉM, com a distinção nomeada: não é uma decisão, é a re-hospedagem que se
// segue a um `continue` já decidido (e exige credencial fresca, que esta rota não transporta).
//
// Nenhum destes casos consome nonce: são recusados na FORMA, antes da autenticação. A prova é o
// fim do teste — o mesmo nonce que atravessou todos eles ainda executa a decisão legítima.
func TestAOS263_VocabularioFechadoDeDecisoes(t *testing.T) {
	node, _, h, opPriv := aos263Node(t)
	const run = "run-263-vocab"
	prompt := aos263Suspende(t, node, run, 1, 810, 1000)

	nonce := aos263Nonce(t)
	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, nonce, tnClock()())

	casos := []struct {
		nome     string
		decisao  string
		esperado string
	}{
		{"extend (sem mutador de tecto)", "extend", "decisao desconhecida"},
		{"summarize_stop (sem caminho de resumo)", "summarize_stop", "decisao desconhecida"},
		{"resume (nao e decisao: e a re-hospedagem)", "resume", exhaustionResumeRoute},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			rec := aos263Post(h, run, aos263Body(em, c.decisao, prompt.StepID), nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s devia dar 400, veio %d (%s)", c.nome, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.esperado) {
				t.Fatalf("a recusa devia explicar-se com %q: %s", c.esperado, rec.Body.String())
			}
		})
	}

	// step_id em falta ⇒ 400: sem ele a assinatura não amarraria pergunta nenhuma.
	if rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, ""), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("sem step_id devia dar 400, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// Campo desconhecido ⇒ 400 (o wire é FECHADO, como no resto da API): um `force: true` que
	// o nó ignorasse em silêncio seria a via para alguém julgar que enviou um bypass.
	corpo := aos263Body(em, exhaustionOptionAbort, prompt.StepID)
	corpo["force"] = true
	if rec := aos263Post(h, run, corpo, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("campo desconhecido devia dar 400, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// NÃO-VACUIDADE: nenhuma das recusas de forma gastou o nonce.
	if rec := aos263Post(h, run, aos263Body(em, exhaustionOptionAbort, prompt.StepID), nil); rec.Code != http.StatusOK {
		t.Fatalf("as recusas de FORMA nao podem queimar o nonce do operador; a decisao legitima deu %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAOS263_RotaDesligadaSemMaquinaria prova o fail-closed da COMPOSIÇÃO: sem uma das peças de
// que a decisão depende (canal de controlo autenticado, registo de pendentes, gates de estado,
// WORM) a rota responde 501 — não 200, não 500. Uma decisão que não se consegue autenticar, ou
// que não se consegue SELAR, não é uma decisão.
func TestAOS263_RotaDesligadaSemMaquinaria(t *testing.T) {
	node, _, _, opPriv := aos263Node(t)
	const run = "run-263-desligado"
	prompt := aos263Suspende(t, node, run, 1, 900, 1000)
	em := aos263Assina(opPriv, run, exhaustionOptionAbort, prompt.StepID, aos263Nonce(t), tnClock()())
	body := aos263Body(em, exhaustionOptionAbort, prompt.StepID)

	for _, caso := range []struct {
		nome  string
		tirar func(*Node)
	}{
		{"sem canal de controlo autenticado", func(n *Node) { n.SteerAuth = nil }},
		{"sem registo de pendentes", func(n *Node) { n.PendingApprovals = nil }},
		{"sem WORM (nao ha onde selar a decisao)", func(n *Node) { n.WORM = nil }},
		{"sem gates de estado (nao ha como ler nem mudar o estado duravel)", func(n *Node) { n.stateGates = nil }},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			// Um Node MUTADO à mão: é a única forma de exercitar a guarda, porque o Bootstrap
			// compõe sempre estas peças quando o four-eyes está composto.
			mutado := *node
			caso.tirar(&mutado)
			_, hm := newAPI(t, &mutado)
			if rec := aos263Post(hm, run, body, nil); rec.Code != http.StatusNotImplemented {
				t.Fatalf("%s devia dar 501, veio %d (%s)", caso.nome, rec.Code, rec.Body.String())
			}
		})
	}
	// E o run continua intocado depois de todas as tentativas.
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("nenhuma tentativa contra um no desligado pode mexer no run; esta em %q", st)
	}
}
