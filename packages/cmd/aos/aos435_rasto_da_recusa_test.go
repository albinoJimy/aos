package main

// aos435_rasto_da_recusa_test.go — a recusa de credencial volta a deixar prova (AOS-435).
//
// O AOS-428 trocou uma negação tardia-MAS-AUDITADA por uma precoce-e-NÃO-AUDITADA. O AOS-433
// fechou a detecção (métrica). Este fecha a prova — e as duas decisões de desenho que a tornam
// segura têm teste próprio: a quem se atribui, e porque é que a partição é UMA.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
)

// submeterComCredencialMa submete com uma credencial que não verifica, e exige o 403.
func submeterComCredencialMa(t *testing.T, h http.Handler, runID string) {
	t.Helper()
	rec := postReq(h, "/runs", submitRequest{RunID: runID, Credential: "isto-nao-e-um-jws"}, euReaderHeaders())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /runs com credencial ma devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// selosDaRecusa lê a partição única de recusas.
func selosDaRecusa(t *testing.T, node *Node) []audit.AuditRecord {
	t.Helper()
	ctx := context.Background()
	head, err := node.WORM.Head(ctx, credencialRecusadaPartition)
	if err != nil || head == 0 {
		return nil
	}
	recs, err := node.WORM.Read(ctx, credencialRecusadaPartition, 1, head)
	if err != nil {
		t.Fatalf("ler a particao %q: %v", credencialRecusadaPartition, err)
	}
	return recs
}

// TestAOS435ARecusaESELADA é o núcleo: a recusa deixa um registo tamper-evidente.
func TestAOS435ARecusaESELADA(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	if antes := selosDaRecusa(t, node); len(antes) != 0 {
		t.Fatalf("a particao ja tinha %d selos antes do teste — o teste deixou de medir o que diz", len(antes))
	}

	const run = "run-435-recusado"
	submeterComCredencialMa(t, h, run)

	selos := selosDaRecusa(t, node)
	if len(selos) != 1 {
		t.Fatalf("uma recusa tinha de produzir UM selo, vieram %d\n"+
			"antes do AOS-428 esta negacao produzia um MediationRecord; depois passou a ser uma "+
			"linha de log, e uma campanha com tokens roubados ficou invisivel ao trilho", len(selos))
	}
	s := selos[0]
	if s.Decision != audit.DecisionDeny {
		t.Errorf("Decision = %q, quer deny", s.Decision)
	}
	if s.RunID != run {
		t.Errorf("RunID = %q, quer %q", s.RunID, run)
	}
	if s.ToolID != credencialRecusadaToolID {
		t.Errorf("ToolID = %q, quer %q", s.ToolID, credencialRecusadaToolID)
	}

	// O MOTIVO VIAJA — é o que distingue uma campanha de tokens expirados de uma de assinaturas
	// forjadas, e é o que o operador precisa para responder.
	var motivo string
	for _, o := range s.Obligations {
		if o.Type == credencialRecusadaMotivoObl && len(o.Fields) > 0 {
			motivo = o.Fields[0]
		}
	}
	if motivo == "" {
		t.Error("o selo nao leva o MOTIVO da recusa — a prova diz que houve recusa mas nao porque")
	}
}

// TestAOS435AtribuidaAoSUBMISSORenaoAoToken — a decisão de atribuição, fixada.
//
// Atribuir ao principal do token seria gravar numa cadeia tamper-evidente uma identidade que
// ninguém confirmou — foi exactamente esse token que não verificou.
func TestAOS435AtribuidaAoSUBMISSORenaoAoToken(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	submeterComCredencialMa(t, h, "run-435-atribuicao")

	selos := selosDaRecusa(t, node)
	if len(selos) != 1 {
		t.Fatalf("esperava 1 selo, vieram %d", len(selos))
	}
	// O submissor é o principal que o gate soberano verificou a partir dos cabeçalhos do leitor.
	quemChamou := euReaderHeaders()[HeaderReaderPrincipal]
	if selos[0].Principal.NHIID != quemChamou {
		t.Errorf("o selo foi atribuido a %q, e quem chamou foi %q\n"+
			"a recusa tem de ser atribuida ao SUBMISSOR verificado — o principal do token e uma "+
			"afirmacao por provar", selos[0].Principal.NHIID, quemChamou)
	}
}

// TestAOS435APARTICAOEUMASO é a defesa, e é o teste que mais importa deste ficheiro.
//
// O `run_id` de uma submissão recusada vem do PEDIDO e o run nunca chega a existir. Se a
// partição fosse por run, um chamador que submetesse com `run_id`s aleatórios criaria partições
// WORM sem limite — poluição do trilho de auditoria, paga por quem o opera.
func TestAOS435APARTICAOEUMASO(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	runs := []string{"run-435-a", "run-435-b", "run-435-c", "run-435-d"}
	for _, r := range runs {
		submeterComCredencialMa(t, h, r)
	}

	// As QUATRO recusas estão na MESMA partição.
	if n := len(selosDaRecusa(t, node)); n != len(runs) {
		t.Fatalf("a particao unica tinha de ter %d selos, tem %d", len(runs), n)
	}
	// E NENHUMA criou uma partição com o seu nome.
	for _, r := range runs {
		for _, suspeita := range []string{r, "governance.credential/" + r, "gov.read/" + r} {
			if head, _ := node.WORM.Head(context.Background(), suspeita); head != 0 {
				t.Errorf("a recusa de %q criou a particao %q — um chamador com run_ids aleatorios "+
					"criaria particoes WORM SEM LIMITE", r, suspeita)
			}
		}
	}
}

// TestAOS435OBannerDeclaraAsQUATROPosturas amarra a linha ao estado composto, nas duas
// dimensões.
func TestAOS435OBannerDeclaraAsQUATROPosturas(t *testing.T) {
	for _, c := range []struct {
		endurecido, selada bool
		quer, naoQuer      []string
	}{
		{true, true, []string{"RECUSADA", "SELADA", "governance.credential", "SUBMISSOR"}, []string{"e ACEITE"}},
		{true, false, []string{"RECUSADA", "NAO e selada"}, []string{"e ACEITE", "e SELADA no WORM"}},
		{false, true, []string{"e ACEITE", "SELADA"}, []string{"e RECUSADA"}},
		{false, false, []string{"e ACEITE", "NAO e selada"}, []string{"e RECUSADA", "e SELADA no WORM"}},
	} {
		linha := strings.Join(credencialNaPortaPostureBanner(c.endurecido, c.selada), " ")
		for _, q := range c.quer {
			if !strings.Contains(linha, q) {
				t.Errorf("endurecido=%v selada=%v: o banner nao diz %q\n%s", c.endurecido, c.selada, q, linha)
			}
		}
		for _, n := range c.naoQuer {
			if strings.Contains(linha, n) {
				t.Errorf("endurecido=%v selada=%v: o banner diz %q, que e falso neste estado", c.endurecido, c.selada, n)
			}
		}
		// A frase que responde à pergunta do operador — «de onde veio este 403?».
		if !strings.Contains(linha, "DUAS GUARDAS") {
			t.Errorf("o banner perdeu a explicacao das duas guardas que respondem 403")
		}
	}
}
