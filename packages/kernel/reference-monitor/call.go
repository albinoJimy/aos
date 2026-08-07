package referencemonitor

import (
	"hash/fnv"
)

// DelegationHop é um elo da cadeia de delegação on-behalf-of do principal: o
// sujeito (Sub) age como (ActAs) a identidade seguinte. Espelha o modelo do
// Event Store (eventstore.DelegationHop) e termina sempre num humano
// responsável (ADR-003).
type DelegationHop struct {
	Sub   string
	ActAs string
}

// Principal é a identidade não-humana (NHI) que origina a tool call, a sua
// cadeia de delegação e a autoridade (capabilities) delegada. O RM resolve e
// valida o principal no hook de identidade (AOS-005); aqui o stub é neutro.
type Principal struct {
	// NHIID é o identificador estável da identidade não-humana.
	NHIID string
	// AgentID é o identificador do agente no run corrente.
	AgentID string
	// AgentClass é a classe da NHI (claim `agent_class` do token, AOS-005),
	// resolvida pelo hook de identidade. É a chave da allowlist de capabilities
	// que o PDP avalia no gate default-deny (AOS-007).
	AgentClass string
	// Board é o board de governação a que a NHI pertence — a CHAVE da soberania por
	// board (AOS-094). Como AgentClass, é resolvido pelo hook de identidade (AOS-005)
	// a partir do token NHI VERIFICADO; nunca deve ser confiado do Call bruto do
	// caller (é forjável). Quando um registo board→região está ligado no PDP
	// (pdp.WithBoardRegions), o PDP resolve o board para a sua região autorizada e
	// emite-a como obrigação `region` que este PEP impõe (enforceRegion). Vazio ⇒ sem
	// board no escopo; um board desconhecido ao registo GOV é negado fail-closed
	// (nunca cross-border por omissão — ADR-011). Não entra no fingerprint do call
	// (o NHIID já liga o Permit à identidade; o board é metadado de governação).
	Board string
	// DelegationChain é a cadeia on-behalf-of (raiz humana → agente actual).
	DelegationChain []DelegationHop
	// Authority são as capabilities que o principal pode exercer (allowlist).
	Authority []string
	// SubjectAuthority é a autoridade-fonte VERIFICADA por SUJEITO da cadeia (raiz
	// humana, cada agente, "agent:<classe>"), resolvida pelo hook de identidade
	// (AOS-005) a partir do token NHI ASSINADO. É a autoridade de escopo derivada da
	// IDENTIDADE (AOS-156): o [ScopeGate] (AOS-071) resolve cada sujeito a partir
	// daqui — a ÚNICA fonte que conhece a autoridade do agente POR-MINT (dinâmica,
	// impossível num directório estático) — intersectando com uma [authz.AuthoritySource]
	// externa quando configurada (defesa-em-profundidade: o directório pode restringir
	// mais, nunca ampliar). nil ⇒ o gate cai só na fonte estática (retro-compatível).
	SubjectAuthority map[string][]string
}

// Resource é o alvo concreto da tool call (contrato C1, tecnica/12 §4).
type Resource struct {
	Type   string // ex.: "url", "file", "db"
	Value  string // ex.: "https://api.example.com/orders"
	Region string // ex.: "eu" (soberania de dados)
}

// CallContext transporta o contexto de decisão que a política avalia: taint,
// orçamento disponível, reversibilidade e sensibilidade (contrato C1).
type CallContext struct {
	// Taint marca conteúdo untrusted que não pode autorizar acções
	// privilegiadas (ADR-005). Ex.: "trusted", "untrusted".
	Taint string
	// BudgetTokensRemaining é o headroom de orçamento por árvore (ADR-008).
	BudgetTokensRemaining int64
	// Reversibility ex.: "reversible", "irreversible".
	Reversibility string
	// Sensitivity ex.: "public", "confidential".
	Sensitivity string
	// RiskClass é a classe de risco SA-ROC (AOS-074) atribuída pelo RiskGate à
	// acção ("safe", "gray", "danger"). Preenchida pelo gate de risco ANTES de
	// qualquer negação, para que a classificação seja selada no audit tamper-evident
	// (ADR-013) tanto em permit como em deny. Vazia se o RiskGate não está na cadeia.
	RiskClass string
	// RiskApprover identifica QUEM autorizou uma acção gray/danger (do canal HITL),
	// para atribuição no audit tamper-evident: um override fica ligado à identidade
	// que o concedeu. Vazio se não houve confirmação humana (safe, auto-aprovada,
	// timeout, recusada) ou se o RiskGate não está na cadeia. Sem segredos (SAROC-05).
	RiskApprover string
	// RiskDecisionMode é a NATUREZA da decisão do gate de risco ("auto", "batch",
	// "human", "timeout", "denied"), selada no audit para distinguir COMO a acção foi
	// resolvida — ex.: um permit gray auto-aprovado por maturidade vs confirmado por
	// humano deixam de ser indistinguíveis no log (SAROC-05).
	RiskDecisionMode string
}

// PortVersion é a versão SemVer da porta C1 (contrato de mediação) que este RM
// implementa. É gravada em cada evento de mediação para que consumidores possam
// evoluir com o contrato (convenção transversal C1, tecnica/12 §72).
const PortVersion = "1.0.0"

// Call é o pedido de tool call submetido a [Monitor.Mediate]. É a única forma
// de descrever uma acção externa no AOS; nenhuma via alternativa a executa.
type Call struct {
	// RequestID correlaciona a mediação com o tracing distribuído (OTel) — é a
	// convenção transversal a todos os contratos (C1, tecnica/12 §72). Opcional
	// em AOS-003; propagado ao evento de auditoria quando presente.
	RequestID string
	// RunID e StepID correlacionam a mediação com a trajectória no Event Store
	// (stream_id = RunID; idempotency_key = RunID:StepID).
	RunID        string
	StepID       string
	ParentStepID string
	// ToolID identifica a tool registada a despachar (default-deny: uma tool
	// não registada é negada).
	ToolID string
	// Capability é o direito escopado que a política avalia (ex.: "cap:http.post").
	Capability string
	Resource   Resource
	Principal  Principal
	Context    CallContext
	// Credential é o token NHI (AOS-005) apresentado pelo chamador. O hook de
	// identidade (o IdentityCheck de platform/identity) verifica-o e RESOLVE o
	// Principal a partir dele. Vazio ⇒ chamada anónima, negada fail-closed pelo
	// hook de identidade (proibição de round-robin anónimo, ADR-003). Não entra
	// no fingerprint do call nem é gravado nos eventos (não é um segredo de infra,
	// mas é um bearer efémero: só metadados da NHI resolvida vão ao audit).
	Credential string
	// Input é o payload opaco entregue à tool após permit.
	Input []byte
	// ApprovalEvidence é a evidência BRUTA de aprovação humana (ADR-013/ADR-016) que o
	// chamador anexa quando uma acção escalada foi aprovada. É OPACA e UNTRUSTED: nada
	// nela é acreditado até o [ApprovalGate] a verificar contra a preview desta call. Um
	// chamador não pode "trazer" uma aprovação — só bytes a verificar. Vazia ⇒ sem
	// aprovação (o caso normal). Nunca é gravada no audit (pode conter material de
	// credencial); só a atribuição resultante entra no registo.
	ApprovalEvidence []byte

	// humanApproved é a PROVA VERIFICADA de aprovação humana desta call. NÃO-EXPORTADO
	// DE PROPÓSITO: só o [ApprovalGate] (deste pacote) a escreve, e só após verificação
	// criptográfica ligada à [ApprovalPreview] — nenhum pacote externo a consegue forjar,
	// o mesmo mecanismo estrutural que torna [Decision.permit] não-forjável. É a ÚNICA
	// excepção que o [TaintGate] aceita à barreira «untrusted não comanda» (AOS-069):
	// o taint da call PERMANECE untrusted (ela foi mesmo originada pelo modelo, e o
	// registo tem de continuar a dizê-lo) — o que muda é existir prova de que um humano
	// com autoridade assumiu ESTA acção. Ler de fora via [Call.HumanApproval].
	humanApproved *ApprovalProof
}

// fingerprint calcula uma impressão determinística do call que liga o Permit à
// acção autorizada. Um Permit só é válido para o call de que foi mintado
// (defesa contra reutilização cruzada). Não é um mecanismo criptográfico — a
// inviolabilidade primária vem do campo não-exportado do Permit (ver decision.go).
func fingerprint(c Call) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(c.RunID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.StepID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.ToolID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Capability))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Resource.Type))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Resource.Value))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Principal.NHIID))
	return h.Sum64()
}
