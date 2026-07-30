package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-214 (EPIC-18) — PROVAS FALSIFICÁVEIS AO NÍVEL DO NÓ do REPLAY SOBERANO de conteúdo selado.
// Cada teste é NÃO-VACUOSO e prova os DOIS sentidos. Correr SEMPRE com -race.
//
//   - AUTORIZADO: um leitor autorizado por soberania (board→região + credencial) reconstrói um run
//     selado e obtém o CONTEÚDO REAL DECIFRADO; a leitura é SELADA no WORM (D6) sem PII.
//   - NÃO-AUTORIZADO: (a) o endpoint com board errado/sem credencial ⇒ 404 não-enumerável (nunca o
//     claro); (b) a reconstrução SEM o opener composto ⇒ ErrPayloadAccessDenied (nunca o claro).
//   - SHRED AGUENTA O REPLAY: depois do /dsar/erase, MESMO o leitor autorizado obtém ErrDecrypt.
//   - LEGAL HOLD: um titular sob hold NÃO é shredded ⇒ a reconstrução autorizada reconstrói.

// aos214SynthSecret é o conteúdo sintético do run que NUNCA deve fugir a um leitor não autorizado
// nem a um selo WORM.
const aos214SynthSecret = "PROMPT-214: contactar SUJEITO-ZORG-214 sobre caso SYNTH-214"

// newGovDurableNode arranca um nó com execução durável (o capturer cifra por-titular no ES) E
// soberania de leitura ligada (o gate D7+D6 autentica o leitor) — a composição que a reconstrução
// soberana exige.
func newGovDurableNode(t *testing.T) *Node {
	t.Helper()
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (gov+durable): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// authorizedReaderEngine compõe o motor de replay com o opener por-titular do nó LIGADO ATRÁS do
// gate (accessor com o escopo soberano) — modela a composição que o endpoint faz DEPOIS de o gate
// D7 autorizar. É o caminho de reconstrução IN-PROCESS do leitor autorizado.
func authorizedReaderEngine(t *testing.T, node *Node) *replay.ReplayEngine {
	t.Helper()
	acc := replay.Accessor{Principal: govReader, Scopes: []string{replay.DefaultSovereignContentScope}}
	e, err := replay.NewEngine(node.EventStore, replay.WithContentOpener(node.contentOpener, acc))
	if err != nil {
		t.Fatalf("NewEngine (autorizado): %v", err)
	}
	return e
}

// TestNode_AOS214_AuthorizedReaderDecrypts prova o PERMIT via o ENDPOINT soberano: um leitor
// autorizado reconstrói o run selado e obtém o conteúdo decifrado (200); a leitura é selada no WORM
// (D6) sem PII.
func TestNode_AOS214_AuthorizedReaderDecrypts(t *testing.T) {
	node := newGovDurableNode(t)
	_, h := newAPI(t, node)
	const subject, runID = "nhi:agent-214-ok", "run-214-ok"
	captureSynthetic(t, node, subject, runID, aos214SynthSecret, "TOOL-OUT-214-OK")

	// ANTES (não-vácuo): a partição de leitura está vazia — o selo é do read.
	part := readAuditPartition(runID)
	if head, _ := node.WORM.Head(context.Background(), part); head != 0 {
		t.Fatalf("particao de leitura devia estar vazia antes da reconstrução, head=%d", head)
	}

	rec := getReq(h, "/runs/"+runID+"/reconstruct", govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("reconstrução autorizada devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp reconstructResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta nao descodifica: %v", err)
	}
	if len(resp.Turns) != 1 {
		t.Fatalf("esperava 1 turno reconstruído, obtive %d", len(resp.Turns))
	}
	if resp.Turns[0].Text != aos214SynthSecret {
		t.Fatalf("conteúdo decifrado errado: %q", resp.Turns[0].Text)
	}
	sawOut := false
	for _, o := range resp.Turns[0].ToolOutputs {
		if o == "TOOL-OUT-214-OK" {
			sawOut = true
		}
	}
	if !sawOut {
		t.Fatalf("output de tool decifrado ausente: %+v", resp.Turns[0].ToolOutputs)
	}

	// (D6) UM selo foi encadeado, a cadeia VERIFICA, e regista quem/run/região/capability SEM PII.
	head, err := node.WORM.Head(context.Background(), part)
	if err != nil || head < 1 {
		t.Fatalf("apos reconstrução devia haver >=1 selo, head=%d err=%v", head, err)
	}
	if err := audit.Verify(context.Background(), node.WORM, part, 1, head); err != nil {
		t.Fatalf("cadeia do selo de reconstrução NAO verifica: %v", err)
	}
	seal, ok, err := node.WORM.At(context.Background(), part, 1)
	if err != nil || !ok {
		t.Fatalf("At(%s,1): ok=%t err=%v", part, ok, err)
	}
	if seal.Capability != capReadReconstruct {
		t.Fatalf("capability do selo devia ser %q, veio %q", capReadReconstruct, seal.Capability)
	}
	if seal.Principal.NHIID != govReader || seal.Resource.Region != govRegion || seal.Resource.Value != runID {
		t.Fatalf("selo devia registar leitor/regiao/run, veio %q/%q/%q", seal.Principal.NHIID, seal.Resource.Region, seal.Resource.Value)
	}
	assertNoPIIInPartition(t, node.WORM, part)
	// Defesa em profundidade: o conteúdo sintético NUNCA aparece num selo de conformidade.
	recs, _ := node.WORM.Read(context.Background(), part, 1, head)
	for _, rc := range recs {
		blob, _ := json.Marshal(rc)
		if bytes.Contains(blob, []byte(aos214SynthSecret)) {
			t.Fatalf("o selo de reconstrução vaza o conteúdo sintético — PII num selo!")
		}
	}
}

// TestNode_AOS214_UnauthorizedReaderDenied prova o DENY fail-closed nos DOIS sentidos:
//   - o endpoint com board DESCONHECIDO ⇒ 404 (não-enumerável), sem conteúdo;
//   - a reconstrução IN-PROCESS SEM opener composto ⇒ ErrPayloadAccessDenied (nunca o claro).
func TestNode_AOS214_UnauthorizedReaderDenied(t *testing.T) {
	node := newGovDurableNode(t)
	_, h := newAPI(t, node)
	const subject, runID = "nhi:agent-214-deny", "run-214-deny"
	captureSynthetic(t, node, subject, runID, aos214SynthSecret, "TOOL-OUT-214-DENY")

	// (a) ENDPOINT: board desconhecido ⇒ 404, e o corpo NÃO carrega o conteúdo.
	denied := getReq(h, "/runs/"+runID+"/reconstruct", map[string]string{
		HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBadBoard,
	})
	if denied.Code != http.StatusNotFound {
		t.Fatalf("board desconhecido devia dar 404, veio %d (%s)", denied.Code, denied.Body.String())
	}
	if bytes.Contains(denied.Body.Bytes(), []byte(aos214SynthSecret)) {
		t.Fatalf("a negação vaza o conteúdo decifrado: %q", denied.Body.String())
	}
	// A negação de um run existente é indistinguível da de um run inexistente (não-enumerável).
	missing := getReq(h, "/runs/run-214-inexistente/reconstruct", govHeaders())
	if missing.Code != http.StatusNotFound || denied.Body.String() != missing.Body.String() {
		t.Fatalf("deny (%q) devia ser indistinguível de inexistente (%q)", denied.Body.String(), missing.Body.String())
	}

	// (b) IN-PROCESS: motor SEM opener composto ⇒ o conteúdo selado é NEGADO fail-closed.
	unauth, err := replay.NewEngine(node.EventStore) // sem WithContentOpener
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	turns, err := unauth.Reconstruct(context.Background(), runID)
	if !errors.Is(err, replay.ErrPayloadAccessDenied) {
		t.Fatalf("sem opener a reconstrução devia dar ErrPayloadAccessDenied, deu: %v (turns=%d)", err, len(turns))
	}
}

// TestNode_AOS214_ShredSurvivesReplay prova que o SHRED AGUENTA O REPLAY: depois do /dsar/erase
// (KEK destruída), MESMO o leitor autorizado obtém ErrDecrypt na reconstrução — o direito ao
// apagamento vale também contra o replay. Prova IN-PROCESS e via o endpoint (410 Gone).
func TestNode_AOS214_ShredSurvivesReplay(t *testing.T) {
	ctx := context.Background()
	node := newGovDurableNode(t)
	_, h := newAPI(t, node)
	const subject, runID = "nhi:agent-214-shred", "run-214-shred"
	captureSynthetic(t, node, subject, runID, aos214SynthSecret, "TOOL-OUT-214-SHRED")

	// ANTES do shred (não-vácuo): o leitor autorizado decifra.
	before := authorizedReaderEngine(t, node)
	if turns, err := before.Reconstruct(ctx, runID); err != nil || len(turns) != 1 {
		t.Fatalf("antes do shred a reconstrução autorizada devia suceder: turns=%d err=%v", len(turns), err)
	}

	// POST /dsar/erase — destrói a KEK por-titular via o fluxo DSAR real.
	res, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-214", SubjectID: subject})
	if err != nil || res.Blocked {
		t.Fatalf("erase devia suceder sem hold: blocked=%t err=%v", res.Blocked, err)
	}

	// DEPOIS do shred: MESMO o autorizado falha na decifração (nunca claro).
	after := authorizedReaderEngine(t, node)
	turns, rerr := after.Reconstruct(ctx, runID)
	if !errors.Is(rerr, audit.ErrDecrypt) {
		t.Fatalf("depois do shred a reconstrução autorizada devia dar ErrDecrypt, deu: %v (turns=%d)", rerr, len(turns))
	}
	// Via o endpoint: 410 Gone (o conteúdo foi apagado).
	rec := getReq(h, "/runs/"+runID+"/reconstruct", govHeaders())
	if rec.Code != http.StatusGone {
		t.Fatalf("reconstrução de um titular apagado devia dar 410, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(aos214SynthSecret)) {
		t.Fatalf("a resposta 410 vaza o conteúdo: %q", rec.Body.String())
	}
}

// TestNode_AOS214_LegalHoldReconstructs prova que um titular sob LEGAL HOLD não é shredded, pelo que
// a reconstrução autorizada RECONSTRÓI normalmente (o hold preserva a reconstruibilidade).
func TestNode_AOS214_LegalHoldReconstructs(t *testing.T) {
	ctx := context.Background()
	node := newGovDurableNode(t)
	const subject, runID = "nhi:agent-214-held", "run-214-held"
	captureSynthetic(t, node, subject, runID, aos214SynthSecret, "TOOL-OUT-214-HELD")

	// O capturer ligou subject→runID no DSARIndex; retém a PARTIÇÃO (o stream do run).
	node.DSARHolds.HoldPartition(runID)

	// Um erase sob hold é BLOQUEADO (a KEK sobrevive).
	res, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-held", SubjectID: subject})
	if !errors.Is(err, dsar.ErrLegalHold) {
		t.Fatalf("erase sob hold devia dar ErrLegalHold, deu: %v (res=%+v)", err, res)
	}

	// A reconstrução autorizada CONTINUA a reconstruir (o hold preservou a chave).
	e := authorizedReaderEngine(t, node)
	turns, rerr := e.Reconstruct(ctx, runID)
	if rerr != nil || len(turns) != 1 {
		t.Fatalf("sob hold a reconstrução autorizada devia suceder: turns=%d err=%v", len(turns), rerr)
	}
	if turns[0].Response.Text != aos214SynthSecret {
		t.Fatalf("conteúdo reconstruído sob hold errado: %q", turns[0].Response.Text)
	}
}
