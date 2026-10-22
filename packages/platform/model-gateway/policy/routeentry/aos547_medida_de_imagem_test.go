package routeentry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// AOS-547 (ADR-043 §2.2): um registo de uso run com `imagem` é aceite SÓ quando cita, ao lado do
// relatório de qualificação, o relatório qualificado da medida de imagem (a experiência `ocr`) do
// MESMO perfil, e o ficheiro dele está na pasta.

const aos547Perfil = `{"requested":"gpt-4o","expected_model":"openai/modelo-novo-4.5","wire_class":"openai-chat-completions","capabilities":["tools","imagem"]}`

type aos547Caso struct {
	perfil   string
	media    func(doc map[string]any)   // altera o relatório da medida; nil ⇒ o bom
	registo  func(doc map[string]any)   // altera o registo
	semMedia bool                       // não escreve o ficheiro do relatório da medida
	citar    func(digest string) string // o digest que o registo cita; nil ⇒ o do relatório
}

func (c aos547Caso) correr(t *testing.T) (*routeentry.Config, error) {
	t.Helper()
	a := novoAmbiente(t)
	perfil := aos547Perfil
	if c.perfil != "" {
		perfil = c.perfil
	}
	report, digest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, perfil)))
	mdoc := rt.OCRReportDoc(rt.ProfileDigest(t, perfil))
	if c.media != nil {
		c.media(mdoc)
	}
	media, mdigest := rt.SealReport(t, mdoc)
	edoc := rt.EntryDoc(t, perfil, rt.Board, rt.Region, digest)
	citado := mdigest
	if c.citar != nil {
		citado = c.citar(mdigest)
	}
	edoc["digest_do_relatorio_de_media"] = citado
	if c.registo != nil {
		c.registo(edoc)
	}
	entry := rt.Marshal(t, edoc)
	rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), report)
	if !c.semMedia {
		if err := os.WriteFile(filepath.Join(a.dir, "r"+routeentry.SuffixMediaReport), media, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := routeentry.LoadDir(a.cfg)
	return &a.cfg, err
}

func TestAOS547_RegistoRunComImagemEAMedidaEAceite(t *testing.T) {
	if _, err := (aos547Caso{}).correr(t); err != nil {
		t.Fatalf("registo run com imagem e a medida: %v", err)
	}
}

func TestAOS547_RecusasDaMedida(t *testing.T) {
	casos := map[string]struct {
		c     aos547Caso
		causa string
	}{
		"sem o ficheiro":       {aos547Caso{semMedia: true}, routeentry.CauseMediaReportMissing},
		"sem o membro":         {aos547Caso{registo: func(d map[string]any) { delete(d, "digest_do_relatorio_de_media") }, semMedia: true}, routeentry.CauseRunProfileMedia},
		"cita outro relatorio": {aos547Caso{citar: func(string) string { return "sha256:" + strings.Repeat("d", 64) }}, routeentry.CauseReportNotCited},
		"digest mal formado":   {aos547Caso{citar: func(string) string { return "sha256:xyz" }}, routeentry.CauseEntryUnreadable},
		"medida de outro perfil": {aos547Caso{media: func(d map[string]any) {
			rt.OCRMeasuredRoute(d)["digest_do_perfil"] = "sha256:" + strings.Repeat("b", 64)
		}}, routeentry.CauseVerdictOfOtherProfile},
		"medida recusada":      {aos547Caso{media: func(d map[string]any) { rt.OCRVerdict(d)["veredicto"] = "recusado" }}, routeentry.CauseNotQualified},
		"medida do modo falso": {aos547Caso{media: func(d map[string]any) { d["modo"] = "falso" }}, routeentry.CauseVerdictNotForProduction},
		"medida de outra imagem": {aos547Caso{media: func(d map[string]any) {
			rt.OCRMeasuredRoute(d)["digest_da_imagem_do_proxy"] = "sha256:" + strings.Repeat("c", 64)
		}}, routeentry.CauseVerdictOfOtherProxyImage},
		"run com video e a medida":  {aos547Caso{perfil: strings.Replace(aos547Perfil, `"imagem"`, `"video"`, 1)}, routeentry.CauseMediaReportOutOfPlace},
		"run sem imagem e a medida": {aos547Caso{perfil: strings.Replace(aos547Perfil, `,"imagem"`, ``, 1)}, routeentry.CauseMediaReportOutOfPlace},
	}
	for nome, c := range casos {
		t.Run(nome, func(t *testing.T) {
			_, err := c.c.correr(t)
			if got := routeentry.CauseOf(err); got != c.causa {
				t.Fatalf("causa = %q (%v); quer %q", got, err, c.causa)
			}
		})
	}
}

// Um registo de uso tool com o membro recusa-se: a medida de imagem é do modelo do run.
func TestAOS547_RegistoToolComOMembroRecusado(t *testing.T) {
	c := casoTool{nome: "tool com a medida do run", causa: routeentry.CauseMediaReportOutOfPlace,
		registo: func(d map[string]any) { d["digest_do_relatorio_de_media"] = "sha256:" + strings.Repeat("e", 64) }}
	c.correr(t)
}

// ACHADO 5 DA REVISÃO: um ficheiro de medida que o registo não cita recusa-se, como qualquer
// ficheiro que o registo não explica.
func TestAOS547_FicheiroDeMedidaNaoCitadoRecusado(t *testing.T) {
	a := novoAmbiente(t)
	report, digest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON)))
	entry := rt.Marshal(t, rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, digest))
	rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), report)
	media, _ := rt.SealReport(t, rt.OCRReportDoc(rt.ProfileDigest(t, rt.ProfileJSON)))
	if err := os.WriteFile(filepath.Join(a.dir, "r"+routeentry.SuffixMediaReport), media, 0o600); err != nil {
		t.Fatal(err)
	}
	exigirCausa(t, a.cfg, routeentry.CauseMediaReportOutOfPlace, "r")
}
