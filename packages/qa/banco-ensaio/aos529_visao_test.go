package bancoensaio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AOS-529 — O BRAÇO DO MODELO DE VISÃO, com um modelo de visão falso. Nenhum pedido sai do posto.

// aos529TextoSoDoModelo é um pedaço do texto de origem que não é facto de nenhum documento: se
// aparecer no relatório, no resumo ou nas saídas, o texto do modelo fugiu.
const aos529TextoSoDoModelo = "Recebemos de"

func aos529MotorDeVisao(t *testing.T, falso *VisaoFalsa, tecto int64, preco *Preco) (*MotorOCRVisao, *Contador) {
	t.Helper()
	srv := httptest.NewServer(falso)
	t.Cleanup(srv.Close)
	contador, err := AbrirContador(filepath.Join(t.TempDir(), "contador.json"), FornecedorKimi, Tectos{PedidosDia: tecto}, nil, "", relogioFixo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(contador.Fechar)
	m, err := NovoMotorOCRVisao(CfgDaVisao{BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Fornecedor: "kimi", Modelo: "modelo-de-visao", Contador: contador, Preco: preco})
	if err != nil {
		t.Fatal(err)
	}
	return m, contador
}

func TestAOS529_Visao_LeAsImagensDeixaOPDFForaEContaOCusto(t *testing.T) {
	b := aos529Bateria(t)
	falso := NovaVisaoFalsa(b)
	preco := &Preco{EntradaMicroUSDPorMTok: 1_000_000, SaidaMicroUSDPorMTok: 5_000_000}
	m, contador := aos529MotorDeVisao(t, falso, 100, preco)
	guardados := map[string]string{}
	r := CorrerExperienciaOCR(context.Background(), b, m, aos529Relogio(), func(id, texto string) { guardados[id] = texto })
	porImagem := custoMicroUSD(*preco, TokensDeEntradaDaVisaoFalsa, TokensDeSaidaDaVisaoFalsa)
	for _, l := range r.Imagens {
		switch {
		case l.Formato == FormatoPDF:
			if l.Estado != EstadoOCRFora || l.Causa != CausaOCRFormato || l.CustoMicroUSD != nil {
				t.Errorf("o PDF fica fora do braco de visao: %+v", l)
			}
		case l.Estado != EstadoOCRLida || l.PresentesComAcentos != l.Factos || l.CustoMicroUSD == nil || *l.CustoMicroUSD != porImagem ||
			l.TokensDeEntrada != TokensDeEntradaDaVisaoFalsa || l.MemoriaPicoBytes != -1 || l.TempoMs != 500:
			t.Errorf("%s: %+v", l.ID, l)
		}
	}
	total := aos529Total(t, r, "total")
	if total.Imagens != 12 || total.Fora != 1 || total.Lidas != 12 || total.PresentesComAcentos != total.Factos || total.Factos != 74 ||
		total.CustoMicroUSD == nil || *total.CustoMicroUSD != 12*porImagem {
		t.Errorf("total: %+v", total)
	}
	if c, ok := m.CustoMicroUSD(); !ok || c != 12*porImagem {
		t.Errorf("custo da corrida %d %v", c, ok)
	}
	if e, _ := contador.Estado(); e.EnviadosNestaCorrida != 12 || falso.Recebidos != 12 {
		t.Errorf("um pedido por imagem, contado: %d no contador, %d no falso", e.EnviadosNestaCorrida, falso.Recebidos)
	}
	if len(guardados) != 12 || !strings.Contains(guardados["recibo-limpo"], aos529TextoSoDoModelo) {
		t.Errorf("o texto lido so vai para quem o guarda: %d", len(guardados))
	}
	cru, _ := json.Marshal(r)
	if strings.Contains(string(cru)+ResumoOCREmTexto(r), aos529TextoSoDoModelo) {
		t.Error("o texto do modelo fugiu para o relatorio ou o resumo")
	}
	if PiorCasoDaVisao(b) != 13 {
		t.Errorf("pior caso: %d (quer 12 imagens e 1 sonda)", PiorCasoDaVisao(b))
	}
}

func TestAOS529_Visao_UmModeloQuePerdeAcentosVeSe(t *testing.T) {
	b := aos529Bateria(t)
	falso := NovaVisaoFalsa(b)
	falso.SemAcentos = true
	m, _ := aos529MotorDeVisao(t, falso, 100, nil)
	r := CorrerExperienciaOCR(context.Background(), b, m, aos529Relogio(), nil)
	com := aos529Total(t, r, "com acentos")
	if com.PresentesPelaRegra != com.Factos || com.PresentesComAcentos >= com.Factos {
		t.Errorf("com acentos: %+v", com)
	}
	for _, l := range r.Imagens {
		if l.Estado == EstadoOCRLida && l.CustoMicroUSD != nil {
			t.Error("sem preco declarado nao ha custo")
		}
	}
}

func TestAOS529_Visao_OTectoEARecusaParamACorrida(t *testing.T) {
	b := aos529Bateria(t)
	m, _ := aos529MotorDeVisao(t, NovaVisaoFalsa(b), 3, nil)
	r := CorrerExperienciaOCR(context.Background(), b, m, aos529Relogio(), nil)
	if !r.Interrompida || r.Paragem != TerminouTecto || len(r.Imagens) != 3 {
		t.Errorf("tecto de 3: interrompida %v, paragem %q, %d imagens", r.Interrompida, r.Paragem, len(r.Imagens))
	}
	recusa := NovaVisaoFalsa(b)
	recusa.Estado = 401
	m, _ = aos529MotorDeVisao(t, recusa, 100, nil)
	r = CorrerExperienciaOCR(context.Background(), b, m, aos529Relogio(), nil)
	if !r.Interrompida || !strings.Contains(r.Paragem, "HTTP 401") || recusa.Recebidos != 1 || strings.Contains(ResumoOCREmTexto(r), "recusado pelo falso") {
		t.Errorf("uma chave recusada para a corrida ao primeiro pedido, sem o corpo do erro: %q, %d pedidos", r.Paragem, recusa.Recebidos)
	}
	erro := NovaVisaoFalsa(b)
	erro.Estado = 400
	m, _ = aos529MotorDeVisao(t, erro, 100, nil)
	r = CorrerExperienciaOCR(context.Background(), b, m, aos529Relogio(), nil)
	if r.Interrompida || aos529Total(t, r, "total").Lidas != 0 || !strings.Contains(ResumoOCREmTexto(r), "falhou:motor") {
		t.Error("um 400 falha a imagem, nao a corrida")
	}
	if _, err := NovoMotorOCRVisao(CfgDaVisao{BaseURL: "http://x/v1", Modelo: "m"}); !errors.Is(err, ErrVisao) {
		t.Error("sem contador nao ha motor de visao")
	}
}

// Pela linha de comandos: as regras do modo real, com o lançador de teste a fazer de proxy.
func TestAOS529_Visao_LinhaDeComandos(t *testing.T) {
	b := aos529Bateria(t)
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)
	lanc := &lancadorDeTeste{t: t, devolver: NovaVisaoFalsa(b)}
	saida := filepath.Join(pasta, "relatorios")

	if e := executar(t, Ambiente{Lancador: lanc, Getenv: func(v string) string {
		if v == "CI" {
			return "1"
		}
		return ""
	}}, "ocr", "--motor", "visao", "--chaves", chaves, "--visao-fornecedor", "kimi"); e.codigo != SaidaEmCI {
		t.Errorf("em CI: %d", e.codigo)
	}
	if e := executar(t, Ambiente{Lancador: lanc}, "ocr", "--motor", "visao", "--visao-fornecedor", "kimi", "--saida", saida); e.codigo != SaidaRecusada {
		t.Errorf("sem chaves: %d", e.codigo)
	}
	if e := executar(t, Ambiente{Lancador: lanc}, "ocr", "--motor", "visao", "--chaves", chaves, "--visao-fornecedor", "kimi", "--visao-modelo", "fora-da-lista"); e.codigo != SaidaRecusada {
		t.Errorf("modelo fora da lista do dono: %d", e.codigo)
	}
	if e := executar(t, Ambiente{Lancador: lanc}, "ocr", "--motor", "falso", "--chaves", chaves, "--saida", saida); e.codigo != SaidaUso {
		t.Errorf("--chaves sem o motor visao: %d", e.codigo)
	}
	e := executar(t, Ambiente{Lancador: lanc}, "ocr", "--motor", "visao", "--chaves", chaves, "--visao-fornecedor", "kimi", "--so-plano")
	if e.codigo != SaidaOK || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "pior caso 13 pedidos") || !strings.Contains(e.stderr, "DESTINO DA CHAVE: https://api.kimi.com") {
		t.Fatalf("so plano: %d, %d lancamentos\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}

	e = executar(t, Ambiente{Lancador: lanc}, "ocr", "--motor", "visao", "--chaves", chaves, "--visao-fornecedor", "kimi", "--saida", saida, "--guardar-respostas")
	if e.codigo != SaidaOK || lanc.lancamentos != 1 || lanc.pedido.Alias != AliasDaVisao || lanc.pedido.apiKey != sentinelaChaveKimi || lanc.pedido.Modelo != modeloKimiDeTeste {
		t.Fatalf("corrida: %d, pedido %+v\n%s", e.codigo, lanc.pedido, e.stderr)
	}
	relatorios, _ := filepath.Glob(filepath.Join(saida, "ensaio-real-ocr-visao-*.json"))
	if len(relatorios) != 1 {
		t.Fatalf("relatorios: %v", relatorios)
	}
	cru, _ := os.ReadFile(relatorios[0])
	var r RelatorioOCR
	if err := json.Unmarshal(cru, &r); err != nil || r.Modo != ModoReal || r.Rota.Fornecedor != "kimi" || r.Pedidos.Enviados != 13 || r.Pedidos.PiorCaso != 13 {
		t.Fatalf("relatorio: %v %+v %+v", err, r.Rota, r.Pedidos)
	}
	if pedidos, _, n := UsoDosRelatoriosDeHoje([]string{saida}, FornecedorKimi, relogioFixo().Format("2006-01-02")); pedidos != 13 || n != 1 {
		t.Errorf("o contador do dia reconstroi-se tambem deste relatorio: %d pedidos em %d", pedidos, n)
	}
	guardadas, _ := filepath.Glob(filepath.Join(saida, PrefixoDaPastaDasRespostas+"*", "recibo-limpo.json"))
	if len(guardadas) != 1 {
		t.Fatalf("leituras guardadas: %v", guardadas)
	}
	lida, _ := os.ReadFile(guardadas[0])
	if !strings.Contains(string(lida), aos529TextoSoDoModelo) {
		t.Error("a leitura guardada tem o texto")
	}
	impresso := tudoOQueFoiEscrito(t, e) + string(cru)
	if strings.Contains(impresso, aos529TextoSoDoModelo) {
		t.Error("o texto do modelo fugiu para as saidas ou o relatorio")
	}
	verSemFugas(t, "visao", impresso, []string{sentinelaChaveKimi, sentinelaBaseKimi, chaveMestraDeTeste})

	// O pior caso não cabe num tecto de 12 pedidos: recusa antes de enviar.
	campos := chavesDeTeste()
	campos[CampoTectoPedidosKimi] = "12"
	apertado := escreverChaves(t, campos)
	lanc2 := &lancadorDeTeste{t: t, devolver: NovaVisaoFalsa(b)}
	if e := executar(t, Ambiente{Lancador: lanc2}, "ocr", "--motor", "visao", "--chaves", apertado, "--visao-fornecedor", "kimi"); e.codigo != SaidaRecusada || lanc2.lancamentos != 0 {
		t.Errorf("tecto de 12: %d, %d lancamentos", e.codigo, lanc2.lancamentos)
	}
}
