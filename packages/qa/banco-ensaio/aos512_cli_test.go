package bancoensaio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// AOS-512 — A LINHA DE COMANDOS: o ficheiro de chaves, as recusas do modo com modelo real, e a
// prova de que nem os segredos nem o texto das respostas chegam a saída nenhuma.

// As sentinelas do ficheiro de chaves de TESTE. Nenhuma é uma chave verdadeira.
const (
	sentinelaChaveKimi = "SENTINELA-CHAVE-KIMI-7f3a91c2d4e6"
	// A base é de um host da lista do fornecedor (o destino é validado); a sentinela vai no
	// CAMINHO, que não pode aparecer em saída nenhuma — o host, esse, é mostrado de propósito.
	sentinelaCaminhoDaBase  = "SENTINELA-CAMINHO-DA-BASE-KIMI-5c1d"
	sentinelaBaseKimi       = "https://api.kimi.com/" + sentinelaCaminhoDaBase + "/v1"
	sentinelaChaveAnthropic = "SENTINELA-CHAVE-ANTHROPIC-0b8e55a1c9f7"
	modeloKimiDeTeste       = "modelo-kimi-de-teste"
	segundoModeloKimi       = "SENTINELA-SEGUNDO-MODELO-KIMI"
	modeloAnthropicDeTeste  = "modelo-anthropic-de-teste"
	regiaoDeTeste           = "UE-DECLARADA-NO-TESTE"
)

// chavesDeTeste devolve os campos de um ficheiro de chaves de teste completo.
func chavesDeTeste() map[string]string {
	return map[string]string{
		CampoChaveKimi: sentinelaChaveKimi, CampoKimiAPIBase: sentinelaBaseKimi,
		CampoKimiModelos:    modeloKimiDeTeste + "," + segundoModeloKimi,
		CampoChaveAnthropic: sentinelaChaveAnthropic, CampoAnthropicModelo: modeloAnthropicDeTeste,
		CampoTectoPedidosKimi: "1000", CampoTectoPedidosAnthro: "1000", CampoTectoUSDAnthropic: "5",
		CampoAnthropicRegiaoProc: regiaoDeTeste,
	}
}

// escreverChaves escreve um ficheiro de chaves de teste e devolve o caminho.
func escreverChaves(t *testing.T, campos map[string]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("# ficheiro de chaves de TESTE (AOS-512): nenhuma chave e verdadeira\n\n")
	for _, campo := range []string{CampoChaveKimi, CampoKimiAPIBase, CampoKimiModelos, CampoChaveAnthropic, CampoAnthropicModelo,
		CampoTectoPedidosKimi, CampoTectoPedidosAnthro, CampoTectoUSDAnthropic, CampoAnthropicRegiaoProc} {
		if v, ok := campos[campo]; ok {
			b.WriteString(campo + "=" + v + "\n")
		}
	}
	caminho := filepath.Join(t.TempDir(), "chaves.env")
	if err := os.WriteFile(caminho, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return caminho
}

// lancadorDeTeste faz de proxy efémero sem Docker: um servidor local que exige a chave mestra
// e, por trás, o provider falso. Guarda o que lhe pediram.
type lancadorDeTeste struct {
	t        *testing.T
	roteiro  []Comportamento
	erro     error
	devolver http.Handler // se presente, responde em vez do provider falso

	lancamentos int
	pedido      PedidoDeProxy
	falso       *ProviderFalso
}

const chaveMestraDeTeste = "SENTINELA-CHAVE-MESTRA-DO-PROXY-4d2c"

func (l *lancadorDeTeste) Lancar(_ context.Context, p PedidoDeProxy) (*ProxyVivo, error) {
	l.lancamentos++
	l.pedido = p
	if l.erro != nil {
		return nil, l.erro
	}
	l.falso = NovoProviderFalso(l.roteiro)
	l.falso.ExigirChave(sha256.Sum256([]byte(chaveMestraDeTeste)))
	var h http.Handler = l.falso
	if l.devolver != nil {
		h = l.devolver
	}
	srv := httptest.NewServer(h)
	l.t.Cleanup(srv.Close)
	return NovoProxyVivo(srv.URL+"/v1", chaveMestraDeTeste, srv.Close), nil
}

// execucao é o resultado de uma execução da linha de comandos.
type execucao struct {
	codigo         int
	stdout, stderr string
	saida          string // a pasta dos relatórios
}

// executar corre a linha de comandos com as saídas capturadas e o relógio fixo.
func executar(t *testing.T, amb Ambiente, args ...string) execucao {
	t.Helper()
	var out, errb bytes.Buffer
	if amb.Relogio == nil {
		amb.Relogio = relogioFixo
	}
	e := execucao{}
	for i, a := range args {
		if a == "--saida" && i+1 < len(args) {
			e.saida = args[i+1]
		}
	}
	e.codigo = Executar(context.Background(), args, &out, &errb, amb)
	e.stdout, e.stderr = out.String(), errb.String()
	return e
}

// tudoOQueFoiEscrito junta as saídas e o nome e o conteúdo de todos os ficheiros das pastas.
func tudoOQueFoiEscrito(t *testing.T, e execucao, pastas ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(e.stdout + "\n" + e.stderr + "\n")
	ficheiros := 0
	for _, pasta := range pastas {
		err := filepath.WalkDir(pasta, func(caminho string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			b.WriteString(caminho + "\n")
			if d.IsDir() || d.Name() == "chaves.env" {
				// O ficheiro de chaves é a entrada: o seu conteúdo não é uma saída do banco.
				return nil
			}
			cru, rerr := os.ReadFile(caminho)
			if rerr != nil {
				return rerr
			}
			ficheiros++
			b.Write(cru)
			b.WriteString("\n")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if ficheiros == 0 && len(pastas) > 0 {
		t.Fatal("o teste de fuga nao leu nenhum ficheiro: nao esta a varrer o que o banco escreveu")
	}
	return b.String()
}

// proibidosDeTexto são os textos de pedidos e de respostas que nunca podem chegar a uma saída:
// a sentinela do provider falso, os textos dos casos de wire, os factos dos documentos, o
// system e um objectivo da bateria.
func proibidosDeTexto(t *testing.T) []string {
	t.Helper()
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	p := append([]string{SentinelaDeTexto, b.System, "ZIMBRO-7741", "Read the document", "DOCUMENTO SINTETICO"}, SentinelasDosCasosDeWire()...)
	for _, c := range b.Casos {
		for _, n := range c.Nos {
			p = append(p, n.Factos...)
			p = append(p, n.Objectivo)
		}
	}
	return p
}

func verSemFugas(t *testing.T, onde, tudo string, proibidos []string) {
	t.Helper()
	for _, p := range proibidos {
		if p != "" && strings.Contains(tudo, p) {
			t.Errorf("%s: FUGA — aparece %q", onde, p)
		}
	}
}

// O MODO COM MODELO REAL, de ponta a ponta, contra um servidor falso que faz de fornecedor: com
// sentinelas em TODOS os campos do ficheiro de chaves e no texto de TODAS as respostas, nada
// disso aparece em stdout, em stderr, no relatório, no resumo, no contador nem em nomes de
// ficheiro.
func TestAOS512_Real_SemSegredosNemTextoEmSaidaNenhuma(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)
	saida := filepath.Join(pasta, "relatorios")
	lanc := &lancadorDeTeste{t: t, roteiro: RoteiroPorOmissao()}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--amostras", "2", "--saida", saida)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo = %d\nstderr: %s", e.codigo, e.stderr)
	}
	// O lançador recebeu a chave e a base do ficheiro — e o modelo escolhido foi o primeiro.
	if lanc.lancamentos != 1 || lanc.pedido.apiKey != sentinelaChaveKimi || lanc.pedido.apiBase != sentinelaBaseKimi ||
		lanc.pedido.Prefixo != "openai" || lanc.pedido.Modelo != modeloKimiDeTeste || lanc.pedido.BinarioDoFalso != "" {
		t.Fatalf("o lancador nao recebeu a rota do ficheiro de chaves")
	}
	if lanc.falso.Pedidos() != 27 {
		t.Fatalf("o fornecedor falso recebeu %d pedidos, quer 27 (a bateria, duas passagens)", lanc.falso.Pedidos())
	}
	tudo := tudoOQueFoiEscrito(t, e, pasta)
	segredos := []string{sentinelaChaveKimi, sentinelaBaseKimi, sentinelaCaminhoDaBase, sentinelaChaveAnthropic, chaveMestraDeTeste,
		segundoModeloKimi, modeloAnthropicDeTeste, regiaoDeTeste, "Bearer ", "Authorization"}
	verSemFugas(t, "modo real", tudo, segredos)
	verSemFugas(t, "modo real", tudo, proibidosDeTexto(t))
	// O pedido de proxy e as chaves não se deixam imprimir, nem por engano.
	c, err := LerChaves(chaves)
	if err != nil {
		t.Fatal(err)
	}
	rota, err := c.Rota(FornecedorKimi, "", Destino{})
	if err != nil {
		t.Fatal(err)
	}
	impresso := fmt.Sprintf("%v|%+v|%#v|%s|%v|%+v|%#v|%v|%+v|%#v", c, c, c, c, rota, rota, rota, lanc.pedido, lanc.pedido, lanc.pedido)
	if cru, jerr := json.Marshal(c); jerr == nil {
		impresso += string(cru)
	}
	if cru, jerr := json.Marshal(rota); jerr == nil {
		impresso += string(cru)
	}
	if cru, jerr := json.Marshal(lanc.pedido); jerr == nil {
		impresso += string(cru)
	}
	verSemFugas(t, "formatacao das chaves", impresso, []string{sentinelaChaveKimi, sentinelaBaseKimi, sentinelaChaveAnthropic})

	// O relatório: o que TEM de lá estar.
	var r Relatorio
	entradas, _ := filepath.Glob(filepath.Join(saida, "ensaio-real-bateria-*.json"))
	if len(entradas) != 1 {
		t.Fatalf("relatorios escritos: %v", entradas)
	}
	cru, _ := os.ReadFile(entradas[0])
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	if r.Modo != ModoReal || r.Rota.Fornecedor != "kimi" || r.Rota.Modelo != modeloKimiDeTeste || r.DataUTC != "2026-10-08T12:00:00Z" {
		t.Errorf("cabecalho do relatorio: %+v", r.Rota)
	}
	if r.Rota.Digest != DigestDaRota("kimi", modeloKimiDeTeste, sentinelaBaseKimi) || r.Digests.Rota != r.Rota.Digest {
		t.Errorf("o digest da rota tem de cobrir o fornecedor, o modelo e o endereco")
	}
	if r.Pedidos.Enviados != 27 || r.Pedidos.TectoDoDia == nil || *r.Pedidos.TectoDoDia != 1000 || *r.Pedidos.GastosHoje != 27 || *r.Pedidos.RestantesHoje != 973 {
		t.Errorf("pedidos no relatorio: %+v", r.Pedidos)
	}
	if r.RegiaoDeclarada != nil {
		t.Errorf("a regiao declarada e da Anthropic: nao vai no relatorio do Kimi")
	}
	if len(r.Limites) == 0 || r.Taxas.N != 16 {
		t.Errorf("o relatorio tem de levar os limites e as 16 observacoes")
	}
	// O contador ficou ao lado do ficheiro de chaves, fora do repositório.
	if dias := lerContador(t, filepath.Join(pasta, "contador.json")); dias[diaDosTestes]["kimi"].Pedidos != 27 {
		t.Errorf("contador: %v", dias)
	}
	// Nomes de ficheiro: só o modo, a experiência e a data.
	nome := regexp.MustCompile(`^ensaio-real-bateria-\d{8}T\d{6}Z\.(json|txt)$`)
	todos, _ := os.ReadDir(saida)
	for _, f := range todos {
		if !nome.MatchString(f.Name()) {
			t.Errorf("nome de ficheiro inesperado: %s", f.Name())
		}
	}
}

// O CAMINHO DE ERRO DO PROXY. O proxy devolve os segredos no corpo de um erro, e o lançador
// falha com os segredos na mensagem: nada disso chega a uma saída.
func TestAOS512_Real_SegredosNoCaminhoDeErroDoProxy(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)
	segredos := []string{sentinelaChaveKimi, sentinelaBaseKimi, sentinelaChaveAnthropic, chaveMestraDeTeste}

	t.Run("o proxy devolve os segredos num erro HTTP", func(t *testing.T) {
		eco := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"AuthenticationError: key %s at %s rejected; header %s; %s"}}`,
				sentinelaChaveKimi, sentinelaBaseKimi, r.Header.Get("Authorization"), SentinelaDeTexto)
		})
		saida := filepath.Join(pasta, "relatorios-erro")
		pedidos := 0
		contado := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pedidos++
			eco.ServeHTTP(w, r)
		})
		e := executar(t, Ambiente{Lancador: &lancadorDeTeste{t: t, devolver: contado}}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--saida", saida)
		// CHAVE RECUSADA: os três primeiros pedidos levam 401 e a corrida aborta, com exit
		// próprio, em vez de gastar o tecto do dia com uma chave que não serve.
		if e.codigo != SaidaParouAMeio || pedidos != RecusasDeChaveQueAbortam {
			t.Fatalf("chave recusada: codigo %d (quer %d), pedidos %d (quer %d)\n%s", e.codigo, SaidaParouAMeio, pedidos, RecusasDeChaveQueAbortam, e.stderr)
		}
		if !strings.Contains(e.stderr, TerminouChaveRecusada) {
			t.Errorf("a causa da paragem tinha de ser dita: %q", e.stderr)
		}
		tudo := tudoOQueFoiEscrito(t, e, pasta)
		verSemFugas(t, "erro HTTP do proxy", tudo, append(segredos, SentinelaDeTexto, "AuthenticationError"))
		if !strings.Contains(tudo, `"401"`) || !strings.Contains(tudo, DesfechoErroHTTP) {
			t.Errorf("o relatorio tinha de contar os 401 em vocabulario fechado")
		}
	})

	t.Run("o lancador falha com os segredos na mensagem", func(t *testing.T) {
		lanc := &lancadorDeTeste{t: t, erro: fmt.Errorf("%w: docker run -e %s=%s falhou; base %s; mestra %s",
			ErrProxy, envChaveDaRota, sentinelaChaveKimi, sentinelaBaseKimi, chaveMestraDeTeste)}
		e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--saida", filepath.Join(pasta, "relatorios-lancador"))
		if e.codigo != SaidaSemInfra {
			t.Fatalf("codigo = %d, quer %d", e.codigo, SaidaSemInfra)
		}
		// A chave mestra do lançador de teste só é conhecida depois de um lançamento com
		// sucesso; a chave e a base do ficheiro são ocultadas sempre.
		verSemFugas(t, "erro do lancador", e.stdout+e.stderr, []string{sentinelaChaveKimi, sentinelaBaseKimi})
		if !strings.Contains(e.stderr, "[oculto]") {
			t.Errorf("a mensagem do lancador tinha de sair com os segredos ocultados: %q", e.stderr)
		}
	})
}

// O modo com modelo real NUNCA corre em CI: recusa antes de ler seja o que for.
func TestAOS512_Real_RecusaEmCI(t *testing.T) {
	for _, variavel := range []string{"CI", "GITHUB_ACTIONS"} {
		lanc := &lancadorDeTeste{t: t}
		amb := Ambiente{Lancador: lanc, Getenv: func(n string) string {
			if n == variavel {
				return "true"
			}
			return ""
		}}
		// O ficheiro de chaves nem existe: a recusa vem antes de o procurar.
		e := executar(t, amb, "real", "--chaves", filepath.Join(t.TempDir(), "nao-existe.env"), "--fornecedor", "kimi")
		if e.codigo != SaidaEmCI || lanc.lancamentos != 0 {
			t.Errorf("%s definida: codigo %d (quer %d), lancamentos %d", variavel, e.codigo, SaidaEmCI, lanc.lancamentos)
		}
		if !strings.Contains(e.stderr, variavel) {
			t.Errorf("a recusa tinha de nomear a variavel %s: %q", variavel, e.stderr)
		}
		// Os outros modos correm em CI: é lá que vivem.
		if e := executar(t, amb, "falso", "--saida", t.TempDir(), "--silencioso"); e.codigo != SaidaOK {
			t.Errorf("o modo falso tem de correr em CI: codigo %d\n%s", e.codigo, e.stderr)
		}
	}
}

// Nenhum script de `scripts/ci` chama o modo com modelo real.
func TestAOS512_Real_NenhumScriptDeCIOChama(t *testing.T) {
	pasta := filepath.Join(raizDosPacotes(t), "..", "scripts", "ci")
	chamada := regexp.MustCompile(`aos-ensaio["']?\s+real\b|\bensaio\b.*\s--chaves\b|` + EnvDasChaves)
	lidos := 0
	err := filepath.WalkDir(pasta, func(caminho string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		cru, rerr := os.ReadFile(caminho)
		if rerr != nil {
			return rerr
		}
		lidos++
		for i, linha := range strings.Split(string(cru), "\n") {
			if strings.HasPrefix(strings.TrimSpace(linha), "#") {
				continue
			}
			if chamada.MatchString(linha) {
				t.Errorf("%s:%d chama o modo com modelo real: %s", filepath.Base(caminho), i+1, strings.TrimSpace(linha))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if lidos < 20 {
		t.Fatalf("so foram lidos %d ficheiros de scripts/ci", lidos)
	}
}

// Sem ficheiro, com um campo em falta, com o marcador do exemplo ou sem tecto, o modo real
// recusa com exit próprio e uma mensagem que nomeia o CAMPO e nunca o valor. Nada é lançado.
func TestAOS512_Real_RecusasDoFicheiroDeChaves(t *testing.T) {
	sem := func(campo string) map[string]string {
		c := chavesDeTeste()
		delete(c, campo)
		return c
	}
	com := func(campo, valor string) map[string]string {
		c := chavesDeTeste()
		c[campo] = valor
		return c
	}
	const valorMau = "SENTINELA-VALOR-MAU-91ab"
	for _, caso := range []struct {
		nome       string
		campos     map[string]string
		fornecedor string
		extra      []string
		nomeia     string
	}{
		{"chave do Kimi em falta", sem(CampoChaveKimi), "kimi", nil, CampoChaveKimi},
		{"chave do Kimi vazia", com(CampoChaveKimi, ""), "kimi", nil, CampoChaveKimi},
		{"chave do Kimi com o marcador do exemplo", com(CampoChaveKimi, "<cole aqui a chave do Kimi>"), "kimi", nil, CampoChaveKimi},
		{"base do Kimi em falta", sem(CampoKimiAPIBase), "kimi", nil, CampoKimiAPIBase},
		{"modelos do Kimi em falta", sem(CampoKimiModelos), "kimi", nil, CampoKimiModelos},
		{"modelo com caracteres recusados", com(CampoKimiModelos, valorMau+" com espacos\""), "kimi", nil, CampoKimiModelos},
		{"modelo pedido fora da lista", chavesDeTeste(), "kimi", []string{"--modelo", "outro-modelo"}, CampoKimiModelos},
		{"tecto do Kimi em falta", sem(CampoTectoPedidosKimi), "kimi", nil, CampoTectoPedidosKimi},
		{"tecto do Kimi a zero", com(CampoTectoPedidosKimi, "0"), "kimi", nil, CampoTectoPedidosKimi},
		{"tecto do Kimi negativo", com(CampoTectoPedidosKimi, "-5"), "kimi", nil, CampoTectoPedidosKimi},
		{"tecto do Kimi ilegivel", com(CampoTectoPedidosKimi, valorMau), "kimi", nil, CampoTectoPedidosKimi},
		{"modelo da Anthropic com o marcador do exemplo", com(CampoAnthropicModelo, "<por definir>"), "anthropic", nil, CampoAnthropicModelo},
		{"chave da Anthropic em falta", sem(CampoChaveAnthropic), "anthropic", nil, CampoChaveAnthropic},
		{"tecto de pedidos da Anthropic em falta", sem(CampoTectoPedidosAnthro), "anthropic", nil, CampoTectoPedidosAnthro},
		{"tecto em dolares ilegivel", com(CampoTectoUSDAnthropic, valorMau), "anthropic", nil, CampoTectoUSDAnthropic},
		{"tecto em dolares a zero", com(CampoTectoUSDAnthropic, "0"), "anthropic", nil, CampoTectoUSDAnthropic},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			lanc := &lancadorDeTeste{t: t}
			args := append([]string{"real", "--chaves", escreverChaves(t, caso.campos), "--fornecedor", caso.fornecedor, "--saida", t.TempDir()}, caso.extra...)
			e := executar(t, Ambiente{Lancador: lanc}, args...)
			if e.codigo != SaidaRecusada || lanc.lancamentos != 0 {
				t.Fatalf("codigo %d (quer %d), lancamentos %d\n%s", e.codigo, SaidaRecusada, lanc.lancamentos, e.stderr)
			}
			if !strings.Contains(e.stderr, caso.nomeia) {
				t.Errorf("a mensagem tinha de nomear o campo %s: %q", caso.nomeia, e.stderr)
			}
			verSemFugas(t, caso.nome, e.stdout+e.stderr, []string{sentinelaChaveKimi, sentinelaBaseKimi, sentinelaChaveAnthropic, valorMau, "cole aqui", "por definir"})
		})
	}

	t.Run("sem caminho para o ficheiro", func(t *testing.T) {
		lanc := &lancadorDeTeste{t: t}
		e := executar(t, Ambiente{Lancador: lanc}, "real", "--fornecedor", "kimi", "--saida", t.TempDir())
		if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, EnvDasChaves) {
			t.Fatalf("codigo %d, lancamentos %d: %q", e.codigo, lanc.lancamentos, e.stderr)
		}
	})
	t.Run("ficheiro que nao existe", func(t *testing.T) {
		lanc := &lancadorDeTeste{t: t}
		e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", filepath.Join(t.TempDir(), "nao-existe.env"), "--fornecedor", "kimi", "--saida", t.TempDir())
		if e.codigo != SaidaRecusada || lanc.lancamentos != 0 {
			t.Fatalf("codigo %d, lancamentos %d", e.codigo, lanc.lancamentos)
		}
	})
	t.Run("linha sem a forma CAMPO=valor nao e ecoada", func(t *testing.T) {
		caminho := escreverChaves(t, chavesDeTeste())
		cru, _ := os.ReadFile(caminho)
		if err := os.WriteFile(caminho, append(cru, []byte("\n"+valorMau+"-colado-sem-nome-de-campo\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		e := executar(t, Ambiente{Lancador: &lancadorDeTeste{t: t}}, "real", "--chaves", caminho, "--fornecedor", "kimi", "--saida", t.TempDir())
		if e.codigo != SaidaRecusada || strings.Contains(e.stderr, valorMau) || !strings.Contains(e.stderr, "linha") {
			t.Fatalf("codigo %d: %q", e.codigo, e.stderr)
		}
	})
	t.Run("o caminho pode vir pela variavel de ambiente", func(t *testing.T) {
		caminho := escreverChaves(t, chavesDeTeste())
		lanc := &lancadorDeTeste{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
		amb := Ambiente{Lancador: lanc, Getenv: func(n string) string {
			if n == EnvDasChaves {
				return caminho
			}
			return ""
		}}
		if e := executar(t, amb, "real", "--fornecedor", "kimi", "--silencioso"); e.codigo != SaidaOK || lanc.lancamentos != 1 {
			t.Fatalf("codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
		}
		// Sem --saida, os relatórios ficam ao lado do ficheiro de chaves.
		if achados, _ := filepath.Glob(filepath.Join(filepath.Dir(caminho), "relatorios", "ensaio-real-*.json")); len(achados) != 1 {
			t.Errorf("o relatorio tinha de ficar na pasta do ficheiro de chaves: %v", achados)
		}
	})
}

// A Anthropic: tecto em dólares exige preço declarado; a região é copiada como declaração.
func TestAOS512_Real_AnthropicPrecoERegiao(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)

	lanc := &lancadorDeTeste{t: t}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--saida", filepath.Join(pasta, "r0"))
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "preco") {
		t.Fatalf("tecto em dolares sem preco: codigo %d, lancamentos %d: %q", e.codigo, lanc.lancamentos, e.stderr)
	}
	semOModelo := filepath.Join(pasta, "precos-sem-o-modelo.json")
	if err := os.WriteFile(semOModelo, []byte(`{"modelos":{"outro":{"entrada_micro_usd_por_mtok":1,"saida_micro_usd_por_mtok":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", semOModelo, "--saida", filepath.Join(pasta, "r0")); e.codigo != SaidaRecusada || lanc.lancamentos != 0 {
		t.Fatalf("tabela de precos sem o modelo: codigo %d, lancamentos %d", e.codigo, lanc.lancamentos)
	}

	precos := filepath.Join(pasta, "precos.json")
	if err := os.WriteFile(precos, []byte(`{"modelos":{"`+modeloAnthropicDeTeste+`":{"entrada_micro_usd_por_mtok":3000000,"saida_micro_usd_por_mtok":15000000}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lanc = &lancadorDeTeste{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	saida := filepath.Join(pasta, "r1")
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", precos, "--saida", saida)
	if e.codigo != SaidaOK || lanc.pedido.Prefixo != "anthropic" || lanc.pedido.apiKey != sentinelaChaveAnthropic || lanc.pedido.apiBase != "" {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
	cru, _ := os.ReadFile(achados[0])
	var r Relatorio
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	if r.RegiaoDeclarada == nil || r.RegiaoDeclarada.Valor != regiaoDeTeste || !strings.Contains(r.RegiaoDeclarada.Nota, "sem efeito") {
		t.Errorf("a regiao declarada tinha de ir para o relatorio como declaracao: %+v", r.RegiaoDeclarada)
	}
	if !r.Custo.Estimativa || r.Custo.MicroUSDHoje == nil || *r.Custo.MicroUSDHoje <= 0 || *r.Custo.TectoMicroUSD != 5_000_000 || !strings.Contains(r.Custo.Nota, "nao e a factura") {
		t.Errorf("o custo tinha de sair como estimativa, com o tecto de 5 USD: %+v", r.Custo)
	}
	if !strings.Contains(e.stdout, "ESTIMADO") {
		t.Errorf("o resumo tinha de dizer que o gasto e estimado")
	}
	// A chave do OUTRO fornecedor não entra na corrida nem em saída nenhuma.
	verSemFugas(t, "anthropic", tudoOQueFoiEscrito(t, e, saida), []string{sentinelaChaveKimi, sentinelaBaseKimi, sentinelaChaveAnthropic})

	// --so-plano: valida tudo, diz o que faria e não lança nem envia nada.
	lanc = &lancadorDeTeste{t: t}
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", precos, "--experiencia", "separadores", "--so-plano")
	if e.codigo != SaidaOK || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "212") {
		t.Fatalf("--so-plano: codigo %d, lancamentos %d: %q", e.codigo, lanc.lancamentos, e.stderr)
	}
}

// O tecto atingido a meio, pela linha de comandos: exit próprio e relatório parcial escrito.
func TestAOS512_Real_TectoAtingidoSaiComCodigoProprio(t *testing.T) {
	campos := chavesDeTeste()
	campos[CampoTectoUSDAnthropic] = "0.000001" // um micro-USD: a primeira resposta esgota-o
	chaves := escreverChaves(t, campos)
	pasta := filepath.Dir(chaves)
	precos := filepath.Join(pasta, "precos.json")
	if err := os.WriteFile(precos, []byte(`{"modelos":{"`+modeloAnthropicDeTeste+`":{"entrada_micro_usd_por_mtok":3000000,"saida_micro_usd_por_mtok":15000000}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lanc := &lancadorDeTeste{t: t, roteiro: []Comportamento{ComportamentoCumpre}}
	saida := filepath.Join(pasta, "relatorios")
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", precos, "--saida", saida)
	if e.codigo != SaidaParouAMeio {
		t.Fatalf("codigo = %d, quer %d\n%s", e.codigo, SaidaParouAMeio, e.stderr)
	}
	if lanc.falso.Pedidos() != 1 {
		t.Fatalf("sairam %d pedidos, quer 1", lanc.falso.Pedidos())
	}
	achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
	if len(achados) != 1 {
		t.Fatalf("o relatorio parcial tinha de ser escrito: %v", achados)
	}
	cru, _ := os.ReadFile(achados[0])
	if !strings.Contains(string(cru), `"terminou": "tecto_atingido"`) {
		t.Errorf("o relatorio parcial tinha de dizer a causa")
	}
}

// O modo falso pela linha de comandos: determinista, e o tecto ensaia-se com --tecto-pedidos.
func TestAOS512_Falso_LinhaDeComandos(t *testing.T) {
	correr := func() string {
		saida := t.TempDir()
		e := executar(t, Ambiente{}, "falso", "--amostras", "2", "--saida", saida)
		if e.codigo != SaidaOK {
			t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
		}
		verSemFugas(t, "modo falso", tudoOQueFoiEscrito(t, e, saida), proibidosDeTexto(t))
		achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
		cru, _ := os.ReadFile(achados[0])
		return string(cru)
	}
	if a, b := correr(), correr(); a != b {
		t.Error("duas corridas do modo falso com o mesmo relogio deram relatorios diferentes")
	}
	// Sem pasta do dono e sem --saida não há onde escrever: recusa em vez de escrever no repo.
	if e := executar(t, Ambiente{}, "falso"); e.codigo != SaidaUso {
		t.Errorf("sem --saida e sem pasta do dono: codigo %d, quer %d", e.codigo, SaidaUso)
	}
	// As opções do modo real não se misturam com os outros modos, e vice-versa.
	if e := executar(t, Ambiente{}, "falso", "--chaves", "x", "--saida", t.TempDir()); e.codigo != SaidaUso {
		t.Errorf("--chaves no modo falso: codigo %d", e.codigo)
	}
	chaves := escreverChaves(t, chavesDeTeste())
	if e := executar(t, Ambiente{Lancador: &lancadorDeTeste{t: t}}, "real", "--chaves", chaves, "--fornecedor", "kimi", "--tecto-pedidos", "999999"); e.codigo != SaidaUso {
		t.Errorf("--tecto-pedidos no modo real (o tecto so vem do ficheiro): codigo %d", e.codigo)
	}
	// Tecto de 3 pela linha de comandos: a bateria precisa de mais, e é recusada sem um pedido.
	contador := filepath.Join(t.TempDir(), "contador.json")
	e := executar(t, Ambiente{}, "falso", "--tecto-pedidos", "3", "--contador", contador, "--saida", t.TempDir())
	if e.codigo != SaidaRecusada || !strings.Contains(e.stderr, "nao cabe") {
		t.Errorf("tecto de 3 para a bateria: codigo %d: %q", e.codigo, e.stderr)
	}
	if e := executar(t, Ambiente{}, "falso", "--tecto-pedidos", "0", "--contador", contador, "--saida", t.TempDir()); e.codigo != SaidaRecusada {
		t.Errorf("tecto zero: codigo %d, quer %d", e.codigo, SaidaRecusada)
	}
}

func TestAOS512_Redactor(t *testing.T) {
	r := &Redactor{}
	r.Acrescentar("segredo-comprido", "abc", "")
	var b bytes.Buffer
	fmt.Fprintf(r.Escritor(&b), "antes segredo-comprido depois abc")
	if got := b.String(); got != "antes [oculto] depois abc" {
		t.Errorf("redactor: %q", got)
	}
	var nulo *Redactor
	nulo.Acrescentar("x")
	if nulo.Texto("segredo-comprido") != "segredo-comprido" {
		t.Error("um redactor nil nao altera o texto")
	}
}

// Os segredos chegam ao contentor do proxy pelo AMBIENTE do processo docker: não estão nos
// argumentos do `docker run` nem na configuração do proxy.
func TestAOS512_Proxy_SegredosSoNoAmbiente(t *testing.T) {
	args, ambiente := arranqueDoContentorDoProxy("contentor", "rede", chaveMestraDeTeste, sentinelaChaveKimi, sentinelaBaseKimi, 90*time.Minute, 75*time.Second)
	linha := strings.Join(args, " ")
	verSemFugas(t, "argumentos do docker run", linha, []string{chaveMestraDeTeste, sentinelaChaveKimi, sentinelaBaseKimi, sentinelaCaminhoDaBase})
	// O contentor apaga-se sozinho quando pára (`--rm`), e o guião recebe, em segundos, o prazo
	// de vida e a tolerância sem sinal de vida — e é ele que vigia.
	if !strings.Contains(linha, "run -d --rm --name contentor ") || !strings.HasSuffix(linha, " 5400 75") {
		t.Errorf("o docker run do proxy tinha de levar --rm, o prazo de vida e a tolerancia: %q", linha[:60]+" ... "+linha[len(linha)-20:])
	}
	for _, quer := range []string{"sys.argv[1]", "/tmp/vivo", "agora - inicio > vida", "agora - visto > tolerancia", "p.kill()"} {
		if !strings.Contains(arranqueDoProxy, quer) {
			t.Errorf("o guiao do contentor do proxy perdeu o vigia (%q)", quer)
		}
	}
	for _, nome := range []string{envChaveMestra, envChaveDaRota, envBaseDaRota} {
		if !strings.Contains(linha, "-e "+nome+" ") {
			t.Errorf("os argumentos tinham de passar a variavel %s pelo nome", nome)
		}
		if strings.Contains(linha, nome+"=") {
			t.Errorf("os argumentos levam um valor para %s", nome)
		}
	}
	if quer := []string{envChaveMestra + "=" + chaveMestraDeTeste, envChaveDaRota + "=" + sentinelaChaveKimi, envBaseDaRota + "=" + sentinelaBaseKimi}; strings.Join(ambiente, "|") != strings.Join(quer, "|") {
		t.Errorf("o ambiente do processo docker nao leva os tres segredos")
	}
	// Sem base (a rota da Anthropic), a variável da base não é passada.
	args, ambiente = arranqueDoContentorDoProxy("contentor", "rede", chaveMestraDeTeste, sentinelaChaveAnthropic, "", time.Hour, time.Minute)
	if strings.Contains(strings.Join(args, " "), envBaseDaRota) || len(ambiente) != 2 {
		t.Errorf("sem base da API, a variavel da base nao pode ser passada")
	}

	cfg, err := configDoProxy("openai", modeloKimiDeTeste, true)
	if err != nil {
		t.Fatal(err)
	}
	verSemFugas(t, "configuracao do proxy", cfg, []string{chaveMestraDeTeste, sentinelaChaveKimi, sentinelaBaseKimi})
	for _, quer := range []string{"model_name: " + AliasDaRota, "model: openai/" + modeloKimiDeTeste, "api_key: os.environ/" + envChaveDaRota,
		"api_base: os.environ/" + envBaseDaRota, "litellm_settings:\n  drop_params: true\n  telemetry: false\n  num_retries: 0\n",
		"router_settings:\n  num_retries: 0\n  disable_cooldowns: true\n"} {
		if !strings.Contains(cfg, quer) {
			t.Errorf("a configuracao do proxy nao tem %q", quer)
		}
	}
	if cfg, _ := configDoProxy("anthropic", modeloAnthropicDeTeste, false); strings.Contains(cfg, "api_base") || !strings.Contains(cfg, "model: anthropic/"+modeloAnthropicDeTeste) {
		t.Errorf("a rota da Anthropic nao leva base e usa o adaptador anthropic")
	}
	// Um nome de modelo que escapasse da linha do YAML é recusado.
	for _, mau := range []string{"m\n  api_key: x", "m odelo", "m\"x", ""} {
		if _, err := configDoProxy("openai", mau, true); !errors.Is(err, ErrProxy) {
			t.Errorf("nome de modelo %q: err = %v, quer ErrProxy", mau, err)
		}
	}
	if _, err := configDoProxy("outro", "m", true); !errors.Is(err, ErrProxy) {
		t.Errorf("adaptador desconhecido: err = %v, quer ErrProxy", err)
	}
}
