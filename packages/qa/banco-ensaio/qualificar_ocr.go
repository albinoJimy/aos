package bancoensaio

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// PREPARAR O REGISTO DE USO `tool` (AOS-530, ADR-042 §2.3).
//
// `aos-ensaio qualificar-ocr` é o `qualificar` do AOS-522 para o alias de uma tool cujo efeito é
// uma chamada a um modelo de visão (a `ocr_read`): corre a experiência `ocr` com o perfil do alias
// (`--perfil`), e — SÓ com o veredicto `qualificado` de uma corrida completa do modo real — deixa
// pronto, numa pasta à parte, o registo de entrada de uso `tool` (fatia 0) e o relatório que ele
// cita. E PÁRA AÍ: quem assina é o dono, com o `aos-issuer route-profile-sign`, como para um
// candidato. Este ficheiro não tem por onde uma chave de assinar entre.
//
// As mesmas regras do `qualificar`: a conferência do registo é a do nó ([conferirPorAssinar], com o
// uso `tool` a levar o relatório pela leitura `ocr` de [routeentry.CheckReport]); o relatório é o
// que a corrida deste processo escreveu ([ocrFeito]); a pasta de saída é criada por esta corrida; e
// as barreiras 2 a 4 são as mesmas ([prepararPacoteDe]). A primeira barreira é a do veredicto: o
// relatório EM MEMÓRIA diz `qualificado`, do modo real, que vale para produção, com a corrida
// completa ([veredictoOCRPositivo]).
//
// O modo real é o motor `visao` (o modelo pago, pelo proxy do banco); o motor `falso` corre as
// mesmas barreiras sem pedido nenhum e nunca deixa pacote (saída 9 com `qualificado`).

const usoDoQualificarOCR = `aos-ensaio qualificar-ocr — mede o alias de uma tool de OCR e prepara o registo de entrada de uso tool para o dono assinar (AOS-530)

  aos-ensaio qualificar-ocr --motor visao --chaves F --visao-fornecedor kimi|anthropic|openrouter [--visao-modelo M]
                            --perfil F --board B --regiao R --saida PASTA [opcoes]
  aos-ensaio qualificar-ocr --motor falso [--falso certo|sem-acentos|falha-pdf|erra-factos]
                            --perfil F --board B --regiao R --saida PASTA

  --perfil FICHEIRO   o perfil do alias da tool: capacidade imagem, sem tools; o seu "requested" e o alias
                      que o no chama pela tool
  --board B           o board do no a que o registo se destina   (sem valor por omissao)
  --regiao R          a regiao desse no                          (sem valor por omissao)
  --saida PASTA       pasta que AINDA NAO EXISTE: e criada por esta corrida, e leva o relatorio, o resumo
                      e, so com veredicto positivo do modo real, pacote/
  --allowlist-dir PASTA --ancora-da-allowlist BASE64
                      a allowlist externa do no, se ele montar uma; sem elas confere-se a embebida
  opcoes: --limiar-de-factos F (de 0.90 a 1; so aperta)  --precos F  --reconstruir-contador
          --guardar-respostas

O registo e de uso tool, com fatia 0: um registo de uso tool nunca e candidato a servir runs. So um
qualificado do motor visao (o modo real), com a corrida completa, deixa um registo por assinar. O
comando NAO assina e nao le chave de assinar nenhuma: assina o dono, com o aos-issuer route-profile-sign.
Saidas: 0 pacote preparado; 7 recusado; 9 qualificado com o motor falso (nada para assinar);
        11 o no recusaria o registo; 4 parou a meio; 3 recusada antes de enviar; 5 em CI; 6 sem infra;
        2 argumentos; 1 outra falha (nunca fica pacote).
`

// opcoesDoQualificarOCR são as opções próprias do `qualificar-ocr`.
type opcoesDoQualificarOCR struct {
	motor, perfil, saida, board, regiao string
	limiar                              float64
	allowlistDir, ancoraDaAllowlist     string
}

// passamParaOOCR são as opções do `qualificar-ocr` que seguem para a corrida `ocr` tal como vieram.
var passamParaOOCR = []string{"falso", "chaves", "visao-fornecedor", "visao-modelo", "precos", "reconstruir-contador", "guardar-respostas"}

// novasFlagsDoQualificarOCR declara o conjunto FECHADO de opções do `qualificar-ocr`.
func novasFlagsDoQualificarOCR(o *opcoesDoQualificarOCR, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("aos-ensaio qualificar-ocr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usoDoQualificarOCR) }
	fs.StringVar(&o.motor, "motor", "", "")
	fs.StringVar(&o.perfil, "perfil", "", "")
	fs.StringVar(&o.saida, "saida", "", "")
	fs.StringVar(&o.board, "board", "", "")
	fs.StringVar(&o.regiao, "regiao", "", "")
	fs.Float64Var(&o.limiar, "limiar-de-factos", LimiarDeFactosDoOCR, "")
	fs.StringVar(&o.allowlistDir, "allowlist-dir", "", "")
	fs.StringVar(&o.ancoraDaAllowlist, "ancora-da-allowlist", "", "")
	fs.String("falso", "", "")
	fs.String("chaves", "", "")
	fs.String("visao-fornecedor", "", "")
	fs.String("visao-modelo", "", "")
	fs.String("precos", "", "")
	fs.Bool("reconstruir-contador", false, "")
	fs.Bool("guardar-respostas", false, "")
	return fs
}

// veredictoOCRPositivo é a PRIMEIRA barreira: a corrida saiu com 0, e o relatório em memória diz
// `qualificado`, do modo real, que vale para produção, com a corrida completa e a rota medida.
func veredictoOCRPositivo(codigo int, r *RelatorioOCR) bool {
	v := r.VeredictoDoOCR
	return codigo == SaidaOK && v != nil && r.Modo == ModoReal && !r.Interrompida && v.RotaMedida != nil &&
		v.Veredicto == VeredictoQualificado && v.ValeParaProducao
}

// qualificarOCR é o subcomando `qualificar-ocr`.
func qualificarOCR(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor) int {
	var o opcoesDoQualificarOCR
	fs := novasFlagsDoQualificarOCR(&o, stderr)
	if err := fs.Parse(args); err != nil {
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "aos-ensaio qualificar-ocr: argumento a mais: %q\n", fs.Arg(0))
		return SaidaUso
	}
	// O MOTOR DE VISÃO NUNCA CORRE EM CI, antes de ler seja o que for — como no `ocr`.
	if o.motor == NomeMotorVisao {
		for _, v := range variaveisDeCI {
			if amb.Getenv(v) != "" {
				fmt.Fprintf(stderr, "aos-ensaio qualificar-ocr: RECUSADO — o motor visao (um modelo real) nao corre em CI (a variavel %s esta definida)\n", v)
				return SaidaEmCI
			}
		}
	}
	dadas := map[string]string{}
	fs.Visit(func(f *flag.Flag) { dadas[f.Name] = f.Value.String() })
	uso := func(mensagem string) int {
		fmt.Fprintln(stderr, "aos-ensaio qualificar-ocr: "+mensagem)
		return SaidaUso
	}
	switch {
	case o.motor != NomeMotorVisao && o.motor != NomeMotorFalso:
		return uso("--motor tem de ser visao (o modo real) ou falso")
	case o.perfil == "" || o.saida == "":
		return uso("--perfil e --saida sao obrigatorios")
	case !caminhoImprimivel(o.saida):
		return uso("--saida nao pode ter aspas nem caracteres de controlo: o caminho e impresso na linha de assinar")
	case o.board == "" || o.regiao == "":
		return uso("--board e --regiao sao obrigatorios e nao tem valor por omissao: sao o board e a regiao do no a que o registo se destina, e ficam debaixo da assinatura")
	case !nomeDeAmbiente.MatchString(o.board) || !nomeDeAmbiente.MatchString(o.regiao):
		return uso(fmt.Sprintf("--board e --regiao tem, cada um, de 1 a %d letras, digitos, _ e - (sem espacos, aspas nem caracteres de controlo)", TamanhoMaximoDoAmbiente))
	case !(o.limiar >= LimiarDeFactosDoOCR) || o.limiar > 1:
		return uso(fmt.Sprintf("--limiar-de-factos tem de estar entre %.2f (o limiar do dono, D7) e 1: so aperta", LimiarDeFactosDoOCR))
	case (o.allowlistDir == "") != (o.ancoraDaAllowlist == ""):
		return uso("--allowlist-dir e --ancora-da-allowlist vao os dois, ou nenhum")
	}
	if _, dada := dadas["falso"]; dada && o.motor != NomeMotorFalso {
		return uso("--falso so vale com --motor falso")
	}

	// O REGISTO QUE O NÓ RECUSARIA NÃO SE MEDE: o provisório, com tudo o que já se sabe, passa pela
	// leitura do nó antes de qualquer pedido.
	perfil, err := lerPerfilDoOCR(o.perfil)
	if err != nil {
		return uso("--perfil: " + err.Error())
	}
	doc, err := os.ReadFile(o.perfil) // #nosec G304 -- caminho dado pelo operador na linha de comandos (ja lido acima)
	if err != nil {
		return uso("nao foi possivel ler o ficheiro de --perfil")
	}
	recusar := func(causa string) int {
		fmt.Fprintf(stderr, "aos-ensaio qualificar-ocr: RECUSADO ANTES DE ENVIAR — o no recusaria este registo no arranque: causa=%s. Nenhum pedido foi enviado e nada foi escrito\n", causa)
		return SaidaPacoteRecusado
	}
	base := registoPorAssinar{
		Uso: routeentry.UseTool, Board: o.board, Regiao: o.regiao, Perfil: doc, Fatia: 0,
		DigestDoPerfil: perfil.Digest(), DigestDoRelatorio: digestPorConhecer,
		DigestDaImagemDoProxy: routeentry.ProductionProxyImageDigest(),
	}
	provisorio, err := montarRegisto(base)
	if err != nil {
		return recusar(routeentry.CauseBadProfile)
	}
	registo, err := conferirPorAssinar(provisorio, nil, o.board, o.regiao)
	if err != nil {
		return recusar(routeentry.CauseOf(err))
	}
	alias := registo.Profile.Requested
	if nomeReservadoDoWindows.MatchString(alias) {
		return uso("o alias do perfil e um nome de dispositivo do Windows (CON, PRN, AUX, NUL, COM0 a COM9, LPT0 a LPT9): os ficheiros do pacote levam o alias no nome")
	}
	emVigor, qualAllowlist, err := allowlistEmVigor(o.allowlistDir, o.ancoraDaAllowlist)
	if err != nil {
		return uso("a allowlist nao se carrega (a pasta, a assinatura ou a ancora dela): nada foi conferido nem enviado")
	}
	if emVigor.Evaluate(allowlist.Input{Board: o.board, Model: alias, Region: o.regiao}) != allowlist.EffectAllow {
		fmt.Fprintf(stderr, "aos-ensaio qualificar-ocr: allowlist conferida: %s\n", qualAllowlist)
		return recusar(routeentry.CauseNameNotAllowlisted)
	}
	fmt.Fprintf(stderr, "aos-ensaio qualificar-ocr: PLANO — o registo de uso tool passa na leitura do no e a allowlist autoriza o alias %q para este board e esta regiao; allowlist conferida: %s\n", alias, qualAllowlist)

	// A PASTA DE SAÍDA É DESTA CORRIDA, criada de forma exclusiva.
	if _, serr := os.Lstat(o.saida); !errors.Is(serr, os.ErrNotExist) {
		fmt.Fprintln(stderr, "aos-ensaio qualificar-ocr: RECUSADO — a pasta de --saida ja existe: tem de ser uma pasta nova, criada por esta corrida")
		return SaidaRecusada
	}
	if err := os.MkdirAll(filepath.Dir(o.saida), 0o700); err != nil {
		return uso("nao foi possivel criar a pasta acima de --saida")
	}
	if err := os.Mkdir(o.saida, 0o700); err != nil {
		fmt.Fprintln(stderr, "aos-ensaio qualificar-ocr: RECUSADO — a pasta de --saida nao se conseguiu criar de novo (outra corrida criou-a entretanto?)")
		return SaidaRecusada
	}
	// `Remove` só apaga uma pasta VAZIA: uma corrida que não escreveu nada não a deixa para trás.
	defer func() { _ = os.Remove(o.saida) }()

	// A CORRIDA: a experiência `ocr`, pela linha de comandos de sempre, com o perfil e o limiar.
	corrida := []string{"--motor", o.motor, "--perfil", o.perfil, "--saida", o.saida, "--silencioso",
		"--limiar-de-factos", strconv.FormatFloat(o.limiar, 'f', -1, 64)}
	for _, nome := range passamParaOOCR {
		if v, dada := dadas[nome]; dada {
			corrida = append(corrida, "--"+nome+"="+v)
		}
	}
	var feito ocrFeito
	codigo := executarOCRCom(ctx, corrida, io.Discard, stderr, amb, red, &feito)
	r := feito.relatorio
	if r == nil || r.VeredictoDoOCR == nil {
		fmt.Fprintln(stderr, "aos-ensaio qualificar-ocr: NADA PARA ASSINAR — a corrida nao deixou relatorio; a causa esta acima")
		if codigo == SaidaOK {
			return SaidaErro
		}
		return codigo
	}
	v := r.VeredictoDoOCR
	motivo := fmt.Sprintf("veredicto %s, motor %s, modo %q", v.Veredicto, r.Motor, r.Modo)
	pacote := filepath.Join(o.saida, PastaDoPacote)
	preparado := false
	var noDisco routeentry.Entry
	switch {
	case codigo != SaidaOK:
		// Parou a meio, ou outra falha: o código da corrida diz qual.
		motivo = "a corrida nao completou (" + motivo + ")"
	case v.Veredicto != VeredictoQualificado:
		codigo = SaidaModeloRecusado
	case !veredictoOCRPositivo(codigo, r):
		// Qualificado, mas não do modo real: o motor falso prova o caminho, e não há o que assinar.
		codigo = SaidaQualificadoEmEnsaio
		motivo += " — " + v.PorqueNaoValeParaProducao
	default:
		var causa string
		var perr error
		noDisco, causa, perr = prepararPacoteDe(pacote, alias, base, relatorioEscrito{
			emJSON: feito.emJSON, digest: r.DigestDoRelatorio,
			digestDoPerfil: v.RotaMedida.DigestDoPerfil, digestDaImagemDoProxy: v.RotaMedida.DigestDaImagemDoProxy,
		}, o.board, o.regiao, amb.depoisDeEscreverOPacote)
		switch {
		case causa != "":
			motivo, codigo = "o no recusaria o pacote no arranque: causa="+causa, SaidaPacoteRecusado
		case perr != nil:
			fmt.Fprintf(stderr, "aos-ensaio qualificar-ocr: %v\n", perr)
			motivo, codigo = perr.Error(), SaidaErro
		default:
			preparado = true
		}
	}

	resumo := resumoDoQualificarOCR(r, alias, o.board, o.regiao, noDisco, preparado, motivo)
	caminhoDoResumo := filepath.Join(o.saida, NomeDoResumoDeUmaPagina)
	if err := os.WriteFile(caminhoDoResumo, []byte(resumo), 0o600); err != nil {
		if preparado {
			_ = os.RemoveAll(pacote)
		}
		fmt.Fprintln(stderr, "aos-ensaio qualificar-ocr: nao foi possivel escrever o resumo; nao ficou pacote nenhum")
		return SaidaErro
	}
	fmt.Fprint(stdout, resumo)
	fmt.Fprintf(stdout, "\nrelatorio: %s\nresumo:    %s\n", feito.emJSON, caminhoDoResumo)
	if !preparado {
		fmt.Fprintf(stderr, "aos-ensaio qualificar-ocr: NADA PARA ASSINAR — %s\n", motivo)
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
	return SaidaOK
}

// resumoDoQualificarOCR é o que o dono lê antes de assinar: o resumo da corrida e o registo de uso
// tool por assinar. Só identificadores, contagens e digests — nunca o texto lido.
func resumoDoQualificarOCR(r *RelatorioOCR, alias, board, regiao string, e routeentry.Entry, preparado bool, motivo string) string {
	var b strings.Builder
	b.WriteString(ResumoOCREmTexto(r))
	b.WriteString("\nREGISTO DE ENTRADA DE USO TOOL (AOS-530, ADR-042):\n")
	fmt.Fprintf(&b, "  alias %q, board %q, regiao %q, uso tool, fatia 0 — nunca e candidato a servir runs\n", alias, board, regiao)
	if !preparado {
		fmt.Fprintf(&b, "  NADA PARA ASSINAR: %s\n", motivo)
		return b.String()
	}
	origem := "ACRESCENTA-SE a tabela de perfis em codigo"
	if antes, tem := modelgateway.RouteProfileFor(alias); tem {
		origem = fmt.Sprintf("SUBSTITUI a entrada da tabela em codigo (modelo esperado de antes %q)", antes.ExpectedModel)
	}
	fmt.Fprintf(&b, "  PREPARADO, POR ASSINAR — %s; modelo esperado %q\n", origem, e.Profile.ExpectedModel)
	fmt.Fprintf(&b, "  digest do registo   %s\n  digest do perfil    %s\n  digest do relatorio %s\n", e.EntryDigest, e.ProfileDigest, e.ReportDigest)
	return b.String()
}
