package domain

// Body é o esquema TIPADO do conteúdo de um registo, específico da sua classe.
// Cada uma das quatro classes tem exactamente um tipo de Body — é isto que torna
// as classes abstracções distintas e não um saco de campos partilhado. Class()
// devolve a classe a que o corpo pertence, o que permite ao domínio rejeitar um
// corpo colocado na classe errada (ErrClassMismatch).
//
// clone devolve uma cópia independente do corpo, para que registos guardados
// nunca partilhem estado mutável com o chamador (os corpos são structs de valor,
// pelo que a cópia é trivial, mas o método mantém a fronteira explícita e
// preparada para corpos futuros com slices/maps).
type Body interface {
	Class() MemoryClass
	clone() Body
}

// EpisodicBody — esquema da memória episódica: uma trajectória de execução
// passada, ligada ao trace por TraceID, com o desfecho resumido. A árvore de
// spans completa vive no backend de observabilidade (EPIC-08); aqui guarda-se a
// referência recuperável e o resumo (Princípio 4: contexto ≠ registo).
type EpisodicBody struct {
	// TraceID liga a trajectória à árvore de spans OTel (run_id/trace_id).
	TraceID string `json:"trace_id"`
	// Goal é o objectivo do episódio (indexável por AOS-038).
	Goal string `json:"goal"`
	// Outcome é o desfecho (ex.: "success", "failed", "escalated").
	Outcome string `json:"outcome"`
	// StepCount é o número de passos da trajectória.
	StepCount int `json:"step_count"`
	// Summary é o resumo recuperável (a projecção, não a trajectória crua).
	Summary string `json:"summary"`
}

// Class implementa Body.
func (EpisodicBody) Class() MemoryClass { return ClassEpisodic }
func (b EpisodicBody) clone() Body      { return b }

// SemanticBody — esquema da memória semântica: um facto/relação da base de
// conhecimento, com grau de confiança. A proveniência (trusted/untrusted) vive
// nos Metadata do registo, não aqui.
type SemanticBody struct {
	// Subject/Predicate/Object formam a asserção factual.
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
	// Confidence é o grau de confiança [0,1] atribuído à asserção.
	Confidence float64 `json:"confidence"`
}

// Class implementa Body.
func (SemanticBody) Class() MemoryClass { return ClassSemantic }
func (b SemanticBody) clone() Body      { return b }

// ProceduralBody — esquema da memória procedural: uma skill/heurística aprendida
// como artefacto comportamental versionado. O pipeline de promoção
// (staging→eval-gate→canary→ratificação) é AOS-040; aqui guarda-se o manifesto.
type ProceduralBody struct {
	// SkillName identifica a skill aprendida.
	SkillName string `json:"skill_name"`
	// Version é a versão SemVer do artefacto comportamental.
	Version string `json:"version"`
	// DefinitionHash é o hash do corpo da skill (pin de integridade).
	DefinitionHash string `json:"definition_hash"`
	// Stage é o estado de promoção documentado (ex.: "staging", "canary",
	// "production"). A aplicação/allowlist é AOS-040 (fora de âmbito aqui).
	Stage string `json:"stage"`
}

// Class implementa Body.
func (ProceduralBody) Class() MemoryClass { return ClassProcedural }
func (b ProceduralBody) clone() Body      { return b }

// WorkingBody — esquema da memória de trabalho: um fragmento do contexto activo
// do turno. A contabilidade de tokens e a gestão da janela (prefixo imutável +
// tail append-only) são AOS-037; aqui guarda-se o fragmento tipado e o custo.
type WorkingBody struct {
	// TurnIndex é o índice do turno no run.
	TurnIndex int `json:"turn_index"`
	// Content é o fragmento de contexto.
	Content string `json:"content"`
	// TokenCount é a ocupação em tokens do fragmento.
	TokenCount int `json:"token_count"`
}

// Class implementa Body.
func (WorkingBody) Class() MemoryClass { return ClassWorking }
func (b WorkingBody) clone() Body      { return b }
