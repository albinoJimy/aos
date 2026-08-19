package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// O TOKEN QUE FUNCIONA E NÃO SE DEIXA MEDIR.
//
// Encontrado em produção a 2026-08-19: o `/readyz` estava VERMELHO e o log dizia, a cada minuto,
// que «o embrulho da DEK e o crypto-shred/DSAR ficam inoperantes». Nenhuma das duas coisas era
// verdade — o token listava chaves Transit, criava e lia KEKs.
//
// A causa: `provision-identity.sh` emite o token com `no_default_policy: true` e uma política só
// sobre `transit/*`. É a política `default` que concede `auth/token/lookup-self`. Ou seja, o
// provisionamento deste projecto produz um token que a sonda deste projecto não sabe interrogar —
// e a sonda lia o 403 como «token morto».
//
// O que estes testes fixam: um 403 no canal de MEDIÇÃO nunca decide sozinho. Quem decide é a
// prova da PROPRIEDADE — consigo ainda falar com o Transit?
// ---------------------------------------------------------------------------

// vaultOpaco é um Vault de teste que serve o Transit e RECUSA `auth/token/*`, exactamente como um
// token least-privilege com `no_default_policy` faz.
type vaultOpaco struct {
	lookups   atomic.Int32
	listas    atomic.Int32
	negarList bool // simula a implantação que também não concede o `list`
	semChaves bool // motor Transit vazio ⇒ 404 no list
}

func (f *vaultOpaco) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/auth/token/"):
		f.lookups.Add(1)
		w.WriteHeader(http.StatusForbidden) // sem política `default`
	case r.URL.Path == "/v1/transit/keys":
		f.listas.Add(1)
		if f.negarList {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if f.semChaves {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": []string{"aos-kek-x"}}})
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func vaultDeTeste(h http.Handler) (*vaultKeyVault, func()) {
	srv := httptest.NewServer(h)
	v := newVaultKeyVault(srv.URL, "transit", "tok-de-teste")
	return v, srv.Close
}

// TestTokenOpacoNaoEDadoComoMorto é o teste central: 403 na medição + Transit a responder ⇒ o nó
// SERVE.
//
// O controlo está no teste seguinte: com o Transit também a recusar, o mesmo caminho tem de dar
// NÃO-SERVE. Sem esse par, este verde seria compatível com «a sonda passou a aceitar tudo» — que
// é um defeito bem pior do que o que se está a corrigir.
func TestTokenOpacoNaoEDadoComoMorto(t *testing.T) {
	fake := &vaultOpaco{}
	v, fechar := vaultDeTeste(fake)
	defer fechar()

	if err := v.refreshToken(context.Background()); err != nil {
		t.Fatalf("um token que FUNCIONA foi dado como falha: %v", err)
	}
	if err := v.tokenVerdict(time.Now()); err != nil {
		t.Errorf("o veredicto poe o /readyz VERMELHO sobre um token que serve: %v", err)
	}
	if fake.listas.Load() == 0 {
		t.Error("a prova de capacidade NAO chegou a correr — o teste estaria a medir outra coisa")
	}
	if !v.tokenOpaco {
		t.Error("o estado OPACO nao ficou registado — o banner e as metricas nao saberiam distingui-lo de um token periodico saudavel")
	}
}

// TestTokenMortoContinuaAPorOReadyzVermelho é o CONTROLO, e é o que impede a correcção de virar
// um fail-open: quando NADA responde, o veredicto tem de ser negativo.
func TestTokenMortoContinuaAPorOReadyzVermelho(t *testing.T) {
	v, fechar := vaultDeTeste(&vaultOpaco{negarList: true})
	defer fechar()

	if err := v.refreshToken(context.Background()); err == nil {
		t.Fatal("um token que nao consegue NEM medir-se NEM trabalhar passou por saudavel")
	}
	if err := v.tokenVerdict(time.Now()); err == nil {
		t.Error("o /readyz ficaria VERDE com a custodia da KEK inalcancavel")
	}
	if v.tokenOpaco {
		t.Error("marcou OPACO um token que nao provou capacidade nenhuma")
	}
}

// TestTransitVazioNaoEFalha — um motor Transit ainda sem chaves devolve 404 ao `list`. Tratá-lo
// como recusa faria o PRIMEIRO arranque de um nó novo nascer vermelho, que é o oposto do que esta
// prova existe para evitar.
func TestTransitVazioNaoEFalha(t *testing.T) {
	v, fechar := vaultDeTeste(&vaultOpaco{semChaves: true})
	defer fechar()

	if err := v.refreshToken(context.Background()); err != nil {
		t.Fatalf("motor Transit VAZIO tratado como token morto: %v", err)
	}
	if err := v.tokenVerdict(time.Now()); err != nil {
		t.Errorf("veredicto negativo num no acabado de provisionar: %v", err)
	}
}

// TestAvisoDeOpacidadeSaiUmaVez — o alarme repetido a cada tick foi o que fez ninguém o ler.
//
// Em produção a mesma linha saiu 43 vezes antes de alguém reparar. Um aviso a essa cadência
// deixa de ser aviso e passa a ser papel de parede.
func TestAvisoDeOpacidadeSaiUmaVez(t *testing.T) {
	v, fechar := vaultDeTeste(&vaultOpaco{})
	defer fechar()

	for i := 0; i < 5; i++ {
		if err := v.refreshToken(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	primeiro := v.avisoDeOpacidade()
	if primeiro == "" {
		t.Fatal("nao houve aviso nenhum — a postura opaca ficaria muda")
	}
	// O aviso tem de NOMEAR a consequência real, senão é uma linha de log sem valor de decisão.
	for _, exigido := range []string{"renew-self", "MORRE", "lookup-self"} {
		if !strings.Contains(primeiro, exigido) {
			t.Errorf("o aviso nao menciona %q — quem o ler nao sabe o que fazer: %s", exigido, primeiro)
		}
	}
	for i := 0; i < 3; i++ {
		if repetido := v.avisoDeOpacidade(); repetido != "" {
			t.Errorf("o aviso repetiu-se (%da vez) — volta a ser papel de parede", i+2)
		}
	}
}

// TestCanalReabertoLevantaAOpacidade — corrigida a política, o `lookup-self` volta a responder e
// o nó tem de sair do estado opaco.
//
// Sem isto, um nó que já viu um 403 ficaria marcado como opaco para sempre, e o operador que
// CORRIGIU a política continuaria a ver a postura degradada — a aprender que arranjar não muda
// nada, que é a maneira mais eficaz de ensinar alguém a ignorar um indicador.
func TestCanalReabertoLevantaAOpacidade(t *testing.T) {
	var abrir atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/auth/token/"):
			if !abrir.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"ttl": 3600, "renewable": true},
			})
		case r.URL.Path == "/v1/transit/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": []string{"k"}}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	v := newVaultKeyVault(srv.URL, "transit", "tok")

	if err := v.refreshToken(context.Background()); err != nil || !v.tokenOpaco {
		t.Fatalf("preparacao: queria estado opaco, veio err=%v opaco=%v", err, v.tokenOpaco)
	}
	abrir.Store(true)
	if err := v.refreshToken(context.Background()); err != nil {
		t.Fatalf("com o canal reaberto: %v", err)
	}
	if v.tokenOpaco {
		t.Error("continuou OPACO depois de a politica ser corrigida — o operador que arranjou nao veria diferenca")
	}
	if v.tokenTTL != time.Hour {
		t.Errorf("o TTL medido nao foi adoptado: %v", v.tokenTTL)
	}
}
