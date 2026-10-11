package bancoensaio

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// saída tem de estar vazia à partida.

// SaidaPacoteRecusado — o registo de entrada, ou o pacote já assinado, seria recusado pelo nó no
// arranque. A causa sai no vocabulário fechado do `routeentry`. No `qualificar` a recusa do
// registo vem ANTES de qualquer pedido; no `conferir`, antes da cópia para o servidor.
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

const usoDoQualificar = `aos-ensaio qualificar — qualifica uma rota e prepara o registo de entrada para o dono assinar (AOS-522)

  aos-ensaio qualificar real  --chaves F --fornecedor kimi|anthropic|openrouter --perfil F --fatia N
                              --board B --regiao R --saida PASTA [opcoes]
  aos-ensaio qualificar falso --perfil F --fatia N --board B --regiao R --saida PASTA [--roteiro ...]
  aos-ensaio qualificar proxy --binario-do-falso F --perfil F --fatia N --board B --regiao R --saida PASTA

  --perfil FICHEIRO   o perfil candidato; o seu "requested" e o alias que o no pede em producao
  --fatia N           a fatia inicial do canary, de 0 a 100, que fica escrita no registo assinado
  --board B           o board do no a que o registo se destina   (sem valor por omissao)
  --regiao R          a regiao desse no                          (sem valor por omissao)
  --saida PASTA       pasta VAZIA ou por criar: relatorio, resumo e, so com veredicto positivo, pacote/
  opcoes: --modelo M  --precos F  --pausa D  --host-esperado H  --destino-fora-da-lista H
          --reconstruir-contador  --so-plano

So um "qualificado" do modo real, com a corrida completa, deixa um registo por assinar. O comando
NAO assina e nao le chave de assinar nenhuma: assina o dono, com o aos-issuer route-profile-sign.
Saidas: 0 pacote preparado; 7 recusado; 8 inconclusivo; 4 parou a meio; 3 recusada antes de enviar;
        9 qualificado num ensaio (nada para assinar); 10 so-plano; 11 o no recusaria o registo; 5 em CI.
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

// flagsDoQualificar são as opções do `qualificar`. As que a corrida também tem passam para ela
// tal como vieram ([passamParaACorrida]).
type opcoesDoQualificar struct {
	perfil, saida, board, regiao string
	fatia                        int
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
	fs.IntVar(&o.fatia, "fatia", 0, "")
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

type opcoesDoConferir struct {
	pacote, ancora, board, regiao   string
	allowlistDir, ancoraDaAllowlist string
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
// O que fica por conferir é o que só existe depois: a assinatura, e a allowlist em vigor no nó. É
// o `conferir` que as vê, por [routeentry.LoadDir].
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

// pastaVaziaOuPorCriar diz se a pasta não existe ou existe sem nada dentro.
func pastaVaziaOuPorCriar(pasta string) bool {
	entradas, err := os.ReadDir(pasta)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && len(entradas) == 0
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
	o.board, o.regiao = strings.TrimSpace(o.board), strings.TrimSpace(o.regiao)
	_, temFatia := dadas["fatia"]
	switch {
	case o.perfil == "" || o.saida == "":
		fmt.Fprintln(stderr, "aos-ensaio qualificar: --perfil e --saida sao obrigatorios")
		return SaidaUso
	case o.board == "" || o.regiao == "":
		// SEM VALOR POR OMISSÃO: com um, um registo preparado para um nó de desenvolvimento servia
		// em produção (revisão do AOS-519).
		fmt.Fprintln(stderr, "aos-ensaio qualificar: --board e --regiao sao obrigatorios e nao tem valor por omissao: sao o board e a regiao do no a que o registo se destina, e ficam debaixo da assinatura")
		return SaidaUso
	case len(o.board) > TamanhoMaximoDoAmbiente || len(o.regiao) > TamanhoMaximoDoAmbiente:
		fmt.Fprintf(stderr, "aos-ensaio qualificar: --board e --regiao tem, cada um, no maximo %d bytes (o resumo que se assina cabe numa pagina)\n", TamanhoMaximoDoAmbiente)
		return SaidaUso
	case !temFatia:
		fmt.Fprintln(stderr, "aos-ensaio qualificar: --fatia e obrigatoria (a fatia inicial do canary, de 0 a 100; a decisao D4 do dono e 10) e fica debaixo da assinatura")
		return SaidaUso
	}

	// O REGISTO QUE O NÓ RECUSARIA NÃO SE QUALIFICA. Antes de enviar seja o que for, monta-se o
	// registo com tudo o que já se sabe — o perfil, o ambiente, a fatia — e passa-se pela leitura
	// do nó. Um perfil que devolve estado, um alias que não serve ou uma fatia fora do intervalo
	// param aqui, com a causa do nó, e não ao fim de vinte minutos de corrida.
	doc, err := os.ReadFile(o.perfil) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		fmt.Fprintln(stderr, "aos-ensaio qualificar: nao foi possivel ler o ficheiro de --perfil")
		return SaidaUso
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
		Board: o.board, Regiao: o.regiao, Perfil: json.RawMessage(doc), Fatia: o.fatia,
		DigestDoPerfil: perfil.Digest(), DigestDoRelatorio: digestPorConhecer,
		DigestDaImagemDoProxy: routeentry.ProductionProxyImageDigest(),
	}
	provisorio, err := montarRegisto(base)
	if err != nil {
		return recusar(routeentry.CauseBadProfile)
	}
	if _, err := conferirPorAssinar(provisorio, nil, o.board, o.regiao); err != nil {
		return recusar(routeentry.CauseOf(err))
	}
	if !pastaVaziaOuPorCriar(o.saida) {
		fmt.Fprintln(stderr, "aos-ensaio qualificar: RECUSADO — a pasta de --saida tem de estar vazia ou por criar: o que la ficar e so desta corrida (um registo de uma corrida anterior nao pode ficar ao lado do relatorio desta)")
		return SaidaRecusada
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
		Alias: perfil.Requested, Board: o.board, Regiao: o.regiao, Fatia: o.fatia,
		Motivo: fmt.Sprintf("veredicto %s, modo %s, a corrida terminou %s", q.Veredicto, r.Modo, r.Terminou),
	}
	if antes, tem := modelgateway.RouteProfileFor(perfil.Requested); tem {
		dados.Substitui, dados.ModeloEsperadoDeAntes = true, antes.ExpectedModel
	}

	// SEM VEREDICTO POSITIVO NÃO HÁ NADA PARA ASSINAR. Positivo é: a linha de comandos saiu com o
	// código de um `qualificado` do modo real, E o relatório diz o mesmo de si.
	positivo := codigo == SaidaOK && r.Modo == ModoReal && r.Terminou == TerminouCompleta &&
		q.Veredicto == VeredictoQualificado && q.ValeParaProducao
	pacote := filepath.Join(o.saida, PastaDoPacote)
	if positivo {
		registo, causa, perr := prepararPacote(pacote, perfil.Requested, base, feita, o.board, o.regiao)
		switch {
		case causa != "":
			dados.Motivo = "o no recusaria o pacote no arranque: causa=" + causa
			codigo = SaidaPacoteRecusado
		case perr != nil:
			fmt.Fprintf(stderr, "aos-ensaio qualificar: %v\n", perr)
			dados.Motivo = "o pacote nao se conseguiu escrever"
			codigo = SaidaErro
		default:
			dados.Preparado, dados.DigestDoRegisto = true, routeentry.EntryDigest(registo)
		}
	}

	dados.Fases = relogio.fechar()
	resumo := ResumoDeUmaPagina(r, dados)
	caminhoDoResumo := filepath.Join(o.saida, NomeDoResumoDeUmaPagina)
	if err := os.WriteFile(caminhoDoResumo, []byte(resumo), 0o600); err != nil {
		// Sem o resumo escrito o dono não tem o que ler antes de assinar: não fica pacote.
		_ = os.RemoveAll(pacote)
		fmt.Fprintln(stderr, "aos-ensaio qualificar: nao foi possivel escrever o resumo de uma pagina; nao ficou pacote nenhum")
		return SaidaErro
	}
	fmt.Fprint(stdout, resumo)
	fmt.Fprintf(stdout, "\nrelatorio: %s\nresumo:    %s\n", feita.emJSON, caminhoDoResumo)
	if !dados.Preparado {
		fmt.Fprintf(stderr, "aos-ensaio qualificar: NADA PARA ASSINAR — %s\n", dados.Motivo)
		return codigo
	}
	entrada := filepath.Join(pacote, perfil.Requested+routeentry.SuffixEntry)
	citado := filepath.Join(pacote, perfil.Requested+routeentry.SuffixReport)
	assinatura := filepath.Join(pacote, perfil.Requested+routeentry.SuffixSignature)
	fmt.Fprintf(stdout, "pacote por assinar: %s\n", pacote)
	fmt.Fprintf(stdout, "\nPASSO SEGUINTE — ASSINAR. A chave e tua e este comando nunca a le. Le o resumo acima primeiro:\n")
	fmt.Fprintf(stdout, "  aos-issuer route-profile-sign --entry \"%s\" --report \"%s\" --key-file <FICHEIRO-DA-SEED-DOS-PERFIS> --out \"%s\"\n", entrada, citado, assinatura)
	fmt.Fprintf(stdout, "DEPOIS — CONFERIR o pacote assinado no posto, como o no o confere no arranque, ANTES de o copiar:\n")
	fmt.Fprintf(stdout, "  aos-ensaio conferir --pacote \"%s\" --ancora <ANCORA-DOS-PERFIS> --board \"%s\" --regiao \"%s\"\n", pacote, o.board, o.regiao)
	return codigo
}

// prepararPacote escreve, na pasta do pacote, o relatório da corrida que acabou e o registo de
// entrada que o cita. Devolve os bytes do registo; ou a causa por que o nó o recusaria (e então
// não escreve nada); ou um erro de escrita (e então não fica nada).
//
// O relatório é o FICHEIRO que a corrida escreveu: lê-se do disco, confere-se o seu digest contra
// o do relatório em memória, e é esse digest que o registo cita. Tudo é conferido ANTES de haver
// ficheiro, e outra vez DEPOIS, a partir do que ficou no disco.
func prepararPacote(pacote, rotulo string, base registoPorAssinar, feita corridaFeita, board, regiao string) (registo []byte, causa string, err error) {
	relatorio, err := os.ReadFile(feita.emJSON) // #nosec G304 -- o relatorio que este processo acabou de escrever
	if err != nil {
		return nil, "", errors.New("nao foi possivel reler o relatorio que a corrida escreveu")
	}
	digest, err := routeentry.VerifyReportDigest(relatorio)
	if err != nil || digest != feita.relatorio.DigestDoRelatorio {
		return nil, routeentry.CauseReportInvalid, nil
	}
	rota := feita.relatorio.QualificacaoDoModelo.Rota
	base.DigestDoPerfil, base.DigestDoRelatorio, base.DigestDaImagemDoProxy = rota.DigestDoPerfil, digest, rota.DigestDaImagemDoProxy
	if registo, err = montarRegisto(base); err != nil {
		return nil, "", err
	}
	if _, cerr := conferirPorAssinar(registo, relatorio, board, regiao); cerr != nil {
		return nil, routeentry.CauseOf(cerr), nil
	}
	if err := os.Mkdir(pacote, 0o700); err != nil {
		return nil, "", errors.New("nao foi possivel criar a pasta do pacote")
	}
	entrada, citado := filepath.Join(pacote, rotulo+routeentry.SuffixEntry), filepath.Join(pacote, rotulo+routeentry.SuffixReport)
	falhou := func(porque string) ([]byte, string, error) {
		_ = os.RemoveAll(pacote)
		return nil, "", errors.New(porque)
	}
	if err := os.WriteFile(citado, relatorio, 0o600); err != nil {
		return falhou("nao foi possivel escrever o relatorio no pacote")
	}
	if err := os.WriteFile(entrada, registo, 0o600); err != nil {
		return falhou("nao foi possivel escrever o registo de entrada no pacote")
	}
	// O que ficou no disco é o que se conferiu.
	noDisco, err1 := os.ReadFile(entrada)      // #nosec G304 -- o ficheiro que se acabou de escrever
	citadoNoDisco, err2 := os.ReadFile(citado) // #nosec G304 -- idem
	if err1 != nil || err2 != nil {
		return falhou("nao foi possivel reler o pacote escrito")
	}
	if _, cerr := conferirPorAssinar(noDisco, citadoNoDisco, board, regiao); cerr != nil {
		return falhou("o pacote escrito nao passa na conferencia do no: causa=" + routeentry.CauseOf(cerr))
	}
	return registo, "", nil
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
	emVigor, qual := (*allowlist.Policy)(nil), "a EMBEBIDA no gateway (se o no monta uma allowlist externa, repita com --allowlist-dir)"
	var err error
	if o.allowlistDir != "" {
		emVigor, err = allowlist.LoadSignedPolicyFromDir(o.allowlistDir, o.ancoraDaAllowlist)
		qual = "a EXTERNA de --allowlist-dir"
	} else {
		emVigor, err = allowlist.LoadPolicy()
	}
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
	// A validação que o gateway faz ao compor-se, e que o nó faz logo a seguir a ler a pasta.
	if _, err := modelgateway.NewRouteProfileSet(perfis...); err != nil {
		fmt.Fprintln(stderr, "aos-ensaio conferir: O NO RECUSARIA ESTE PACOTE: os perfis nao se compoem com a tabela em codigo. Nao o copie para o servidor")
		return SaidaPacoteRecusado
	}
	for _, e := range entradas {
		origem := "ACRESCENTA-SE a tabela em codigo"
		if antes, tem := modelgateway.RouteProfileFor(e.Profile.Requested); tem {
			origem = fmt.Sprintf("SUBSTITUI a entrada da tabela em codigo (modelo esperado de antes %q)", antes.ExpectedModel)
		}
		fmt.Fprintf(stdout, "conferir: ACEITE — registo %s: alias %q, modelo esperado %q, fatia inicial %d%%; %s\n", e.Label, e.Profile.Requested, e.Profile.ExpectedModel, e.InitialSharePercent, origem)
		fmt.Fprintf(stdout, "conferir:   digest do registo %s\n", e.EntryDigest)
		fmt.Fprintf(stdout, "conferir:   digest do perfil  %s\n", e.ProfileDigest)
		fmt.Fprintf(stdout, "conferir:   digest do relatorio %s\n", e.ReportDigest)
	}
	fmt.Fprintf(stdout, "conferir: board %q, regiao %q; allowlist conferida: %s\n", o.board, o.regiao, qual)
	fmt.Fprintln(stdout, "conferir: o no aceitaria este pacote no arranque. Compare o digest do registo com o que o route-profile-sign imprimiu. Fica por conferir o que so o servidor sabe: que a ancora, o board e a regiao dados aqui sao os do no")
	return SaidaOK
}
