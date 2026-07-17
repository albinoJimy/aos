package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// Sensitivity é o eixo de SENSIBILIDADE dos dados que a acção manuseia. O
// reticulado é ordenado (Public < Internal < Sensitive) e FAIL-CLOSED pelo tipo:
// o valor-zero [SensitivityUnknown] classifica como o TOPO (Sensitive) — dados de
// sensibilidade indeterminada tratam-se como sensíveis, nunca como públicos.
type Sensitivity uint8

const (
	// SensitivityUnknown é o valor-zero: sensibilidade indeterminada. Fail-closed —
	// [Sensitivity.Level] resolve-o para o topo (sensível).
	SensitivityUnknown Sensitivity = iota
	// SensitivityPublic — dados públicos, sem risco de exfiltração.
	SensitivityPublic
	// SensitivityInternal — dados internos/confidenciais de baixo impacto.
	SensitivityInternal
	// SensitivitySensitive — PII, segredos, dados regulados. O topo do reticulado.
	SensitivitySensitive
)

// Level devolve a POSIÇÃO ordinal no reticulado (0 = público … 2 = sensível),
// resolvendo o valor-zero desconhecido para o topo (fail-closed).
func (s Sensitivity) Level() int {
	switch s {
	case SensitivityPublic:
		return 0
	case SensitivityInternal:
		return 1
	case SensitivitySensitive:
		return 2
	default:
		// Desconhecido/forjado ⇒ topo (sensível). Fail-closed.
		return 2
	}
}

// String devolve a forma textual canónica (fail-closed: desconhecido serializa
// "sensitive", nunca uma forma que sugira baixo risco).
func (s Sensitivity) String() string {
	switch s {
	case SensitivityPublic:
		return "public"
	case SensitivityInternal:
		return "internal"
	default:
		return "sensitive"
	}
}

// raise eleva a sensibilidade UM nível no reticulado (satura no topo). É o efeito
// do taint untrusted: conteúdo não-confiável eleva a sensibilidade/risco.
func (s Sensitivity) raise() Sensitivity {
	switch s.Level() {
	case 0:
		return SensitivityInternal
	default:
		return SensitivitySensitive
	}
}

// Egress é o eixo de EGRESS: a acção envia dados PARA FORA? O reticulado é
// ordenado (None < Internal < External) e FAIL-CLOSED: o valor-zero
// [EgressUnknown] classifica como External (o pior caso de exfiltração).
type Egress uint8

const (
	// EgressUnknown é o valor-zero: destino indeterminado. Fail-closed — trata-se
	// como egress externo.
	EgressUnknown Egress = iota
	// EgressNone — a acção não fala para fora (leitura/escrita local).
	EgressNone
	// EgressInternal — egress para um destino interno/confiável.
	EgressInternal
	// EgressExternal — egress para um destino externo (vector de exfiltração).
	EgressExternal
)

// IsExternal indica se o egress atinge o exterior (fail-closed: desconhecido conta
// como externo). Exportada para que o RiskGate (pacote ápice) decida, sem duplicar
// a regra, se uma acção danger com egress carece de destino concreto (ver SAROC-04).
func (e Egress) IsExternal() bool {
	return e == EgressExternal || e == EgressUnknown
}

// isExternal é o alias interno usado por [Classify].
func (e Egress) isExternal() bool { return e.IsExternal() }

// String devolve a forma textual canónica (fail-closed: desconhecido → "external").
func (e Egress) String() string {
	switch e {
	case EgressNone:
		return "none"
	case EgressInternal:
		return "internal"
	default:
		return "external"
	}
}

// Reversibility é o eixo de REVERSIBILIDADE: a acção pode ser desfeita? FAIL-CLOSED
// pelo tipo — o valor-zero [ReversibilityUnknown] classifica como IRREVERSÍVEL (o
// pior caso: uma acção de reversibilidade indeterminada não pode ser tratada como
// desfazível).
type Reversibility uint8

const (
	// ReversibilityUnknown é o valor-zero: indeterminada. Fail-closed — irreversível.
	ReversibilityUnknown Reversibility = iota
	// Reversible — a acção pode ser desfeita (ex.: uma leitura, uma escrita com undo).
	Reversible
	// Irreversible — a acção NÃO pode ser desfeita (ex.: delete, send, transferência).
	Irreversible
)

// IsIrreversible indica se a acção é irreversível (fail-closed: qualquer valor que
// não seja explicitamente [Reversible] conta como irreversível).
func (r Reversibility) IsIrreversible() bool {
	return r != Reversible
}

// String devolve a forma textual canónica (fail-closed: não-reversível →
// "irreversible").
func (r Reversibility) String() string {
	if r == Reversible {
		return "reversible"
	}
	return "irreversible"
}

// Class é a CLASSE DE RISCO resultante da classificação — o eixo composto que o
// gate SA-ROC (ADR-013) usa para calibrar a fricção. É ordenada (Safe < Gray <
// Danger); o valor-zero é [ClassDanger] (fail-closed: uma classe não computada é
// tratada como o pior caso).
type Class uint8

const (
	// ClassDanger é o valor-zero (fail-closed): egress externo de dados sensíveis
	// OU acção irreversível. Escala para confirmação individual com preview.
	ClassDanger Class = iota
	// ClassSafe — local, reversível, sem egress, baixa sensibilidade: corre sem gate.
	ClassSafe
	// ClassGray — algum risco mas agrupável: agrega-se em lote com resumo.
	ClassGray
)

// String devolve a forma textual canónica da classe (selada no audit).
func (c Class) String() string {
	switch c {
	case ClassSafe:
		return "safe"
	case ClassGray:
		return "gray"
	default:
		return "danger"
	}
}

// Action é a acção proposta a classificar, projectada nos três eixos de risco. O
// RiskGate (pacote ápice) constrói-a a partir do [referencemonitor.Call]: a
// sensibilidade do [CallContext], o egress-class derivado da capability/recurso
// (sem importar o sandbox) e a reversibilidade do [CallContext]. O sinal de taint
// (AOS-069) entra em [Action.Taint] e ELEVA a sensibilidade efectiva.
type Action struct {
	Sensitivity   Sensitivity
	Egress        Egress
	Reversibility Reversibility
	// Taint é o rótulo de confiança da AUTORIZAÇÃO (AOS-069). Untrusted eleva a
	// sensibilidade efectiva um nível (conteúdo não-confiável aumenta o risco). O
	// valor-zero é untrusted (fail-closed, ver taint.Label).
	Taint taint.Label
}

// effectiveSensitivity aplica o efeito do taint: um pedido cuja autorização é
// untrusted vê a sua sensibilidade ELEVADA um nível no reticulado (satura no
// topo). É o ponto onde a barreira control/data-plane (AOS-069) alimenta o eixo de
// sensibilidade do risco.
func (a Action) effectiveSensitivity() Sensitivity {
	if a.Taint.IsUntrusted() {
		return a.Sensitivity.raise()
	}
	return a.Sensitivity
}

// Classification é o veredicto do classificador: a classe mais os eixos efectivos
// (após aplicação do taint) e a versão da política em vigor. É selada no audit
// para que a decisão seja explicável e reproduzível.
type Classification struct {
	Class         Class
	Sensitivity   Sensitivity // efectiva (após taint)
	Egress        Egress
	Reversibility Reversibility
	// PolicyVersion é a versão tamper-evident da política de classificação
	// ("tag#digest12"), selada no audit.
	PolicyVersion string
	// Rationale descreve, sem segredos, porque a classe foi atribuída.
	Rationale string
}

// Irreversible é um atalho: a acção classificada é irreversível (o gate aplica-lhe
// o timeout fail-closed).
func (c Classification) Irreversible() bool { return c.Reversibility.IsIrreversible() }

// Rule é uma regra NOMEADA da política de classificação (policy-as-code): descreve
// uma condição composta que atribui uma classe. As regras são DECLARATIVAS e
// entram no digest da política (tamper-evident), documentando o mapa de decisão do
// ADR-013 de forma versionada e testável.
type Rule struct {
	ID   string `json:"id"`
	Says Class  `json:"class"`
	Desc string `json:"desc"`
}

// Policy é a POLÍTICA DE CLASSIFICAÇÃO como código, VERSIONADA por um digest
// sha256 canónico (à imagem das allowlists de AOS-067/058). O mapa de decisão do
// ADR-013 é fixo (a função [Classify] é a autoridade); as [Rule] documentam-no de
// forma declarativa e entram no digest, tornando qualquer alteração ao contrato
// tamper-evident e detectável por teste de drift.
type Policy struct {
	// VersionTag é a etiqueta legível da política (ex.: "saroc-v1").
	VersionTag string
	// Rules documenta o mapa de decisão (entra no digest). Não é avaliada em runtime
	// — [Classify] é a autoridade — mas fixa o contrato versionado.
	Rules []Rule

	digest string // sha256 hex do conteúdo canónico (calculado uma vez)
}

// DefaultPolicyTag é a etiqueta da política de classificação de referência.
const DefaultPolicyTag = "saroc-v1"

// NewPolicy constrói uma política de classificação com a etiqueta e as regras
// dadas, calculando o digest tamper-evident uma vez. É imutável e segura para uso
// concorrente. As [Rule] documentam o contrato do ADR-013 de forma versionada
// (entram no digest); a autoridade de runtime é [Classify].
func NewPolicy(tag string, rules []Rule) *Policy {
	p := &Policy{
		VersionTag: tag,
		Rules:      append([]Rule(nil), rules...),
	}
	p.digest = p.canonicalDigest()
	return p
}

// DefaultPolicy devolve a política de classificação de referência do ADR-013, com
// o digest já calculado. É imutável e segura para uso concorrente.
func DefaultPolicy() *Policy {
	return NewPolicy(DefaultPolicyTag, []Rule{
		{ID: "safe", Says: ClassSafe, Desc: "local, reversivel, sem egress, sensibilidade <= interna"},
		{ID: "danger-irreversible", Says: ClassDanger, Desc: "accao irreversivel"},
		{ID: "danger-exfil", Says: ClassDanger, Desc: "egress externo de dados sensiveis"},
		{ID: "gray-default", Says: ClassGray, Desc: "algum risco mas agrupavel (residual)"},
	})
}

// Version devolve a versão tamper-evident da política ("tag#digest12"): a etiqueta
// legível mais os 12 primeiros hex do digest sha256 canónico. É o valor selado no
// audit em cada classificação, ligando a decisão à versão EXACTA da política.
func (p *Policy) Version() string {
	return p.VersionTag + "#" + p.digest[:12]
}

// Hash devolve o digest sha256 COMPLETO (hex) do conteúdo canónico da política.
func (p *Policy) Hash() string { return p.digest }

// canonicalDigest calcula um sha256 determinista do CONTEÚDO da política (etiqueta
// + regras ordenadas por id), independente da ordem de declaração. Stdlib apenas.
func (p *Policy) canonicalDigest() string {
	type canonRule struct {
		ID    string `json:"id"`
		Class uint8  `json:"class"`
		Desc  string `json:"desc"`
	}
	rules := make([]canonRule, 0, len(p.Rules))
	for _, r := range p.Rules {
		rules = append(rules, canonRule{ID: r.ID, Class: uint8(r.Says), Desc: r.Desc})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	canon := struct {
		Version string      `json:"version"`
		Rules   []canonRule `json:"rules"`
	}{Version: p.VersionTag, Rules: rules}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Classify é a POLÍTICA DE CLASSIFICAÇÃO (policy-as-code do ADR-013): mapeia a
// [Action] numa [Class] combinando os três eixos, com o taint a ELEVAR a
// sensibilidade. É PURA e DETERMINISTA (sem relógio/rand): função apenas dos eixos.
//
// Mapa de decisão (fail-closed — cada eixo desconhecido resolve para o pior caso):
//
//   - DANGER se a acção é IRREVERSÍVEL (não pode ser desfeita), OU se há EGRESS
//     EXTERNO de dados SENSÍVEIS (o vector CamoLeak de exfiltração).
//   - SAFE se é local (sem egress), reversível e de sensibilidade <= interna.
//   - GRAY caso contrário (risco residual agrupável): ex. egress interno, ou egress
//     externo de dados não-sensíveis, ou dados sensíveis sem egress mas reversíveis.
//
// Um p nil usa a [DefaultPolicy] (nunca fail-open: a ausência de política não abre
// exceção, apenas cai no contrato de referência).
func Classify(p *Policy, a Action) Classification {
	if p == nil {
		p = DefaultPolicy()
	}
	sens := a.effectiveSensitivity()
	rev := a.Reversibility
	eg := a.Egress

	out := Classification{
		Sensitivity:   sens,
		Egress:        eg,
		Reversibility: rev,
		PolicyVersion: p.Version(),
	}

	switch {
	case rev.IsIrreversible():
		out.Class = ClassDanger
		out.Rationale = "accao irreversivel (timeout fail-closed)"
	case eg.isExternal() && sens.Level() >= SensitivitySensitive.Level():
		out.Class = ClassDanger
		out.Rationale = "egress externo de dados sensiveis (exfiltracao)"
	case eg == EgressNone && sens.Level() <= SensitivityInternal.Level():
		out.Class = ClassSafe
		out.Rationale = "local, reversivel, sem egress, sensibilidade <= interna"
	default:
		out.Class = ClassGray
		out.Rationale = "risco residual agrupavel"
	}
	return out
}
