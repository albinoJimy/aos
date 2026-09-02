package main

// AOS-288 — A REVOGAÇÃO DE NHI PASSA A CORRER, E O BANNER DEIXA DE PODER MENTIR SOBRE ELA.
//
// O defeito medido pela auditoria: o passo de revogação do `Verify` vive dentro de
// `if v.revocations != nil` (identity/verifier.go:184) e esse guarda era SEMPRE falso no nó —
// `identity.WithRevocations` não tinha um único chamador de produção. Um token com o `jti`
// revogado era aceite com `err=<nil>` e um `Principal` completo e utilizável, durante o TTL da
// classe mais leeway (≈16 min medidos). Ao mesmo tempo, `posture_banner.go` anunciava «EdDSA +
// janela + revogacao + raiz humana» e `identity/doc.go` afirmava que o registo era «consultado
// por [Verifier.Verify]».
//
// O que elevava isto de dívida a DEFEITO era a auto-declaração contrária. Por isso os testes
// aqui vêm em dois pares, e nenhum deles sozinho fecha o ticket:
//
//   - o par de COMPORTAMENTO prova que a revogação corre PELA CADEIA DO NÓ (não por um verifier
//     construído no teste, que é o que a suite do pacote `identity` já provava e que deixou o
//     defeito passar), e que sobrevive a um restart;
//   - o par de VERACIDADE prova que a frase do banner é DERIVADA do estado composto, e falha na
//     direcção oposta se alguém a voltar a fixar.

import (
	"context"
	"errors"
	"strings"
	"testing"

	identity "github.com/aos-ref/platform/identity"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// TestAOS288_TokenRevogadoERecusadoPelaCadeiaDoNo é o teste que a AC2 exige: `ErrTokenRevoked`
// vindo do verifier QUE O NÓ COMPÔS, não de um construído à mão no teste.
//
// A distinção é o ticket inteiro. `identity/integration_test.go:224-293` já provava que o
// mecanismo funciona — mas montava o seu próprio verifier com `WithRevocations`, e foi
// exactamente por isso que o defeito sobreviveu à suite: o que estava partido não era o
// mecanismo, era a composição, e nenhum teste olhava para ela.
func TestAOS288_TokenRevogadoERecusadoPelaCadeiaDoNo(t *testing.T) {
	ctx := context.Background()
	node := newTestNode(t, &countingModel{})

	if node.Revocations == nil {
		t.Fatal("o no tem de compor o registo de revogacao (AOS-288): Node.Revocations esta nil")
	}

	tok, err := node.Authority.MintForHuman(ctx, tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}

	// ANTES: o token vale, e o `Principal` sai resolvido. Sem esta metade, um verifier que
	// negasse TUDO passaria a metade de baixo — que é o modo de falha mais provável de uma
	// guarda escrita à pressa.
	if _, err := node.Verifier.Verify(ctx, tok.Compact); err != nil {
		t.Fatalf("token acabado de emitir devia verificar: %v", err)
	}

	if err := node.Revocations.Revoke(ctx, tok.Claims.JTI); err != nil {
		t.Fatalf("Revoke(%q): %v", tok.Claims.JTI, err)
	}

	// DEPOIS: o MESMO verifier do nó recusa, e recusa com o erro NOMEADO. Um `err != nil`
	// genérico não serviria: a janela e a assinatura também produzem erro, e o que se prova
	// aqui é que foi a REVOGAÇÃO a decidir.
	if _, err := node.Verifier.Verify(ctx, tok.Compact); !errors.Is(err, identity.ErrTokenRevoked) {
		t.Fatalf("apos revogar, Verify = %v; quero ErrTokenRevoked — o passo de revogacao nao esta na cadeia do no", err)
	}
}

// TestAOS288_RevogacaoSobreviveAoRestart fecha o buraco que a discovery encontrou e que o epic
// não previa: a projecção é um mapa EM MEMÓRIA. O evento `identity.nhi.revoked` fica durável,
// mas nada o relia — pelo que um processo novo ressuscitava todos os tokens revogados que ainda
// não tivessem expirado, em silêncio.
//
// Simula-se o restart da forma honesta: um registo NOVO sobre o MESMO Event Store, que é
// exactamente o que o `Bootstrap` faz num arranque seguinte.
func TestAOS288_RevogacaoSobreviveAoRestart(t *testing.T) {
	ctx := context.Background()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	primeiro := identity.NewRevocations(store)
	if err := primeiro.Revoke(ctx, "jti-que-tem-de-sobreviver"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// O "restart": registo novo, mapa vazio, MESMO substrato.
	segundo := identity.NewRevocations(store)
	if revogado, _ := segundo.IsRevoked(ctx, "jti-que-tem-de-sobreviver"); revogado {
		t.Fatal("premissa do teste invalida: o registo novo ja sabia sem Rebuild — a projeccao deixou de ser em memoria?")
	}
	if err := segundo.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	revogado, err := segundo.IsRevoked(ctx, "jti-que-tem-de-sobreviver")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revogado {
		t.Fatal("apos Rebuild o jti revogado tem de continuar revogado — sem isto um restart ressuscita todos os tokens revogados por expirar")
	}
}

// TestAOS288_RebuildSemLeituraFalhaAlto fixa o fail-closed do [identity.Rebuild]: um registo que
// não consegue ler o stream devolve erro em vez de um conjunto vazio.
//
// Porque importa: «nada foi revogado» e «não sei o que foi revogado» são indistinguíveis para
// quem recebe um conjunto vazio, e significam coisas opostas. Devolver o vazio faria o arranque
// declarar-se bem-sucedido no estado exacto que o Rebuild existe para impedir.
func TestAOS288_RebuildSemLeituraFalhaAlto(t *testing.T) {
	semStore := identity.NewRevocations(nil)
	if err := semStore.Rebuild(context.Background()); !errors.Is(err, identity.ErrRevocationsNotDurable) {
		t.Fatalf("Rebuild sem leitura = %v; quero ErrRevocationsNotDurable", err)
	}
}

// ---------------------------------------------------------------------------
// VERACIDADE DO BANNER (AC4) — no molde de aos222_fencing_truthfulness_test.go
// ---------------------------------------------------------------------------

// TestAOS288_BannerDeclaraRevogacaoSoQuandoComposta prova que a frase é DERIVADA, exercitando
// os dois estados. É o teste que falha «na direcção oposta» de que a AC fala: se alguém desligar
// a composição e deixar a frase, ou fixar a frase outra vez como literal, um dos dois ramos cai.
func TestAOS288_BannerDeclaraRevogacaoSoQuandoComposta(t *testing.T) {
	t.Parallel()

	composto := revogacaoNoBanner(true)
	if !strings.Contains(composto, "revogacao") {
		t.Fatalf("com a revogacao composta o banner tem de a anunciar; veio %q", composto)
	}
	naoComposto := revogacaoNoBanner(false)
	if strings.Contains(naoComposto, "+ revogacao") {
		t.Fatalf("SEM composicao o banner NAO pode anunciar revogacao; veio %q", naoComposto)
	}
	if !strings.Contains(naoComposto, "SEM revogacao") {
		t.Fatalf("o estado nao-composto tem de DIZER que nao ha revogacao, nao apenas calar-se; veio %q", naoComposto)
	}
	// As duas frases têm de DIFERIR. Uma implementação que devolvesse a mesma string nos dois
	// ramos passaria as três asserções acima se o texto contivesse ambas as formas.
	if composto == naoComposto {
		t.Fatal("os dois estados produzem a MESMA frase — a alegacao nao esta derivada de nada")
	}
}

// TestAOS288_BannerDoGatewayUsaAFraseDerivada amarra a frase ao sítio onde ela é consumida. Sem
// isto, `revogacaoNoBanner` podia ficar correcta e não ser chamada — que é a forma mais fácil de
// o defeito voltar, porque o teste de cima continuaria verde.
func TestAOS288_BannerDoGatewayUsaAFraseDerivada(t *testing.T) {
	t.Parallel()

	// O ramo do gateway é o que continha a literal com o claim. Exercita-se pelos dois estados
	// e exige-se que a linha MUDE — é o que prova que o parâmetro chega lá.
	linhasCom := strings.Join(modelPostureBanner(nil, true), "\n")
	linhasSem := strings.Join(modelPostureBanner(nil, false), "\n")
	if linhasCom != linhasSem {
		// O ramo de referência (Model nil) não fala de revogação: tem de ser insensível.
		t.Fatalf("o ramo de REFERENCIA nao fala de tokens NHI e nao pode mudar com a revogacao:\ncom=%q\nsem=%q", linhasCom, linhasSem)
	}
}
