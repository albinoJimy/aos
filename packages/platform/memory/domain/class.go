package domain

// MemoryClass identifica uma das QUATRO classes de memória do AOS. Cada classe é
// uma abstracção DISTINTA — identidade própria, esquema tipado (ver o Body
// respectivo) e ciclo de vida documentado (ver doc do pacote). O modelo de
// domínio NÃO colapsa as quatro num único store: a classe faz parte da identidade
// de um registo e escopa toda a operação de porta (CRUD/query por classe).
type MemoryClass string

const (
	// ClassEpisodic — memória episódica: trajectórias de execução passadas (o que
	// o agente fez, em que sequência, com que resultado). Matéria-prima de replay,
	// RCA e eval. Ciclo de vida: append-only, nunca descartada do registo; TTL
	// tipicamente longo/permanente.
	ClassEpisodic MemoryClass = "episodic"

	// ClassSemantic — memória semântica: base de conhecimento factual (factos,
	// entidades, relações). Deriva de episódios curados; superfície do memory
	// poisoning (ASI06), pelo que a proveniência é decisiva (AOS-042). Ciclo de
	// vida: consolidada por curadoria, TTL/redação por política.
	ClassSemantic MemoryClass = "semantic"

	// ClassProcedural — memória procedural: skills e heurísticas aprendidas
	// (auto-escritas). A mudança de maior risco do sistema; versionada SemVer e
	// sujeita a eval-gate + ratificação antes de produção (ADR-012, AOS-040 — não
	// implementado aqui). Ciclo de vida: staging → produção → rollback.
	ClassProcedural MemoryClass = "procedural"

	// ClassWorking — memória de trabalho: contexto activo do turno (janela que o
	// modelo vê). Materializável por replay do log; ciclo de vida curto/efémero,
	// gerido pela janela de contexto (AOS-037 — não implementado aqui).
	ClassWorking MemoryClass = "working"
)

// Valid indica se c é uma das quatro classes canónicas. Fail-closed: uma classe
// desconhecida nunca é aceite numa escrita.
func (c MemoryClass) Valid() bool {
	switch c {
	case ClassEpisodic, ClassSemantic, ClassProcedural, ClassWorking:
		return true
	default:
		return false
	}
}

// String implementa fmt.Stringer.
func (c MemoryClass) String() string { return string(c) }

// AllClasses devolve as quatro classes canónicas por ordem estável. Útil para
// varreduras determinísticas (ex.: contract tests table-driven).
func AllClasses() []MemoryClass {
	return []MemoryClass{ClassEpisodic, ClassSemantic, ClassProcedural, ClassWorking}
}
