package broker

import (
	"context"
	"sort"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// EffectiveAuthority calcula a autoridade EFECTIVA de um principal para a troca:
// a intersecção entre a autoridade do UTILIZADOR (as capabilities que o token
// carrega, [referencemonitor.Principal.Authority]) e o ESCOPO da CLASSE do agente
// (o conjunto-máximo que a classe autoriza). É a materialização de AOS-057
// (utilizador ∩ classe) na fronteira do broker. Determinista: resultado ordenado
// e sem duplicados.
func EffectiveAuthority(userAuthority, classScope []string) []string {
	set := make(map[string]struct{}, len(classScope))
	for _, c := range classScope {
		set[c] = struct{}{}
	}
	var out []string
	seen := make(map[string]struct{}, len(userAuthority))
	for _, u := range userAuthority {
		if _, ok := set[u]; !ok {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// permitsCapability indica se a capability pedida está na autoridade efectiva.
func permitsCapability(userAuthority, classScope []string, capability string) bool {
	if capability == "" {
		return false
	}
	for _, c := range EffectiveAuthority(userAuthority, classScope) {
		if c == capability {
			return true
		}
	}
	return false
}

// ScopeGate é um [referencemonitor.Hook] que impõe a consistência de escopo da
// troca de credenciais NA fronteira do Reference Monitor, em DOIS eixos da chave do
// Vault:
//
//   - CAPABILITY (AOS-057): utilizador ∩ classe — nega se a capability pedida não
//     pertencer à autoridade efectiva do principal;
//   - PROVIDER (AOS-324): nega se o provedor pedido não pertencer à autoridade
//     efectiva de provedor (tecto da classe ∩ grants do token). Ver [provider.go]
//     para a política, a postura por omissão e os seus limites DECLARADOS.
//
// O terceiro eixo — REGION — é imposto a montante pelo Reference Monitor
// (`ObligationRegion`), que compara `call.Resource.Region`; o broker alinha esse
// campo com o `Downstream.Region` da chave e não o duplica aqui.
//
// Só actua sobre o toolID da troca do broker (para não interferir com outras tools
// na mesma cadeia). Assim, "só se troca por credenciais consistentes com o escopo"
// é uma propriedade IMPOSTA pela mediação e registada como negação no Event Store.
type ScopeGate struct {
	toolID         string
	classScopes    map[string][]string // AgentClass → escopo-máximo da classe
	classProviders map[string][]string // AgentClass → provedores; nil ⇒ ProviderPostureUnset
	providerHosts  map[string][]string // Provider → hosts; nil ⇒ ResourceBindingUnset (AOS-331)
}

// ScopeGateOption configura eixos ADICIONAIS do [ScopeGate] sem quebrar os
// chamadores existentes de [NewScopeGate].
type ScopeGateOption func(*ScopeGate)

// WithGateClassProviders declara a política do eixo PROVIDER do gate: o mapa
// AgentClass → provedores autorizados (AOS-324). Declará-la coloca o gate em
// [ProviderPostureEnforced]; a sua ausência é [ProviderPostureUnset] — estado
// DECLARADO (ver [ScopeGate.ProviderPosture] e o doc de provider.go), não um
// deny-all silencioso. Um mapa nil explícito mantém a postura unset.
func WithGateClassProviders(classProviders map[string][]string) ScopeGateOption {
	return func(g *ScopeGate) { g.classProviders = copyProviderPolicy(classProviders) }
}

// WithGateProviderHosts declara a allowlist de HOSTS por provedor (AOS-331): amarra o provedor
// autorizado ao RECURSO de destino. Declará-la coloca o gate em [ResourceBindingEnforced]; a sua
// ausência é [ResourceBindingUnset] — estado DECLARADO, não um deny-all silencioso. Um mapa nil
// explícito mantém a postura unset.
func WithGateProviderHosts(providerHosts map[string][]string) ScopeGateOption {
	return func(g *ScopeGate) { g.providerHosts = copyProviderHosts(providerHosts) }
}

// ResourceBindingPosture reporta a postura do eixo recurso↔provedor deste gate. Existe para o
// banner de arranque a poder declarar (AOS-332) e para os testes a poderem assertar.
func (g ScopeGate) ResourceBindingPosture() ResourceBindingPosture {
	return resourceBindingPosture(g.providerHosts)
}

// copyProviderPolicy copia a política de provedores (nil preserva-se como nil — é
// a distinção entre "não declarada" e "declarada vazia").
func copyProviderPolicy(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	cp := make(map[string][]string, len(m))
	for k, v := range m {
		cp[k] = append([]string(nil), v...)
	}
	return cp
}

// NewScopeGate constrói o gate para o toolID de troca e o mapa de escopos por
// classe (AOS-057). Um mapa nil trata todas as classes como escopo vazio (nega
// tudo — fail-closed) no eixo capability.
//
// O eixo PROVIDER (AOS-324) declara-se por [WithGateClassProviders]; sem essa opção
// o gate fica em [ProviderPostureUnset] e só nega, nesse eixo, um pedido SEM
// provedor. [Broker.ScopeGate] propaga automaticamente a política registada em
// [WithClassProviders], pelo que o composition root só tem de a declarar UMA vez.
func NewScopeGate(toolID string, classScopes map[string][]string, opts ...ScopeGateOption) ScopeGate {
	cp := make(map[string][]string, len(classScopes))
	for k, v := range classScopes {
		cp[k] = append([]string(nil), v...)
	}
	g := ScopeGate{toolID: toolID, classScopes: cp}
	for _, o := range opts {
		o(&g)
	}
	return g
}

// ProviderPosture devolve a postura DECLARADA do eixo provider deste gate.
func (g ScopeGate) ProviderPosture() ProviderPosture { return providerPosture(g.classProviders) }

// Name identifica o hook (usado em DeniedBy e nos spies do RM).
func (g ScopeGate) Name() string { return "broker-scope" }

// Evaluate implementa [referencemonitor.Hook]. Fora do toolID da troca é neutro.
func (g ScopeGate) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if call.ToolID != g.toolID {
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	classScope := g.classScopes[call.Principal.AgentClass]
	if !permitsCapability(call.Principal.Authority, classScope, call.Capability) {
		return referencemonitor.HookResult{
			Decision: referencemonitor.HookDeny,
			Reason:   ErrOutOfScope.Error(),
		}, nil
	}
	// EIXO PROVIDER (AOS-324). O provedor vem do envelope NÃO-SECRETO da troca
	// (`Call.Input`), porque o contrato C1 do RM não tem campo de provedor.
	provider, ok := providerFromCallInput(call.Input)
	if !ok {
		// Sem envelope legível não há provedor a avaliar. Sob política DECLARADA
		// isso é informação insuficiente ⇒ NEGA (fail-closed); em
		// [ProviderPostureUnset] o eixo não é imposto e o gate não se opõe.
		if g.ProviderPosture() == ProviderPostureEnforced {
			return referencemonitor.HookResult{
				Decision: referencemonitor.HookDeny,
				Reason:   ErrProviderUndetermined.Error(),
			}, nil
		}
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	if err := authorizeProvider(g.classProviders, call.Principal.AgentClass, call.Principal.Authority, provider); err != nil {
		return referencemonitor.HookResult{
			Decision: referencemonitor.HookDeny,
			Reason:   err.Error(),
		}, nil
	}
	// EIXO RECURSO↔PROVEDOR (AOS-331). O provedor estar autorizado não diz para ONDE a
	// credencial dele vai ser apresentada. Lê-se do `Call.Resource` — o contrato C1 — e não do
	// envelope, porque é esse o valor que a mediação SELA: decidir sobre um e selar o outro
	// seria repetir a divergência de namespaces que o AOS-330 fechou no eixo do Vault.
	if err := authorizeResource(g.providerHosts, provider, call.Resource.Type, call.Resource.Value); err != nil {
		return referencemonitor.HookResult{
			Decision: referencemonitor.HookDeny,
			Reason:   err.Error(),
		}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}
