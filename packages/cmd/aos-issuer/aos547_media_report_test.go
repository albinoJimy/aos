package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// AOS-547 — um registo de uso run com `imagem` cita a medida de imagem: só se assina com
// `--media-report`, conferido como o nó o confere; e o nó carrega o que se assinou.

const aos547Perfil = `{"requested":"gpt-4o","expected_model":"openai/modelo-novo-4.5","wire_class":"openai-chat-completions","capabilities":["tools","imagem"]}`

func aos547Posto(t *testing.T, media func(map[string]any)) (dir, seed string, pub ed25519.PublicKey) {
	t.Helper()
	dir = t.TempDir()
	key := rt.NewKey(t)
	seed = filepath.Join(t.TempDir(), "perfis.seed")
	if err := os.WriteFile(seed, []byte(hex.EncodeToString(key.Priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, digest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, aos547Perfil)))
	mdoc := rt.OCRReportDoc(rt.ProfileDigest(t, aos547Perfil))
	if media != nil {
		media(mdoc)
	}
	mrep, mdigest := rt.SealReport(t, mdoc)
	edoc := rt.EntryDoc(t, aos547Perfil, rt.Board, rt.Region, digest)
	edoc["digest_do_relatorio_de_media"] = mdigest
	rt.Write(t, dir, "novo", rt.Marshal(t, edoc), "", report)
	if err := os.WriteFile(filepath.Join(dir, "novo"+routeentry.SuffixMediaReport), mrep, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, seed, key.Pub
}

func aos547Assinar(dir, seed string, extra ...string) error {
	args := append([]string{"route-profile-sign", "--entry", filepath.Join(dir, "novo"+routeentry.SuffixEntry),
		"--report", filepath.Join(dir, "novo"+routeentry.SuffixReport), "--key-file", seed}, extra...)
	return run(args, &bytes.Buffer{}, &bytes.Buffer{})
}

func TestAOS547_RouteProfileSign_ComAMedidaONoAceita(t *testing.T) {
	dir, seed, pub := aos547Posto(t, nil)
	media := filepath.Join(dir, "novo"+routeentry.SuffixMediaReport)
	sig := filepath.Join(dir, "novo"+routeentry.SuffixSignature)
	if err := aos547Assinar(dir, seed, "--out", sig); err == nil || !strings.Contains(err.Error(), "passe-o com --media-report") {
		t.Fatalf("sem --media-report: %v", err)
	}
	if _, err := os.Stat(sig); !os.IsNotExist(err) {
		t.Fatal("ficou assinatura sem a medida")
	}
	if err := aos547Assinar(dir, seed, "--media-report", media, "--out", sig); err != nil {
		t.Fatalf("com --media-report: %v", err)
	}
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routeentry.LoadDir(routeentry.Config{Dir: dir, TrustAnchor: base64.StdEncoding.EncodeToString(pub), Board: rt.Board, Region: rt.Region, Allowlist: pol}); err != nil {
		t.Fatalf("o no nao carrega o que se assinou: %v", err)
	}
	if err := aos547Assinar(dir, seed, "--media-report", media, "--out", media); err == nil {
		t.Fatal("--out por cima do --media-report")
	}
}

func TestAOS547_RouteProfileSign_NaoAssinaUmaMedidaRecusada(t *testing.T) {
	dir, seed, _ := aos547Posto(t, func(d map[string]any) { rt.OCRVerdict(d)["veredicto"] = "recusado" })
	err := aos547Assinar(dir, seed, "--media-report", filepath.Join(dir, "novo"+routeentry.SuffixMediaReport), "--out", filepath.Join(dir, "x.sig"))
	if err == nil || !strings.Contains(err.Error(), routeentry.CauseNotQualified) {
		t.Fatalf("medida recusada: %v", err)
	}
	// E --media-report num registo que não a cita recusa-se.
	p := aos519NovoPosto(t, nil, nil)
	if _, _, err := p.assinar("--media-report", filepath.Join(dir, "novo"+routeentry.SuffixMediaReport)); err == nil {
		t.Fatal("--media-report aceite num registo sem a medida")
	}
}
