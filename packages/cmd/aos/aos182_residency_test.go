package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-182 (DEF-202) — FALSIFICABILIDADE da FRONTEIRA DE SOBERANIA POR-RUN. Prova, nos DOIS
// sentidos e por TODAS as vias de leitura, que uma vez composta a soberania (>=2 regiões):
//
//   - a RESIDÊNCIA do run é SELADA na criação (submit) a partir da resolução board→região do
//     SUBMISSOR — fonte autoritativa, não auto-declarada — de forma durável e tamper-evidente;
//   - um leitor da MESMA região lê (200) — âncora não-vacuosa;
//   - um leitor de OUTRA região, COM credencial VÁLIDA do SEU board, é RECUSADO (404
//     não-enumerável) em GET, trajectória E reconstrução — NUNCA se serve conteúdo cross-region;
//   - o selo D6 grava a residência do run (obrigação [readResidencyObligation]) além da região
//     do leitor — prova a coincidência que a authz impôs;
//   - FAIL-CLOSED no submit soberano: sem credencial/board resolvível ⇒ 403; WORM não sela ⇒ 503;
//   - a selagem de residência é IDEMPOTENTE por-RunID (retries/re-submissões não poluem o trilho);
//   - RETRO-COMPAT: um run SEM residência selada (criado in-process/legado) é servido SEM check.
//
// Correr SEMPRE com -race.

const (
	govBoardUS  = "board:us-182"
	govRegionUS = "us"
)

// newTwoRegionGovNode compõe um nó COM soberania de leitura ligada e DUAS regiões distintas
// (EU=govBoard e US=govBoardUS) — a topologia mínima para exercitar a fronteira cross-region.
func newTwoRegionGovNode(t *testing.T, model agentruntime.ModelClient) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.BoardRegions = map[string]string{govBoard: govRegion, govBoardUS: govRegionUS}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (duas regiões): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// euReaderHeaders / usReaderHeaders — leitores AUTORIZADOS de cada região (ambos com board VÁLIDO).
func euReaderHeaders() map[string]string {
	return map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBoard}
}
func usReaderHeaders() map[string]string {
	return map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBoardUS}
}

// submitHTTPAndWait submete um run pela via HTTP (POST /runs, que SELA a residência) com os headers
// do submissor dados e espera que termine (para GET devolver o desfecho).
func submitHTTPAndWait(t *testing.T, svc *NodeService, h http.Handler, runID string, headers map[string]string) {
	t.Helper()
	rec := postReq(h, "/runs", submitRequest{RunID: runID, PrincipalNHI: "nhi:" + runID}, headers)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs (%s) devia dar 201, veio %d (%s)", runID, rec.Code, rec.Body.String())
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(waitCtx, runID); err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%t err=%v", runID, ok, err)
	}
}

// ---------------------------------------------------------------------------
// (1) RESIDÊNCIA SELADA NA CRIAÇÃO + leitor da mesma região lê (não-vacuoso).
// ---------------------------------------------------------------------------

// TestAOS182_ResidencySealedOnSubmit prova que o POST /runs SELA a residência do run a partir da
// região do SUBMISSOR (EU), de forma durável e tamper-evidente por-RunID, e que o leitor EU lê o
// desfecho (200) — a âncora POSITIVA não-vacuosa. Prova também que o selo D6 da leitura carrega a
// RESIDÊNCIA do run (obrigação [readResidencyObligation]), não só a região do leitor.
func TestAOS182_ResidencySealedOnSubmit(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	const runID = "run-182-eu"

	// ANTES do submit (não-vácuo): a partição de residência está VAZIA.
	if head, _ := node.WORM.Head(context.Background(), readResidencyPartition(runID)); head != 0 {
		t.Fatalf("residência devia estar vazia antes do submit, head=%d", head)
	}

	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())

	// A residência foi SELADA com a região do submissor (EU), tamper-evidente por-RunID.
	seal, ok, err := node.WORM.At(context.Background(), readResidencyPartition(runID), 1)
	if err != nil || !ok {
		t.Fatalf("residência do run devia estar selada, ok=%t err=%v", ok, err)
	}
	if seal.Resource.Region != govRegion {
		t.Fatalf("residência selada devia ser %q, veio %q", govRegion, seal.Resource.Region)
	}
	if seal.Capability != capRunResidency || seal.RunID != runID {
		t.Fatalf("selo de residência mal formado: cap=%q run=%q", seal.Capability, seal.RunID)
	}
	assertNoPIIInPartition(t, node.WORM, readResidencyPartition(runID))

	// O leitor EU lê o desfecho (200) — a fronteira NÃO barra o legítimo.
	rec := getReq(h, "/runs/"+runID, euReaderHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("leitor EU (mesma região) devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// O selo D6 da leitura grava a RESIDÊNCIA do run (obrigação), além da região do leitor.
	rseal, ok, err := node.WORM.At(context.Background(), readAuditPartition(runID), 1)
	if err != nil || !ok {
		t.Fatalf("selo de leitura D6 devia existir, ok=%t err=%v", ok, err)
	}
	foundResidency := false
	for _, ob := range rseal.Obligations {
		if ob.Type == readResidencyObligation {
			for _, f := range ob.Fields {
				if f == govRegion {
					foundResidency = true
				}
			}
		}
	}
	if !foundResidency {
		t.Fatalf("selo D6 devia carregar a residência %q na obrigação %q, obrigações=%+v",
			govRegion, readResidencyObligation, rseal.Obligations)
	}
}

// ---------------------------------------------------------------------------
// (2) CROSS-REGION RECUSADO em TODAS as vias, não-enumerável (dois sentidos).
// ---------------------------------------------------------------------------

// TestAOS182_CrossRegionDeniedAllReadPaths prova o coração do DEF-202: um leitor de OUTRA região
// (US), COM credencial VÁLIDA do SEU board, é RECUSADO ao ler um run residente noutra (EU) — em
// GET, trajectória E reconstrução — enquanto o leitor EU (mesma região) é servido. A recusa é o
// 404 UNIFORME e não-enumerável (byte-a-byte igual à de um run inexistente) e nunca vaza o run.
func TestAOS182_CrossRegionDeniedAllReadPaths(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	const runID = "run-182-cross"

	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())
	// Semeia o stream de trajectória (existe ⇒ backfill) — para a via SSE ter conteúdo a proteger.
	appendTraj(t, node.EventStore, runID, "s1")

	// SENTIDO A (permit): o leitor EU lê o desfecho.
	okEU := getReq(h, "/runs/"+runID, euReaderHeaders())
	if okEU.Code != http.StatusOK {
		t.Fatalf("leitor EU devia dar 200 (âncora não-vacuosa), veio %d", okEU.Code)
	}

	// SENTIDO B (deny): o leitor US, com board VÁLIDO, é recusado no GET — 404 não-enumerável.
	denyGet := getReq(h, "/runs/"+runID, usReaderHeaders())
	if denyGet.Code != http.StatusNotFound {
		t.Fatalf("leitor US (cross-region) devia dar 404 no GET, veio %d (%s)", denyGet.Code, denyGet.Body.String())
	}
	// NÃO-ENUMERÁVEL: indistinguível de um run INEXISTENTE lido por um leitor autorizado.
	missing := getReq(h, "/runs/run-182-inexistente", euReaderHeaders())
	if missing.Code != http.StatusNotFound || denyGet.Body.String() != missing.Body.String() {
		t.Fatalf("deny cross-region (%q) devia ser byte-a-byte igual a inexistente (%q) — enumerável!",
			denyGet.Body.String(), missing.Body.String())
	}
	if strings.Contains(denyGet.Body.String(), runID) {
		t.Fatalf("a recusa cross-region vaza o RunID: %q", denyGet.Body.String())
	}

	// A recusa vale para a TRAJECTÓRIA (SSE) — 404 antes de qualquer streaming.
	denyTraj := getReq(h, "/runs/"+runID+"/trajectory", usReaderHeaders())
	if denyTraj.Code != http.StatusNotFound {
		t.Fatalf("leitor US devia ser recusado na trajectória (404), veio %d", denyTraj.Code)
	}
	// E para a RECONSTRUÇÃO — 404 antes de o opener ser sequer composto; nunca o claro.
	denyRec := getReq(h, "/runs/"+runID+"/reconstruct", usReaderHeaders())
	if denyRec.Code != http.StatusNotFound {
		t.Fatalf("leitor US devia ser recusado na reconstrução (404), veio %d (%s)", denyRec.Code, denyRec.Body.String())
	}
	if strings.Contains(denyRec.Body.String(), runID) {
		t.Fatalf("a recusa de reconstrução vaza o RunID: %q", denyRec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// (3) FAIL-CLOSED no submit soberano: 403 sem credencial; 503 se o WORM não selar.
// ---------------------------------------------------------------------------

// TestAOS182_SubmitFailClosedWithoutCredential prova que, em modo SOBERANO, o POST /runs EXIGE um
// submissor com região resolvível: sem credencial ⇒ 403; board desconhecido ⇒ 403. Sem residência
// resolvível NÃO se hospeda o run (nem se sela residência órfã).
func TestAOS182_SubmitFailClosedWithoutCredential(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node)

	anon := postReq(h, "/runs", submitRequest{RunID: "run-182-anon", PrincipalNHI: "nhi:x"}, nil)
	if anon.Code != http.StatusForbidden {
		t.Fatalf("submit sem credencial devia dar 403, veio %d (%s)", anon.Code, anon.Body.String())
	}
	bad := postReq(h, "/runs", submitRequest{RunID: "run-182-badboard", PrincipalNHI: "nhi:x"},
		map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBadBoard})
	if bad.Code != http.StatusForbidden {
		t.Fatalf("submit com board desconhecido devia dar 403, veio %d", bad.Code)
	}
	// NENHUMA residência foi selada para os runs recusados (fail-closed sem efeito).
	for _, runID := range []string{"run-182-anon", "run-182-badboard"} {
		if head, _ := node.WORM.Head(context.Background(), readResidencyPartition(runID)); head != 0 {
			t.Fatalf("submit recusado NÃO devia selar residência para %s, head=%d", runID, head)
		}
	}
}

// TestAOS182_SubmitDeniedWhenResidencySealFails prova que a selagem de residência é PRÉ-CONDIÇÃO da
// hospedagem: se o WORM não conseguir selar, o submit é NEGADO fail-closed (503) — nenhum run
// soberano fica legível sem a sua residência durável.
func TestAOS182_SubmitDeniedWhenResidencySealFails(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	regions := govsov.NewRegistry(map[string]string{govBoard: govRegion, govBoardUS: govRegionUS})
	// O gate de leitura usa um WORM que FALHA sempre o Append (também na selagem de residência).
	h, err := NewAPIHandler(svc, node, WithReadSovereignty(regions, sealFailWORM{}))
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}

	rec := postReq(h, "/runs", submitRequest{RunID: "run-182-sealfail", PrincipalNHI: "nhi:x"}, euReaderHeaders())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("submit com WORM em falha devia dar 503, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// O run NÃO foi hospedado (a selagem falhou ANTES de svc.Submit).
	if _, done := svc.Outcome("run-182-sealfail"); done {
		t.Fatalf("um submit negado por selagem NÃO devia ter hospedado o run")
	}
	for _, id := range svc.InProgress() {
		if id == "run-182-sealfail" {
			t.Fatalf("um submit negado por selagem NÃO devia estar em curso")
		}
	}
}

// ---------------------------------------------------------------------------
// (4) SELAGEM DE RESIDÊNCIA IDEMPOTENTE por-RunID (sem poluir o trilho).
// ---------------------------------------------------------------------------

// TestAOS182_ResidencySealIdempotent prova que re-submeter o MESMO RunID — inclusive de OUTRA
// região — NÃO acrescenta um segundo registo de residência: a fronteira em vigor (a primeira
// selada, EU) é imutável e o trilho tamper-evidente não é poluído por retries nem por uma tentativa
// cross-region de re-residenciar o run.
func TestAOS182_ResidencySealIdempotent(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	const runID = "run-182-idem"

	submitHTTPAndWait(t, svc, h, runID, euReaderHeaders())

	// Re-submissão idempotente EU (mesmo run) ⇒ 201, sem novo registo de residência.
	if rec := postReq(h, "/runs", submitRequest{RunID: runID, PrincipalNHI: "nhi:" + runID}, euReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("re-submissão EU devia dar 201 idempotente, veio %d", rec.Code)
	}
	// Re-submissão de OUTRA região (US, board válido) ⇒ 201 idempotente, mas NÃO re-residencia.
	if rec := postReq(h, "/runs", submitRequest{RunID: runID, PrincipalNHI: "nhi:" + runID}, usReaderHeaders()); rec.Code != http.StatusCreated {
		t.Fatalf("re-submissão US devia dar 201 idempotente, veio %d", rec.Code)
	}

	// A partição de residência tem EXACTAMENTE UM registo, e a região em vigor continua a EU.
	head, err := node.WORM.Head(context.Background(), readResidencyPartition(runID))
	if err != nil {
		t.Fatalf("Head(residência): %v", err)
	}
	if head != 1 {
		t.Fatalf("residência devia ter 1 registo (idempotente), veio head=%d — trilho poluído", head)
	}
	region, sealed, err := node.WORM.At(context.Background(), readResidencyPartition(runID), 1)
	if err != nil || !sealed {
		t.Fatalf("At(residência,1): sealed=%t err=%v", sealed, err)
	}
	if region.Resource.Region != govRegion {
		t.Fatalf("residência em vigor devia continuar %q (primeira selada), veio %q", govRegion, region.Resource.Region)
	}

	// Consequência de leitura: o leitor US continua RECUSADO (a re-submissão não moveu a fronteira).
	if rec := getReq(h, "/runs/"+runID, usReaderHeaders()); rec.Code != http.StatusNotFound {
		t.Fatalf("US devia continuar recusado após re-submissão, veio %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// (5) RETRO-COMPAT: run SEM residência selada é servido SEM check cross-region.
// ---------------------------------------------------------------------------

// TestAOS182_UnsealedRunServedWithoutCheck documenta o RESIDUAL de retro-compat (caveat nomeado em
// DEF-202/FECHADO-RESIDUAL): um run que atinge um estado legível SEM ter passado pela criação
// soberana (via in-process/legado, ou pré-existente antes do deploy da soberania) NÃO tem residência
// selada e é servido SEM check cross-region — mesmo em modo soberano. O CONTRASTE com um run selado
// (recusado cross-region no teste anterior) prova que o enforcement liga SÓ quando há residência.
func TestAOS182_UnsealedRunServedWithoutCheck(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	svc, h := newAPI(t, node)
	const runID = "run-182-unsealed"

	// Cria o run DIRECTAMENTE via svc.Submit (contorna handleSubmit ⇒ NÃO sela residência).
	submitAndWait(t, svc, runID)

	// Sem residência selada, o run é servido a QUALQUER leitor autorizado (retro-compat), incluindo
	// um de outra região — é exactamente o caveat que a governança nomeia.
	if head, _ := node.WORM.Head(context.Background(), readResidencyPartition(runID)); head != 0 {
		t.Fatalf("um run criado in-process NÃO devia ter residência selada, head=%d", head)
	}
	if rec := getReq(h, "/runs/"+runID, euReaderHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("run sem residência devia ser legível (retro-compat), veio %d", rec.Code)
	}
	if rec := getReq(h, "/runs/"+runID, usReaderHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("run sem residência devia ser servido SEM check cross-region (retro-compat), veio %d", rec.Code)
	}
}
