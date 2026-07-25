package identity

import (
	"github.com/aos-ref/kernel/contract/identity/delegation"
)

// ChildRequest descreve a emissão on-behalf-of de um sub-agente (token filho). O
// pai é apresentado como token compacto ([Issuer.IssueChild]); o filho herda a
// cadeia de delegação do pai mais um novo elo.
type ChildRequest struct {
	// AgentID é a identidade única do sub-agente a criar. Obrigatório.
	AgentID string
	// AgentClass selecciona a ClassPolicy do filho (TTL/escopo-máximo da classe).
	// Obrigatório e configurada, senão ErrUnknownClass.
	AgentClass string
	// PolicyRef é a referência de política do filho (policy_ref, AOS-004).
	PolicyRef string
	// Authority são as capabilities PEDIDAS para o filho. Têm de ser subconjunto
	// da autoridade da folha do pai (senão escalada ⇒ ErrDelegationInvalid). A
	// autoridade efectiva é ainda intersectada com o escopo-máximo da classe do
	// filho (a classe pode estreitar, nunca alargar).
	Authority []string
}

// Claims é o conjunto de asserções do token NHI. Codifica o par (utilizador,
// agente), a classe/política e o escopo scoped/time-bound (AOS-005).
type Claims struct {
	// UserID é o humano responsável na raiz da cadeia de delegação.
	UserID string `json:"user_id"`
	// AgentID é a identidade única do agente (a NHI).
	AgentID string `json:"agent_id"`
	// AgentClass é a classe sob cuja política o agente actua.
	AgentClass string `json:"agent_class"`
	// PolicyRef aponta a política (policy_ref) aplicável (AOS-004).
	PolicyRef string `json:"policy_ref,omitempty"`
	// Scope são as capabilities/recursos concedidos (autoridade = utilizador ∩
	// classe, e ⊆ pai em on-behalf-of).
	Scope []string `json:"scope"`
	// Issuer (iss) identifica o emissor; a verificação usa a sua chave pública.
	Issuer string `json:"iss"`
	// IssuedAt (iat), NotBefore (nbf) e Expiry (exp) são segundos Unix. O TTL
	// curto (exp-iat) minimiza a janela de revogação.
	IssuedAt  int64 `json:"iat"`
	NotBefore int64 `json:"nbf"`
	Expiry    int64 `json:"exp"`
	// JTI é o identificador único do token (usado na revogação).
	JTI string `json:"jti"`
	// DelegationChain é a cadeia de delegação on-behalf-of embebida (AOS-006):
	// ordenada da raiz humana ("human:<user_id>") até ao agente actual. Vai SELADA
	// pela assinatura do token (o emissor assina a cadeia inteira). O verificador
	// valida-a (não-escalada, raiz humana, encadeamento de hash).
	DelegationChain delegation.Chain `json:"delegation_chain,omitempty"`
}

// Token é o resultado de uma emissão: a string bearer compacta e as claims que
// nela vão embutidas (para conveniência do chamador; a fonte de verdade é o
// Compact assinado).
type Token struct {
	// Compact é a string bearer a apresentar ao Reference Monitor (Call.Credential).
	Compact string
	// Claims são as asserções embutidas (cópia de conveniência).
	Claims Claims
}
