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
				Reason:   g.razaoComPostura(ErrProviderUndetermined),
			}, nil
		}
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	if err := authorizeProvider(g.classProviders, call.Principal.AgentClass, call.Principal.Authority, provider); err != nil {
		return referencemonitor.HookResult{
			Decision: referencemonitor.HookDeny,
			Reason:   g.razaoComPostura(err),
		}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// providerPolicyReasonKey é a chave que nomeia a POSTURA do eixo provider dentro da
// razão de uma negação do gate. O NOME é o mesmo do campo `provider_policy` que o
// AOS-324 sela no evento da troca emitida e que o AOS-339 sela no da negação
// server-side, para que `grep provider_policy` sobre o WORM encontre os três
// caminhos.
//
// A FORMA, essa, NÃO é a mesma, e dizer que era foi um erro desta implementação
// apanhado em revisão: os outros dois são campos JSON e saem como
// `"provider_policy":"enforced"`; este é texto e sai como `provider_policy=enforced`.
// Um `grep provider_policy=` — com o sinal de igual — encontra APENAS este. É o
// prefixo SEM o `=` que é comum aos três.
const providerPolicyReasonKey = "provider_policy="

// razaoComPostura anexa a POSTURA em vigor à razão de uma negação do EIXO PROVIDER
// (AOS-332).
//
// # PORQUE SÓ O EIXO PROVIDER, QUANDO O EVENTO DO AOS-339 CARIMBA OS DOIS
//
// A assimetria é deliberada e vem da diferença entre os dois CONTENTORES. No
// `credential.exchange.denied` (AOS-339) a postura é um campo TIPADO ao lado de um
// campo `axis`: quem lê vê `axis=capability` E `provider_policy=enforced` e percebe
// que o segundo é o REGIME do broker, não a causa da decisão. Aqui a postura vai
// dentro da frase que EXPLICA a recusa — anexá-la a uma negação de capability leria
// como se o regime do eixo provider a tivesse causado, que é falso.
//
// Campo tipado ao lado do eixo: metadado de configuração, seguro em qualquer eixo.
// Sufixo na explicação: só onde o regime decidiu.
//
// # PORQUE É NA RAZÃO, QUE É TEXTO LIVRE
//
// Uma negação do gate não produz evento do broker: termina na cadeia de mediação, e
// o que fica selado é o `tool.call.denied` do Reference Monitor. Um hook controla
// desse registo exactamente TRÊS coisas — `Decision`, `Reason` e `PolicyVersion` —
// e o `DeniedBy`, que é o RM a escrever o `Name()` do hook. Não há campo livre em
// [referencemonitor.HookResult], em `MediationRecord` nem no payload de mediação.
//
// As TRÊS alternativas foram medidas e rejeitadas:
//
//   - `HookResult.Obligations` NÃO SOBREVIVE a um deny: `Monitor.fail` recebe as
//     obrigações por parâmetro explícito e o ramo `HookDeny` passa `nil` literal
//     (só o ramo de escalate propaga `res.Obligations`). E mesmo que sobrevivesse,
//     o conjunto de tipos aceites é FECHADO e fail-closed no permit — a postura
//     teria de viajar sob `ObligationAudit`, que significa outra coisa. Duas
//     torções para transportar um campo.
//   - `HookResult.PolicyVersion` É tipado, estável e selado — e é por isso que a
//     tentação existe. Mas o RM guarda o ÚLTIMO valor não-vazio da cadeia
//     (`monitor.go`, antes do switch de decisão), e este gate corre DEPOIS do hook
//     de política: escrever nele APAGARIA a versão de política do PDP no evento.
//     Trocar-se-ia um campo em falta por um campo corrompido, e o segundo é pior.
//   - Acrescentar um campo ao `mediationPayload` do kernel poria uma preocupação do
//     BROKER no contrato C1 do Reference Monitor, que é genérico sobre tools.
//
// # O LIMITE, DECLARADO
//
// Isto é texto livre, e o AOS-339 argumentou — com razão — que um código estável
// vale mais do que uma mensagem. A diferença é a propriedade do payload: lá o
// evento é do broker e o campo é tipado; aqui o evento é do RM e o broker não tem
// onde declarar um campo. A chave é fixa numa constante para que a forma não
// derive, e a prova é um teste que lê o payload selado — não a mensagem em memória.
func (g ScopeGate) razaoComPostura(err error) string {
	return err.Error() + "; " + providerPolicyReasonKey + string(g.ProviderPosture())
}
