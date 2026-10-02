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
	// UserID é o HUMANO na raiz da cadeia do token (claim `user_id`), resolvido pelo hook de
	// identidade a partir do token VERIFICADO. O nó grava-o também no Goal, a partir da credencial
	// verificada no `POST /runs`, para a retoma confrontar a credencial fresca com a do run
	// (AOS-440): o mesmo agente cunhado para OUTRO humano não continua o run de ninguém.
	UserID string
	// MandateID é o mandato sob o qual o token foi cunhado (AOS-427), resolvido pelo hook de
	// identidade a partir do token VERIFICADO — vazio quando o emissor não é mandatado. Vai ao
	// evento de mediação e ao selo WORM: fecha o resíduo 3 do ADR-033 («este run correu sob o
	// mandato X» deixa de depender de reconstituir o token).
	MandateID string
	// MandateSigner é a IMPRESSÃO DIGITAL do pino que VERIFICOU esse mandato (AOS-446 fase 1),
	// resolvida pelo mesmo hook e pela mesma via — do token verificado, nunca da call. Vazia
	// quando o emissor não é mandatado. Vai ao evento de mediação e ao selo WORM (a partir do
	// `SchemaV5`): o `mandate_id` diz QUAL mandato, esta diz sob QUE CHAVE ele foi aceite, que é
	// o que denuncia um `AOS_MANDATE_SIGNERS` trocado por quem tem root no host.
	MandateSigner string
	// RequestedBy é o SUBMISSOR do run (AOS-439): quem pediu o plano de que o run é trabalho. NÃO
	// é uma afirmação de identidade nem vem do token — é derivado pelo NÓ do seu próprio log da
	// fila de planos, no `POST /runs`, e viaja do Goal até aqui. O hook de identidade PRESERVA-O
	// quando substitui o resto do Principal: é atribuição, não autoridade, e nenhum gate decide por
	// ele. Vazio num run que não é trabalho de um plano.
	RequestedBy string
	// Subject é o TITULAR DOS DADOS do run (AOS-440): a chave por-titular sob a qual o conteúdo do
	// run é selado. Vazio ⇒ NHIID, que é o que todos os runs anteriores usaram ([Principal.Titular]).
	// Separado do NHIID porque são perguntas diferentes: o NHIID diz QUEM chamou o nó, o Subject
	// diz DE QUEM são os dados. Num run filho de um plano, o chamador é o drenador e o titular é o
	// submissor.
	//
	// A FONTE é o `Goal.Subject` do agent-runtime: o loop copia-o para cá em cada tool call, e é
	// por aqui que ele atravessa a via durável (a Activity leva o Principal inteiro) até ao
	// step-ledger, que sela o output da tool sob a MESMA chave que a captura do turno.
	Subject string
}

// Titular devolve o titular dos dados: [Principal.Subject], ou o NHIID quando vazio (AOS-440).
func (p Principal) Titular() string {
	if p.Subject != "" {
		return p.Subject
	}
	return p.NHIID
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
	// AllowedTools é a LISTA-BRANCA de tools do run (AOS-413, ADR-027 §2.3), por nome (o
	// `ToolID`). nil ⇒ sem restrição além do que o resto da cadeia decide; não-nil, MESMO
	// VAZIA ⇒ só as listadas, e uma lista vazia nega todas. Quem a preenche é o Agent
	// Runtime, a partir do goal do run — nunca o modelo. ESTREITA, nunca alarga: uma tool na
	// lista continua sujeita a toda a mediação.
	//
	// É imposta AQUI DENTRO (AOS-485): pelo [RunAllowlistGate] quando a cadeia o tem e, em
	// qualquer cadeia, pelo backstop de [Monitor.evaluate] — ver [RunAllowsTool].
	//
	// NÃO entra no fingerprint nem na [ApprovalPreview]: não descreve a acção, restringe-a. A
	// mesma acção com outra lista é a mesma acção, e uma call fora da lista é negada antes de
	// haver permit a ligar ou aprovação a casar.
	//
	// ARMADILHA: copiar este campo com `append([]string(nil), x...)` transforma a lista VAZIA
	// em nil e ABRE todas as tools. Atribui-se o slice tal como veio.
	AllowedTools []string

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
