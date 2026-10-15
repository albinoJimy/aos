package bancoensaio

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

Um motor novo entra como adaptador (ocr_motores.go). Sem veredicto: saida 0 se a corrida
completou; 2 argumentos; 3 recusada antes de enviar (chaves, tecto, contador); 4 interrompida;
5 motor visao em CI; 6 sem Docker, sem a imagem do motor ou sem o proxy.
`

type opcoesDoOCR struct {
	motor, falso, imagem, linguas, saida      string
	chaves, visaoFornecedor, visaoModelo      string
	precos                                    string
	silencioso, guardar, soPlano, reconstruir bool
}

// executarOCR corre o subcomando `ocr`.
func executarOCR(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor) int {
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
	if err := fs.Parse(args); err != nil {
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
	if o.motor != NomeMotorTesseract && o.imagem != "" {
		fmt.Fprintln(stderr, "aos-ensaio ocr: --imagem so vale com --motor tesseract")
		return SaidaUso
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
		visao, codigo = prepararVisao(ctx, o, bateria, amb, red, stderr)
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
	emJSON, emTexto, err := EscreverRelatorioOCR(pasta, r)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
		return SaidaErro
	}
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

// execucaoDaVisao é o que a corrida de visão preparou: o motor, o contador e o proxy.
type execucaoDaVisao struct {
	motor      *MotorOCRVisao
	contador   *Contador
	fornecedor Fornecedor
	modelo     string
	piorCaso   int64
	// pasta é a pasta dos relatórios quando não vem por --saida: a do dono, como no modo real.
	pasta      string
	fecharProx func()
}

func (v *execucaoDaVisao) fechar() {
	if v.fecharProx != nil {
		v.fecharProx()
	}
	v.contador.Fechar()
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
func prepararVisao(ctx context.Context, o opcoesDoOCR, b *BateriaOCR, amb Ambiente, red *Redactor, stderr io.Writer) (*execucaoDaVisao, int) {
	caminho := o.chaves
	if caminho == "" {
		caminho = amb.Getenv(EnvDasChaves)
	}
	if caminho == "" {
		fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — o motor visao exige o caminho do ficheiro de chaves (--chaves ou %s)\n", EnvDasChaves)
		return nil, SaidaRecusada
	}
	fornecedor, err := LerFornecedor(o.visaoFornecedor)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: --visao-fornecedor: %v\n", err)
		return nil, SaidaUso
	}
	chaves, err := LerChaves(caminho)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — %v\n", err)
		return nil, SaidaRecusada
	}
	// A lista de modelos do dono manda: um modelo fora dela é recusado por [Chaves.Rota].
	rota, err := chaves.Rota(fornecedor, o.visaoModelo, Destino{deTeste: amb.destinoDeTeste})
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — %v\n", err)
		return nil, SaidaRecusada
	}
	red.Acrescentar(rota.Segredos()...)
	var precos *TabelaDePrecos
	if o.precos != "" {
		if precos, err = LerTabelaDePrecos(o.precos); err != nil {
			fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — %v\n", err)
			return nil, SaidaRecusada
		}
	}
	pastaDoEstado := filepath.Dir(caminho)
	if amb.PastaDoDono != "" {
		pastaDoEstado = amb.PastaDoDono
	}
	caminhoDoContador := filepath.Join(pastaDoEstado, "contador.json")
	pastaDaSaida := o.saida
	if pastaDaSaida == "" {
		pastaDaSaida = filepath.Join(pastaDoEstado, "relatorios")
	}
	pastas := []string{filepath.Join(pastaDoEstado, "relatorios"), filepath.Join(filepath.Dir(caminho), "relatorios"), pastaDaSaida}
	if !verificarContadorDoDia(caminhoDoContador, pastas, []Fornecedor{fornecedor}, amb.Relogio().UTC().Format("2006-01-02"), o.reconstruir, stderr) {
		return nil, SaidaRecusada
	}
	// FAIL-CLOSED: sem tecto, sem preço para um tecto em dólares, ou com o contador em uso, não abre.
	contador, err := AbrirContador(caminhoDoContador, fornecedor, rota.Tectos, precos, rota.Modelo, amb.Relogio)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — %v\n", err)
		return nil, SaidaRecusada
	}
	v := &execucaoDaVisao{contador: contador, fornecedor: fornecedor, modelo: rota.Modelo, piorCaso: PiorCasoDaVisao(b), pasta: pastaDaSaida}
	if err := contador.Cabe(v.piorCaso); err != nil {
		contador.Fechar()
		fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — o pior caso (%d pedidos: %d imagens e 1 sonda) nao cabe: %v\n", v.piorCaso, v.piorCaso-1, err)
		return nil, SaidaRecusada
	}
	estado, err := contador.Estado()
	if err != nil {
		contador.Fechar()
		fmt.Fprintf(stderr, "aos-ensaio ocr: RECUSADO — %v\n", err)
		return nil, SaidaRecusada
	}
	fmt.Fprintf(stderr, "aos-ensaio ocr: motor VISAO — fornecedor %s, modelo %s; pior caso %d pedidos (%d imagens e 1 sonda; o PDF fica fora); hoje (%s UTC) restam %d de %d\n",
		fornecedor, rota.Modelo, v.piorCaso, v.piorCaso-1, estado.Dia, estado.PedidosRestantes, estado.TectoPedidos)
	fmt.Fprintf(stderr, "aos-ensaio ocr: DESTINO DA CHAVE: %s  (contador: %s)\n", rota.Destino, caminhoDoContador)
	if o.soPlano {
		return v, SaidaOK
	}
	if amb.Lancador == nil {
		contentores, redes, verr := VarrerOrfaos(ctx)
		if verr != nil {
			contador.Fechar()
			fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", verr)
			return nil, SaidaSemInfra
		}
		if contentores+redes > 0 {
			fmt.Fprintf(stderr, "aos-ensaio ocr: VARRIDOS %d contentor(es) e %d rede(s) aos512-* de uma corrida anterior\n", contentores, redes)
		}
	}
	pedido := PedidoDeProxy{Prefixo: prefixoDoProxy(fornecedor), Modelo: rota.Modelo, Alias: AliasDaVisao, Vida: VidaMaximaDoProxy}.ComSegredos(rota.apiKey, rota.apiBase)
	vivo, err := lancadorDe(amb, red).Lancar(ctx, pedido)
	if err != nil {
		contador.Fechar()
		fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
		return nil, SaidaSemInfra
	}
	red.Acrescentar(vivo.ChaveMestra())
	v.fecharProx = vivo.Fechar
	v.motor, err = NovoMotorOCRVisao(CfgDaVisao{
		BaseURL: vivo.BaseURL, Credencial: vivo.ChaveMestra(), Fornecedor: string(fornecedor), Modelo: rota.Modelo,
		Contador: contador, Preco: precoOpcional(precos, rota.Modelo),
	})
	if err != nil {
		v.fechar()
		fmt.Fprintf(stderr, "aos-ensaio ocr: %v\n", err)
		return nil, SaidaErro
	}
	return v, SaidaOK
}

// sondar faz a sonda da rota antes da primeira imagem: sem 200, a corrida não começa.
func (v *execucaoDaVisao) sondar(ctx context.Context, stderr io.Writer) int {
	s := v.motor.Sondar(ctx)
	if s.Resultado == SondaOK {
		return SaidaOK
	}
	fmt.Fprintf(stderr, "aos-ensaio ocr: a SONDA nao teve 200 (HTTP %d, %s): a corrida NAO COMECOU; gastou-se no maximo 1 pedido do tecto\n", s.HTTP, s.Resultado)
	if s.Resultado == SondaTecto || s.Resultado == SondaContador {
		return SaidaRecusada
	}
	return SaidaParouAMeio
}

// preencher põe no relatório o modo, a rota, os pedidos e o custo — com os nomes de campo das
// corridas reais, para o contador do dia se reconstruir também a partir dele.
func (v *execucaoDaVisao) preencher(r *RelatorioOCR, amb Ambiente) {
	r.Modo = ModoReal
	r.Rota = &RotaDoOCR{Fornecedor: string(v.fornecedor), Modelo: v.modelo}
	r.Pedidos = &PedidosDoOCR{PiorCaso: v.piorCaso, DiaUTC: amb.Relogio().UTC().Format("2006-01-02")}
	r.Custo = &CustoDoOCR{}
	if c, ok := v.motor.CustoMicroUSD(); ok {
		r.Custo.MicroUSDDaCorrida = &c
	}
	if e, err := v.contador.Estado(); err == nil {
		r.Pedidos.Enviados = e.EnviadosNestaCorrida
		if e.TemUSD {
			hoje := e.MicroUSDHoje
			r.Custo.MicroUSDHoje = &hoje
		}
	}
}
