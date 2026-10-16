package bancoensaio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// AOS-530 (peça 2 de 3) — O VEREDICTO DA EXPERIÊNCIA `ocr` E O REGISTO DE USO `tool`.
//
// Com motores falsos, e com o modelo de visão falso atrás do lançador de teste: nenhum pedido sai do
// posto, nenhuma chave é lida e nada é assinado fora do teste (a chave que aqui assina é gerada no
// teste, para provar que o pacote preparado é o que o nó aceita).

// aos530Perfil escreve o perfil do alias da tool (a allowlist embebida autoriza `gpt-4o-mini`).
func aos530Perfil(t *testing.T, perfil string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "perfil-ocr.json")
	if err := os.WriteFile(caminho, []byte(perfil), 0o600); err != nil {
		t.Fatal(err)
	}
	return caminho
}

// aos530PerfilKimi é o perfil do alias da tool COERENTE com a rota que os testes do motor visao
// medem: o fornecedor kimi do ficheiro de chaves de teste, servido pelo proxy como `openai/<modelo>`.
var aos530PerfilKimi = strings.Replace(rt.ToolProfileJSON, `"openrouter/anthropic/modelo-de-visao"`, `"openai/`+modeloKimiDeTeste+`"`, 1)

// aos530Veredicto corre o motor falso dado e calcula o veredicto com o limiar do dono.
func aos530Veredicto(t *testing.T, perfil string) (*RelatorioOCR, *VeredictoDoOCR) {
	t.Helper()
	r := aos529Correr(t, perfil)
	return r, VeredictoOCR(r, LimiarDeFactosDoOCR, nil)
}

// O MOTOR QUE LÊ TUDO SAI QUALIFICADO; o que erra factos abaixo do limiar, e o que falha uma
// imagem, saem RECUSADOS — e nenhum do modo falso vale para produção.
func TestAOS530_VeredictoComMotoresFalsos(t *testing.T) {
	_, certo := aos530Veredicto(t, FalsoOCRCerto)
	if certo.Veredicto != VeredictoQualificado || len(certo.Razoes) != 0 || certo.ImagensQueFalharam != 0 ||
		certo.PresentesPelaRegra != certo.Factos || certo.ImagensNasContas != 13 || certo.LimiarDeFactos != 0.90 {
		t.Fatalf("o motor que le tudo: %+v", certo)
	}
	if certo.ValeParaProducao || !strings.Contains(certo.PorqueNaoValeParaProducao, "modo real") {
		t.Fatalf("um motor falso nunca vale para producao: %+v", certo)
	}

	// O que perde os acentos: a regra dos factos não pune o acento, e sai qualificado.
	if _, s := aos530Veredicto(t, FalsoOCRSemAcentos); s.Veredicto != VeredictoQualificado {
		t.Fatalf("sem acentos: %+v", s)
	}

	_, erra := aos530Veredicto(t, FalsoOCRErraFactos)
	if erra.Veredicto != VeredictoRecusado || erra.ImagensQueFalharam != 0 || factosNoLimiar(erra.PresentesPelaRegra, erra.Factos, LimiarDeFactosDoOCR) ||
		len(erra.Razoes) != 1 || !strings.Contains(erra.Razoes[0], "abaixo do limiar") {
		t.Fatalf("o motor que erra factos: %+v (%d de %d)", erra, erra.PresentesPelaRegra, erra.Factos)
	}

	// O que falha uma imagem (o PDF): recusado pela imagem não lida, mesmo que a fracção chegasse.
	_, falha := aos530Veredicto(t, FalsoOCRFalhaPDF)
	if falha.Veredicto != VeredictoRecusado || falha.ImagensQueFalharam != 1 || !strings.Contains(strings.Join(falha.Razoes, ";"), "nao foram lidas") {
		t.Fatalf("o motor que falha o PDF: %+v", falha)
	}
	if !factosNoLimiar(falha.PresentesPelaRegra, falha.Factos, LimiarDeFactosDoOCR) {
		t.Log("nota: o motor que falha o PDF tambem fica abaixo do limiar; o caso continua a provar a razao propria")
	}
}

// O LIMIAR NA FRONTEIRA, e uma corrida interrompida ou vazia, recusam pelas razões certas.
func TestAOS530_LimiarEFronteiras(t *testing.T) {
	for _, c := range []struct {
		presentes, factos int
		limiar            float64
		quer              bool
	}{
		{90, 100, 0.90, true}, {89, 100, 0.90, false}, {899, 1000, 0.90, false}, {9, 10, 0.90, true},
		{100, 100, 1, true}, {99, 100, 1, false}, {95, 100, 0.95, true}, {94, 100, 0.95, false},
	} {
		if got := factosNoLimiar(c.presentes, c.factos, c.limiar); got != c.quer {
			t.Errorf("%d de %d com limiar %v: %v; quer %v", c.presentes, c.factos, c.limiar, got, c.quer)
		}
	}
	vazio := VeredictoOCR(&RelatorioOCR{}, LimiarDeFactosDoOCR, nil)
	if vazio.Veredicto != VeredictoRecusado || vazio.ImagensNasContas != 0 {
		t.Fatalf("sem imagens: %+v", vazio)
	}
	r := aos529Correr(t, FalsoOCRCerto)
	r.Interrompida = true
	if v := VeredictoOCR(r, LimiarDeFactosDoOCR, nil); v.Veredicto != VeredictoRecusado {
		t.Fatalf("interrompida: %+v", v)
	}
	// Só o qualificado do modo real vale para produção.
	r = aos529Correr(t, FalsoOCRCerto)
	r.Modo = ModoReal
	if v := VeredictoOCR(r, LimiarDeFactosDoOCR, nil); !v.ValeParaProducao || v.PorqueNaoValeParaProducao != "" {
		t.Fatalf("qualificado do modo real: %+v", v)
	}
	// As imagens fora do motor não entram nas contas.
	r = aos529Correr(t, FalsoOCRCerto)
	r.Imagens[0].Estado = EstadoOCRFora
	if v := VeredictoOCR(r, LimiarDeFactosDoOCR, nil); v.ImagensNasContas != 12 || v.Veredicto != VeredictoQualificado {
		t.Fatalf("com uma imagem fora: %+v", v)
	}
}

// O MODELO DE VISÃO QUE LÊ TUDO sai qualificado; o que devolve respostas CORTADAS (motivo `length`)
// falha cada imagem com a causa `cortada` e sai recusado — com o custo contado na mesma.
func TestAOS530_VisaoCortadaERecusada(t *testing.T) {
	b := aos529Bateria(t)
	m, _ := aos529MotorDeVisao(t, NovaVisaoFalsa(b), 100, nil)
	r := CorrerExperienciaOCR(t.Context(), b, m, aos529Relogio(), nil)
	r.Modo = ModoReal
	if v := VeredictoOCR(r, LimiarDeFactosDoOCR, nil); v.Veredicto != VeredictoQualificado || !v.ValeParaProducao || v.ImagensNasContas != 12 {
		t.Fatalf("o modelo de visao que le tudo: %+v", v)
	}

	cortada := NovaVisaoFalsa(b)
	cortada.Cortar = true
	m, _ = aos529MotorDeVisao(t, cortada, 100, nil)
	r = CorrerExperienciaOCR(t.Context(), b, m, aos529Relogio(), nil)
	r.Modo = ModoReal
	for _, l := range r.Imagens {
		if l.Estado == EstadoOCRFora {
			continue
		}
		if l.Estado != EstadoOCRFalhou || l.Causa != CausaOCRCortada || l.TokensDeEntrada != TokensDeEntradaDaVisaoFalsa {
			t.Fatalf("%s: uma resposta cortada falha a imagem com custo: %+v", l.ID, l)
		}
	}
	if v := VeredictoOCR(r, LimiarDeFactosDoOCR, nil); v.Veredicto != VeredictoRecusado || v.ValeParaProducao || v.ImagensQueFalharam != 12 {
		t.Fatalf("o modelo de visao com respostas cortadas: %+v", v)
	}
}

// O RELATÓRIO ESCRITO tem a versão, o veredicto e o digest de si próprio, e passa pela leitura
// FECHADA do nó: os nomes que o banco escreve são os que o nó conhece.
func TestAOS530_ORelatorioPassaPelaLeituraDoNo(t *testing.T) {
	if VersaoDoRelatorioOCR != routeentry.ReportVersion || ModoReal != routeentry.ModeReal || VeredictoQualificado != routeentry.VerdictQualified ||
		routeentry.ExperimentOCR != "ocr" {
		t.Fatal("as constantes do banco e do no divergem")
	}
	r := aos529Correr(t, FalsoOCRCerto)
	r.Modo = ModoReal
	r.Rota, r.Pedidos, r.Custo = &RotaDoOCR{Fornecedor: "kimi", Modelo: "m"}, &PedidosDoOCR{Enviados: 13, PiorCaso: 13, DiaUTC: "2026-10-15"}, &CustoDoOCR{}
	r.Paragem, r.Interrompida = "x", true // só para os nomes aparecerem no JSON; o veredicto calcula-se abaixo
	rota := &RotaMedidaDoOCR{Fornecedor: "kimi", Modelo: "m", ModeloEsperado: "openai/m", DigestDoPerfil: "sha256:" + strings.Repeat("a", 64),
		ImagemDoProxy: ImagemDoProxy, DigestDaImagemDoProxy: digestDaImagem(ImagemDoProxy)}
	r.VeredictoDoOCR = VeredictoOCR(r, LimiarDeFactosDoOCR, rota)
	r.VeredictoDoOCR.PorqueNaoValeParaProducao = "x"
	emJSON, _, err := EscreverRelatorioOCR(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	cru, _ := os.ReadFile(emJSON)
	if _, err := VerificarDigestDoRelatorio(cru); err != nil {
		t.Fatalf("o relatorio nao confere o seu digest: %v", err)
	}
	var topo map[string]json.RawMessage
	if err := json.Unmarshal(cru, &topo); err != nil {
		t.Fatal(err)
	}
	conferir := func(onde string, membros map[string]json.RawMessage, lista []string) {
		for nome := range membros {
			if !strings.Contains(","+strings.Join(lista, ",")+",", ","+nome+",") {
				t.Errorf("%s: o banco escreve %q, que o no nao conhece", onde, nome)
			}
		}
	}
	conferir("topo", topo, routeentry.OCRReportTopFields())
	var bloco map[string]json.RawMessage
	_ = json.Unmarshal(topo["veredicto_do_ocr"], &bloco)
	conferir("veredicto", bloco, routeentry.OCRVerdictFields())
	var rotaMedida map[string]json.RawMessage
	_ = json.Unmarshal(bloco["rota_medida"], &rotaMedida)
	conferir("rota medida", rotaMedida, routeentry.OCRMeasuredRouteFields())
	if len(topo) < 19 || len(bloco) != len(routeentry.OCRVerdictFields()) || len(rotaMedida) != len(routeentry.OCRMeasuredRouteFields()) {
		t.Errorf("o JSON de teste nao exercita todos os nomes: topo %d, veredicto %d, rota %d", len(topo), len(bloco), len(rotaMedida))
	}
	v, err := routeentry.ReadOCRVerdict(cru)
	if err != nil || v.Mode != ModoReal || v.Verdict != VeredictoRecusado || v.ProfileDigest != rota.DigestDoPerfil || v.ProxyImageDigest != rota.DigestDaImagemDoProxy {
		t.Fatalf("leitura do no: %+v %v", v, err)
	}
	// Um relatório escrito com a rota medida não se escreve por cima de outro.
	if _, _, err := EscreverRelatorioOCR(filepath.Dir(emJSON), r); err == nil {
		t.Fatal("um segundo relatorio no mesmo segundo escreveu por cima do primeiro")
	}
}

// A LINHA DE COMANDOS do `ocr`: o limiar só aperta, e o `--perfil` é o de uma rota de imagem.
func TestAOS530_OCR_OpcoesDoVeredicto(t *testing.T) {
	saida := t.TempDir()
	imagem := aos530Perfil(t, rt.ToolProfileJSON)
	comTools := aos530Perfil(t, strings.Replace(rt.ToolProfileJSON, `["imagem"]`, `["imagem","tools"]`, 1))
	semImagem := aos530Perfil(t, rt.ProfileJSON)
	semCapacidades := aos530Perfil(t, strings.Replace(rt.ToolProfileJSON, `["imagem"]`, `[]`, 1))
	for _, c := range []struct {
		nome string
		args []string
	}{
		{"limiar abaixo do dono", []string{"--limiar-de-factos", "0.5"}},
		{"limiar acima de 1", []string{"--limiar-de-factos", "1.5"}},
		{"limiar NaN", []string{"--limiar-de-factos", "NaN"}},
		{"perfil com tools", []string{"--perfil", comTools}},
		{"perfil sem imagem", []string{"--perfil", semImagem}},
		{"perfil sem imagem nem tools", []string{"--perfil", semCapacidades}},
	} {
		args := append([]string{"ocr", "--motor", "falso", "--saida", saida}, c.args...)
		if e := executar(t, Ambiente{}, args...); e.codigo != SaidaUso {
			t.Errorf("%s: saida %d; quer %d\n%s", c.nome, e.codigo, SaidaUso, e.stderr)
		}
	}
	if e := executar(t, Ambiente{}, "ocr", "--motor", "tesseract", "--imagem", "sha256:"+strings.Repeat("a", 64), "--perfil", imagem, "--saida", saida); e.codigo != SaidaUso {
		t.Errorf("--perfil com o tesseract: %d", e.codigo)
	}
	// O veredicto não muda a saída do `ocr`: o que erra factos sai 0, com o veredicto no relatório.
	e := executar(t, Ambiente{}, "ocr", "--motor", "falso", "--falso", FalsoOCRErraFactos, "--perfil", imagem, "--saida", saida)
	if e.codigo != SaidaOK || !strings.Contains(e.stdout, "VEREDICTO: RECUSADO") || !strings.Contains(e.stdout, "rota medida: fornecedor=falso") {
		t.Fatalf("ocr com erra-factos: %d\n%s", e.codigo, e.stdout)
	}
}

// aos530QualificarOCR corre o `qualificar-ocr` com o modelo de visão (ou o motor falso) dado.
func aos530QualificarOCR(t *testing.T, lanc *lancadorDeTeste, perfil, saida string, extra ...string) execucao {
	t.Helper()
	args := []string{"qualificar-ocr", "--perfil", perfil, "--board", rt.Board, "--regiao", rt.Region, "--saida", saida}
	if lanc != nil {
		args = append(args, "--motor", "visao", "--chaves", escreverChaves(t, chavesDeTeste()), "--visao-fornecedor", "kimi")
	}
	return executar(t, Ambiente{Lancador: lanc}, append(args, extra...)...)
}

// O CAMINHO FELIZ: o modelo de visão que lê tudo, no modo real, deixa o pacote de uso `tool` por
// assinar; assinado no teste, o nó carrega-o como registo de uso `tool`.
func TestAOS530_QualificarOCR_PreparaORegistoTool(t *testing.T) {
	lanc := &lancadorDeTeste{t: t, devolver: NovaVisaoFalsa(aos529Bateria(t))}
	saida := filepath.Join(t.TempDir(), "entrada")
	e := aos530QualificarOCR(t, lanc, aos530Perfil(t, aos530PerfilKimi), saida)
	if e.codigo != SaidaOK || lanc.lancamentos != 1 {
		t.Fatalf("qualificar-ocr: %d, %d lancamentos\n%s\n%s", e.codigo, lanc.lancamentos, e.stdout, e.stderr)
	}
	for _, quer := range []string{"VEREDICTO: QUALIFICADO", "uso tool, fatia 0", "PREPARADO, POR ASSINAR", "aos-issuer route-profile-sign --entry ", "gpt-4o-mini.registo.sig"} {
		if !strings.Contains(e.stdout, quer) {
			t.Errorf("a saida nao diz %q:\n%s", quer, e.stdout)
		}
	}
	pacote := filepath.Join(saida, PastaDoPacote)
	nomes, _ := os.ReadDir(pacote)
	if len(nomes) != 2 {
		t.Fatalf("o pacote tem de ter o registo e o relatorio, e nada mais: %v", nomes)
	}
	registo, err := os.ReadFile(filepath.Join(pacote, "gpt-4o-mini"+routeentry.SuffixEntry))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(registo, &doc); err != nil || doc["uso"] != routeentry.UseTool || doc["fatia_inicial_percentagem"] != float64(0) {
		t.Fatalf("o registo preparado: %v %v", doc, err)
	}
	// Assinado com uma chave do teste, o nó aceita-o como registo de uso tool.
	key := rt.NewKey(t)
	if err := os.WriteFile(filepath.Join(pacote, "gpt-4o-mini"+routeentry.SuffixSignature), []byte(routeentry.Sign(registo, key.Priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	entradas, err := routeentry.LoadDir(routeentry.Config{Dir: pacote, TrustAnchor: key.Anchor(), Board: rt.Board, Region: rt.Region, Allowlist: pol})
	if err != nil || len(entradas) != 1 || !entradas[0].ForTool() || entradas[0].InitialSharePercent != 0 {
		t.Fatalf("o no nao carrega o pacote preparado: %v %+v", err, entradas)
	}
	if c := executar(t, Ambiente{}, "conferir", "--pacote", pacote, "--ancora", key.Anchor(), "--board", rt.Board, "--regiao", rt.Region); c.codigo != SaidaOK {
		t.Fatalf("conferir: %d\n%s", c.codigo, c.stderr)
	}
}

// SEM QUALIFICADO DO MODO REAL NÃO HÁ PACOTE: o motor falso que lê tudo sai 9; o que erra factos e
// o modelo de visão com respostas cortadas saem 7; um perfil que o nó recusaria sai 11 antes de
// enviar. Em nenhum fica `pacote/`.
func TestAOS530_QualificarOCR_SoComQualificadoReal(t *testing.T) {
	perfil := aos530Perfil(t, aos530PerfilKimi)
	semPacote := func(nome, saida string, e execucao, quer int) {
		t.Helper()
		if e.codigo != quer {
			t.Errorf("%s: saida %d; quer %d\n%s\n%s", nome, e.codigo, quer, e.stdout, e.stderr)
		}
		if _, err := os.Stat(filepath.Join(saida, PastaDoPacote)); err == nil {
			t.Errorf("%s: ficou pacote", nome)
		}
		if quer != SaidaPacoteRecusado && !strings.Contains(e.stderr, "NADA PARA ASSINAR") {
			t.Errorf("%s: nao disse que nao ha nada para assinar\n%s", nome, e.stderr)
		}
	}
	saida := filepath.Join(t.TempDir(), "falso-certo")
	semPacote("falso certo", saida, aos530QualificarOCR(t, nil, perfil, saida, "--motor", "falso"), SaidaQualificadoEmEnsaio)
	saida = filepath.Join(t.TempDir(), "falso-erra")
	semPacote("falso erra factos", saida, aos530QualificarOCR(t, nil, perfil, saida, "--motor", "falso", "--falso", FalsoOCRErraFactos), SaidaModeloRecusado)

	cortada := NovaVisaoFalsa(aos529Bateria(t))
	cortada.Cortar = true
	saida = filepath.Join(t.TempDir(), "visao-cortada")
	semPacote("visao cortada", saida, aos530QualificarOCR(t, &lancadorDeTeste{t: t, devolver: cortada}, perfil, saida), SaidaModeloRecusado)

	// Um limiar mais apertado do que o motor consegue: recusado.
	saida = filepath.Join(t.TempDir(), "limiar")
	semPacote("sem acentos com limiar 1", saida, aos530QualificarOCR(t, nil, perfil, saida, "--motor", "falso", "--falso", FalsoOCRSemAcentos, "--limiar-de-factos", "1"), SaidaQualificadoEmEnsaio)

	// O alias fora da allowlist: o nó recusaria o registo, antes de qualquer pedido.
	lanc := &lancadorDeTeste{t: t, devolver: NovaVisaoFalsa(aos529Bateria(t))}
	fora := aos530Perfil(t, strings.Replace(rt.ToolProfileJSON, `"gpt-4o-mini"`, `"ocr-visao"`, 1))
	saida = filepath.Join(t.TempDir(), "fora")
	e := aos530QualificarOCR(t, lanc, fora, saida)
	semPacote("fora da allowlist", saida, e, SaidaPacoteRecusado)
	if lanc.lancamentos != 0 || !strings.Contains(e.stderr, routeentry.CauseNameNotAllowlisted) {
		t.Errorf("fora da allowlist: %d lancamentos\n%s", lanc.lancamentos, e.stderr)
	}
	// O PERFIL DE OUTRO MODELO: com o motor visao, um expected_model que não é a rota desta corrida
	// (`openai/<modelo medido>`) recusa com 3 — antes de abrir o contador e o proxy, sem pedido
	// nenhum e sem pacote.
	lanc = &lancadorDeTeste{t: t, devolver: NovaVisaoFalsa(aos529Bateria(t))}
	saida = filepath.Join(t.TempDir(), "outro-modelo")
	e = aos530QualificarOCR(t, lanc, aos530Perfil(t, rt.ToolProfileJSON), saida)
	semPacote("expected_model de outro modelo", saida, e, SaidaRecusada)
	if lanc.lancamentos != 0 || !strings.Contains(e.stderr, "expected_model do perfil nao e a rota desta corrida (openai/"+modeloKimiDeTeste+")") {
		t.Errorf("expected_model de outro modelo: %d lancamentos\n%s", lanc.lancamentos, e.stderr)
	}
	// O perfil com tools nem chega ao registo.
	saida = filepath.Join(t.TempDir(), "tools")
	comTools := aos530Perfil(t, strings.Replace(rt.ToolProfileJSON, `["imagem"]`, `["imagem","tools"]`, 1))
	if e := aos530QualificarOCR(t, nil, comTools, saida, "--motor", "falso"); e.codigo != SaidaUso {
		t.Errorf("perfil com tools: %d", e.codigo)
	}
	// O motor tesseract não é uma rota do gateway.
	if e := aos530QualificarOCR(t, nil, perfil, filepath.Join(t.TempDir(), "t"), "--motor", "tesseract"); e.codigo != SaidaUso {
		t.Errorf("motor tesseract: %d", e.codigo)
	}
	// Em CI o motor visao não corre.
	if e := executar(t, Ambiente{Getenv: func(v string) string {
		if v == "CI" {
			return "1"
		}
		return ""
	}}, "qualificar-ocr", "--motor", "visao", "--perfil", perfil); e.codigo != SaidaEmCI {
		t.Errorf("em CI: %d", e.codigo)
	}
}
