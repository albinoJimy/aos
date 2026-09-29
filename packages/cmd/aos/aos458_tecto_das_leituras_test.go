package main

// AOS-458 — AS LEITURAS PASSARAM A TER TECTO DE TAXA, E PORQUE ISSO FALTAVA.
//
// # O DEFEITO, MEDIDO
//
// Sete rotas do `planoDados` chamam `readGovernance.authorize` e **não tinham tecto de taxa
// nenhum**: `GET /runs/{id}`, `GET /runs/{id}/trajectory`, `GET /runs/{id}/reconstruct`,
// `GET /tools`, `GET /plans/{id}`, `POST /plans/claim`, `POST /plans/outcome`. Em produção
// (`AOS_MODE=production` EXIGE a credencial forte OIDC) esse `authorize` verifica um JWS —
// RS256 ≈ 42 µs, ES256 ≈ 91 µs, mais caro do que a `ed25519.Verify` de 52 µs do `POST /runs`.
//
// Medido antes desta correcção, com o balde de dados E o de controlo a 1 token e o relógio parado:
//
//	GET /runs/{id}             200 pedidos ⇒ 200 verificações, 0 × 429
//	GET /runs/{id}/trajectory  200 pedidos ⇒ 200 verificações, 0 × 429
//	GET /tools                 200 pedidos ⇒ 200 verificações, 0 × 429
//	POST /runs                 200 pedidos ⇒   1 verificação, 199 × 429
//
// # DE ONDE VEIO ESTE TICKET, e não foi de um cliente a queixar-se
//
// O AOS-456b foi fechado como «não se faz» com o argumento de que **nenhuma** porta do nó permitia
// forçar verificação criptográfica sem tecto. O argumento era FALSO, e a refutação já estava
// escrita no banner do próprio nó (`ingress_env.go`): «NÃO cobre as LEITURAS. `GET /runs/{id}` não
// é limitado por taxa nenhuma». Uma revisão adversarial independente mediu-o.
//
// A correcção NÃO é a inversão que o 456b pedia — é a oposta e é mais simples: o balde entra no
// INVÓLUCRO da rota, ANTES do `authorize`, sem precisar de saber quem chama.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// TestAOS458AsLeiturasTemTectoDeTaxa — o critério. Cada rota de leitura, com o balde a 1 token:
// o 1.º pedido atravessa o invólucro e o 2.º leva 429 SEM a rota correr.
func TestAOS458AsLeiturasTemTectoDeTaxa(t *testing.T) {
	rotas := []struct{ metodo, alvo string }{
		{http.MethodGet, "/runs/qualquer"},
		{http.MethodGet, "/runs/qualquer/trajectory"},
		{http.MethodGet, "/runs/qualquer/reconstruct"},
		{http.MethodGet, "/tools"},
		{http.MethodGet, "/plans/qualquer"},
		{http.MethodPost, "/plans/claim"},
		{http.MethodPost, "/plans/outcome"},
	}
	for _, rt := range rotas {
		t.Run(rt.metodo+" "+rt.alvo, func(t *testing.T) {
			node := newTwoRegionGovNode(t, &countingModel{})
			svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
			if err != nil {
				t.Fatalf("NewNodeService: %v", err)
			}
			t.Cleanup(func() { _ = svc.Shutdown(t.Context()) })
			regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
			// UM token de leitura, relógio parado: o 2.º pedido tem de ser recusado.
			h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, node.WORM),
				WithReadRateLimit(1000, 1), WithAPIClock(aos277Clock()))
			if err != nil {
				t.Fatalf("NewAPIHandler: %v", err)
			}

			pedir := func() int {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(rt.metodo, rt.alvo, nil)
				for k, v := range euReaderHeaders() {
					req.Header.Set(k, v)
				}
				h.ServeHTTP(rec, req)
				return rec.Code
			}

			// (1) O 1.º pedido consome o token e a rota CORRE — o código é o dela, qualquer que seja.
			if c := pedir(); c == http.StatusTooManyRequests {
				t.Fatalf("o 1o pedido, DENTRO do burst, levou 429 — o teste nao mede o tecto")
			}
			// (2) O 2.º leva 429, e é o tecto que faltava.
			if c := pedir(); c != http.StatusTooManyRequests {
				t.Fatalf("o 2o pedido devia dar 429 e deu %d — esta rota de LEITURA nao tem tecto de "+
					"taxa, logo um chamador pode forcar verificacao de JWS (RS256 ~42 us, ES256 ~91 us) "+
					"sem limite. E o defeito que o AOS-458 fecha, e que a analise do AOS-456b afirmou "+
					"nao existir em porta nenhuma", c)
			}
		})
	}
}

// TestAOS458OTectoPRECEDEAVerificacao — não basta existir: tem de correr ANTES da criptografia,
// senão limita a taxa e não o vector. O sensor é um verificador de credencial que CONTA as chamadas.
//
// A CREDENCIAL FORTE COMPOSTA é a diferença que cegou os gates do AOS-456b: eles usavam
// `WithReadSovereignty(regions, worm)` ⇒ `cred == nil` ⇒ via legada por HEADERS, sem criptografia
// nenhuma. Um gate que nunca exercita a verificação não pode medir o tecto dela.
func TestAOS458OTectoPRECEDEAVerificacao(t *testing.T) {
	h, _, worm := noParaTeste(t)
	contada := &credencialContada{}
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, contada, worm, aos277Clock())
	// UM token de leitura, relógio parado. O helper não compõe baldes — é precisamente o caso que a
	// guarda nil de `allow()` cobre —, pelo que aqui compõe-se explicitamente o que se mede.
	h.readBucket = newTokenBucket(1, 1000, aos277Clock())

	envolvido := h.barreirasDe(planoDados, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.readGov.authorize(r); !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	recusados := 0
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		envolvido(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/runs/r-%02d", i), nil))
		if rec.Code == http.StatusTooManyRequests {
			recusados++
		}
	}
	if n := contada.chamadas(); n != 1 {
		t.Fatalf("20 pedidos com o balde de leitura a 1 token produziram %d verificacoes, esperava 1 — "+
			"o tecto NAO precede a criptografia, logo limita a taxa e nao o vector de CPU. E onde o "+
			"balde tem de estar: no INVOLUCRO da rota (planos.go), nao no corpo do handler", n)
	}
	if recusados != 19 {
		t.Fatalf("esperava 19 recusas, vieram %d — o sensor nao esta a medir o que diz", recusados)
	}
}

// TestAOS458OsTresBaldesMORDEMNumHandlerDoConstrutor — fecha o fail-open que a guarda nil de
// [tokenBucket.allow] abre. Um balde nil não limita; o que garante que isso nunca acontece em
// produção é o construtor compor sempre os três.
//
// MEDE O EFEITO e não os campos: `NewAPIHandler` devolve o mux, e um sensor que lesse a struct
// provaria menos. Cada balde é posto a 1 token, com os outros dois LARGOS, e exige-se que o segundo
// pedido da sua classe leve 429.
func TestAOS458OsTresBaldesMORDEMNumHandlerDoConstrutor(t *testing.T) {
	casos := []struct {
		nome, metodo, alvo, porque string
		opcao                      APIOption
	}{
		{"readBucket (plano de DADOS)", http.MethodGet, "/tools",
			"as LEITURAS voltariam a poder forcar verificacao de JWS sem tecto (AOS-458)",
			WithReadRateLimit(1000, 1)},
		{"bucket (SUBMISSAO)", http.MethodPost, "/runs",
			"POST /runs ficaria sem admission de taxa",
			WithRateLimit(1000, 1)},
		{"ctrlBucket (CONTROLO)", http.MethodPost, "/runs/x/pause",
			"/steer,/pause,/approve,/resume ficariam sem admission",
			WithControlRateLimit(1000, 1)},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			node := newTwoRegionGovNode(t, &countingModel{})
			svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
			if err != nil {
				t.Fatalf("NewNodeService: %v", err)
			}
			t.Cleanup(func() { _ = svc.Shutdown(t.Context()) })
			regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
			// Os OUTROS dois baldes ficam LARGOS: o 429 que se mede tem de vir do que se aponta.
			h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, node.WORM),
				WithAPIClock(aos277Clock()), WithReadRateLimit(1000, 4096), WithRateLimit(1000, 4096),
				WithControlRateLimit(1000, 4096), c.opcao)
			if err != nil {
				t.Fatalf("NewAPIHandler: %v", err)
			}
			pedir := func() int {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(c.metodo, c.alvo, nil)
				for k, v := range euReaderHeaders() {
					req.Header.Set(k, v)
				}
				h.ServeHTTP(rec, req)
				return rec.Code
			}
			if p := pedir(); p == http.StatusTooManyRequests {
				t.Fatalf("o 1o pedido, dentro do burst, levou 429 — o teste nao mede este balde")
			}
			if p := pedir(); p != http.StatusTooManyRequests {
				t.Fatalf("o 2o pedido devia dar 429 e deu %d: o construtor nao compos %s, e a guarda "+
					"nil de allow() torna isso um fail-OPEN silencioso em vez de um panic. %s",
					p, c.nome, c.porque)
			}
		})
	}
}

// credencialContada é um [readCredentialVerifier] que CONTA as verificações. Nega sempre: o que se
// mede é quantas vezes o nó chega a fazer o trabalho, não o desfecho.
type credencialContada struct{ n int }

func (c *credencialContada) verify(_ context.Context, _ *http.Request) (string, string, error) {
	c.n++
	return "", "", ErrNoReadCredential
}
func (c *credencialContada) chamadas() int { return c.n }
