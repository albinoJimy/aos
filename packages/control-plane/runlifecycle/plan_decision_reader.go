package runlifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// plan_decision_reader.go — O ESTADO DO GATE DE APROVAÇÃO, DERIVADO DO LOG (AOS-408).
//
// A aprovação de plano é ASSÍNCRONA (decisão do dono, 2026-09-17): o processo que decompõe não
// espera pelo humano. Para isso o estado «pendente de decisão» tem de ser um FACTO durável e não a
// ausência de factos — senão «à espera do humano» não se distingue de «nunca foi proposto», e um
// restart perde o caso.
//
// Este leitor não inventa persistência: o pendente é `plan.validated` apenso SEM decisão terminal
// (`plan.approved`/`plan.rejected`), dentro do TTL contado do instante do próprio `plan.validated`.
// É o irmão do [GateReader], com a mesma disciplina: relê o stream UMA vez e fixa um retrato
// imutável; de leitura, nunca escreve; fail-closed sobre um plano que não é o amarrado.
//
// Também satisfaz [plandispatch.CardOracle], e é esse o ponto em que substitui o `cardsFailClosed`
// que o `aos-orq` passava ao despacho: um nó que exige cartão só despacha se a decisão do plano
// estiver APROVADA. Sem decisão ⇒ false (o nó espera, sem consumir headroom).

// PlanDecisionReader lê o estado do gate de um plano. Amarrado a UM plan_id na construção.
type PlanDecisionReader struct {
	store  EventStore
	planID string
}

// NewPlanDecisionReader amarra o leitor ao stream de um plano. store e plan_id são obrigatórios.
func NewPlanDecisionReader(store EventStore, planID string) (*PlanDecisionReader, error) {
	if store == nil {
		return nil, ErrDeps
	}
	if planID == "" {
		return nil, fmt.Errorf("%w: plan_id", ErrEmptyRunID)
	}
	return &PlanDecisionReader{store: store, planID: planID}, nil
}

// Snapshot relê o stream do plano e fixa o retrato do gate.
//
// PRECEDÊNCIA TERMINAL: a PRIMEIRA decisão terminal vence e as seguintes são ignoradas — a mesma
// regra do jornal de ramos. É preciso ser explícito porque o step id dos factos de decisão é
// `planstep:decision:<decision>`, o que permite um `approved` e um `rejected` coexistirem no mesmo
// stream (dois operadores, ou uma recusa por TTL seguida de uma aprovação tardia). Sem esta regra,
// a ordem de leitura decidiria o veredicto.
func (r *PlanDecisionReader) Snapshot(ctx context.Context) (*PlanDecisionSnapshot, error) {
	events, err := readStream(ctx, r.store, r.planID)
	if err != nil {
		return nil, fmt.Errorf("runlifecycle: retrato da decisão do plano %q: %w", r.planID, err)
	}
	snap := &PlanDecisionSnapshot{planID: r.planID}
	for i := range events {
		switch events[i].Type {
		case plannerevents.EventProposed:
			snap.proposed = true
		case plannerevents.EventValidated:
			if !snap.validated {
				snap.validated = true
				snap.validatedAt = instanteDoEvento(events[i].Ts)
				snap.planHash = hashDoPayload(events[i].Payload)
				snap.snapshotDigest = digestDoSnapshotDoPayload(events[i].Payload)
			}
		case plannerevents.EventApproved, plannerevents.EventRejected:
			if snap.decision == "" {
				if events[i].Type == plannerevents.EventApproved {
					snap.decision = plannerevents.DecisionApproved
				} else {
					snap.decision = plannerevents.DecisionRejected
				}
				snap.decidedAt = instanteDoEvento(events[i].Ts)
				snap.decidedHash = hashDoPayload(events[i].Payload)
				snap.decisionRef = refDoPayload(events[i].Payload)
			}
		}
	}
	return snap, nil
}

// instanteDoEvento converte o `ts` do envelope (RFC3339Nano, UTC) no instante que ancora o TTL.
// Um `ts` ilegível devolve o zero de [time.Time], e o zero DESLIGA a expiração em vez de expirar
// tudo: um carimbo que não se sabe ler não é prova de que o prazo passou, e recusar por causa dele
// negaria decisões legítimas. A falta de prazo fica visível — [PlanDecisionSnapshot.ValidatedAt]
// devolve o zero — em vez de silenciosa.
func instanteDoEvento(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// digestDoSnapshotDoPayload extrai o `snapshot_digest` selado no `plan.validated`.
func digestDoSnapshotDoPayload(payload []byte) string {
	var corpo struct {
		SnapshotDigest string `json:"snapshot_digest"`
	}
	if err := unmarshalPayload(payload, &corpo); err != nil {
		return ""
	}
	return corpo.SnapshotDigest
}

// refDoPayload extrai o `decision_ref` — QUEM decidiu, sem assinatura nem PII. É o que distingue
// uma decisão humana (`hitl:<principal>`) de uma auto-aprovação da máquina (`auto:autonomy:<nível>`)
// ou de uma expiração de prazo (`ttl:expirado`). Sem esta distinção, uma auto-aprovação de um plano
// inócuo satisfaria o gate humano de um plano perigoso.
func refDoPayload(payload []byte) string {
	var corpo struct {
		DecisionRef string `json:"decision_ref"`
	}
	if err := unmarshalPayload(payload, &corpo); err != nil {
		return ""
	}
	return corpo.DecisionRef
}

// hashDoPayload extrai o `plan_hash` de um payload de facto do plano. Vazio quando o payload não o
// traz — o chamador trata a ausência como «não verificável» e recusa, nunca como «bate».
func hashDoPayload(payload []byte) string {
	var corpo struct {
		PlanHash string `json:"plan_hash"`
	}
	if err := unmarshalPayload(payload, &corpo); err != nil {
		return ""
	}
	return corpo.PlanHash
}

// PlanDecisionSnapshot é o retrato IMUTÁVEL do estado do gate de um plano.
type PlanDecisionSnapshot struct {
	planID         string
	proposed       bool
	validated      bool
	validatedAt    time.Time
	planHash       string
	snapshotDigest string
	decision       plannerevents.Decision
	decidedAt      time.Time
	decidedHash    string
	decisionRef    string
}

var _ plandispatch.CardOracle = (*PlanDecisionSnapshot)(nil)

// PlanID é o plano a que este retrato está amarrado.
func (s *PlanDecisionSnapshot) PlanID() string { return s.planID }

// Proposed/Validated reportam os factos a montante da decisão.
func (s *PlanDecisionSnapshot) Proposed() bool  { return s.proposed }
func (s *PlanDecisionSnapshot) Validated() bool { return s.validated }

// PlanHash é o hash do plano VALIDADO — o organigrama que a decisão do humano cobre.
func (s *PlanDecisionSnapshot) PlanHash() string { return s.planHash }

// SnapshotDigest é o digest do CONTEÚDO do snapshot sob o qual o plano foi validado (vazio num
// facto anterior ao AOS-408). Quem decide ou materializa tem de apresentar o mesmo conteúdo.
func (s *PlanDecisionSnapshot) SnapshotDigest() string { return s.snapshotDigest }

// ValidatedAt é a âncora do TTL do pendente (o instante do `plan.validated`).
func (s *PlanDecisionSnapshot) ValidatedAt() time.Time { return s.validatedAt }

// Decision é a decisão terminal já registada (vazia se não houver).
func (s *PlanDecisionSnapshot) Decision() plannerevents.Decision { return s.decision }

// DecidedAt é o instante da decisão terminal.
func (s *PlanDecisionSnapshot) DecidedAt() time.Time { return s.decidedAt }

// Approved diz se o plano tem decisão terminal APROVADA.
//
// ATENÇÃO: só por si NÃO autoriza nada. A decisão aprova um ORGANIGRAMA CONCRETO, e quem a consome
// tem de confrontar [PlanDecisionSnapshot.DecidedHash] com o hash do documento em mão e
// [PlanDecisionSnapshot.DecisionRef] com a origem exigida. Um `plan_id` reutilizado por duas
// decomposições tem UMA só âncora de `plan.validated` (o step id é fixo, logo a segunda é
// idempotente) mas pode ter decisões de hashes diferentes: usar `Approved()` sozinho deixaria a
// aprovação de um plano materializar outro.
func (s *PlanDecisionSnapshot) Approved() bool {
	return s.decision == plannerevents.DecisionApproved
}

// DecidedHash é o `plan_hash` que a DECISÃO carrega — o organigrama que foi de facto decidido.
// Distinto de [PlanDecisionSnapshot.PlanHash], que é o do primeiro `plan.validated`.
func (s *PlanDecisionSnapshot) DecidedHash() string { return s.decidedHash }

// DecisionRef é a referência da decisão (`hitl:<principal>`, `auto:autonomy:<nível>`,
// `ttl:expirado`, …): QUEM decidiu, sem assinatura nem PII.
func (s *PlanDecisionSnapshot) DecisionRef() string { return s.decisionRef }

// AprovadoPorHumano diz se existe decisão APROVADA, para ESTE hash, tomada por um humano
// autenticado (referência `hitl:`).
//
// É o predicado que o gate de efeito consome. As três condições são inseparáveis: sem o hash, a
// aprovação de um organigrama autoriza outro; sem a origem, a auto-aprovação por nível de
// autonomia — que existe para planos SEM risco — passaria a autorizar planos de risco.
func (s *PlanDecisionSnapshot) AprovadoPorHumano(planHash string) bool {
	if !s.Approved() || planHash == "" || s.decidedHash != planHash {
		return false
	}
	return strings.HasPrefix(s.decisionRef, PrefixoDecisaoHumana)
}

// PrefixoDecisaoHumana é o prefixo da referência de uma decisão tomada por um humano autenticado
// (assinatura verificada contra chave pinada). Vive aqui, junto de quem o interpreta, para o
// produtor e o consumidor não convencionarem o mesmo literal em dois sítios.
const PrefixoDecisaoHumana = "hitl:"

// Pendente diz se o plano está À ESPERA de decisão humana no instante `agora`: validado, sem
// decisão terminal e dentro do TTL. Um TTL não-positivo significa «sem prazo».
func (s *PlanDecisionSnapshot) Pendente(agora time.Time, ttl time.Duration) bool {
	if !s.validated || s.decision != "" {
		return false
	}
	return !s.Expirado(agora, ttl)
}

// Expirado diz se o prazo do pendente passou. É imposto no MOMENTO DA DECISÃO e não por um
// varredor: um pendente fora do prazo é recusado mesmo que ninguém tenha ainda escrito a
// expiração — o prazo não é uma corrida com o varredor (é a disciplina do `handleApprove` do nó).
func (s *PlanDecisionSnapshot) Expirado(agora time.Time, ttl time.Duration) bool {
	if ttl <= 0 || !s.validated || s.validatedAt.IsZero() {
		return false
	}
	return agora.Sub(s.validatedAt) > ttl
}

// Cleared satisfaz [plandispatch.CardOracle]: o cartão de um nó só está resolvido se a decisão do
// PLANO estiver aprovada.
//
// A granularidade é do PLANO e não do nó, e isso é deliberado: a decisão assinada cobre o cartão
// inteiro, e o gate recusa fail-closed assinar um cartão sem que TODOS os nós de risco constem dos
// nós revistos. O que fica de fora — e está declarado no ticket — é a evidência POR NÓ no log: um
// auditor vê «este plano foi aprovado», não «este nó foi revisto».
//
// Fail-closed: plano diferente do amarrado é erro (nunca um false silencioso); sem decisão, false.
func (s *PlanDecisionSnapshot) Cleared(_ context.Context, planID, _ string) (bool, error) {
	if planID != s.planID {
		return false, fmt.Errorf("%w: pedido %q, amarrado %q", ErrForeignPlan, planID, s.planID)
	}
	return s.Approved(), nil
}
