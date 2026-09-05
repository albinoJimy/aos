package broker

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/aos-ref/platform/broker/internal/vault"
)

// EIXO PROVIDER DA TROCA (AOS-324) — O TERCEIRO EIXO DA AUTORIZAÇÃO.
//
// A chave do material no Vault é o TRIPLO {Provider, Region, Capability}
// ([vault.Key], montada em [Broker.dispatch] a partir do PEDIDO). Até AOS-324 só
// dois desses eixos tinham imposição:
//
//   - CAPABILITY — imposta pelo [ScopeGate] deste pacote (utilizador ∩ classe,
//     AOS-057) e, a jusante, pela allowlist assinada do PDP (AOS-007);
//   - REGION — imposta pelo Reference Monitor via `ObligationRegion`
//     (kernel/reference-monitor), que compara `call.Resource.Region` — o mesmo
//     valor que [Broker.Exchange] alinha com o `Downstream.Region` da chave;
//   - PROVIDER — SEM IMPOSIÇÃO NENHUMA. E como a troca reutiliza uma capability
//     CONSTANTE ([ExchangeCapabilityHTTPPost], decisão AOS-264), o provider era o
//     ÚNICO discriminante da chave — e ninguém o validava. Um principal autorizado
//     a trocar para o provedor A obtinha material de QUALQUER provedor presente no
//     Vault: confusão de deputado.
//
// Este ficheiro fecha esse eixo: o provedor pedido passa a ser comparado com a
// AUTORIDADE do principal (autoridade do utilizador ∩ tecto da classe), no mesmo
// molde do eixo capability.
//
// # POLÍTICA DE PROVEDORES (duas fontes, a segunda só ESTREITA)
//
//  1. TECTO DA CLASSE — o mapa AgentClass → provedores, declarado pelo composition
//     root em [WithClassProviders] (e propagado ao gate por [Broker.ScopeGate]).
//     É o análogo, no eixo provider, do mapa de escopos de classe do AOS-057.
//     [ProviderAny] ("*") é o curinga EXPLÍCITO para uma classe que pode alcançar
//     qualquer provedor — tem de ser ESCRITO, nunca é implícito.
//  2. GRANTS DO PRINCIPAL — entradas com o prefixo [ProviderGrantPrefix] na
//     [referencemonitor.Principal.Authority] (ex.: "prov:stripe"), vindas do token
//     NHI verificado. Quando o principal declara grants, a autoridade efectiva de
//     provedor é a INTERSECÇÃO com o tecto da classe: o token pode RESTRINGIR mais,
//     NUNCA ampliar (mesmo princípio da fonte externa de autoridade do ScopeGate do
//     RM). Um principal sem grants fica sujeito apenas ao tecto da classe.
//
// # ESTADO POR OMISSÃO — DECLARADO, NÃO SILENCIOSO ([ProviderPostureUnset])
//
// Sem política declarada (nenhum [WithClassProviders]), a postura é
// [ProviderPostureUnset]: a COMPARAÇÃO POR CONJUNTO não corre — não há autoridade
// de provedor contra a qual comparar — e o eixo NÃO é imposto. Este estado é
// DECLARADO e legível, nunca silencioso:
//
//   - [Broker.ProviderPosture] e [ScopeGate.ProviderPosture] devolvem-no;
//   - CADA troca sela a postura no Event Store, no campo `provider_policy` do
//     evento `credential.exchange.issued` — uma troca não-imposta fica auditável e
//     greppável, e o wiring (DEF-218) pode assertar `"enforced"` como pré-condição;
//   - o que a postura NÃO relaxa: um pedido SEM provedor
//     (`Downstream.Provider` vazio) é NEGADO fail-closed nas DUAS posturas
//     ([ErrProviderUndetermined]) — sem provedor não há chave legítima, e o Vault
//     nunca é consultado com um eixo em branco.
//
// A alternativa — negar tudo enquanto não houver política — seria fail-closed no
// papel e um deny-all no dia do wiring, com o broker a recusar trocas sem que o
// operador soubesse porquê. A postura escolhida torna a ausência de política um
// FACTO REGISTADO em cada troca, e a imposição um interruptor de UMA linha
// ([WithClassProviders]) no composition root.
//
// # LIMITE DECLARADO
//
// Sob [ProviderPostureUnset] o eixo continua sem imposição por conjunto: quem ligar
// o broker sem declarar provedores herda o defeito de AOS-324 — com a diferença de
// que agora está escrito, medido em cada evento e fechado por configuração. É por
// isso que este ticket é PRÉ-CONDIÇÃO de DEF-218 (wiring do broker).

// ProviderPosture é a postura DECLARADA do eixo provider de uma troca.
type ProviderPosture string

const (
	// ProviderPostureUnset — nenhuma política de provedores declarada: o eixo não é
	// imposto por conjunto (só o provedor vazio é negado). Selado em cada troca.
	ProviderPostureUnset ProviderPosture = "unset"
	// ProviderPostureEnforced — política declarada: o provedor pedido TEM de
	// pertencer à autoridade efectiva de provedor do principal, ou a troca é NEGADA.
	ProviderPostureEnforced ProviderPosture = "enforced"
)

// ProviderAny é o curinga EXPLÍCITO do tecto de uma classe: a classe pode trocar
// por qualquer provedor. Tem de ser escrito na política; nunca é implícito.
const ProviderAny = "*"

// ProviderGrantPrefix é o prefixo das entradas de AUTORIDADE DE PROVEDOR na
// [referencemonitor.Principal.Authority] (ex.: "prov:stripe"). Distingue-as das
// capabilities ("cap:…"), que continuam a ser avaliadas no eixo capability.
const ProviderGrantPrefix = "prov:"

// ProviderGrants extrai os provedores concedidos por uma autoridade de principal
// (as entradas [ProviderGrantPrefix]). Determinista: ordenado e sem duplicados.
// Uma entrada "prov:" sem nome é ignorada (não concede nada).
func ProviderGrants(authority []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(authority))
	for _, a := range authority {
		if !strings.HasPrefix(a, ProviderGrantPrefix) {
			continue
		}
		p := strings.TrimPrefix(a, ProviderGrantPrefix)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// EffectiveProviders calcula a autoridade EFECTIVA de provedor: o tecto da CLASSE
// estreitado pelos grants do PRINCIPAL, quando este os declara. Sem grants, vale o
// tecto da classe; com grants, vale a intersecção (o token restringe, nunca amplia)
// — e um tecto [ProviderAny] deixa passar exactamente os grants do principal.
// Determinista: resultado ordenado e sem duplicados.
func EffectiveProviders(authority, classProviders []string) []string {
	ceiling := dedupSorted(classProviders)
	grants := ProviderGrants(authority)
	if len(grants) == 0 {
		return ceiling
	}
	if containsProvider(ceiling, ProviderAny) {
		return grants
	}
	set := make(map[string]struct{}, len(ceiling))
	for _, p := range ceiling {
		set[p] = struct{}{}
	}
	var out []string
	for _, g := range grants {
		if _, ok := set[g]; ok {
			out = append(out, g)
		}
	}
	return out
}

// dedupSorted normaliza uma lista de provedores (ordenada, sem duplicados/vazios).
func dedupSorted(in []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, p := range in {
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func containsProvider(list []string, p string) bool {
	for _, v := range list {
		if v == p {
			return true
		}
	}
	return false
}

// providerPosture devolve a postura declarada para um mapa de política (nil ⇒
// [ProviderPostureUnset]).
func providerPosture(classProviders map[string][]string) ProviderPosture {
	if classProviders == nil {
		return ProviderPostureUnset
	}
	return ProviderPostureEnforced
}

// authorizeProvider é a REGRA ÚNICA do eixo provider, partilhada pelo [ScopeGate]
// (na mediação) e pela verificação defensiva server-side de [Broker.dispatch].
// Devolve nil se a troca pode prosseguir no eixo provider; caso contrário o
// sentinela ATRIBUÍVEL da negação ([ErrProviderUndetermined] ou
// [ErrProviderOutOfScope]) — NUNCA [ErrNoMaterial], que é ausência de material e
// não política.
//
// Fail-closed em ambas as posturas para um provedor vazio; sob
// [ProviderPostureEnforced], fail-closed também para um provedor fora da autoridade
// efectiva (incluindo o caso de a classe não constar da política — uma classe que
// nada declara não alcança provedor nenhum).
func authorizeProvider(classProviders map[string][]string, class string, authority []string, provider string) error {
	if provider == "" {
		return ErrProviderUndetermined
	}
	// AOS-330 — O EIXO TEM DE SOBREVIVER AO PATH, e a decisão é tomada no MESMO namespace em
	// que a chave vive.
	//
	// A normalização do path não é injectiva: `acme:eu`, `acme/eu` e `acme_eu` dobram todos em
	// `acme_eu`. Com a política a decidir sobre o valor CRU e o Vault a resolver sobre o
	// NORMALIZADO, autorizar um provedor não era autorizar a chave que ele alcança — e um
	// provedor aprovado alcançava material aprovisionado para outro.
	//
	// A ESCOLHA É A SEGUNDA VIA DO TICKET — recusar o que a normalização altere — e não
	// «decidir sobre o normalizado». Um provedor cujo nome não sobrevive ao path não é um
	// provedor identificável: `" "`, `"	"` e `"*"` normalizam todos para `_`, e aceitá-los
	// seria autorizar um eixo em branco com três roupas diferentes. Decidir sobre o
	// normalizado tornaria a colisão INVISÍVEL em vez de impossível: a política passaria a
	// tratar `acme:eu` e `acme_eu` como o mesmo provedor, silenciosamente.
	//
	// ISTO É `ErrProviderUndetermined`, NÃO `ErrProviderOutOfScope`: o eixo não é um provedor
	// que está fora da autoridade, é um valor que não identifica provedor nenhum. Sob
	// `enforced`, `" "` era antes atribuído a «fora de escopo» — atribuição errada do mesmo
	// defeito.
	//
	// SÓ O EIXO PROVIDER. A `Capability` contém SEMPRE `:` (`cap:http.post` → `cap_http.post`),
	// pelo que a mesma regra aplicada a ela negaria todas as trocas do sistema; e a `Region`
	// tem vocabulário fechado que sobrevive. O ticket nomeia o provedor, e é onde o eixo
	// morde: um nome de provedor vem de configuração de deployment, não de vocabulário fixo.
	if !vault.SegmentoEstavel(provider) {
		return ErrProviderUndetermined
	}
	if providerPosture(classProviders) == ProviderPostureUnset {
		return nil
	}
	allowed := EffectiveProviders(authority, classProviders[class])
	if containsProvider(allowed, ProviderAny) || containsProvider(allowed, provider) {
		return nil
	}
	return ErrProviderOutOfScope
}

// providerFromCallInput lê o provedor do envelope NÃO-SECRETO da troca
// ([exchangeInput]) transportado em `Call.Input`.
//
// PORQUÊ DAQUI E NÃO DO `Call`: o contrato C1 do Reference Monitor
// (`Call`/`Resource`) tem `Region` mas NÃO tem provedor — é por isso que o eixo
// region pôde ser fechado por uma obrigação do kernel (`ObligationRegion`) e o eixo
// provider não pode sê-lo sem alterar esse contrato. O broker é o dono do envelope
// da SUA troca, e é aqui que o provedor existe.
//
// Devolve ok=false se o input não for o envelope da troca (ex.: um [ScopeGate]
// avaliado directamente em testes de unidade, sem envelope).
func providerFromCallInput(input []byte) (string, bool) {
	if len(input) == 0 {
		return "", false
	}
	var in exchangeInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", false
	}
	return in.Provider, true
}
