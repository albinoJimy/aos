package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-268/AOS-072 — VERIFICAÇÃO ANCORADA DO WORM no arranque. Prova FALSIFICÁVEL de que o nó,
// além da re-verificação de hash-chain SEM CHAVE de AOS-221 (que NÃO apanha a truncatura do tail
// nem a reescrita da génese), verifica o WORM contra uma ÂNCORA ASSINADA out-of-band com um piso
// de frescura persistido — e ABORTA fail-closed quando a âncora não valida.
//
// A vacuidade é fechada em dois sentidos: os testes de detecção mostram que o MESMO WORM
// truncado/rollback arranca SEM a âncora (AOS-221 sozinho não vê o vector) e ABORTA COM ela.

const aos268Partition = "aos268.worm"

// aos268BuildDurableWORM constrói um WORM DURÁVEL com n registos na partição de teste e devolve o
// caminho do WAL. O store é fechado no fim (o Bootstrap reabre o mesmo caminho).
func aos268BuildDurableWORM(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worm.wal")
	fs, err := audit.OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := fs.Append(ctx, audit.AuditRecord{
			Partition:  aos268Partition,
			Decision:   audit.DecisionAllow,
			Capability: "cap:aos268.record",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := closeIfCloser(fs); err != nil {
		t.Fatalf("close store: %v", err)
	}
	return path
}

// aos268Seal abre o WAL, sela um checkpoint no audit_seq pedido com um signer efémero e volta a
// fechar. Devolve o checkpoint assinado e a pubkey (trust-anchor). É o lado de SELAGEM que em
// produção corre OUT-OF-PROCESS (chave privada fora do runtime) — aqui simulado para o teste.
func aos268Seal(t *testing.T, path string, seq uint64) (audit.Checkpoint, ed25519.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	fs, err := audit.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen para selar: %v", err)
	}
	cp, err := signer.Seal(context.Background(), fs, aos268Partition, seq)
	if err != nil {
		_ = closeIfCloser(fs)
		t.Fatalf("seal seq=%d: %v", seq, err)
	}
	if err := closeIfCloser(fs); err != nil {
		t.Fatalf("close apos selar: %v", err)
	}
	return cp, signer.Public()
}

// aos268TruncateLastFrame remove o ÚLTIMO frame do WAL (comprimento+payload+CRC), numa fronteira
// de frame — deixa uma cadeia MAIS CURTA mas internamente íntegra (o que AOS-221 aceita). É a
// truncatura do tail em disco.
func aos268TruncateLastFrame(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	off := 0
	lastStart := -1
	for off+8 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[off : off+4]))
		if n == 0 || off+4+n+4 > len(data) {
			break
		}
		lastStart = off
		off += 4 + n + 4
	}
	if lastStart < 0 {
		t.Fatalf("nenhum frame encontrado no WAL")
	}
	if err := os.WriteFile(path, data[:lastStart], 0o600); err != nil {
		t.Fatalf("truncar wal: %v", err)
	}
}

// TestNode_WORMAnchor_IntegroAncora — o WORM íntegro, com a âncora sobre o head e o piso ==
// head, ARRANCA e o banner declara ANCORADA (AC1/AC2). Não é vácuo: os testes de detecção abaixo
// mostram o mesmo caminho a ABORTAR quando a âncora não vale.
func TestNode_WORMAnchor_IntegroAncora(t *testing.T) {
	ctx := context.Background()
	path := aos268BuildDurableWORM(t, 3)
	cp, pub := aos268Seal(t, path, 3)

	cfg := tnBaseConfig()
	cfg.WORMPath = path
	cfg.WORMAnchor = &WormAnchor{Public: pub, Checkpoints: []audit.Checkpoint{cp}, ExpectedHeads: map[string]uint64{cp.Partition: 3}}

	var banner bytes.Buffer
	node, err := Bootstrap(ctx, cfg, &banner)
	if err != nil {
		t.Fatalf("bootstrap com WORM ancorado integro NAO devia falhar: %v", err)
	}
	defer node.Close()
	if !strings.Contains(banner.String(), "verificacao ancorada do WORM (AOS-268/AOS-072): ANCORADA") {
		t.Fatalf("banner nao declarou ANCORADA:\n%s", banner.String())
	}
}

// TestNode_WORMAnchor_TruncaturaDoTailDetectada — a truncatura do tail em disco é INVISÍVEL a
// AOS-221 (a cadeia curta re-encadeia) mas a verificação ancorada apanha-a: a âncora sela o head
// N, o store passou a ter N-1, logo `to`==N ultrapassa o head ⇒ audit.ErrRangeBeyondHead e o
// arranque ABORTA (AC2/AC3). Sentido duplo: SEM a âncora o mesmo WORM truncado ARRANCA (prova
// que o vector é real e que AOS-221 sozinho o cala).
func TestNode_WORMAnchor_TruncaturaDoTailDetectada(t *testing.T) {
	ctx := context.Background()
	path := aos268BuildDurableWORM(t, 3)
	cp, pub := aos268Seal(t, path, 3)

	// Truncatura do tail: head passa de 3 para 2.
	aos268TruncateLastFrame(t, path)

	// (control) SEM a âncora: AOS-221 aceita a cadeia curta ⇒ arranca.
	t.Run("sem_ancora_arranca", func(t *testing.T) {
		cfg := tnBaseConfig()
		cfg.WORMPath = path
		node, err := Bootstrap(ctx, cfg, io.Discard)
		if err != nil {
			t.Fatalf("sem ancora, o WORM truncado devia arrancar (AOS-221 nao ve o tail): %v", err)
		}
		_ = node.Close()
	})

	// (prova) COM a âncora: aborta fail-closed.
	t.Run("com_ancora_aborta", func(t *testing.T) {
		cfg := tnBaseConfig()
		cfg.WORMPath = path
		cfg.WORMAnchor = &WormAnchor{Public: pub, Checkpoints: []audit.Checkpoint{cp}, ExpectedHeads: map[string]uint64{cp.Partition: 3}}
		node, err := Bootstrap(ctx, cfg, io.Discard)
		if err == nil {
			_ = node.Close()
			t.Fatalf("o arranque devia ABORTAR com o tail truncado sob a ancora")
		}
		if !errors.Is(err, audit.ErrRangeBeyondHead) {
			t.Fatalf("esperava audit.ErrRangeBeyondHead (head caiu abaixo da ancora), veio: %v", err)
		}
	})
}

// TestNode_WORMAnchor_RollbackStaleRejeitado — um checkpoint LEGÍTIMO mas ANTERIOR (selado num
// audit_seq abaixo do piso persistido), reapresentado para mascarar a truncatura dos registos
// posteriores, é rejeitado com audit.ErrCheckpointStale ANTES sequer de tocar o store (AC2/AC3).
func TestNode_WORMAnchor_RollbackStaleRejeitado(t *testing.T) {
	ctx := context.Background()
	path := aos268BuildDurableWORM(t, 3)
	// Checkpoint ANTIGO selado no seq=2, enquanto a cadeia real está no head=3 e o piso é 3.
	cpOld, pub := aos268Seal(t, path, 2)

	cfg := tnBaseConfig()
	cfg.WORMPath = path
	cfg.WORMAnchor = &WormAnchor{Public: pub, Checkpoints: []audit.Checkpoint{cpOld}, ExpectedHeads: map[string]uint64{cpOld.Partition: 3}}

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err == nil {
		_ = node.Close()
		t.Fatalf("o arranque devia ABORTAR com um checkpoint anterior ao piso (rollback)")
	}
	if !errors.Is(err, audit.ErrCheckpointStale) {
		t.Fatalf("esperava audit.ErrCheckpointStale, veio: %v", err)
	}
}

// TestNode_WORMAnchor_CheckpointForjadoRejeitado — um checkpoint cuja assinatura NÃO valida
// contra o trust-anchor apresentado (aqui: um trust-anchor DIFERENTE do selador) é rejeitado com
// audit.ErrCheckpointSignature — a âncora forjada nunca ancora nada (AC2/AC3).
func TestNode_WORMAnchor_CheckpointForjadoRejeitado(t *testing.T) {
	ctx := context.Background()
	path := aos268BuildDurableWORM(t, 3)
	cp, _ := aos268Seal(t, path, 3)

	// Trust-anchor ERRADO: uma pubkey que não corresponde ao selador do checkpoint.
	wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen wrong key: %v", err)
	}

	cfg := tnBaseConfig()
	cfg.WORMPath = path
	cfg.WORMAnchor = &WormAnchor{Public: wrongPub, Checkpoints: []audit.Checkpoint{cp}, ExpectedHeads: map[string]uint64{cp.Partition: 3}}

	node, err := Bootstrap(ctx, cfg, io.Discard)
	if err == nil {
		_ = node.Close()
		t.Fatalf("o arranque devia ABORTAR com um checkpoint que nao valida contra o anchor")
	}
	if !errors.Is(err, audit.ErrCheckpointSignature) {
		t.Fatalf("esperava audit.ErrCheckpointSignature, veio: %v", err)
	}
}

// TestParseWormAnchorFromEnv — a fronteira de ambiente é fail-closed no molde de AOS-220:
// ausência total ⇒ nil (não-ancorado); config PARCIAL ⇒ ErrWormAnchorIncomplete; malformações
// ⇒ os erros dedicados; config completa e bem-formada ⇒ o material parseado.
func TestParseWormAnchorFromEnv(t *testing.T) {
	// Prepara um checkpoint bem-formado em ficheiro e a pubkey em hex.
	path := aos268BuildDurableWORM(t, 2)
	cp, pub := aos268Seal(t, path, 2)
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("marshal cp: %v", err)
	}
	cpFile := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(cpFile, cpJSON, 0o600); err != nil {
		t.Fatalf("write cp file: %v", err)
	}
	anchorHex := hex.EncodeToString(pub)

	clearWormEnv := func(t *testing.T) {
		t.Setenv("AOS_WORM_TRUST_ANCHOR", "")
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", "")
		t.Setenv("AOS_WORM_EXPECTED_HEAD", "")
		t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", "")
	}

	// Ficheiro de pisos no formato NOVO: {"particao": audit_seq} — o que
	// `aos-issuer worm-seal --heads` emite.
	headsFile := filepath.Join(t.TempDir(), "heads.json")
	if err := os.WriteFile(headsFile, []byte(fmt.Sprintf("{%q: 2}", cp.Partition)), 0o600); err != nil {
		t.Fatalf("write heads file: %v", err)
	}
	headsZero := filepath.Join(t.TempDir(), "heads-zero.json")
	if err := os.WriteFile(headsZero, []byte(fmt.Sprintf("{%q: 0}", cp.Partition)), 0o600); err != nil {
		t.Fatalf("write heads-zero: %v", err)
	}

	t.Run("ausencia_total_nao_ancora", func(t *testing.T) {
		clearWormEnv(t)
		got, err := parseWormAnchorFromEnv()
		if err != nil || got != nil {
			t.Fatalf("ausencia total devia dar (nil,nil), veio (%v,%v)", got, err)
		}
	})

	t.Run("parcial_aborta", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", anchorHex) // só uma das três
		if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrWormAnchorIncomplete) {
			t.Fatalf("esperava ErrWormAnchorIncomplete, veio: %v", err)
		}
	})

	t.Run("anchor_malformado", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", "nao-e-hex")
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
		t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsFile)
		if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrBadWormTrustAnchor) {
			t.Fatalf("esperava ErrBadWormTrustAnchor, veio: %v", err)
		}
	})

	t.Run("checkpoint_ilegivel", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", anchorHex)
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", filepath.Join(t.TempDir(), "inexistente.json"))
		t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsFile)
		if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrBadWormCheckpoint) {
			t.Fatalf("esperava ErrBadWormCheckpoint, veio: %v", err)
		}
	})

	t.Run("head_invalido", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", anchorHex)
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
		t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsZero) // piso 0 e invalido
		if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrBadWormExpectedHead) {
			t.Fatalf("esperava ErrBadWormExpectedHead, veio: %v", err)
		}
	})

	t.Run("completo_parseia", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", anchorHex)
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
		t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsFile)
		got, err := parseWormAnchorFromEnv()
		if err != nil {
			t.Fatalf("config completa devia parsear: %v", err)
		}
		if got == nil || len(got.Checkpoints) != 1 || got.ExpectedHeads[cp.Partition] != 2 || got.Checkpoints[0].AuditSeq != 2 || !got.Public.Equal(pub) {
			t.Fatalf("material parseado incoerente: %+v", got)
		}
	})
}

// TestWormAnchorPostureBanner — as duas dobras da linha de banner declaram estados DIFERENTES e
// ambos verdadeiros (mesma disciplina de posture_banner.go).
func TestWormAnchorPostureBanner(t *testing.T) {
	anchored := strings.Join(wormAnchorPostureBanner(1, 1), "\n")
	if !strings.Contains(anchored, "ANCORADA") || strings.Contains(anchored, "NAO ANCORADA") {
		t.Fatalf("dobra composta devia declarar ANCORADA:\n%s", anchored)
	}
	notAnchored := strings.Join(wormAnchorPostureBanner(0, 0), "\n")
	if !strings.Contains(notAnchored, "NAO ANCORADA") {
		t.Fatalf("dobra nao-composta devia declarar NAO ANCORADA:\n%s", notAnchored)
	}
	// A dobra não-ancorada NOMEIA as três envs que fecham o vector.
	for _, env := range []string{"AOS_WORM_TRUST_ANCHOR", "AOS_WORM_CHECKPOINT_FILE", "AOS_WORM_EXPECTED_HEAD"} {
		if !strings.Contains(notAnchored, env) {
			t.Fatalf("dobra nao-ancorada nao nomeou %s:\n%s", env, notAnchored)
		}
	}
}

// ---------------------------------------------------------------------------
// COBERTURA MULTI-PARTIÇÃO.
//
// O campo era SINGULAR e o nó ancorava, quando muito, UMA partição — o WORM de produção tinha 108.
// O inventário dizia «ancoraria 1 em 108»; com o selador em falta, ancorava zero.
// ---------------------------------------------------------------------------

// TestAncoraCobreVariasParticoes prova que o consumo deixou de ser singular.
func TestAncoraCobreVariasParticoes(t *testing.T) {
	if _, err := parseCheckpoints([]byte(`[{"Partition":"a","AuditSeq":1},{"Partition":"b","AuditSeq":2}]`)); err != nil {
		t.Fatalf("o array que `worm-seal` emite nao parseia: %v", err)
	}
	cps, err := parseCheckpoints([]byte(`[{"Partition":"a","AuditSeq":1},{"Partition":"b","AuditSeq":2}]`))
	if err != nil || len(cps) != 2 {
		t.Fatalf("esperava 2 checkpoints, veio %d (%v)", len(cps), err)
	}
	// CONTROLO: o objecto ÚNICO — a forma que o contrato singular documentava — continua a
	// parsear. Sem este ramo, a mudança partiria quem tivesse seguido a documentação anterior.
	um, err := parseCheckpoints([]byte(`{"Partition":"a","AuditSeq":1}`))
	if err != nil || len(um) != 1 {
		t.Fatalf("o objecto unico deixou de parsear: %d (%v)", len(um), err)
	}
}

// TestPisoEmFaltaAbortaPorParticao — uma partição com checkpoint e SEM piso é recusada.
//
// Sem esta guarda a âncora dessa partição seria verificada sem frescura, e um checkpoint legítimo
// mas ANTERIOR passaria — que é exactamente o rollback que o piso existe para recusar. O buraco
// abrir-se-ia por OMISSÃO, na partição que alguém se esquecesse de listar.
func TestPisoEmFaltaAbortaPorParticao(t *testing.T) {
	path := aos268BuildDurableWORM(t, 2)
	cp, pub := aos268Seal(t, path, 2)

	dir := t.TempDir()
	cpFile := filepath.Join(dir, "cp.json")
	bs, _ := json.Marshal([]audit.Checkpoint{cp})
	if err := os.WriteFile(cpFile, bs, 0o600); err != nil {
		t.Fatal(err)
	}
	// Pisos com OUTRA partição — a ancorada fica sem piso.
	headsFile := filepath.Join(dir, "heads.json")
	if err := os.WriteFile(headsFile, []byte(`{"outra-particao": 9}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AOS_WORM_TRUST_ANCHOR", hex.EncodeToString(pub))
	t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
	t.Setenv("AOS_WORM_EXPECTED_HEAD", "")
	t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsFile)

	if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrBadWormExpectedHead) {
		t.Fatalf("particao ancorada SEM piso devia abortar, veio: %v", err)
	}

	// CONTROLO: com o piso da partição certa, parseia.
	if err := os.WriteFile(headsFile, []byte(fmt.Sprintf("{%q: 2}", cp.Partition)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseWormAnchorFromEnv(); err != nil {
		t.Fatalf("com o piso correcto devia parsear: %v", err)
	}
}

// TestEnvObsoletaAbortaEmVozAlta — `AOS_WORM_EXPECTED_HEAD` (singular) já não é o contrato, e
// ignorá-la seria a pior das saídas.
//
// Uma env obsoleta ignorada deixa o operador convencido de que ancorou o que não ancorou. É a
// forma de falha que este projecto já pagou uma vez, com o banner a dizer ORACULO LIGADO sobre uma
// configuração inerte.
func TestEnvObsoletaAbortaEmVozAlta(t *testing.T) {
	t.Setenv("AOS_WORM_TRUST_ANCHOR", "")
	t.Setenv("AOS_WORM_CHECKPOINT_FILE", "")
	t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", "")
	t.Setenv("AOS_WORM_EXPECTED_HEAD", "42")

	_, err := parseWormAnchorFromEnv()
	if !errors.Is(err, ErrWormExpectedHeadObsoleta) {
		t.Fatalf("a env obsoleta foi IGNORADA (err=%v) — o operador ficaria convencido de que ancorou", err)
	}
	// CONTROLO: sem ela, a ausência total continua a ser «não ancora», sem erro.
	t.Setenv("AOS_WORM_EXPECTED_HEAD", "")
	got, err := parseWormAnchorFromEnv()
	if err != nil || got != nil {
		t.Fatalf("ausencia total devia dar (nil, nil), veio (%v, %v)", got, err)
	}
}

// TestBannerDeclaraACobertura — o banner tem de dizer QUANTAS de quantas.
//
// Dizer só «ANCORADA» seria verdadeiro sobre o mecanismo e enganador sobre o efeito: a cobertura
// nunca é completa por desenho, porque as partições nascem por run. É a mesma distinção que custou
// doze horas com o oráculo de autonomia — ligado, e sem efeito.
func TestBannerDeclaraACobertura(t *testing.T) {
	parcial := strings.Join(wormAnchorPostureBanner(3, 108), "\n")
	for _, exigido := range []string{"3 de 108", "105"} {
		if !strings.Contains(parcial, exigido) {
			t.Errorf("o banner nao declara a cobertura (falta %q): %s", exigido, parcial)
		}
	}
	// CONTROLO: sem âncoras, continua a ser a linha de NÃO ANCORADA — e não «0 de N ancorada»,
	// que se leria como se algo tivesse corrido.
	nenhuma := strings.Join(wormAnchorPostureBanner(0, 108), "\n")
	if !strings.Contains(nenhuma, "NAO ANCORADA") {
		t.Errorf("sem ancoras o banner devia dizer NAO ANCORADA: %s", nenhuma)
	}
}

// TestTodosOsCheckpointsSaoVerificados é a propriedade CENTRAL da ancoragem multi-partição, e
// ficou por provar até uma mutação a revelar: substituir o ciclo por `a.Checkpoints[:1]` — o
// defeito singular de origem — não fazia cair teste nenhum.
//
// O cenário: DUAS âncoras, a primeira boa e a SEGUNDA com o piso acima do que o store tem. Quem
// verificar as duas aborta; quem verificar só a primeira arranca — e serviria um WORM cuja segunda
// partição ninguém ancorou, com o banner a dizer que ancorou.
func TestTodosOsCheckpointsSaoVerificados(t *testing.T) {
	ctx := context.Background()
	path := aos268BuildDurableWORM(t, 3)
	cp, pub := aos268Seal(t, path, 3)

	// A segunda âncora nomeia uma partição que o store NÃO tem. É a forma mais nítida de
	// "esta âncora não verifica": se for avaliada, falha; se for saltada, o arranque passa.
	fantasma := cp
	fantasma.Partition = cp.Partition + "-inexistente"

	cfg := tnBaseConfig()
	cfg.WORMPath = path
	cfg.WORMAnchor = &WormAnchor{
		Public:      pub,
		Checkpoints: []audit.Checkpoint{cp, fantasma},
		ExpectedHeads: map[string]uint64{
			cp.Partition:       3,
			fantasma.Partition: 3,
		},
	}

	var banner bytes.Buffer
	node, err := Bootstrap(ctx, cfg, &banner)
	if err == nil {
		node.Close()
		t.Fatal("arrancou com a SEGUNDA ancora por verificar — o no serviria um WORM cuja particao " +
			"ninguem ancorou, com o banner a dizer que ancorou")
	}
	if !strings.Contains(err.Error(), "verificacao ancorada do WORM") {
		t.Errorf("abortou pela razao errada: %v", err)
	}

	// CONTROLO: SÓ com a primeira (a boa), o mesmo caminho ARRANCA. Sem este ramo, o teste acima
	// passaria mesmo que o Bootstrap estivesse a abortar por qualquer outro motivo.
	cfg2 := tnBaseConfig()
	cfg2.WORMPath = path
	cfg2.WORMAnchor = &WormAnchor{
		Public:        pub,
		Checkpoints:   []audit.Checkpoint{cp},
		ExpectedHeads: map[string]uint64{cp.Partition: 3},
	}
	n2, err2 := Bootstrap(ctx, cfg2, &bytes.Buffer{})
	if err2 != nil {
		t.Fatalf("so com a ancora boa devia arrancar: %v", err2)
	}
	n2.Close()
}
