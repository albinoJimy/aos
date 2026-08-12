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
	cfg.WORMAnchor = &WormAnchor{Public: pub, Checkpoint: cp, ExpectedHead: 3}

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
		cfg.WORMAnchor = &WormAnchor{Public: pub, Checkpoint: cp, ExpectedHead: 3}
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
	cfg.WORMAnchor = &WormAnchor{Public: pub, Checkpoint: cpOld, ExpectedHead: 3}

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
	cfg.WORMAnchor = &WormAnchor{Public: wrongPub, Checkpoint: cp, ExpectedHead: 3}

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
		t.Setenv("AOS_WORM_EXPECTED_HEAD", "2")
		if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrBadWormTrustAnchor) {
			t.Fatalf("esperava ErrBadWormTrustAnchor, veio: %v", err)
		}
	})

	t.Run("checkpoint_ilegivel", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", anchorHex)
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", filepath.Join(t.TempDir(), "inexistente.json"))
		t.Setenv("AOS_WORM_EXPECTED_HEAD", "2")
		if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrBadWormCheckpoint) {
			t.Fatalf("esperava ErrBadWormCheckpoint, veio: %v", err)
		}
	})

	t.Run("head_invalido", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", anchorHex)
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
		t.Setenv("AOS_WORM_EXPECTED_HEAD", "0") // piso 0 é inválido
		if _, err := parseWormAnchorFromEnv(); !errors.Is(err, ErrBadWormExpectedHead) {
			t.Fatalf("esperava ErrBadWormExpectedHead, veio: %v", err)
		}
	})

	t.Run("completo_parseia", func(t *testing.T) {
		clearWormEnv(t)
		t.Setenv("AOS_WORM_TRUST_ANCHOR", anchorHex)
		t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
		t.Setenv("AOS_WORM_EXPECTED_HEAD", "2")
		got, err := parseWormAnchorFromEnv()
		if err != nil {
			t.Fatalf("config completa devia parsear: %v", err)
		}
		if got == nil || got.ExpectedHead != 2 || got.Checkpoint.AuditSeq != 2 || !got.Public.Equal(pub) {
			t.Fatalf("material parseado incoerente: %+v", got)
		}
	})
}

// TestWormAnchorPostureBanner — as duas dobras da linha de banner declaram estados DIFERENTES e
// ambos verdadeiros (mesma disciplina de posture_banner.go).
func TestWormAnchorPostureBanner(t *testing.T) {
	anchored := strings.Join(wormAnchorPostureBanner(true), "\n")
	if !strings.Contains(anchored, "ANCORADA") || strings.Contains(anchored, "NAO ANCORADA") {
		t.Fatalf("dobra composta devia declarar ANCORADA:\n%s", anchored)
	}
	notAnchored := strings.Join(wormAnchorPostureBanner(false), "\n")
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
