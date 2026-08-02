package main

import (
	"errors"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// TestParseRetentionFromEnv cobre a superfície de env da política de retenção TTL (AOS-092/
// AOS-213): a via feliz, o fail-closed ambos-ou-nenhuma, e cada classe de erro (classe
// desconhecida, duração inválida, versão não-SemVer, classe repetida). Fecha a lacuna em que
// Config.Retention era inalcançável pelo binário.
func TestParseRetentionFromEnv(t *testing.T) {
	// unset ⇒ zero-value, sem erro (nada expira — comportamento por omissão).
	t.Run("unset", func(t *testing.T) {
		t.Setenv("AOS_RETENTION_VERSION", "")
		t.Setenv("AOS_RETENTION_PERIODS", "")
		rc, err := parseRetentionFromEnv()
		if err != nil {
			t.Fatalf("unset devia ser aceite, veio %v", err)
		}
		if rc.Version() != "" {
			t.Fatalf("unset devia dar zero-value (versao vazia), veio %q", rc.Version())
		}
	})

	// via feliz: versão + duas classes, com períodos parseados.
	t.Run("valido", func(t *testing.T) {
		t.Setenv("AOS_RETENTION_VERSION", "1.2.3")
		t.Setenv("AOS_RETENTION_PERIODS", "pii_operational=720h,audit=8760h")
		rc, err := parseRetentionFromEnv()
		if err != nil {
			t.Fatalf("config valida recusada: %v", err)
		}
		if rc.Version() != "1.2.3" {
			t.Fatalf("versao = %q, esperado 1.2.3", rc.Version())
		}
		if d, ok := rc.Period(audit.ClassPIIOperational); !ok || d != 720*time.Hour {
			t.Fatalf("pii_operational = (%v,%v), esperado (720h,true)", d, ok)
		}
		if d, ok := rc.Period(audit.ClassAudit); !ok || d != 8760*time.Hour {
			t.Fatalf("audit = (%v,%v), esperado (8760h,true)", d, ok)
		}
		// classe omitida ⇒ nunca expira (sem período).
		if _, ok := rc.Period(audit.ClassDiagnostic); ok {
			t.Fatalf("classe omitida (diagnostic) nao devia ter periodo")
		}
	})

	// fail-closed: cada caso inválido tem de devolver ErrBadRetention (aborta, não degrada).
	for _, tc := range []struct {
		name, version, periods string
	}{
		{"so_versao", "1.0.0", ""},
		{"so_periodos", "", "audit=1h"},
		{"classe_desconhecida", "1.0.0", "diagnostc=1h"},
		{"duracao_ilegivel", "1.0.0", "audit=xyz"},
		{"duracao_zero", "1.0.0", "audit=0s"},
		{"duracao_negativa", "1.0.0", "audit=-1h"},
		{"versao_nao_semver", "v1", "audit=1h"},
		{"classe_repetida", "1.0.0", "audit=1h,audit=2h"},
		{"entrada_sem_igual", "1.0.0", "audit"},
	} {
		t.Run("invalido_"+tc.name, func(t *testing.T) {
			t.Setenv("AOS_RETENTION_VERSION", tc.version)
			t.Setenv("AOS_RETENTION_PERIODS", tc.periods)
			_, err := parseRetentionFromEnv()
			if !errors.Is(err, ErrBadRetention) {
				t.Fatalf("esperado ErrBadRetention, veio %v", err)
			}
		})
	}
}
