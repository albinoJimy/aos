package oidc

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// OS TECTOS TEMPORAIS SÃO EXERCIDOS — E OS TESTES NÃO DERIVAM OS PARÂMETROS DA CONSTANTE.
//
// Achado da varredura adversarial de 2026-08-21, reproduzido em cópia isolada:
//
//	leeway por omissão (60 s)  →  299 s  ...  TUDO VERDE
//
// O mecanismo da vacuidade tem nome: AUTO-REFERÊNCIA DE PARÂMETRO. Um teste que constrói o seu
// relógio a partir da própria constante que quer vigiar («adianta o relógio maxAge + 10 min»)
// passa para QUALQUER valor dela, incluindo dez anos. E `constante > 0` não é a propriedade: um
// tecto de dez anos é maior que zero e é indistinguível de não haver tecto nenhum.
//
// A propriedade é «LIMITADO E PEQUENO». Por isso cada tecto ganha aqui DUAS pernas independentes:
//
//	(A) VALOR — um limite superior ABSOLUTO, com a razão escrita. Mata a mutação «dez anos».
//	(B) COMPORTAMENTO — desvios ABSOLUTOS, que não derivam da constante. Prova que o valor está
//	    CABLADO e não apenas declarado, e enquadra-o por baixo (senão «recusa tudo» passava).
//
// E O QUE A VARREDURA NÃO TINHA DITO: o leeway SOMA-SE ao MaxAge (`now > iat + maxAge + leeway`,
// ver checkTime). A janela efectiva de replay é `MaxAge + Leeway`. Os dois «tectos» são UM só, e
// afinar o que parece ser tolerância de relógio afina a janela de roubo. É a perna (C).
// ---------------------------------------------------------------------------------------------

// tectoLeewayDefensavel é o limite superior ARGUMENTADO do leeway por omissão, e é uma constante
// DO TESTE — nunca derivada de [defaultLeeway], que é o que se quer vigiar.
//
// Acima disto, o valor deixa de ser «desvio entre relógios com NTP» e passa a ser uma extensão
// silenciosa da janela em que um ID-token roubado ainda serve. Quem precisar de mais tem de vir
// aqui, mudar este número e escrever porquê — que é exactamente o custo que se pretende impor.
const tectoLeewayDefensavel = 2 * time.Minute

// (A) VALOR.
func TestOLeewayPorOmissaoTemTectoDefensavel(t *testing.T) {
	if defaultLeeway <= 0 {
		t.Fatal("defaultLeeway <= 0 — sem tolerância, qualquer desvio de relógio recusa tokens válidos")
	}
	if defaultLeeway > tectoLeewayDefensavel {
		t.Fatalf("defaultLeeway = %v, acima do tecto defensável de %v. Isto NAO e uma tolerancia de "+
			"relogio: o leeway soma-se ao MaxAge, portanto este valor ALARGA a janela em que um "+
			"ID-token roubado continua a servir", defaultLeeway, tectoLeewayDefensavel)
	}
}

// (B) COMPORTAMENTO, com desvios ABSOLUTOS.
//
// Enquadra o valor pelos dois lados: 30 s de expirado passa, 90 s não. Nenhum dos dois números
// deriva de [defaultLeeway] — é essa a diferença face ao teste que a varredura apanhou.
func TestOLeewayPorOmissaoEstaCABLADO(t *testing.T) {
	idp := newTestIDP(t)
	// Leeway NÃO dado ⇒ vale o valor por omissão. É o caminho que a mutação atacava.
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks"})

	// DENTRO: expirado há 30 s. Sem tolerância nenhuma isto seria recusado.
	c := idp.validClaims()
	c["exp"] = fixedNow().Unix() - 30
	if _, err := v.Validate(context.Background(), idp.sign(t, "RS256", rsaKid, c, nil)); err != nil {
		t.Fatalf("expirado ha 30s devia passar dentro da tolerancia por omissao: %v", err)
	}

	// FORA: expirado há 90 s. Um leeway inflacionado (o 299 s da varredura) aceitaria isto.
	c = idp.validClaims()
	c["exp"] = fixedNow().Unix() - 90
	if _, err := v.Validate(context.Background(), idp.sign(t, "RS256", rsaKid, c, nil)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expirado ha 90s foi ACEITE (erro=%v) — a tolerancia por omissao esta inflacionada "+
			"e um token morto continua a servir", err)
	}
}

// (C) A JANELA EFECTIVA É A SOMA, e fica ASSERIDA em vez de implícita.
//
// Este é o ramo que torna o composto visível: com MaxAge de 2 min e a tolerância por omissão, um
// `iat` de 2m30s ainda passa — a janela real é 3 min, não 2. Quem ler o MaxAge sozinho lê metade
// da verdade.
func TestAJanelaEfectivaEMaxAgeMAISLeeway(t *testing.T) {
	idp := newTestIDP(t)
	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks", MaxAge: 2 * time.Minute})

	// 2m30s: PARA LÁ do MaxAge e AQUÉM de MaxAge+leeway. Passa — e é o ponto.
	c := idp.validClaims()
	c["iat"] = fixedNow().Unix() - 150
	if _, err := v.Validate(context.Background(), idp.sign(t, "RS256", rsaKid, c, nil)); err != nil {
		t.Fatalf("iat de 2m30s com MaxAge de 2min devia PASSAR (a janela e MaxAge+leeway=3min) e "+
			"veio %v — se este ramo cair, a soma deixou de acontecer e o comentario da Config mente", err)
	}

	// 3m30s: para lá da soma. Recusa.
	c = idp.validClaims()
	c["iat"] = fixedNow().Unix() - 210
	if _, err := v.Validate(context.Background(), idp.sign(t, "RS256", rsaKid, c, nil)); !errors.Is(err, ErrTokenTooOld) {
		t.Fatalf("iat de 3m30s foi ACEITE com MaxAge de 2min (erro=%v)", err)
	}
}
