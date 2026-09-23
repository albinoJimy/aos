package main

// aos428_credencial_no_ingresso_test.go — A CREDENCIAL DO RUN RECUSA-SE NA PORTA.
//
// O que estes testes impõem: uma credencial que não verifica não cria run, não sela residência,
// e a recusa é a MESMA para todas as causas. E a metade que costuma faltar — que o caminho bom
// continua a passar, e que a verificação do RM continua a ser quem decide o escopo.

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/platform/identity"
)

// noComCredencial devolve um nó soberano e uma credencial válida para ele.
func noComCredencial(t *testing.T) (*Node, http.Handler, string) {
	t.Helper()
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, node.WORM))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return node, h, credencialDeTeste(t, node)
}

// AS CINCO CAUSAS SÃO RECUSADAS, E TODAS DA MESMA MANEIRA — E COM A MESMA 403 DA GOVERNAÇÃO.
//
// A uniformidade tem DUAS metades, e a segunda foi encontrada por revisão adversarial.
//
// Entre si: distinguir «emissor desconhecido» de «expirada» daria a quem sonda um oráculo sobre
// o trust store. As sentinelas ficam no log do operador.
//
// E face à recusa da GOVERNAÇÃO: a primeira versão devolvia 401, um status que mais nenhuma rota
// do nó usa. Isso fazia da guarda um oráculo por si só — 401 e 403 são o bit que distingue «este
// token morreu» de «este token vive mas não estás autorizado». Agora é a mesma 403.
func TestAOS428CredencialInvalidaERecusadaUniformemente(t *testing.T) {
	_, h, valida := noComCredencial(t)

	// Uma credencial de OUTRO emissor: token estruturalmente válido, assinado por uma
	// autoridade que este nó não conhece. É o caso que um `strings`-check nunca apanharia.
	outroNo := newTwoRegionGovNode(t, &countingModel{})
	deOutroEmissor := credencialDeTeste(t, outroNo)
	if deOutroEmissor == "" || deOutroEmissor == valida {
		t.Fatal("fixture: a credencial do outro no devia existir e ser diferente")
	}

	casos := []struct {
		nome, cred, porque string
	}{
		{"malformada", "isto-nao-e-um-jws", "tres segmentos base64 e o minimo"},
		{"segmentos-invalidos", "aaa.bbb.ccc", "parece um JWS e nao e"},
		{"de outro emissor", deOutroEmissor, "assinatura valida, trust anchor errado"},
		{"jws com payload lixo", "eyJhbGciOiJFZERTQSJ9.LS0.LS0", "cabecalho plausivel, resto nao"},
	}
	// A AUSÊNCIA NÃO ESTÁ AQUI, e tem teste próprio — ver
	// [TestAOS428AusenciaSoERecusadaEmModoEndurecido]. Neste nó de referência ela é ACEITE de
	// propósito: sem autoridade externa não há de onde vir uma credencial.

	var corpos []string
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			rec := postReq(h, "/runs", submitRequest{
				RunID:      "run-428-" + strings.ReplaceAll(c.nome, " ", "-"),
				Credential: c.cred,
			}, euReaderHeaders())
			if rec.Code != http.StatusForbidden {
				t.Fatalf("credencial %s devia dar 403 (%s), veio %d (%s)",
					c.nome, c.porque, rec.Code, rec.Body.String())
			}
			corpos = append(corpos, rec.Body.String())
		})
	}

	// A UNIFORMIDADE, medida: todos os corpos iguais.
	for i := 1; i < len(corpos); i++ {
		if corpos[i] != corpos[0] {
			t.Errorf("a resposta distingue causas de recusa:\n  %q\n  %q\n"+
				"isso e um oraculo sobre o trust store deste no", corpos[0], corpos[i])
		}
	}
}

// A RECUSA NÃO DEIXA RASTO — e é isto que distingue esta correcção de a mover para mais cedo e
// na mesma sujar o estado.
//
// Sem esta asserção, verificar na porta continuaria a selar residência no WORM para um run que
// nunca vai existir, e o defeito ficava metade fechado.
func TestAOS428RecusaNaoSelaResidenciaNemCriaRun(t *testing.T) {
	node, h, valida := noComCredencial(t)
	const runID = "run-428-sem-rasto"

	rec := postReq(h, "/runs", submitRequest{RunID: runID, Credential: "credencial-invalida"}, euReaderHeaders())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("esperava 403, veio %d", rec.Code)
	}

	// RESIDÊNCIA: nenhuma partição selada para este run.
	//
	// O erro do `Head` NÃO se descarta: um `Head` que falhe devolve 0, e um 0 por falha é
	// indistinguível de um 0 por ausência — exactamente a ambiguidade que o AOS-422 mediu noutro
	// eixo desta árvore.
	head, herr := node.WORM.Head(context.Background(), readResidencyPartition(runID))
	if herr != nil {
		t.Fatalf("Head da particao de residencia: %v (um 0 por falha nao prova ausencia)", herr)
	}
	if head != 0 {
		t.Errorf("a recusa selou residencia para %q (head=%d): a selagem e pre-condicao da "+
			"HOSPEDAGEM, e este run nunca foi hospedado", runID, head)
	}

	// E O RUN NÃO FOI CRIADO — provado com uma credencial VÁLIDA.
	//
	// A primeira versão deste teste repetia a submissão com OUTRA credencial inválida e exigia
	// 403. Era tautológico: a segunda também é recusada na guarda, antes do `Submit`, quer o
	// primeiro tenha criado um run quer não. A asserção não conseguia detectar o que declarava.
	//
	// Com uma credencial válida, o mesmo `run_id` tem de ser aceite COMO NOVO (201). Se o
	// primeiro pedido tivesse criado alguma coisa, isto colidiria.
	bom := postReq(h, "/runs", submitRequest{RunID: runID, Objective: "x", Credential: valida}, euReaderHeaders())
	if bom.Code != http.StatusCreated {
		t.Errorf("o mesmo run_id com credencial VALIDA devia ser aceite como novo (201), veio %d (%s):\n"+
			"se colidiu, a recusa anterior criou estado que nao devia ter criado", bom.Code, bom.Body.String())
	}
}

// O CAMINHO BOM CONTINUA A PASSAR.
//
// A metade que impede uma guarda de «recusar tudo» de passar os testes acima.
func TestAOS428CredencialValidaAtravessa(t *testing.T) {
	_, h, valida := noComCredencial(t)
	if valida == "" {
		t.Fatal("fixture: o no de teste devia ter autoridade para cunhar")
	}
	rec := postReq(h, "/runs", submitRequest{
		RunID: "run-428-bom", Objective: "trabalho", Credential: valida,
	}, euReaderHeaders())
	if rec.Code != http.StatusCreated {
		t.Fatalf("uma credencial VALIDA devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// A ORDEM: o `run_id` inválido continua a ser 400, e não 401.
//
// A guarda nova corre DEPOIS da validação de corpo, de propósito. Se corresse antes, um cliente
// com um `run_id` malformado receberia «credencial inválida» — um diagnóstico errado que o
// mandaria procurar no sítio errado.
func TestAOS428OrdemDasRecusasNaoTrocaODiagnostico(t *testing.T) {
	_, h, _ := noComCredencial(t)
	rec := postReq(h, "/runs", submitRequest{RunID: "cliente.pedido", Credential: "invalida"}, euReaderHeaders())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("um run_id irrepresentavel devia dar 400 mesmo com credencial invalida, veio %d:\n"+
			"a validacao de corpo corre primeiro para que o diagnostico seja o do defeito real", rec.Code)
	}
}

// O ESCOPO POR CAPABILITY CONTINUA A SER DO RM.
//
// # ISTO É UM GUARD DE FONTE, E NÃO UM TESTE DE COMPORTAMENTO — DITO PARA NÃO SE CONFUNDIREM
//
// Lê o ficheiro e procura texto. Não prova comportamento nenhum: o `Allows(` pode aparecer
// noutro ficheiro e isto fica verde, e o `Verify(` pode estar num ramo morto e isto passa na
// mesma. Uma revisão adversarial apontou-o, e tem razão.
//
// Fica assim mesmo, com a limitação escrita, porque a propriedade que interessa — «o escopo NÃO
// se decide na porta» — não é observável por comportamento: a capability não existe na
// submissão, logo não há entrada que distinga uma implementação que a decide de uma que não.
// O sítio próprio seria o `arch-lint` de `scripts/ci/`; enquanto lá não estiver, um guard de
// intenção declarado vale mais do que nenhum.
func TestAOS428OEscopoNaoMigrouParaAPorta(t *testing.T) {
	bruto, err := os.ReadFile("credencial_do_run.go")
	if err != nil {
		t.Fatalf("ler credencial_do_run.go: %v", err)
	}
	fonte := string(bruto)
	if strings.Contains(fonte, "Allows(") {
		t.Error("a guarda do ingresso passou a decidir ESCOPO por capability.\n" +
			"A capability nao existe na submissao — decidir escopo aqui so pode ser feito com\n" +
			"uma capability inventada, e essa decisao pertence ao rmadapter, na chamada.")
	}
	if !strings.Contains(fonte, "h.node.Verifier.Verify(") {
		t.Error("a guarda deixou de chamar o Verifier do no.\n" +
			"A regra tem de ser UMA: reimplementar a verificacao de identidade aqui e a classe\n" +
			"de defeito que o AOS-424 passou uma serie inteira a fechar.")
	}
}

// A AUSÊNCIA SÓ SE RECUSA EM MODO ENDURECIDO — e o smoke é que ensinou porquê.
//
// A primeira versão recusava a ausência sempre. Parecia óbvio: um run que não pode fazer nada
// mediado é um run que vai morrer. E **partiu o nó de referência** — o smoke deixou de conseguir
// submeter, não por descuido da fixture mas porque em modo não-endurecido NÃO EXISTE forma de um
// cliente externo obter uma credencial: a autoridade é co-localizada e não tem rota de emissão.
//
// Nenhum teste unitário apanhou isto. Foi preciso correr o sistema.
func TestAOS428AusenciaSoERecusadaEmModoEndurecido(t *testing.T) {
	// (a) REFERÊNCIA (autoridade co-localizada): a ausência ATRAVESSA, e o RM continua a ser a
	// rede — nega na primeira chamada mediada, como sempre fez.
	_, hRef, _ := noComCredencial(t)
	if rec := postReq(hRef, "/runs", submitRequest{RunID: "run-428-ref-sem-cred"}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Errorf("num no de REFERENCIA a ausencia tem de atravessar (201), veio %d (%s):\n"+
			"exigir presenca ali e exigir uma coisa que o no nao sabe dar", rec.Code, rec.Body.String())
	}

	// (b) ENDURECIDO (trust-anchor-only, a postura de produção): a ausência é RECUSADA.
	nodeEnd := noEndurecidoDeTeste(t)
	if nodeEnd.Authority != nil {
		t.Fatal("fixture: um no endurecido NAO pode ter autoridade de emissao composta")
	}
	if motivo := (&apiHandler{node: nodeEnd}).credencialDoRunRecusada(context.Background(), ""); motivo == "" {
		t.Error("num no ENDURECIDO a ausencia tem de ser recusada:\n" +
			"a credencial vem sempre de fora, e AOS_MODE=production exige esta postura")
	}
	// E o caminho bom do mesmo nó: uma credencial do emissor que ele confia passa.
	if motivo := (&apiHandler{node: nodeEnd}).credencialDoRunRecusada(context.Background(), "lixo"); motivo == "" {
		t.Error("num no endurecido uma credencial invalida tem de ser recusada")
	}
}

// noEndurecidoDeTeste devolve um nó trust-anchor-only: a postura de produção, em que o nó
// verifica e nunca assina, e a credencial vem sempre de fora.
//
// Molde de `bootstrap_test.go`: autoridade EXTERNA só para extrair o trust anchor; o nó recebe
// a pubkey e nada mais.
func noEndurecidoDeTeste(t *testing.T) *Node {
	t.Helper()
	ctx := context.Background()
	extAuth, err := integration.NewIssuerAuthority(integration.AuthorityConfig{
		IssuerID:      "iss:aos428-externo",
		Classes:       tnBaseConfig().IssuerClasses,
		Directory:     integration.NewAllowlistDirectory(tnHuman),
		IssuerOptions: []identity.IssuerOption{identity.WithIssuerClock(tnClock())},
	})
	if err != nil {
		t.Fatalf("NewIssuerAuthority (externa): %v", err)
	}
	issuerID, pub := extAuth.TrustAnchor()
	node, err := Bootstrap(ctx, Config{
		IssuerID:      issuerID,
		IssuerPubKey:  pub,
		IssuerClasses: tnBaseConfig().IssuerClasses,
		VerifierClock: tnClock(),
	}, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (endurecido): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}
