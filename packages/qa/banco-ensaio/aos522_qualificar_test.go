package bancoensaio

// AOS-522 — QUALIFICAR E PREPARAR A ASSINATURA NUM COMANDO.
//
// Tudo com provider FALSO e lançador falso: nenhum pedido sai da máquina, e a única chave de
// assinar que aqui existe é gerada no teste — para provar que o pacote que o comando prepara,
// depois de assinado POR FORA, carrega na função que o nó usa no arranque.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// aos522Perfil é um perfil com um alias que a allowlist embebida autoriza (`gpt-4o`, que a tabela
// em código também tem) e o modelo que o proxy do lançador falso declara servir.
const aos522Perfil = `{"requested":"gpt-4o","expected_model":"openai/` + modeloKimiDeTeste + `","wire_class":"openai-chat-completions","capabilities":["tools"]}`

// aos522Posto é o posto do dono num teste: o ficheiro de chaves de teste, o lançador falso e um
// relógio que avança um segundo de cada vez que é lido.
type aos522Posto struct {
	t      *testing.T
	chaves string
	lanc   *lancadorDaQualificacao
	amb    Ambiente
	lidas  map[string]bool
}

func aos522NovoPosto(t *testing.T, roteiro ...Comportamento) *aos522Posto {
	t.Helper()
	campos := chavesDeTeste()
	campos[CampoTectoPedidosKimi] = "5000"
	p := &aos522Posto{t: t, chaves: escreverChaves(t, campos), lanc: &lancadorDaQualificacao{t: t, roteiro: roteiro}, lidas: map[string]bool{}}
	hora := relogioFixo()
	p.amb = Ambiente{
		Lancador: p.lanc,
		Relogio:  func() time.Time { hora = hora.Add(time.Second); return hora },
		Getenv:   func(nome string) string { p.lidas[nome] = true; return "" },
	}
	return p
}

// real devolve os argumentos de um `qualificar real` completo, com a pasta de saída dada.
func (p *aos522Posto) real(saida, perfil string, extra ...string) []string {
	return append([]string{"qualificar", "real", "--chaves", p.chaves, "--fornecedor", "kimi", "--perfil", aos516Ficheiro(p.t, perfil),
		"--fatia", "10", "--board", routeentrytest.Board, "--regiao", routeentrytest.Region, "--saida", saida}, extra...)
}

// aos522Ficheiros devolve os caminhos, relativos à pasta, de tudo o que lá está.
func aos522Ficheiros(t *testing.T, pasta string) []string {
	t.Helper()
	var nomes []string
	_ = filepath.WalkDir(pasta, func(caminho string, d os.DirEntry, err error) error {
		if err == nil && caminho != pasta {
			rel, _ := filepath.Rel(pasta, caminho)
			nomes = append(nomes, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(nomes)
	return nomes
}

// aos522SemRegisto confere que a pasta tem o relatório e o resumo e NÃO tem registo de entrada —
// nem parcial, nem com outro nome: nenhum ficheiro além dos três, e nenhum com o formato do registo.
func aos522SemRegisto(t *testing.T, saida string) {
	t.Helper()
	nomes := aos522Ficheiros(t, saida)
	if len(nomes) != 3 || nomes[2] != NomeDoResumoDeUmaPagina || !strings.HasSuffix(nomes[0], ".json") || !strings.HasSuffix(nomes[1], ".txt") {
		t.Fatalf("a pasta de saida tinha de ter o relatorio (.json e .txt) e o resumo, e mais nada: %v", nomes)
	}
	for _, n := range nomes {
		cru, _ := os.ReadFile(filepath.Join(saida, n))
		if bytes.Contains(cru, []byte(routeentry.EntryFormat)) || strings.Contains(n, "registo") {
			t.Errorf("ha um registo de entrada em %s", n)
		}
	}
}

// aos522NumaLinha desfaz as quebras de linha do resumo, para se procurar uma frase inteira.
func aos522NumaLinha(s string) string { return strings.Join(strings.Fields(s), " ") }

// O CAMINHO FELIZ. Uma rota boa em modo real sai com o pacote preparado; o registo cita o digest do
// relatório QUE ESTÁ NA PASTA; e o pacote, assinado por fora com uma chave de teste, carrega em
// routeentry.LoadDir — a função do arranque do nó — e passa no `conferir`.
func TestAOS522_CaminhoFeliz_PreparaOPacoteQueONoCarrega(t *testing.T) {
	p := aos522NovoPosto(t, ComportamentoCumpre)
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, p.real(saida, aos522Perfil)...)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	nomes := aos522Ficheiros(t, saida)
	pacote := filepath.Join(saida, PastaDoPacote)
	if len(nomes) != 6 || !reflect.DeepEqual(aos522Ficheiros(t, pacote), []string{"gpt-4o.registo.json", "gpt-4o.relatorio.json"}) {
		t.Fatalf("pasta de saida = %v", nomes)
	}
	// O registo cita o digest do relatório que está na pasta: recalcula-se aqui, dos dois ficheiros.
	registo, _ := os.ReadFile(filepath.Join(pacote, "gpt-4o.registo.json"))
	citado, _ := os.ReadFile(filepath.Join(pacote, "gpt-4o.relatorio.json"))
	daCorrida, _ := os.ReadFile(aos518Relatorios(t, saida)[0])
	if !bytes.Equal(citado, daCorrida) {
		t.Fatal("o relatorio do pacote nao e, byte a byte, o que a corrida escreveu")
	}
	recalculado, err := DigestDoRelatorio(daCorrida)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Board  string `json:"board"`
		Regiao string `json:"regiao"`
		Citado string `json:"digest_do_relatorio"`
		Fatia  int    `json:"fatia_inicial_percentagem"`
	}
	if err := json.Unmarshal(registo, &doc); err != nil || doc.Citado != recalculado || doc.Fatia != 10 || doc.Board != routeentrytest.Board || doc.Regiao != routeentrytest.Region {
		t.Fatalf("registo = %+v (%v); o digest recalculado do relatorio e %s", doc, err, recalculado)
	}

	// O resumo: os digests, a fatia, o alias, o modelo esperado, e que SUBSTITUI a tabela em código.
	resumo, _ := os.ReadFile(filepath.Join(saida, NomeDoResumoDeUmaPagina))
	for _, quer := range []string{
		TituloDoVeredicto + " QUALIFICADO", TituloDoQueSeAssina, `alias pedido: "gpt-4o"`, `modelo esperado: "openai/` + modeloKimiDeTeste + `"`, "fatia inicial: 10%",
		`board: "board-eu"; regiao: "eu"`, "SUBSTITUI a entrada da tabela de perfis em codigo", recalculado, routeentry.ProductionProxyImageDigest(),
		routeentry.EntryDigest(registo), "AVISO — TECTO_EM_DOLARES_NAO_IMPOSTO", "pior caso declarado antes de enviar: 673",
	} {
		if !strings.Contains(aos522NumaLinha(string(resumo)), quer) {
			t.Errorf("o resumo nao diz %q:\n%s", quer, resumo)
		}
	}
	if !strings.Contains(e.stdout, string(resumo)) {
		t.Error("o resumo escrito nao e o mostrado")
	}
	// O RELÓGIO: cada fase com início e fim, pela ordem, e nenhuma com duração zero.
	relogio := string(resumo)[strings.Index(string(resumo), TituloDoRelogio):]
	ultimo := 0
	for _, fase := range []string{"— total ", "   " + FaseDoPlano, "   " + FaseDaSonda, "   " + FaseDaCorrida, "   " + FaseDaPreparacao} {
		i := strings.Index(relogio, fase)
		if i < ultimo {
			t.Fatalf("a fase %q falta ou esta fora de ordem no relogio:\n%s", fase, relogio)
		}
		ultimo = i
	}
	if strings.Contains(relogio, "(0s)") || strings.Contains(relogio, FaseDoControlo) {
		t.Errorf("relogio com uma fase de duracao zero, ou com um controlo que nao correu:\n%s", relogio)
	}
	// A linha de assinar, exacta, no fim — e a de conferir. Sem caminho de chave nenhuma.
	for _, quer := range []string{"aos-issuer route-profile-sign --entry ", "gpt-4o.registo.sig", "aos-ensaio conferir --pacote ", "<FICHEIRO-DA-SEED-DOS-PERFIS>"} {
		if !strings.Contains(e.stdout, quer) {
			t.Errorf("a saida nao traz %q", quer)
		}
	}
	// Nem segredos nem texto em lado nenhum; e no que o comando ESCREVE (os ficheiros e o stdout)
	// nem o caminho do ficheiro de chaves nem o host da rota. (O stderr da corrida mostra o host e
	// o contador de proposito, antes de enviar: e do AOS-512, e nao muda.)
	verSemFugas(t, "qualificar", tudoOQueFoiEscrito(t, e, saida), append(proibidosDeTexto(t), sentinelaChaveKimi, sentinelaBaseKimi, sentinelaCaminhoDaBase, chaveMestraDeTeste))
	verSemFugas(t, "qualificar (ficheiros e stdout)", tudoOQueFoiEscrito(t, execucao{stdout: e.stdout}, saida), []string{p.chaves, filepath.Dir(p.chaves), "chaves.env", "api.kimi.com"})

	// ASSINA-SE POR FORA, com uma chave que só existe neste teste — o comando não assinou nada.
	chave := routeentrytest.NewKey(t)
	if err := os.WriteFile(filepath.Join(pacote, "gpt-4o.registo.sig"), []byte(routeentry.Sign(registo, chave.Priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	embebida, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	entradas, err := routeentry.LoadDir(routeentry.Config{Dir: pacote, TrustAnchor: chave.Anchor(), Board: routeentrytest.Board, Region: routeentrytest.Region, Allowlist: embebida})
	if err != nil || len(entradas) != 1 || entradas[0].Profile.Requested != "gpt-4o" || entradas[0].InitialSharePercent != 10 || entradas[0].ReportDigest != recalculado {
		t.Fatalf("o no nao carrega o pacote preparado: %v %+v", err, entradas)
	}
	c := executar(t, Ambiente{}, "conferir", "--pacote", pacote, "--ancora", chave.Anchor(), "--board", routeentrytest.Board, "--regiao", routeentrytest.Region)
	if c.codigo != SaidaOK || !strings.Contains(c.stdout, "ACEITE") || !strings.Contains(c.stdout, entradas[0].EntryDigest) || !strings.Contains(c.stdout, "SUBSTITUI") {
		t.Fatalf("conferir: codigo %d\n%s%s", c.codigo, c.stdout, c.stderr)
	}
}

// SEM VEREDICTO POSITIVO NÃO HÁ NADA PARA ASSINAR. `recusado`, `inconclusivo`, um ensaio sem modelo
// real e uma corrida interrompida a meio deixam o relatório e o resumo — e registo nenhum. O código
// de saída distingue os três veredictos.
func TestAOS522_SemVeredictoPositivo_NaoHaRegisto(t *testing.T) {
	codigos := map[string]int{}
	for nome, c := range map[string]struct {
		roteiro   Comportamento
		codigo    int
		veredicto string
	}{
		"recusado":     {ComportamentoTexto, SaidaModeloRecusado, VeredictoRecusado},
		"inconclusivo": {ComportamentoErro500, SaidaInconclusivo, VeredictoInconclusivo},
	} {
		p := aos522NovoPosto(t, c.roteiro)
		saida := filepath.Join(t.TempDir(), "saida")
		e := executar(t, p.amb, p.real(saida, aos522Perfil)...)
		if e.codigo != c.codigo {
			t.Fatalf("%s: codigo %d, quer %d\n%s", nome, e.codigo, c.codigo, e.stderr)
		}
		codigos[nome] = e.codigo
		aos522SemRegisto(t, saida)
		resumo, _ := os.ReadFile(filepath.Join(saida, NomeDoResumoDeUmaPagina))
		if !strings.Contains(string(resumo), TituloDoVeredicto+" "+strings.ToUpper(c.veredicto)) || !strings.Contains(string(resumo), TituloDeNadaParaAssinar+" — veredicto "+c.veredicto) ||
			strings.Contains(string(resumo), TituloDoQueSeAssina) || !strings.Contains(e.stderr, "NADA PARA ASSINAR — veredicto "+c.veredicto) || strings.Contains(e.stdout, "route-profile-sign") {
			t.Errorf("%s: o resumo e a saida tinham de dizer que nao ha nada para assinar, e porque:\n%s\n%s", nome, resumo, e.stderr)
		}
	}
	if codigos["recusado"] == codigos["inconclusivo"] || codigos["recusado"] == SaidaOK || codigos["inconclusivo"] == SaidaOK {
		t.Errorf("o codigo de saida nao distingue os tres veredictos: %v e %d", codigos, SaidaOK)
	}

	// UM ENSAIO (modo falso) que sai `qualificado`: não vale para produção, e não há registo.
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, Ambiente{}, "qualificar", "falso", "--perfil", aos516Ficheiro(t, aos519PerfilDeProducao), "--fatia", "10",
		"--board", routeentrytest.Board, "--regiao", routeentrytest.Region, "--saida", saida, "--roteiro", "cumpre")
	if e.codigo != SaidaQualificadoEmEnsaio {
		t.Fatalf("ensaio: codigo %d\n%s", e.codigo, e.stderr)
	}
	aos522SemRegisto(t, saida)
	if resumo, _ := os.ReadFile(filepath.Join(saida, NomeDoResumoDeUmaPagina)); !strings.HasPrefix(string(resumo), "ENSAIO SEM MODELO REAL") || !strings.Contains(string(resumo), "modo falso") || !strings.Contains(string(resumo), TituloDeNadaParaAssinar) {
		t.Errorf("o resumo de um ensaio tinha de abrir por dizer que nao vale, e que nao ha nada para assinar:\n%s", resumo)
	}
}

// lancadorQueInterrompe faz de proxy bom e cancela a corrida ao fim de um número de pedidos.
type lancadorQueInterrompe struct {
	t        *testing.T
	apos     int64
	cancelar context.CancelFunc
	pedidos  atomic.Int64
}

func (l *lancadorQueInterrompe) Lancar(_ context.Context, p PedidoDeProxy) (*ProxyVivo, error) {
	falso := NovoProviderFalso([]Comportamento{ComportamentoCumpre})
	falso.ExigirChave(sha256.Sum256([]byte(chaveMestraDeTeste)))
	falso.DeclararModeloServido(p.Prefixo + "/" + p.Modelo)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.pedidos.Add(1) == l.apos {
			l.cancelar()
		}
		falso.ServeHTTP(w, r)
	}))
	l.t.Cleanup(srv.Close)
	return NovoProxyVivo(srv.URL+"/v1", chaveMestraDeTeste, srv.Close), nil
}

// UMA CORRIDA INTERROMPIDA A MEIO NÃO DEIXA REGISTO — de um modelo que, até ali, cumpria tudo.
func TestAOS522_CorridaInterrompida_NaoDeixaRegisto(t *testing.T) {
	p := aos522NovoPosto(t)
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	lanc := &lancadorQueInterrompe{t: t, apos: 30, cancelar: cancelar}
	p.amb.Lancador = lanc
	saida := filepath.Join(t.TempDir(), "saida")
	var out, errb bytes.Buffer
	codigo := Executar(ctx, p.real(saida, aos522Perfil), &out, &errb, p.amb)
	if codigo != SaidaParouAMeio || lanc.pedidos.Load() < 30 || lanc.pedidos.Load() >= 97 {
		t.Fatalf("codigo %d, pedidos %d\n%s", codigo, lanc.pedidos.Load(), errb.String())
	}
	aos522SemRegisto(t, saida)
	if resumo, _ := os.ReadFile(filepath.Join(saida, NomeDoResumoDeUmaPagina)); !strings.Contains(string(resumo), "a corrida terminou "+TerminouInterrompid) {
		t.Errorf("o resumo nao diz que a corrida foi interrompida:\n%s", resumo)
	}
}

// O REGISTO QUE O NÓ RECUSARIA PÁRA ANTES DE ENVIAR, com a causa que o nó daria — a mesma palavra
// que routeentry.ParseEntry dá ao registo equivalente. Nada é enviado e nada é escrito.
func TestAOS522_ORegistoQueONoRecusariaParaAntesDeEnviar(t *testing.T) {
	devolve := `{"requested":"gpt-4o","expected_model":"openai/` + modeloKimiDeTeste + `","wire_class":"openai-chat-completions","capabilities":["tools"],"projection_version":"1.3.0","devolver":"obrigatorio"}`
	comPonto := strings.Replace(aos522Perfil, `"gpt-4o"`, `"rota.de.producao"`, 1)
	for nome, c := range map[string]struct {
		perfil string
		fatia  string
		causa  string
	}{
		"perfil que devolve estado": {devolve, "10", routeentry.CauseProfileReturnsState},
		"alias que nao serve":       {comPonto, "10", routeentry.CauseBadRouteName},
		"fatia fora do intervalo":   {aos522Perfil, "101", routeentry.CauseShareOutOfRange},
		"fatia negativa":            {aos522Perfil, "-1", routeentry.CauseShareOutOfRange},
		"perfil que nao se le":      {`{"requested":"gpt-4o","chave_a_mais":1}`, "10", routeentry.CauseBadProfile},
	} {
		p := aos522NovoPosto(t, ComportamentoCumpre)
		saida := filepath.Join(t.TempDir(), "saida")
		args := p.real(saida, c.perfil)
		args[9] = c.fatia
		e := executar(t, p.amb, args...)
		if e.codigo != SaidaPacoteRecusado || p.lanc.lancamentos != 0 || !strings.Contains(e.stderr, "causa="+c.causa+".") {
			t.Errorf("%s: codigo %d, lancamentos %d\n%s", nome, e.codigo, p.lanc.lancamentos, e.stderr)
		}
		if _, err := os.Stat(saida); err == nil {
			t.Errorf("%s: a recusa escreveu na pasta de saida", nome)
		}
	}
	// A MESMA PALAVRA QUE O NÓ: o registo equivalente, lido pela função do nó.
	for causa, perfil := range map[string]string{routeentry.CauseProfileReturnsState: devolve, routeentry.CauseBadRouteName: comPonto} {
		doNo := routeentrytest.Marshal(t, routeentrytest.EntryDoc(t, perfil, routeentrytest.Board, routeentrytest.Region, digestPorConhecer))
		if _, err := routeentry.ParseEntry(doNo); routeentry.CauseOf(err) != causa {
			t.Errorf("o no daria %q a este registo, e o comando disse %q", routeentry.CauseOf(err), causa)
		}
	}

	// BOARD, REGIÃO E FATIA NÃO TÊM VALOR POR OMISSÃO: em falta, o comando não arranca.
	for _, falta := range []string{"--board", "--regiao", "--fatia"} {
		p := aos522NovoPosto(t, ComportamentoCumpre)
		saida := filepath.Join(t.TempDir(), "saida")
		var args []string
		completos := p.real(saida, aos522Perfil)
		for i := 0; i < len(completos); i++ {
			if completos[i] == falta {
				i++
				continue
			}
			args = append(args, completos[i])
		}
		e := executar(t, p.amb, args...)
		if e.codigo != SaidaUso || p.lanc.lancamentos != 0 || !strings.Contains(e.stderr, falta) {
			t.Errorf("sem %s: codigo %d, lancamentos %d\n%s", falta, e.codigo, p.lanc.lancamentos, e.stderr)
		}
		if _, err := os.Stat(saida); err == nil {
			t.Errorf("sem %s: escreveu na pasta de saida", falta)
		}
	}
	// Um board só de espaços é um board em falta; e um maior do que o tecto não cabe no resumo.
	for _, board := range []string{"  ", strings.Repeat("b", TamanhoMaximoDoAmbiente+1)} {
		p := aos522NovoPosto(t, ComportamentoCumpre)
		args := p.real(filepath.Join(t.TempDir(), "saida"), aos522Perfil)
		args[11] = board
		if e := executar(t, p.amb, args...); e.codigo != SaidaUso || p.lanc.lancamentos != 0 {
			t.Errorf("board %q: codigo %d", board, e.codigo)
		}
	}
}

// A CONFERÊNCIA ANTES DA CÓPIA: o pacote assinado é conferido pela função do arranque do nó. Um
// pacote bom passa; e cada pacote que o nó recusaria é recusado aqui com a MESMA causa que
// routeentry.LoadDir lhe dá.
func TestAOS522_Conferir_RecusaComACausaDoNo(t *testing.T) {
	p := aos522NovoPosto(t, ComportamentoCumpre)
	saida := filepath.Join(t.TempDir(), "saida")
	if e := executar(t, p.amb, p.real(saida, aos522Perfil)...); e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	origem := filepath.Join(saida, PastaDoPacote)
	chave, outra := routeentrytest.NewKey(t), routeentrytest.NewKey(t)
	registo, _ := os.ReadFile(filepath.Join(origem, "gpt-4o.registo.json"))
	embebida, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	type caso struct {
		causa   string
		estraga func(pasta string)
		ancora  string
		board   string
	}
	escrever := func(caminho string, conteudo []byte) {
		if err := os.WriteFile(caminho, conteudo, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for nome, c := range map[string]caso{
		"pacote bom":               {"", func(string) {}, chave.Anchor(), routeentrytest.Board},
		"assinado com outra chave": {routeentry.CauseSignatureInvalid, func(string) {}, outra.Anchor(), routeentrytest.Board},
		"sem assinatura":           {routeentry.CauseSignatureMissing, func(d string) { _ = os.Remove(filepath.Join(d, "gpt-4o.registo.sig")) }, chave.Anchor(), routeentrytest.Board},
		"no de outro board":        {routeentry.CauseOtherEnvironment, func(string) {}, chave.Anchor(), "board-de-desenvolvimento"},
		"relatorio alterado": {routeentry.CauseReportInvalid, func(d string) {
			cru, _ := os.ReadFile(filepath.Join(d, "gpt-4o.relatorio.json"))
			escrever(filepath.Join(d, "gpt-4o.relatorio.json"), bytes.Replace(cru, []byte(`"veredicto": "qualificado"`), []byte(`"veredicto": "recusado"`), 1))
		}, chave.Anchor(), routeentrytest.Board},
		"registo alterado depois": {routeentry.CauseSignatureInvalid, func(d string) {
			escrever(filepath.Join(d, "gpt-4o.registo.json"), bytes.Replace(registo, []byte(`"fatia_inicial_percentagem": 10`), []byte(`"fatia_inicial_percentagem": 100`), 1))
		}, chave.Anchor(), routeentrytest.Board},
		"o resumo copiado para a pasta": {routeentry.CauseUnknownFile, func(d string) { escrever(filepath.Join(d, NomeDoResumoDeUmaPagina), []byte("x")) }, chave.Anchor(), routeentrytest.Board},
	} {
		pasta := t.TempDir()
		for _, f := range aos522Ficheiros(t, origem) {
			cru, _ := os.ReadFile(filepath.Join(origem, f))
			escrever(filepath.Join(pasta, f), cru)
		}
		escrever(filepath.Join(pasta, "gpt-4o.registo.sig"), []byte(routeentry.Sign(registo, chave.Priv)+"\n"))
		c.estraga(pasta)
		_, doNo := routeentry.LoadDir(routeentry.Config{Dir: pasta, TrustAnchor: c.ancora, ForeignAnchors: []string{allowlist.EmbeddedTrustAnchor()}, Board: c.board, Region: routeentrytest.Region, Allowlist: embebida})
		if routeentry.CauseOf(doNo) != c.causa {
			t.Fatalf("%s: o no da a causa %q; o teste esperava %q", nome, routeentry.CauseOf(doNo), c.causa)
		}
		e := executar(t, Ambiente{}, "conferir", "--pacote", pasta, "--ancora", c.ancora, "--board", c.board, "--regiao", routeentrytest.Region)
		if c.causa == "" {
			if e.codigo != SaidaOK {
				t.Errorf("%s: codigo %d\n%s", nome, e.codigo, e.stderr)
			}
			continue
		}
		if e.codigo != SaidaPacoteRecusado || !strings.Contains(e.stderr, "causa="+c.causa+" ") || strings.Contains(e.stdout, "ACEITE") {
			t.Errorf("%s: codigo %d; quer a causa do no (%s)\n%s%s", nome, e.codigo, c.causa, e.stdout, e.stderr)
		}
	}
	// A âncora da allowlist não serve de âncora dos perfis, e uma pasta vazia não é um pacote.
	if e := executar(t, Ambiente{}, "conferir", "--pacote", origem, "--ancora", allowlist.EmbeddedTrustAnchor(), "--board", routeentrytest.Board, "--regiao", routeentrytest.Region); e.codigo != SaidaPacoteRecusado || !strings.Contains(e.stderr, "causa="+routeentry.CauseSharedTrustAnchor) {
		t.Errorf("ancora partilhada: codigo %d\n%s", e.codigo, e.stderr)
	}
	if e := executar(t, Ambiente{}, "conferir", "--pacote", t.TempDir(), "--ancora", chave.Anchor(), "--board", routeentrytest.Board, "--regiao", routeentrytest.Region); e.codigo != SaidaPacoteRecusado {
		t.Errorf("pasta vazia: codigo %d", e.codigo)
	}
	// Sem board ou sem região não se confere nada.
	if e := executar(t, Ambiente{}, "conferir", "--pacote", origem, "--ancora", chave.Anchor(), "--board", routeentrytest.Board); e.codigo != SaidaUso {
		t.Errorf("conferir sem regiao: codigo %d", e.codigo)
	}
}

// O RESUMO CABE NUMA PÁGINA E TEM UMA ORDEM. No pior caso — todos os avisos, todas as razões, o
// controlo, e valores compridos — não passa de 60 linhas de 100 colunas; e diz, por esta ordem:
// veredicto, razões, amostra, controlos negativos, o que a corrida não prova, o gasto. Com
// `exigencia_nao_provada`, essa linha vem antes de TODAS as outras.
func TestAOS522_Resumo_UmaPaginaEAOrdem(t *testing.T) {
	_, r := aos518Qualificar(t, aos518PerfilObrigatorio, "--roteiro", "cumpre", "--silencioso")
	q := aos518Veredicto(t, r)
	gasto, um := int64(2_999_999), 1
	q.Avisos = []string{AvisoFactosAusentes, AvisoTectoUSDNaoImposto, AvisoExigenciaNaoProvada}
	q.Razoes = append(RazoesFirmesDoModelo(), RazoesPassageirasDoModelo()...)
	q.NaoProva = naoProvaDaQualificacao(ModoProxy, false)
	q.Orcamento.MicroUSDEstimado, q.Orcamento.QualificacoesDoPerfilHoje, q.Orcamento.QualificacoesDaRotaHoje = &gasto, &um, &um
	for i := range q.Limiares {
		q.Limiares[i].Violado = true
	}
	q.Rota.DigestDaImagemDoProxy = routeentry.ProductionProxyImageDigest()
	hora := relogioFixo()
	var fases []FaseMedida
	for _, nome := range []string{FaseDoPlano, FaseDaSonda, FaseDaCorrida, FaseDoControlo, FaseDaPreparacao} {
		fases = append(fases, FaseMedida{Nome: nome, Inicio: hora, Fim: hora.Add(7 * time.Minute)})
		hora = hora.Add(7 * time.Minute)
	}
	comprido := strings.Repeat("b", TamanhoMaximoDoAmbiente)
	resumo := ResumoDeUmaPagina(r, DadosDoResumo{
		Alias: strings.Repeat("a", 64), Board: comprido, Regiao: comprido, Fatia: 100, Substitui: true, ModeloEsperadoDeAntes: "openai/" + strings.Repeat("m", 90),
		Preparado: true, DigestDoRegisto: "sha256:" + strings.Repeat("f", 64), Fases: fases,
	})
	linhas := strings.Split(strings.TrimRight(resumo, "\n"), "\n")
	if len(linhas) > LinhasDoResumoDeUmaPagina || LinhasDoResumoDeUmaPagina != 60 || ColunasDoResumoDeUmaPagina != 100 {
		t.Errorf("o resumo do pior caso tem %d linhas; a pagina sao 60 de 100 colunas\n%s", len(linhas), resumo)
	}
	for _, l := range linhas {
		if n := len([]rune(l)); n > ColunasDoResumoDeUmaPagina {
			t.Errorf("linha com %d colunas: %q", n, l)
		}
	}
	// A ORDEM.
	ultimo := -1
	for _, titulo := range []string{"AVISO — EXIGENCIA_NAO_PROVADA", "AVISO — FACTOS_AUSENTES", "AVISO — TECTO_EM_DOLARES", "ENSAIO SEM MODELO REAL", TituloDoVeredicto, TituloDasRazoes, TituloDaAmostra,
		TituloDosControlos, TituloDoQueNaoProva, TituloDoGasto, TituloDoQueSeAssina, TituloDoRelogio} {
		i := strings.Index(resumo, titulo)
		if i <= ultimo {
			t.Fatalf("%q falta ou esta fora de ordem (em %d, depois de %d):\n%s", titulo, i, ultimo, resumo)
		}
		ultimo = i
	}
	if !strings.HasPrefix(resumo, "AVISO — EXIGENCIA_NAO_PROVADA") {
		t.Errorf("com exigencia_nao_provada, essa linha vem antes de todas as outras:\n%.200s", resumo)
	}
	// Cada coisa que a corrida não prova está na página, e todas as razões também.
	for _, n := range q.NaoProva {
		if !strings.Contains(strings.Join(strings.Fields(resumo), " "), strings.ReplaceAll(n, "_", " ")) {
			t.Errorf("o resumo nao diz o que a corrida nao prova: %q", n)
		}
	}
	for _, razao := range q.Razoes {
		if !strings.Contains(resumo, razao) {
			t.Errorf("o resumo nao traz a razao %q", razao)
		}
	}
	if !strings.Contains(resumo, "limiares violados: L1, L2, L3, L4, L5, L6") || !strings.Contains(resumo, "gasto ESTIMADO 3.0000 USD") {
		t.Errorf("faltam os limiares violados ou o gasto:\n%s", resumo)
	}
}

// O CONTROLO NEGATIVO DO RESUMO. Um relatório sem os controlos negativos feitos não dá um resumo que
// os mostre como passados: dá «CONTROLO NAO CORRIDO» — e o veredicto do AOS-518 nesse caso não é
// `qualificado`.
func TestAOS522_Resumo_ControloNaoCorridoNuncaSaiComoPassado(t *testing.T) {
	// O perfil declara devolução obrigatória e a corrida principal não deu `qualificado`: o
	// controlo não correu.
	_, r := aos518Qualificar(t, aos518PerfilObrigatorio, "--roteiro", "cumpre", "--silencioso")
	q := aos518Veredicto(t, r)
	if q.Controlo == nil || q.Controlo.Resultado != ControloNaoCorreu || q.Veredicto == VeredictoQualificado {
		t.Fatalf("a preparacao do teste mudou: controlo %+v, veredicto %s", q.Controlo, q.Veredicto)
	}
	linhaDe := func(r *Relatorio) string {
		resumo := ResumoDeUmaPagina(r, DadosDoResumo{Alias: "gpt-4o", Motivo: "teste"})
		i, j := strings.Index(resumo, TituloDosControlos), strings.Index(resumo, TituloDoQueNaoProva)
		if i < 0 || j < i {
			t.Fatalf("o resumo nao tem a seccao dos controlos:\n%s", resumo)
		}
		return resumo[i:j]
	}
	naoPassou := func(nome, linha string) {
		t.Helper()
		if !strings.Contains(linha, LinhaDoControloNaoCorrido) || strings.Contains(linha, "passou") || strings.Contains(linha, "falhou_como_devido") {
			t.Errorf("%s: a linha dos controlos tinha de dizer %q e nada que pareca um controlo feito: %q", nome, LinhaDoControloNaoCorrido, linha)
		}
	}
	naoPassou("controlo que nao correu", linhaDe(r))
	// O bloco do controlo EM FALTA num relatório cujo perfil o pedia.
	q.Controlo = nil
	naoPassou("controlo em falta", linhaDe(r))
	// Um resultado que este leitor não conhece.
	q.Controlo = &ControloSemDevolucao{Resultado: "um_resultado_novo"}
	naoPassou("resultado desconhecido", linhaDe(r))
	// E os três que correram dizem o que mostraram — só `falhou_como_devido` diz que o exige.
	for resultado, quer := range map[string]string{ControloFalhouComoDevido: "Mostra que o EXIGE", ControloPassou: "NAO exige o estado", ControloInconclusivo: "nem uma coisa nem outra"} {
		q.Controlo = &ControloSemDevolucao{Resultado: resultado}
		if linha := aos522NumaLinha(linhaDe(r)); !strings.Contains(linha, resultado) || !strings.Contains(linha, quer) || strings.Contains(linha, LinhaDoControloNaoCorrido) {
			t.Errorf("%s: %q", resultado, linha)
		}
	}
	// Sem devolução obrigatória não há controlo — e o resumo não o inventa.
	_, r = aos518Qualificar(t, aos518PerfilSimples, "--roteiro", "cumpre", "--silencioso")
	if linha := linhaDe(r); !strings.Contains(linha, "nao se aplica") || strings.Contains(linha, "passou") {
		t.Errorf("sem devolucao obrigatoria: %q", linha)
	}
}

// A CHAVE PRIVADA NÃO TEM POR ONDE ENTRAR. O conjunto de opções do `qualificar` e do `conferir` é
// fechado e é este; as variáveis de ambiente lidas são estas; e no modo real o comando recusa em CI
// antes de ler seja o que for.
func TestAOS522_AChavePrivadaNaoTemPorOndeEntrar(t *testing.T) {
	nomes := func(fs *flag.FlagSet) []string {
		var l []string
		fs.VisitAll(func(f *flag.Flag) { l = append(l, f.Name) })
		sort.Strings(l)
		return l
	}
	doQualificar := nomes(novasFlagsDoQualificar(&opcoesDoQualificar{}, io.Discard))
	if quer := []string{"binario-do-falso", "board", "chaves", "destino-fora-da-lista", "fatia", "fornecedor", "host-esperado", "modelo", "pausa", "perfil", "precos", "reconstruir-contador", "regiao", "roteiro", "saida", "so-plano"}; !reflect.DeepEqual(doQualificar, quer) {
		t.Errorf("opcoes do qualificar = %v", doQualificar)
	}
	doConferir := nomes(novasFlagsDoConferir(&opcoesDoConferir{}, io.Discard))
	if quer := []string{"allowlist-dir", "ancora", "ancora-da-allowlist", "board", "pacote", "regiao"}; !reflect.DeepEqual(doConferir, quer) {
		t.Errorf("opcoes do conferir = %v", doConferir)
	}
	// Nenhuma opção é de uma chave de assinar. (`--chaves` é o ficheiro das chaves do FORNECEDOR, e
	// as âncoras são chaves públicas.)
	for _, n := range append(doQualificar, doConferir...) {
		for _, proibido := range []string{"seed", "key", "assin", "privad", "sign"} {
			if strings.Contains(n, proibido) {
				t.Errorf("a opcao --%s parece uma via para a chave de assinar", n)
			}
		}
	}
	// As opções que seguem para a corrida existem todas, e nenhuma é do registo.
	for _, n := range passamParaACorrida {
		if !contem(doQualificar, n) || contem([]string{"perfil", "saida", "board", "regiao", "fatia"}, n) {
			t.Errorf("a opcao --%s segue para a corrida e nao devia, ou nao existe", n)
		}
	}

	// AS VARIÁVEIS DE AMBIENTE LIDAS numa qualificação inteira que prepara o pacote.
	p := aos522NovoPosto(t, ComportamentoCumpre)
	saida := filepath.Join(t.TempDir(), "saida")
	if e := executar(t, p.amb, p.real(saida, aos522Perfil)...); e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	var lidas []string
	for n := range p.lidas {
		lidas = append(lidas, n)
	}
	sort.Strings(lidas)
	if !reflect.DeepEqual(lidas, []string{"CI", "GITHUB_ACTIONS"}) {
		t.Errorf("variaveis de ambiente lidas = %v", lidas)
	}
	// E não ficou assinatura nenhuma: assinar é do dono.
	for _, n := range aos522Ficheiros(t, saida) {
		if strings.HasSuffix(n, routeentry.SuffixSignature) {
			t.Errorf("o comando escreveu uma assinatura: %s", n)
		}
	}

	// EM CI, no modo real: recusado antes de ler o perfil — que aqui nem existe.
	for _, v := range variaveisDeCI {
		v := v
		q := aos522NovoPosto(t, ComportamentoCumpre)
		q.amb.Getenv = func(k string) string {
			if k == v {
				return "true"
			}
			return ""
		}
		args := q.real(filepath.Join(t.TempDir(), "saida"), aos522Perfil)
		args[7] = filepath.Join(t.TempDir(), "nao-existe.json")
		if e := executar(t, q.amb, args...); e.codigo != SaidaEmCI || q.lanc.lancamentos != 0 {
			t.Errorf("%s definida: codigo %d, lancamentos %d\n%s", v, e.codigo, q.lanc.lancamentos, e.stderr)
		}
	}
}

// O ORÇAMENTO NÃO SE CONTORNA. O pior caso é declarado antes do primeiro pedido; a terceira
// qualificação do mesmo perfil no mesmo dia é recusada antes de enviar; e o comando não corre por
// cima de uma pasta de saída que já tem alguma coisa.
func TestAOS522_Orcamento_ATerceiraDoDiaERecusadaAntesDeEnviar(t *testing.T) {
	p := aos522NovoPosto(t, ComportamentoCumpre)
	raiz := t.TempDir()
	perfil := aos516Ficheiro(t, aos522Perfil)
	args := func(saida string, extra ...string) []string {
		a := p.real(saida, aos522Perfil, extra...)
		a[7] = perfil
		return a
	}
	// Só o plano: declara o pior caso, não envia, não escreve.
	plano := executar(t, p.amb, args(filepath.Join(raiz, "plano"), "--so-plano")...)
	if plano.codigo != SaidaSoPlano || p.lanc.lancamentos != 0 || !strings.Contains(plano.stderr, "pior caso declarado: 673 pedidos") {
		t.Fatalf("--so-plano: codigo %d, lancamentos %d\n%s", plano.codigo, p.lanc.lancamentos, plano.stderr)
	}
	if _, err := os.Stat(filepath.Join(raiz, "plano")); err == nil {
		t.Error("--so-plano escreveu na pasta de saida")
	}
	for i, quer := range []int{SaidaOK, SaidaOK, SaidaRecusada} {
		saida := filepath.Join(raiz, "saida-"+string(rune('1'+i)))
		antes := p.lanc.lancamentos
		e := executar(t, p.amb, args(saida)...)
		if e.codigo != quer {
			t.Fatalf("qualificacao %d: codigo %d, quer %d\n%s", i+1, e.codigo, quer, e.stderr)
		}
		// O pior caso sai ANTES do primeiro pedido: está na saída mesmo quando nada é enviado.
		if quer == SaidaOK && !strings.Contains(e.stderr, "pior caso declarado: 673 pedidos") {
			t.Errorf("qualificacao %d: o pior caso nao foi declarado", i+1)
		}
		if quer == SaidaRecusada {
			if p.lanc.lancamentos != antes || !strings.Contains(e.stderr, "qualificacoes_do_dia_esgotadas") {
				t.Errorf("a terceira tinha de ser recusada antes de enviar: lancamentos %d (eram %d)\n%s", p.lanc.lancamentos, antes, e.stderr)
			}
			if _, err := os.Stat(saida); err == nil {
				t.Error("a terceira qualificacao escreveu na pasta de saida")
			}
		}
	}
	// A PASTA DE SAÍDA TEM DE ESTAR VAZIA: não se corre por cima da corrida anterior.
	antes := p.lanc.lancamentos
	e := executar(t, p.amb, args(filepath.Join(raiz, "saida-1"))...)
	if e.codigo != SaidaRecusada || p.lanc.lancamentos != antes || !strings.Contains(e.stderr, "vazia ou por criar") {
		t.Errorf("pasta de saida com conteudo: codigo %d\n%s", e.codigo, e.stderr)
	}
}

// OS SUBCOMANDOS DE SEMPRE NÃO MUDAM: o texto de uso geral é o de antes, sem os dois novos, e os
// dois novos têm o seu.
func TestAOS522_Inerte_OUsoGeralEODeAntes(t *testing.T) {
	e := executar(t, Ambiente{})
	if e.codigo != SaidaUso || e.stderr != usoDoBanco || strings.Contains(usoDoBanco, "qualificar ") || strings.Contains(usoDoBanco, "conferir") {
		t.Errorf("o uso geral mudou: codigo %d", e.codigo)
	}
	for sub, uso := range map[string]string{"qualificar": usoDoQualificar, "conferir": usoDoConferir} {
		if e := executar(t, Ambiente{}, sub); e.codigo != SaidaUso || e.stderr != uso {
			t.Errorf("%s sem argumentos: codigo %d\n%s", sub, e.codigo, e.stderr)
		}
	}
	if e := executar(t, Ambiente{}, "qualificar", "bateria"); e.codigo != SaidaUso {
		t.Errorf("qualificar com um modo que nao existe: codigo %d", e.codigo)
	}
}
