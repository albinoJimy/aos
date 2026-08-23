package oidc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// A JANELA DO ANTI-REPLAY TEM DE COBRIR A JANELA DE ACEITACAO.
//
// [Verifier.Validate] aceita enquanto `now <= exp + leeway`, mas o registo anti-replay era
// despejado em `exp`. Entre os dois havia `leeway` segundos — 60 s por omissao — em que o
// token AINDA passava e o par (iss,jti) ja nao estava registado: o mesmo token voltava a ser
// aceite. E o fim da vida do token e exactamente quando um token capturado e reapresentado.
//
// PORQUE `exp` CURTO E NAO O `validClaims` NORMAL. A janela de aceitacao acaba no MINIMO entre
// `exp+leeway` e `iat+maxAge+leeway`. O `validClaims` traz `exp = now+3600`; com um MaxAge
// definido mandaria o MaxAge e o buraco NAO apareceria — o teste passaria sem exercitar nada.
// Com MaxAge por omissao (0 ⇒ a verificacao de idade esta desligada) quem manda e o `exp`, que
// e a forma do realm entregue (exp = iat+300 com MaxAge de 5 min: as duas fronteiras coincidem).
func TestReplay_CobreALeewayDepoisDoExp(t *testing.T) {
	casos := []struct {
		nome    string
		avancar func(exp int64) int64
	}{
		// O empate exacto: `e <= now` apagava a entrada enquanto `now.After(exp)` ainda dizia
		// "valido" — o enviesamento de resolucao-de-segundo a cair para o lado errado.
		{"empate no proprio segundo do exp", func(exp int64) int64 { return exp }},
		{"a meio da leeway", func(exp int64) int64 { return exp + 30 }},
		// A ultima fronteira ainda aceite: `now == exp+leeway` NAO e `After`, logo passa.
		{"no ultimo segundo aceite", func(exp int64) int64 { return exp + int64(defaultLeeway.Seconds()) }},
	}

	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			idp := newTestIDP(t)
			t0 := fixedNow()

			var agora atomic.Int64
			agora.Store(t0.Unix())
			relogio := func() time.Time { return time.Unix(agora.Load(), 0).UTC() }

			exp := t0.Add(60 * time.Second).Unix()
			v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks", Clock: relogio})

			alvo := idp.validClaims()
			alvo["exp"] = exp
			alvo["jti"] = "jti-alvo"
			tokAlvo := idp.sign(t, "RS256", rsaKid, alvo, nil)

			if _, err := v.Validate(context.Background(), tokAlvo); err != nil {
				t.Fatalf("1a validacao (em t0) recusada: %v", err)
			}

			agora.Store(tc.avancar(exp))

			// CONTROLO ANTI-VACUIDADE. Sem isto o teste nao mede nada: se o token ja nao fosse
			// aceitavel neste instante, a recusa abaixo seria "expirou" e nao "replay", e um
			// `errors.Is(err, ErrTokenReplayed)` a falhar nao distinguiria as duas causas.
			// Um jti DIFERENTE com o MESMO exp prova que a janela de aceitacao ainda esta aberta.
			ctl := idp.validClaims()
			ctl["exp"] = exp
			ctl["jti"] = "jti-controlo"
			tokCtl := idp.sign(t, "RS256", rsaKid, ctl, nil)
			if _, err := v.Validate(context.Background(), tokCtl); err != nil {
				t.Fatalf("CONTROLO: um jti NOVO devia ser aceite neste instante, veio %v — "+
					"a janela de aceitacao ja fechou e a asercao seguinte nao mediria o replay", err)
			}

			// O ALVO: o MESMO (iss,jti), dentro da janela em que ainda e aceite, e REPLAY.
			if _, err := v.Validate(context.Background(), tokAlvo); !errors.Is(err, ErrTokenReplayed) {
				t.Fatalf("o mesmo jti devia dar ErrTokenReplayed, veio %v", err)
			}
		})
	}
}

// FORA da janela de aceitacao a entrada TEM de ser despejada: reter para sempre seria uma fuga
// de memoria num mapa sem tecto. Este teste MEDE O MAPA, e a razao e uma mutacao que nao caiu.
//
// A primeira versao deste teste afirmava que, passada a janela, "quem recusa e a expiracao" — e
// passava com o despejo DESLIGADO. Passava porque [Verifier.Validate] recusa por expiracao ANTES
// de chegar ao checkReplay: o token nunca la ia, e a asercao nao dizia nada sobre o mapa. Uma
// mutacao "nunca despejar" nao a fazia cair, que e a definicao de teste vacuoso.
//
// Observar `len(v.seen)` e o unico sitio deste ficheiro que toca o interior do Verifier. E
// deliberado: a propriedade E sobre o interior — nenhum comportamento observavel de fora
// distingue "despejou" de "nao despejou", porque a recusa por expiracao mascara as duas.
func TestReplay_DespejaOQueJaSaiuDaJanela(t *testing.T) {
	idp := newTestIDP(t)
	t0 := fixedNow()

	var agora atomic.Int64
	agora.Store(t0.Unix())
	relogio := func() time.Time { return time.Unix(agora.Load(), 0).UTC() }

	v := idp.verifier(t, Config{JWKSURI: idp.issuer() + "/jwks", Clock: relogio})

	expCurto := t0.Add(60 * time.Second).Unix()
	for _, jti := range []string{"a", "b", "c"} {
		c := idp.validClaims()
		c["exp"] = expCurto
		c["jti"] = jti
		if _, err := v.Validate(context.Background(), idp.sign(t, "RS256", rsaKid, c, nil)); err != nil {
			t.Fatalf("validacao de %q recusada: %v", jti, err)
		}
	}
	if got := len(v.seen); got != 3 {
		t.Fatalf("PRECONDICAO: len(seen)=%d, quero 3 — o registo nem sequer aconteceu", got)
	}

	// UM SEGUNDO depois de a janela dos tres fechar. Um token NOVO (com exp proprio, para ser
	// aceite aqui) dispara a eviction preguicosa, que so corre dentro do checkReplay.
	agora.Store(expCurto + int64(defaultLeeway.Seconds()) + 1)
	novo := idp.validClaims()
	novo["exp"] = agora.Load() + 60
	novo["jti"] = "novo"
	if _, err := v.Validate(context.Background(), idp.sign(t, "RS256", rsaKid, novo, nil)); err != nil {
		t.Fatalf("o token NOVO devia ser aceite (e e ele que dispara o despejo), veio %v", err)
	}

	if got := len(v.seen); got != 1 {
		t.Fatalf("len(seen)=%d, quero 1 — os tres fora da janela deviam ter sido despejados", got)
	}
}
