package bancoensaio

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// O SUBCOMANDO `ocr` (AOS-529). Tem as suas opções e o seu texto de uso, como o `qualificar`:
// as corridas de sempre não mudam. Os motores `falso` e `tesseract` não usam chaves nem falam com
// fornecedor nenhum; o motor `visao` é um modelo pago, e segue as regras do modo real (ver
// ocr_visao.go): o ficheiro de chaves do dono, os tectos e o contador, nunca em CI.

const usoDoOCR = `aos-ensaio ocr — mede um motor de OCR com a bateria de imagens sinteticas (AOS-529). NAO toca em producao.

  aos-ensaio ocr bateria [--imagens PASTA]
        mostra o digest e as imagens da bateria; com --imagens, escreve-as na PASTA para as ver
  aos-ensaio ocr --motor falso [--falso certo|sem-acentos|falha-pdf|erra-factos] [opcoes]
        o motor falso: devolve o texto de origem, sem acentos, com factos errados, ou falha nos PDF
  aos-ensaio ocr --motor tesseract --imagem NOME@sha256:DIGEST|sha256:ID [--linguas por+eng] [opcoes]
        o Tesseract num contentor Docker fixado pelo conteudo (NOME@sha256:<64 hex> sem etiqueta, ou o
        ID sha256:<64 hex> de uma imagem construida no posto com packages/qa/banco-ensaio/ocr/Dockerfile),
        sem rede, 45 s, 1 GiB e 512 processos por chamada, raiz so de leitura; --pull never: o banco nao a
        descarrega
  aos-ensaio ocr --motor visao --chaves F --visao-fornecedor kimi|anthropic|openrouter [--visao-modelo M]
                 [--precos FICHEIRO] [--so-plano] [--reconstruir-contador] [opcoes]
        um MODELO DE VISAO pago, pelo proxy efemero do banco (Docker, imagem fixada do proxy), com a chave,
        os tectos, o contador e a lista de modelos do ficheiro do dono; um pedido por imagem (o PDF fica
        fora deste braco) e uma sonda; nunca em CI

opcoes:
  --saida PASTA          onde escrever o relatorio (omissao: a pasta relatorios do dono)
  --silencioso           nao mostra o resumo, so os caminhos
  --guardar-respostas    grava, ao lado do relatorio, o texto lido de cada imagem (so la)
  --limiar-de-factos F   o limiar do veredicto (AOS-530), de 0.90 (a omissao, D7 do dono) a 1; so aperta
  --perfil FICHEIRO      (so motores visao e falso) o perfil da rota do alias da tool, com a capacidade
                         imagem e sem tools: o digest dele, o modelo e a imagem do proxy vao para a rota
                         medida do veredicto, que liga o relatorio a um registo de entrada de uso tool
  --titular              (com --perfil) o perfil e o do MODELO DO RUN, com tools e imagem: a corrida e a
                         medida de imagem que um registo de uso run com imagem cita (AOS-547); quem a
                         corre e o qualificar, ao lado da qualificacao

O relatorio traz o VEREDICTO (AOS-530): qualificado ou recusado contra o limiar de factos, e se vale
para producao (so um qualificado do motor visao, que e o modo real). A saida NAO depende dele: 0 se a
corrida completou; 2 argumentos; 3 recusada antes de enviar (chaves, tecto, contador); 4 interrompida;
5 motor visao em CI; 6 sem Docker, sem a imagem do motor ou sem o proxy. Para preparar o registo de
uso tool a partir do veredicto: aos-ensaio qualificar-ocr. Um motor novo entra como adaptador
(ocr_motores.go).
`

type opcoesDoOCR struct {
	motor, falso, imagem, linguas, saida      string
	chaves, visaoFornecedor, visaoModelo      string
	precos, perfil                            string
	limiar                                    float64
	silencioso, guardar, soPlano, reconstruir bool
	// titular: o --perfil é o do MODELO DO RUN (AOS-547), com `tools` e `imagem`.
	titular bool
}

// ocrFeito é o que uma corrida `ocr` deixou escrito: o relatório, tal como foi gravado, e o
// caminho do JSON. Fica vazio quando a linha de comandos sai sem ter escrito relatório. É por aqui
// que o `qualificar-ocr` lê a corrida que acabou de fazer (AOS-530), e não um relatório de fora.
type ocrFeito struct {
	relatorio *RelatorioOCR
	emJSON    string
}

// ErrPerfilDoOCR — o perfil dado com `--perfil` não é o de uma rota que lê imagens.
var ErrPerfilDoOCR = errors.New("o perfil da rota medida pela experiencia ocr tem de declarar a capacidade " + modelgateway.CapabilityImage + " e nao pode declarar " + modelgateway.CapabilityTools + " (ADR-042 2.3 e 2.4: o modelo que le a imagem nao pode pedir efeitos)")

// ErrPerfilDoTitular — com `--titular`, o perfil não declara `imagem`.
var ErrPerfilDoTitular = errors.New("com --titular, o perfil e o do modelo do run e tem de declarar a capacidade " + modelgateway.CapabilityImage + " (AOS-547): e ela que o registo de uso run com a medida de imagem autoriza")

// lerPerfilDoTitular lê e confere o perfil do MODELO DO RUN para a medida de imagem (AOS-547): a
// leitura fechada do gateway e a capacidade `imagem`; `tools` é o que se espera dele.
func lerPerfilDoTitular(caminho string) (*modelgateway.RouteProfile, error) {
	doc, err := os.ReadFile(caminho) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		return nil, errors.New("nao foi possivel ler o ficheiro de --perfil")
	}
	p, err := LerPerfilCandidato(doc)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(p.Capabilities, modelgateway.CapabilityImage) {
		return nil, ErrPerfilDoTitular
	}
	return p, nil
}

// lerPerfilDoOCR lê e confere o perfil de `--perfil`: a leitura fechada do gateway, o nome da rota
// do banco, a capacidade `imagem` e nenhuma `tools`.
func lerPerfilDoOCR(caminho string) (*modelgateway.RouteProfile, error) {
	doc, err := os.ReadFile(caminho) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		return nil, errors.New("nao foi possivel ler o ficheiro de --perfil")
	}
	p, err := LerPerfilCandidato(doc)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(p.Capabilities, modelgateway.CapabilityImage) || slices.Contains(p.Capabilities, modelgateway.CapabilityTools) {
		return nil, ErrPerfilDoOCR
	}
	return p, nil
}

// executarOCR corre o subcomando `ocr`.
func executarOCR(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor) int {
	return executarOCRCom(ctx, args, stdout, stderr, amb, red, &ocrFeito{})
}

// executarOCRCom é o subcomando `ocr`, com o relatório escrito deixado em `feito`.
func executarOCRCom(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor, feito *ocrFeito) int {
	if len(args) > 0 && args[0] == "bateria" {
		return mostrarBateriaOCR(args[1:], stdout, stderr)
	}
	var o opcoesDoOCR
	fs := flag.NewFlagSet("aos-ensaio ocr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usoDoOCR) }
	fs.StringVar(&o.motor, "motor", "", "")
	fs.StringVar(&o.falso, "falso", FalsoOCRCerto, "")
	fs.StringVar(&o.imagem, "imagem", "", "")
	fs.StringVar(&o.linguas, "linguas", LinguasDoTesseract, "")
	fs.StringVar(&o.saida, "saida", "", "")
	fs.StringVar(&o.chaves, "chaves", "", "")
	fs.StringVar(&o.visaoFornecedor, "visao-fornecedor", "", "")
	fs.StringVar(&o.visaoModelo, "visao-modelo", "", "")
	fs.StringVar(&o.precos, "precos", "", "")
	fs.BoolVar(&o.silencioso, "silencioso", false, "")
	fs.BoolVar(&o.guardar, "guardar-respostas", false, "")
	fs.BoolVar(&o.soPlano, "so-plano", false, "")
	fs.BoolVar(&o.reconstruir, "reconstruir-contador", false, "")
	fs.StringVar(&o.perfil, "perfil", "", "")
	fs.BoolVar(&o.titular, "titular", false, "")
	fs.Float64Var(&o.limiar, "limiar-de-factos", LimiarDeFactosDoOCR, "")
	if err := fs.Parse(args); err != nil {
		return SaidaUso
	}
	// O LIMIAR SÓ APERTA: abaixo da D7 do dono o veredicto não seria o dele. (`!(x >= y)` apanha
	// também um NaN.)
	if !(o.limiar >= LimiarDeFactosDoOCR) || o.limiar > 1 {
		fmt.Fprintf(stderr, "aos-ensaio ocr: --limiar-de-factos tem de estar entre %.2f (o limiar do dono, D7) e 1: so aperta\n", LimiarDeFactosDoOCR)
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "aos-ensaio ocr: argumento a mais: %q\n", fs.Arg(0))
		return SaidaUso
	}
	// O MOTOR DE VISÃO NUNCA CORRE EM CI, como o modo real: a recusa vem antes de ler o que seja.
	if o.motor == NomeMotorVisao {
		for _, v := range variaveisDeCI {
			if amb.Getenv(v) != "" {
				fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — o motor visao (um modelo real) nao corre em CI (a variavel %s esta definida)\n", v)
				return SaidaEmCI
			}
		}
	}
	if o.motor != NomeMotorVisao && (o.chaves != "" || o.visaoFornecedor != "" || o.visaoModelo != "" || o.precos != "" || o.soPlano || o.reconstruir) {
		fmt.Fprintln(stderr, "aos-ensaio ocr: --chaves, --visao-fornecedor, --visao-modelo, --precos, --so-plano e --reconstruir-contador so valem com --motor visao")
		return SaidaUso
	}
	if o.titular && o.perfil == "" {
		fmt.Fprintln(stderr, "aos-ensaio ocr: --titular so vale com --perfil")
		return SaidaUso
	}
	if o.motor != NomeMotorTesseract && o.imagem != "" {
		fmt.Fprintln(stderr, "aos-ensaio ocr: --imagem so vale com --motor tesseract")
		return SaidaUso
	}
	var perfil *modelgateway.RouteProfile
	if o.perfil != "" {
		// Um registo de uso tool é de um alias que o nó chama pelo gateway: um modelo, real ou falso.
		// O Tesseract local não é uma rota do gateway.
		if o.motor != NomeMotorVisao && o.motor != NomeMotorFalso {
			fmt.Fprintln(stderr, "aos-ensaio ocr: --perfil so vale com --motor visao ou falso")
			return SaidaUso
		}
		var perr error
		ler := lerPerfilDoOCR
		if o.titular {
			ler = lerPerfilDoTitular
		}
		if perfil, perr = ler(o.perfil); perr != nil {
			fmt.Fprintf(stderr, "aos-ensaio ocr: --perfil: %v\n", perr)
			return SaidaUso
		}
	}
	bateria, err := CarregarBateriaOCR()
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
		return SaidaErro
	}
	pasta := o.saida
	if pasta == "" && amb.PastaDoDono != "" {
		pasta = filepath.Join(amb.PastaDoDono, "relatorios")
	}
	var (
		m     MotorOCR
		visao *execucaoDaVisao
	)
	switch o.motor {
	case NomeMotorFalso:
		f, ferr := NovoMotorOCRFalso(o.falso)
		if ferr != nil {
			fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", ferr)
			return SaidaUso
		}
		m = f
	case NomeMotorTesseract:
		if !imagemFixada.MatchString(o.imagem) || !linguasValidas.MatchString(o.linguas) {
			fmt.Fprintln(stderr, "aos-ensaio ocr: --motor tesseract exige --imagem NOME@sha256:<64 hex> (sem etiqueta) ou o ID sha256:<64 hex>, e --linguas como por+eng")
			return SaidaUso
		}
		t, terr := NovoMotorOCRTesseract(ctx, o.imagem, o.linguas, amb.dockerDeTeste)
		if terr != nil {
			fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", terr)
			return SaidaSemInfra
		}
		m = t
	case NomeMotorVisao:
		var codigo int
		visao, codigo = prepararVisao(ctx, o, bateria, perfil, amb, red, stderr)
		if visao == nil {
			return codigo
		}
		defer visao.fechar()
		if pasta == "" {
			pasta = visao.pasta
		}
		if o.soPlano {
			fmt.Fprintln(stderr, "aos-ensaio ocr: --so-plano: nenhum pedido foi enviado")
			return SaidaOK
		}
		if codigo := visao.sondar(ctx, stderr); codigo != SaidaOK {
			return codigo
		}
		m = visao.motor
	default:
		fmt.Fprintf(stderr, "aos-ensaio ocr: --motor tem de ser um de: %s\n", strings.Join(MotoresDeOCR, ", "))
		return SaidaUso
	}
	if pasta == "" {
		fmt.Fprintln(stderr, "aos-ensaio ocr: sem pasta do dono conhecida: indique --saida PASTA")
		return SaidaUso
	}
	textos := map[string]string{}
	var guardar func(string, string)
	if o.guardar {
		guardar = func(id, texto string) { textos[id] = texto }
	}
	r := CorrerExperienciaOCR(ctx, bateria, m, amb.Relogio, guardar)
	if visao != nil {
		visao.preencher(r, amb)
	}
	// O VEREDICTO (AOS-530), depois do modo: é ele que diz se vale para produção.
	var rota *RotaMedidaDoOCR
	if perfil != nil {
		rota = &RotaMedidaDoOCR{Fornecedor: NomeMotorFalso, Modelo: m.Nome(), ModeloEsperado: perfil.ExpectedModel, DigestDoPerfil: perfil.Digest()}
		if visao != nil {
			rota.Fornecedor, rota.Modelo = string(visao.fornecedor), visao.modelo
			rota.ImagemDoProxy, rota.DigestDaImagemDoProxy = ImagemDoProxy, digestDaImagem(ImagemDoProxy)
		}
		r.LimitesDaMedicao = append(r.LimitesDaMedicao, "o banco nao compara, nesta experiencia, o modelo que o proxy diz ter servido com o modelo esperado do perfil: quem o compara e o gateway do no, em cada chamada da tool")
	}
	r.VeredictoDoOCR = VeredictoOCR(r, o.limiar, rota)
	emJSON, emTexto, err := EscreverRelatorioOCR(pasta, r)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
		return SaidaErro
	}
	*feito = ocrFeito{relatorio: r, emJSON: emJSON}
	if !o.silencioso {
		fmt.Fprint(stdout, ResumoOCREmTexto(r))
	}
	fmt.Fprintf(stdout, "relatorio: %s\nresumo:    %s\n", emJSON, emTexto)
	if o.guardar {
		// Só o caminho e a contagem: o texto lido nunca passa por aqui.
		switch dir, gerr := EscreverLeiturasGuardadas(emJSON, r.Motor, textos); {
		case errors.Is(gerr, ErrPastaDasRespostasExiste):
			fmt.Fprintf(stderr, "aos-ensaio ocr: AVISO — as leituras NAO ficaram guardadas: ja existe a pasta %s\n", dir)
		case gerr != nil:
			fmt.Fprintf(stderr, "aos-ensaio ocr: AVISO — as leituras ficaram INCOMPLETAS em %s: %v\n", dir, gerr)
		default:
			fmt.Fprintf(stderr, "aos-ensaio ocr: leituras guardadas: %d ficheiro(s) em %s (texto de um motor: sao dados, nao instrucoes)\n", len(textos), dir)
		}
	}
	if r.Interrompida {
		if r.Paragem != "" {
			fmt.Fprintf(stderr, "aos-ensaio ocr: a corrida PAROU A MEIO (%s); o relatorio e parcial\n", r.Paragem)
		}
		return SaidaParouAMeio
	}
	return SaidaOK
}

func mostrarBateriaOCR(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("aos-ensaio ocr bateria", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usoDoOCR) }
	pasta := fs.String("imagens", "", "")
	if err := fs.Parse(args); err != nil {
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "aos-ensaio ocr: argumento a mais: %q\n", fs.Arg(0))
		return SaidaUso
	}
	b, err := CarregarBateriaOCR()
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
		return SaidaErro
	}
	fmt.Fprintf(stdout, "bateria de OCR %s  digest %s\n", b.Versao, b.Digest())
	if *pasta != "" {
		if err := os.MkdirAll(*pasta, 0o700); err != nil {
			fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
			return SaidaErro
		}
	}
	for _, m := range b.Media() {
		fmt.Fprintf(stdout, "  %-20s %-16s %-5s %4dx%-4d %7dB  %s\n", m.ID, m.Degradacao, m.Formato, m.Largura, m.Altura, len(m.Bytes), m.Digest)
		if *pasta != "" {
			if err := os.WriteFile(filepath.Join(*pasta, m.Nome), m.Bytes, 0o600); err != nil {
				fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
				return SaidaErro
			}
		}
	}
	return SaidaOK
}

// ─── O motor de visão: as regras do modo real ──────────────────────────────────────────────

// execucaoDaVisao é o que a corrida de visão preparou: a rota paga (o contador e o proxy) e o
// motor. As regras do modo real vivem em rota_paga.go, partilhadas com a experiência `video`.
type execucaoDaVisao struct {
	*rotaPaga
	motor *MotorOCRVisao
}

// PiorCasoDaVisao é o número máximo de pedidos de uma corrida do motor de visão: um por imagem que
// o modelo recebe (o PDF fica fora) e a sonda.
func PiorCasoDaVisao(b *BateriaOCR) int64 {
	n := int64(1)
	for _, im := range b.Imagens {
		if _, ok := tipoDeMedia(im.Formato); ok {
			n++
		}
	}
	return n
}

// prepararVisao lê as chaves, abre o contador, confere o pior caso e levanta o proxy. Devolve nil e
// o código de saída quando recusa; com `--so-plano` não levanta o proxy.
//
// `perfil` é o de `--perfil` (nil sem ele). Com ele a corrida QUALIFICA um perfil (AOS-530): a rota
// desta corrida tem de ser a que o perfil espera, recusado ANTES de abrir o contador ou o proxy; e
// conta nas qualificações do dia (D7), conferidas antes do proxy e escritas quando a sonda passa.
func prepararVisao(ctx context.Context, o opcoesDoOCR, b *BateriaOCR, perfil *modelgateway.RouteProfile, amb Ambiente, red *Redactor, stderr io.Writer) (*execucaoDaVisao, int) {
	const prefixo = "aos-ensaio ocr"
	rp, codigo := planearRotaPaga(planoDaRotaPaga{
		prefixo: prefixo, chaves: o.chaves, fornecedor: o.visaoFornecedor, modelo: o.visaoModelo, precos: o.precos, saida: o.saida,
		reconstruir: o.reconstruir, perfil: perfil, piorCaso: PiorCasoDaVisao(b), unidade: "imagens", nota: "; o PDF fica fora",
		ticketDoPerfil: "AOS-530", alias: AliasDaVisao,
	}, amb, red, stderr)
	if rp == nil {
		return nil, codigo
	}
	v := &execucaoDaVisao{rotaPaga: rp}
	if o.soPlano {
		return v, SaidaOK
	}
	if codigo := rp.levantar(ctx, prefixo, amb, red, stderr); codigo != SaidaOK {
		return nil, codigo
	}
	var err error
	v.motor, err = NovoMotorOCRVisao(CfgDaVisao{
		BaseURL: rp.base, Credencial: rp.credencial, Fornecedor: string(rp.fornecedor), Modelo: rp.modelo,
		Contador: rp.contador, Preco: precoOpcional(rp.precos, rp.modelo),
	})
	if err != nil {
		v.fechar()
		fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
		return nil, SaidaErro
	}
	return v, SaidaOK
}

// sondar faz a sonda da rota antes da primeira imagem: sem 200, a corrida não começa.
func (v *execucaoDaVisao) sondar(ctx context.Context, stderr io.Writer) int {
	return v.depoisDaSonda(v.motor.Sondar(ctx), "aos-ensaio ocr", stderr)
}

// preencher põe no relatório o modo, a rota, os pedidos e o custo — com os nomes de campo das
// corridas reais, para o contador do dia se reconstruir também a partir dele.
func (v *execucaoDaVisao) preencher(r *RelatorioOCR, amb Ambiente) {
	r.Modo = ModoReal
	r.Rota, r.Pedidos, r.Custo = v.uso(amb, v.motor.CustoMicroUSD)
}
