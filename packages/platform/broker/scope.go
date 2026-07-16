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

// ScopeGate é um [referencemonitor.Hook] que impõe a consistência de escopo
// (utilizador ∩ classe) da troca de credenciais NA fronteira do Reference Monitor.
// Só actua sobre o toolID da troca do broker (para não interferir com outras
// tools na mesma cadeia); para esse toolID, NEGA fail-closed se a capability
// pedida não pertencer à autoridade efectiva do principal. Assim, "só se troca por
// credenciais consistentes com o escopo" é uma propriedade IMPOSTA pela mediação e
// registada como negação no Event Store.
type ScopeGate struct {
	toolID      string
	classScopes map[string][]string // AgentClass → escopo-máximo da classe
}

// NewScopeGate constrói o gate para o toolID de troca e o mapa de escopos por
// classe (AOS-057). Um mapa nil trata todas as classes como escopo vazio (nega
// tudo — fail-closed).
func NewScopeGate(toolID string, classScopes map[string][]string) ScopeGate {
	cp := make(map[string][]string, len(classScopes))
	for k, v := range classScopes {
		cp[k] = append([]string(nil), v...)
	}
	return ScopeGate{toolID: toolID, classScopes: cp}
}

// Name identifica o hook (usado em DeniedBy e nos spies do RM).
func (g ScopeGate) Name() string { return "broker-scope" }

// Evaluate implementa [referencemonitor.Hook]. Fora do toolID da troca é neutro.
func (g ScopeGate) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	if call.ToolID != g.toolID {
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	classScope := g.classScopes[call.Principal.AgentClass]
	if permitsCapability(call.Principal.Authority, classScope, call.Capability) {
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	return referencemonitor.HookResult{
		Decision: referencemonitor.HookDeny,
		Reason:   ErrOutOfScope.Error(),
	}, nil
}
