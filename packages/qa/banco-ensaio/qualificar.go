package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// QUALIFICAR E PREPARAR A ASSINATURA NUM COMANDO (AOS-522, fase A3).
//
// `aos-ensaio qualificar <modo>` encadeia os passos 2 a 4 do caminho de um modelo novo: corre a
// experiência `qualificacao` do AOS-518, escreve o resumo de uma página e — SÓ com o veredicto
// `qualificado` de uma corrida completa do modo real — deixa pronto, numa pasta à parte, o
// registo de entrada do AOS-519 e o relatório que ele cita. E PÁRA AÍ.
//
// QUEM ASSINA É O DONO. Este ficheiro não tem argumento, variável de ambiente nem leitura de
// ficheiro por onde uma chave de assinar possa entrar: a assinatura é um passo separado, com o
// `aos-issuer route-profile-sign`, cuja linha exacta sai no fim.
//
// UMA SÓ FUNÇÃO DE CONFERÊNCIA. O que aqui se prepara é conferido pelas funções do pacote
// `routeentry` — as mesmas que o nó corre no arranque —, e não por uma cópia: [routeentry.ParseEntry]
// e [routeentry.CheckReport] antes de haver assinatura, [routeentry.LoadDir] depois (`conferir`).
// A recusa sai com a mesma palavra que o nó daria.
//
// SÓ A CORRIDA QUE ACABOU DE FAZER. Não há argumento para um relatório vindo de fora nem para um
// veredicto: o relatório é o que a corrida deste processo escreveu ([corridaFeita]), e a pasta de
// saída é criada por esta corrida — se já existir, o comando não corre.
//
// AS BARREIRAS ENTRE A CORRIDA E O PACOTE são independentes, e cada uma tem o seu teste:
//
//  1. o código de saída da corrida E o relatório em memória dizem os dois `qualificado` do modo
//     real, com a corrida completa ([veredictoPositivo]);
//  2. o relatório RELIDO DO DISCO é o que a corrida escreveu (o mesmo digest) — [ErrRelatorioTrocado];
//  3. o registo e o relatório passam nas conferências do nó ([conferirPorAssinar]);
//  4. o que ficou ESCRITO no pacote é, byte a byte, o que se conferiu, e volta a passar nas
//     conferências do nó a partir do disco — [ErrPacoteAlterado].

// SaidaPacoteRecusado — o registo de entrada, ou o pacote já assinado, seria recusado pelo nó no
// arranque. A causa sai no vocabulário fechado do `routeentry`. No `qualificar` a recusa do
// registo vem, em regra, ANTES de qualquer pedido (o plano); pode vir DEPOIS da corrida, se o
// relatório que ela escreveu não passar nas conferências do nó. No `conferir`, antes da cópia.
const SaidaPacoteRecusado = 11

const (
	// TamanhoMaximoDoAmbiente é o tecto, em bytes, do board e da região que o `qualificar` aceita.
	// O nó não tem este tecto: é do resumo, que mostra os dois e tem de caber numa página.
	TamanhoMaximoDoAmbiente = 64
	// NomeDoResumoDeUmaPagina é o ficheiro do resumo que o dono lê antes de assinar.
	NomeDoResumoDeUmaPagina = "resumo-de-uma-pagina.txt"
	// PastaDoPacote é a subpasta, dentro da pasta de saída, com o que há para assinar: o registo e
	// o relatório, e mais nada — o nó recusa uma pasta com um ficheiro que não conhece.
	PastaDoPacote = "pacote"
)

var (
	// ErrRelatorioTrocado — o relatório que está no disco, entre a corrida e a preparação, não é o
	// que a corrida escreveu: o seu digest não é o do relatório em memória.
	ErrRelatorioTrocado = errors.New("o relatorio no disco nao e o que a corrida escreveu (causa: relatorio_trocado_depois_da_corrida); nao ficou pacote nenhum")
	// ErrPacoteAlterado — o que ficou escrito no pacote não é o que se conferiu.
	ErrPacoteAlterado = errors.New("o pacote escrito nao e o que foi conferido (causa: pacote_alterado_depois_de_escrito); o pacote foi retirado")
)

const usoDoQualificar = `aos-ensaio qualificar — qualifica uma rota e prepara o registo de entrada para o dono assinar (AOS-522)

  aos-ensaio qualificar real  --chaves F --fornecedor kimi|anthropic|openrouter --perfil F --fatia N
                              --board B --regiao R --saida PASTA [opcoes]
  aos-ensaio qualificar falso --perfil F --fatia N --board B --regiao R --saida PASTA [--roteiro ...]
  aos-ensaio qualificar proxy --binario-do-falso F --perfil F --fatia N --board B --regiao R --saida PASTA

  --perfil FICHEIRO   o perfil candidato; o seu "requested" e o alias que o no pede em producao
  --fatia N           a fatia inicial do canary, de 0 a 100 (so algarismos), que fica no registo assinado
  --board B           o board do no a que o registo se destina   (sem valor por omissao)
  --regiao R          a regiao desse no                          (sem valor por omissao)
                      board e regiao: de 1 a 64 letras, digitos, _ e -
  --saida PASTA       pasta que AINDA NAO EXISTE: e criada por esta corrida, e leva o relatorio, o
                      resumo e, so com veredicto positivo, pacote/
  --allowlist-dir PASTA --ancora-da-allowlist BASE64
                      a allowlist externa do no, se ele montar uma; sem elas o plano confere o alias
                      contra a allowlist embebida
  opcoes: --modelo M  --precos F  --pausa D  --host-esperado H  --destino-fora-da-lista H
          --reconstruir-contador  --so-plano

So um "qualificado" do modo real, com a corrida completa, deixa um registo por assinar. O comando
NAO assina e nao le chave de assinar nenhuma: assina o dono, com o aos-issuer route-profile-sign.
Saidas: 0 pacote preparado; 7 recusado; 8 inconclusivo; 4 parou a meio; 3 recusada antes de enviar;
        9 qualificado num ensaio (nada para assinar); 10 so-plano; 11 o no recusaria o registo;
        5 em CI; 2 argumentos; 1 outra falha (nunca fica pacote).
`

const usoDoConferir = `aos-ensaio conferir — confere um pacote ASSINADO como o no o confere no arranque (AOS-522)

  aos-ensaio conferir --pacote PASTA --ancora BASE64 --board B --regiao R
                      [--allowlist-dir PASTA --ancora-da-allowlist BASE64]

  --pacote PASTA      a pasta com <rotulo>.registo.json, .registo.sig e .relatorio.json, e mais nada
  --ancora BASE64     a chave PUBLICA dos perfis de rota (aos-issuer route-profile-anchor)
  --board, --regiao   o board e a regiao do no (sem valor por omissao)
  --allowlist-dir     a allowlist externa do no, se ele montar uma; sem ela confere-se a embebida

Saidas: 0 o no aceitaria o pacote; 11 recusaria (com a causa que o no daria); 2 argumentos.
`

// opcoesDoQualificar são as opções próprias do `qualificar`. As que a corrida também tem passam
// para ela tal como vieram ([passamParaACorrida]).
type opcoesDoQualificar struct {
	perfil, saida, board, regiao    string
	fatia                           string
	allowlistDir, ancoraDaAllowlist string
}

// passamParaACorrida são as opções do `qualificar` que seguem para a experiência `qualificacao`.
var passamParaACorrida = []string{
	"chaves", "fornecedor", "modelo", "precos", "pausa", "host-esperado", "destino-fora-da-lista",
	"reconstruir-contador", "so-plano", "roteiro", "binario-do-falso",
}

// novasFlagsDoQualificar declara o conjunto FECHADO de opções do `qualificar`.
func novasFlagsDoQualificar(o *opcoesDoQualificar, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("aos-ensaio qualificar", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usoDoQualificar) }
	fs.StringVar(&o.perfil, "perfil", "", "")
	fs.StringVar(&o.saida, "saida", "", "")
	fs.StringVar(&o.board, "board", "", "")
	fs.StringVar(&o.regiao, "regiao", "", "")
	fs.StringVar(&o.fatia, "fatia", "", "")
	fs.StringVar(&o.allowlistDir, "allowlist-dir", "", "")
	fs.StringVar(&o.ancoraDaAllowlist, "ancora-da-allowlist", "", "")
	fs.String("chaves", "", "")
	fs.String("fornecedor", "", "")
	fs.String("modelo", "", "")
	fs.String("precos", "", "")
	fs.Duration("pausa", 0, "")
	fs.String("host-esperado", "", "")
	fs.String("destino-fora-da-lista", "", "")
	fs.Bool("reconstruir-contador", false, "")
	fs.Bool("so-plano", false, "")
	fs.String("roteiro", "", "")
	fs.String("binario-do-falso", "", "")
	return fs
}

type opcoesDoConferir struct {
	pacote, ancora, board, regiao   string
	allowlistDir, ancoraDaAllowlist string
}

// novasFlagsDoConferir declara o conjunto FECHADO de opções do `conferir`. Só material público.
func novasFlagsDoConferir(o *opcoesDoConferir, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("aos-ensaio conferir", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usoDoConferir) }
	fs.StringVar(&o.pacote, "pacote", "", "")
	fs.StringVar(&o.ancora, "ancora", "", "")
	fs.StringVar(&o.board, "board", "", "")
	fs.StringVar(&o.regiao, "regiao", "", "")
	fs.StringVar(&o.allowlistDir, "allowlist-dir", "", "")
	fs.StringVar(&o.ancoraDaAllowlist, "ancora-da-allowlist", "", "")
	return fs
}

// O que se aceita na linha de comandos, em alfabetos FECHADOS. O que vai para o registo assinado e
// para a linha do `aos-issuer` que se imprime não leva espaços, aspas, novas linhas nem caracteres
// de controlo (um ESC num nome reescrevia o que o dono vê no terminal antes de assinar).
var (
	// nomeDeAmbiente é o alfabeto do board e da região. O nó só exige que não sejam vazios; este
	// alfabeto é do comando.
	nomeDeAmbiente = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// fatiaEmDecimal é a fatia como se escreve: só algarismos decimais, sem sinal, sem zeros à
	// esquerda, sem `_` nem prefixo de base. O intervalo (0 a 100) é o nó que o confere.
	fatiaEmDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,2})$`)
	// nomeReservadoDoWindows são os nomes de dispositivo: um ficheiro `NUL.registo.json` não é um
	// ficheiro no posto do dono, que é Windows.
	nomeReservadoDoWindows = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])$`)
)

// caminhoImprimivel diz se um caminho se pode imprimir numa linha de comandos entre aspas: sem
// caracteres de controlo e sem aspas.
func caminhoImprimivel(s string) bool {
	for _, c := range s {
		if c < 0x20 || c == 0x7f || c == '"' || (c >= 0x80 && c < 0xa0) {
			return false
		}
	}
	return s != ""
}

// allowlistEmVigor carrega a allowlist que o nó tem em vigor: a externa, se a pasta e a âncora
// forem dadas; senão a embebida no gateway. É a escolha que o nó faz no arranque.
func allowlistEmVigor(dir, ancora string) (*allowlist.Policy, string, error) {
	if dir != "" {
		p, err := allowlist.LoadSignedPolicyFromDir(dir, ancora)
		return p, "a EXTERNA de --allowlist-dir", err
	}
	p, err := allowlist.LoadPolicy()
	return p, "a EMBEBIDA no gateway (se o no monta uma allowlist externa, repita com --allowlist-dir e --ancora-da-allowlist)", err
}

// registoPorAssinar é o registo de entrada do AOS-519, com os nomes dos membros que
// [routeentry.ParseEntry] lê. Quem prende estes nomes aos do nó é a conferência: um nome que
// mudasse lá recusava aqui (`registo_ilegivel`), antes de haver ficheiro.
type registoPorAssinar struct {
	Formato               string          `json:"formato"`
	Board                 string          `json:"board"`
	Regiao                string          `json:"regiao"`
	Perfil                json.RawMessage `json:"perfil"`
	DigestDoPerfil        string          `json:"digest_do_perfil"`
	DigestDoRelatorio     string          `json:"digest_do_relatorio"`
	DigestDaImagemDoProxy string          `json:"digest_da_imagem_do_proxy"`
	Fatia                 int             `json:"fatia_inicial_percentagem"`
}

// digestPorConhecer faz de digest do relatório no registo PROVISÓRIO que se confere antes de
// enviar: tem a forma de um digest e não é o de relatório nenhum.
var digestPorConhecer = "sha256:" + strings.Repeat("0", 64)

// montarRegisto escreve os bytes de um registo de entrada.
func montarRegisto(r registoPorAssinar) ([]byte, error) {
	r.Formato = routeentry.EntryFormat
	cru, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(cru, '\n'), nil
}

// conferirPorAssinar faz a um registo ainda SEM assinatura as conferências que o nó lhe faz no
// arranque e que não dependem dela, com as funções do nó e pela ordem do nó: a leitura fechada do
// registo, o ambiente, a imagem do proxy declarada e — havendo relatório — o relatório citado. Um
// relatório nil é o registo provisório de antes da corrida: o relatório ainda não existe.
//
// O que fica por conferir é o que só existe depois: a assinatura. É o `conferir` que a vê, por
// [routeentry.LoadDir].
func conferirPorAssinar(registo, relatorio []byte, board, regiao string) (routeentry.Entry, error) {
	e, err := routeentry.ParseEntry(registo)
	if err != nil {
		return routeentry.Entry{}, err
	}
	if e.Board != board || e.Region != regiao || board == "" || regiao == "" {
		return routeentry.Entry{}, &routeentry.Refusal{Cause: routeentry.CauseOtherEnvironment}
	}
	if e.ProxyImageDigest != routeentry.ProductionProxyImageDigest() {
		return routeentry.Entry{}, &routeentry.Refusal{Cause: routeentry.CauseProxyImageNotDeclared}
	}
	if relatorio == nil {
		return e, nil
	}
	if err := routeentry.CheckReport(e, relatorio); err != nil {
		return routeentry.Entry{}, err
	}
	return e, nil
}

// FaseMedida é uma fase do `qualificar` com a hora a que começou e a que acabou.
type FaseMedida struct {
	Nome        string
	Inicio, Fim time.Time
}

// relogioDasFases regista a hora de início de cada fase; uma fase acaba quando a seguinte começa.
type relogioDasFases struct {
	agora func() time.Time
	fases []FaseMedida
}

func (r *relogioDasFases) marcar(nome string) {
	t := r.agora().UTC()
	if n := len(r.fases); n > 0 {
		r.fases[n-1].Fim = t
	}
	r.fases = append(r.fases, FaseMedida{Nome: nome, Inicio: t})
}

// fechar acaba a última fase e devolve todas.
func (r *relogioDasFases) fechar() []FaseMedida {
	if n := len(r.fases); n > 0 && r.fases[n-1].Fim.IsZero() {
		r.fases[n-1].Fim = r.agora().UTC()
	}
	return append([]FaseMedida(nil), r.fases...)
}

// revisaoDoBinario devolve a revisão do repositório de que este binário foi construído, ou
// «desconhecida» (um `go run`, um teste).
func revisaoDoBinario() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return s.Value[:12]
			}
		}
	}
	return "desconhecida"
}

// veredictoPositivo é a PRIMEIRA barreira: a linha de comandos saiu com o código de um
// `qualificado` do modo real, E o relatório diz o mesmo de si — o modo, a corrida completa, o
// veredicto e que vale para produção. O código sozinho não chega, nem o relatório sozinho.
func veredictoPositivo(codigo int, r *Relatorio) bool {
	q := r.QualificacaoDoModelo
	return codigo == SaidaOK && q != nil && r.Modo == ModoReal && r.Terminou == TerminouCompleta &&
		q.Veredicto == VeredictoQualificado && q.ValeParaProducao
}

// qualificar é o subcomando `qualificar`.
func qualificar(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor) int {
	relogio := &relogioDasFases{agora: amb.Relogio}
	relogio.marcar(FaseDoPlano)
	if len(args) == 0 {
		fmt.Fprint(stderr, usoDoQualificar)
		return SaidaUso
	}
	modo := args[0]
	if modo != ModoFalso && modo != ModoProxy && modo != ModoReal {
		fmt.Fprint(stderr, usoDoQualificar)
		return SaidaUso
	}
	// O MODO REAL NUNCA CORRE EM CI — como o banco já faz, e antes de ler seja o que for.
	if modo == ModoReal {
		for _, v := range variaveisDeCI {
			if amb.Getenv(v) != "" {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — o modo com modelo real nao corre em CI (a variavel %s esta definida)\n", v)
				return SaidaEmCI
			}
		}
	}
	var o opcoesDoQualificar
	fs := novasFlagsDoQualificar(&o, stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "aos-ensaio qualificar: argumento a mais: %q\n", fs.Arg(0))
		return SaidaUso
	}
	dadas := map[string]string{}
	fs.Visit(func(f *flag.Flag) { dadas[f.Name] = f.Value.String() })
	uso := func(mensagem string) int {
		fmt.Fprintln(stderr, "aos-ensaio qualificar: "+mensagem)
		return SaidaUso
	}
	switch {
	case o.perfil == "" || o.saida == "":
		return uso("--perfil e --saida sao obrigatorios")
	case !caminhoImprimivel(o.saida):
		return uso("--saida nao pode ter aspas nem caracteres de controlo: o caminho e impresso na linha de assinar")
	case o.board == "" || o.regiao == "":
		// SEM VALOR POR OMISSÃO: com um, um registo preparado para um nó de desenvolvimento servia
		// em produção (revisão do AOS-519).
		return uso("--board e --regiao sao obrigatorios e nao tem valor por omissao: sao o board e a regiao do no a que o registo se destina, e ficam debaixo da assinatura")
	case !nomeDeAmbiente.MatchString(o.board) || !nomeDeAmbiente.MatchString(o.regiao):
		return uso(fmt.Sprintf("--board e --regiao tem, cada um, de 1 a %d letras, digitos, _ e - (sem espacos, aspas nem caracteres de controlo)", TamanhoMaximoDoAmbiente))
	case o.fatia == "":
		return uso("--fatia e obrigatoria (a fatia inicial do canary, de 0 a 100; a decisao D4 do dono e 10) e fica debaixo da assinatura")
	case !fatiaEmDecimal.MatchString(o.fatia):
		return uso("--fatia escreve-se so com algarismos decimais, sem sinal, sem zeros a esquerda e sem prefixo (10, e nao +10, 010, 0x0A, 0b11 nem 1_0)")
	case (o.allowlistDir == "") != (o.ancoraDaAllowlist == ""):
		return uso("--allowlist-dir e --ancora-da-allowlist vao os dois, ou nenhum")
	}
	fatia, _ := strconv.Atoi(o.fatia) // só algarismos, no máximo três: não falha

	// O REGISTO QUE O NÓ RECUSARIA NÃO SE QUALIFICA. Antes de enviar seja o que for, monta-se o
	// registo com tudo o que já se sabe — o perfil, o ambiente, a fatia — e passa-se pela leitura
	// do nó. Um perfil que devolve estado, um alias que não serve ou uma fatia fora do intervalo
	// param aqui, com a causa do nó, e não ao fim de vinte minutos de corrida.
	doc, err := os.ReadFile(o.perfil) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		return uso("nao foi possivel ler o ficheiro de --perfil")
	}
	recusar := func(causa string) int {
		fmt.Fprintf(stderr, "aos-ensaio qualificar: RECUSADO ANTES DE ENVIAR — o no recusaria este registo no arranque: causa=%s. Nenhum pedido foi enviado e nada foi escrito\n", causa)
		return SaidaPacoteRecusado
	}
	perfil, err := modelgateway.ParseRouteProfile(doc)
	if err != nil {
		// A leitura fechada do gateway — a mesma por que o perfil passa dentro do registo.
		return recusar(routeentry.CauseBadProfile)
	}
	base := registoPorAssinar{
		Board: o.board, Regiao: o.regiao, Perfil: json.RawMessage(doc), Fatia: fatia,
		DigestDoPerfil: perfil.Digest(), DigestDoRelatorio: digestPorConhecer,
		DigestDaImagemDoProxy: routeentry.ProductionProxyImageDigest(),
	}
	provisorio, err := montarRegisto(base)
	if err != nil {
		return recusar(routeentry.CauseBadProfile)
	}
	candidato, err := conferirPorAssinar(provisorio, nil, o.board, o.regiao)
	if err != nil {
		return recusar(routeentry.CauseOf(err))
	}
	alias := candidato.Profile.Requested
	if nomeReservadoDoWindows.MatchString(alias) {
		return uso("o alias do perfil e um nome de dispositivo do Windows (CON, PRN, AUX, NUL, COM0 a COM9, LPT0 a LPT9): os ficheiros do pacote levam o alias no nome, e no posto nao seriam ficheiros")
	}
	// A ALLOWLIST CONTINUA A MANDAR, e sabe-se JÁ: um alias que a allowlist do nó não autoriza para
	// este board e esta região gastava uma qualificação e só era recusado depois de assinado.
	emVigor, qualAllowlist, err := allowlistEmVigor(o.allowlistDir, o.ancoraDaAllowlist)
	if err != nil {
		return uso("a allowlist nao se carrega (a pasta, a assinatura ou a ancora dela): nada foi conferido nem enviado")
	}
	if emVigor.Evaluate(allowlist.Input{Board: o.board, Model: alias, Region: o.regiao}) != allowlist.EffectAllow {
		fmt.Fprintf(stderr, "aos-ensaio qualificar: allowlist conferida: %s\n", qualAllowlist)
		return recusar(routeentry.CauseNameNotAllowlisted)
	}
	fmt.Fprintf(stderr, "aos-ensaio qualificar: PLANO — o registo passa na leitura do no (formato, fatia, perfil, alias, classe de estado, imagem do proxy declarada) e a allowlist autoriza o alias para este board e esta regiao; allowlist conferida: %s. Fica por conferir o que so existe depois: o relatorio (no fim da corrida) e a assinatura (no `conferir`)\n", qualAllowlist)

	// A PASTA DE SAÍDA É DESTA CORRIDA: tem de não existir, e é criada de forma exclusiva — duas
	// corridas para a mesma pasta, a segunda falha aqui sem tocar em nada.
	if _, serr := os.Lstat(o.saida); !errors.Is(serr, os.ErrNotExist) {
		fmt.Fprintln(stderr, "aos-ensaio qualificar: RECUSADO — a pasta de --saida ja existe: tem de ser uma pasta nova, criada por esta corrida (o que la ficar e so dela)")
		return SaidaRecusada
	}
	_, soPlano := dadas["so-plano"]
	if !soPlano {
		if err := os.MkdirAll(filepath.Dir(o.saida), 0o700); err != nil {
			return uso("nao foi possivel criar a pasta acima de --saida")
		}
		if err := os.Mkdir(o.saida, 0o700); err != nil {
			fmt.Fprintln(stderr, "aos-ensaio qualificar: RECUSADO — a pasta de --saida nao se conseguiu criar de novo (outra corrida criou-a entretanto?)")
			return SaidaRecusada
		}
		// Uma corrida que não chega a escrever nada não deixa a pasta vazia para trás. `Remove` só
		// apaga uma pasta VAZIA: nunca leva nada que lá esteja.
		defer func() { _ = os.Remove(o.saida) }()
	}

	// A CORRIDA. É a experiência `qualificacao` do AOS-518, pela linha de comandos de sempre: os
	// tectos, as qualificações do dia e a imagem do proxy conferem-se lá, e não aqui.
	corrida := []string{modo, "--experiencia", string(ExperienciaQualificacao), "--perfil", o.perfil, "--saida", o.saida}
	for _, nome := range passamParaACorrida {
		if v, dada := dadas[nome]; dada {
			corrida = append(corrida, "--"+nome+"="+v)
		}
	}
	amb.fase = relogio.marcar
	var feita corridaFeita
	// O resumo comprido da corrida não se mostra: fica no ficheiro. O progresso (stderr) fica.
	codigo := executarCom(ctx, corrida, io.Discard, stderr, amb, red, &feita)
	relogio.marcar(FaseDaPreparacao)
	if amb.entreACorridaEAPreparacao != nil {
		amb.entreACorridaEAPreparacao(&feita)
	}

	r := feita.relatorio
	if r == nil || r.QualificacaoDoModelo == nil {
		// A corrida não chegou a escrever relatório (recusada, só o plano, sem infra-estrutura).
		fmt.Fprintln(stderr, "aos-ensaio qualificar: NADA PARA ASSINAR — a corrida nao deixou relatorio; a causa esta acima")
		if codigo == SaidaOK {
			return SaidaErro
		}
		return codigo
	}
	q := r.QualificacaoDoModelo
	dados := DadosDoResumo{
		Registo: candidato, RevisaoDoBinario: revisaoDoBinario(),
		Motivo: fmt.Sprintf("veredicto %s, modo %s, a corrida terminou %s", q.Veredicto, r.Modo, r.Terminou),
	}
	if antes, tem := modelgateway.RouteProfileFor(alias); tem {
		dados.Substitui, dados.ModeloEsperadoDeAntes = true, antes.ExpectedModel
	}

	// SEM VEREDICTO POSITIVO NÃO HÁ NADA PARA ASSINAR.
	pacote := filepath.Join(o.saida, PastaDoPacote)
	if veredictoPositivo(codigo, r) {
		noDisco, causa, perr := prepararPacote(pacote, alias, base, feita, o.board, o.regiao, amb.depoisDeEscreverOPacote)
		switch {
		case causa != "":
			dados.Motivo = "o no recusaria o pacote no arranque: causa=" + causa
			codigo = SaidaPacoteRecusado
		case perr != nil:
			fmt.Fprintf(stderr, "aos-ensaio qualificar: %v\n", perr)
			dados.Motivo = perr.Error()
			codigo = SaidaErro
		default:
			// O QUE O RESUMO MOSTRA É O QUE FICOU ESCRITO: o registo lido de volta do disco.
			dados.Preparado, dados.Registo = true, noDisco
		}
	} else if codigo == SaidaOK {
		// O código diz `qualificado` e o relatório não: não se escolhe um dos dois.
		dados.Motivo = "o codigo de saida da corrida e o relatorio nao dizem o mesmo (" + dados.Motivo + ")"
		codigo = SaidaErro
	}

	dados.Fases = relogio.fechar()
	resumo := ResumoDeUmaPagina(r, dados)
	caminhoDoResumo := filepath.Join(o.saida, NomeDoResumoDeUmaPagina)
	if err := os.WriteFile(caminhoDoResumo, []byte(resumo), 0o600); err != nil {
		// Sem o resumo escrito o dono não tem o que ler antes de assinar: não fica pacote. Só se
		// apaga o pacote que ESTA corrida criou.
		if dados.Preparado {
			_ = os.RemoveAll(pacote)
		}
		fmt.Fprintln(stderr, "aos-ensaio qualificar: nao foi possivel escrever o resumo de uma pagina; nao ficou pacote nenhum")
		return SaidaErro
	}
	fmt.Fprint(stdout, resumo)
	fmt.Fprintf(stdout, "\nrelatorio: %s\nresumo:    %s\n", feita.emJSON, caminhoDoResumo)
	if !dados.Preparado {
		fmt.Fprintf(stderr, "aos-ensaio qualificar: NADA PARA ASSINAR — %s\n", dados.Motivo)
		return codigo
	}
	entrada := filepath.Join(pacote, alias+routeentry.SuffixEntry)
	citado := filepath.Join(pacote, alias+routeentry.SuffixReport)
	assinatura := filepath.Join(pacote, alias+routeentry.SuffixSignature)
	daAllowlist := ""
	if o.allowlistDir != "" && caminhoImprimivel(o.allowlistDir) && caminhoImprimivel(o.ancoraDaAllowlist) {
		daAllowlist = fmt.Sprintf(" --allowlist-dir \"%s\" --ancora-da-allowlist \"%s\"", o.allowlistDir, o.ancoraDaAllowlist)
	}
	fmt.Fprintf(stdout, "pacote por assinar: %s\n", pacote)
	fmt.Fprintf(stdout, "\nPASSO SEGUINTE — ASSINAR. A chave e tua e este comando nunca a le. Le o resumo acima primeiro:\n")
	fmt.Fprintf(stdout, "  aos-issuer route-profile-sign --entry \"%s\" --report \"%s\" --key-file <FICHEIRO-DA-SEED-DOS-PERFIS> --out \"%s\"\n", entrada, citado, assinatura)
	fmt.Fprintf(stdout, "DEPOIS — CONFERIR o pacote assinado no posto, como o no o confere no arranque, ANTES de o copiar:\n")
	fmt.Fprintf(stdout, "  aos-ensaio conferir --pacote \"%s\" --ancora <ANCORA-DOS-PERFIS> --board \"%s\" --regiao \"%s\"%s\n", pacote, o.board, o.regiao, daAllowlist)
	return codigo
}

// prepararPacote escreve, na pasta do pacote, o relatório da corrida que acabou e o registo de
// entrada que o cita. Devolve o registo LIDO DE VOLTA DO DISCO; ou a causa por que o nó o
// recusaria (e então não escreve nada); ou um erro (e então não fica nada).
//
// São as barreiras 2, 3 e 4 do cabeçalho deste ficheiro. `depoisDeEscrever` é uma costura de
// teste, chamada entre a escrita e a releitura; em produção é nil.
func prepararPacote(pacote, rotulo string, base registoPorAssinar, feita corridaFeita, board, regiao string, depoisDeEscrever func(pacote string)) (noDisco routeentry.Entry, causa string, err error) {
	nada := routeentry.Entry{}
	relatorio, err := os.ReadFile(feita.emJSON) // #nosec G304 -- o relatorio que este processo acabou de escrever
	if err != nil {
		return nada, "", errors.New("nao foi possivel reler o relatorio que a corrida escreveu")
	}
	// BARREIRA 2: o relatório no disco é o que a corrida escreveu.
	digest, err := routeentry.VerifyReportDigest(relatorio)
	if err != nil {
		return nada, routeentry.CauseReportInvalid, nil
	}
	if digest != feita.relatorio.DigestDoRelatorio {
		return nada, "", ErrRelatorioTrocado
	}
	rota := feita.relatorio.QualificacaoDoModelo.Rota
	base.DigestDoPerfil, base.DigestDoRelatorio, base.DigestDaImagemDoProxy = rota.DigestDoPerfil, digest, rota.DigestDaImagemDoProxy
	registo, err := montarRegisto(base)
	if err != nil {
		return nada, "", err
	}
	// BARREIRA 3: as conferências do nó, antes de haver ficheiro.
	if _, cerr := conferirPorAssinar(registo, relatorio, board, regiao); cerr != nil {
		return nada, routeentry.CauseOf(cerr), nil
	}
	// A pasta do pacote é criada AQUI, de forma exclusiva: daqui para baixo, apagá-la é apagar só o
	// que esta corrida escreveu.
	if err := os.Mkdir(pacote, 0o700); err != nil {
		return nada, "", errors.New("nao foi possivel criar a pasta do pacote")
	}
	entrada, citado := filepath.Join(pacote, rotulo+routeentry.SuffixEntry), filepath.Join(pacote, rotulo+routeentry.SuffixReport)
	falhou := func(porque error) (routeentry.Entry, string, error) {
		_ = os.RemoveAll(pacote)
		return nada, "", porque
	}
	// O rótulo é o alias do perfil, que já passou pelo alfabeto fechado dos rótulos do nó (letras,
	// dígitos, `_` e `-`; sem separador de caminho) em [routeentry.ParseEntry], acima.
	if err := os.WriteFile(citado, relatorio, 0o600); err != nil { // #nosec G703 -- pasta de --saida do operador mais um rotulo conferido contra um alfabeto fechado
		return falhou(errors.New("nao foi possivel escrever o relatorio no pacote"))
	}
	if err := os.WriteFile(entrada, registo, 0o600); err != nil {
		return falhou(errors.New("nao foi possivel escrever o registo de entrada no pacote"))
	}
	if depoisDeEscrever != nil {
		depoisDeEscrever(pacote)
	}
	// BARREIRA 4: o que ficou no disco é, byte a byte, o que se conferiu — e volta a passar.
	registoNoDisco, err1 := os.ReadFile(entrada) // #nosec G304 -- o ficheiro que se acabou de escrever
	citadoNoDisco, err2 := os.ReadFile(citado)   // #nosec G304 -- idem
	if err1 != nil || err2 != nil || !bytes.Equal(registoNoDisco, registo) || !bytes.Equal(citadoNoDisco, relatorio) {
		return falhou(ErrPacoteAlterado)
	}
	noDisco, cerr := conferirPorAssinar(registoNoDisco, citadoNoDisco, board, regiao)
	if cerr != nil {
		return falhou(ErrPacoteAlterado)
	}
	return noDisco, "", nil
}

// comporPerfis é a validação que o gateway faz ao compor-se com os perfis assinados, e que o nó
// faz logo a seguir a ler a pasta. É uma variável para um teste a poder fazer falhar: com os
// perfis de hoje, tudo o que ela recusa já foi recusado por [routeentry.LoadDir].
var comporPerfis = func(perfis ...modelgateway.RouteProfile) error {
	_, err := modelgateway.NewRouteProfileSet(perfis...)
	return err
}

// conferir é o subcomando `conferir`: passa um pacote JÁ ASSINADO por [routeentry.LoadDir], a
// função que o nó corre no arranque, com a âncora, o board, a região e a allowlist que o nó tem.
// Aceite aqui, é aceite pelo nó; recusado aqui, a causa é a palavra que o nó daria.
func conferir(args []string, stdout, stderr io.Writer) int {
	var o opcoesDoConferir
	fs := novasFlagsDoConferir(&o, stderr)
	if err := fs.Parse(args); err != nil {
		return SaidaUso
	}
	o.board, o.regiao, o.ancora = strings.TrimSpace(o.board), strings.TrimSpace(o.regiao), strings.TrimSpace(o.ancora)
	if fs.NArg() > 0 || o.pacote == "" || o.ancora == "" || o.board == "" || o.regiao == "" || (o.allowlistDir == "") != (o.ancoraDaAllowlist == "") {
		fmt.Fprint(stderr, usoDoConferir)
		return SaidaUso
	}
	// A allowlist EM VIGOR no nó: a externa, se ele a monta; senão a embebida. Como no nó.
	emVigor, qual, err := allowlistEmVigor(o.allowlistDir, o.ancoraDaAllowlist)
	if err != nil {
		fmt.Fprintln(stderr, "aos-ensaio conferir: a allowlist nao se carrega (a pasta, a assinatura ou a ancora dela): nada foi conferido")
		return SaidaUso
	}
	entradas, err := routeentry.LoadDir(routeentry.Config{
		Dir: o.pacote, TrustAnchor: o.ancora,
		ForeignAnchors: []string{o.ancoraDaAllowlist, allowlist.EmbeddedTrustAnchor()},
		Board:          o.board, Region: o.regiao, Allowlist: emVigor,
	})
	if err != nil {
		var recusa *routeentry.Refusal
		if errors.As(err, &recusa) {
			fmt.Fprintf(stderr, "aos-ensaio conferir: O NO RECUSARIA ESTE PACOTE e nao arrancava: causa=%s registo=%s. Nao o copie para o servidor\n", recusa.Cause, recusa.Label)
		} else {
			fmt.Fprintln(stderr, "aos-ensaio conferir: O NO RECUSARIA ESTE PACOTE e nao arrancava. Nao o copie para o servidor")
		}
		return SaidaPacoteRecusado
	}
	if len(entradas) == 0 {
		fmt.Fprintln(stderr, "aos-ensaio conferir: a pasta nao tem registo nenhum: nao ha pacote para conferir")
		return SaidaPacoteRecusado
	}
	perfis := make([]modelgateway.RouteProfile, 0, len(entradas))
	for _, e := range entradas {
		perfis = append(perfis, e.Profile)
	}
	if err := comporPerfis(perfis...); err != nil {
		fmt.Fprintln(stderr, "aos-ensaio conferir: O NO RECUSARIA ESTE PACOTE: os perfis nao se compoem com a tabela em codigo. Nao o copie para o servidor")
		return SaidaPacoteRecusado
	}
	for _, e := range entradas {
		origem := "ACRESCENTA-SE a tabela em codigo"
		if antes, tem := modelgateway.RouteProfileFor(e.Profile.Requested); tem {
			origem = fmt.Sprintf("SUBSTITUI a entrada da tabela em codigo (modelo esperado de antes %q)", antes.ExpectedModel)
		}
		fmt.Fprintf(stdout, "conferir: ACEITE — registo %s: alias %q, modelo esperado %q, fatia inicial %d%%; %s, segundo a tabela deste binario (revisao %s), que pode nao ser a do no\n", e.Label, e.Profile.Requested, e.Profile.ExpectedModel, e.InitialSharePercent, origem, revisaoDoBinario())
		fmt.Fprintf(stdout, "conferir:   digest do registo %s\n", e.EntryDigest)
		fmt.Fprintf(stdout, "conferir:   digest do perfil  %s\n", e.ProfileDigest)
		fmt.Fprintf(stdout, "conferir:   digest do relatorio %s\n", e.ReportDigest)
	}
	fmt.Fprintf(stdout, "conferir: board %q, regiao %q; allowlist conferida: %s\n", o.board, o.regiao, qual)
	fmt.Fprintln(stdout, "conferir: o no aceitaria este pacote no arranque. Compare o digest do registo com o que o route-profile-sign imprimiu. Fica por conferir o que so o servidor sabe: que a ancora, o board e a regiao dados aqui sao os do no")
	return SaidaOK
}
