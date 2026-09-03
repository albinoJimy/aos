package main

// SAGA DE COMPENSAÇÃO / ROLLBACK NO CAMINHO DE FALHA DURÁVEL (AOS-254, F12).
//
// O PROBLEMA que este ficheiro fecha: o [saga.SagaCoordinator]/[saga.CompensationRegistry]
// (AOS-020) estavam COMPLETOS mas SÓ com chamadores de teste. A aresta failed→compensating da
// tabela declarativa de AOS-017 era um caminho MORTO — e era-o DUPLAMENTE: (1) o estado `failed`
// durável só passou a ser alcançável com AOS-252 (antes um run falhado ficava em `running` para
// sempre, indistinguível de crash); (2) mesmo com `failed` selado, ninguém COMPUNHA o coordinator
// no caminho de saída de um run, pelo que a compensação nunca corria.
//
// O QUE ESTA COMPOSIÇÃO FAZ, e com que peças EXISTENTES (nada aqui é reinventado):
//
//   1. LIGA o registo de compensações ao dispatcher durável (AOS-021) via
//      [activity.WithCompensationRegistry], na composição de produção
//      ([integration.SecuredConfig].CompensationRegistry, cablado no Bootstrap). É a costura que
//      FALTAVA: uma [activity.Compensation] passa a ter ONDE registar a acção inversa e o
//      coordinator tem de ONDE a ler — o MESMO *saga.CompensationRegistry ([Node.Compensations]).
//   2. No PONTO ÚNICO DE SAÍDA do run ([NodeService.sealTerminalState], AOS-252), quando o selo
//      terminal materializa `failed` — a falha RECUPERÁVEL, a ÚNICA origem da saga na tabela —
//      compõe o [saga.SagaCoordinator] REAL sobre a máquina de estados POR-RUN (AOS-017), o
//      step-ledger durável do nó (AOS-014) e o registo, e CONDUZ failed→compensating→ready. Cada
//      reversão corre dentro de [durable.StepLedger.Apply] (idempotente, 0 reversões duplicadas).
//
// FAIL-CLOSED e NUNCA EM SILÊNCIO (AOS-254/AC2). Um `failed` sem substrato de compensação (sem
// step-ledger durável e/ou sem registo composto) ou sem NENHUMA compensação registada NÃO finge
// que compensou: DECLARA a ausência (span + selo WORM) e deixa o run em `failed`. Transitar para
// `ready` (o "retry limpo" da saga) sem ter revertido nada seria mentir sobre o estado do mundo.
//
// ALCANCE HONESTO (declarado no banner e nos selos): o loop base NUNCA povoa
// [activity.Activity.Compensation] — a tool call do modelo não traz uma acção inversa —, pelo que
// em PRODUÇÃO o registo está VAZIO e o caminho de falha declara sempre "sem compensação". A aresta
// failed→compensating fica ALCANÇÁVEL (o coordinator está composto e CONDU-la quando há
// compensações — provado pela composição por-cadeia-real), mas só um produtor de activities
// reversíveis (AOS-022, a durabilidade PLENA da intenção de compensar) a torna corrente. AOS-254
// entrega a COMPOSIÇÃO e a semântica honesta da ausência; não inventa compensações que não existem.
//
// LIMITE conhecido (eixo, não regressão): o registo é NÓ-SCOPED (uma instância por runtime), como
// o dispatcher durável que o alimenta; a chave é o step_id, não (run_id, step_id). Em produção
// está vazio (o loop não regista), pelo que a coexistência de compensações de runs concorrentes é
// território de AOS-022 (marcador durável por step_id + factory por ToolID no rebuild), o mesmo
// limite já declarado em activity/dispatch.go ("LIMITE HONESTO (durabilidade)").
//
// DEFERIDO (DEF-270) — a COMPENSAÇÃO REAL (`failed`→`compensating`→`ready`, com a Action inversa a
// correr) fica por entregar; o que este ficheiro entrega e prova é a CONDUÇÃO e o ramo ALCANÇÁVEL
// (declarar a ausência, atribuivelmente). Faltam DUAS peças, e enquanto faltarem habilitar a
// compensação seria compensar o run ERRADO: (a) um PRODUTOR de compensações — o loop base nunca
// popula activity.Activity.Compensation, logo o registo está SEMPRE vazio e a transição é código
// morto; (b) o registo RUN-SCOPED — fechá-lo exige mudar o kernel (saga.Compensation carrega só
// StepID; a chave teria de ser run_id:step_id), pelo que o gate Len()==0 acima depende de o registo
// estar GLOBALMENTE vazio, não de "sem compensação para ESTE run". Ver DEF-270 no registo de
// deferimentos e a secção AOS-254 do EPIC-20 (Estado: PARCIAL).

import (
	"context"
	"errors"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/saga"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/platform/audit"
)

const (
	// opSagaCompensate é o nome do span de ESCOPO da saga de rollback (observabilidade da
	// compensação, além dos spans por-transição que a própria [state.Machine] já emite).
	opSagaCompensate = "aos.saga.compensate"

	// sagaCompensationPartition é a partição WORM tamper-evident dos selos de responsabilização
	// da saga (entrada em compensating, conclusão, escalada, e as DECLARAÇÕES de ausência). É uma
	// partição de GOVERNAÇÃO dedicada — o molde de exhaustionDecisionPartition (AOS-263).
	sagaCompensationPartition = "governance.saga"

	// sagaCompensationToolID/sagaRunResourceType nomeiam a acção de governação selada e o seu
	// alvo (o run), para o registo ser atribuível sem ambiguidade.
	sagaCompensationToolID = "aos.saga.rollback"
	sagaRunResourceType    = "run"

	// capSagaCompensate é o rótulo de capability da acção de compensação (auditoria, nunca segredo).
	capSagaCompensate = "cap:saga.compensate"

	// sagaObl é o tipo de obligation que carrega o detalhe legível do selo (rótulo, nunca segredo).
	sagaObl = "saga_compensation"

	// Razões canónicas dos selos WORM da saga (rótulos de auditoria legíveis, nunca segredos).
	reasonSagaEntered        = "saga_entered_compensating"
	reasonSagaCompleted      = "saga_compensated_retry_clean"
	reasonSagaEscalated      = "saga_compensation_exhausted_escalated"
	reasonSagaNoCompensation = "saga_no_compensation_registered"
	reasonSagaDisabled       = "saga_compensation_disabled_no_substrate"
	reasonSagaDriveFailed    = "saga_drive_failed"
)

// driveSagaCompensation conduz a SAGA DE ROLLBACK (AOS-020/AOS-254) sobre um run que ACABOU de
// selar `failed` no caminho de saída de [NodeService.hostRun] (chamado por [sealTerminalState]).
// É a composição de PRODUÇÃO que faltava: sem ela failed→compensating era um caminho morto.
//
// titular é o Principal.NHIID do run: a compensação corre DENTRO do step-ledger, que cifra o
// registo por-titular (AOS-093/245) e — com [durable.WithRequireTitular] — RECUSA um Apply sem
// titular; por isso ele é anexado ao ctx ([durable.ContextWithTitular]).
//
// Corre com context.Background() de propósito: como o selo terminal (AOS-252), é um facto de
// recuperação que NÃO pode ser cancelado pelo ctx do run (shutdown/deadline) — senão a compensação
// de um run cancelado ficaria por fazer e o log mentiria sobre o estado do mundo. NUNCA em
// SILÊNCIO (AC2): sem substrato ou sem compensação registada, DECLARA a ausência (span + WORM).
func (s *NodeService) driveSagaCompensation(rs *runState, titular string) {
	if s.node == nil || s.node.stateGates == nil {
		return
	}
	gate := s.node.stateGates.resolveGate(rs.runID)
	if gate == nil || gate.m == nil {
		return
	}
	// Só age sobre um run que está MESMO em `failed` — a falha RECUPERÁVEL, a única origem da
	// saga na tabela de AOS-017. complete/timed_out/killed/paused/waiting não têm rollback (e o
	// selo de AOS-252 nunca os reescreve para `failed`).
	if gate.m.Current() != state.Failed {
		return
	}

	// AOS-299: contexto FRESCO (o do run já terminou), mas com o token do lease anexado — a
	// compensação escreve no step-ledger, e essas escritas são fenceadas. Sem isto a saga
	// recusaria cada reversão com ErrStaleFencingToken, que seria trocar um efeito por reverter
	// por um efeito NÃO revertido.
	ctx := durable.ContextWithFencingToken(context.Background(), rs.lease.Token)

	// SUBSTRATO em falta ⇒ compensação DESLIGADA, DECLARADA (não silenciosa). Sem o step-ledger
	// durável (AOS_DURABLE_EXECUTION) não há idempotência de compensação; sem registo composto não
	// há acções inversas a percorrer. Em qualquer caso o run FICA em `failed` (honesto).
	if s.node.Ledger == nil || s.node.Compensations == nil {
		s.declareSagaAbsence(ctx, rs.runID, titular, reasonSagaDisabled,
			"compensacao DESLIGADA: step-ledger duravel e/ou registo de compensacoes nao compostos (exige AOS_DURABLE_EXECUTION)")
		s.log("saga (AOS-254): run %q selado FAILED mas a compensacao esta DESLIGADA (sem ledger/registo) — DECLARADO, run permanece em failed", rs.runID)
		return
	}

	// SEM COMPENSAÇÃO REGISTADA ⇒ declara a AUSÊNCIA (AC2). É o caso de PRODUÇÃO: o loop base nunca
	// povoa [activity.Activity.Compensation], logo o registo está vazio. Reverter nada e transitar
	// para `ready` seria fingir um "retry limpo" — em vez disso o run FICA em `failed` e o selo WORM
	// regista, atribuivelmente, que efeitos porventura já aplicados NÃO foram revertidos.
	if s.node.Compensations.Len() == 0 {
		s.declareSagaAbsence(ctx, rs.runID, titular, reasonSagaNoCompensation,
			"run FAILED sem NENHUMA compensacao registada — efeitos ja aplicados (se algum) NAO revertidos; failed->compensating fica alcancavel mas nao ha accao inversa a correr")
		s.log("saga (AOS-254): run %q FAILED sem compensacao registada — AUSENCIA declarada (span+WORM), run permanece em failed", rs.runID)
		return
	}

	// HÁ COMPENSAÇÕES: compõe o coordinator REAL sobre a máquina de estados POR-RUN (AOS-017), o
	// step-ledger durável do nó (AOS-014) e o registo — e conduz failed→compensating→ready.
	obs := &sagaWORMObserver{svc: s, runID: rs.runID, titular: titular}
	coord, err := saga.NewSagaCoordinator(gate.m, s.node.Ledger, s.node.Compensations, saga.WithObserver(obs))
	if err != nil {
		s.declareSagaAbsence(ctx, rs.runID, titular, reasonSagaDriveFailed,
			"composicao do coordinator de compensacao FALHOU")
		s.log("saga (AOS-254): composicao do coordinator para o run %q FALHOU: %v", rs.runID, err)
		return
	}

	// O titular anexado ao ctx é a chave por-titular sob a qual o ledger sela o registo de cada
	// reversão (comp-<step_id>); sem ele o Apply seria RECUSADO ([durable.WithRequireTitular]).
	ctx = durable.ContextWithTitular(ctx, titular)

	_, span := s.sagaTracer().StartSpan(ctx, opSagaCompensate)
	span.SetAttribute(agentruntime.AttrRunID, rs.runID)
	span.SetAttribute("aos.saga.registered", int64(s.node.Compensations.Len()))
	defer span.End()

	switch cerr := coord.Compensate(ctx); {
	case cerr == nil:
		span.SetAttribute("aos.saga.outcome", "compensated")
		s.log("saga (AOS-254): run %q COMPENSADO (failed->compensating->ready, retry limpo) — compensacoes percorridas por ordem inversa via step-ledger idempotente", rs.runID)
	case errors.Is(cerr, saga.ErrCompensationExhausted):
		// A saga NÃO finge sucesso: o run FICA preso em `compensating` e o observer já selou a
		// escalada no WORM. Declarado, não silencioso.
		span.SetAttribute("aos.saga.outcome", "escalated")
		s.log("saga (AOS-254): compensacao do run %q ESGOTOU a politica de retry — run PRESO em compensating, ESCALADO: %v", rs.runID, cerr)
	default:
		// Falha de transição/Event Store (ex.: perda de quórum). Declarada — nunca silenciosa.
		span.SetAttribute("aos.saga.outcome", "drive_failed")
		s.declareSagaAbsence(ctx, rs.runID, titular, reasonSagaDriveFailed,
			"conducao da saga FALHOU (transicao/Event Store recusou) — run pode ter ficado a meio da compensacao")
		s.log("saga (AOS-254): conducao da compensacao do run %q FALHOU: %v", rs.runID, cerr)
	}
}

// declareSagaAbsence DECLARA — span + selo WORM — que a compensação NÃO correu (substrato em falta,
// nenhuma compensação registada, ou falha da condução), em vez de o caminho de falha calar. É a
// materialização de "nunca em silêncio" (AC2): o run permanece em `failed` e o selo atribui a razão.
func (s *NodeService) declareSagaAbsence(ctx context.Context, runID, titular, reason, detail string) {
	_, span := s.sagaTracer().StartSpan(ctx, opSagaCompensate)
	span.SetAttribute(agentruntime.AttrRunID, runID)
	span.SetAttribute("aos.saga.reason", reason)
	span.SetAttribute("aos.saga.outcome", "absent")
	span.End()
	s.sealSagaRecord(ctx, runID, titular, audit.DecisionDeny, reason, detail)
}

// sealSagaRecord grava UM registo tamper-evident na partição da saga. Só metadados de
// responsabilização (nunca segredos): o titular (NHI, um identificador — como os demais selos do
// nó), o run, a razão canónica e um detalhe LEGÍVEL fixo. Uma falha do WORM é registada em voz
// alta (o selo é a prova; sem ele fica só o log do processo).
func (s *NodeService) sealSagaRecord(ctx context.Context, runID, titular string, decision audit.Decision, reason, detail string) {
	if s.node == nil || s.node.WORM == nil {
		s.log("saga (AOS-254): WORM indisponivel — selo de compensacao do run %q NAO gravado (%s)", runID, reason)
		return
	}
	rec := audit.AuditRecord{
		Partition:  sagaCompensationPartition,
		Timestamp:  time.Now().UTC(),
		Decision:   decision,
		Reason:     reason,
		Principal:  audit.Principal{NHIID: titular},
		Capability: capSagaCompensate,
		RunID:      runID,
		ToolID:     sagaCompensationToolID,
		Resource:   audit.Resource{Type: sagaRunResourceType, Value: runID},
		Obligations: []audit.Obligation{{
			Type:   sagaObl,
			Params: map[string]string{"detail": detail},
		}},
	}
	if _, err := s.node.WORM.Append(ctx, rec); err != nil {
		s.log("saga (AOS-254): selo WORM da compensacao do run %q FALHOU (%s): %v", runID, reason, err)
	}
}

// sagaTracer devolve o tracer do nó (o MESMO da observabilidade OTLP quando ligada), com fallback
// nil-safe para o [agentruntime.NoopTracer] — para que a declaração de span nunca dependa de a
// observabilidade estar composta.
func (s *NodeService) sagaTracer() agentruntime.Tracer {
	if s.node != nil && s.node.Tracer != nil {
		return s.node.Tracer
	}
	return agentruntime.NoopTracer{}
}

// sagaWORMObserver é o gancho de observabilidade do [saga.SagaCoordinator] que sela o CICLO DE VIDA
// da saga no WORM (entrada, conclusão, escalada). As chaves entram na forma OPACA (hash) que o
// coordinator já produz — nunca a chave em claro nem o payload da compensação. Cada REVERSÃO em si
// já é durável no step-ledger (evento step.ledger.applied, chave comp-<step_id>), pelo que aqui não
// se re-sela cada uma — só os marcos de responsabilização da saga.
type sagaWORMObserver struct {
	svc     *NodeService
	runID   string
	titular string
}

// Started — a saga entrou (ou retomou) a compensação: sela a ENTRADA em compensating.
func (o *sagaWORMObserver) Started(runID string, count int) {
	o.svc.sealSagaRecord(context.Background(), runID, o.titular, audit.DecisionAllow, reasonSagaEntered,
		"saga entrou em compensating — compensacoes a percorrer por ordem inversa (LIFO idempotente)")
	o.svc.log("saga (AOS-254): run %q entrou em compensating — %d compensacao(oes) a percorrer", runID, count)
}

// Compensated — uma reversão resolveu-se (correu agora ou foi deduplicada). Rasto de log; o registo
// durável da reversão vive já no step-ledger, sem necessidade de o duplicar no WORM.
func (o *sagaWORMObserver) Compensated(keyHash string, ranNow bool) {
	verbo := "reproduzida (dedup — ja aplicada, sem re-execucao)"
	if ranNow {
		verbo = "revertida AGORA"
	}
	o.svc.log("saga (AOS-254): run %q compensacao %s %s", o.runID, keyHash, verbo)
}

// Retry — uma tentativa de compensação falhou e vai repetir (idempotente). Rasto de log.
func (o *sagaWORMObserver) Retry(keyHash string, attempt int, err error) {
	o.svc.log("saga (AOS-254): run %q compensacao %s tentativa %d FALHOU (retry idempotente): %v", o.runID, keyHash, attempt, err)
}

// Escalated — uma compensação esgotou a política de retry: sela a ESCALADA. O run FICA preso em
// compensating (a tabela de AOS-017 não tem aresta compensating→killed) — a saga não finge sucesso.
func (o *sagaWORMObserver) Escalated(keyHash string, err error) {
	o.svc.sealSagaRecord(context.Background(), o.runID, o.titular, audit.DecisionDeny, reasonSagaEscalated,
		"compensacao irrecuperavel apos retries — run PRESO em compensating, escalado por alerta")
	o.svc.log("saga (AOS-254): run %q compensacao %s ESCALADA (irrecuperavel): %v", o.runID, keyHash, err)
}

// Completed — a saga compensou tudo e transitou compensating→ready: sela a CONCLUSÃO (retry limpo).
func (o *sagaWORMObserver) Completed(runID string) {
	o.svc.sealSagaRecord(context.Background(), runID, o.titular, audit.DecisionAllow, reasonSagaCompleted,
		"saga concluida: compensating->ready (retry limpo) — todas as compensacoes registadas revertidas")
	o.svc.log("saga (AOS-254): run %q compensacao CONCLUIDA — compensating->ready", runID)
}

// Assegura em compile-time que o observador satisfaz a porta do coordinator.
var _ saga.Observer = (*sagaWORMObserver)(nil)
