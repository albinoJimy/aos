package main

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// O TECTO DE IDADE DA LEITURA SOBERANA É LIMITADO — E O TESTE NÃO O PERGUNTA A ELE PRÓPRIO.
//
// Achado da varredura adversarial de 2026-08-21: `sovereignReadDefaultMaxAge` mutado de 5 min
// para DEZ ANOS ficava tudo verde. A única asserção que existia era
//
//	if cfg.MaxAge != sovereignReadDefaultMaxAge { … }
//
// que é uma TAUTOLOGIA: compara o que o parser devolve com a constante de que o parser o tirou.
// Passa para qualquer valor — e passa mesmo para ZERO, que é precisamente o estado que a
// constante existe para impedir (sem MaxAge, um ID-token de soberania capturado é reapresentável
// durante toda a janela do `exp`).
//
// Não é um teste inútil: prova CABLAGEM (o default é aplicado quando a env está vazia). Fica, e
// ganha aqui os dois vizinhos que lhe faltavam — o VALOR, com um limite absoluto, e a cablagem
// pelo caminho REAL (`nodeConfigFromEnv`), não pelo parser em isolamento.
//
// O comportamento — que a janela efectiva é `MaxAge + leeway` e que um token velho é mesmo
// recusado — é provado com desvios absolutos em `packages/integration/oidc/tectos_test.go`, no
// pacote que o impõe. Aqui prova-se que este nó lhe entrega um número defensável.
// ---------------------------------------------------------------------------------------------

// tectoLeituraSoberanaDefensavel é o limite superior ARGUMENTADO, e é uma constante DO TESTE —
// nunca derivada de [sovereignReadDefaultMaxAge], que é o que se quer vigiar.
const tectoLeituraSoberanaDefensavel = 15 * time.Minute

// (A) VALOR.
func TestOTectoDeLeituraSoberanaTemValorDefensavel(t *testing.T) {
	if sovereignReadDefaultMaxAge <= 0 {
		t.Fatal("sovereignReadDefaultMaxAge <= 0 — sem tecto, um ID-token de soberania capturado e " +
			"reapresentavel durante TODA a janela do exp, com ou sem jti")
	}
	if sovereignReadDefaultMaxAge > tectoLeituraSoberanaDefensavel {
		t.Fatalf("sovereignReadDefaultMaxAge = %v, acima do tecto defensável de %v — um tecto desta "+
			"ordem e indistinguivel de nao haver tecto nenhum",
			sovereignReadDefaultMaxAge, tectoLeituraSoberanaDefensavel)
	}
}

// (B) CABLAGEM PELO CAMINHO REAL.
//
// A asserção antiga vivia sobre `parseSovereignReadOIDC()` chamado à parte. Este vai pelo
// `nodeConfigFromEnv()`, que é o que o `run` invoca — e assere sobre o VALOR ABSOLUTO, não sobre
// a constante. Uma mutação da constante cai aqui mesmo que a tautologia continue a passar.
func TestONoEntregaUmTectoLimitadoAoVerificadorDeLeitura(t *testing.T) {
	t.Setenv("AOS_SOVEREIGN_OIDC_ISSUER", "https://idp.example")
	t.Setenv("AOS_SOVEREIGN_OIDC_AUDIENCE", "aos-node")
	t.Setenv("AOS_SOVEREIGN_OIDC_MAX_AGE", "") // vazio ⇒ o default tem de entrar
	t.Setenv("AOS_SOVEREIGN_OIDC_REQUIRE_JTI", "")

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	// CONTROLO DO CENÁRIO: sem isto, um caminho que devolvesse `nil` (via legada por headers)
	// faria as asserções seguintes não correr, e o teste passaria a provar nada.
	if cfg.SovereignReadOIDC == nil {
		t.Fatal("SovereignReadOIDC ficou nil com issuer e audience dados — o no cairia na via " +
			"legada por headers e este teste mediria o vazio")
	}
	if cfg.SovereignReadOIDC.MaxAge <= 0 {
		t.Fatal("o no entrega MaxAge 0 ao verificador de leitura — o tecto de replay desapareceu " +
			"no caminho entre a constante e o oidc.NewVerifier do Bootstrap")
	}
	if cfg.SovereignReadOIDC.MaxAge > tectoLeituraSoberanaDefensavel {
		t.Fatalf("o no entrega MaxAge = %v, acima do tecto defensável de %v",
			cfg.SovereignReadOIDC.MaxAge, tectoLeituraSoberanaDefensavel)
	}
}

// TestOOverrideDoOperadorNAOEVigiado é um RESIDUAL DECLARADO, escrito como teste para que não se
// perca num comentário.
//
// `AOS_SOVEREIGN_OIDC_MAX_AGE` aceita qualquer duração positiva: um operador pode pôr `87600h` e
// o arranque não protesta. Isto NÃO é corrigido aqui de propósito — é um botão deliberado, e
// impor-lhe um tecto seria uma mudança de POLÍTICA, não um teste. O que se recusa é que a
// diferença passe despercebida: o default é vigiado, o override é do operador.
//
// Se algum dia se decidir limitar o override, este teste é o sítio onde a decisão fica registada
// e onde a mudança será notada.
func TestOOverrideDoOperadorNAOEVigiado(t *testing.T) {
	t.Setenv("AOS_SOVEREIGN_OIDC_ISSUER", "https://idp.example")
	t.Setenv("AOS_SOVEREIGN_OIDC_AUDIENCE", "aos-node")
	t.Setenv("AOS_SOVEREIGN_OIDC_MAX_AGE", "87600h") // dez anos
	t.Setenv("AOS_SOVEREIGN_OIDC_REQUIRE_JTI", "")

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Skipf("o arranque passou a RECUSAR overrides absurdos (%v) — se foi deliberado, apaga "+
			"este teste e escreve o novo limite; se nao foi, ha um regresso a explicar", err)
	}
	if cfg.SovereignReadOIDC == nil {
		t.Fatal("cenario nao montado")
	}
	if cfg.SovereignReadOIDC.MaxAge != 87600*time.Hour {
		t.Fatalf("o override deixou de ser respeitado tal como escrito: veio %v", cfg.SovereignReadOIDC.MaxAge)
	}
}
