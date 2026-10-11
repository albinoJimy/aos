package routeentry_test

// AOS-519 (revisão adversarial) — OS TECTOS DE TAMANHO DOS TRÊS FICHEIROS.
//
// Cada ficheiro tem um tecto, e um ficheiro acima dele é recusado sem ser interpretado. Os casos
// são VÁLIDOS em tudo o resto — o enchimento é espaço em branco, que o JSON e o base64 toleram, e
// o registo é assinado já com ele —, pelo que sem o tecto a pasta carregava: é o tecto, e só ele,
// que os recusa. No tecto exacto, carregam.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// encher devolve `b` com espaços no fim até ter exactamente `n` bytes.
func encher(t *testing.T, b []byte, n int) []byte {
	t.Helper()
	if len(b) > n {
		t.Fatalf("o conteudo ja tem %d bytes, mais do que %d", len(b), n)
	}
	return append(append([]byte(nil), b...), bytes.Repeat([]byte(" "), n-len(b))...)
}

func TestAOS519_TectosDeTamanho(t *testing.T) {
	// montar escreve um registo válido em que cada ficheiro é enchido até ao tamanho dado (0 ⇒ sem encher).
	montar := func(t *testing.T, registo, assinatura, relatorio int) *ambiente {
		t.Helper()
		a := novoAmbiente(t)
		report, digest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON)))
		if relatorio > 0 {
			report = encher(t, report, relatorio)
		}
		entry := rt.Marshal(t, rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, digest))
		if registo > 0 {
			entry = encher(t, entry, registo)
		}
		sig := []byte(routeentry.Sign(entry, a.key.Priv))
		if assinatura > 0 {
			sig = encher(t, sig, assinatura)
		}
		for sufixo, conteudo := range map[string][]byte{routeentry.SuffixEntry: entry, routeentry.SuffixSignature: sig, routeentry.SuffixReport: report} {
			if err := os.WriteFile(filepath.Join(a.dir, "r"+sufixo), conteudo, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return a
	}
	t.Run("no tecto exacto, os tres carregam", func(t *testing.T) {
		a := montar(t, routeentry.MaxEntryBytes, routeentry.MaxSignatureBytes, routeentry.MaxReportBytes)
		if entradas, err := routeentry.LoadDir(a.cfg); err != nil || len(entradas) != 1 {
			t.Fatalf("com os tres ficheiros no tecto exacto a pasta tinha de carregar: %v", err)
		}
	})
	t.Run("registo um byte acima", func(t *testing.T) {
		exigirCausa(t, montar(t, routeentry.MaxEntryBytes+1, 0, 0).cfg, routeentry.CauseEntryUnreadable, "r")
	})
	t.Run("assinatura um byte acima", func(t *testing.T) {
		exigirCausa(t, montar(t, 0, routeentry.MaxSignatureBytes+1, 0).cfg, routeentry.CauseSignatureInvalid, "r")
	})
	t.Run("relatorio um byte acima", func(t *testing.T) {
		exigirCausa(t, montar(t, 0, 0, routeentry.MaxReportBytes+1).cfg, routeentry.CauseReportUnreadable, "r")
	})
	t.Run("registo vazio", func(t *testing.T) {
		a := montar(t, 0, 0, 0)
		if err := os.WriteFile(filepath.Join(a.dir, "r"+routeentry.SuffixEntry), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		exigirCausa(t, a.cfg, routeentry.CauseEntryUnreadable, "r")
	})
}
