package routeentry_test

// AOS-536 (parte 1) — O REGISTO DE USO `tool` DE VÍDEO.
//
// Um registo de uso `tool` passa a aceitar um perfil de vídeo — declara `video`, não declara
// `tools` nem `imagem` — além do perfil de imagem do AOS-530. Exactamente uma capacidade de media;
// o relatório citado é o da experiência dessa media (`ocr` para imagem, `video` versão 2 para
// vídeo), e um não serve pelo outro. Os registos de hoje lêem-se byte-iguais: o de uso `run` está
// pinado em aos530_uso_test.go, e o de uso `tool` de imagem pina-se aqui.

import (
	"crypto/ed25519"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// registoToolDeImagem são os bytes EXACTOS de um registo de uso `tool` de imagem com a forma do
// AOS-530. O perfil é o de [rt.ToolProfileJSON].
const registoToolDeImagem = `{
  "formato": "aos-registo-de-entrada-de-perfil/1",
  "uso": "tool",
  "board": "board-eu",
  "regiao": "eu",
  "perfil": {"requested":"gpt-4o-mini","expected_model":"openrouter/anthropic/modelo-de-visao","wire_class":"openai-chat-completions","capabilities":["imagem"]},
  "digest_do_perfil": "sha256:b254bd68637a018b528a9985294b01d36b61fe9889a32d682598b38a2e26a84b",
  "digest_do_relatorio": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
  "digest_da_imagem_do_proxy": "sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63",
  "fatia_inicial_percentagem": 0
}
`

// Os valores PINADOS do registo tool de imagem: o digest dos bytes (conferido FORA do Go, com o
// `sha256sum` dos mesmos bytes) e a assinatura ed25519 com a semente fixa de aos530_uso_test.go.
const (
	digestDoPerfilToolDeImagem            = "sha256:b254bd68637a018b528a9985294b01d36b61fe9889a32d682598b38a2e26a84b"
	digestPinadoDoRegistoToolDeImagem     = "sha256:d7771901c3ef236f63daf509e526f67c5af030f5dfbaf66cd77b68d21274a66e"
	assinaturaPinadaDoRegistoToolDeImagem = "/6GCECzG+6hvE07Ay9TpmQaMzQjdaUKLaMqYcGP/0LhXM5k1QYvIhpWcYBv+VRqQrXnM4L5MAcZgIEOw0BojDw=="
)

// registoToolDeImagemBytes devolve os bytes pinados, depois de conferir que o digest do perfil
// escrito neles é o de [rt.ToolProfileJSON].
func registoToolDeImagemBytes(t *testing.T) []byte {
	t.Helper()
	if d := rt.ProfileDigest(t, rt.ToolProfileJSON); d != digestDoPerfilToolDeImagem {
		t.Fatalf("o digest do perfil de imagem pinado ja nao e o de rt.ToolProfileJSON: %s", d)
	}
	return []byte(registoToolDeImagem)
}

// UM REGISTO TOOL DE IMAGEM DE HOJE é o de antes: o mesmo digest, a mesma assinatura, lido como uso
// `tool` com a media `imagem`.
func TestAOS536_RegistoToolDeImagemTemOMesmoDigestEAMesmaAssinatura(t *testing.T) {
	raw := registoToolDeImagemBytes(t)
	if d := routeentry.EntryDigest(raw); d != digestPinadoDoRegistoToolDeImagem {
		t.Fatalf("digest do registo tool de imagem = %s; pinado %s", d, digestPinadoDoRegistoToolDeImagem)
	}
	priv := sementeFixa()
	if s := routeentry.Sign(raw, priv); s != assinaturaPinadaDoRegistoToolDeImagem {
		t.Fatalf("assinatura do registo tool de imagem = %s; pinada %s", s, assinaturaPinadaDoRegistoToolDeImagem)
	}
	if !routeentry.VerifySignature(raw, []byte(assinaturaPinadaDoRegistoToolDeImagem), priv.Public().(ed25519.PublicKey)) {
		t.Fatal("a assinatura pinada nao verifica")
	}
	e, err := routeentry.ParseEntry(raw)
	if err != nil {
		t.Fatalf("o registo tool de imagem deixou de se ler: %v", err)
	}
	if !e.ForTool() || e.Media != modelgateway.CapabilityImage || e.InitialSharePercent != 0 || e.EntryDigest != digestPinadoDoRegistoToolDeImagem {
		t.Fatalf("registo tool de imagem lido como %+v", e)
	}
	// O registo de uso run de antes continua sem media.
	if r, err := routeentry.ParseEntry(registoDeAntesBytes(t)); err != nil || r.Media != "" {
		t.Fatalf("registo run de antes: %+v %v", r, err)
	}
}

// O CAMINHO FELIZ DE UM REGISTO TOOL DE VÍDEO: carrega pela pasta, com o uso, a fatia 0 e a media.
func TestAOS536_RegistoToolDeVideoCarrega(t *testing.T) {
	a := novoAmbiente(t)
	rt.GoodVideoTool(t, a.dir, "video", rt.VideoToolProfileJSON, rt.Board, rt.Region, a.key)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 1 || !entradas[0].ForTool() || entradas[0].Media != modelgateway.CapabilityVideo || entradas[0].InitialSharePercent != 0 ||
		entradas[0].Profile.Requested != "gpt-4o-mini" {
		t.Fatalf("registo tool de video: %+v", entradas)
	}
	// O de imagem continua a carregar, com a media dele.
	b := novoAmbiente(t)
	rt.GoodTool(t, b.dir, "ocr", rt.ToolProfileJSON, rt.Board, rt.Region, b.key)
	if entradas, err := routeentry.LoadDir(b.cfg); err != nil || entradas[0].Media != modelgateway.CapabilityImage {
		t.Fatalf("registo tool de imagem: %+v, %v", entradas, err)
	}
}

// casoMedia é um registo de uso tool VÁLIDO, de imagem ou de vídeo, com UMA coisa estragada.
type casoMedia struct {
	nome, causa string
	// perfil é o perfil do registo; o relatório é feito por `doc` para o digest dele.
	perfil string
	doc    func(profileDigest string) map[string]any
	// estragar muda o relatório antes de o selar; registo muda o registo antes de o assinar.
	estragar func(doc map[string]any)
	registo  func(doc map[string]any)
}

func (c casoMedia) correr(t *testing.T) {
	t.Helper()
	a := novoAmbiente(t)
	rdoc := c.doc(rt.ProfileDigest(t, c.perfil))
	if c.estragar != nil {
		c.estragar(rdoc)
	}
	report, digest := rt.SealReport(t, rdoc)
	edoc := rt.ToolEntryDoc(t, c.perfil, rt.Board, rt.Region, digest)
	if c.registo != nil {
		c.registo(edoc)
	}
	entry := rt.Marshal(t, edoc)
	rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), report)
	exigirCausa(t, a.cfg, c.causa, "r")
}

// comCapacidades troca as capacidades do perfil de vídeo.
func comCapacidades(capacidades string) string {
	return strings.Replace(rt.VideoToolProfileJSON, `["video"]`, capacidades, 1)
}

// AS RECUSAS DE UM REGISTO TOOL DE VÍDEO, uma de cada vez — e o relatório de uma media num registo
// da outra.
func TestAOS536_RecusasDoRegistoDeVideo(t *testing.T) {
	video, ocr, qualificacao := rt.VideoReportDoc, rt.OCRReportDoc, rt.ReportDoc
	casos := []casoMedia{
		// O perfil: exactamente uma capacidade de media, e nenhuma `tools`.
		{nome: "video e imagem", causa: routeentry.CauseToolProfileCapabilities, perfil: comCapacidades(`["imagem","video"]`), doc: video},
		{nome: "video e tools", causa: routeentry.CauseToolProfileCapabilities, perfil: comCapacidades(`["tools","video"]`), doc: video},
		{nome: "video, imagem e tools", causa: routeentry.CauseToolProfileCapabilities, perfil: comCapacidades(`["imagem","tools","video"]`), doc: video},
		{nome: "so tools", causa: routeentry.CauseToolProfileCapabilities, perfil: comCapacidades(`["tools"]`), doc: video},
		{nome: "sem capacidades", causa: routeentry.CauseToolProfileCapabilities, perfil: comCapacidades(`[]`), doc: video},
		{nome: "fatia maior que zero", causa: routeentry.CauseToolWithShare, perfil: rt.VideoToolProfileJSON, doc: video, registo: func(d map[string]any) { d["fatia_inicial_percentagem"] = 5 }},

		// O relatório de outra media, ou de outra experiência.
		{nome: "registo de video a citar um relatorio ocr", causa: routeentry.CauseReportOfOtherExperiment, perfil: rt.VideoToolProfileJSON, doc: ocr},
		{nome: "registo de imagem a citar um relatorio video", causa: routeentry.CauseReportOfOtherExperiment, perfil: rt.ToolProfileJSON, doc: video},
		{nome: "registo de video a citar uma qualificacao", causa: routeentry.CauseReportOfOtherExperiment, perfil: rt.VideoToolProfileJSON, doc: qualificacao},

		// A forma do relatório `video`.
		{nome: "relatorio video da versao 1", causa: routeentry.CauseReportUnknownShape, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { d["versao_do_relatorio"] = "1" }},
		{nome: "relatorio video sem versao", causa: routeentry.CauseReportUnknownShape, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { delete(d, "versao_do_relatorio") }},
		{nome: "relatorio video com membro desconhecido", causa: routeentry.CauseReportUnknownShape, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { d["imagens"] = []any{} }},
		{nome: "veredicto com membro desconhecido", causa: routeentry.CauseReportUnknownShape, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { rt.VideoVerdict(d)["imagens_nas_contas"] = 8 }},
		{nome: "rota medida com membro desconhecido", causa: routeentry.CauseReportUnknownShape, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { rt.VideoMeasuredRoute(d)["extra"] = "x" }},
		{nome: "veredicto no bloco do ocr", causa: routeentry.CauseReportUnknownShape, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) {
			d["veredicto_do_ocr"] = d["veredicto_do_video"]
			delete(d, "veredicto_do_video")
		}},
		{nome: "sem o bloco do veredicto", causa: routeentry.CauseReportWithoutVerdict, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { delete(d, "veredicto_do_video") }},

		// O veredicto.
		{nome: "modo falso", causa: routeentry.CauseVerdictNotForProduction, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { d["modo"] = "falso" }},
		{nome: "sem modo", causa: routeentry.CauseVerdictNotForProduction, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { delete(d, "modo") }},
		{nome: "nao vale para producao", causa: routeentry.CauseVerdictNotForProduction, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { rt.VideoVerdict(d)["vale_para_producao"] = false }},
		{nome: "veredicto recusado", causa: routeentry.CauseNotQualified, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { rt.VideoVerdict(d)["veredicto"] = "recusado" }},
		{nome: "outro perfil", causa: routeentry.CauseVerdictOfOtherProfile, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) {
			rt.VideoMeasuredRoute(d)["digest_do_perfil"] = "sha256:" + strings.Repeat("b", 64)
		}},
		{nome: "sem rota medida", causa: routeentry.CauseVerdictOfOtherProfile, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) { delete(rt.VideoVerdict(d), "rota_medida") }},
		{nome: "outra imagem do proxy", causa: routeentry.CauseVerdictOfOtherProxyImage, perfil: rt.VideoToolProfileJSON, doc: video, estragar: func(d map[string]any) {
			rt.VideoMeasuredRoute(d)["digest_da_imagem_do_proxy"] = "sha256:" + strings.Repeat("c", 64)
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, c.correr)
	}
}

// UM REGISTO DE USO RUN que cite um relatório `video` recusa como antes, por forma desconhecida — a
// regra de sempre, sem mudança.
func TestAOS536_RegistoRunComRelatorioDeVideo(t *testing.T) {
	a := novoAmbiente(t)
	report, digest := rt.SealReport(t, rt.VideoReportDoc(rt.ProfileDigest(t, rt.ProfileJSON)))
	e := rt.Marshal(t, rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, digest))
	rt.Write(t, a.dir, "r", e, routeentry.Sign(e, a.key.Priv), report)
	exigirCausa(t, a.cfg, routeentry.CauseReportUnknownShape, "r")
}

// A LEITURA DIRECTA: um relatório `video` sem o digest de si próprio é inválido; um bem selado lê-se
// com o modo, o veredicto, o perfil e a imagem do proxy; o `ocr` lê-se só pelo leitor `ocr`.
func TestAOS536_ReadVideoVerdict(t *testing.T) {
	perfil := rt.ProfileDigest(t, rt.VideoToolProfileJSON)
	if _, err := routeentry.ReadVideoVerdict(rt.Marshal(t, rt.VideoReportDoc(perfil))); routeentry.CauseOf(err) != routeentry.CauseReportInvalid {
		t.Fatalf("sem digest: %v", err)
	}
	cru, digest := rt.SealReport(t, rt.VideoReportDoc(perfil))
	v, err := routeentry.ReadVideoVerdict(cru)
	if err != nil || v.Digest != digest || v.Mode != routeentry.ModeReal || v.Verdict != routeentry.VerdictQualified || !v.ValidForProduction ||
		v.ProfileDigest != perfil || v.ProxyImageDigest != routeentry.ProductionProxyImageDigest() {
		t.Fatalf("leitura: %+v %v", v, err)
	}
	if _, err := routeentry.ReadOCRVerdict(cru); routeentry.CauseOf(err) != routeentry.CauseReportOfOtherExperiment {
		t.Fatalf("o leitor ocr leu um relatorio video: %v", err)
	}
	ocr, _ := rt.SealReport(t, rt.OCRReportDoc(perfil))
	if _, err := routeentry.ReadVideoVerdict(ocr); routeentry.CauseOf(err) != routeentry.CauseReportOfOtherExperiment {
		t.Fatalf("o leitor video leu um relatorio ocr: %v", err)
	}
	if routeentry.VideoReportVersion != "2" || routeentry.ExperimentVideo != "video" {
		t.Fatal("a versao ou a experiencia do relatorio video mudou")
	}
}

// Os nomes dos membros que a fixture do relatório `video` escreve são os que as listas fechadas
// admitem (o teste do banco prende as listas ao que o banco escreve).
func TestAOS536_ListasFechadasDoVideoCobremOQueSeLe(t *testing.T) {
	dentro := func(lista []string, nome string) bool {
		for _, n := range lista {
			if n == nome {
				return true
			}
		}
		return false
	}
	doc := rt.VideoReportDoc("sha256:" + strings.Repeat("0", 64))
	for nome := range doc {
		if !dentro(routeentry.VideoReportTopFields(), nome) {
			t.Errorf("topo: %q fora da lista", nome)
		}
	}
	for nome := range rt.VideoVerdict(doc) {
		if !dentro(routeentry.VideoVerdictFields(), nome) {
			t.Errorf("veredicto: %q fora da lista", nome)
		}
	}
	for nome := range rt.VideoMeasuredRoute(doc) {
		if !dentro(routeentry.OCRMeasuredRouteFields(), nome) {
			t.Errorf("rota medida: %q fora da lista", nome)
		}
	}
}
