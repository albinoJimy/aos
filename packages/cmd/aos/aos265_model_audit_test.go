package main

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// closeAudit liberta o descritor do WORM durável aberto por parseModelAuditFromEnv
// ao fim do teste. Em produção o [audit.FileStore] fica aberto durante a vida do nó
// (ver model_audit_env.go); no teste, deixá-lo aberto trava o t.TempDir().RemoveAll
// no Windows (o WAL fica lock'd) — por isso amarramos o Close ao t.Cleanup. O store
// pode ser nil (env vazia ⇒ MemStore) ou não implementar io.Closer (MemStore); ambos
// são no-op. Close é idempotente.
func closeAudit(t *testing.T, store audit.Store) {
	t.Helper()
	if c, ok := store.(io.Closer); ok && c != nil {
		t.Cleanup(func() { _ = c.Close() })
	}
}

// AOS-265 — o audit de governação do Model Gateway aponta a um WORM DURÁVEL quando
// AOS_MODEL_AUDIT_PATH está definida (troca-o pelo MemStore volátil), fail-closed em
// config inválida, e o banner declara o modo amarrado ao estado composto.

func TestParseModelAuditFromEnv_Vazio_MemStore(t *testing.T) {
	t.Setenv("AOS_MODEL_AUDIT_PATH", "")
	store, path, err := parseModelAuditFromEnv()
	if err != nil {
		t.Fatalf("parseModelAuditFromEnv: %v", err)
	}
	if store != nil || path != "" {
		t.Fatalf("esperado (nil, \"\") para env vazia, obtido (%v, %q)", store, path)
	}
}

func TestParseModelAuditFromEnv_Duravel_AbreWORM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gw-audit.wal")
	t.Setenv("AOS_MODEL_AUDIT_PATH", path)

	store, gotPath, err := parseModelAuditFromEnv()
	if err != nil {
		t.Fatalf("parseModelAuditFromEnv: %v", err)
	}
	if store == nil {
		t.Fatal("esperado WORM durável, obtido nil")
	}
	closeAudit(t, store)
	if gotPath != path {
		t.Fatalf("path = %q, esperado %q", gotPath, path)
	}
	// reabrir o mesmo caminho tem de funcionar (crash-safe replay) — prova de durabilidade.
	store2, _, err := parseModelAuditFromEnv()
	if err != nil {
		t.Fatalf("reabrir WORM durável: %v", err)
	}
	closeAudit(t, store2)
}

func TestParseModelAuditFromEnv_Invalido_FailClosed(t *testing.T) {
	// um DIRECTÓRIO não é um WAL abrível para append ⇒ OpenFileStore falha ⇒ ErrBadModelAudit.
	t.Setenv("AOS_MODEL_AUDIT_PATH", t.TempDir())
	_, _, err := parseModelAuditFromEnv()
	if !errors.Is(err, ErrBadModelAudit) {
		t.Fatalf("esperado ErrBadModelAudit, obtido %v", err)
	}
}

func TestModelAuditPostureBanner_DeclaraModo(t *testing.T) {
	// sem gateway composto ⇒ sem linha.
	if lines := modelAuditPostureBanner(false, ""); lines != nil {
		t.Fatalf("sem gateway devia ser nil, obtido %v", lines)
	}
	// gateway + sem path ⇒ declara VOLATIL.
	volatil := modelAuditPostureBanner(true, "")
	if len(volatil) != 1 || !strings.Contains(volatil[0], "VOLATIL") {
		t.Fatalf("banner volatil inesperado: %v", volatil)
	}
	// gateway + path ⇒ declara DURAVEL e nomeia o caminho.
	dur := modelAuditPostureBanner(true, "/var/lib/aos/gw-audit.wal")
	if len(dur) != 1 || !strings.Contains(dur[0], "DURAVEL") || !strings.Contains(dur[0], "/var/lib/aos/gw-audit.wal") {
		t.Fatalf("banner duravel inesperado: %v", dur)
	}
}
