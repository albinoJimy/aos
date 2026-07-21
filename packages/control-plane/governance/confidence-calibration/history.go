package confcalib

import (
	"context"

	"github.com/aos-ref/substrate/redaction"
)

// redactionPolicyVersion identifica a política de minimização aplicada ao histórico
// apresentado. Minimização MÁXIMA (RemoveAllPolicy): o histórico de correcções é para
// CALIBRAR a decisão, não para recuperar o conteúdo — remover (nunca tokenizar) é o
// default mais seguro e não exige KeySource.
const redactionPolicyVersion = "confcalib-history-v1"

// CorrectionRecord é a projecção de UM evento control.steer (AOS-119) relevante para o
// contexto corrente: em que CONTEXTO ocorreu, que TIPO de correcção foi, e o TEXTO já
// minimizado. É o que o adaptador do WIRING produz ao ler os control.steer do Event
// Store — mas o core NÃO confia cegamente: re-redige defensivamente (ver
// [CorrectionHistory.SummaryFor]) para garantir AC5 mesmo se o adaptador falhar.
type CorrectionRecord struct {
	// Context é a chave de contexto semelhante (agente/capability/domínio) sob a qual a
	// correcção ocorreu. É o eixo de agrupamento do histórico.
	Context string
	// Kind é o tipo/categoria da correcção (rótulo controlado, ex. "steer",
	// "scope_narrowing", "factual"). NÃO é texto livre — não carrega PII.
	Kind string
	// RedactedText é o texto da correcção, minimizado pelo adaptador. O core re-redige-o
	// antes de o apresentar (belt-and-suspenders) — o histórico apresentado passa sempre
	// Engine.ScanText == [].
	RedactedText string
}

// CorrectionSource é a PORTA para a fonte do histórico de correcções (AOS-119): os
// eventos control.steer agrupáveis por contexto. O adaptador CONCRETO — que lê os
// control.steer do Event Store e os projecta em [CorrectionRecord] — vive no WIRING, não
// no core (que assim não arrasta o agent-runtime/control). Corrections devolve as
// correcções candidatas para contextKey; o core filtra por contexto semelhante e
// RE-REDIGE o texto antes de o apresentar.
type CorrectionSource interface {
	Corrections(ctx context.Context, contextKey string) ([]CorrectionRecord, error)
}

// CorrectionSourceFunc adapta uma função à porta [CorrectionSource].
type CorrectionSourceFunc func(ctx context.Context, contextKey string) ([]CorrectionRecord, error)

// Corrections implementa [CorrectionSource].
func (f CorrectionSourceFunc) Corrections(ctx context.Context, contextKey string) ([]CorrectionRecord, error) {
	return f(ctx, contextKey)
}

// CorrectionSummary é o histórico de correcções APRESENTADO para um contexto (AC2): a
// contagem total e a repartição por tipo, mais os registos JÁ REDIGIDOS. É um artefacto
// de apresentação passivo — informa a decisão humana. Garante AC5: nenhum RedactedText
// contém PII em claro (Engine.ScanText == [] sobre cada um).
type CorrectionSummary struct {
	// Context é a chave de contexto a que este sumário se refere.
	Context string
	// Count é o nº de correcções em contexto semelhante.
	Count int
	// ByKind é a repartição da contagem por tipo de correcção.
	ByKind map[string]int
	// Records são os registos redigidos, apresentáveis (sem PII). Ordem estável (a da
	// fonte, filtrada ao contexto).
	Records []CorrectionRecord
}

// CorrectionHistory projecta a fonte de correcções (a PORTA) num [CorrectionSummary] por
// contexto, GARANTINDO a redacção do texto apresentado. Compõe o [redaction.Engine] — não
// reimplementa a detecção de PII.
type CorrectionHistory struct {
	source CorrectionSource
	engine *redaction.Engine
	policy redaction.Policy
}

// NewCorrectionHistory constrói o histórico sobre a porta [CorrectionSource], com um
// [redaction.Engine] de minimização máxima (RemoveAllPolicy — remove todas as classes,
// sem KeySource). Um engine/política à medida podem ser injectados com
// [NewCorrectionHistoryWithEngine].
func NewCorrectionHistory(source CorrectionSource) *CorrectionHistory {
	return NewCorrectionHistoryWithEngine(source, redaction.NewEngine(nil), redaction.RemoveAllPolicy(redactionPolicyVersion))
}

// NewCorrectionHistoryWithEngine constrói o histórico com um [redaction.Engine] e uma
// [redaction.Policy] explícitos (ex. para testes ou uma política de minimização à
// medida). Um engine nil cai no default de minimização máxima.
func NewCorrectionHistoryWithEngine(source CorrectionSource, engine *redaction.Engine, policy redaction.Policy) *CorrectionHistory {
	if engine == nil {
		engine = redaction.NewEngine(nil)
		policy = redaction.RemoveAllPolicy(redactionPolicyVersion)
	}
	return &CorrectionHistory{source: source, engine: engine, policy: policy}
}

// redact minimiza um texto de correcção. Fail-closed: se a redacção falhar por qualquer
// razão, devolve um marcador opaco em vez do texto original — nunca vaza PII por erro.
func (h *CorrectionHistory) redact(text string) string {
	out, _, err := h.engine.RedactText(text, "", h.policy)
	if err != nil {
		return "[REDACTED]"
	}
	return out
}

// SummaryFor projecta as correcções relevantes para contextKey num [CorrectionSummary]
// (AC2): agrupa por CONTEXTO SEMELHANTE (agente/capability/domínio) e conta por tipo. As
// correcções de OUTRO contexto são EXCLUÍDAS (o core filtra por contexto exacto — o
// adaptador pode devolver mais do que o pedido). Cada RedactedText é RE-REDIGIDO
// (belt-and-suspenders) para garantir AC5: o histórico apresentado passa ScanText == [].
//
// É LEITURA PURA: não muta a fonte nem recalcula qualquer sinal de confiança.
func (h *CorrectionHistory) SummaryFor(ctx context.Context, contextKey string) (CorrectionSummary, error) {
	sum := CorrectionSummary{Context: contextKey, ByKind: map[string]int{}}
	if h.source == nil {
		return sum, nil
	}
	records, err := h.source.Corrections(ctx, contextKey)
	if err != nil {
		return CorrectionSummary{}, err
	}
	for _, r := range records {
		// Filtra por CONTEXTO SEMELHANTE: só as correcções do mesmo contexto contam.
		if r.Context != contextKey {
			continue
		}
		sum.Count++
		sum.ByKind[r.Kind]++
		sum.Records = append(sum.Records, CorrectionRecord{
			Context:      r.Context,
			Kind:         r.Kind,
			RedactedText: h.redact(r.RedactedText),
		})
	}
	return sum, nil
}
