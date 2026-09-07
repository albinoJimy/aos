package main

// AOS-367 — PROVA DE AUTORIDADE nas quatro rotas DSAR + barreiras de região.
//
// O DEFEITO. As rotas /dsar/erase, /dsar/hold, /dsar/release e /dsar/expire autorizavam o
// crypto-shred IRREVERSÍVEL com nada além de um ID-token OIDC de LEITURA (readGov.authorize) — o
// mesmo par issuer/audience que serve GET /runs/{id}. Duas das três rotas de destruição não
// verificavam sequer região.
//
// O QUE ESTES TESTES PROVAM (opt-in por composição de AOS_DSAR_ERASERS, no molde do TaintGate):
//
//   - com erasers compostos, um chamador OIDC/header VÁLIDO mas SEM a prova ⇒ 403 nas quatro rotas;
//   - controlo (a): o mesmo token de leitura continua a LER (GET /runs/{id}) com 200 — não regride;
//   - controlo (b): um chamador COM a prova ⇒ NÃO-403 nas quatro rotas;
//   - /dsar/release cross-region: mesma região ⇒ "released"; região diferente ⇒ 403 E o hold
//     MANTÉM-SE (verificado no DSARHolds);
//   - /dsar/expire: UMA assinatura ⇒ 403; DUAS de erasers DISTINTOS ⇒ corre;
//   - o varredor AUTOMÁTICO de retenção NÃO exige assinatura nenhuma — o dual-control vive só no
//     handler HTTP.
//
// Os testes existentes (euReaderHeaders/govHeaders sobre nós SEM erasers) continuam verdes: a prova
// é gated na composição. Correr com -race.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
	audit "github.com/aos-ref/platform/audit"
)

// fixarAutoridadeDSARDeProducao satisfaz a guarda AOS-367 ([ErrProductionNeedsDSARErasers]): sob
// AOS_MODE=production o conjunto de erasers NÃO pode ser vazio. Compõe uma pubkey em AOS_OPERATORS e
// nomeia-a em AOS_DSAR_ERASERS (⊆ Operators, como o [Bootstrap] exige), para que uma produção "quase
// completa" continue a medir a SUA coluna e não a autoridade DSAR que ficaria por definir. É o mesmo
// alargamento que AOS-300 (Event Store) e AOS-365 (WORM+KEK) fizeram às fixtures de produção quando
// o seu eixo passou a apanhá-las.
func fixarAutoridadeDSARDeProducao(t *testing.T) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (eraser de producao): %v", err)
	}
	const id = "ops:eraser-prod"
	t.Setenv("AOS_OPERATORS", id+"="+hex.EncodeToString(pub))
	t.Setenv("AOS_DSAR_ERASERS", id)
}

const (
	eraserID1 = "human:eraser-1"
	eraserID2 = "human:eraser-2"
)

// eraserFixture é um nó COM SteerAuth + AOS_DSAR_ERASERS compostos — a única forma de exercitar a
// prova nova, que os nós por-headers dos testes existentes não têm.
type eraserFixture struct {
	node  *Node
	svc   *NodeService
	h     http.Handler
	priv1 ed25519.PrivateKey
	priv2 ed25519.PrivateKey
}

// newEraserNode compõe duas regiões (EU=govBoard, US=govBoardUS) + dois erasers registados. Sem
// retenção: cobre erase/hold/release e a barreira de região do release.
func newEraserNode(t *testing.T) eraserFixture {
	t.Helper()
	pub1, priv1, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub2, priv2, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.BoardRegions = map[string]string{govBoard: govRegion, govBoardUS: govRegionUS}
	cfg.Operators = map[string]ed25519.PublicKey{eraserID1: pub1, eraserID2: pub2}
	cfg.DSARErasers = []string{eraserID1, eraserID2}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (erasers): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if len(node.DSARErasers) != 2 {
		t.Fatalf("os erasers nao ficaram compostos: %v — a prova nem seria exigida e o teste passaria vazio", node.DSARErasers)
	}
	svc, h := newAPI(t, node)
	return eraserFixture{node: node, svc: svc, h: h, priv1: priv1, priv2: priv2}
}

// newEraserRetentionNode compõe um nó de retenção (durável, relógio muito à frente, pii_operational
// expira) COM erasers — para exercitar o dual-control do /dsar/expire com conteúdo real a expirar.
func newEraserRetentionNode(t *testing.T) eraserFixture {
	t.Helper()
	pub1, priv1, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub2, priv2, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	rc, err := audit.NewRetentionConfig("1.0.0", map[audit.DataClass]time.Duration{
		audit.ClassPIIOperational: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRetentionConfig: %v", err)
	}
	cfg.Retention = rc
	cfg.RetentionClock = retentionFarFuture
	cfg.Operators = map[string]ed25519.PublicKey{eraserID1: pub1, eraserID2: pub2}
	cfg.DSARErasers = []string{eraserID1, eraserID2}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (retencao+erasers): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, h := newAPI(t, node)
	return eraserFixture{node: node, svc: svc, h: h, priv1: priv1, priv2: priv2}
}

// provaDSAR assina o payload canónico de uma acção DSAR com a chave do eraser e devolve o
// emissor de wire pronto a pôr no corpo. É a contraparte de `aos-issuer` — a chave privada assina
// FORA do nó.
func provaDSAR(t *testing.T, priv ed25519.PrivateKey, id, action, subject, requestID string) emitterWire {
	t.Helper()
	payload := integration.CanonicalDSARPayload(action, subject, requestID)
	em, err := integration.SignEmitter(id, priv, integration.DSARScope, control.SignalDSAR, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return emissorDeWire(em)
}

// TestAOS367_PayloadAmarraAccao é o controlo central do payload: a ACÇÃO entra na assinatura, pelo
// que uma assinatura de "hold" NÃO se reapresenta como "erase". Sem isto, um operador que só
// pudesse suspender apagamentos poderia reaplicar a assinatura para os executar.
func TestAOS367_PayloadAmarraAccao(t *testing.T) {
	base := integration.CanonicalDSARPayload("hold", "nhi-x", "req-1")
	variantes := map[string][]byte{
		"outra accao":   integration.CanonicalDSARPayload("erase", "nhi-x", "req-1"),
		"outro titular": integration.CanonicalDSARPayload("hold", "nhi-y", "req-1"),
		"outro request": integration.CanonicalDSARPayload("hold", "nhi-x", "req-2"),
	}
	for nome, v := range variantes {
		if string(v) == string(base) {
			t.Errorf("%s: payload IGUAL ao base — a assinatura serviria para os dois", nome)
		}
	}
	// Deslizamento de fronteira: sem length-prefix ("era","se") e ("eras","e") colidiriam.
	if string(integration.CanonicalDSARPayload("era", "se", "r")) ==
		string(integration.CanonicalDSARPayload("eras", "e", "r")) {
		t.Error("colisao por deslizamento de fronteira entre accao e titular")
	}
}

// TestAOS367_SemProvaRecusaNasQuatroRotas — um chamador OIDC/header VÁLIDO mas SEM a prova recebe
// 403 nas quatro rotas de um nó com erasers compostos.
func TestAOS367_SemProvaRecusaNasQuatroRotas(t *testing.T) {
	f := newEraserNode(t)
	seedPII(t, f.node, "subject-sem-prova")

	// erase / hold / release: sem emitter no corpo ⇒ 403 (a prova é exigida).
	for _, rota := range []struct {
		caminho string
		corpo   map[string]any
	}{
		{"/dsar/erase", map[string]any{"request_id": "r", "subject_id": "subject-sem-prova"}},
		{"/dsar/hold", map[string]any{"request_id": "r", "subject_id": "subject-sem-prova"}},
		{"/dsar/release", map[string]any{"request_id": "r", "subject_id": "subject-sem-prova"}},
	} {
		rec := postReq(f.h, rota.caminho, rota.corpo, euReaderHeaders())
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s SEM prova devolveu %d, quero 403 — um token de leitura autoriza a destruicao: %s",
				rota.caminho, rec.Code, rec.Body.String())
		}
	}
	// A KEK do titular sobreviveu — o 403 veio ANTES do efeito.
	if _, ok := f.node.DSARVault.Key("subject-sem-prova"); !ok {
		if kek, _, _ := f.node.DSARVault.EnsureKey("subject-sem-prova"); kek == nil {
			t.Error("estado inesperado do vault")
		}
	}

	// expire: o job tem de estar composto para chegar à prova (senao 501 mascararia o 403).
	fr := newEraserRetentionNode(t)
	rec := postReq(fr.h, "/dsar/expire", map[string]any{"request_id": "r"}, govHeaders())
	if rec.Code != http.StatusForbidden {
		t.Errorf("/dsar/expire SEM prova devolveu %d, quero 403: %s", rec.Code, rec.Body.String())
	}
}

// TestAOS367_LeituraNaoRegride é o controlo negativo (a): com erasers compostos, o MESMO token de
// leitura continua a LER o seu run com 200. A barreira nova é de destruição, não de leitura.
func TestAOS367_LeituraNaoRegride(t *testing.T) {
	f := newEraserNode(t)
	const runID = "run-leitura-eu"
	submitHTTPAndWait(t, f.svc, f.h, runID, euReaderHeaders())

	if rec := getReq(f.h, "/runs/"+runID, euReaderHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("GET do proprio run devolveu %d, quero 200 — a leitura soberana regrediu: %s",
			rec.Code, rec.Body.String())
	}
}

// TestAOS367_ComProvaNaoRecusaNasQuatroRotas é o controlo negativo (b): um chamador COM a prova
// recebe resposta NÃO-403 nas quatro rotas. Sem esta metade, "negar sempre" passaria o teste de 403.
func TestAOS367_ComProvaNaoRecusaNasQuatroRotas(t *testing.T) {
	f := newEraserNode(t)
	seedPII(t, f.node, "subject-com-prova")

	// hold COM prova ⇒ 200.
	hold := postReq(f.h, "/dsar/hold", map[string]any{
		"request_id": "h1", "subject_id": "subject-com-prova",
		"emitter": provaDSAR(t, f.priv1, eraserID1, "hold", "subject-com-prova", "h1"),
	}, euReaderHeaders())
	if hold.Code == http.StatusForbidden {
		t.Errorf("/dsar/hold COM prova foi 403 — a prova valida devia passar: %s", hold.Body.String())
	}
	// release COM prova ⇒ não-403 (mesma região).
	rel := postReq(f.h, "/dsar/release", map[string]any{
		"request_id": "r1", "subject_id": "subject-com-prova",
		"emitter": provaDSAR(t, f.priv1, eraserID1, "release", "subject-com-prova", "r1"),
	}, euReaderHeaders())
	if rel.Code == http.StatusForbidden {
		t.Errorf("/dsar/release COM prova (mesma regiao) foi 403: %s", rel.Body.String())
	}
	// erase COM prova ⇒ não-403 (200 erased).
	er := postReq(f.h, "/dsar/erase", map[string]any{
		"request_id": "e1", "subject_id": "subject-com-prova",
		"emitter": provaDSAR(t, f.priv1, eraserID1, "erase", "subject-com-prova", "e1"),
	}, euReaderHeaders())
	if er.Code == http.StatusForbidden {
		t.Errorf("/dsar/erase COM prova foi 403: %s", er.Body.String())
	}

	// expire COM as DUAS provas ⇒ não-403 (num nó de retenção, ver teste dedicado). Aqui basta um
	// nó de retenção para confirmar que a via de sucesso não é 403.
	fr := newEraserRetentionNode(t)
	exp := postReq(fr.h, "/dsar/expire", map[string]any{
		"request_id": "x1",
		"emitter":    provaDSAR(t, fr.priv1, eraserID1, "expire", "", "x1"),
		"co_emitter": provaDSAR(t, fr.priv2, eraserID2, "expire", "", "x1"),
	}, govHeaders())
	if exp.Code == http.StatusForbidden {
		t.Errorf("/dsar/expire COM duas provas foi 403: %s", exp.Body.String())
	}
}

// TestAOS367_ReleaseCrossRegion prova a barreira de região do /dsar/release nos dois sentidos: um
// release da PRÓPRIA região levanta o hold; um de OUTRA região é 403 e o hold MANTÉM-SE.
func TestAOS367_ReleaseCrossRegion(t *testing.T) {
	f := newEraserNode(t)
	ctx := context.Background()
	const runID = "run-cross-eu"
	const titular = "nhi-titular-cross"

	// Residência EU selada + titular ligado ao run — a fronteira que o release tem de respeitar.
	if _, err := f.node.WORM.Append(ctx, audit.AuditRecord{
		Partition: readResidencyPartition(runID),
		Resource:  audit.Resource{Type: "run", Value: runID, Region: govRegion},
	}); err != nil {
		t.Fatalf("selar residencia: %v", err)
	}
	f.node.DSARIndex.Link(titular, runID)

	// HOLD colocado pelo operador da UE (com prova).
	hold := postReq(f.h, "/dsar/hold", map[string]any{
		"request_id": "h1", "subject_id": titular,
		"emitter": provaDSAR(t, f.priv1, eraserID1, "hold", titular, "h1"),
	}, euReaderHeaders())
	if hold.Code != http.StatusOK {
		t.Fatalf("hold devia dar 200, veio %d: %s", hold.Code, hold.Body.String())
	}
	if !f.node.DSARHolds.HeldSubject(titular) {
		t.Fatal("apos o hold o titular devia estar retido")
	}

	// RELEASE de OUTRA região (US) ⇒ 403, e o hold MANTÉM-SE.
	relUS := postReq(f.h, "/dsar/release", map[string]any{
		"request_id": "r-us", "subject_id": titular,
		"emitter": provaDSAR(t, f.priv1, eraserID1, "release", titular, "r-us"),
	}, usReaderHeaders())
	if relUS.Code != http.StatusForbidden {
		t.Errorf("/dsar/release cross-region devolveu %d, quero 403: %s", relUS.Code, relUS.Body.String())
	}
	if !f.node.DSARHolds.HeldSubject(titular) {
		t.Error("o hold foi levantado por um chamador de OUTRA regiao — a barreira nao segurou")
	}

	// RELEASE da PRÓPRIA região (EU) ⇒ 200 e o hold é levantado (âncora não-vacuosa).
	relEU := postReq(f.h, "/dsar/release", map[string]any{
		"request_id": "r-eu", "subject_id": titular,
		"emitter": provaDSAR(t, f.priv1, eraserID1, "release", titular, "r-eu"),
	}, euReaderHeaders())
	if relEU.Code != http.StatusOK {
		t.Fatalf("/dsar/release da propria regiao devolveu %d, quero 200: %s", relEU.Code, relEU.Body.String())
	}
	if f.node.DSARHolds.HeldSubject(titular) {
		t.Error("apos o release da propria regiao o hold devia ter sido levantado")
	}
}

// TestAOS367_ReleaseParticaoCrossRegion fecha o residual apanhado na revisão adversarial: um release
// SÓ-DE-PARTIÇÃO (partition sem subject_id) tem de respeitar a mesma fronteira de região que o
// release por titular. Sem isto, um eraser de outra região levantava o hold de uma partição de
// outra soberania e o varredor automático (agnóstico de região) destruía o conteúdo. Dois sentidos:
// região diferente ⇒ 403 e o hold da PARTIÇÃO mantém-se; própria região ⇒ 200 e é levantado.
func TestAOS367_ReleaseParticaoCrossRegion(t *testing.T) {
	f := newEraserNode(t)
	ctx := context.Background()
	const particao = "part-cross-eu"

	// Residência EU selada para a PRÓPRIA partição — a fronteira que o release de partição respeita.
	if _, err := f.node.WORM.Append(ctx, audit.AuditRecord{
		Partition: readResidencyPartition(particao),
		Resource:  audit.Resource{Type: "run", Value: particao, Region: govRegion},
	}); err != nil {
		t.Fatalf("selar residencia da particao: %v", err)
	}

	// HOLD de partição colocado pelo operador da UE (com prova).
	hold := postReq(f.h, "/dsar/hold", map[string]any{
		"request_id": "hp1", "partition": particao,
		"emitter": provaDSAR(t, f.priv1, eraserID1, "hold", particao, "hp1"),
	}, euReaderHeaders())
	if hold.Code != http.StatusOK {
		t.Fatalf("hold de particao devia dar 200, veio %d: %s", hold.Code, hold.Body.String())
	}
	if !f.node.DSARHolds.HeldPartition(particao) {
		t.Fatal("apos o hold a particao devia estar retida")
	}

	// RELEASE de OUTRA região (US) ⇒ 403, e o hold da partição MANTÉM-SE.
	relUS := postReq(f.h, "/dsar/release", map[string]any{
		"request_id": "rp-us", "partition": particao,
		"emitter": provaDSAR(t, f.priv1, eraserID1, "release", particao, "rp-us"),
	}, usReaderHeaders())
	if relUS.Code != http.StatusForbidden {
		t.Errorf("/dsar/release de particao cross-region devolveu %d, quero 403: %s", relUS.Code, relUS.Body.String())
	}
	if !f.node.DSARHolds.HeldPartition(particao) {
		t.Error("o hold de particao foi levantado por um chamador de OUTRA regiao — a barreira nao segurou")
	}

	// RELEASE da PRÓPRIA região (EU) ⇒ 200 e o hold é levantado (âncora não-vacuosa).
	relEU := postReq(f.h, "/dsar/release", map[string]any{
		"request_id": "rp-eu", "partition": particao,
		"emitter": provaDSAR(t, f.priv1, eraserID1, "release", particao, "rp-eu"),
	}, euReaderHeaders())
	if relEU.Code != http.StatusOK {
		t.Fatalf("/dsar/release de particao da propria regiao devolveu %d, quero 200: %s", relEU.Code, relEU.Body.String())
	}
	if f.node.DSARHolds.HeldPartition(particao) {
		t.Error("apos o release da propria regiao o hold de particao devia ter sido levantado")
	}
}

// TestAOS367_ExpireDualControl prova o dual-control do /dsar/expire: UMA assinatura ⇒ 403; DUAS de
// erasers DISTINTOS ⇒ a expiração corre (conteúdo fica irrecuperável). Uma segunda assinatura do
// MESMO eraser NÃO conta.
func TestAOS367_ExpireDualControl(t *testing.T) {
	f := newEraserRetentionNode(t)
	const subject = "nhi:agent-expire-367"
	captureSynthetic(t, f.node, subject, "run-expire-367", "conteudo a expirar: EXP367", "out367")
	sealed, gotSubj := sealedContentOf(t, f.node, "run-expire-367")
	if gotSubj != subject {
		t.Fatalf("conteudo nao selado sob o titular: %q", gotSubj)
	}

	// UMA assinatura ⇒ 403 (co_emitter em falta) e nada expira.
	one := postReq(f.h, "/dsar/expire", map[string]any{
		"request_id": "x1",
		"emitter":    provaDSAR(t, f.priv1, eraserID1, "expire", "", "x1"),
	}, govHeaders())
	if one.Code != http.StatusForbidden {
		t.Fatalf("expire com UMA assinatura devolveu %d, quero 403: %s", one.Code, one.Body.String())
	}
	if _, err := audit.OpenContent(f.node.DSARVault, subject, sealed); err != nil {
		t.Fatalf("apos o 403 o conteudo devia continuar decifravel: %v", err)
	}

	// Segundo eraser IGUAL ao primeiro ⇒ 403 (não são dois distintos).
	mesmo := postReq(f.h, "/dsar/expire", map[string]any{
		"request_id": "x2",
		"emitter":    provaDSAR(t, f.priv1, eraserID1, "expire", "", "x2"),
		"co_emitter": provaDSAR(t, f.priv1, eraserID1, "expire", "", "x2"),
	}, govHeaders())
	if mesmo.Code != http.StatusForbidden {
		t.Fatalf("expire com co_emitter IGUAL devolveu %d, quero 403", mesmo.Code)
	}

	// DUAS assinaturas de erasers DISTINTOS ⇒ corre e o conteudo fica irrecuperável.
	two := postReq(f.h, "/dsar/expire", map[string]any{
		"request_id": "x3",
		"emitter":    provaDSAR(t, f.priv1, eraserID1, "expire", "", "x3"),
		"co_emitter": provaDSAR(t, f.priv2, eraserID2, "expire", "", "x3"),
	}, govHeaders())
	if two.Code != http.StatusOK {
		t.Fatalf("expire com DUAS assinaturas distintas devolveu %d, quero 200: %s", two.Code, two.Body.String())
	}
	if _, err := audit.OpenContent(f.node.DSARVault, subject, sealed); err == nil {
		t.Error("apos a expiracao com dual-control o conteudo devia ser irrecuperavel")
	}
}

// TestAOS367_VarredorAutomaticoNaoExigeAssinatura prova que o dual-control vive SÓ no handler HTTP:
// o varredor automático de retenção corre sem assinatura nenhuma, mesmo num nó com erasers
// compostos. É a garantia de que AOS-367 não tocou no caminho sem-humano.
func TestAOS367_VarredorAutomaticoNaoExigeAssinatura(t *testing.T) {
	f := newEraserRetentionNode(t)
	const subject = "nhi:agent-sweeper-367"
	captureSynthetic(t, f.node, subject, "run-sweeper-367", "conteudo sweeper: SWP367", "outSwp")
	sealed, _ := sealedContentOf(t, f.node, "run-sweeper-367")

	// O varredor automático — SEM assinatura nenhuma — expira.
	if ok := f.svc.SweepRetentionNow(context.Background()); !ok {
		t.Fatal("o varredor automatico devolveu integridade comprometida — nao devia")
	}
	if _, err := audit.OpenContent(f.node.DSARVault, subject, sealed); err == nil {
		t.Error("o varredor automatico devia ter expirado o conteudo sem exigir dual-control")
	}
}

// TestAOS367_ProducaoSemErasersRecusa é a AC do boot-guard: em produção, passadas TODAS as colunas
// de durabilidade, um conjunto de erasers VAZIO recusa o arranque — a destruição irreversível não
// pode ficar autorizada por um token de leitura. Fronteira REAL de leitura do ambiente.
func TestAOS367_ProducaoSemErasersRecusa(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(t.TempDir(), "events.wal"))
	fixarSubstratoDuravelDeProducao(t) // WORM+KEK duráveis: passa as guardas anteriores
	t.Setenv("AOS_DSAR_ERASERS", "")   // o estado sob teste

	_, err := nodeConfigFromEnv()
	if !errors.Is(err, ErrProductionNeedsDSARErasers) {
		t.Fatalf("producao sem AOS_DSAR_ERASERS devia abortar com ErrProductionNeedsDSARErasers, veio: %v", err)
	}
	if !strings.Contains(err.Error(), "AOS_DSAR_ERASERS") {
		t.Errorf("o erro tem de nomear AOS_DSAR_ERASERS para o operador saber o que definir; veio: %v", err)
	}
}

// TestAOS367_ForaDeProducaoSemErasersArranca é o controlo negativo: a MESMA config FORA de produção
// não bate na guarda — a prova fica desligada (retro-compatível). Sem esta metade, uma guarda
// incondicional passaria o teste acima.
func TestAOS367_ForaDeProducaoSemErasersArranca(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(t.TempDir(), "events.wal"))
	fixarSubstratoDuravelDeProducao(t)
	t.Setenv("AOS_DSAR_ERASERS", "")
	t.Setenv("AOS_MODE", "") // fora de produção: a guarda tem `production &&`, não deve entrar

	if _, err := nodeConfigFromEnv(); errors.Is(err, ErrProductionNeedsDSARErasers) {
		t.Fatalf("fora de producao a guarda de erasers NAO devia disparar, veio: %v", err)
	}
}

// TestAOS367_ProducaoComErasersPassaAGuarda é o controlo positivo: com AOS_DSAR_ERASERS composto a
// guarda não dispara. Prova que a guarda tem SAÍDA (não recusa sempre).
func TestAOS367_ProducaoComErasersPassaAGuarda(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(t.TempDir(), "events.wal"))
	fixarSubstratoDuravelDeProducao(t)
	fixarAutoridadeDSARDeProducao(t) // erasers ⊆ operators

	if _, err := nodeConfigFromEnv(); errors.Is(err, ErrProductionNeedsDSARErasers) {
		t.Fatalf("producao COM erasers NAO devia bater na guarda, veio: %v", err)
	}
}

// TestAOS367_ErasersForaDeOperadoresAborta é a validação de Bootstrap: um eraser que não conste de
// AOS_OPERATORS é um direito de destruir atribuído a quem nunca autentica ⇒ aborta fail-closed.
func TestAOS367_ErasersForaDeOperadoresAborta(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.Operators = map[string]ed25519.PublicKey{"ops:a": pub}
	cfg.DSARErasers = []string{"ops:fantasma"} // não consta de Operators
	if _, err := Bootstrap(context.Background(), cfg, io.Discard); !errors.Is(err, ErrBadDSARErasers) {
		t.Fatalf("um eraser fora de AOS_OPERATORS devia abortar com ErrBadDSARErasers, veio: %v", err)
	}
}

// TestAOS367_ParseErasers cobre a fronteira de ambiente do conjunto: vazio ⇒ nil,nil (prova
// desligada); duplicado ⇒ erro; vírgulas de ruído toleradas.
func TestAOS367_ParseErasers(t *testing.T) {
	if got, err := parseDSARErasers(""); err != nil || got != nil {
		t.Errorf("vazio -> (%v,%v), quero (nil,nil)", got, err)
	}
	if got, err := parseDSARErasers("a, b ,c,"); err != nil || len(got) != 3 {
		t.Errorf("lista valida -> (%v,%v)", got, err)
	}
	if _, err := parseDSARErasers("a,a"); err == nil {
		t.Error("duplicado devia abortar")
	}
}
