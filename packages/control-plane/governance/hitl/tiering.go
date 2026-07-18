package hitl

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// Mode é o modo de fricção que o tiering SA-ROC (ADR-013) atribui a uma classe de
// risco. É o vocabulário policy-as-code do gate HITL: o valor-zero [ModeConfirm] é
// o mais restritivo (fail-closed — uma classe não mapeada exige confirmação
// individual, nunca corre livre).
type Mode uint8

const (
	// ModeConfirm é o valor-zero (fail-closed): confirmação INDIVIDUAL com preview e
	// dual-control 4-eyes. É o modo de danger/irreversível.
	ModeConfirm Mode = iota
	// ModeBatch — a acção agrupa-se numa confirmação de LOTE (anti-fatigue). É o modo
	// de gray. Não exige 4-eyes.
	ModeBatch
	// ModeRun — a acção corre SEM gate (sem HITL). É o modo de safe: se uma acção
	// safe chega ao canal é um erro de wiring e é negada fail-closed.
	ModeRun
)

// String devolve a forma textual canónica do modo (selada no audit / spans).
func (m Mode) String() string {
	switch m {
	case ModeRun:
		return "run"
	case ModeBatch:
		return "batch"
	default:
		return "confirm"
	}
}

// TieringRule é uma regra NOMEADA da política de tiering (policy-as-code): mapeia
// uma classe de risco a um modo de fricção. É declarativa e entra no digest da
// política (tamper-evident), documentando o eixo SA-ROC de forma versionada e
// testável.
type TieringRule struct {
	Class risk.Class `json:"class"`
	Mode  Mode       `json:"mode"`
	Desc  string     `json:"desc"`
}

// TieringPolicy é a POLÍTICA DE TIERING SA-ROC como código, versionada por um
// digest sha256 canónico (à imagem das allowlists/policies de AOS-058/074). O mapa
// classe→modo é a expressão policy-as-code do ADR-013: safe corre, gray agrupa em
// lote, danger confirma. Fail-closed: [ModeFor] devolve [ModeConfirm] para qualquer
// classe não explicitamente mapeada.
type TieringPolicy struct {
	// VersionTag é a etiqueta legível da política (ex.: "saroc-tiering-v1").
	VersionTag string
	// Rules é o mapa de decisão declarativo (uma regra por classe conhecida).
	Rules []TieringRule
}

// DefaultTieringPolicy devolve o tiering SA-ROC de referência (ADR-013): safe→run,
// gray→batch, danger→confirm. É a policy-as-code fail-closed que o [Channel] usa por
// omissão.
func DefaultTieringPolicy() TieringPolicy {
	return TieringPolicy{
		VersionTag: "saroc-tiering-v1",
		Rules: []TieringRule{
			{Class: risk.ClassSafe, Mode: ModeRun, Desc: "safe: local/reversivel/sem egress -> corre sem gate"},
			{Class: risk.ClassGray, Mode: ModeBatch, Desc: "gray: algum risco mas agrupavel -> confirmacao de lote"},
			{Class: risk.ClassDanger, Mode: ModeConfirm, Desc: "danger/irreversivel -> confirmacao individual + 4-eyes"},
		},
	}
}

// ModeFor devolve o modo de fricção para a classe. FAIL-CLOSED: uma classe não
// mapeada (ou uma política vazia) devolve [ModeConfirm] — o pior caso exige sempre
// confirmação individual, nunca corre sem gate por omissão de regra.
func (p TieringPolicy) ModeFor(class risk.Class) Mode {
	for _, r := range p.Rules {
		if r.Class == class {
			return r.Mode
		}
	}
	return ModeConfirm
}

// Digest devolve o digest sha256 canónico da política (12 bytes hex, à imagem de
// "tag#digest12"), tornando qualquer alteração ao mapa de tiering tamper-evident e
// detectável por teste de drift. O digest cobre a VersionTag e cada regra
// (classe+modo+descrição) em ordem length-prefixed determinista.
func (p TieringPolicy) Digest() string {
	buf := make([]byte, 0, 128)
	buf = putString(buf, "aos.hitl.tiering.v1")
	buf = putString(buf, p.VersionTag)
	buf = putUint64(buf, uint64(len(p.Rules)))
	for _, r := range p.Rules {
		buf = append(buf, byte(r.Class))
		buf = append(buf, byte(r.Mode))
		buf = putString(buf, r.Desc)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:12])
}

// Version devolve o identificador tamper-evident "tag#digest12" da política, selado
// no audit como PolicyVersion da decisão HITL.
func (p TieringPolicy) Version() string {
	return p.VersionTag + "#" + p.Digest()
}
