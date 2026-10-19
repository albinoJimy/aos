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

// AOS-536 (parte 1) — O `qualificar-video` E O RELATÓRIO `video` LIDO PELO NÓ.
//
// Com motores falsos, e com o modelo de vídeo falso atrás do lançador de teste e o codificador de
// teste no lugar do ffmpeg: nenhum pedido sai do posto, nenhuma chave é lida, nenhum contentor
// arranca, e nada é assinado fora do teste (a chave que aqui assina é gerada no teste, para provar
// que o pacote preparado é o que o nó aceita).

// aos536PerfilKimi é o perfil do alias da tool de vídeo COERENTE com a rota que os testes do motor
// visao medem: o fornecedor kimi do ficheiro de chaves de teste, servido pelo proxy como
// `openai/<modelo>`.
var aos536PerfilKimi = strings.Replace(rt.VideoToolProfileJSON, `"openrouter/google/modelo-de-video"`, `"openai/`+modeloKimiDeTeste+`"`, 1)

// aos536QualificarVideo corre o `qualificar-video` com o modelo de vídeo falso (lanc) ou o motor
// falso (lanc nil).
func aos536QualificarVideo(t *testing.T, lanc *lancadorDeTeste, perfil, saida string, extra ...string) execucao {
	t.Helper()
	args := []string{"qualificar-video", "--perfil", perfil, "--board", rt.Board, "--regiao", rt.Region, "--saida", saida}
	amb := Ambiente{codificadorDeTeste: &aos537Codificador{nome: aos536NomeDoCodificador}}
	if lanc != nil {
		amb.Lancador = lanc
		args = append(args, "--motor", "visao", "--chaves", escreverChaves(t, chavesDeTeste()), "--visao-fornecedor", "kimi")
	}
	return executar(t, amb, append(args, extra...)...)
}

// aos536Fornecedor é o modelo de vídeo falso que descreve cada vídeo da bateria pelos factos.
func aos536Fornecedor(t *testing.T) *FornecedorDeVideoFalso {
	t.Helper()
	videos, err := aos537Bateria(t).ComMP4(t.Context(), &aos537Codificador{nome: aos536NomeDoCodificador})
	if err != nil {
		t.Fatal(err)
	}
	return NovoFornecedorDeVideoFalso(videos)
}

// aos536NomeDoCodificador dá ao codificador de teste destes testes um nome só deles: os MP4 ficam em
// cache por processo pela descrição do codificador, e um nome partilhado com os do AOS-537 encheria
// a cache por baixo de um teste que conta os fotogramas que o codificador recebe.
const aos536NomeDoCodificador = "aos536"

// O RELATÓRIO `video` ESCRITO passa pela leitura FECHADA do nó: as constantes coincidem, e os nomes
// que o banco escreve — todos, com os opcionais preenchidos — são os que o nó conhece.
func TestAOS536_ORelatorioVideoPassaPelaLeituraDoNo(t *testing.T) {
	if VersaoDoRelatorioVideo != routeentry.VideoReportVersion || routeentry.ExperimentVideo != "video" {
		t.Fatal("as constantes do banco e do no divergem")
	}
	r := aos537Correr(t, FalsoVideoCerto)
	if r.Experiencia != routeentry.ExperimentVideo {
		t.Fatalf("experiencia %q", r.Experiencia)
	}
	r.Modo, r.Codificador = ModoReal, "codificador"
	r.Instrucao = &InstrucaoDoVideoNoRelatorio{Versao: "1", Digest: "sha256:x", MaxTokens: 1}
	r.Rota, r.Pedidos, r.Custo = &RotaDoOCR{Fornecedor: "kimi", Modelo: "m"}, &PedidosDoOCR{Enviados: 9, PiorCaso: 9, DiaUTC: "2026-10-19"}, &CustoDoOCR{}
	r.Paragem, r.Interrompida = "x", true // só para os nomes aparecerem no JSON; o veredicto calcula-se abaixo
	rota := &RotaMedidaDoOCR{Fornecedor: "kimi", Modelo: "m", ModeloEsperado: "openai/m", DigestDoPerfil: "sha256:" + strings.Repeat("a", 64),
		ImagemDoProxy: ImagemDoProxy, DigestDaImagemDoProxy: digestDaImagem(ImagemDoProxy)}
	r.VeredictoDoVideo = VeredictoVideo(r, LimiarDeFactosDoVideo, rota)
	emJSON, _, err := EscreverRelatorioVideo(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	cru, _ := os.ReadFile(emJSON)
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
	conferir("topo", topo, routeentry.VideoReportTopFields())
	var bloco map[string]json.RawMessage
	_ = json.Unmarshal(topo["veredicto_do_video"], &bloco)
	conferir("veredicto", bloco, routeentry.VideoVerdictFields())
	var rotaMedida map[string]json.RawMessage
	_ = json.Unmarshal(bloco["rota_medida"], &rotaMedida)
	conferir("rota medida", rotaMedida, routeentry.OCRMeasuredRouteFields())
	if len(topo) != len(routeentry.VideoReportTopFields()) || len(bloco) != len(routeentry.VideoVerdictFields()) || len(rotaMedida) != len(routeentry.OCRMeasuredRouteFields()) {
		t.Errorf("o JSON de teste nao exercita todos os nomes: topo %d, veredicto %d, rota %d", len(topo), len(bloco), len(rotaMedida))
	}
	v, err := routeentry.ReadVideoVerdict(cru)
	if err != nil || v.Mode != ModoReal || v.Verdict != VeredictoRecusado || v.ProfileDigest != rota.DigestDoPerfil || v.ProxyImageDigest != rota.DigestDaImagemDoProxy {
		t.Fatalf("leitura do no: %+v %v", v, err)
	}
	// O leitor `ocr` do nó não o lê: um relatório `video` não serve um registo de imagem.
	if _, err := routeentry.ReadOCRVerdict(cru); routeentry.CauseOf(err) != routeentry.CauseReportOfOtherExperiment {
		t.Fatalf("o leitor ocr leu o relatorio video: %v", err)
	}
}

// O CAMINHO FELIZ: o modelo de vídeo que descreve tudo, no modo real, deixa o pacote de uso `tool`
// de vídeo por assinar; assinado no teste, o nó carrega-o como registo de uso `tool` de vídeo, e o
// `conferir` diz o uso e a media.
func TestAOS536_QualificarVideo_PreparaORegistoToolDeVideo(t *testing.T) {
	lanc := &lancadorDeTeste{t: t, devolver: aos536Fornecedor(t)}
	saida := filepath.Join(t.TempDir(), "entrada")
	e := aos536QualificarVideo(t, lanc, aos530Perfil(t, aos536PerfilKimi), saida)
	if e.codigo != SaidaOK || lanc.lancamentos != 1 || lanc.pedido.Alias != AliasDoVideo {
		t.Fatalf("qualificar-video: %d, %d lancamentos\n%s\n%s", e.codigo, lanc.lancamentos, e.stdout, e.stderr)
	}
	for _, quer := range []string{"experiencia video (AOS-537)", "VEREDICTO: QUALIFICADO", "REGISTO DE ENTRADA DE USO TOOL DE VIDEO (AOS-536)", "uso tool, fatia 0",
		"PREPARADO, POR ASSINAR", "aos-issuer route-profile-sign --entry ", "gpt-4o-mini.registo.sig", "aos-ensaio conferir --pacote "} {
		if !strings.Contains(e.stdout, quer) {
			t.Errorf("a saida nao diz %q:\n%s", quer, e.stdout)
		}
	}
	if !strings.Contains(e.stderr, "QUALIFICACAO de um perfil (AOS-537)") {
		t.Errorf("a corrida nao contou nas qualificacoes do dia:\n%s", e.stderr)
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
	citado, _ := os.ReadFile(filepath.Join(pacote, "gpt-4o-mini"+routeentry.SuffixReport))
	if v, err := routeentry.ReadVideoVerdict(citado); err != nil || v.Verdict != VeredictoQualificado || v.Mode != ModoReal {
		t.Fatalf("o relatorio citado: %+v %v", v, err)
	}
	// Assinado com uma chave do teste, o nó aceita-o como registo de uso tool de vídeo.
	key := rt.NewKey(t)
	if err := os.WriteFile(filepath.Join(pacote, "gpt-4o-mini"+routeentry.SuffixSignature), []byte(routeentry.Sign(registo, key.Priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	entradas, err := routeentry.LoadDir(routeentry.Config{Dir: pacote, TrustAnchor: key.Anchor(), Board: rt.Board, Region: rt.Region, Allowlist: pol})
	if err != nil || len(entradas) != 1 || !entradas[0].ForTool() || entradas[0].Media != "video" || entradas[0].InitialSharePercent != 0 {
		t.Fatalf("o no nao carrega o pacote preparado: %v %+v", err, entradas)
	}
	c := executar(t, Ambiente{}, "conferir", "--pacote", pacote, "--ancora", key.Anchor(), "--board", rt.Board, "--regiao", rt.Region)
	if c.codigo != SaidaOK || !strings.Contains(c.stdout, "uso: tool") || !strings.Contains(c.stdout, "media: video — o relatorio citado e da experiencia video") {
		t.Fatalf("conferir: %d\n%s\n%s", c.codigo, c.stdout, c.stderr)
	}
}

// O `conferir` de um pacote de imagem (AOS-530) diz a media `imagem` e a experiência `ocr`.
func TestAOS536_ConferirDizAMediaDeImagem(t *testing.T) {
	pacote := t.TempDir()
	key := rt.NewKey(t)
	rt.GoodTool(t, pacote, "ocr", rt.ToolProfileJSON, rt.Board, rt.Region, key)
	c := executar(t, Ambiente{}, "conferir", "--pacote", pacote, "--ancora", key.Anchor(), "--board", rt.Board, "--regiao", rt.Region)
	if c.codigo != SaidaOK || !strings.Contains(c.stdout, "media: imagem — o relatorio citado e da experiencia ocr") {
		t.Fatalf("conferir: %d\n%s\n%s", c.codigo, c.stdout, c.stderr)
	}
}

// SEM QUALIFICADO DO MODO REAL NÃO HÁ PACOTE: o motor falso que descreve tudo sai 9; o que erra
// factos e o modelo de vídeo com respostas cortadas saem 7; o expected_model de outro modelo sai 3
// antes de enviar; um perfil que o nó recusaria sai 11 ou 2 antes de enviar. Em nenhum fica
// `pacote/`.
func TestAOS536_QualificarVideo_SoComQualificadoReal(t *testing.T) {
	perfil := aos530Perfil(t, aos536PerfilKimi)
	semPacote := func(nome, saida string, e execucao, quer int) {
		t.Helper()
		if e.codigo != quer {
			t.Errorf("%s: saida %d; quer %d\n%s\n%s", nome, e.codigo, quer, e.stdout, e.stderr)
		}
		if _, err := os.Stat(filepath.Join(saida, PastaDoPacote)); err == nil {
			t.Errorf("%s: ficou pacote", nome)
		}
		if quer == SaidaQualificadoEmEnsaio || quer == SaidaModeloRecusado {
			if !strings.Contains(e.stderr, "NADA PARA ASSINAR") {
				t.Errorf("%s: nao disse que nao ha nada para assinar\n%s", nome, e.stderr)
			}
		}
	}
	saida := filepath.Join(t.TempDir(), "falso-certo")
	semPacote("falso certo", saida, aos536QualificarVideo(t, nil, perfil, saida, "--motor", "falso"), SaidaQualificadoEmEnsaio)
	saida = filepath.Join(t.TempDir(), "falso-erra")
	semPacote("falso erra factos", saida, aos536QualificarVideo(t, nil, perfil, saida, "--motor", "falso", "--falso", FalsoVideoErraFactos), SaidaModeloRecusado)
	saida = filepath.Join(t.TempDir(), "falso-cortado")
	semPacote("falso cortado", saida, aos536QualificarVideo(t, nil, perfil, saida, "--motor", "falso", "--falso", FalsoVideoCortado), SaidaModeloRecusado)

	cortada := aos536Fornecedor(t)
	cortada.Cortar = true
	saida = filepath.Join(t.TempDir(), "visao-cortada")
	semPacote("visao cortada", saida, aos536QualificarVideo(t, &lancadorDeTeste{t: t, devolver: cortada}, perfil, saida), SaidaModeloRecusado)

	// O PERFIL DE OUTRO MODELO: com o motor visao, um expected_model que não é a rota desta corrida
	// recusa com 3 — antes do contador, do ffmpeg e do proxy, sem pedido nenhum e sem pacote.
	lanc := &lancadorDeTeste{t: t, devolver: aos536Fornecedor(t)}
	saida = filepath.Join(t.TempDir(), "outro-modelo")
	e := aos536QualificarVideo(t, lanc, aos530Perfil(t, rt.VideoToolProfileJSON), saida)
	semPacote("expected_model de outro modelo", saida, e, SaidaRecusada)
	if lanc.lancamentos != 0 || !strings.Contains(e.stderr, "expected_model do perfil nao e a rota desta corrida (openai/"+modeloKimiDeTeste+")") {
		t.Errorf("expected_model de outro modelo: %d lancamentos\n%s", lanc.lancamentos, e.stderr)
	}

	// O alias de produção `video-visao` com a allowlist EMBEBIDA, que não o autoriza: 11 antes de
	// qualquer pedido. (Em produção corre-se com a allowlist externa do nó.)
	lanc = &lancadorDeTeste{t: t, devolver: aos536Fornecedor(t)}
	fora := aos530Perfil(t, strings.Replace(aos536PerfilKimi, `"gpt-4o-mini"`, `"video-visao"`, 1))
	saida = filepath.Join(t.TempDir(), "fora")
	e = aos536QualificarVideo(t, lanc, fora, saida)
	semPacote("fora da allowlist", saida, e, SaidaPacoteRecusado)
	if lanc.lancamentos != 0 || !strings.Contains(e.stderr, routeentry.CauseNameNotAllowlisted) {
		t.Errorf("fora da allowlist: %d lancamentos\n%s", lanc.lancamentos, e.stderr)
	}

	// Os perfis com as capacidades erradas nem chegam ao registo: 2, sem pedido.
	for nome, caps := range map[string]string{
		"video e imagem": `["imagem","video"]`, "video e tools": `["tools","video"]`, "so imagem": `["imagem"]`, "sem capacidades": `[]`,
	} {
		lanc := &lancadorDeTeste{t: t, devolver: aos536Fornecedor(t)}
		p := aos530Perfil(t, strings.Replace(aos536PerfilKimi, `["video"]`, caps, 1))
		saida := filepath.Join(t.TempDir(), "caps")
		if e := aos536QualificarVideo(t, lanc, p, saida); e.codigo != SaidaUso || lanc.lancamentos != 0 {
			t.Errorf("perfil %s: %d, %d lancamentos\n%s", nome, e.codigo, lanc.lancamentos, e.stderr)
		}
		if _, err := os.Stat(saida); err == nil {
			t.Errorf("perfil %s: criou a pasta de saida", nome)
		}
	}
	// O motor tesseract não é deste comando.
	if e := aos536QualificarVideo(t, nil, perfil, filepath.Join(t.TempDir(), "t"), "--motor", "tesseract"); e.codigo != SaidaUso {
		t.Errorf("motor tesseract: %d", e.codigo)
	}
	// Em CI o motor visao não corre.
	if e := executar(t, Ambiente{Getenv: func(v string) string {
		if v == "CI" {
			return "1"
		}
		return ""
	}}, "qualificar-video", "--motor", "visao", "--perfil", perfil); e.codigo != SaidaEmCI {
		t.Errorf("em CI: %d", e.codigo)
	}
}

// aos536PerfilDeProducao é o perfil do alias de produção escolhido pelo dono (2026-10-19), BYTE A
// BYTE o do exemplo do README do banco.
const aos536PerfilDeProducao = `{"requested":"video-visao","expected_model":"openrouter/google/gemini-3.5-flash-lite","wire_class":"openai-chat-completions","capabilities":["video"]}`

// O PERFIL DO README passa as conferências do `qualificar-video` até à corrida: com a allowlist
// externa que autoriza `video-visao`, o motor falso corre e sai 9 (nada para assinar); com a
// embebida, que não o autoriza, sai 11 antes de correr.
func TestAOS536_OPerfilDoREADMEServe(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil || !strings.Contains(string(readme), aos536PerfilDeProducao) {
		t.Fatalf("o README nao traz o perfil de producao byte a byte: %v", err)
	}
	perfil := aos530Perfil(t, aos536PerfilDeProducao)
	dir, ancora := aos522AllowlistExterna(t, rt.Board, "video-visao", rt.Region)
	saida := filepath.Join(t.TempDir(), "com-allowlist")
	e := aos536QualificarVideo(t, nil, perfil, saida, "--motor", "falso", "--allowlist-dir", dir, "--ancora-da-allowlist", ancora)
	if e.codigo != SaidaQualificadoEmEnsaio || !strings.Contains(e.stdout, `alias "video-visao"`) {
		t.Fatalf("com a allowlist externa: %d\n%s\n%s", e.codigo, e.stdout, e.stderr)
	}
	saida = filepath.Join(t.TempDir(), "embebida")
	if e := aos536QualificarVideo(t, nil, perfil, saida, "--motor", "falso"); e.codigo != SaidaPacoteRecusado || !strings.Contains(e.stderr, routeentry.CauseNameNotAllowlisted) {
		t.Fatalf("com a allowlist embebida: %d\n%s", e.codigo, e.stderr)
	}
}

// A EXPERIÊNCIA `video` aceita um perfil com a capacidade `video`, e só diz que o perfil não a
// declara quando não a declara.
func TestAOS536_ExperienciaVideoAceitaOPerfilDeVideo(t *testing.T) {
	ler := func(perfil string) *RelatorioVideo {
		t.Helper()
		saida := t.TempDir()
		e := executar(t, Ambiente{}, "video", "--motor", "falso", "--perfil", aos530Perfil(t, perfil), "--saida", saida)
		if e.codigo != SaidaOK {
			t.Fatalf("video com o perfil: %d\n%s", e.codigo, e.stderr)
		}
		feitos, _ := filepath.Glob(filepath.Join(saida, "ensaio-video-falso-*.json"))
		if len(feitos) != 1 {
			t.Fatalf("relatorios: %v", feitos)
		}
		cru, _ := os.ReadFile(feitos[0])
		var r RelatorioVideo
		if err := json.Unmarshal(cru, &r); err != nil || r.VeredictoDoVideo == nil || r.VeredictoDoVideo.RotaMedida == nil {
			t.Fatalf("relatorio: %v", err)
		}
		return &r
	}
	const nota = "o perfil medido nao declara a capacidade video"
	if r := ler(aos536PerfilKimi); strings.Contains(strings.Join(r.LimitesDaMedicao, "\n"), nota) || strings.Contains(strings.Join(r.LimitesDaMedicao, "\n"), "AOS-535): identifica-se so pelo digest") {
		t.Errorf("um perfil com video tem a nota de quem nao o declara: %v", r.LimitesDaMedicao)
	}
	if r := ler(strings.Replace(aos536PerfilKimi, `["video"]`, `[]`, 1)); !strings.Contains(strings.Join(r.LimitesDaMedicao, "\n"), nota) {
		t.Errorf("um perfil sem video nao o diz: %v", r.LimitesDaMedicao)
	}
}
