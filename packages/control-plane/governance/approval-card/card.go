package approvalcard

import (
	"encoding/json"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/redaction"
)

// CostEstimate é o custo ESTIMADO da acção apresentado no card (opcional). É um
// enriquecimento do modelo de APRESENTAÇÃO — o [risk.ConfirmationRequest] do kernel
// não tem campo de custo; o wiring popula-o a partir do scheduler
// (admission.EstimatedTokens, AOS-027) e da agregação otel-genai (micro-USD, AOS-078).
// O card só o EXIBE; não o calcula.
type CostEstimate struct {
	// EstimatedTokens é o custo previsto em tokens (fonte: scheduler/admission).
	EstimatedTokens int64 `json:"estimated_tokens"`
	// MicroUSD é o custo previsto em micro-USD inteiro (fonte: otel-genai cost_aggregation).
	MicroUSD int64 `json:"micro_usd"`
}

// ApprovalCard é o MODELO CANÓNICO, serializável e versionado do approval-card
// (AC1/AC2/AC5). Resolve o EFEITO CONCRETO de uma acção escalada para que a supervisão
// humana escrute o que REALMENTE vai acontecer (não o template da tool). É
// independente de plataforma — os adaptadores de AOS-122 renderizam-no.
//
// Todos os campos de risco são LIDOS do [risk.ConfirmationRequest] (AC2): o card NÃO
// chama [risk.Classify] nem reclassifica. O Preview é o efeito resolvido JÁ redigido
// (AC6): nunca o Call.Input (o segredo), nunca PII em claro.
type ApprovalCard struct {
	// SchemaVersion é a versão SemVer do contrato deste card (AC5), carimbada no wire.
	SchemaVersion CardSchemaVersion
	// RequestID identifica unicamente esta apresentação (atribuído pela camada de
	// apresentação; independente do request-id por-aprovação que o [hitl.Channel] gera).
	RequestID string
	// Requester é o principal cuja acção está a ser gatada — a base do 4-eyes do
	// [hitl.Channel]. LIDO de [risk.ConfirmationRequest.Principal].
	Requester string
	// Class é a classe de risco LIDA do gate (AOS-074). O card não a calcula.
	Class risk.Class
	// Irreversible marca uma acção não-desfazível. LIDO do gate; motiva o dual-control.
	Irreversible bool
	// Reversibility é o eixo de reversibilidade exibido. Derivado de Irreversible por
	// omissão (o [risk.ConfirmationRequest] só carrega o bool), ou fornecido via
	// [WithReversibility] quando um enriquecimento mais fino existe. Fail-closed:
	// qualquer coisa != Reversible conta como irreversível ([risk.Reversibility]).
	Reversibility risk.Reversibility
	// Preview é o efeito CONCRETO RESOLVIDO (capability + recurso + args resolvidos),
	// JÁ redigido (AC1/AC6). Nunca o Call.Input, nunca PII em claro.
	Preview string
	// Capability e Resource identificam a acção resolvida (LIDOS do gate).
	Capability string
	Resource   string
	// Batch indica um card de LOTE gray (uma apresentação para o grupo).
	Batch bool
	// DualControlRequired indica que a autorização EXIGE dois aprovadores DISTINTOS
	// (AC3). É verdadeiro sse e só se a acção é irreversível.
	DualControlRequired bool
	// EstimatedCost é o custo estimado, opcional (enriquecimento de apresentação).
	EstimatedCost *CostEstimate
}

// buildConfig acumula as opções de [BuildCard].
type buildConfig struct {
	requestID        string
	engine           *redaction.Engine
	subject          string
	policy           redaction.Policy
	redact           bool
	cost             *CostEstimate
	reversibility    risk.Reversibility
	hasReversibility bool
}

// BuildOption configura [BuildCard].
type BuildOption func(*buildConfig)

// WithRequestID atribui o identificador de apresentação do card. Obrigatório: um card
// sem RequestID é recusado por [ApprovalCard.Validate] (fail-closed).
func WithRequestID(id string) BuildOption {
	return func(c *buildConfig) { c.requestID = id }
}

// WithRedaction liga o gate de ausência de PII (AC6): o [redaction.Engine] é aplicado
// ao Preview e ao Resource ANTES de entrarem no card. Recomenda-se a
// [redaction.RemoveAllPolicy] (minimização máxima, sem KeySource) para o preview. Sem
// esta opção, o card confia que o Preview recebido já vem sem segredos (o kernel nunca
// põe o Call.Input no preview), mas a aplicação explícita torna o critério provável
// por [redaction.Engine.Scan] == [].
func WithRedaction(engine *redaction.Engine, subject string, policy redaction.Policy) BuildOption {
	return func(c *buildConfig) {
		c.engine = engine
		c.subject = subject
		c.policy = policy
		c.redact = engine != nil
	}
}

// WithEstimatedCost enriquece o card com o custo estimado (tokens + micro-USD).
func WithEstimatedCost(tokens, microUSD int64) BuildOption {
	return func(c *buildConfig) { c.cost = &CostEstimate{EstimatedTokens: tokens, MicroUSD: microUSD} }
}

// WithReversibility fixa o eixo de reversibilidade exibido (quando um enriquecimento
// mais fino que o bool Irreversible existe). É um valor LIDO da classificação, nunca
// recalculado pelo card.
func WithReversibility(r risk.Reversibility) BuildOption {
	return func(c *buildConfig) {
		c.reversibility = r
		c.hasReversibility = true
	}
}

// BuildCard constrói o modelo canónico A PARTIR de uma [risk.ConfirmationRequest],
// LENDO Class/Irreversible/Preview/Capability/Resource/Principal — NÃO reclassifica
// (AC1/AC2). O Preview/Resource são redigidos pelo [redaction.Engine] quando
// [WithRedaction] é dado (AC6). O card resultante é VALIDADO (fail-closed): um
// RequestID vazio, uma versão incompatível ou uma incoerência abortam a construção.
func BuildCard(req risk.ConfirmationRequest, opts ...BuildOption) (ApprovalCard, error) {
	cfg := buildConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	preview := req.Preview
	resource := req.Resource
	if cfg.redact {
		var err error
		if preview, _, err = cfg.engine.RedactText(preview, cfg.subject, cfg.policy); err != nil {
			return ApprovalCard{}, ErrRedaction
		}
		if resource, _, err = cfg.engine.RedactText(resource, cfg.subject, cfg.policy); err != nil {
			return ApprovalCard{}, ErrRedaction
		}
	}

	// Reversibilidade: por omissão derivada do bool Irreversible que o gate carrega
	// (fail-closed — Irreversible ⇒ risk.Irreversible). Um enriquecimento explícito só
	// pode ser um valor LIDO da classificação, não um recálculo.
	rev := risk.Reversible
	if req.Irreversible {
		rev = risk.Irreversible
	}
	if cfg.hasReversibility {
		rev = cfg.reversibility
	}

	card := ApprovalCard{
		SchemaVersion:       CurrentVersion,
		RequestID:           cfg.requestID,
		Requester:           req.Principal,
		Class:               req.Class,
		Irreversible:        req.Irreversible,
		Reversibility:       rev,
		Preview:             preview,
		Capability:          req.Capability,
		Resource:            resource,
		Batch:               req.Batch,
		DualControlRequired: req.Irreversible,
		EstimatedCost:       cfg.cost,
	}
	if err := card.Validate(); err != nil {
		return ApprovalCard{}, err
	}
	return card, nil
}

// Validate impõe as invariantes fail-closed do card (AC5):
//   - versão de schema compatível com [CurrentVersion] (MESMO MAJOR), senão
//     [ErrIncompatibleSchema];
//   - RequestID e Requester não-vazios, senão [ErrInvalidCard];
//   - coerência do dual-control: se exigido, a acção TEM de ser irreversível (o card
//     não pode pedir dois aprovadores para uma acção reversível — seria fricção
//     injustificada — nem omitir o dual-control numa irreversível).
//   - coerência do rótulo de reversibilidade: uma acção irreversível NÃO pode exibir
//     [risk.Reversible] no eixo apresentado — o rótulo cosmético não pode contradizer o
//     bool que motiva o dual-control (senão a supervisão veria "reversible" numa acção
//     que exige dois aprovadores, minando a fidelidade do preview). Fail-closed.
func (c ApprovalCard) Validate() error {
	if !CurrentVersion.Compatible(c.SchemaVersion) {
		return ErrIncompatibleSchema
	}
	if c.RequestID == "" || c.Requester == "" {
		return ErrInvalidCard
	}
	if c.DualControlRequired != c.Irreversible {
		return ErrInvalidCard
	}
	if c.Irreversible && c.Reversibility == risk.Reversible {
		return ErrInvalidCard
	}
	return nil
}

// confirmationRequest reconstrói a [risk.ConfirmationRequest] que o card DEVOLVE ao
// [risk.ConfirmationChannel] (o [hitl.Channel] de AOS-095). É a projecção inversa de
// [BuildCard]: o Preview já redigido, o Requester como Principal (base do 4-eyes). O
// card não decide nem assina — só transporta o pedido de volta à porta que o faz.
func (c ApprovalCard) confirmationRequest() risk.ConfirmationRequest {
	return risk.ConfirmationRequest{
		Class:        c.Class,
		Batch:        c.Batch,
		Irreversible: c.Irreversible,
		Preview:      c.Preview,
		Principal:    c.Requester,
		Capability:   c.Capability,
		Resource:     c.Resource,
	}
}

// cardWire é a forma serializada ESTÁVEL do card: schema_version carimbado, classe/
// reversibilidade como rótulos textuais canónicos (independentes da representação
// numérica interna). É o contrato de wire que os adaptadores de AOS-122 consomem.
type cardWire struct {
	SchemaVersion       string        `json:"schema_version"`
	RequestID           string        `json:"request_id"`
	Requester           string        `json:"requester"`
	Class               string        `json:"class"`
	Irreversible        bool          `json:"irreversible"`
	Reversibility       string        `json:"reversibility"`
	Preview             string        `json:"preview"`
	Capability          string        `json:"capability"`
	Resource            string        `json:"resource"`
	Batch               bool          `json:"batch"`
	DualControlRequired bool          `json:"dual_control_required"`
	EstimatedCost       *CostEstimate `json:"estimated_cost,omitempty"`
}

// MarshalJSON serializa o card na forma de wire estável, carimbando a schema_version
// e os rótulos canónicos de classe/reversibilidade.
func (c ApprovalCard) MarshalJSON() ([]byte, error) {
	return json.Marshal(cardWire{
		SchemaVersion:       c.SchemaVersion.String(),
		RequestID:           c.RequestID,
		Requester:           c.Requester,
		Class:               c.Class.String(),
		Irreversible:        c.Irreversible,
		Reversibility:       c.Reversibility.String(),
		Preview:             c.Preview,
		Capability:          c.Capability,
		Resource:            c.Resource,
		Batch:               c.Batch,
		DualControlRequired: c.DualControlRequired,
		EstimatedCost:       c.EstimatedCost,
	})
}

// UnmarshalJSON desserializa a forma de wire e VALIDA fail-closed: uma versão
// malformada ou um MAJOR incompatível é rejeitado (nunca um card silenciosamente
// aceite). Os rótulos de classe/reversibilidade são reparseados para a representação
// interna.
func (c *ApprovalCard) UnmarshalJSON(data []byte) error {
	var w cardWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	ver, err := ParseCardSchemaVersion(w.SchemaVersion)
	if err != nil {
		return err
	}
	out := ApprovalCard{
		SchemaVersion:       ver,
		RequestID:           w.RequestID,
		Requester:           w.Requester,
		Class:               parseClass(w.Class),
		Irreversible:        w.Irreversible,
		Reversibility:       parseReversibility(w.Reversibility),
		Preview:             w.Preview,
		Capability:          w.Capability,
		Resource:            w.Resource,
		Batch:               w.Batch,
		DualControlRequired: w.DualControlRequired,
		EstimatedCost:       w.EstimatedCost,
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*c = out
	return nil
}

// parseClass reparseia o rótulo textual de classe na representação interna. Fail-closed:
// um rótulo desconhecido resolve [risk.ClassDanger] (o valor-zero, o pior caso),
// espelhando a semântica de [risk.Class].
func parseClass(s string) risk.Class {
	switch s {
	case "safe":
		return risk.ClassSafe
	case "gray":
		return risk.ClassGray
	default:
		return risk.ClassDanger
	}
}

// parseReversibility reparseia o rótulo textual de reversibilidade. Fail-closed: só
// "reversible" resolve [risk.Reversible]; tudo o resto resolve [risk.Irreversible] (o
// pior caso), espelhando [risk.Reversibility].
func parseReversibility(s string) risk.Reversibility {
	if s == "reversible" {
		return risk.Reversible
	}
	return risk.Irreversible
}
