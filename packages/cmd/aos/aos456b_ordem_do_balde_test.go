package main

// AOS-456b — A ORDEM «BALDE ANTES DE VERIFICAÇÃO» É UMA PROPRIEDADE DE SEGURANÇA, E ESTE FICHEIRO
// PRENDE-A.
//
// # PORQUE EXISTE
//
// O AOS-456b pedia justiça em TAXA por-chamador. Atribuir taxa a um chamador exige saber QUEM ele é
// **antes** de decidir sobre o balde — isto é, mover a verificação de identidade para antes do
// `bucket.allow()`. Essa inversão foi **decidida como NÃO SE FAZ** (2026-09-28), e a razão é medida,
// não estética:
//
//	HOJE   `handleSubmit` consome o balde global na PRIMEIRA linha (api.go, `h.bucket.allow()`) e a
//	       primeira `ed25519.Verify` acontece ~150 linhas depois, dentro do gate soberano. Logo o
//	       balde LIMITA quantas verificações um chamador pode forçar: com os defaults, 64/s.
//	       Um atacante não autenticado impõe no máximo 64 × 52,7 µs ≈ 3,4 ms/s de CPU — 0,34% de
//	       um core.
//
//	       MEDIDO NESTE CONTENTOR por [BenchmarkAOS456BCustoDaVerificacao], e os números são os
//	       daqui e não os do desenho: verificar 52,7 µs (idêntico para assinatura válida e
//	       inválida — tempo constante), recusar sem verificar 30,2 ns, **rácio 1742x**. O desenho
//	       mediu 59,9 µs / 41 ns = 1461x noutra máquina; a ordem de grandeza confirma-se e os
//	       absolutos não, que é razão suficiente para não reciclar o número de outra medição.
//
//	COM A INVERSÃO que o 456b exige, esse limitador desaparece: cada pedido, válido ou não, paga a
//	       verificação antes de o balde poder recusá-lo. O «orçamento de verificação» que o desenho
//	       propunha existiria para fechar um buraco que a própria mudança abriu.
//
// A justiça em taxa POR-ORIGEM já existe onde pertence — no `edge`: `deploy/server/nginx.conf`
// declara `limit_req_zone $binary_remote_addr rate=16r/s` com burst 32, e está em produção. O que
// fica por cobrir é taxa por-PRINCIPAL (dois chamadores atrás do mesmo NAT partilham a quota), e
// isso está declarado como não-feito no AOS-456b com esta razão.
//
// # O QUE ESTE TESTE IMPEDE
//
// Que alguém inverta a ordem — por causa do 456b ou por refactor — e o repositório não dê sinal. É
// um gate, não um teste de unidade: a asserção é sobre a ARQUITECTURA do caminho de ingresso.
//
// O sensor é a métrica REAL (`aos_ingress_credential_denials_total`, exposta em `/metrics`), que só
// sobe quando uma credencial é efectivamente VERIFICADA e recusada. Se a verificação passar a
// correr antes do balde, um pedido recusado pelo balde passa a fazê-la subir — e este teste
// avermelha com a razão escrita.

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
)

// recusasDeCredencial lê o contador REAL de `/metrics`. Falha o teste se a série não existir — uma
// leitura que devolvesse 0 por ausência tornaria todas as asserções deste ficheiro vacuosas.
func recusasDeCredencial(t *testing.T, h http.Handler) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devolveu %d", rec.Code)
	}
	re := regexp.MustCompile(`(?m)^aos_ingress_credential_denials_total\s+(\d+)\s*$`)
	m := re.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("a serie aos_ingress_credential_denials_total NAO existe em /metrics — sem ela este "+
			"gate nao mede nada. Se a metrica foi renomeada, actualize ESTE teste em vez de o apagar:\n%s",
			rec.Body.String())
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("contador ilegivel %q: %v", m[1], err)
	}
	return n
}

// TestAOS456BOBaldeCorreANTESDaVerificacaoCriptografica — o gate.
func TestAOS456BOBaldeCorreANTESDaVerificacaoCriptografica(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(t.Context()) })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	// BALDE DE UM TOKEN, RELÓGIO PARADO: o primeiro pedido passa a admissão, o segundo não. É o que
	// torna a diferença observável num só par de pedidos.
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, node.WORM),
		WithRateLimit(1000, 1), WithAPIClock(aos277Clock()))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}

	base := recusasDeCredencial(t, h)

	// (1) PRIMEIRO pedido: o balde tem 1 token, logo a admissão passa e a credencial CHEGA a ser
	// verificada. Recusada ⇒ 403 e o contador sobe. É o controlo que prova que o sensor funciona.
	if rec := postReq(h, "/runs", submitRequest{RunID: "456b-um", Credential: "credencial-invalida"},
		euReaderHeaders()); rec.Code != http.StatusForbidden {
		t.Fatalf("1o pedido (dentro do burst, credencial invalida) devia dar 403, veio %d: %s",
			rec.Code, rec.Body.String())
	}
	if n := recusasDeCredencial(t, h); n != base+1 {
		t.Fatalf("o contador de recusas de credencial devia ter subido para %d, esta em %d — o SENSOR "+
			"deste gate nao funciona, e sem ele a assercao (2) e vacuosa", base+1, n)
	}

	// (2) SEGUNDO pedido: o balde está VAZIO (relógio parado, sem reabastecimento). A mesma
	// credencial inválida TEM de levar 429 — e o contador NÃO pode subir, porque a verificação nunca
	// deve acontecer.
	if rec := postReq(h, "/runs", submitRequest{RunID: "456b-dois", Credential: "credencial-invalida"},
		euReaderHeaders()); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("2o pedido (balde vazio) devia dar 429, veio %d — a ADMISSAO deixou de ser a primeira "+
			"coisa que corre em handleSubmit: %s", rec.Code, rec.Body.String())
	}
	if n := recusasDeCredencial(t, h); n != base+1 {
		t.Fatalf("O BALDE DEIXOU DE PROTEGER A VERIFICACAO: um pedido RECUSADO pelo rate-limit fez o "+
			"contador de recusas de credencial subir para %d (esperava %d), logo a `ed25519.Verify` "+
			"CORREU num pedido que nunca foi admitido.\n\n"+
			"Isto inverte a ordem que o AOS-456b decidiu NAO inverter. Hoje o balde limita as "+
			"verificacoes a AOS_INGRESS_RATE/s (64 por omissao ⇒ ~0,34%% de um core); sem ele, cada "+
			"pedido nao autenticado paga uma verificacao de ~53 us (medida aqui) e a CPU e partilhada "+
			"por todos.\n\n"+
			"Se a inversao for DELIBERADA, ela precisa do orcamento de verificacao que o AOS-456b "+
			"descreve E de reabrir a decisao no ticket — nao de apagar este teste.", n, base+1)
	}

	// (3) NÃO-VACUOSIDADE do relógio parado: com o balde reposto, a verificação volta a acontecer.
	// Sem isto, um balde que recusasse SEMPRE passaria (1) por acaso e (2) por vacuidade.
	h2, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, node.WORM),
		WithRateLimit(1000, 1), WithAPIClock(aos277Clock()))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	baseDepois := recusasDeCredencial(t, h2)
	if rec := postReq(h2, "/runs", submitRequest{RunID: "456b-tres", Credential: "credencial-invalida"},
		euReaderHeaders()); rec.Code != http.StatusForbidden {
		t.Fatalf("com o balde reposto o pedido devia voltar a ser VERIFICADO e dar 403, veio %d", rec.Code)
	}
	if n := recusasDeCredencial(t, h2); n != baseDepois+1 {
		t.Fatalf("com o balde reposto o contador devia subir para %d, esta em %d", baseDepois+1, n)
	}
}

// BenchmarkAOS456BCustoDaVerificacao mede, NESTE contentor, o número de que a decisão do AOS-456b
// depende: o custo de uma `ed25519.Verify`, que é o trabalho que o balde hoje impede um chamador
// não autenticado de forçar.
//
// A decisão não deve citar um número medido noutra máquina: o rácio entre o custo de recusar e o
// custo de verificar é o argumento inteiro, e é reproduzível aqui.
func BenchmarkAOS456BCustoDaVerificacao(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("um payload de credencial de tamanho tipico para o verificador do no")
	assinatura := ed25519.Sign(priv, msg)
	adulterada := append([]byte(nil), assinatura...)
	adulterada[0] ^= 0xff

	b.Run("valida", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if !ed25519.Verify(pub, msg, assinatura) {
				b.Fatal("a assinatura valida devia verificar")
			}
		}
	})
	// O CUSTO É O MESMO para uma assinatura inválida — `ed25519.Verify` é de tempo constante. É por
	// isso que um atacante não precisa de credenciais válidas para impor o custo: qualquer lixo
	// bem-formado serve, e é isso que o balde hoje limita.
	b.Run("invalida", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if ed25519.Verify(pub, msg, adulterada) {
				b.Fatal("a assinatura adulterada NAO devia verificar")
			}
		}
	})
	// O CONTRASTE: recusar sem verificar. É o que o balde faz hoje ao pedido não admitido.
	b.Run("recusar-sem-verificar", func(b *testing.B) {
		bucket := newTokenBucket(1000, 1, aos277Clock())
		for i := 0; i < b.N; i++ {
			_ = bucket.allow()
		}
	})
	b.Log("o racio entre 'invalida' e 'recusar-sem-verificar' e o argumento do AOS-456b: e quanto " +
		"trabalho a inversao da ordem daria de graca a um chamador nao autenticado")
}
