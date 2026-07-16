package network

import (
	"errors"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// TestPolicy_Evaluate_DefaultDeny_Escopado cobre o critério POLÍTICA: egress na
// allowlist DO PRINCIPAL é permitido; para OUTRO principal (fora do escopo) é
// negado. A decisão-base é DENY; a única forma de allow é uma regra explícita que
// case (principal/classe, host/IP, porta).
func TestPolicy_Evaluate_DefaultDeny_Escopado(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("Load embutida: %v", err)
	}

	cases := []struct {
		name      string
		principal referencemonitor.Principal
		dest      Destination
		want      Effect
	}{
		// web-fetcher: permitido por host e por CIDR, na porta certa.
		{"host permitido (classe)", principalClass("web-fetcher"), NewDestination("api.github.com", 443), EffectAllow},
		{"host permitido (nhi)", principalNHI("agent-fetcher-01"), NewDestination("api.github.com", 443), EffectAllow},
		{"cidr permitido", principalClass("web-fetcher"), NewDestination("93.184.216.34", 443), EffectAllow},
		{"cidr permitido porta alt", principalClass("web-fetcher"), NewDestination("93.184.216.34", 8443), EffectAllow},
		// Porta fora da lista → deny (filtragem por porta).
		{"porta nao permitida", principalClass("web-fetcher"), NewDestination("api.github.com", 80), EffectDeny},
		// Destino fora da allowlist → deny.
		{"host fora da allowlist", principalClass("web-fetcher"), NewDestination("evil.example.com", 443), EffectDeny},
		{"ip fora do cidr", principalClass("web-fetcher"), NewDestination("8.8.8.8", 443), EffectDeny},
		// ESCOPO POR PRINCIPAL: billing não alcança os destinos de web-fetcher e
		// vice-versa, mesmo que o destino seja válido para OUTRO principal.
		{"billing nao alcanca dest de web-fetcher", principalClass("billing"), NewDestination("api.github.com", 443), EffectDeny},
		{"web-fetcher nao alcanca cidr de billing", principalClass("web-fetcher"), NewDestination("10.20.0.5", 443), EffectDeny},
		{"billing alcanca o seu proprio cidr", principalClass("billing"), NewDestination("10.20.0.5", 443), EffectAllow},
		// Principal sem identidade → nenhuma regra casa (fail-closed).
		{"principal anonimo", referencemonitor.Principal{}, NewDestination("api.github.com", 443), EffectDeny},
		// Classe desconhecida → deny.
		{"classe desconhecida", principalClass("scraper"), NewDestination("api.github.com", 443), EffectDeny},
		// Destino inválido (sem porta) → deny.
		{"destino sem porta", principalClass("web-fetcher"), NewDestination("api.github.com", 0), EffectDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Evaluate(tc.principal, tc.dest); got != tc.want {
				t.Fatalf("Evaluate(%+v, %s) = %q, quero %q", tc.principal, tc.dest, got, tc.want)
			}
		})
	}
}

// TestPolicy_FailClosed_Malformada cobre o critério FAIL-CLOSED no carregamento: uma
// allowlist cujo default não seja deny, ou com regras ambíguas/malformadas, é
// REJEITADA — nunca produz uma Policy fail-open.
func TestPolicy_FailClosed_Malformada(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"json invalido", `{`},
		{"versao vazia", `{"version":"","default":"deny","rules":[]}`},
		{"default allow", `{"version":"v1","default":"allow","rules":[]}`},
		{"default ausente", `{"version":"v1","rules":[]}`},
		{"regra sem id", `{"version":"v1","default":"deny","rules":[{"principals":["class:x"],"destinations":[{"hosts":["h"],"ports":[443]}]}]}`},
		{"regra sem principal", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":[],"destinations":[{"hosts":["h"],"ports":[443]}]}]}`},
		{"selector nao escopado", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["x"],"destinations":[{"hosts":["h"],"ports":[443]}]}]}`},
		{"destino sem localizador", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:x"],"destinations":[{"ports":[443]}]}]}`},
		{"destino sem porta", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:x"],"destinations":[{"hosts":["h"]}]}]}`},
		{"cidr invalido", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:x"],"destinations":[{"cidrs":["nao-e-cidr"],"ports":[443]}]}]}`},
		{"porta invalida", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:x"],"destinations":[{"hosts":["h"],"ports":[70000]}]}]}`},
		{"host vazio", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:x"],"destinations":[{"hosts":[""],"ports":[443]}]}]}`},
		// CIDR catch-all (0.0.0.0/0, ::/0) → recusado (egress irrestrito não exprimível).
		{"cidr catch-all ipv4", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:x"],"destinations":[{"cidrs":["0.0.0.0/0"],"ports":[443]}]}]}`},
		{"cidr catch-all ipv6", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:x"],"destinations":[{"cidrs":["::/0"],"ports":[443]}]}]}`},
		// Selector só-prefixo (id vazio) → inerte, recusado no carregamento.
		{"selector nhi vazio", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["nhi:"],"destinations":[{"hosts":["h"],"ports":[443]}]}]}`},
		{"selector class vazio", `{"version":"v1","default":"deny","rules":[{"id":"r","principals":["class:"],"destinations":[{"hosts":["h"],"ports":[443]}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.json))
			if !errors.Is(err, ErrPolicyMalformed) {
				t.Fatalf("Parse(%s) err = %v, quero ErrPolicyMalformed", tc.name, err)
			}
		})
	}
}

// TestPolicy_Versionada_TamperEvident cobre o critério VERSÃO: a allowlist é
// versionada por um digest sha256 canónico; alterar qualquer regra muda o digest
// (tamper-evident) mas a re-ordenação/espaçamento do JSON NÃO o muda (canónico).
func TestPolicy_Versionada_TamperEvident(t *testing.T) {
	base := `{"version":"v1","default":"deny","rules":[
		{"id":"a","principals":["class:x"],"destinations":[{"hosts":["h1.io","h2.io"],"ports":[443,8443]}]}
	]}`
	// Mesmo conteúdo, ordem/espaços diferentes (hosts e portas trocados).
	reordered := `{"version":"v1","default":"deny","rules":[
		{"id":"a","principals":["class:x"],"destinations":[{"hosts":["h2.io","h1.io"],"ports":[8443,443]}]}
	]}`
	// Conteúdo alterado (um host a mais): digest TEM de mudar.
	tampered := `{"version":"v1","default":"deny","rules":[
		{"id":"a","principals":["class:x"],"destinations":[{"hosts":["h1.io","h2.io","h3.io"],"ports":[443,8443]}]}
	]}`

	pBase, err := Parse([]byte(base))
	if err != nil {
		t.Fatalf("Parse base: %v", err)
	}
	pReord, err := Parse([]byte(reordered))
	if err != nil {
		t.Fatalf("Parse reordered: %v", err)
	}
	pTamper, err := Parse([]byte(tampered))
	if err != nil {
		t.Fatalf("Parse tampered: %v", err)
	}

	if pBase.Hash() != pReord.Hash() {
		t.Fatalf("digest canónico deve ser estável a re-ordenação: %s != %s", pBase.Hash(), pReord.Hash())
	}
	if pBase.Hash() == pTamper.Hash() {
		t.Fatalf("digest deve mudar com o conteúdo (tamper-evident): %s == %s", pBase.Hash(), pTamper.Hash())
	}
	// Version() = tag#digest12.
	if got := pBase.Version(); !strings.HasPrefix(got, "v1#") || len(got) != len("v1#")+12 {
		t.Fatalf("Version() = %q, formato esperado tag#digest12", got)
	}
	// A embutida carrega e tem versão estável entre cargas (determinismo).
	e1, _ := Load()
	e2, _ := Load()
	if e1.Version() != e2.Version() {
		t.Fatalf("versão da embutida instável: %s != %s", e1.Version(), e2.Version())
	}
}

// TestPolicy_Evaluate_AllowDeny_ProvaEstrutural prova que a decisão-base é DENY: uma
// policy sem QUALQUER regra nega tudo; só uma regra explícita produz allow.
func TestPolicy_Evaluate_AllowDeny_ProvaEstrutural(t *testing.T) {
	empty, err := Parse([]byte(`{"version":"v1","default":"deny","rules":[]}`))
	if err != nil {
		t.Fatalf("Parse vazia: %v", err)
	}
	if got := empty.Evaluate(principalClass("x"), NewDestination("h", 443)); got != EffectDeny {
		t.Fatalf("policy vazia deve negar tudo, obtive %q", got)
	}
	withRule, err := Parse([]byte(`{"version":"v1","default":"deny","rules":[
		{"id":"r","principals":["class:x"],"destinations":[{"hosts":["h"],"ports":[443]}]}]}`))
	if err != nil {
		t.Fatalf("Parse com regra: %v", err)
	}
	if got := withRule.Evaluate(principalClass("x"), NewDestination("h", 443)); got != EffectAllow {
		t.Fatalf("regra explícita deve permitir, obtive %q", got)
	}
}
