package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// NÃO SE DESTRÓI O QUE NÃO SE PODE LER.
//
// Achado da varredura adversarial de 2026-08-21, demonstrado com controlo:
//
//	GET /runs/{id}  por chamador de outra região  → 404 (não pode LER)
//	POST /dsar/erase sobre o titular desse run    → 200 "erased"  (podia DESTRUIR)
//
// Uma fronteira de soberania que trava a leitura e deixa passar a destruição irreversível não é
// uma fronteira; é uma preferência. Ver [readGovernance.podeApagarTitular] para a regra e para a
// correcção da minha própria estimativa de custo.
// ---------------------------------------------------------------------------------------------

// cenarioDoisTitulares monta duas regiões, sela a residência de um run EU e liga-lhe um titular.
// Devolve o nó, o handler e o id do titular.
func cenarioTitularEU(t *testing.T) (*Node, http.Handler, string, []byte) {
	t.Helper()
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)

	const runID = "run-residencia-eu"
	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())

	const titular = "nhi-titular-eu"
	// CONTROLO DO CENÁRIO: o índice tem de estar composto, senão a regra nega por fail-closed e
	// o teste passaria pela razão errada.
	if node.DSARIndex == nil {
		t.Fatal("DSARIndex nao composto — a regra negaria por fail-closed e o teste nao provaria nada")
	}
	node.DSARIndex.Link(titular, runID)

	// E a residência do run TEM de estar selada, senão nada constrange e o teste é vacuoso.
	if _, selada, err := govDoNo(t, node).runResidency(context.Background(), runID); err != nil || !selada {
		t.Fatalf("a residencia do run nao ficou selada (selada=%t err=%v) — sem ela a regra nao "+
			"constrange NADA e este teste passaria vazio", selada, err)
	}
	sonda := selarSonda(t, node, titular)
	return node, h, titular, sonda
}

func TestEraseCrossRegionERecusado(t *testing.T) {
	node, h, titular, sonda := cenarioTitularEU(t)

	// ÂNCORA DA ASSIMETRIA: o mesmo chamador US não consegue LER o run.
	if rec := getReq(h, "/runs/run-residencia-eu", usReaderHeaders()); rec.Code != http.StatusNotFound {
		t.Fatalf("GET cross-region devia dar 404 (nao-enumeravel), veio %d — o cenario nao tem "+
			"fronteira e a comparacao que se segue nao significa nada", rec.Code)
	}

	rec := postReq(h, "/dsar/erase", map[string]any{
		"request_id": "req-cross-1", "subject_id": titular,
	}, usReaderHeaders())
	if rec.Code != http.StatusForbidden {
		t.Errorf("/dsar/erase cross-region devolveu %d, queria 403 — um chamador que nao pode LER "+
			"o run consegue DESTRUIR os dados do titular dele: %s", rec.Code, rec.Body.String())
	}
	// E A CHAVE SOBREVIVE. Sem este ramo, um 403 devolvido DEPOIS do shred passaria no teste
	// acima e a destruição teria acontecido na mesma.
	if !kekVivaSonda(node, titular, sonda) {
		t.Error("a KEK do titular foi destruida apesar do 403 — a recusa veio DEPOIS do efeito")
	}
}

// TestEraseDaPropriaRegiaoPASSA é a âncora não-vacuosa.
//
// Sem ela, a implementação mais simples que passa o teste acima é «negar sempre» — e um nó que
// nunca honra um pedido de apagamento tem um problema de conformidade tão real como um que apaga
// de mais.
func TestEraseDaPropriaRegiaoPASSA(t *testing.T) {
	node, h, titular, sonda := cenarioTitularEU(t)

	rec := postReq(h, "/dsar/erase", map[string]any{
		"request_id": "req-mesma-1", "subject_id": titular,
	}, euReaderHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("/dsar/erase da PROPRIA regiao devolveu %d: %s", rec.Code, rec.Body.String())
	}
	if kekVivaSonda(node, titular, sonda) {
		t.Error("o erase devolveu 200 e a KEK do titular sobreviveu")
	}
}

// TestEraseDeTitularSemResidenciaSeladaPASSA é a perna de RETRO-COMPATIBILIDADE, e não é
// generosidade: sem ela, todo o titular anterior à residência selada — e todo o titular que
// alguma vez tenha tido uma aprovação, porque `gov.approvals` entra no índice e não tem região —
// ficaria PERMANENTEMENTE inapagável.
//
// É a mesma concessão que [readGovernance.podeLerRun] faz para runs legados.
func TestEraseDeTitularSemResidenciaSeladaPASSA(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	const titular = "nhi-titular-legado"
	node.DSARIndex.Link(titular, "gov.approvals") // stream de governacao, SEM residencia

	rec := postReq(h, "/dsar/erase", map[string]any{
		"request_id": "req-legado-1", "subject_id": titular,
	}, usReaderHeaders())
	if rec.Code != http.StatusOK {
		t.Errorf("/dsar/erase de um titular sem residencia selada devolveu %d — um stream de "+
			"governacao sem regiao nao pode tornar um titular inapagavel: %s", rec.Code, rec.Body.String())
	}
}

// TestEraseSemIndiceNEGA — fail-closed por wiring em falta.
//
// É a MESMA postura que [audit.Shredder.held] toma para o legal hold por-partição: sem o índice
// não é possível provar que o titular não tem dados noutra região, e não se destrói na dúvida.
func TestEraseSemIndiceNEGA(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	g := govDoNo(t, node)
	id := readerIdentity{principal: govReader, board: govBoardUS, region: govRegionUS}
	if g.podeApagarTitular(context.Background(), id, "nhi-qualquer", nil) {
		t.Error("sem indice composto a regra PERMITIU o apagamento — e um fail-open silencioso " +
			"por wiring em falta, exactamente o que o Shredder.held recusa fazer")
	}
	// CONTROLO: com um índice VAZIO (composto, titular sem dados) permite — senão não se estaria
	// a testar «não consigo provar», estar-se-ia a testar «nego sempre».
	if !g.podeApagarTitular(context.Background(), id, "nhi-qualquer", audit.NewInMemorySubjectPartitionIndex()) {
		t.Error("com indice composto e titular sem dados a regra NEGOU — nada havia a proteger")
	}
}

// govDoNo reconstrói o gate soberano do nó. O `readGovernance` vive no apiHandler e não no
// [Node], pelo que um teste que queira exercer a REGRA sem passar por HTTP tem de o compor com as
// mesmas peças — as MESMAS que o `newAPI` usa (ver api.go).
func govDoNo(t *testing.T, node *Node) *readGovernance {
	t.Helper()
	if node.SovereignReadRegions == nil || node.WORM == nil {
		t.Fatal("soberania de leitura nao composta — o cenario nao esta montado")
	}
	return newReadGovernance(node.SovereignReadRegions, nil, node.WORM, time.Now)
}

// kekVivaSonda / selarSonda respondem se a KEK do titular AINDA existe, e respondem-no da única
// forma que não se pode falsificar: cifrando uma sonda ANTES e tentando decifrá-la DEPOIS.
// Perguntar ao cofre «tens esta chave?» seria uma pergunta sobre a ESTRUTURA; isto é uma pergunta
// sobre o EFEITO, que é o que o crypto-shred promete.
//
// PORQUE NÃO SE USA O `sealedContentOf` (aos093_substrate_erase_test.go), que já existe e é mais
// fiel: aquele EXTRAI um blob que um run REAL selou, e para isso precisa do `captureSynthetic`
// sobre um nó durável. Este cenário liga o titular ao índice À MÃO, para exercer a fronteira de
// região sem depender da forma como o conteúdo lá foi parar — precisa de CRIAR a sonda, não de a
// encontrar. Cifra pela MESMA via que o `contentSealer` de produção usa
// (`audit.SealContent(vault, subject, …)`), pelo que não é um segundo mecanismo, é a mesma porta.
//
// Um teste que TENHA um run real deve preferir o `sealedContentOf`: prova mais.
func selarSonda(t *testing.T, node *Node, subject string) []byte {
	t.Helper()
	b, err := audit.SealContent(node.DSARVault, subject, []byte("sonda"), nil)
	if err != nil {
		t.Fatalf("selar sonda para %q: %v", subject, err)
	}
	return b
}

func kekVivaSonda(node *Node, subject string, sonda []byte) bool {
	_, err := audit.OpenContent(node.DSARVault, subject, sonda)
	return !errors.Is(err, audit.ErrDecrypt) && err == nil
}
