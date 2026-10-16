package routeentry_test

// AOS-530 (peça 2 de 3) — O USO DE UM REGISTO DE ENTRADA (ADR-042 §2.3, emenda ao ADR-041 §2.1).
//
// Um registo sem o membro `uso` é de uso `run` e lê-se EXACTAMENTE como antes: provado sobre bytes
// pinados (o digest e a assinatura são constantes deste ficheiro, não recalculados no teste). Um
// registo de uso `tool` tem fatia 0 e cita um relatório `ocr` qualificado do modo real; tudo o
// resto recusa com causa própria.

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// registoDeAntes são os bytes EXACTOS de um registo de entrada com a forma de antes do AOS-530
// (sem `uso`). Os três digests que cita são fixos; o perfil é o de [rt.ProfileJSON], e o digest dele
// também está escrito por extenso.
const registoDeAntes = `{
  "formato": "aos-registo-de-entrada-de-perfil/1",
  "board": "board-eu",
  "regiao": "eu",
  "perfil": {"requested":"gpt-4o","expected_model":"openai/modelo-novo-4.5","wire_class":"openai-chat-completions","capabilities":["tools"]},
  "digest_do_perfil": "sha256:c4ad85602b146ea6e795e13133f89acc3a2af104d8c9de58370aab9a4c96f6a5",
  "digest_do_relatorio": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
  "digest_da_imagem_do_proxy": "sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63",
  "fatia_inicial_percentagem": 10
}
`

// Os valores PINADOS do registo de antes: o digest dos bytes — conferido FORA do Go, com o
// `sha256sum` dos mesmos bytes — e a assinatura ed25519 (determinística) com a semente fixa abaixo.
// Estão escritos à mão: se a leitura nova mudasse o digest ou a mensagem assinada de um registo sem
// `uso`, deixavam de bater.
const (
	digestPinadoDoRegistoDeAntes     = "sha256:d4f5a8d07226f5ebbf591e1b2a912b5e0a294f507f78ce9b66fd3df38dfc270e"
	assinaturaPinadaDoRegistoDeAntes = "yUYAG8cMQVzry/s18O6EBOz29VA2GLYUqBVlYUPBnT/5cNl6rpz27BcJ1mvMIlgjE7ps3oyRn/7PFWlDQXt0Dw=="
)

// sementeFixa é a semente ed25519 do assinante do teste pinado (32 bytes: 0, 1, …, 31).
func sementeFixa() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

// registoDeAntesBytes devolve os bytes pinados, depois de conferir que o digest do perfil escrito
// neles é o de [rt.ProfileJSON] (sem isso o registo não se leria, e o teste não diria nada).
func registoDeAntesBytes(t *testing.T) []byte {
	t.Helper()
	if !strings.Contains(registoDeAntes, rt.ProfileDigest(t, rt.ProfileJSON)) {
		t.Fatal("o digest do perfil pinado ja nao e o de rt.ProfileJSON")
	}
	return []byte(registoDeAntes)
}

// UM REGISTO SEM `uso` É O DE ANTES: o mesmo digest, a mesma assinatura (verificada contra a chave
// pública da semente fixa), lido como uso `run`, com os mesmos campos.
func TestAOS530_RegistoSemUsoTemOMesmoDigestEAMesmaAssinatura(t *testing.T) {
	raw := registoDeAntesBytes(t)
	if d := routeentry.EntryDigest(raw); d != digestPinadoDoRegistoDeAntes {
		t.Fatalf("digest do registo de antes = %s; pinado %s", d, digestPinadoDoRegistoDeAntes)
	}
	priv := sementeFixa()
	if s := routeentry.Sign(raw, priv); s != assinaturaPinadaDoRegistoDeAntes {
		t.Fatalf("assinatura do registo de antes = %s; pinada %s", s, assinaturaPinadaDoRegistoDeAntes)
	}
	if !routeentry.VerifySignature(raw, []byte(assinaturaPinadaDoRegistoDeAntes), priv.Public().(ed25519.PublicKey)) {
		t.Fatal("a assinatura pinada nao verifica")
	}
	e, err := routeentry.ParseEntry(raw)
	if err != nil {
		t.Fatalf("o registo de antes deixou de se ler: %v", err)
	}
	if e.Use != routeentry.UseRun || e.ForTool() || e.InitialSharePercent != 10 || e.EntryDigest != digestPinadoDoRegistoDeAntes ||
		e.Profile.Requested != "gpt-4o" || e.Board != rt.Board || e.Region != rt.Region {
		t.Fatalf("registo de antes lido como %+v", e)
	}
}

// A MESMA DECISÃO: um registo válido sem `uso` carrega pela pasta como antes, e o relatório
// citado passa pela regra de sempre (a da qualificação); um relatório `ocr` é recusado para ele
// como antes (forma desconhecida), e não pela regra nova.
func TestAOS530_RegistoSemUsoDecideComoAntes(t *testing.T) {
	a := novoAmbiente(t)
	entry, _ := rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
	var doc map[string]any
	if err := json.Unmarshal(entry, &doc); err != nil {
		t.Fatal(err)
	}
	if _, tem := doc["uso"]; tem {
		t.Fatal("a fixture de antes ganhou o membro uso")
	}
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil || len(entradas) != 1 || entradas[0].Use != routeentry.UseRun {
		t.Fatalf("registo sem uso: %+v, %v", entradas, err)
	}

	b := novoAmbiente(t)
	report, digest := rt.SealReport(t, rt.OCRReportDoc(rt.ProfileDigest(t, rt.ProfileJSON)))
	e := rt.Marshal(t, rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, digest))
	rt.Write(t, b.dir, "r", e, routeentry.Sign(e, b.key.Priv), report)
	exigirCausa(t, b.cfg, routeentry.CauseReportUnknownShape, "r")
}

// O CAMINHO FELIZ DE UM REGISTO DE USO TOOL: carrega, com o uso e a fatia 0.
func TestAOS530_RegistoToolValidoCarrega(t *testing.T) {
	a := novoAmbiente(t)
	rt.GoodTool(t, a.dir, "ocr", rt.ToolProfileJSON, rt.Board, rt.Region, a.key)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 1 || !entradas[0].ForTool() || entradas[0].Use != routeentry.UseTool || entradas[0].InitialSharePercent != 0 ||
		entradas[0].Profile.Requested != "gpt-4o-mini" {
		t.Fatalf("registo tool: %+v", entradas)
	}
	// `uso: run` escrito por extenso também se lê, como uso run.
	b := novoAmbiente(t)
	rdoc := rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON))
	report, digest := rt.SealReport(t, rdoc)
	edoc := rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, digest)
	edoc["uso"] = routeentry.UseRun
	entry := rt.Marshal(t, edoc)
	rt.Write(t, b.dir, "r", entry, routeentry.Sign(entry, b.key.Priv), report)
	if entradas, err := routeentry.LoadDir(b.cfg); err != nil || entradas[0].Use != routeentry.UseRun {
		t.Fatalf("uso run por extenso: %+v, %v", entradas, err)
	}
}

// casoTool é um registo de uso tool VÁLIDO com UMA coisa estragada.
type casoTool struct {
	nome      string
	causa     string
	relatorio func(doc map[string]any)
	registo   func(doc map[string]any)
	// semRelatorioOCR cita um relatório de qualificação (o dos candidatos) em vez de um `ocr`.
	semRelatorioOCR bool
}

func (c casoTool) correr(t *testing.T) {
	t.Helper()
	a := novoAmbiente(t)
	perfil := rt.ToolProfileJSON
	rdoc := rt.OCRReportDoc(rt.ProfileDigest(t, perfil))
	if c.semRelatorioOCR {
		rdoc = rt.ReportDoc(rt.ProfileDigest(t, perfil))
	}
	if c.relatorio != nil {
		c.relatorio(rdoc)
	}
	report, digest := rt.SealReport(t, rdoc)
	edoc := rt.ToolEntryDoc(t, perfil, rt.Board, rt.Region, digest)
	if c.registo != nil {
		c.registo(edoc)
	}
	entry := rt.Marshal(t, edoc)
	rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), report)
	exigirCausa(t, a.cfg, c.causa, "r")
}

// AS RECUSAS DO USO E DO RELATÓRIO DE UM REGISTO TOOL, uma de cada vez.
func TestAOS530_RecusasDoUso(t *testing.T) {
	casos := []casoTool{
		{nome: "uso fora do vocabulario", causa: routeentry.CauseBadUse, registo: func(d map[string]any) { d["uso"] = "ferramenta" }},
		{nome: "uso em maiusculas", causa: routeentry.CauseBadUse, registo: func(d map[string]any) { d["uso"] = "TOOL" }},
		{nome: "uso vazio", causa: routeentry.CauseBadUse, registo: func(d map[string]any) { d["uso"] = "" }},
		{nome: "uso que nao e texto", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { d["uso"] = true }},
		{nome: "uso com o nome em caixa diferente", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { delete(d, "uso"); d["Uso"] = "tool" }},
		{nome: "tool com fatia maior que zero", causa: routeentry.CauseToolWithShare, registo: func(d map[string]any) { d["fatia_inicial_percentagem"] = 1 }},
		{nome: "tool a citar um relatorio de qualificacao", causa: routeentry.CauseReportOfOtherExperiment, semRelatorioOCR: true},
		{nome: "relatorio ocr sem veredicto (o do AOS-529)", causa: routeentry.CauseReportWithoutVerdict, relatorio: func(d map[string]any) { delete(d, "veredicto_do_ocr") }},
		{nome: "relatorio ocr de outra versao", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { d["versao_do_relatorio"] = "2" }},
		{nome: "relatorio ocr com membro desconhecido", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { d["modo_extra"] = "real" }},
		{nome: "veredicto com membro desconhecido", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { rt.OCRVerdict(d)["veredicto_extra"] = "qualificado" }},
		{nome: "rota medida com membro desconhecido", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { rt.OCRMeasuredRoute(d)["extra"] = "x" }},
		{nome: "modo falso", causa: routeentry.CauseVerdictNotForProduction, relatorio: func(d map[string]any) { d["modo"] = "falso" }},
		{nome: "sem modo (motor local ou falso)", causa: routeentry.CauseVerdictNotForProduction, relatorio: func(d map[string]any) { delete(d, "modo") }},
		{nome: "nao vale para producao", causa: routeentry.CauseVerdictNotForProduction, relatorio: func(d map[string]any) { rt.OCRVerdict(d)["vale_para_producao"] = false }},
		{nome: "veredicto recusado", causa: routeentry.CauseNotQualified, relatorio: func(d map[string]any) { rt.OCRVerdict(d)["veredicto"] = "recusado" }},
		{nome: "outro perfil", causa: routeentry.CauseVerdictOfOtherProfile, relatorio: func(d map[string]any) {
			rt.OCRMeasuredRoute(d)["digest_do_perfil"] = "sha256:" + strings.Repeat("b", 64)
		}},
		{nome: "sem rota medida", causa: routeentry.CauseVerdictOfOtherProfile, relatorio: func(d map[string]any) { delete(rt.OCRVerdict(d), "rota_medida") }},
		{nome: "outra imagem do proxy", causa: routeentry.CauseVerdictOfOtherProxyImage, relatorio: func(d map[string]any) {
			rt.OCRMeasuredRoute(d)["digest_da_imagem_do_proxy"] = "sha256:" + strings.Repeat("c", 64)
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, c.correr)
	}
}

// Os nomes dos membros que o nó lê de um relatório `ocr` são os que as listas fechadas admitem.
func TestAOS530_ListasFechadasDoOCRCobremOQueSeLe(t *testing.T) {
	tem := func(lista []string, nome string) bool {
		for _, n := range lista {
			if n == nome {
				return true
			}
		}
		return false
	}
	doc := rt.OCRReportDoc("sha256:" + strings.Repeat("0", 64))
	for nome := range doc {
		if !tem(routeentry.OCRReportTopFields(), nome) {
			t.Errorf("topo: %q fora da lista", nome)
		}
	}
	for nome := range rt.OCRVerdict(doc) {
		if !tem(routeentry.OCRVerdictFields(), nome) {
			t.Errorf("veredicto: %q fora da lista", nome)
		}
	}
	for nome := range rt.OCRMeasuredRoute(doc) {
		if !tem(routeentry.OCRMeasuredRouteFields(), nome) {
			t.Errorf("rota medida: %q fora da lista", nome)
		}
	}
}
