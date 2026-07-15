package promotion

import (
	"context"
	"sync"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/domain"
)

// SelfAuthoredClassifier decide se uma entrada é uma SKILL AUTO-ESCRITA — a mudança
// de maior risco do sistema (misevolution/drift), que exige eval-gate + ratificação
// além da verificação de integridade. A distinção é ESTRUTURAL (por kind), nunca uma
// flag confiável do publicador.
type SelfAuthoredClassifier func(domain.Entry) bool

// DefaultSelfAuthoredClassifier classifica como auto-escrita TODA a entrada
// kind=skill, INDEPENDENTEMENTE da origem declarada.
//
// SEGURANÇA (AOS-053 Q1): a classificação assenta SÓ no kind — que está ligado ao
// material AUTENTICADO (o digest cobre (kind, contract) e a assinatura cobre
// (id, version, digest)), logo não é forjável sem quebrar a assinatura — e NUNCA no
// Provenance.Origin, uma string livre do publicador que NÃO é coberta pela assinatura
// nem pelo digest e é, portanto, FORJÁVEL. Uma versão anterior isentava do eval-gate
// qualquer skill cuja Origin não denotasse "self" (ex.: "git+https://…"); no modelo
// de ameaça de misevolution o agente que escreve a própria skill é, por definição, um
// publicador com chave confiável, pelo que bastava declarar uma origem externa para
// uma skill auto-escrita chegar a active SEM eval-gate e SEM ratificação. Fail-closed
// no eixo do risco: TODA a skill exige a governação completa; a isenção de uma skill
// de origem COMPROVADAMENTE externa fica reservada a uma atestação criptográfica de
// origem (TOFU/AOS-049), nunca a uma flag do publicador. Um tool/mcp_server
// (kind≠skill) continua a atravessar apenas a verificação de integridade.
func DefaultSelfAuthoredClassifier(e domain.Entry) bool {
	return e.Kind == domain.KindSkill
}

// ApprovalLedger regista as APROVAÇÕES de governação (eval-gate PASSOU + ratificação
// humana VÁLIDA) por (id, version, digest). É o elo entre o Pipeline (que corre os
// gates) e o [CompositeVerifier] (o gate estrutural do Registry): o Pipeline
// APROVA após passar os gates; o verificador EXIGE a aprovação para promover uma
// skill auto-escrita. Como a chave inclui o digest, uma aprovação nunca é
// reutilizável para conteúdo adulterado (o digest muda com o conteúdo). Seguro para
// concorrência. Construir com [NewApprovalLedger].
type ApprovalLedger struct {
	mu       sync.RWMutex
	approved map[string]struct{}
}

// NewApprovalLedger constrói um ledger vazio (fail-closed: nada é aprovado por
// omissão).
func NewApprovalLedger() *ApprovalLedger {
	return &ApprovalLedger{approved: make(map[string]struct{})}
}

func approvalKey(id string, v domain.Version, digest string) string {
	return id + "@" + v.String() + "#" + digest
}

// Approve regista a aprovação de governação de (id, version, digest). Idempotente.
func (l *ApprovalLedger) Approve(id string, v domain.Version, digest string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.approved[approvalKey(id, v, digest)] = struct{}{}
}

// IsApproved indica se (id, version, digest) tem aprovação de governação registada.
func (l *ApprovalLedger) IsApproved(id string, v domain.Version, digest string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.approved[approvalKey(id, v, digest)]
	return ok
}

// LoadFromAudit reconstrói as aprovações de governação a partir das transições
// SELADAS na partição de audit WORM (AOS-011), tornando o fecho de governação
// DURÁVEL (AOS-053 Q2): as aprovações (eval-gate PASSOU + ratificação VÁLIDA)
// sobrevivem ao reinício do processo, em vez de viverem apenas no mapa em memória.
// Sem isto, um Registry reconstruído sobre o MESMO Event Store após um arranque a
// frio teria o ledger vazio — recusando a re-activação (rollback) de uma skill
// auto-escrita previamente aprovada. A prova DURÁVEL de aprovação é o selo
// `ratified`, escrito só DEPOIS de o eval-gate passar E a ratificação humana validar;
// é rechaveado por (id, version, digest) — a mesma chave do ledger, pelo que uma
// aprovação nunca é reutilizável para conteúdo adulterado (o digest muda com o
// conteúdo). Idempotente e fail-closed: um erro de leitura do audit (ou uma versão
// selada mal-formada) propaga-se — nunca se inventa uma aprovação.
func (l *ApprovalLedger) LoadFromAudit(ctx context.Context, store audit.Store, partition string) error {
	if store == nil {
		return ErrNoAudit
	}
	if partition == "" {
		partition = DefaultPromotionPartition
	}
	head, err := store.Head(ctx, partition)
	if err != nil {
		return err
	}
	if head == 0 {
		return nil
	}
	recs, err := store.Read(ctx, partition, 1, head)
	if err != nil {
		return err
	}
	ratifiedCap := capPromotionPrefix + stageRatified
	for _, r := range recs {
		if r.Capability != ratifiedCap || r.Decision != audit.DecisionAllow {
			continue
		}
		v, perr := domain.ParseVersion(r.PolicyVersion)
		if perr != nil {
			return perr
		}
		l.Approve(r.ToolID, v, r.Resource.Value)
	}
	return nil
}

// RebuildApprovalLedger constrói um ApprovalLedger já povoado a partir da partição
// de audit WORM (ver [ApprovalLedger.LoadFromAudit]) — o construtor a usar no
// ARRANQUE para que o gate de governação seja durável através de reinícios. store
// nil devolve ErrNoAudit; partition vazia usa [DefaultPromotionPartition].
func RebuildApprovalLedger(ctx context.Context, store audit.Store, partition string) (*ApprovalLedger, error) {
	l := NewApprovalLedger()
	if err := l.LoadFromAudit(ctx, store, partition); err != nil {
		return nil, err
	}
	return l, nil
}

// CompositeVerifier implementa a porta registry.AdmissionVerifier — o GATE que o
// Registry atravessa em TODA a promoção a active (staging→active e reactivação
// deprecated→active). Compõe duas exigências:
//
//   - INTEGRIDADE (assinatura, AOS-048): delega no verificador injectado. Aplica-se
//     a TODOS os artefactos. Uma falha → recusa a promoção.
//   - APROVAÇÃO DE GOVERNAÇÃO (só skills auto-escritas): exige que o [ApprovalLedger]
//     tenha uma aprovação para (id, version, digest) — o que só acontece depois de o
//     Pipeline correr eval-gate + ratificação com sucesso. Assim, mesmo uma chamada
//     directa a Registry.SetStatus(active) que IGNORE o Pipeline NÃO consegue
//     promover uma skill auto-escrita: sem aprovação → ErrNotApproved. É o FECHO
//     ESTRUTURAL do "salto para active" para o caso de maior risco.
//
// Um artefacto de terceiros (tool/mcp_server/skill externa) só precisa da
// integridade — a distinção tools vs skills auto-escritas é imposta AQUI, no gate.
type CompositeVerifier struct {
	integrity  registry.AdmissionVerifier
	ledger     *ApprovalLedger
	classifier SelfAuthoredClassifier
}

// NewCompositeVerifier constrói o verificador composto. integrity e ledger são
// obrigatórios; um classifier nil usa [DefaultSelfAuthoredClassifier].
func NewCompositeVerifier(integrity registry.AdmissionVerifier, ledger *ApprovalLedger, classifier SelfAuthoredClassifier) *CompositeVerifier {
	if classifier == nil {
		classifier = DefaultSelfAuthoredClassifier
	}
	return &CompositeVerifier{integrity: integrity, ledger: ledger, classifier: classifier}
}

// Verify implementa registry.AdmissionVerifier. Fail-closed: integridade em falta/
// inválida recusa; uma skill auto-escrita sem aprovação de governação recusa com
// ErrNotApproved. A ordem — integridade primeiro — garante que a assinatura é
// sempre a primeira condição atravessada.
func (v *CompositeVerifier) Verify(ctx context.Context, entry domain.Entry) error {
	if v.integrity == nil || v.ledger == nil {
		return ErrNilIntegrity
	}
	if err := v.integrity.Verify(ctx, entry); err != nil {
		return err
	}
	if v.classifier(entry) && !v.ledger.IsApproved(entry.ID, entry.Version, entry.Digest) {
		return ErrNotApproved
	}
	return nil
}
