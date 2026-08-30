package runlifecycle

import (
	"context"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// emitters.go — OS CHAMADORES DE PRODUÇÃO QUE NÃO EXISTIAM (DEF-272, DEF-273).
//
// O registo de deferimentos é explícito sobre o que faltava, e não era contrato nem
// validação — era quem chamasse:
//
//	DEF-272: «o que NÃO existe é quem chame `Recorder.RecordVerdict` por um
//	         verificador em execução — é wiring do ciclo-de-vida do run».
//	DEF-273: «não existe implementação de `plandispatch.PayloadView` nem chamador de
//	         `Recorder.RecordPayloadPublished` fora de testes».
//
// E era explícito sobre porque não podiam viver onde estavam apontados: o eixo
// anterior (AOS-238) está fechado e é PROIBIDO por guard-test de importar o módulo de
// ciclo de vida. Este pacote é o primeiro sítio no repositório que pode legalmente
// conter os dois (ADR-023 §2.6). As implementações de LEITURA vivem em readers.go; os
// EMISSORES vivem aqui.
//
// # Porque os emissores exigem POSSE
//
// Um veredicto e uma publicação de payload são factos de um run em execução, e um run
// em execução tem um dono (ADR-023 §2.1). Emitir sem posse seria escrever factos de um
// run que se não serve — que é a segunda autoridade que este ticket existe para não
// haver. Por isso os emissores não recebem um Event Store: recebem uma [Tenure].

// planFenced constrói um [durable.FencedAppender] que decide o fencing pelo LEASE DO
// RUN mas escreve no stream do PLANO.
//
// # Porque um redireccionamento, e não uma segunda implementação do fencing
//
// O [durable.FencedAppender] usa o mesmo identificador para duas coisas: resolver o
// token corrente e escolher o stream de escrita. Aqui essas duas coisas divergem — a
// autoridade é `lease:<run_id>`, o destino é o stream do plano.
//
// A alternativa óbvia seria repetir aqui a lógica de fencing (ler o token corrente,
// comparar, consultar a expiração, escrever). Seria uma SEGUNDA implementação do mesmo
// conceito, que é exactamente a divergência que este ticket existe para acabar: duas
// implementações do mesmo mecanismo divergem, e a que diverge em silêncio é a que
// concede.
//
// O redireccionamento reutiliza o enforcement INTEIRO — token inferior ao corrente E
// lease expirado/largado ([durable.LeaseExpiryAuthority]) — e muda só o destino. Zero
// lógica de fencing escrita aqui.
type planFenced struct {
	store EventStore
	to    string
}

var _ durable.EventStore = planFenced{}

func (p planFenced) Append(ctx context.Context, _ string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return p.store.Append(ctx, p.to, in, opts...)
}

func (p planFenced) Read(ctx context.Context, _ string, fromSeq uint64) ([]eventstore.Event, error) {
	return p.store.Read(ctx, p.to, fromSeq)
}

// PlanRecorder é o emissor dos factos do domínio do plano de um run POSSUÍDO: o
// veredicto de um verificador (AOS-271), a publicação de um contrato de saída
// (AOS-272) e a decisão de ramo do despachante (AOS-270).
//
// Todas as escritas são fenced pelo lease do RUN, ainda que o destino seja o stream do
// PLANO — ver [planFenced]. Um emissor cuja posse foi superada, expirou ou foi largada
// é recusado com [durable.ErrStaleFencingToken] SEM tocar no log.
//
// Construir com [NewPlanRecorder].
type PlanRecorder struct {
	tenure   *Tenure
	planID   string
	prod     eventstore.Producer
	recorder *plannerevents.Recorder
}

// PlanID devolve o plano a que este emissor está amarrado.
func (p *PlanRecorder) PlanID() string { return p.planID }

// producer devolve a NHI emissora com que este emissor foi construído, para que o
// grafo materializado sob a MESMA posse seja atribuído à MESMA identidade. Sem isto,
// o `plan.materialized` e os `task.node.created` que ele produz sairiam com produtores
// diferentes — o mesmo acto com duas assinaturas no log.
func (p *PlanRecorder) producer() eventstore.Producer { return p.prod }

// NewPlanRecorder constrói o emissor sobre a posse de um run e o stream do seu plano.
// `producer` é a NHI emissora gravada nos factos (ADR-003).
func NewPlanRecorder(t *Tenure, planID string, producer eventstore.Producer) (*PlanRecorder, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: posse nil", ErrDeps)
	}
	if planID == "" {
		return nil, fmt.Errorf("%w: plan_id", ErrEmptyRunID)
	}
	tokens, ok := t.leases.(durable.TokenSource)
	if !ok {
		return nil, fmt.Errorf("%w: a autoridade de lease não é uma durable.TokenSource", ErrDeps)
	}
	fenced, err := durable.NewFencedAppender(planFenced{store: t.store, to: planID}, tokens)
	if err != nil {
		return nil, err
	}
	rec, err := plannerevents.NewRecorder(tenureAppender{t: t, fenced: fenced}, plannerevents.WithProducer(producer))
	if err != nil {
		return nil, err
	}
	return &PlanRecorder{tenure: t, planID: planID, prod: producer, recorder: rec}, nil
}

// tenureAppender é a via de escrita que o [plannerevents.Recorder] consome: aplica a
// recusa LOCAL da posse largada ([ErrNotHeld]) e delega no appender fenced, que aplica
// a recusa DURÁVEL. As duas camadas do ADR-023 §2.4, na ordem em que custam menos.
type tenureAppender struct {
	t      *Tenure
	fenced *durable.FencedAppender
}

var _ plannerevents.Appender = tenureAppender{}

func (a tenureAppender) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	a.t.mu.RLock()
	released, token := a.t.released, a.t.lease.Token
	a.t.mu.RUnlock()
	if released {
		return eventstore.AppendResult{}, fmt.Errorf("%w: run %s", ErrNotHeld, a.t.runID)
	}
	// O runID governa o FENCING (é a chave do lease); o streamID é ignorado pelo
	// [planFenced], que escreve sempre no stream do plano a que foi amarrado.
	_ = streamID
	return a.fenced.Append(ctx, a.t.runID, token, in, opts...)
}

// RecordVerdict EMITE o veredicto estruturado de um nó verificador em execução — o
// chamador de produção cuja ausência é o DEF-272.
//
// Não valida nada por si: delega em [plannerevents.Recorder.RecordVerdict], que passa
// obrigatoriamente por `NewVerdictRecorded`. É de propósito que este método é fino —
// uma validação a mais aqui seria uma SEGUNDA fronteira sobre o que é um veredicto
// admissível, e duas fronteiras sobre a mesma coisa divergem.
//
// Exige o NÓ do documento APROVADO porque a validação a jusante AMARRA os `subjects[]`
// às arestas de entrada do verificador: sem o nó, um veredicto podia atribuir-se a
// trabalho que o emissor nunca observou.
func (p *PlanRecorder) RecordVerdict(ctx context.Context, payload plannerevents.VerdictRecordedPayload, verifier plan.Node) (uint64, error) {
	if payload.PlanID == "" {
		payload.PlanID = p.planID
	}
	if payload.PlanID != p.planID {
		return 0, fmt.Errorf("%w: veredicto para %q, emissor amarrado a %q", ErrForeignPlan, payload.PlanID, p.planID)
	}
	return p.recorder.RecordVerdict(ctx, payload, verifier)
}

// RecordPayloadPublished EMITE a referência de um contrato de saída cumprido — o
// chamador de produção cuja ausência é metade do DEF-273 (a outra metade, a
// implementação da [plandispatch.PayloadView], é o [PayloadReader]).
//
// Como no veredicto, a validação é a de `NewPayloadPublished`: `type`, `taint` e
// `contract_digest` são DERIVADOS do contrato do documento aprovado, nunca aceites de
// quem publica — é isso que impede a publicação de desclassificar.
func (p *PlanRecorder) RecordPayloadPublished(ctx context.Context, payload plannerevents.PayloadPublishedPayload, producer plan.Node) (uint64, error) {
	if payload.PlanID == "" {
		payload.PlanID = p.planID
	}
	if payload.PlanID != p.planID {
		return 0, fmt.Errorf("%w: publicação para %q, emissor amarrado a %q", ErrForeignPlan, payload.PlanID, p.planID)
	}
	return p.recorder.RecordPayloadPublished(ctx, payload, producer)
}

// BranchJournal devolve a implementação de [plandispatch.BranchJournal] sobre este
// emissor: o registo append-only das decisões de ramo DO DESPACHANTE.
//
// # Isto NÃO contradiz «o SCH é derivador» (ADR-023 §2.2)
//
// Uma decisão de ramo não é uma transição de estado de ciclo de vida: é o registo de
// uma AVALIAÇÃO que ADR-022 §2.4(3) exige que seja um facto e não um cálculo repetido,
// e vive no stream do PLANO, não no do run. O despachante continua sem escrever uma
// única transição de estado — que é a fronteira exacta que a decisão fixa.
//
// A distinção é observável e não só declarada: o que aqui se escreve tem o tipo
// `plan.branch_decided` e nunca `state.transition`, `task.node.state_changed` ou
// `task.node.created`.
func (p *PlanRecorder) BranchJournal() plandispatch.BranchJournal {
	return &branchJournal{rec: p}
}

// branchJournal implementa [plandispatch.BranchJournal] sobre o [PlanRecorder].
type branchJournal struct{ rec *PlanRecorder }

var _ plandispatch.BranchJournal = (*branchJournal)(nil)

// Decisions devolve as decisões JÁ REGISTADAS do plano, do log, indexadas por node_id.
// Uma leitura por passagem, como a porta exige — vista coerente, não mosaico.
func (j *branchJournal) Decisions(ctx context.Context, planID string) (map[string]plandispatch.BranchDecision, error) {
	if planID != j.rec.planID {
		return nil, fmt.Errorf("%w: pedido %q, amarrado %q", ErrForeignPlan, planID, j.rec.planID)
	}
	events, err := readStream(ctx, j.rec.tenure.store, planID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]plandispatch.BranchDecision)
	for i := range events {
		if events[i].Type != plannerevents.EventBranchDecided {
			continue
		}
		var p plannerevents.BranchDecidedPayload
		if uerr := unmarshalPayload(events[i].Payload, &p); uerr != nil {
			return nil, fmt.Errorf("runlifecycle: decisão de ramo ilegível no plano %q (seq=%d): %w", planID, events[i].Seq, uerr)
		}
		if _, seen := out[p.NodeID]; seen {
			// Primeiro vence: a decisão de ramo de um nó é um facto ÚNICO e imutável
			// (step id sem discriminador de tentativa). Um segundo facto não pode
			// existir; se existisse, deixá-lo vencer seria deixar um ramo mudar no
			// replay, que é precisamente o que ADR-022 §2.4(3) proíbe.
			continue
		}
		out[p.NodeID] = plandispatch.BranchDecision{
			NodeID:          p.NodeID,
			Taken:           p.Taken,
			ConditionDigest: p.ConditionDigest,
			Sources:         append([]string(nil), p.Sources...),
		}
	}
	return out, nil
}

// Record apensa UMA decisão de ramo. É idempotente por (plan_id, node_id) — a
// garantia é do step id do [plannerevents.Recorder], não desta função.
func (j *branchJournal) Record(ctx context.Context, planID string, d plandispatch.BranchDecision) error {
	if planID != j.rec.planID {
		return fmt.Errorf("%w: pedido %q, amarrado %q", ErrForeignPlan, planID, j.rec.planID)
	}
	_, err := j.rec.recorder.RecordBranchDecided(ctx, plannerevents.BranchDecidedPayload{
		PlanID:          planID,
		NodeID:          d.NodeID,
		Taken:           d.Taken,
		ConditionDigest: d.ConditionDigest,
		Sources:         append([]string(nil), d.Sources...),
	})
	return err
}
