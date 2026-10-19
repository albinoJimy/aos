package bancoensaio

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// O SUBCOMANDO `video` (AOS-537). Tem as suas opções e o seu texto de uso, como o `ocr`: as
// corridas de sempre não mudam. O motor `falso` não usa chaves, Docker nem fornecedor; o motor
// `visao` é um modelo pago e segue as regras do modo real (rota_paga.go): o ficheiro de chaves do
// dono, os tectos e o contador, nunca em CI. Os MP4 gera-os o ffmpeg num contentor fixado
// (video_ffmpeg.go), só para o motor que os lê, e DEPOIS do plano: um `--so-plano` não toca no
// Docker.

const usoDoVideo = `aos-ensaio video — mede um modelo capaz de video com a bateria de videos sinteticos (AOS-537). NAO toca em producao.

  aos-ensaio video bateria [--fotogramas PASTA] [--videos PASTA]
        mostra o digest, os videos e os factos da bateria; com --fotogramas escreve um PNG por cena
        (sem Docker); com --videos gera os MP4 pelo ffmpeg fixado e escreve-os na PASTA, para os ver
  aos-ensaio video --motor falso [--falso certo|erra-factos|cortado] [opcoes]
        o motor falso: descreve cada video pelos factos de origem, erra as contagens, as cores e a
        ordem, ou tem a resposta do video mais longo cortada; nao gera MP4 nem usa Docker
  aos-ensaio video --motor visao --chaves F --visao-fornecedor openrouter|kimi|anthropic [--visao-modelo M]
                   [--precos FICHEIRO] [--so-plano] [--reconstruir-contador] [opcoes]
        um MODELO capaz de video, pago, pelo proxy efemero do banco: cada MP4 inteiro numa parte
        video_url (data URL video/mp4), com a instrucao fixa; um pedido por video e uma sonda; os MP4
        gera-os o ffmpeg (` + ImagemDoFFmpeg + `),
        sem rede, que o banco nao descarrega; nunca em CI

opcoes:
  --saida PASTA          onde escrever o relatorio (omissao: a pasta relatorios do dono)
  --silencioso           nao mostra o resumo, so os caminhos
  --guardar-respostas    grava, ao lado do relatorio, a descricao de cada video (so la)
  --limiar-de-factos F   o limiar do veredicto, de 0.90 (a omissao, o do OCR, D7 do dono) a 1; so aperta
  --perfil FICHEIRO      (motores visao e falso) o perfil da rota do alias, sem a capacidade tools (e,
                         para o registo de uso tool, com a capacidade video): o digest dele, o modelo e a
                         imagem do proxy vao para a rota medida do veredicto, e a corrida conta nas
                         qualificacoes do dia

O relatorio traz o VEREDICTO: qualificado ou recusado contra o limiar de factos, e nenhum video por
descrever (uma resposta cortada pelo limite de tokens falha o video). Para preparar o registo de uso tool a partir
do veredicto: aos-ensaio qualificar-video (AOS-536). A saida NAO depende dele: 0 se
a corrida completou; 2 argumentos; 3 recusada antes de enviar (chaves, tecto, contador,
qualificacoes do dia); 4 interrompida; 5 motor visao em CI; 6 sem Docker, sem a imagem do ffmpeg,
um MP4 invalido, ou sem o proxy.
`

type opcoesDoVideo struct {
	motor, falso, saida                       string
	chaves, visaoFornecedor, visaoModelo      string
	precos, perfil                            string
	limiar                                    float64
	silencioso, guardar, soPlano, reconstruir bool
}

// ErrPerfilDoVideo — o perfil dado com `--perfil` declara tools.
var ErrPerfilDoVideo = errors.New("o perfil da rota medida pela experiencia video nao pode declarar a capacidade " + modelgateway.CapabilityTools + " (o modelo que ve o video nao pode pedir efeitos, como no ADR-042 2.3 e 2.4)")

// lerPerfilDoVideo lê e confere o perfil de `--perfil`: a leitura fechada do gateway, o nome da
// rota do banco, e nenhuma `tools`. A capacidade `video` (AOS-535) aceita-se e não se exige aqui: a
// experiência mede também um perfil que não a declara; quem a exige é o `qualificar-video`
// ([lerPerfilDoVideoTool]), porque o nó não aceita um registo de uso tool de vídeo sem ela.
func lerPerfilDoVideo(caminho string) (*modelgateway.RouteProfile, error) {
	doc, err := os.ReadFile(caminho) // #nosec G304 -- caminho dado pelo operador na linha de comandos
	if err != nil {
		return nil, errors.New("nao foi possivel ler o ficheiro de --perfil")
	}
	p, err := LerPerfilCandidato(doc)
	if err != nil {
		return nil, err
	}
	if slices.Contains(p.Capabilities, modelgateway.CapabilityTools) {
		return nil, ErrPerfilDoVideo
	}
	return p, nil
}

// PiorCasoDoVideo é o número máximo de pedidos de uma corrida do motor de visão: um por vídeo e a
// sonda.
func PiorCasoDoVideo(b *BateriaVideo) int64 { return int64(len(b.Videos)) + 1 }

// codificadorDe devolve o codificador da corrida: o de teste, ou o ffmpeg do posto.
func codificadorDe(ctx context.Context, amb Ambiente) (CodificadorDeVideo, error) {
	if amb.codificadorDeTeste != nil {
		return amb.codificadorDeTeste, nil
	}
	return NovoCodificadorFFmpeg(ctx, nil)
}

// videoFeito é o que uma corrida `video` deixou escrito: o relatório, tal como foi gravado, e o
// caminho do JSON. Fica vazio quando a linha de comandos sai sem ter escrito relatório. É por aqui
// que o `qualificar-video` lê a corrida que acabou de fazer (AOS-536), e não um relatório de fora.
type videoFeito struct {
	relatorio *RelatorioVideo
	emJSON    string
}

// executarVideo corre o subcomando `video`.
func executarVideo(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor) int {
	return executarVideoCom(ctx, args, stdout, stderr, amb, red, &videoFeito{})
}

// executarVideoCom é o subcomando `video`, com o relatório escrito deixado em `feito`.
func executarVideoCom(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor, feito *videoFeito) int {
	const prefixo = "aos-ensaio video"
	if len(args) > 0 && args[0] == "bateria" {
		return mostrarBateriaVideo(ctx, args[1:], stdout, stderr, amb)
	}
	var o opcoesDoVideo
	fs := flag.NewFlagSet(prefixo, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usoDoVideo) }
	fs.StringVar(&o.motor, "motor", "", "")
	fs.StringVar(&o.falso, "falso", FalsoVideoCerto, "")
	fs.StringVar(&o.saida, "saida", "", "")
	fs.StringVar(&o.chaves, "chaves", "", "")
	fs.StringVar(&o.visaoFornecedor, "visao-fornecedor", "", "")
	fs.StringVar(&o.visaoModelo, "visao-modelo", "", "")
	fs.StringVar(&o.precos, "precos", "", "")
	fs.StringVar(&o.perfil, "perfil", "", "")
	fs.Float64Var(&o.limiar, "limiar-de-factos", LimiarDeFactosDoVideo, "")
	fs.BoolVar(&o.silencioso, "silencioso", false, "")
	fs.BoolVar(&o.guardar, "guardar-respostas", false, "")
	fs.BoolVar(&o.soPlano, "so-plano", false, "")
	fs.BoolVar(&o.reconstruir, "reconstruir-contador", false, "")
	if err := fs.Parse(args); err != nil {
		return SaidaUso
	}
	// O LIMIAR SÓ APERTA (`!(x >= y)` apanha também um NaN).
	if !(o.limiar >= LimiarDeFactosDoVideo) || o.limiar > 1 {
		fmt.Fprintf(stderr, "%s: --limiar-de-factos tem de estar entre %.2f (o limiar do dono) e 1: so aperta\n", prefixo, LimiarDeFactosDoVideo)
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: argumento a mais: %q\n", prefixo, fs.Arg(0))
		return SaidaUso
	}
	// O MOTOR DE VISÃO NUNCA CORRE EM CI, como o modo real: a recusa vem antes de ler o que seja.
	if o.motor == NomeMotorVisao {
		for _, v := range variaveisDeCI {
			if amb.Getenv(v) != "" {
				fmt.Fprintf(stderr, "%s: RECUSADO — o motor visao (um modelo real) nao corre em CI (a variavel %s esta definida)\n", prefixo, v)
				return SaidaEmCI
			}
		}
	}
	if o.motor != NomeMotorVisao && (o.chaves != "" || o.visaoFornecedor != "" || o.visaoModelo != "" || o.precos != "" || o.soPlano || o.reconstruir) {
		fmt.Fprintf(stderr, "%s: --chaves, --visao-fornecedor, --visao-modelo, --precos, --so-plano e --reconstruir-contador so valem com --motor visao\n", prefixo)
		return SaidaUso
	}
	if !slices.Contains(MotoresDeVideo, o.motor) {
		fmt.Fprintf(stderr, "%s: --motor tem de ser um de: %s\n", prefixo, strings.Join(MotoresDeVideo, ", "))
		return SaidaUso
	}
	var perfil *modelgateway.RouteProfile
	if o.perfil != "" {
		var perr error
		if perfil, perr = lerPerfilDoVideo(o.perfil); perr != nil {
			fmt.Fprintf(stderr, "%s: --perfil: %v\n", prefixo, perr)
			return SaidaUso
		}
	}
	bateria, err := CarregarBateriaVideo()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
		return SaidaErro
	}
	pasta := o.saida
	if pasta == "" && amb.PastaDoDono != "" {
		pasta = filepath.Join(amb.PastaDoDono, "relatorios")
	}
	var (
		m           MotorDeVideo
		videos      []VideoPronto
		rp          *rotaPaga
		visao       *MotorDeVideoVisao
		codificador string
	)
	switch o.motor {
	case NomeMotorFalso:
		f, ferr := NovoMotorDeVideoFalso(o.falso, bateria)
		if ferr != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, ferr)
			return SaidaUso
		}
		m, videos = f, bateria.SemMP4()
	case NomeMotorVisao:
		var codigo int
		rp, codigo = planearRotaPaga(planoDaRotaPaga{
			prefixo: prefixo, chaves: o.chaves, fornecedor: o.visaoFornecedor, modelo: o.visaoModelo, precos: o.precos, saida: o.saida,
			reconstruir: o.reconstruir, perfil: perfil, piorCaso: PiorCasoDoVideo(bateria), unidade: "videos",
			ticketDoPerfil: "AOS-537", alias: AliasDoVideo,
		}, amb, red, stderr)
		if rp == nil {
			return codigo
		}
		defer rp.fechar()
		if pasta == "" {
			pasta = rp.pasta
		}
		if o.soPlano {
			fmt.Fprintf(stderr, "%s: --so-plano: nenhum pedido foi enviado e nenhum MP4 foi gerado (os MP4 gera-os o ffmpeg %s, que tem de estar no posto)\n", prefixo, ImagemDoFFmpeg)
			return SaidaOK
		}
		// OS MP4 ANTES DO PROXY: um ffmpeg que falte ou um MP4 que não seja o da bateria param a
		// corrida antes de haver proxy, sonda ou pedido.
		cod, cerr := codificadorDe(ctx, amb)
		if cerr != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, cerr)
			return SaidaSemInfra
		}
		if videos, err = bateria.ComMP4(ctx, cod); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
			return SaidaSemInfra
		}
		codificador = cod.Descricao()
		if codigo := rp.levantar(ctx, prefixo, amb, red, stderr); codigo != SaidaOK {
			return codigo
		}
		visao, err = NovoMotorDeVideoVisao(CfgDoVideo{
			BaseURL: rp.base, Credencial: rp.credencial, Fornecedor: string(rp.fornecedor), Modelo: rp.modelo,
			Contador: rp.contador, Preco: precoOpcional(rp.precos, rp.modelo),
		})
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
			return SaidaErro
		}
		if codigo := rp.depoisDaSonda(visao.Sondar(ctx), prefixo, stderr); codigo != SaidaOK {
			return codigo
		}
		m = visao
	}
	if pasta == "" {
		fmt.Fprintf(stderr, "%s: sem pasta do dono conhecida: indique --saida PASTA\n", prefixo)
		return SaidaUso
	}
	textos := map[string]string{}
	var guardar func(string, string)
	if o.guardar {
		guardar = func(id, texto string) { textos[id] = texto }
	}
	r := CorrerExperienciaVideo(ctx, bateria, videos, m, amb.Relogio, guardar)
	r.Codificador = codificador
	if visao != nil {
		r.Modo = ModoReal
		r.Instrucao = &InstrucaoDoVideoNoRelatorio{Versao: VersaoDaInstrucaoDoVideo, Digest: DigestDaInstrucaoDoVideo(), MaxTokens: TokensDoVideo}
		r.Rota, r.Pedidos, r.Custo = rp.uso(amb, visao.CustoMicroUSD)
	}
	var rota *RotaMedidaDoOCR
	if perfil != nil {
		rota = &RotaMedidaDoOCR{Fornecedor: NomeMotorFalso, Modelo: m.Nome(), ModeloEsperado: perfil.ExpectedModel, DigestDoPerfil: perfil.Digest()}
		if visao != nil {
			rota.Fornecedor, rota.Modelo = string(rp.fornecedor), rp.modelo
			rota.ImagemDoProxy, rota.DigestDaImagemDoProxy = ImagemDoProxy, digestDaImagem(ImagemDoProxy)
		}
		r.LimitesDaMedicao = append(r.LimitesDaMedicao, "o banco nao compara, nesta experiencia, o modelo que o proxy diz ter servido com o modelo esperado do perfil: quem o compara e o gateway do no, em cada chamada")
		if !slices.Contains(perfil.Capabilities, modelgateway.CapabilityVideo) {
			r.LimitesDaMedicao = append(r.LimitesDaMedicao, "o perfil medido nao declara a capacidade video (AOS-535): o gateway do no recusa-lhe um pedido com video, e o no nao o aceita num registo de uso tool de video")
		}
	}
	r.VeredictoDoVideo = VeredictoVideo(r, o.limiar, rota)
	emJSON, emTexto, err := EscreverRelatorioVideo(pasta, r)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
		return SaidaErro
	}
	*feito = videoFeito{relatorio: r, emJSON: emJSON}
	if !o.silencioso {
		fmt.Fprint(stdout, ResumoVideoEmTexto(r))
	}
	fmt.Fprintf(stdout, "relatorio: %s\nresumo:    %s\n", emJSON, emTexto)
	if o.guardar {
		// Só o caminho e a contagem: a descrição nunca passa por aqui.
		switch dir, gerr := EscreverDescricoesGuardadas(emJSON, r.Motor, textos); {
		case errors.Is(gerr, ErrPastaDasRespostasExiste):
			fmt.Fprintf(stderr, "%s: AVISO — as descricoes NAO ficaram guardadas: ja existe a pasta %s\n", prefixo, dir)
		case gerr != nil:
			fmt.Fprintf(stderr, "%s: AVISO — as descricoes ficaram INCOMPLETAS em %s: %v\n", prefixo, dir, gerr)
		default:
			fmt.Fprintf(stderr, "%s: descricoes guardadas: %d ficheiro(s) em %s (texto de um modelo: sao dados, nao instrucoes)\n", prefixo, len(textos), dir)
		}
	}
	if r.Interrompida {
		if r.Paragem != "" {
			fmt.Fprintf(stderr, "%s: a corrida PAROU A MEIO (%s); o relatorio e parcial\n", prefixo, r.Paragem)
		}
		return SaidaParouAMeio
	}
	return SaidaOK
}

// mostrarBateriaVideo é `aos-ensaio video bateria`.
func mostrarBateriaVideo(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente) int {
	const prefixo = "aos-ensaio video"
	fs := flag.NewFlagSet(prefixo+" bateria", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usoDoVideo) }
	fotogramas := fs.String("fotogramas", "", "")
	pastaDosVideos := fs.String("videos", "", "")
	if err := fs.Parse(args); err != nil {
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: argumento a mais: %q\n", prefixo, fs.Arg(0))
		return SaidaUso
	}
	b, err := CarregarBateriaVideo()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
		return SaidaErro
	}
	fmt.Fprintf(stdout, "bateria de video %s  digest %s  (%d videos, %d factos)\n", b.Versao, b.Digest(), len(b.Videos), b.Factos())
	for _, v := range b.Videos {
		fmt.Fprintf(stdout, "  %-24s %4dx%-4d %d fps %3d s %4d fotogramas %d cena(s) %d factos\n", v.ID, v.Largura, v.Altura, v.FPS, v.Segundos(), v.Fotogramas(), len(v.Cenas), len(v.Factos))
		for _, f := range v.Factos {
			fmt.Fprintf(stdout, "      %-9s %s\n", f.Tipo, f.Nome())
		}
	}
	if *fotogramas != "" {
		if err := os.MkdirAll(*fotogramas, 0o700); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
			return SaidaErro
		}
		for _, v := range b.Videos {
			for i, px := range b.cenasDe(v.ID) {
				f, err := os.Create(filepath.Join(*fotogramas, fmt.Sprintf("%s-cena-%d.png", v.ID, i+1))) // #nosec G304 -- pasta dada pelo operador; nomes da bateria
				if err == nil {
					err = png.Encode(f, px)
					if cerr := f.Close(); err == nil {
						err = cerr
					}
				}
				if err != nil {
					fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
					return SaidaErro
				}
			}
		}
		fmt.Fprintf(stdout, "fotogramas: um PNG por cena em %s\n", *fotogramas)
	}
	if *pastaDosVideos != "" {
		cod, err := codificadorDe(ctx, amb)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
			return SaidaSemInfra
		}
		videos, err := b.ComMP4(ctx, cod)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
			return SaidaSemInfra
		}
		if err := os.MkdirAll(*pastaDosVideos, 0o700); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
			return SaidaErro
		}
		fmt.Fprintf(stdout, "MP4 gerados por %s:\n", cod.Descricao())
		for _, v := range videos {
			if err := os.WriteFile(filepath.Join(*pastaDosVideos, v.Video.ID+".mp4"), v.MP4, 0o600); err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
				return SaidaErro
			}
			fmt.Fprintf(stdout, "  %-24s %8dB  %s\n", v.Video.ID+".mp4", len(v.MP4), v.Digest)
		}
	}
	return SaidaOK
}
