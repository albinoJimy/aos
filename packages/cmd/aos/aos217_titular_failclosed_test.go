package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-217 (achado A1 +A7) — FAIL-CLOSED DO TITULAR NO SUBMIT SOBERANO.
//
// O defeito: a cifra por-titular de AOS-093 só corre com Subject != ""
// (nondeterminism_capture.go:246). O titular do run é goal.Principal.NHIID, preenchido do
// CAMPO DE CORPO req.PrincipalNHI (api.go), DESACOPLADO da credencial verificada do
// submissor. Em modo soberano handleSubmit autenticava o submissor mas NÃO exigia nem ligava
// o titular: um POST /runs com principal_nhi:"" (credencial válida) + DurableExecution
// persistia texto do modelo e outputs de tools em CLARO no WAL do Event Store e ficava
// NÃO-shreddable.
//
// O fix DERIVA o titular do submissor VERIFICADO (submitter.principal de readGov.authorize),
// ignorando o corpo auto-declarado — fecha também A7 — e é FAIL-CLOSED: sem principal
// resolvível o submit é recusado (403) e nada persiste.
//
// Estes testes são NÃO-VACUOSOS e correm com -race:
//   (T1) prova a FUGA ANTES do fix — um turno capturado com titular VAZIO deixa a PII em
//        claro no WAL (o mecanismo que o titular-do-corpo="" produzia);
//   (T2) o submit soberano com titular do corpo VAZIO/DECOY deriva o titular do submissor:
//        o conteúdo no WAL está CIFRADO (grep-no-WAL sem PII) e é SHREDDABLE (ErrDecrypt),
//        e o campo de corpo é IGNORADO (fecha A7);
//   (T3) submit soberano SEM titular resolvível (sem credencial) ⇒ 403, NADA persiste;
//   (T4) RETRO-COMPAT: modo legado (readGov nil) inalterado — o titular do corpo é honrado.

// aos217SynthPII é o texto sintético (nunca PII real) que o modelo de teste emite na sua
// resposta e que, sob o fix, NUNCA deve aparecer em claro no WAL do Event Store.
const aos217SynthPII = "RESPOSTA-SINTETICA: dossier de SUJEITO-YARN-7788 no caso SYNTH-217"

// synthPIIModel conclui o run no 1º turno emitindo aos217SynthPII na resposta — o conteúdo
// não-determinístico que o capturer sela por-titular antes do WAL.
type synthPIIModel struct{}

func (m *synthPIIModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{
		Text:  aos217SynthPII,
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// newDurableGovNode compõe um nó com execução DURÁVEL (WAL do ES + WORM em disco) E soberania
// de leitura ligada (uma região) — a topologia mínima para exercitar o titular fail-closed do
// submit soberano ponta-a-ponta (submit HTTP ⇒ execução ⇒ captura selada no WAL ⇒ DSAR).
func newDurableGovNode(t *testing.T, model agentruntime.ModelClient) (*Node, string) {
	t.Helper()
	dir := t.TempDir()
	esPath := filepath.Join(dir, "events.wal")
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.DurableExecution = true
	cfg.EventStorePath = esPath
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (durável+soberano): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node, esPath
}

// submitSovereignAndWait submete um run pela via HTTP (POST /runs, que em modo soberano
// deriva o titular do submissor e sela a residência) com os headers do submissor dados e
// espera que termine, exigindo 201.
func submitSovereignAndWait(t *testing.T, svc *NodeService, h http.Handler, req submitRequest, headers map[string]string) {
	t.Helper()
	rec := postReq(h, "/runs", req, headers)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs (%s) devia dar 201, veio %d (%s)", req.RunID, rec.Code, rec.Body.String())
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(waitCtx, req.RunID); err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%t err=%v", req.RunID, ok, err)
	}
}

// ---------------------------------------------------------------------------
// (T1) FUGA ANTES DO FIX (não-vacuidade): titular VAZIO ⇒ PII em CLARO no WAL.
// ---------------------------------------------------------------------------

// TestNode_AOS217_LeakWithEmptyTitular_Falsifiable prova que a fuga é REAL: um turno capturado
// pelo capturer REAL do nó com titular VAZIO (Subject:"") — exactamente o que o submit soberano
// produzia quando goal.Principal.NHIID vinha do corpo req.PrincipalNHI:"" — NÃO é selado (a
// cifra por-titular é bypassed em nondeterminism_capture.go:246) e o conteúdo fica em CLARO no
// WAL do Event Store. É a âncora de não-vacuidade: sem o fix, um run soberano sem titular vaza.
func TestNode_AOS217_LeakWithEmptyTitular_Falsifiable(t *testing.T) {
	node, esPath := newDurableNode(t)
	const runID = "run-217-leak"

	// Captura um turno com titular VAZIO — o mecanismo que o titular-do-corpo="" produzia.
	tc := agentruntime.TurnCapture{
		RunID:   runID,
		StepID:  "step-000001",
		Turn:    1,
		Subject: "", // titular ausente ⇒ cifra por-titular NÃO corre (o defeito)
		Response: agentruntime.ModelResponse{
			Text:  capSynthPII,
			Final: true,
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		},
	}
	if err := node.Capturer.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture (titular vazio): %v", err)
	}

	// FUGA: o texto sintético aparece em CLARO no WAL — a cifra por-titular foi bypassed.
	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	if !bytes.Contains(wal, []byte(capSynthPII)) {
		t.Fatal("titular vazio devia deixar a PII sintética em CLARO no WAL (prova não-vácua da fuga que o fix fecha)")
	}
	// E o evento persistido NÃO foi selado (sem SealedSubject).
	if _, subj := sealedContentOf(t, node, runID); subj != "" {
		t.Fatalf("um turno com titular vazio NÃO devia ter SealedSubject, veio %q", subj)
	}
}

// ---------------------------------------------------------------------------
// (T2) O FIX: submit soberano DERIVA o titular ⇒ conteúdo CIFRADO + SHREDDABLE, corpo IGNORADO.
// ---------------------------------------------------------------------------

// TestNode_AOS217_SovereignSubmitDerivesTitular prova o coração do fix ponta-a-ponta: um POST
// /runs com o campo de corpo principal_nhi a DISCORDAR (um DECOY) mas credencial VÁLIDA do
// submissor (govReader) resulta num run cujo conteúdo no WAL:
//   - está CIFRADO sob o titular DERIVADO do submissor (govReader), NÃO sob o DECOY do corpo
//     nem em claro (grep-no-WAL) — fecha A1 e A7;
//   - é SHREDDABLE: DSAR erase do titular derivado ⇒ ErrDecrypt (irrecuperável);
//   - o DECOY do corpo é INERTE: um erase do DECOY NÃO afecta o conteúdo (o titular real é o
//     do submissor verificado).
func TestNode_AOS217_SovereignSubmitDerivesTitular(t *testing.T) {
	ctx := context.Background()
	node, esPath := newDurableGovNode(t, &synthPIIModel{})
	svc, h := newAPI(t, node)
	const runID = "run-217-derive"
	const decoy = "nhi:DECOY-ATACANTE-217"

	// Submit soberano com titular do corpo = DECOY (auto-declarado), credencial VÁLIDA (govReader).
	submitSovereignAndWait(t, svc, h,
		submitRequest{RunID: runID, Objective: "trabalho soberano", PrincipalNHI: decoy},
		govHeaders())

	// (a) CONFIDENCIALIDADE: a PII sintética NÃO está em claro no WAL (foi selada por-titular).
	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	for _, needle := range []string{aos217SynthPII, "SUJEITO-YARN-7788"} {
		if bytes.Contains(wal, []byte(needle)) {
			t.Fatalf("WAL contém conteúdo em CLARO %q — o submit soberano não selou o titular (A1)", needle)
		}
	}

	// (b) O titular DERIVADO é o do submissor verificado (govReader), NÃO o DECOY do corpo (A7).
	sealed, subject := sealedContentOf(t, node, runID)
	if subject != govReader {
		t.Fatalf("titular selado devia ser o submissor verificado %q (derivado), veio %q — corpo não foi ignorado (A7)", govReader, subject)
	}
	if subject == decoy {
		t.Fatal("titular selado NÃO devia ser o DECOY auto-declarado do corpo (A7)")
	}
	if len(sealed) == 0 {
		t.Fatal("o conteúdo do run devia estar selado (SealedContent não-vazio)")
	}

	// (c) Recuperável ANTES do erase sob o titular derivado (não-vácuo) e contém a PII.
	plain, err := audit.OpenContent(node.DSARVault, govReader, sealed)
	if err != nil {
		t.Fatalf("OpenContent sob o titular derivado antes do erase: %v", err)
	}
	if !bytes.Contains(plain, []byte(aos217SynthPII)) {
		t.Fatal("o conteúdo decifrado sob o titular derivado devia conter a PII sintética (prova não-vácua)")
	}

	// (d) O DECOY do corpo é INERTE: um erase do DECOY NÃO torna o conteúdo irrecuperável.
	if _, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-decoy", SubjectID: decoy}); err != nil {
		t.Fatalf("DSAR erase do DECOY: %v", err)
	}
	if _, err := audit.OpenContent(node.DSARVault, govReader, sealed); err != nil {
		t.Fatalf("após erase do DECOY o conteúdo devia continuar recuperável sob o titular real: %v", err)
	}

	// (e) SHREDDABLE: o erase do titular DERIVADO torna o conteúdo irrecuperável (ErrDecrypt).
	if _, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-real", SubjectID: govReader}); err != nil {
		t.Fatalf("DSAR erase do titular derivado: %v", err)
	}
	if _, err := audit.OpenContent(node.DSARVault, govReader, sealed); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("após erase do titular derivado, OpenContent devia falhar com ErrDecrypt, deu: %v", err)
	}

	// (f) O WAL não foi reescrito — o texto continua ausente (só a KEK morreu).
	walAfter, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL após erase: %v", err)
	}
	if bytes.Contains(walAfter, []byte(aos217SynthPII)) {
		t.Fatal("após erase o texto sintético apareceu no WAL — impossível (nunca foi cifrado?)")
	}
}

// TestNode_AOS217_SovereignSubmitEmptyBodyStillSealed prova que o titular do corpo VAZIO — o
// caso A1 exacto — já não vaza: o submit soberano deriva o titular do submissor e o conteúdo é
// selado (nada em claro no WAL), sem depender de o cliente declarar principal_nhi.
func TestNode_AOS217_SovereignSubmitEmptyBodyStillSealed(t *testing.T) {
	node, esPath := newDurableGovNode(t, &synthPIIModel{})
	svc, h := newAPI(t, node)
	const runID = "run-217-emptybody"

	submitSovereignAndWait(t, svc, h,
		submitRequest{RunID: runID, Objective: "soberano sem titular no corpo", PrincipalNHI: ""},
		govHeaders())

	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	if bytes.Contains(wal, []byte(aos217SynthPII)) {
		t.Fatal("titular do corpo VAZIO devia ainda assim ser selado (derivado do submissor) — A1 não fechado")
	}
	if _, subject := sealedContentOf(t, node, runID); subject != govReader {
		t.Fatalf("titular selado devia ser o submissor derivado %q mesmo com corpo vazio, veio %q", govReader, subject)
	}
}

// ---------------------------------------------------------------------------
// (T3) FAIL-CLOSED: submit soberano SEM titular resolvível ⇒ 403, NADA persiste.
// ---------------------------------------------------------------------------

// TestNode_AOS217_FailClosedNoResolvableTitular prova o FAIL-CLOSED: em modo soberano, um submit
// sem credencial resolvível NÃO tem titular sob o qual cifrar — é RECUSADO (403) e NADA persiste
// (sem residência selada, run não hospedado, sem stream no WAL). É o "sem titular resolvível ⇒
// recusado": sem credencial não há principal a derivar.
func TestNode_AOS217_FailClosedNoResolvableTitular(t *testing.T) {
	node, esPath := newDurableGovNode(t, &synthPIIModel{})
	svc, h := newAPI(t, node)
	const runID = "run-217-notitular"

	// Anónimo (sem headers de leitura) ⇒ sem principal resolvível ⇒ 403.
	rec := postReq(h, "/runs", submitRequest{RunID: runID, PrincipalNHI: "nhi:auto-declarado"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("submit soberano sem credencial devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// NADA persiste: sem residência selada, run não hospedado, sem stream no WAL.
	if head, _ := node.WORM.Head(context.Background(), readResidencyPartition(runID)); head != 0 {
		t.Fatalf("submit recusado NÃO devia selar residência, head=%d", head)
	}
	if _, done := svc.Outcome(runID); done {
		t.Fatal("um submit recusado NÃO devia ter hospedado o run")
	}
	for _, id := range svc.InProgress() {
		if id == runID {
			t.Fatal("um submit recusado NÃO devia estar em curso")
		}
	}
	// O WAL não contém nenhum conteúdo do run recusado (nunca chegou à execução/captura).
	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	if bytes.Contains(wal, []byte(aos217SynthPII)) {
		t.Fatal("um submit recusado NÃO devia ter persistido conteúdo do run no WAL")
	}
}

// ---------------------------------------------------------------------------
// (T4) RETRO-COMPAT: modo LEGADO (readGov nil) inalterado — titular do corpo honrado.
// ---------------------------------------------------------------------------

// TestNode_AOS217_LegacyModeTitularUnforced prova que o fix liga SÓ em modo soberano: num nó
// SEM soberania composta (readGov nil, sem BoardRegions), o submit continua a honrar o titular
// do corpo req.PrincipalNHI — o conteúdo é selado sob ESSE titular (não sob nenhum submissor,
// que não existe). Runs fora de produção/soberania não são forçados (retro-compat).
func TestNode_AOS217_LegacyModeTitularUnforced(t *testing.T) {
	dir := t.TempDir()
	esPath := filepath.Join(dir, "events.wal")
	cfg := tnBaseConfig()
	cfg.Model = &synthPIIModel{}
	cfg.DurableExecution = true
	cfg.EventStorePath = esPath
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	// SEM BoardRegions ⇒ readGov nil ⇒ modo legado.
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (legado durável): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, h := newAPI(t, node)
	const runID = "run-217-legacy"
	const legacyTitular = "nhi:titular-legado-217"

	// Sem readGov, o submit NÃO exige credencial e honra o titular do corpo.
	submitSovereignAndWait(t, svc, h,
		submitRequest{RunID: runID, Objective: "run legado", PrincipalNHI: legacyTitular},
		nil)

	// O conteúdo foi selado sob o titular DO CORPO (comportamento legado inalterado).
	if _, subject := sealedContentOf(t, node, runID); subject != legacyTitular {
		t.Fatalf("modo legado devia selar sob o titular do corpo %q, veio %q — retro-compat quebrada", legacyTitular, subject)
	}
	// E nada em claro no WAL (o titular do corpo não-vazio já selava em AOS-093).
	wal, err := os.ReadFile(esPath)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	if bytes.Contains(wal, []byte(aos217SynthPII)) {
		t.Fatal("modo legado com titular do corpo não-vazio devia selar o conteúdo")
	}
}
