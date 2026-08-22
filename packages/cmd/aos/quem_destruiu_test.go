package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// QUEM DESTRUIU FICA NA CADEIA.
//
// Achado da varredura adversarial de 2026-08-21, demonstrado com um experimento: as duas vias
// HUMANAS de crypto-shred autenticavam o chamador e deitavam fora a identidade
// (`if _, ok := h.readGov.authorize(r); !ok`), e os registos selados ficavam com
// `Principal.NHIID` vazio.
//
// A assimetria era indefensável:
//
//	/dsar/hold      REVERSÍVEL   selava principal e região
//	varredor        AUTOMÁTICO   sela NHI própria e RECUSA correr sem ela
//	/dsar/erase     IRREVERSÍVEL não selava ninguém
//	/dsar/expire    IRREVERSÍVEL não selava atribuição NENHUMA
//
// A auditoria era mais forte no que se pode desfazer do que no que não se pode.
// ---------------------------------------------------------------------------------------------

// selosDaParticao devolve os registos de uma partição do WORM do nó.
func selosDaParticao(t *testing.T, node *Node, particao string) []audit.AuditRecord {
	t.Helper()
	ctx := context.Background()
	head, err := node.WORM.Head(ctx, particao)
	if err != nil || head == 0 {
		return nil
	}
	recs, err := node.WORM.Read(ctx, particao, 1, head)
	if err != nil {
		t.Fatalf("Read(%s): %v", particao, err)
	}
	return recs
}

func TestEraseSelaQUEMPediu(t *testing.T) {
	node, h, leitor := noComGovernacaoDeLeitura(t)

	rec := postJSONComLeitor(t, h, "POST", "/dsar/erase", map[string]any{
		"request_id": "req-quem-1",
		"subject_id": "nhi:titular-x",
	}, leitor)
	if rec.Code != http.StatusOK {
		t.Fatalf("/dsar/erase devolveu %d: %s", rec.Code, rec.Body.String())
	}

	selos := selosDaParticao(t, node, "dsar:nhi:titular-x")
	if len(selos) == 0 {
		// A partição pode ser nomeada de outra forma; procura em todas.
		selos = selosDeQualquerParticaoCom(t, node, "req-quem-1")
	}
	if len(selos) == 0 {
		t.Fatal("nenhum selo de DSAR na cadeia — o cenario nao esta montado")
	}

	for _, s := range selos {
		if s.Principal.NHIID == "" {
			t.Errorf("o selo %q ficou SEM principal — a cadeia prova que a KEK morreu e nao prova "+
				"por ordem de quem", s.Capability)
			continue
		}
		if s.Principal.NHIID != leitor {
			t.Errorf("o selo %q nomeia %q e quem pediu foi %q", s.Capability, s.Principal.NHIID, leitor)
		}
	}
}

// TestExpireSelaAtribuicaoANTESDeCorrer — o pior dos dois: esta rota não selava nada.
func TestExpireSelaAtribuicaoAntesDeCorrer(t *testing.T) {
	node, h, leitor := noComGovernacaoDeLeitura(t)

	rec := postJSONComLeitor(t, h, "POST", "/dsar/expire", map[string]any{}, leitor)
	if rec.Code != http.StatusOK {
		t.Fatalf("/dsar/expire devolveu %d: %s", rec.Code, rec.Body.String())
	}

	selos := selosDaParticao(t, node, retentionPartition)
	if len(selos) == 0 {
		t.Fatal("a rota nao selou atribuicao NENHUMA — o caminho sem humano regista quem e o " +
			"caminho COM humano nao regista ninguem")
	}
	var achou bool
	for _, s := range selos {
		if s.Principal.NHIID == leitor {
			achou = true
			// E o gatilho distingue-a do varredor: mesma forma, autor diferente.
			var gatilho string
			for _, ob := range s.Obligations {
				if v, ok := ob.Params["trigger"]; ok {
					gatilho = v
				}
			}
			if gatilho != retentionTriggerRota {
				t.Errorf("gatilho = %q, queria %q — as duas passagens teem de ser distinguiveis "+
					"na cadeia", gatilho, retentionTriggerRota)
			}
		}
	}
	if !achou {
		t.Errorf("nenhum selo nomeia %q; selos = %d", leitor, len(selos))
	}
}

// TestOVarredorAutomaticoCONTINUAASelarASuaNHI é o CONTROLO da parametrização.
//
// Sem ele, uma alteração que passasse o principal da rota para os dois caminhos faria o varredor
// automático deixar de se identificar em nome próprio — e o teste acima passaria na mesma.
func TestOVarredorAutomaticoContinuaASelarASuaNHI(t *testing.T) {
	node := noRealComRetencao(t)
	// CADÊNCIA POSITIVA, e não é um detalhe: com 0 o escalonador fica DESARMADO (perna 1 de
	// [retentionSchedulerArmed]) e `SweepRetentionNow` devolve `true` SEM correr passagem nenhuma.
	// A primeira versão deste controlo passava cadência 0 e media o vazio — o `true` que recebia
	// não era «a passagem correu», era «não havia passagem». Um controlo vacuoso é pior do que
	// nenhum: dá licença à parametrização que ele existe para vigiar.
	svc := newScheduledRetentionService(t, node, time.Hour)
	if !retentionSchedulerArmed(node, time.Hour) {
		t.Fatal("o escalonador ficou DESARMADO — este controlo mediria o vazio")
	}

	if !svc.SweepRetentionNow(t.Context()) {
		t.Fatal("a passagem devia concluir")
	}
	selos := selosDaParticao(t, node, retentionPartition)
	if len(selos) == 0 {
		t.Fatal("o varredor nao selou nada")
	}
	for _, s := range selos {
		if s.Principal.NHIID != retentionSchedulerNHI {
			t.Errorf("o varredor selou %q e devia selar a sua NHI propria %q",
				s.Principal.NHIID, retentionSchedulerNHI)
		}
		for _, ob := range s.Obligations {
			if v, ok := ob.Params["trigger"]; ok && v != retentionTriggerScheduler {
				t.Errorf("gatilho do varredor = %q, queria %q", v, retentionTriggerScheduler)
			}
		}
	}
}

// selosDeQualquerParticaoCom procura, em TODAS as partições, registos com o request id dado.
func selosDeQualquerParticaoCom(t *testing.T, node *Node, requestID string) []audit.AuditRecord {
	t.Helper()
	lister, ok := node.WORM.(interface{ Partitions() []string })
	if !ok {
		return nil
	}
	var out []audit.AuditRecord
	for _, p := range lister.Partitions() {
		for _, r := range selosDaParticao(t, node, p) {
			if r.RequestID == requestID && strings.HasPrefix(r.Capability, "dsar.") {
				out = append(out, r)
			}
		}
	}
	return out
}

// noComGovernacaoDeLeitura compõe um nó com soberania de leitura ligada e devolve o handler e o
// principal do leitor autenticado — que é o «quem» que estes testes exigem ver na cadeia.
func noComGovernacaoDeLeitura(t *testing.T) (*Node, http.Handler, string) {
	t.Helper()
	node := newGovNode(t, &countingModel{})
	_, h := newAPI(t, node)
	return node, h, govReader
}

// postJSONComLeitor é o postJSON com os cabeçalhos do leitor de governação.
func postJSONComLeitor(t *testing.T, h http.Handler, method, target string, body any, _ string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, r)
	for k, v := range euReaderHeaders() {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
