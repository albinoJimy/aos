package main

// AOS-258 — A PROVA DE NÓ DO ORÇAMENTO: o permit sobrevive ao tecto, e o deny é atribuível.
//
// # Porque é que este ficheiro existe
//
// AOS-256/AOS-257 ligaram o orçamento e provaram-no na cadeia (`packages/integration`). Faltava
// a prova mais cara de simular e a única que apanha um defeito de COMPOSIÇÃO: a partir de um
// [Bootstrap] REAL, com a variável de ambiente que o operador define, um run cuja tool call
// EXECUTA com o orçamento composto.
//
// Uma suite que só provasse NEGAÇÕES ficaria verde com o wiring partido — e partido da forma
// mais cara que existe: o hook de orçamento sem o nó por-run registado faz
// `budget.Budget.Reserve` devolver ErrUnknownNode, o adaptador converte-o em deny, e o nó nega
// 100% das tool calls de TODOS os runs com uma razão que parece falta de orçamento. Um teste de
// denies passaria; a produção estaria completamente parada. Por isso a prova central aqui é o
// PERMIT, e a negação é a sua prova negativa.
//
// # As três provas, e o que cada uma isola
//
//  1. [TestAOS258_No_DentroDoTecto_PermitEAToolExecuta] — tecto folgado: a call atravessa a
//     cadeia inteira do nó (identity→revalidation→policy→taint→scope→BUDGET→egress) e a tool
//     EXECUTA. Nenhum registo do WORM foi negado pelo orçamento;
//  2. [TestAOS258_No_AlemDoTecto_DenyBudgetSeladoEAtribuido] — o MESMO nó, o MESMO run, a MESMA
//     call: muda só `AOS_BUDGET_MAX_TOKENS`, abaixo da estimativa. A call é NEGADA, a tool NUNCA
//     corre, e o WORM tem o registo com `denied_by=budget` ATRIBUÍDO (run/step/tool/capability/
//     principal) e SELADO (hash-chain verificada, EntryHash recomputado do conteúdo);
//  3. [TestAOS258_No_OMesmoRunPermiteDepoisNega] — a queimadura: DUAS calls idênticas no MESMO
//     run com um tecto que só chega para UMA. A primeira executa, a segunda é negada pelo
//     orçamento. É o que torna (1) não-vacuoso quanto ao orçamento estar LIGADO: o permit e o
//     deny saem do mesmo processo, da mesma cadeia e do mesmo nó de orçamento — a única coisa
//     que mudou entre eles foi o headroom que a primeira call gastou.
//
// Os tectos NÃO são números mágicos: derivam de [integration.TokenOnlyEstimator] aplicado à
// call que o loop vai construir (ver [aos258Estimativa]). Se o estimador mudar, os tectos
// acompanham-no — e um teste calibrado à mão passaria a mentir em silêncio.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
)

// aos258Tool é a tool registada no RM do nó (a entry ASSINADA do catálogo que
// [obsPermitNodeWith] compõe — sem ela a revalidação nega antes do PDP).
const aos258Tool = "counter"

// aos258Payload é o argumento da tool call. É deliberadamente GRANDE, e cresceu em AOS-260
// por uma razão que mudou o significado do tecto: desde que o turno de modelo passou a ser
// ADMITIDO pelo mesmo orçamento por-run, o tecto deixou de ser um número que só as tool calls
// consomem. Estes três testes continuam a ser sobre a decisão da TOOL CALL — para isso, a
// estimativa da tool tem de DOMINAR a pegada do turno de modelo (prompt materializado +
// provisão de output), senão o run pára por falta de orçamento para pensar antes de chegar a
// pedir a tool, e a prova passa a ser sobre outra coisa.
//
// Com ~4 kB de argumentos a estimativa fica na ordem dos milhares de tokens, contra ~120 do
// prompt deste run: a diferença entre "dentro" e "além" do tecto é ampla e os tectos abaixo
// continuam a derivar da FUNÇÃO ([aos258Estimativa]), nunca de constantes calibradas à mão.
var aos258Payload = aos258PayloadGrande()

// aos258PayloadGrande constrói o argumento JSON denso. Estruturado de propósito: é sobre
// payloads assim que a heurística de bytes subestima e a contagem por átomos de AOS-258
// (que é a que está em vigor) se afasta dela.
func aos258PayloadGrande() []byte {
	var b strings.Builder
	b.WriteString(`{"op":"tick","doc_id":"notes","fields":[`)
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, `{"id":%d,"title":"documento-%d","author":"equipa-%d","updated_at":"2026-01-%02d"},`, i, i, i%7, i%28+1)
	}
	b.WriteString(`{"id":"fim"}],"limit":250}`)
	return []byte(b.String())
}

// aos258FolgaDoTurnoDeModelo é o headroom que se acrescenta ao tecto para os TURNOS DE MODELO
// do run — a parte do orçamento que AOS-260 passou a admitir e que estes testes não querem
// medir. Metade da estimativa da tool call: grande o bastante para vários turnos de modelo
// deste run (prompt ~120 tokens + provisão, que é ela própria limitada a 1/8 do tecto), e
// PEQUENA o bastante para não pagar uma segunda tool call — que é a distinção de que
// [TestAOS258_No_OMesmoRunPermiteDepoisNega] vive.
func aos258FolgaDoTurnoDeModelo() int64 { return aos258Estimativa() / 2 }

// aos258Estimativa é o número de tokens que o estimador REAL (AOS-258) atribui à call que o
// loop vai construir para esta invocação. É a fonte dos tectos dos três testes: o contrato
// entre o teste e o estimador é a FUNÇÃO, nunca uma constante copiada.
func aos258Estimativa() int64 {
	return integration.TokenOnlyEstimator(&referencemonitor.Call{
		ToolID:     aos258Tool,
		Capability: durCap,
		Input:      aos258Payload,
	}).Tokens
}

// aos258Model emite `calls` tool calls (uma por turno) e conclui no turno seguinte. Espelha o
// [toolEmittingModel] mas com contagem configurável — a prova (3) precisa de duas.
type aos258Model struct {
	calls int
	turns int
}

func (m *aos258Model) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.turns++
	if m.turns <= m.calls {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:     aos258Tool,
				Capability: durCap, // cap:fs.read — permitida pelo bundle Cedar assinado
				Input:      aos258Payload,
			}},
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	}
	return agentruntime.ModelResponse{Text: "concluido", Final: true, Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

// aos258Run compõe o nó de PERMIT (a mesma cadeia de produção de [obsPermitNodeWith], sem
// observabilidade) com `AOS_BUDGET_MAX_TOKENS` = maxTokens, corre um run com `calls` tool
// calls e devolve quantas vezes a tool EXECUTOU e o WORM do nó.
//
// O tecto entra pela variável de ambiente e não por um campo de teste de propósito: é o
// caminho do operador — [budgetFromEnv] → [integration.SecuredConfig.Budget] → ponto de
// injecção "budget" da cadeia. Um teste que injectasse o *RunBudget* à mão saltaria
// exactamente a costura que este ficheiro existe para provar.
func aos258Run(t *testing.T, runID string, maxTokens int64, calls int) (execs int64, worm audit.Store, permits, denials uint64) {
	t.Helper()

	// A pegada do TURNO DE MODELO (AOS-260) sai do mesmo tecto. Estes testes só são sobre a
	// decisão da tool call se a estimativa da tool DOMINAR essa pegada — caso contrário o run
	// pára por falta de orçamento para pensar e as asserções passam a medir outra coisa.
	if est := aos258Estimativa(); est < 600 {
		t.Fatalf("a estimativa da tool call (%d tokens) e pequena demais para isolar a decisao da TOOL CALL do turno de modelo (prompt ~120 tokens + provisao) — aumente aos258Payload", est)
	}
	t.Setenv("AOS_BUDGET_MAX_TOKENS", strconv.FormatInt(maxTokens, 10))

	model := &aos258Model{calls: calls}
	node, credential := obsPermitNodeWith(t, "", model, nil)
	t.Cleanup(func() { _ = node.Close() })

	var n int64
	if err := node.Runtime.Register(aos258Tool, func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&n, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(%s) no RM do no: %v", aos258Tool, err)
	}

	res, _, err := node.Runtime.Run(context.Background(), agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: credential,
		Model:      agentruntime.ModelConfig{ModelID: "model:aos258"},
		Objective:  "prova de no do orcamento por-run",
		MaxTurns:   calls + 3,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("o run devia ter concluido, veio %+v", res)
	}
	// ANTI-VACUIDADE transversal: sem tool call EMITIDA, tudo o resto seria uma prova sobre o
	// nada — o defeito que AOS-192 apanhou.
	if model.turns <= calls {
		t.Fatalf("o modelo devia ter emitido %d tool calls e concluido; turnos=%d", calls, model.turns)
	}

	p, d, _ := node.Runtime.Monitor().Metrics().Snapshot()
	return atomic.LoadInt64(&n), node.WORM, p, d
}

// registosDoRun devolve a partição de audit do run (a [audit.MediationSink] parte por RunID),
// depois de VERIFICAR a hash-chain — ler registos sem verificar o selo provaria o conteúdo, não
// a sua inviolabilidade.
func registosDoRun(t *testing.T, worm audit.Store, runID string) []audit.AuditRecord {
	t.Helper()
	ctx := context.Background()

	head, err := worm.Head(ctx, runID)
	if err != nil {
		t.Fatalf("Head da particao %q: %v", runID, err)
	}
	if head == 0 {
		t.Fatalf("o WORM do no nao tem registo nenhum para o run %q — nenhuma mediacao foi selada (a prova ficaria vacuosa)", runID)
	}
	if err := audit.Verify(ctx, worm, runID, 1, head); err != nil {
		t.Fatalf("a hash-chain do run %q NAO verifica: %v", runID, err)
	}
	recs, err := worm.Read(ctx, runID, 1, head)
	if err != nil {
		t.Fatalf("Read da particao %q: %v", runID, err)
	}
	return recs
}

// negadoPeloOrcamento devolve o primeiro registo com denied_by="budget" (e se existe).
func negadoPeloOrcamento(recs []audit.AuditRecord) (audit.AuditRecord, bool) {
	for _, r := range recs {
		if r.DeniedBy == "budget" {
			return r, true
		}
	}
	return audit.AuditRecord{}, false
}

// TestAOS258_No_DentroDoTecto_PermitEAToolExecuta é a prova NÃO-VACUOSA que faltava ao ticket:
// com o orçamento COMPOSTO (a env definida ⇒ o BudgetCheck real no lugar do stub) e um tecto
// acima da estimativa, a tool call atravessa a cadeia REAL do nó e a tool EXECUTA.
//
// Falharia com `execs=0` se o nó por-run não fosse registado (ErrUnknownNode ⇒ deny), se a
// estimativa estourasse o tecto por engano, ou se a reserva ficasse presa. É a prova de que o
// caminho feliz SOBREVIVE ao orçamento composto.
func TestAOS258_No_DentroDoTecto_PermitEAToolExecuta(t *testing.T) {
	tecto := aos258Estimativa() * 4 // folgado: cabe a call e sobra
	execs, worm, permits, denials := aos258Run(t, "run-aos258-permit", tecto, 1)

	if execs != 1 {
		t.Fatalf("a tool devia ter EXECUTADO 1x com o orcamento composto e tecto de %d tokens (estimativa=%d); execs=%d.\n"+
			"execs=0 e exactamente como se apresenta um orcamento composto SEM o no por-run registado: 100%% das tool calls negadas, em todos os runs.", tecto, aos258Estimativa(), execs)
	}
	if permits < 1 {
		t.Errorf("permits=%d — a tool call legitima devia ter sido PERMITIDA pela cadeia real com o orcamento composto", permits)
	}
	if denials != 0 {
		t.Errorf("denials=%d — nenhuma barreira devia negar uma call dentro do tecto", denials)
	}
	if rec, ok := negadoPeloOrcamento(registosDoRun(t, worm, "run-aos258-permit")); ok {
		t.Errorf("o WORM tem uma negacao do ORCAMENTO num run dentro do tecto (audit_seq=%d, reason=%q)", rec.AuditSeq, rec.Reason)
	}
}

// TestAOS258_No_AlemDoTecto_DenyBudgetSeladoEAtribuido é a outra metade do critério: além do
// tecto, a call é NEGADA pelo orçamento, a tool NUNCA corre, e a negação fica SELADA e
// ATRIBUÍDA no WORM.
//
// Atribuída importa tanto como selada: um log inviolável que prove QUE uma acção foi recusada
// mas não POR QUEM obriga a bissectar o sistema para responder à pergunta mais básica de uma
// auditoria — e uma negação por orçamento é indistinguível de uma negação por política sem o
// `denied_by`.
func TestAOS258_No_AlemDoTecto_DenyBudgetSeladoEAtribuido(t *testing.T) {
	const runID = "run-aos258-deny"
	estimativa := aos258Estimativa()
	tecto := estimativa - 1 // ABAIXO da estimativa: a reserva não cabe
	if tecto < 1 {
		t.Fatalf("a estimativa (%d) e demasiado pequena para construir um tecto valido — aumente aos258Payload", estimativa)
	}

	execs, worm, permits, denials := aos258Run(t, runID, tecto, 1)

	if execs != 0 {
		t.Fatalf("a tool EXECUTOU %dx com um tecto (%d) abaixo da estimativa (%d) — o orcamento nao esta a decidir nada", execs, tecto, estimativa)
	}
	if denials < 1 {
		t.Fatalf("denials=%d — a call alem do tecto devia ter sido NEGADA", denials)
	}
	if permits != 0 {
		t.Errorf("permits=%d — nenhum permit devia ser mintado para uma call sem headroom", permits)
	}

	recs := registosDoRun(t, worm, runID)
	rec, ok := negadoPeloOrcamento(recs)
	if !ok {
		var vistos []string
		for _, r := range recs {
			vistos = append(vistos, string(r.Decision)+"/"+r.DeniedBy)
		}
		t.Fatalf("nenhum registo do WORM foi atribuido ao ORCAMENTO (denied_by=%q esperado); registos vistos: %s.\n"+
			"Se a negacao veio de outra barreira, o tecto nao foi quem decidiu e a prova e vacuosa.", "budget", strings.Join(vistos, ", "))
	}

	// (a) ATRIBUIÇÃO — quem, o quê, onde. Sem estes campos a negação é anónima.
	if rec.Decision != audit.DecisionDeny {
		t.Errorf("decision=%q, esperado deny", rec.Decision)
	}
	if rec.Reason == "" {
		t.Errorf("a negacao por orcamento nao tem reason — o operador nao consegue distinguir «sem headroom» de «no desconhecido» (que e um defeito de wiring, nao de orcamento)")
	}
	if rec.RunID != runID {
		t.Errorf("run_id=%q, esperado %q", rec.RunID, runID)
	}
	if rec.StepID == "" {
		t.Errorf("step_id vazio — a negacao nao e correlacionavel com o passo concreto da trajectoria")
	}
	if rec.ToolID != aos258Tool {
		t.Errorf("tool_id=%q, esperado %q", rec.ToolID, aos258Tool)
	}
	if rec.Capability != durCap {
		t.Errorf("capability=%q, esperado %q", rec.Capability, durCap)
	}
	if rec.Principal.NHIID != durAgent {
		t.Errorf("principal.nhi_id=%q, esperado %q — a negacao tem de ficar ligada a IDENTIDADE que a sofreu", rec.Principal.NHIID, durAgent)
	}

	// (b) SELO — o registo está na hash-chain e o seu EntryHash deriva do CONTEÚDO. A cadeia
	// inteira já foi verificada em [registosDoRun]; aqui fecha-se o ciclo sobre ESTE registo,
	// que é o que sustenta a afirmação «denied_by=budget selado».
	if len(rec.EntryHash) == 0 || len(rec.PrevHash) == 0 {
		t.Fatalf("o registo da negacao nao esta encadeado (entry_hash=%d bytes, prev_hash=%d bytes)", len(rec.EntryHash), len(rec.PrevHash))
	}
	recomputado := audit.ComputeEntryHash(rec.PrevHash, rec)
	if string(recomputado) != string(rec.EntryHash) {
		t.Errorf("o EntryHash do registo da negacao NAO deriva do seu conteudo — o selo nao ata a atribuicao (denied_by/reason) ao registo")
	}
}

// TestAOS258_No_OMesmoRunPermiteDepoisNega é a queimadura do tecto DENTRO de um run, e a
// âncora de não-vacuidade das duas provas anteriores: duas tool calls IDÊNTICAS, um tecto que
// só chega para uma.
//
// A primeira EXECUTA (permit sob orçamento composto); a segunda é NEGADA pelo orçamento — e a
// única diferença entre elas é o headroom que a primeira CONFIRMOU (o Commit de AOS-257). Um
// orçamento que nunca debitasse deixava as duas passar; um que nunca registasse o nó do run
// negava as duas. Só a composição correcta produz exactamente 1.
func TestAOS258_No_OMesmoRunPermiteDepoisNega(t *testing.T) {
	const runID = "run-aos258-queima"
	// Cabe exactamente UMA reserva de tool call, mais a folga dos turnos de modelo (AOS-260):
	// sem essa folga o run pararia por orçamento ANTES de emitir a segunda call, e a prova —
	// que é sobre o par permit+deny da MESMA call — ficaria sobre outra coisa. A folga é
	// menor do que uma estimativa, pelo que continua a não pagar a segunda call.
	tecto := aos258Estimativa() + aos258FolgaDoTurnoDeModelo()

	execs, worm, permits, denials := aos258Run(t, runID, tecto, 2)

	if execs != 1 {
		t.Fatalf("execs=%d, esperado exactamente 1 com um tecto de %d tokens para DUAS calls de %d tokens cada.\n"+
			"execs=0 ⇒ o no de orcamento do run nao foi registado (tudo negado); execs=2 ⇒ o permit nao CONFIRMOU a reserva e o tecto nunca se gasta.", execs, tecto, aos258Estimativa())
	}
	if permits < 1 || denials < 1 {
		t.Fatalf("esperava o par permit+deny no MESMO run (permits=%d, denials=%d) — e o par que prova que o orcamento esta ligado e a decidir", permits, denials)
	}
	if _, ok := negadoPeloOrcamento(registosDoRun(t, worm, runID)); !ok {
		t.Errorf("a segunda call nao foi atribuida ao ORCAMENTO no WORM — se foi outra barreira a negar, o tecto nao e quem esta a decidir")
	}
}
