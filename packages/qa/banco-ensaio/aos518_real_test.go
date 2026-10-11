package bancoensaio

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// AOS-518 — A QUALIFICAÇÃO NO MODO REAL, pela linha de comandos inteira, com um LANÇADOR FALSO no
// lugar do proxy: nenhum pedido sai da máquina. O que aqui se prende é o orçamento da decisão D7 —
// o pior caso contra o tecto do dia, as duas qualificações por perfil por dia, o tecto em dólares —
// e a conferência da imagem do proxy.

// lancadorDaQualificacao faz de proxy efémero: o provider falso atrás, a exigir a chave mestra e a
// DECLARAR o modelo servido como o proxy declara (`<adaptador>/<modelo>`).
type lancadorDaQualificacao struct {
	t       *testing.T
	roteiro []Comportamento

	lancamentos int
	falso       *ProviderFalso
}

func (l *lancadorDaQualificacao) Lancar(_ context.Context, p PedidoDeProxy) (*ProxyVivo, error) {
	l.lancamentos++
	l.falso = NovoProviderFalso(l.roteiro)
	l.falso.ExigirChave(sha256.Sum256([]byte(chaveMestraDeTeste)))
	l.falso.DeclararModeloServido(p.Prefixo + "/" + p.Modelo)
	srv := httptest.NewServer(l.falso)
	l.t.Cleanup(srv.Close)
	return NovoProxyVivo(srv.URL+"/v1", chaveMestraDeTeste, srv.Close), nil
}

func (l *lancadorDaQualificacao) pedidos() int {
	if l.falso == nil {
		return 0
	}
	return l.falso.Pedidos()
}

// aos518PerfilDoKimi é o perfil de teste da rota do Kimi: o que o proxy declara servir é `openai/<modelo>`.
func aos518PerfilDoKimi(extra string) string {
	return `{"requested":"rota-de-ensaio","expected_model":"openai/` + modeloKimiDeTeste + `","wire_class":"openai-chat-completions","capabilities":["tools"]` + extra + `}`
}

func aos518Relatorios(t *testing.T, pasta string) []string {
	t.Helper()
	achados, _ := filepath.Glob(filepath.Join(pasta, "*.json"))
	return achados
}

func aos518LerRelatorio(t *testing.T, caminho string) (*Relatorio, []byte) {
	t.Helper()
	cru, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	r := &Relatorio{}
	if err := json.Unmarshal(cru, r); err != nil {
		t.Fatal(err)
	}
	return r, cru
}

// O KIMI DE TESTE SAI QUALIFICADO, o relatório identifica a rota inteira — com a imagem do proxy —
// e o seu digest confere; e as qualificações do dia contam-se por perfil: a terceira é recusada
// ANTES de enviar, com causa própria.
func TestAOS518_Real_QualificaEContaAsQualificacoesDoDia(t *testing.T) {
	campos := chavesDeTeste()
	campos[CampoTectoPedidosKimi] = "5000"
	chaves := escreverChaves(t, campos)
	pasta := filepath.Dir(chaves)
	saida := filepath.Join(pasta, "relatorios")
	perfil := aos516Ficheiro(t, aos518PerfilDoKimi(""))
	lanc := &lancadorDaQualificacao{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	hora := relogioFixo()
	amb := Ambiente{Lancador: lanc, Relogio: func() time.Time { return hora }}
	args := []string{"real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "qualificacao", "--perfil", perfil, "--saida", saida, "--silencioso"}

	// O plano: valida tudo, declara o pior caso, e NAO conta como qualificacao.
	e := executar(t, amb, append(args, "--so-plano")...)
	if e.codigo != SaidaOK || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "pior caso declarado: 673 pedidos") || !strings.Contains(e.stderr, "ja foi qualificado 0 vez(es) hoje, de um maximo de 2") {
		t.Fatalf("--so-plano: codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}
	if _, err := os.Stat(filepath.Join(pasta, NomeDoRegistoDeQualificacoes)); err == nil {
		t.Fatalf("--so-plano escreveu o registo das qualificacoes")
	}

	// A primeira.
	e = executar(t, amb, args...)
	if e.codigo != SaidaOK {
		t.Fatalf("primeira qualificacao: codigo %d\n%s", e.codigo, e.stderr)
	}
	achados := aos518Relatorios(t, saida)
	if len(achados) != 1 {
		t.Fatalf("relatorios: %v", achados)
	}
	r, cru := aos518LerRelatorio(t, achados[0])
	q := aos518Veredicto(t, r)
	if r.Modo != ModoReal || q.Veredicto != VeredictoQualificado || q.Contagens.NosComToolsCumpridos != 40 || r.Sonda == nil || r.Sonda.Resultado != SondaOK {
		t.Fatalf("modo %s, %s %v, cumpridos %d, sonda %+v", r.Modo, q.Veredicto, q.Razoes, q.Contagens.NosComToolsCumpridos, r.Sonda)
	}
	// A rota qualificada: modelo, fornecedor, perfil E a imagem do proxy, pelo digest.
	if q.Rota.Fornecedor != "kimi" || q.Rota.Modelo != modeloKimiDeTeste || q.Rota.ModeloEsperado != "openai/"+modeloKimiDeTeste ||
		q.Rota.ImagemDoProxy != ImagemDoProxyDeProducao || q.Rota.DigestDaImagemDoProxy != "sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63" {
		t.Errorf("rota qualificada = %+v", q.Rota)
	}
	// O orcamento: 672 + a sonda; 97 enviados (96 + a sonda); a primeira de duas.
	o := q.Orcamento
	if o.PiorCasoDeclarado != 673 || o.PedidosEnviados != 97 || lanc.pedidos() != 97 || o.QualificacoesDoPerfilHoje == nil || *o.QualificacoesDoPerfilHoje != 1 || o.MicroUSDEstimado != nil {
		t.Errorf("orcamento = %+v (qualificacoes hoje %v), o falso recebeu %d", o, o.QualificacoesDoPerfilHoje, lanc.pedidos())
	}
	if !contem(q.NaoProva, NaoProvaProxyDeProducao) || contem(q.NaoProva, NaoProvaModeloReal) {
		t.Errorf("nao_prova no modo real = %v", q.NaoProva)
	}
	if d, err := VerificarDigestDoRelatorio(cru); err != nil || d != r.DigestDoRelatorio {
		t.Errorf("o digest do relatorio do modo real nao confere: %v", err)
	}
	// Nem segredos, nem texto, nem o host da rota nos ficheiros escritos.
	tudo := tudoOQueFoiEscrito(t, e, pasta)
	verSemFugas(t, "qualificacao real", tudo, append(proibidosDeTexto(t), sentinelaChaveKimi, sentinelaBaseKimi, sentinelaCaminhoDaBase, chaveMestraDeTeste))
	if strings.Contains(string(cru), "api.kimi.com") {
		t.Errorf("o host da rota foi para o relatorio")
	}

	// A segunda passa; a terceira e recusada antes de levantar o proxy.
	hora = hora.Add(time.Minute)
	if e = executar(t, amb, args...); e.codigo != SaidaOK {
		t.Fatalf("segunda qualificacao: codigo %d\n%s", e.codigo, e.stderr)
	}
	lancamentos := lanc.lancamentos
	hora = hora.Add(time.Minute)
	e = executar(t, amb, args...)
	if e.codigo != SaidaRecusada || lanc.lancamentos != lancamentos || !strings.Contains(e.stderr, "qualificacoes_do_dia_esgotadas") {
		t.Fatalf("terceira qualificacao: codigo %d, lancamentos %d (eram %d)\n%s", e.codigo, lanc.lancamentos, lancamentos, e.stderr)
	}
	if n := len(aos518Relatorios(t, saida)); n != 2 {
		t.Errorf("a terceira qualificacao nao pode escrever relatorio: ha %d", n)
	}
	// O plano tambem a recusa.
	if e = executar(t, amb, append(args, "--so-plano")...); e.codigo != SaidaRecusada {
		t.Errorf("--so-plano depois de duas: codigo %d", e.codigo)
	}

	// OUTRO perfil (outro digest) ainda pode: a conta e por perfil.
	hora = hora.Add(time.Minute)
	outro := append([]string(nil), args...)
	outro[8] = aos516Ficheiro(t, aos518PerfilDoKimi(`,"params":{"max_tokens":16000}`))
	if e = executar(t, amb, outro...); e.codigo != SaidaOK {
		t.Fatalf("outro perfil: codigo %d\n%s", e.codigo, e.stderr)
	}
	// E no dia seguinte o mesmo perfil volta a poder.
	hora = hora.Add(24 * time.Hour)
	if e = executar(t, amb, args...); e.codigo != SaidaOK {
		t.Fatalf("no dia seguinte: codigo %d\n%s", e.codigo, e.stderr)
	}
	recente := aos518Relatorios(t, saida)
	r, _ = aos518LerRelatorio(t, recente[len(recente)-1])
	if n := aos518Veredicto(t, r).Orcamento.QualificacoesDoPerfilHoje; n == nil || *n != 1 {
		t.Errorf("no dia seguinte a contagem recomeca: %v", n)
	}

	// Um registo que nao se le recusa — nao recomeca do zero.
	if err := os.WriteFile(filepath.Join(pasta, NomeDoRegistoDeQualificacoes), []byte("{nao e json"), 0o600); err != nil {
		t.Fatal(err)
	}
	lancamentos = lanc.lancamentos
	if e = executar(t, amb, args...); e.codigo != SaidaRecusada || lanc.lancamentos != lancamentos {
		t.Errorf("registo ilegivel: codigo %d, lancamentos %d (eram %d)", e.codigo, lanc.lancamentos, lancamentos)
	}
}

// O registo das qualificacoes, sozinho.
func TestAOS518_RegistoDeQualificacoes(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), NomeDoRegistoDeQualificacoes)
	for i := 1; i <= QualificacoesPorPerfilPorDia; i++ {
		if n, err := RegistarQualificacao(caminho, "2026-10-11", "sha256:aa"); err != nil || n != i {
			t.Fatalf("registo %d: %d, %v", i, n, err)
		}
	}
	if n, err := RegistarQualificacao(caminho, "2026-10-11", "sha256:aa"); err != ErrQualificacoesDoDia || n != QualificacoesPorPerfilPorDia {
		t.Errorf("a terceira tinha de ser recusada: %d, %v", n, err)
	}
	if n, _ := QualificacoesDeHoje(caminho, "2026-10-11", "sha256:aa"); n != QualificacoesPorPerfilPorDia {
		t.Errorf("a recusa nao pode contar: %d", n)
	}
	if n, err := RegistarQualificacao(caminho, "2026-10-11", "sha256:bb"); err != nil || n != 1 {
		t.Errorf("outro perfil: %d, %v", n, err)
	}
	if n, err := RegistarQualificacao(caminho, "2026-10-12", "sha256:aa"); err != nil || n != 1 {
		t.Errorf("outro dia: %d, %v", n, err)
	}
	// So fica o dia de hoje: o registo nao cresce.
	if n, _ := QualificacoesDeHoje(caminho, "2026-10-11", "sha256:aa"); n != 0 {
		t.Errorf("o dia anterior tinha de ter saido do registo: %d", n)
	}
	for _, mau := range []string{"", "[]", `{"dias":null}`, `{"dias":{"2026-10-12":{"sha256:aa":-5}}}`, `{"dias":{},"outra":1}`} {
		if err := os.WriteFile(caminho, []byte(mau), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := QualificacoesDeHoje(caminho, "2026-10-12", "sha256:aa"); err != ErrRegistoDeQualificacoes {
			t.Errorf("registo %q: %v, quer recusa", mau, err)
		}
		if _, err := RegistarQualificacao(caminho, "2026-10-12", "sha256:aa"); err != ErrRegistoDeQualificacoes {
			t.Errorf("registo %q: o registo ilegivel foi reescrito (%v)", mau, err)
		}
	}
}

// O PIOR CASO CONTRA O TECTO DO DIA: com um tecto de 672 a qualificacao (673 com a sonda) nao
// cabe — relatorio `inconclusivo`, nenhum proxy levantado, nenhum pedido, nenhuma qualificacao contada.
func TestAOS518_Real_PiorCasoNaoCabeNoTectoDoDia(t *testing.T) {
	campos := chavesDeTeste()
	campos[CampoTectoPedidosKimi] = "672"
	chaves := escreverChaves(t, campos)
	pasta := filepath.Dir(chaves)
	saida := filepath.Join(pasta, "relatorios")
	lanc := &lancadorDaQualificacao{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "qualificacao",
		"--perfil", aos516Ficheiro(t, aos518PerfilDoKimi("")), "--saida", saida)
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 {
		t.Fatalf("codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}
	achados := aos518Relatorios(t, saida)
	if len(achados) != 1 {
		t.Fatalf("a recusa por tecto tinha de deixar relatorio: %v", achados)
	}
	r, cru := aos518LerRelatorio(t, achados[0])
	q := aos518Veredicto(t, r)
	if r.Terminou != TerminouNaoCoube || q.Veredicto != VeredictoInconclusivo || !reflect.DeepEqual(q.Razoes, []string{RazaoPiorCasoNaoCabe, RazaoAmostraIncompleta}) {
		t.Errorf("terminou %s, %s %v", r.Terminou, q.Veredicto, q.Razoes)
	}
	if r.Pedidos.Enviados != 0 || *r.Pedidos.GastosHoje != 0 || q.Orcamento.PiorCasoDeclarado != 673 || q.Orcamento.QualificacoesDoPerfilHoje != nil {
		t.Errorf("pedidos %+v, orcamento %+v", r.Pedidos, q.Orcamento)
	}
	if _, err := VerificarDigestDoRelatorio(cru); err != nil {
		t.Errorf("o relatorio de recusa tambem traz o seu digest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pasta, NomeDoRegistoDeQualificacoes)); err == nil {
		t.Errorf("uma qualificacao que nao enviou nada foi contada")
	}
	// Com 673 ja cabe.
	campos[CampoTectoPedidosKimi] = "673"
	chaves = escreverChaves(t, campos)
	if e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "qualificacao",
		"--perfil", aos516Ficheiro(t, aos518PerfilDoKimi("")), "--saida", filepath.Join(filepath.Dir(chaves), "relatorios"), "--silencioso"); e.codigo != SaidaOK {
		t.Errorf("com o tecto igual ao pior caso: codigo %d\n%s", e.codigo, e.stderr)
	}
}

// A IMAGEM DO PROXY: a qualificacao recusa arrancar, antes de enviar, se a imagem em uso nao for a
// declarada para producao — com causa propria.
func TestAOS518_Real_ImagemDoProxyQueNaoEADeProducao(t *testing.T) {
	if ImagemDoProxy != ImagemDoProxyDeProducao {
		t.Fatalf("a imagem do proxy do banco (%s) ja nao e a declarada para producao (%s): nenhuma qualificacao arranca", ImagemDoProxy, ImagemDoProxyDeProducao)
	}
	if !regexp.MustCompile(`^ghcr\.io/berriai/litellm@sha256:[0-9a-f]{64}$`).MatchString(ImagemDoProxyDeProducao) {
		t.Fatalf("a imagem declarada para producao nao esta fixada pelo digest: %s", ImagemDoProxyDeProducao)
	}
	for nome, c := range map[string]struct {
		emUso string
		ok    bool
	}{
		"a de producao":    {ImagemDoProxyDeProducao, true},
		"outro digest":     {"ghcr.io/berriai/litellm@sha256:" + strings.Repeat("0", 64), false},
		"uma tag":          {"ghcr.io/berriai/litellm:main-stable", false},
		"vazia":            {"", false},
		"so o repositorio": {"ghcr.io/berriai/litellm", false},
	} {
		if err := conferirImagemDoProxy(c.emUso, ImagemDoProxyDeProducao); (err == nil) != c.ok {
			t.Errorf("%s: %v", nome, err)
		}
	}
	if conferirImagemDoProxy("litellm:main-stable", "litellm:main-stable") == nil {
		t.Errorf("uma imagem declarada por tag nao e uma imagem fixada: tinha de recusar")
	}

	chaves := escreverChaves(t, chavesDeTeste())
	saida := filepath.Join(filepath.Dir(chaves), "relatorios")
	perfil := aos516Ficheiro(t, aos518PerfilDoKimi(""))
	for _, modo := range [][]string{
		{"real", "--chaves", chaves, "--fornecedor", "kimi"},
		{"proxy", "--binario-do-falso", "nao-chega-a-ser-usado"},
	} {
		lanc := &lancadorDaQualificacao{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
		amb := Ambiente{Lancador: lanc, imagemDoProxyDeTeste: "ghcr.io/berriai/litellm@sha256:" + strings.Repeat("0", 64)}
		e := executar(t, amb, append(modo, "--experiencia", "qualificacao", "--perfil", perfil, "--saida", saida)...)
		if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "imagem_do_proxy_nao_e_a_de_producao") {
			t.Errorf("%s: codigo %d, lancamentos %d\n%s", modo[0], e.codigo, lanc.lancamentos, e.stderr)
		}
		if achados := aos518Relatorios(t, saida); len(achados) != 0 {
			t.Errorf("%s: a recusa pela imagem escreveu %v", modo[0], achados)
		}
		// As outras experiencias nao conferem a imagem: nao sao qualificacoes.
		if modo[0] == "real" {
			if e = executar(t, amb, append(modo, "--so-plano")...); e.codigo != SaidaOK {
				t.Errorf("a bateria simples nao depende da imagem declarada: codigo %d\n%s", e.codigo, e.stderr)
			}
		}
	}
	// A biblioteca recusa o mesmo, mesmo que alguem monte a corrida sem a linha de comandos.
	amb := aos518Ambiente(t, aos518PerfilSimples, aos518FalsoBom(), nil, 10)
	amb.cfg.Modo = ModoProxy
	if _, err := Correr(context.Background(), amb.cfg); err != ErrImagemDoProxy {
		t.Errorf("uma qualificacao no modo proxy sem a imagem de producao: %v", err)
	}
}

// A imagem declarada no banco e a que o compose de producao fixa. Enquanto o compose usar uma tag
// (antes do AOS-517), nao ha digest com que comparar e o teste di-lo — nao passa em silencio.
func TestAOS518_ImagemDoProxyEADoComposeDeProducao(t *testing.T) {
	compose := filepath.Join(raizDosPacotes(t), "..", "deploy", "server", "docker-compose.prod.yml")
	cru, err := os.ReadFile(compose)
	if err != nil {
		t.Fatalf("nao foi possivel ler o compose de producao: %v", err)
	}
	imagens := regexp.MustCompile(`(?m)^\s*image:\s*(ghcr\.io/berriai/litellm\S*)\s*$`).FindAllStringSubmatch(string(cru), -1)
	if len(imagens) != 1 {
		t.Fatalf("o compose de producao tem %d servicos com a imagem do proxy; quer 1", len(imagens))
	}
	imagem := imagens[0][1]
	if !strings.Contains(imagem, "@sha256:") {
		t.Skipf("SALTADO: o compose de producao ainda usa uma tag (%s) e nao um digest — e o AOS-517 que o fixa. "+
			"POR VERIFICAR nesta execucao: que a imagem declarada no banco e a do compose. O digest do banco (%s) foi lido no servidor a 2026-10-11.", imagem, digestDaImagem(ImagemDoProxyDeProducao))
	}
	if digestDaImagem(imagem) != digestDaImagem(ImagemDoProxyDeProducao) {
		t.Errorf("o compose de producao fixa %s e o banco declara %s: uma qualificacao feita com o banco nao vale para producao", imagem, ImagemDoProxyDeProducao)
	}
}

// O expected_model do perfil tem de ser a rota da corrida, e o modo real continua a recusar em CI.
func TestAOS518_Real_RecusasAntesDeEnviar(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	saida := filepath.Join(filepath.Dir(chaves), "relatorios")
	lanc := &lancadorDaQualificacao{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "qualificacao",
		"--perfil", aos516Ficheiro(t, aos518PerfilSimples), "--saida", saida)
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "expected_model do perfil nao e a rota desta corrida") {
		t.Errorf("perfil de outra rota: codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}
	for _, v := range []string{"CI", "GITHUB_ACTIONS"} {
		v := v
		e = executar(t, Ambiente{Lancador: lanc, Getenv: func(k string) string {
			if k == v {
				return "true"
			}
			return ""
		}}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--experiencia", "qualificacao", "--perfil", aos516Ficheiro(t, aos518PerfilDoKimi("")), "--saida", saida)
		if e.codigo != SaidaEmCI || lanc.lancamentos != 0 {
			t.Errorf("%s definida: codigo %d, lancamentos %d", v, e.codigo, lanc.lancamentos)
		}
	}
}

// A SONDA QUE FALHA PELA CONTA: `inconclusivo` — o veredicto nao e sobre o modelo —, um so pedido,
// e NAO gasta uma das duas qualificacoes do dia.
func TestAOS518_Real_SondaSemSaldoEInconclusivoENaoContaComoQualificacao(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)
	saida := filepath.Join(pasta, "relatorios")
	f := novoFornecedorDeTeste(repetir(semSaldo, 300))
	e := executar(t, Ambiente{Lancador: &lancadorDeTeste{t: t, devolver: f}}, "real", "--chaves", chaves, "--fornecedor", "kimi",
		"--experiencia", "qualificacao", "--perfil", aos516Ficheiro(t, aos518PerfilDoKimi("")), "--saida", saida, "--silencioso")
	if e.codigo != SaidaParouAMeio || f.recebidos() != 1 {
		t.Fatalf("codigo %d (quer %d), pedidos %d (quer 1)\n%s", e.codigo, SaidaParouAMeio, f.recebidos(), e.stderr)
	}
	r, _ := aos518LerRelatorio(t, aos518Relatorios(t, saida)[0])
	q := aos518Veredicto(t, r)
	if r.Terminou != TerminouSaldoInsuficiente || q.Veredicto != VeredictoInconclusivo || !reflect.DeepEqual(q.Razoes, []string{RazaoSaldoInsuficiente, RazaoAmostraIncompleta}) {
		t.Errorf("terminou %s, %s %v", r.Terminou, q.Veredicto, q.Razoes)
	}
	if q.Orcamento.QualificacoesDoPerfilHoje != nil {
		t.Errorf("uma sonda falhada contou como qualificacao")
	}
	if _, err := os.Stat(filepath.Join(pasta, NomeDoRegistoDeQualificacoes)); err == nil {
		t.Errorf("uma sonda falhada escreveu o registo das qualificacoes")
	}
	verSemFugas(t, "sonda sem saldo", tudoOQueFoiEscrito(t, e, pasta), []string{sentinelaDaConta, "suspended", "recharge", sentinelaChaveKimi, chaveMestraDeTeste})
}

// O TECTO EM DOLARES DA QUALIFICACAO (3 USD estimados): com um preco absurdo a corrida para ao fim
// de poucos pedidos, e o veredicto e `inconclusivo`. So actua quando o fornecedor tem tecto em
// dolares no ficheiro de chaves — e o que faz o contador estimar o gasto.
func TestAOS518_Real_TectoEmDolaresDaQualificacao(t *testing.T) {
	campos := chavesDeTeste()
	campos[CampoTectoUSDAnthropic] = "500"
	chaves := escreverChaves(t, campos)
	pasta := filepath.Dir(chaves)
	precos := filepath.Join(pasta, "precos.json")
	// 1000 USD por milhao de tokens de entrada: cada pedido da bateria custa perto de 1 USD.
	if err := os.WriteFile(precos, []byte(`{"modelos":{"`+modeloAnthropicDeTeste+`":{"entrada_micro_usd_por_mtok":1000000000,"saida_micro_usd_por_mtok":1000000000}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	perfil := `{"requested":"rota-de-ensaio","expected_model":"anthropic/` + modeloAnthropicDeTeste + `","wire_class":"openai-chat-completions","capabilities":["tools"]}`
	lanc := &lancadorDaQualificacao{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	saida := filepath.Join(pasta, "relatorios")
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", precos,
		"--experiencia", "qualificacao", "--perfil", aos516Ficheiro(t, perfil), "--saida", saida, "--silencioso")
	if e.codigo != SaidaParouAMeio {
		t.Fatalf("codigo %d, quer %d\n%s", e.codigo, SaidaParouAMeio, e.stderr)
	}
	r, _ := aos518LerRelatorio(t, aos518Relatorios(t, saida)[0])
	q := aos518Veredicto(t, r)
	if r.Terminou != TerminouTectoDaQualificacao || q.Veredicto != VeredictoInconclusivo || q.Razoes[0] != RazaoTectoDaQualificacao {
		t.Fatalf("terminou %s, %s %v", r.Terminou, q.Veredicto, q.Razoes)
	}
	o := q.Orcamento
	if o.MicroUSDEstimado == nil || *o.MicroUSDEstimado < TectoMicroUSDDaQualificacao || o.TectoMicroUSD != TectoMicroUSDDaQualificacao {
		t.Fatalf("orcamento = %+v", o)
	}
	// Parou LOGO: bem antes do tecto de pedidos, e sem gastar mais do que um run alem do tecto.
	if lanc.pedidos() >= 40 || *o.MicroUSDEstimado > 4*TectoMicroUSDDaQualificacao {
		t.Errorf("a corrida fez %d pedidos e gastou %d micro-USD estimados antes de parar", lanc.pedidos(), *o.MicroUSDEstimado)
	}
	if !strings.Contains(ResumoEmTexto(r), "gasto ESTIMADO") {
		t.Errorf("o resumo nao mostra o gasto estimado da qualificacao")
	}
}
