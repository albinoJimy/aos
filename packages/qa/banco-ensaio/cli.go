package bancoensaio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// A LINHA DE COMANDOS do banco (`aos-ensaio`). Vive aqui, na biblioteca, para os testes a
// correrem inteira — com as saídas capturadas — sem lançar um processo.

// Os códigos de saída. Cada causa de paragem tem o seu.
const (
	// SaidaOK — a corrida completou e o relatório foi escrito.
	SaidaOK = 0
	// SaidaErro — falha que não é nenhuma das abaixo.
	SaidaErro = 1
	// SaidaUso — argumentos inválidos.
	SaidaUso = 2
	// SaidaRecusada — a corrida foi recusada antes de arrancar: ficheiro de chaves, tecto,
	// preço ou contador. Nenhum pedido saiu.
	SaidaRecusada = 3
	// SaidaParouAMeio — a corrida parou a meio (tecto atingido, contador, interrupção, chave
	// recusada, conta sem saldo, limite de ritmo, rota indisponível) ou a sonda não a deixou começar. O relatório
	// parcial foi escrito e diz a causa.
	SaidaParouAMeio = 4
	// SaidaEmCI — o modo com modelo real foi pedido em CI. Recusado, sem ler nada.
	SaidaEmCI = 5
	// SaidaSemInfra — falta o Docker ou a imagem do proxy, ou o proxy não arrancou.
	SaidaSemInfra = 6
	// SaidaModeloRecusado — a experiência `qualificacao` deu o veredicto `recusado` (AOS-518).
	SaidaModeloRecusado = 7
	// SaidaInconclusivo — a experiência `qualificacao` correu até ao fim e deu `inconclusivo`.
	// (Uma qualificação que parou a meio sai com [SaidaParouAMeio]; uma que não coube no tecto,
	// com [SaidaRecusada]. Só `qualificado` sai com [SaidaOK].)
	SaidaInconclusivo = 8
	// SaidaQualificadoEmEnsaio — `qualificado` nos modos `falso` ou `proxy`: o fornecedor é um
	// provider falso e o veredicto NÃO vale para produção. Nunca é [SaidaOK], para que um
	// encadeamento que só olhe para o código não confunda um ensaio com uma qualificação.
	SaidaQualificadoEmEnsaio = 9
	// SaidaSoPlano — `real --experiencia qualificacao --so-plano`: o plano é válido e nada foi
	// enviado. Não é [SaidaOK], que na qualificação quer dizer `qualificado`.
	SaidaSoPlano = 10
)

// EnvDasChaves é a variável de ambiente com o caminho do ficheiro de chaves, quando não vem
// pela flag `--chaves`. Leva um CAMINHO, nunca uma chave.
const EnvDasChaves = "AOS_ENSAIO_CHAVES"

// variaveisDeCI são as variáveis cuja presença diz que o processo corre em CI.
var variaveisDeCI = []string{"CI", "GITHUB_ACTIONS"}

// Ambiente é o que a linha de comandos recebe de fora: é por aqui que os testes a isolam.
type Ambiente struct {
	// Getenv lê uma variável de ambiente.
	Getenv func(string) string
	// PastaDoDono é a pasta do dono fora do repositório (`~/.aos-ensaio`). Vazia ⇒ os
	// caminhos do contador e dos relatórios têm de vir por flag.
	PastaDoDono string
	// Lancador levanta o proxy efémero. nil ⇒ [LancadorDocker].
	Lancador LancadorDeProxy
	// Relogio dá a hora. nil ⇒ time.Now.
	Relogio func() time.Time

	// destinoDeTeste é uma base da API aceite sem validação de esquema nem de host. Só os
	// testes do próprio pacote a definem (o campo não é exportado): não há flag nem variável
	// de ambiente que cá chegue, e por isso um operador não a consegue usar por engano.
	destinoDeTeste string
	// imagemDoProxyDeTeste, quando não vazia, faz de imagem do proxy EM USO na conferência da
	// qualificação. Só os testes do próprio pacote a definem, para provar a recusa.
	imagemDoProxyDeTeste string
	// fase, quando presente, é avisada do início de cada fase de uma corrida (AOS-522): é o
	// relógio do subcomando `qualificar`. Não entra no relatório nem muda a corrida.
	fase func(nome string)
	// entreACorridaEAPreparacao e depoisDeEscreverOPacote são COSTURAS DE TESTE do `qualificar`
	// (AOS-522): a primeira é chamada com a corrida feita e antes de se decidir se há pacote; a
	// segunda, entre a escrita do pacote e a sua releitura. Servem para um teste trocar o que está
	// em memória ou no disco e provar que cada barreira actua sozinha. Não são exportadas: não há
	// opção nem variável de ambiente que cá chegue.
	entreACorridaEAPreparacao func(feita *corridaFeita)
	depoisDeEscreverOPacote   func(pacote string)
	// medidaDeImagemASeguir é o pior caso, em pedidos, da medida de imagem que o `qualificar` corre
	// a seguir a esta qualificação (AOS-547), e zero sem ela. Com ela, a qualificação só arranca se
	// o dia ainda tiver as DUAS vagas do perfil e da rota e o contador couber nas duas corridas: a
	// medida não pode ser recusada depois de a qualificação ter sido paga.
	medidaDeImagemASeguir int64
	// entreAMedidaEAPreparacao é outra COSTURA DE TESTE do `qualificar` (AOS-547): chamada com a
	// medida de imagem feita e antes de se preparar o pacote.
	entreAMedidaEAPreparacao func(m *medidaDeMedia)
	// dockerDeTeste, quando presente, faz de Docker do motor de OCR em contentor (AOS-529). Só os
	// testes do próprio pacote a definem.
	dockerDeTeste corredorDocker
	// codificadorDeTeste, quando presente, faz de ffmpeg da experiência `video` (AOS-537): gera os
	// MP4 sem Docker. Só os testes do próprio pacote o definem.
	codificadorDeTeste CodificadorDeVideo
}

// corridaFeita é o que uma corrida deixou escrito: o relatório, tal como foi gravado, e os dois
// caminhos. Fica vazia quando a linha de comandos sai sem ter escrito relatório.
type corridaFeita struct {
	relatorio       *Relatorio
	emJSON, emTexto string
}

type opcoes struct {
	experiencia string
	amostras    int
	semente     uint64
	braco       string
	roteiro     string
	saida       string
	chaves      string
	fornecedor  string
	modelo      string
	precos      string
	contador    string
	tecto       int64
	binario     string
	soPlano     bool
	escuta      string
	chaveSha    string
	silencioso  bool
	pausa       time.Duration
	foraDaLista string
	reconstruir bool
	vida        time.Duration
	perfil      string
	// As quatro abaixo são da qualificação da devolução do estado opaco (AOS-516).
	estado        string
	turnosDoFalso int
	exigeID       bool
	hostEsperado  string
	formaDoFalso  string
	// imagemDeSaida é do subcomando interno `falso-provider` (AOS-558): o falso responde com uma
	// imagem gerada na forma da OpenRouter.
	imagemDeSaida bool
	// guardarRespostas liga as respostas guardadas a pedido (AOS-525, ver respostas.go).
	guardarRespostas bool
	// As quatro abaixo são da experiência `conselheiro` (AOS-528): o conselheiro real (fornecedor
	// e modelo, modo real), o falso (modo falso) e o máximo de consultas por run.
	conselheiroFornecedor string
	conselheiroModelo     string
	conselheiroFalso      string
	maxConsultas          int
}

const usoDoBanco = `aos-ensaio — banco de ensaio da fronteira runtime-modelo (AOS-512). NAO toca em producao.

  aos-ensaio falso   [opcoes]                     provider falso em processo (sem rede, sem Docker)
  aos-ensaio proxy   --binario-do-falso F [opcoes] imagem real do proxy a frente do provider falso (Docker)
  aos-ensaio real    --chaves F --fornecedor kimi|anthropic|openrouter [opcoes]
                                                   modelo real pelo proxy efemero (Docker); tectos do ficheiro
  aos-ensaio bateria [--experiencia degraus|conselheiro]
                                                   mostra o digest e os casos da bateria (com
                                                   --experiencia degraus, os da 2.0.0; conselheiro, os da 3.0.0)
  aos-ensaio ocr     [--motor falso|tesseract|visao ...]  mede um motor de OCR com imagens sinteticas (AOS-529;
                                                   'aos-ensaio ocr -h' para as opcoes); o relatorio traz o
                                                   veredicto contra o limiar de factos (AOS-530)
  aos-ensaio video   [--motor falso|visao ...]        mede um modelo capaz de video com videos sinteticos
                                                   (AOS-537; 'aos-ensaio video -h' para as opcoes); o MP4
                                                   gera-o o ffmpeg num contentor fixado; veredicto contra o
                                                   limiar de factos
  aos-ensaio qualificar-ocr --motor visao|falso --perfil F --board B --regiao R --saida PASTA ...
                                                   prepara o registo de entrada de USO TOOL do alias de uma
                                                   tool de OCR para o dono assinar (AOS-530; 'aos-ensaio
                                                   qualificar-ocr -h' para as opcoes)
  aos-ensaio qualificar-video --motor visao|falso --perfil F --board B --regiao R --saida PASTA ...
                                                   prepara o registo de entrada de USO TOOL do alias de uma
                                                   tool de video para o dono assinar (AOS-536; 'aos-ensaio
                                                   qualificar-video -h' para as opcoes)
  aos-ensaio limpar  [--chaves F | --contador F]   remove contentores e redes aos512-* que tenham ficado de uma
                                                   corrida anterior (um proxy orfao guarda a chave no ambiente)
                                                   e a trava do contador de um processo que ja morreu

opcoes comuns:
  --experiencia bateria|separadores|qualificacao|degraus|conselheiro   (omissao: bateria)
                                      qualificacao (AOS-518): a bateria 8 vezes com --perfil (obrigatorio) e um
                                      VEREDICTO calculado — qualificado (saida 0; 9 nos modos falso e proxy, que nao
                                      valem para producao), recusado (7) ou inconclusivo (8; 4 se parou a meio; 3 se
                                      o pior caso nao cabe no tecto); --so-plano sai com 10. Nao aceita
                                      --amostras nem --braco. O relatorio traz o digest de si proprio.
                                      degraus (AOS-525): a bateria 2.0.0, de dificuldade crescente (sete degraus),
                                      no braco A, com taxas POR DEGRAU e SEM veredicto (saida 0 se a corrida
                                      completou, sejam quais forem as taxas). Pior caso: 180 pedidos por passagem
                                      (--amostras). Nao aceita --braco nem --estado; nos modos falso e proxy o
                                      roteiro por omissao e degrau_cumpre.
                                      conselheiro (AOS-528): a bateria 3.0.0 (dificil), no braco A, em dois bracos
                                      intercalados — so-executor e executor+conselheiro, este com a tool aconselhar —,
                                      com os casos cumpridos, a taxa de consulta e o custo POR BRACO e por caso, SEM
                                      veredicto. Sem conselheiro corre so o braco so-executor (a corrida do tecto:
                                      o modelo forte como executor). Pior caso do executor: 168 pedidos por passagem
                                      e braco; do conselheiro: 21 por consulta permitida e passagem. Nos modos falso e
                                      proxy o roteiro por omissao e conselho_segue.
  --amostras N                        passagens pela bateria (omissao 1); nos separadores sao 53 por braco
  --semente N                         fixa a ordem dos bracos (vai no relatorio)
  --braco A|B|C|D                     braco da bateria (omissao A)
  --saida PASTA                       onde escrever o relatorio
  --silencioso                        nao mostra o resumo, so os caminhos
  --pausa DURACAO                     intervalo entre dois passos (ex.: 2s), para um limite de taxa
  --guardar-respostas                 (AOS-525; desligada por omissao) grava, ao lado do relatorio, a pasta
                                      respostas-<nome do relatorio>/ com um ficheiro JSON por run: o texto do
                                      modelo e os argumentos das tool calls nativas. Nao entra no relatorio nem
                                      no seu digest. Os casos sao sempre os da bateria sintetica embutida
  --perfil FICHEIRO                   perfil de rota CANDIDATO em JSON (AOS-513): parametros do pedido, versao
                                      da projeccao, classe de estado; o digest vai no relatorio
  --host-esperado HOST                so com um perfil que devolve estado: o host do endpoint que o proxy deve
                                      declarar ter servido (sem ele compara-se so o modelo servido)
so falso e proxy:   --roteiro cumpre,texto,...   --tecto-pedidos N --contador FICHEIRO
                                                 (na experiencia degraus: degrau_cumpre, degrau_omite,
                                                 degrau_distractor, degrau_uma_leitura, degrau_um_por_turno;
                                                 uma conversa por run; so valem nessa experiencia)
                    --estado exige|proibe        o provider falso do ESTADO OPACO (AOS-516) em vez do do roteiro:
                                                 exige de volta o estado que emitiu em cada turno com tools, ou
                                                 recusa qualquer estado; no modo proxy pede --perfil com um
                                                 expected_model anthropic/<modelo> e a rota do proxy e essa
                    --turnos-do-falso N          turnos com tool call do provider falso do estado (omissao 2)
                    --forma-do-falso openrouter  so com --estado: o falso emite e exige a forma da OpenRouter
                                                 (reasoning e reasoning_details); no modo proxy o expected_model
                                                 do perfil e entao openrouter/<autor>/<modelo> ou openai/<autor>/<modelo>
so conselheiro:     --conselheiro-falso certo|errado   (so falso) o conselheiro falso: responde com os factos
                                                 certos, ou com os errados, do que o executor lhe mandar
                    --conselheiro-fornecedor kimi|anthropic|openrouter  (so real) o fornecedor do conselheiro,
                                                 pelas chaves, tectos e listas do ficheiro de chaves
                    --conselheiro-modelo M       (so real) o modelo do conselheiro, da lista do ficheiro
                    --max-consultas N            consultas por run do braco com conselheiro (1 a 5; omissao 2)
                    --precos FICHEIRO            tambem nos modos falso e proxy: o custo estimado por braco
                    (os comportamentos conselho_cumpre, conselho_nao_consulta, conselho_segue e
                    conselho_ignora do --roteiro so valem nesta experiencia)
so real:            --modelo M  --precos FICHEIRO  --so-plano
                    --destino-fora-da-lista HOST   aceita um destino da chave fora da lista do fornecedor;
                                                   HOST tem de ser exactamente o host do ficheiro (https na mesma)
                    --reconstruir-contador         recria um contador desaparecido a partir dos relatorios de hoje
`

// Executar corre a linha de comandos e devolve o código de saída.
func Executar(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente) int {
	red := &Redactor{}
	stdout, stderr = red.Escritor(stdout), red.Escritor(stderr)
	if amb.Getenv == nil {
		amb.Getenv = func(string) string { return "" }
	}
	if amb.Relogio == nil {
		amb.Relogio = time.Now
	}
	if len(args) > 0 {
		// Os dois subcomandos do AOS-522 têm as suas opções e o seu texto de uso: os de sempre não
		// mudam de um byte.
		switch args[0] {
		case "qualificar":
			return qualificar(ctx, args[1:], stdout, stderr, amb, red)
		case "conferir":
			return conferir(args[1:], stdout, stderr)
		case "ocr":
			// A experiência de OCR (AOS-529) tem as suas opções e o seu uso (ocr_cli.go).
			return executarOCR(ctx, args[1:], stdout, stderr, amb, red)
		case "video":
			// A experiência de vídeo (AOS-537) tem as suas opções e o seu uso (video_cli.go).
			return executarVideo(ctx, args[1:], stdout, stderr, amb, red)
		case "qualificar-ocr":
			// O registo de uso tool (AOS-530) tem as suas opções e o seu uso (qualificar_ocr.go).
			return qualificarOCR(ctx, args[1:], stdout, stderr, amb, red)
		case "qualificar-video":
			// O registo de uso tool de vídeo (AOS-536), pelo mesmo caminho (qualificar_video.go).
			return qualificarVideo(ctx, args[1:], stdout, stderr, amb, red)
		}
	}
	return executarCom(ctx, args, stdout, stderr, amb, red, &corridaFeita{})
}

// executarCom é a linha de comandos das corridas, com as saídas já redigidas. Em `feita` fica o
// relatório que tiver sido escrito: é por aí que o subcomando `qualificar` (AOS-522) lê a corrida
// que acabou de fazer, e não um relatório vindo de fora.
func executarCom(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor, feita *corridaFeita) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usoDoBanco)
		return SaidaUso
	}
	modo, resto := args[0], args[1:]

	// O MODO COM MODELO REAL NUNCA CORRE EM CI. A recusa vem antes de tudo: antes de ler os
	// argumentos, o ficheiro de chaves ou o contador.
	if modo == ModoReal {
		for _, v := range variaveisDeCI {
			if amb.Getenv(v) != "" {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — o modo com modelo real nao corre em CI (a variavel %s esta definida)\n", v)
				return SaidaEmCI
			}
		}
	}

	var o opcoes
	fs := flag.NewFlagSet("aos-ensaio "+modo, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.experiencia, "experiencia", string(ExperienciaBateria), "")
	fs.IntVar(&o.amostras, "amostras", 0, "")
	fs.Uint64Var(&o.semente, "semente", 512, "")
	fs.StringVar(&o.braco, "braco", string(BracoA), "")
	fs.StringVar(&o.roteiro, "roteiro", "", "")
	fs.StringVar(&o.saida, "saida", "", "")
	fs.StringVar(&o.chaves, "chaves", "", "")
	fs.StringVar(&o.fornecedor, "fornecedor", "", "")
	fs.StringVar(&o.modelo, "modelo", "", "")
	fs.StringVar(&o.precos, "precos", "", "")
	fs.StringVar(&o.contador, "contador", "", "")
	fs.Int64Var(&o.tecto, "tecto-pedidos", 0, "")
	fs.StringVar(&o.binario, "binario-do-falso", "", "")
	fs.BoolVar(&o.soPlano, "so-plano", false, "")
	fs.StringVar(&o.escuta, "escuta", "", "")
	fs.StringVar(&o.chaveSha, "chave-sha256", "", "")
	fs.BoolVar(&o.silencioso, "silencioso", false, "")
	fs.DurationVar(&o.pausa, "pausa", 0, "")
	fs.StringVar(&o.foraDaLista, "destino-fora-da-lista", "", "")
	fs.BoolVar(&o.reconstruir, "reconstruir-contador", false, "")
	fs.DurationVar(&o.vida, "vida", 0, "")
	fs.StringVar(&o.perfil, "perfil", "", "")
	fs.StringVar(&o.estado, "estado", "", "")
	fs.IntVar(&o.turnosDoFalso, "turnos-do-falso", 0, "")
	fs.BoolVar(&o.exigeID, "exige-id", false, "")
	fs.StringVar(&o.hostEsperado, "host-esperado", "", "")
	fs.StringVar(&o.formaDoFalso, "forma-do-falso", "", "")
	fs.BoolVar(&o.imagemDeSaida, "imagem-de-saida", false, "")
	fs.BoolVar(&o.guardarRespostas, "guardar-respostas", false, "")
	fs.StringVar(&o.conselheiroFornecedor, "conselheiro-fornecedor", "", "")
	fs.StringVar(&o.conselheiroModelo, "conselheiro-modelo", "", "")
	fs.StringVar(&o.conselheiroFalso, "conselheiro-falso", "", "")
	fs.IntVar(&o.maxConsultas, "max-consultas", 0, "")
	fs.Usage = func() { fmt.Fprint(stderr, usoDoBanco) }
	if err := fs.Parse(resto); err != nil {
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "aos-ensaio: argumento a mais: %q\n", fs.Arg(0))
		return SaidaUso
	}

	// A BATERIA É A DA EXPERIÊNCIA (AOS-525): a por degraus só na experiência `degraus`; em
	// todas as outras, e em todos os subcomandos, a 1.0.0 de sempre.
	degraus := Experiencia(o.experiencia) == ExperienciaDegraus
	conselho := Experiencia(o.experiencia) == ExperienciaConselheiro
	carregar := CarregarBateria
	switch {
	case degraus:
		carregar = CarregarBateriaPorDegraus
	case conselho:
		// A bateria difícil (AOS-528) só na experiência `conselheiro`.
		carregar = CarregarBateriaDificil
	}
	bateria, err := carregar()
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaErro
	}
	switch modo {
	case "bateria":
		fmt.Fprintf(stdout, "bateria %s  digest %s\n", bateria.Versao, bateria.Digest())
		for _, c := range bateria.Casos {
			fmt.Fprintf(stdout, "  %s  %d no(s)  %s\n", c.ID, len(c.Nos), c.Descricao)
		}
		return SaidaOK
	case "falso-provider":
		return servirFalso(ctx, o, stderr)
	case "limpar":
		return limpar(ctx, o, amb, stdout, stderr)
	case ModoFalso, ModoProxy, ModoReal:
	default:
		fmt.Fprint(stderr, usoDoBanco)
		return SaidaUso
	}

	if msg := recusaDasOpcoesDoConselheiro(modo, o, conselho); msg != "" {
		fmt.Fprintf(stderr, "aos-ensaio: %s\n", msg)
		return SaidaUso
	}
	plano, err := planoDasOpcoes(bateria, o)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaUso
	}
	previstos, err := plano.PedidosMaximos(bateria)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaUso
	}
	// O pior caso do conselheiro (AOS-528): as consultas, por run do braço com ele. Zero sem ele.
	previstosDoConselheiro, err := plano.PedidosMaximosDoConselheiro(bateria)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaUso
	}
	if o.pausa < 0 || o.pausa > time.Minute {
		fmt.Fprintln(stderr, "aos-ensaio: --pausa tem de estar entre 0 e 1m")
		return SaidaUso
	}
	cfg := CfgDaCorrida{Modo: modo, Plano: plano, Bateria: bateria, Relogio: amb.Relogio, Extra: map[string]string{}, Pausa: o.pausa, fase: amb.fase}
	qualifica := plano.Experiencia == ExperienciaQualificacao
	// O PERFIL CANDIDATO (AOS-513). Lido pela leitura fechada do gateway: uma chave ou um valor
	// fora do conjunto recusa a corrida antes de qualquer pedido. O digest entra no relatório e
	// no digest da configuração.
	var perfil *modelgateway.RouteProfile
	if o.perfil != "" {
		doc, lerr := os.ReadFile(o.perfil) // #nosec G304 -- caminho dado pelo operador na linha de comandos
		if lerr != nil {
			fmt.Fprintln(stderr, "aos-ensaio: nao foi possivel ler o ficheiro de --perfil")
			return SaidaUso
		}
		if perfil, err = LerPerfilCandidato(doc); err != nil {
			fmt.Fprintf(stderr, "aos-ensaio: --perfil: %v\n", err)
			return SaidaUso
		}
		cfg.PerfilDigest = perfil.Digest()
		cfg.Extra["perfil"] = cfg.PerfilDigest
		// AOS-519: o alias da rota é o nome pedido do perfil. Só entra no digest da configuração
		// quando não é o de sempre, para as corridas de antes manterem o seu.
		if perfil.Requested != AliasDaRota {
			cfg.Extra["alias"] = perfil.Requested
		}
	}
	// A QUALIFICAÇÃO DA DEVOLUÇÃO (AOS-516). O host esperado só tem leitor quando o perfil devolve
	// estado: fora disso seria uma opção sem efeito, e recusa-se em vez de a ignorar.
	devolve := perfil != nil && perfil.StateReturn != modelgateway.StateReturnNever
	// A QUALIFICAÇÃO DO MODELO (AOS-518). É de uma rota com o seu perfil, atrás da imagem do proxy
	// declarada para produção, com um tecto próprio de pedidos.
	imagemEmUso := ImagemDoProxy
	if amb.imagemDoProxyDeTeste != "" {
		imagemEmUso = amb.imagemDoProxyDeTeste
	}
	var perfilDeControlo *modelgateway.RouteProfile
	if qualifica {
		if perfil == nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", ErrQualificacaoSemPerfil)
			return SaidaUso
		}
		cfg.Qualificacao = &CfgDaQualificacao{
			ModeloEsperado: perfil.ExpectedModel, Limite: NovoLimiteDaQualificacao(TectoDePedidosDaQualificacao),
			TectoMicroUSD: TectoMicroUSDDaQualificacao,
		}
		if modo != ModoFalso {
			// ANTES de enviar seja o que for: outra imagem do proxy é outra rota.
			if ierr := conferirImagemDoProxy(imagemEmUso, ImagemDoProxy); ierr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", ierr)
				return SaidaRecusada
			}
			cfg.Qualificacao.ImagemDoProxy = imagemEmUso
		}
		if perfil.StateReturn == modelgateway.StateReturnRequired {
			// «Aceite» separa-se de «exigido»: a qualificação inclui a corrida de controlo sem devolução.
			doControlo := PerfilDeControlo(*perfil)
			perfilDeControlo = &doControlo
			cfg.Qualificacao.ControloAplicavel, cfg.Qualificacao.ControloPerfilDigest = true, doControlo.Digest()
			cfg.Extra["perfil_de_controlo"] = cfg.Qualificacao.ControloPerfilDigest
			previstos += cfg.pedidosDoControlo()
		}
	}
	o.hostEsperado = strings.ToLower(strings.TrimSpace(o.hostEsperado))
	if o.hostEsperado != "" && ((!devolve && !qualifica) || !hostAceite(o.hostEsperado)) {
		fmt.Fprintln(stderr, "aos-ensaio: --host-esperado e um nome de host (sem esquema nem caminho) e so vale com um --perfil que devolva estado, ou na experiencia qualificacao")
		return SaidaUso
	}
	if o.estado != "" && o.estado != EstadoExige && o.estado != EstadoProibe {
		fmt.Fprintf(stderr, "aos-ensaio: --estado aceita %s ou %s\n", EstadoExige, EstadoProibe)
		return SaidaUso
	}
	if degraus && o.estado != "" {
		// O provider falso do estado não conhece a bateria por degraus: não a cumpre nem a falha.
		fmt.Fprintln(stderr, "aos-ensaio: --estado nao vale na experiencia degraus (o provider falso do estado nao responde aos degraus)")
		return SaidaUso
	}
	if conselho && o.estado != "" {
		fmt.Fprintln(stderr, "aos-ensaio: --estado nao vale na experiencia conselheiro (o provider falso do estado nao responde a bateria dificil)")
		return SaidaUso
	}
	if conselho {
		// A configuração do conselho existe sempre nesta experiência: os preços servem também a
		// corrida só com o executor (a do tecto).
		cfg.Conselho = &CfgDoConselho{}
		if aliasDoPerfil(perfil) == AliasDoConselheiro {
			fmt.Fprintf(stderr, "aos-ensaio: o nome pedido do perfil e o alias da rota do conselheiro (%s): escolha outro\n", AliasDoConselheiro)
			return SaidaUso
		}
	}
	if o.turnosDoFalso < 0 || o.turnosDoFalso > 15 || (o.turnosDoFalso != 0 && o.estado == "") || o.exigeID || o.imagemDeSaida {
		fmt.Fprintln(stderr, "aos-ensaio: --turnos-do-falso (1 a 15) so vale com --estado; --exige-id e --imagem-de-saida sao so do subcomando interno falso-provider")
		return SaidaUso
	}
	if o.formaDoFalso != "" && (o.formaDoFalso != FormaOpenRouter || o.estado == "") {
		fmt.Fprintf(stderr, "aos-ensaio: --forma-do-falso aceita %s e so vale com --estado\n", FormaOpenRouter)
		return SaidaUso
	}
	if devolve || (qualifica && o.hostEsperado != "") {
		cfg.Extra["host_esperado"] = fmt.Sprint(o.hostEsperado != "")
	}
	cfg.QualificaDevolucao = o.estado != ""
	if !o.silencioso {
		cfg.Progresso = stderr
	}
	pastaDaSaida := o.saida
	var (
		baseURL, segredoDaRota string
		fechar                 = func() {}
	)
	defer func() { fechar() }()
	// rastoDoProxy lê, enquanto o proxy está vivo, o rasto dos seus erros sem mensagens.
	var rastoDoProxy func() string
	// naoCoube trata uma corrida cujo pior caso não cabe no tecto do dia. Uma qualificação não é
	// só recusada: fica escrita, com o veredicto `inconclusivo`. Nenhum pedido saiu.
	naoCoube := func(cerr error) int {
		fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", cerr)
		if !qualifica || !errors.Is(cerr, ErrNaoCabe) {
			return SaidaRecusada
		}
		pasta := pastaDaSaida
		if pasta == "" && amb.PastaDoDono != "" {
			pasta = filepath.Join(amb.PastaDoDono, "relatorios")
		}
		if pasta == "" {
			fmt.Fprintln(stderr, "aos-ensaio: sem pasta do dono conhecida: indique --saida PASTA")
			return SaidaUso
		}
		relatorio, rerr := RelatorioDaQualificacaoQueNaoCoube(cfg)
		if rerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", rerr)
			return SaidaErro
		}
		emJSON, emTexto, eerr := EscreverRelatorio(pasta, relatorio)
		if eerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", eerr)
			return SaidaErro
		}
		*feita = corridaFeita{relatorio: relatorio, emJSON: emJSON, emTexto: emTexto}
		if !o.silencioso {
			fmt.Fprint(stdout, ResumoEmTexto(relatorio))
		}
		fmt.Fprintf(stdout, "relatorio: %s\nresumo:    %s\n", emJSON, emTexto)
		fmt.Fprintf(stderr, "aos-ensaio: VEREDICTO DA QUALIFICACAO: %s — o pior caso nao cabe no tecto do dia; nenhum pedido foi enviado\n", strings.ToUpper(relatorio.QualificacaoDoModelo.Veredicto))
		return SaidaRecusada
	}

	// O prazo máximo de vida dos contentores, derivado do plano: o pior caso de cada pedido (o
	// timeout de 120 s mais a pausa) vezes o máximo de pedidos, com folga para o arranque. É a
	// última rede: antes dele actua o vigia do sinal de vida.
	vida := time.Duration(previstos+previstosDoConselheiro)*(120*time.Second+o.pausa) + 15*time.Minute
	if vida > 12*time.Hour {
		vida = 12 * time.Hour
	}
	if modo != ModoFalso && amb.Lancador == nil && !o.soPlano {
		// ANTES de levantar um proxy, remove-se o que tenha ficado de uma corrida anterior.
		contentores, redes, verr := VarrerOrfaos(ctx)
		if verr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", verr)
			return SaidaSemInfra
		}
		if contentores+redes > 0 {
			fmt.Fprintf(stderr, "aos-ensaio: VARRIDOS %d contentor(es) e %d rede(s) aos512-* de uma corrida anterior (um proxy orfao guarda a chave no ambiente)\n", contentores, redes)
		}
	}

	switch modo {
	case ModoFalso, ModoProxy:
		if o.chaves != "" || o.fornecedor != "" || (o.precos != "" && !conselho) || o.soPlano || o.foraDaLista != "" || o.reconstruir {
			fmt.Fprintln(stderr, "aos-ensaio: --chaves, --fornecedor, --precos (salvo na experiencia conselheiro), --so-plano, --destino-fora-da-lista e --reconstruir-contador sao so do modo real")
			return SaidaUso
		}
		if o.estado != "" && o.roteiro != "" {
			fmt.Fprintln(stderr, "aos-ensaio: --estado e --roteiro escolhem o provider falso: so um deles")
			return SaidaUso
		}
		roteiro := RoteiroPorOmissao()
		switch {
		case degraus:
			// O roteiro de sempre devolve tudo o que as tools entregam: na bateria por degraus isso
			// eram os distractores na saída. O dos degraus responde só com os factos pedidos.
			roteiro = RoteiroDosDegraus()
		case conselho:
			roteiro = RoteiroDoConselheiro()
		}
		if o.roteiro != "" {
			if roteiro, err = LerRoteiro(o.roteiro); err != nil {
				fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
				return SaidaUso
			}
		}
		if !degraus {
			// Os comportamentos dos degraus só respondem à bateria por degraus: fora dela davam
			// 400 a todos os pedidos, e a corrida saía com 0 sem ter medido nada.
			for _, c := range roteiro {
				if comportamentoDosDegraus(c) {
					fmt.Fprintf(stderr, "aos-ensaio: o comportamento %s do --roteiro so vale na experiencia degraus\n", c)
					return SaidaUso
				}
			}
		}
		if !conselho {
			// Idem para os da experiência `conselheiro` (AOS-528): só respondem à bateria difícil.
			for _, c := range roteiro {
				if comportamentoDoConselheiro(c) {
					fmt.Fprintf(stderr, "aos-ensaio: o comportamento %s do --roteiro so vale na experiencia conselheiro\n", c)
					return SaidaUso
				}
			}
		}
		cfg.Extra["roteiro"] = EscreverRoteiro(roteiro)
		if o.estado != "" {
			// O provider falso do estado não tem roteiro: o que decide a corrida é o modo dele.
			delete(cfg.Extra, "roteiro")
			cfg.Extra["falso_do_estado"] = fmt.Sprintf("%s/%d", o.estado, o.turnosDoFalso)
			if o.formaDoFalso != "" {
				cfg.Extra["forma_do_falso"] = o.formaDoFalso
			}
		}
		cfg.Rota = RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco"}
		// O digest da rota fica já escrito (abaixo é reescrito, com o mesmo valor ou com o da rota
		// do perfil): um relatório de recusa por tecto sai antes de haver rota levantada.
		cfg.Rota.Digest = DigestDaRota(cfg.Rota.Fornecedor, cfg.Rota.Modelo, "em-processo")
		if modo == ModoProxy {
			cfg.Rota.Digest = DigestDaRota(cfg.Rota.Fornecedor, cfg.Rota.Modelo, "proxy:"+ImagemDoProxy)
		}
		if o.tecto != 0 || o.contador != "" {
			// O tecto nos modos sem modelo real existe para ensaiar o próprio tecto.
			if o.contador == "" {
				fmt.Fprintln(stderr, "aos-ensaio: --tecto-pedidos exige --contador FICHEIRO")
				return SaidaUso
			}
			cfg.Contador, err = AbrirContador(o.contador, FornecedorFalso, Tectos{PedidosDia: o.tecto}, nil, "", amb.Relogio)
			if err != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
				return SaidaRecusada
			}
			defer cfg.Contador.Fechar()
		}
		if cfg.Contador != nil {
			// O conselheiro falso conta no mesmo fornecedor (`falso`): o tecto é um só.
			if err := cfg.Contador.Cabe(previstos + previstosDoConselheiro); err != nil {
				return naoCoube(err)
			}
		}
		if conselho {
			if cerr := precosDoConselho(cfg.Conselho, o.precos, "modelo-falso-do-banco", "modelo-falso-do-conselheiro"); cerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", cerr)
				return SaidaRecusada
			}
		}
		if modo == ModoFalso && o.conselheiroFalso != "" {
			// O CONSELHEIRO FALSO (AOS-528): outro servidor em processo, a segunda rota.
			ouvinte, lerr := net.Listen("tcp", "127.0.0.1:0")
			if lerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
				return SaidaErro
			}
			srv := &http.Server{Handler: &ConselheiroFalso{Modo: o.conselheiroFalso}, ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = srv.Serve(ouvinte) }()
			defer func() { _ = srv.Close() }()
			var contadorDoConselheiro *Contador
			if cfg.Contador != nil {
				if contadorDoConselheiro, err = cfg.Contador.Irmao(FornecedorFalso, Tectos{PedidosDia: o.tecto}, nil, ""); err != nil {
					fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
					return SaidaRecusada
				}
			}
			// Como o `segredoDaRota` do executor no modo falso: um valor fixo que o servidor falso
			// local aceita, e não uma credencial de fornecedor nenhum.
			segredoDoConselheiroFalso := "credencial-do-conselheiro-falso"
			conselheiro, cerr := NovoConselheiro(CfgDoConselheiro{
				BaseURL: "http://" + ouvinte.Addr().String() + "/v1", Credencial: segredoDoConselheiroFalso,
				Contador: contadorDoConselheiro, MaxConsultas: plano.MaxConsultas,
			})
			if cerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: %v\n", cerr)
				return SaidaErro
			}
			cfg.Conselho.Conselheiro = conselheiro
			cfg.Conselho.Rota = RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-conselheiro"}
			cfg.Conselho.Rota.Digest = DigestDaRota(cfg.Conselho.Rota.Fornecedor, cfg.Conselho.Rota.Modelo, "em-processo")
			cfg.Extra["conselheiro"] = extraDoConselheiro(cfg.Conselho.Rota, plano.MaxConsultas)
			cfg.Extra["conselheiro_falso"] = o.conselheiroFalso
		}
		if modo == ModoFalso {
			doRoteiro := NovoProviderFalso(roteiro)
			if qualifica {
				// Sem proxy, é o falso que declara o modelo servido, com o nome que o perfil espera
				// (o comportamento `troca_modelo` declara outro).
				doRoteiro.DeclararModeloServido(perfil.ExpectedModel)
			}
			var falso http.Handler = doRoteiro
			if o.estado != "" {
				// Sem proxy, é o falso que declara o modelo servido — com o nome que o perfil
				// espera. Sem perfil não declara nada.
				doEstado := &FalsoDeEstado{Proibe: o.estado == EstadoProibe, Turnos: o.turnosDoFalso, FormaDoEstado: o.formaDoFalso}
				if perfil != nil {
					doEstado.ServidoComo = perfil.ExpectedModel
					doEstado.ExigeID = perfil.ToolCallID == modelgateway.ToolCallIDProvider
				}
				cfg.FormaNoFornecedor = func() (*FormaNoFornecedor, error) { return doEstado.Forma(), nil }
				falso = doEstado
			}
			ouvinte, lerr := net.Listen("tcp", "127.0.0.1:0")
			if lerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
				return SaidaErro
			}
			srv := &http.Server{Handler: falso, ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = srv.Serve(ouvinte) }()
			fechar = func() { _ = srv.Close() }
			baseURL, segredoDaRota = "http://"+ouvinte.Addr().String()+"/v1", "credencial-do-modo-falso"
			cfg.Rota.Digest = DigestDaRota(cfg.Rota.Fornecedor, cfg.Rota.Modelo, "em-processo")
			break
		}
		if o.binario == "" {
			fmt.Fprintln(stderr, "aos-ensaio: o modo proxy exige --binario-do-falso (um binario LINUX do aos-ensaio; ver scripts/ci/banco-ensaio-proxy.sh)")
			return SaidaUso
		}
		aleatorio := make([]byte, 16)
		if _, rerr := rand.Read(aleatorio); rerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", rerr)
			return SaidaErro
		}
		chaveDoFalso := "sk-falso-" + hex.EncodeToString(aleatorio)
		red.Acrescentar(chaveDoFalso)
		pedidoDeProxy := PedidoDeProxy{Prefixo: "openai", Modelo: "modelo-falso-do-banco", Alias: aliasDoPerfil(perfil), BinarioDoFalso: o.binario, Roteiro: roteiro, Vida: vida}
		if o.estado != "" {
			// A rota do proxy é a que o perfil ESPERA — é esse o nome que o proxy vai declarar ter
			// servido. Com o falso do AOS-515 tem de ser uma rota anthropic/…: é o caminho de
			// tradução que se mede. Com a forma da OpenRouter é openrouter/… (o adaptador próprio
			// do proxy) ou openai/… (o adaptador genérico com a base da OpenRouter).
			prefixos := []string{"anthropic"}
			if o.formaDoFalso == FormaOpenRouter {
				prefixos = []string{PrefixoDaOpenRouter, "openai"}
			}
			prefixo, modelo := "", ""
			for _, p := range prefixos {
				if m, tem := strings.CutPrefix(perfilEsperado(perfil), p+"/"); tem && nomeDeModeloAceite(m) {
					prefixo, modelo = p, m
				}
			}
			if prefixo == "" || (o.formaDoFalso == FormaOpenRouter && !modeloDaOpenRouterAceite(modelo)) {
				fmt.Fprintln(stderr, "aos-ensaio: o modo proxy com --estado exige --perfil com expected_model anthropic/<modelo> ou, com --forma-do-falso openrouter, openrouter/<autor>/<modelo> ou openai/<autor>/<modelo> (a rota openai/ do falso do AOS-515 mede-se em scripts/ci/wire-live.sh)")
				return SaidaUso
			}
			pedidoDeProxy.Prefixo, pedidoDeProxy.Modelo, pedidoDeProxy.Roteiro = prefixo, modelo, nil
			pedidoDeProxy.Estado, pedidoDeProxy.TurnosDoFalso, pedidoDeProxy.FormaDoFalso = o.estado, o.turnosDoFalso, o.formaDoFalso
			cfg.Rota.Modelo = perfil.ExpectedModel
		}
		vivo, lerr := lancadorDe(amb, red).Lancar(ctx, pedidoDeProxy.ComSegredos(chaveDoFalso, ""))
		if lerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
			return SaidaSemInfra
		}
		red.Acrescentar(vivo.ChaveMestra())
		fechar = vivo.Fechar
		baseURL, segredoDaRota = vivo.BaseURL, vivo.ChaveMestra()
		cfg.Rota.Digest = DigestDaRota(cfg.Rota.Fornecedor, cfg.Rota.Modelo, "proxy:"+ImagemDoProxy)
		if o.estado != "" && vivo.EnderecoDoFalso != "" {
			cfg.FormaNoFornecedor = func() (*FormaNoFornecedor, error) { return LerFormaDoFalso(ctx, vivo.EnderecoDoFalso) }
		}

	case ModoReal:
		if o.roteiro != "" || o.tecto != 0 || o.binario != "" || o.contador != "" {
			// O contador do modo real é SEMPRE o que está ao lado do ficheiro de chaves: uma
			// flag que apontasse para outro ficheiro punha a contagem do dia a zero.
			fmt.Fprintln(stderr, "aos-ensaio: --roteiro, --tecto-pedidos, --binario-do-falso e --contador nao sao do modo real (o tecto vem do ficheiro de chaves, e o contador fica ao lado dele)")
			return SaidaUso
		}
		if o.estado != "" {
			fmt.Fprintln(stderr, "aos-ensaio: --estado escolhe um provider falso: nao e do modo real")
			return SaidaUso
		}
		// Só arranca com um caminho EXPLÍCITO para o ficheiro de chaves: não há omissão.
		caminho := o.chaves
		if caminho == "" {
			caminho = amb.Getenv(EnvDasChaves)
		}
		if caminho == "" {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — o modo real exige o caminho do ficheiro de chaves (--chaves ou %s)\n", EnvDasChaves)
			return SaidaRecusada
		}
		fornecedor, ferr := LerFornecedor(o.fornecedor)
		if ferr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", ferr)
			return SaidaUso
		}
		chaves, cerr := LerChaves(caminho)
		if cerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", cerr)
			return SaidaRecusada
		}
		rota, rerr := chaves.Rota(fornecedor, o.modelo, Destino{ForaDaLista: strings.ToLower(strings.TrimSpace(o.foraDaLista)), deTeste: amb.destinoDeTeste})
		if rerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", rerr)
			return SaidaRecusada
		}
		red.Acrescentar(rota.Segredos()...)
		prefixo := prefixoDoProxy(fornecedor)
		// O CONSELHEIRO (AOS-528): outro fornecedor e outro modelo, possivelmente, pelas MESMAS
		// regras do executor — as chaves, os tectos e a lista de modelos do ficheiro do dono.
		var (
			rotaDoConselheiro       RotaReal
			fornecedorDoConselheiro Fornecedor
		)
		comConselheiro := o.conselheiroFornecedor != ""
		if comConselheiro {
			if fornecedorDoConselheiro, ferr = LerFornecedor(o.conselheiroFornecedor); ferr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: --conselheiro-fornecedor: %v\n", ferr)
				return SaidaUso
			}
			if rotaDoConselheiro, rerr = chaves.Rota(fornecedorDoConselheiro, o.conselheiroModelo, Destino{deTeste: amb.destinoDeTeste}); rerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — conselheiro: %v\n", rerr)
				return SaidaRecusada
			}
			red.Acrescentar(rotaDoConselheiro.Segredos()...)
		}
		if qualifica && perfil.ExpectedModel != prefixo+"/"+rota.Modelo {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — o expected_model do perfil nao e a rota desta corrida (%s/%s): a qualificacao e da rota, e o modelo servido tem de ser o que o perfil espera\n", prefixo, rota.Modelo)
			return SaidaRecusada
		}
		if devolve && perfil.ExpectedModel != prefixo+"/"+rota.Modelo {
			// O proxy declara ter servido `<adaptador>/<modelo>`. Com outro nome no perfil a rota
			// nunca se provava igual, o estado nunca era devolvido, e cada run gastava um pedido do
			// tecto para parar no segundo. Recusa-se antes de enviar seja o que for.
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — o perfil devolve estado e o seu expected_model nao e a rota desta corrida (%s/%s): o estado so volta a rota que o produziu\n", prefixo, rota.Modelo)
			return SaidaRecusada
		}
		var precos *TabelaDePrecos
		if o.precos != "" {
			if precos, err = LerTabelaDePrecos(o.precos); err != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
				return SaidaRecusada
			}
			if p, perr := precos.PrecoDe(rota.Modelo); perr == nil {
				cfg.Extra["preco"] = fmt.Sprintf("%d/%d", p.EntradaMicroUSDPorMTok, p.SaidaMicroUSDPorMTok)
			}
			if comConselheiro {
				if p, perr := precos.PrecoDe(rotaDoConselheiro.Modelo); perr == nil {
					cfg.Extra["preco_conselheiro"] = fmt.Sprintf("%d/%d", p.EntradaMicroUSDPorMTok, p.SaidaMicroUSDPorMTok)
				}
			}
		}
		if conselho {
			cfg.Conselho.PrecoDoExecutor = precoOpcional(precos, rota.Modelo)
			if comConselheiro {
				cfg.Conselho.PrecoDoConselheiro = precoOpcional(precos, rotaDoConselheiro.Modelo)
			}
		}
		// O CONTADOR E O REGISTO DAS QUALIFICAÇÕES vivem na PASTA DO DONO (`~/.aos-ensaio`), que
		// não depende de `--chaves`: copiar o ficheiro de chaves para outra pasta não repõe a
		// contagem do dia (AOS-518). Com as chaves no sítio documentado é a mesma pasta de sempre.
		// Sem pasta do dono conhecida (só nos testes), fica ao lado do ficheiro de chaves. Não há flag.
		pastaDoEstado := filepath.Dir(caminho)
		if amb.PastaDoDono != "" {
			pastaDoEstado = amb.PastaDoDono
		}
		caminhoDoContador := filepath.Join(pastaDoEstado, "contador.json")
		if pastaDaSaida == "" {
			pastaDaSaida = filepath.Join(pastaDoEstado, "relatorios")
		}
		pastasDeRelatorios := []string{filepath.Join(pastaDoEstado, "relatorios"), filepath.Join(filepath.Dir(caminho), "relatorios"), pastaDaSaida}
		// O do conselheiro também (AOS-528): o contador é um só ficheiro para os dois.
		fornecedores := []Fornecedor{fornecedor}
		if comConselheiro && fornecedorDoConselheiro != fornecedor {
			fornecedores = append(fornecedores, fornecedorDoConselheiro)
		}
		if !verificarContadorDoDia(caminhoDoContador, pastasDeRelatorios, fornecedores, amb.Relogio().UTC().Format("2006-01-02"), o.reconstruir, stderr) {
			return SaidaRecusada
		}
		// FAIL-CLOSED: sem tecto, sem preço para um tecto em dólares, com um contador que não
		// se lê ou em uso por outro processo, o contador não abre e a corrida não arranca.
		cfg.Contador, err = AbrirContador(caminhoDoContador, fornecedor, rota.Tectos, precos, rota.Modelo, amb.Relogio)
		if err != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
			return SaidaRecusada
		}
		defer cfg.Contador.Fechar()
		cfg.Rota = RotaDoRelatorio{Fornecedor: string(fornecedor), Modelo: rota.Modelo, Digest: DigestDaRota(string(fornecedor), rota.Modelo, rota.apiBase)}
		cfg.RegiaoDeclarada = rota.RegiaoDeclarada
		// Mais um: a sonda antes da corrida conta no tecto como um pedido.
		cfg.Sondar = true
		if err := cfg.Contador.Cabe(previstos + 1 + amb.medidaDeImagemASeguir); err != nil {
			return naoCoube(err)
		}
		// O CONTADOR DO CONSELHEIRO (AOS-528): o mesmo ficheiro e a mesma trava, na linha do
		// fornecedor dele, com os tectos dele e o preço do modelo dele. O pior caso dele — as
		// consultas e a sonda — tem de caber no que resta; no mesmo fornecedor, somado ao do executor.
		var contadorDoConselheiro *Contador
		if comConselheiro {
			if contadorDoConselheiro, err = cfg.Contador.Irmao(fornecedorDoConselheiro, rotaDoConselheiro.Tectos, precos, rotaDoConselheiro.Modelo); err != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — conselheiro (%s): %v\n", fornecedorDoConselheiro, err)
				return SaidaRecusada
			}
			cabe := contadorDoConselheiro.Cabe(previstosDoConselheiro + 1)
			if fornecedorDoConselheiro == fornecedor {
				cabe = cfg.Contador.Cabe(previstos + 1 + previstosDoConselheiro + 1)
			}
			if cabe != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — o pior caso do conselheiro (%d consultas e 1 sonda, no fornecedor %s) nao cabe: %v\n", previstosDoConselheiro, fornecedorDoConselheiro, cabe)
				return SaidaRecusada
			}
		}
		// AS QUALIFICAÇÕES DO DIA (decisão D7): no máximo duas por perfil. O registo fica ao lado
		// do ficheiro de chaves; confere-se antes de enviar e escreve-se quando a sonda passa.
		caminhoDasQualificacoes := filepath.Join(pastaDoEstado, NomeDoRegistoDeQualificacoes)
		chaveDaQualificacao := ChaveDaQualificacao{Dia: amb.Relogio().UTC().Format("2006-01-02"), PerfilDigest: cfg.PerfilDigest, Fornecedor: string(fornecedor), Modelo: rota.Modelo}
		if qualifica {
			feitas, qerr := QualificacoesDeHoje(caminhoDasQualificacoes, pastasDeRelatorios, chaveDaQualificacao)
			if qerr == nil && feitas.esgotada() {
				qerr = ErrQualificacoesDoDia
			}
			// A MEDIDA DE IMAGEM A SEGUIR (AOS-547) conta como outra qualificação do mesmo perfil e da
			// mesma rota: as duas vagas têm de existir já.
			if qerr == nil && amb.medidaDeImagemASeguir > 0 && (feitas.DoPerfil+2 > QualificacoesPorPerfilPorDia || feitas.DaRota+2 > QualificacoesPorRotaPorDia) {
				qerr = fmt.Errorf("%w: a medida de imagem (AOS-547) corre a seguir e conta como outra; o dia ja nao tem as duas vagas deste perfil e desta rota", ErrQualificacoesDoDia)
			}
			if qerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", qerr)
				return SaidaRecusada
			}
			fmt.Fprintf(stderr, "aos-ensaio: QUALIFICACAO — pior caso declarado: %d pedidos (com a sonda e, se o perfil devolve estado por obrigacao, o controlo); tecto proprio: %d pedidos e %.2f USD estimados; este perfil ja foi qualificado %d vez(es) hoje, de um maximo de %d; esta rota, %d de %d\n",
				previstos+1, int64(TectoDePedidosDaQualificacao), float64(TectoMicroUSDDaQualificacao)/1e6, feitas.DoPerfil, QualificacoesPorPerfilPorDia, feitas.DaRota, QualificacoesPorRotaPorDia)
			if !rota.Tectos.TemUSD {
				fmt.Fprintln(stderr, "aos-ensaio: AVISO — o tecto de 3 USD da qualificacao NAO sera imposto: este fornecedor nao tem tecto em dolares no ficheiro de chaves (actua so o tecto de pedidos)")
			}
		}
		cfg.AbortarAposRecusasDeChave = RecusasDeChaveQueAbortam
		cfg.AbortarApos429Iniciais, cfg.AbortarAposSerieDe429 = Respostas429QueAbortam, SerieDe429QueAborta
		estado, eerr := cfg.Contador.Estado()
		if eerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", eerr)
			return SaidaRecusada
		}
		fmt.Fprintf(stderr, "aos-ensaio: modo REAL — fornecedor %s, modelo %s; a corrida faz ate %d pedidos, mais 1 de sonda antes de comecar; hoje (%s UTC) restam %d de %d\n",
			fornecedor, rota.Modelo, previstos, estado.Dia, estado.PedidosRestantes, estado.TectoPedidos)
		// O destino mostra-se ANTES de enviar seja o que for: é onde o dono confirma para onde a
		// chave vai. O host de um fornecedor público não é segredo; o caminho da base não vai.
		fmt.Fprintf(stderr, "aos-ensaio: DESTINO DA CHAVE: %s  (contador: %s)\n", rota.Destino, caminhoDoContador)
		if comConselheiro {
			doConselheiro, cerr := contadorDoConselheiro.Estado()
			if cerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", cerr)
				return SaidaRecusada
			}
			fmt.Fprintf(stderr, "aos-ensaio: CONSELHEIRO — fornecedor %s, modelo %s; ate %d consultas (%d por run do braco com ele), mais 1 de sonda; hoje (%s UTC) restam %d de %d deste fornecedor\n",
				fornecedorDoConselheiro, rotaDoConselheiro.Modelo, previstosDoConselheiro, plano.MaxConsultas, doConselheiro.Dia, doConselheiro.PedidosRestantes, doConselheiro.TectoPedidos)
			fmt.Fprintf(stderr, "aos-ensaio: DESTINO DA CHAVE DO CONSELHEIRO: %s\n", rotaDoConselheiro.Destino)
		}
		if conselho {
			fmt.Fprintf(stderr, "aos-ensaio: PIOR CASO POR FORNECEDOR (com as sondas): %s\n", piorCasoPorFornecedor(fornecedor, previstos+1, fornecedorDoConselheiro, previstosDoConselheiro))
		}
		if o.soPlano {
			fmt.Fprintln(stderr, "aos-ensaio: --so-plano: nenhum pedido foi enviado")
			if o.guardarRespostas {
				fmt.Fprintln(stderr, "aos-ensaio: --guardar-respostas nao tem efeito com --so-plano: nenhum run corre, e nao ha respostas para guardar")
			}
			if qualifica {
				return SaidaSoPlano
			}
			return SaidaOK
		}
		pedidoDeProxy := PedidoDeProxy{Prefixo: prefixo, Modelo: rota.Modelo, Alias: aliasDoPerfil(perfil), Vida: vida}.ComSegredos(rota.apiKey, rota.apiBase)
		if comConselheiro {
			// A SEGUNDA ROTA do mesmo proxy: o mesmo caminho, a mesma imagem fixada, o mesmo rasto.
			pedidoDeProxy.Conselheiro = RotaDoConselheiroNoProxy{Prefixo: prefixoDoProxy(fornecedorDoConselheiro), Modelo: rotaDoConselheiro.Modelo, Alias: AliasDoConselheiro}.ComSegredos(rotaDoConselheiro.apiKey, rotaDoConselheiro.apiBase)
		}
		vivo, lerr := lancadorDe(amb, red).Lancar(ctx, pedidoDeProxy)
		if lerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
			return SaidaSemInfra
		}
		red.Acrescentar(vivo.ChaveMestra())
		fechar = vivo.Fechar
		rastoDoProxy = vivo.Rasto
		baseURL, segredoDaRota = vivo.BaseURL, vivo.ChaveMestra()
		if comConselheiro {
			conselheiro, cerr := NovoConselheiro(CfgDoConselheiro{BaseURL: vivo.BaseURL, Credencial: vivo.ChaveMestra(), Contador: contadorDoConselheiro, MaxConsultas: plano.MaxConsultas})
			if cerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: %v\n", cerr)
				return SaidaErro
			}
			cfg.Conselho.Conselheiro = conselheiro
			cfg.Conselho.Rota = RotaDoRelatorio{Fornecedor: string(fornecedorDoConselheiro), Modelo: rotaDoConselheiro.Modelo, Digest: DigestDaRota(string(fornecedorDoConselheiro), rotaDoConselheiro.Modelo, rotaDoConselheiro.apiBase)}
			cfg.Extra["conselheiro"] = extraDoConselheiro(cfg.Conselho.Rota, plano.MaxConsultas)
		}
		if qualifica {
			cfg.Qualificacao.Registar = func() (OrdemDaQualificacao, error) {
				return RegistarQualificacao(caminhoDasQualificacoes, pastasDeRelatorios, chaveDaQualificacao)
			}
		}
	}

	if pastaDaSaida == "" {
		if amb.PastaDoDono == "" {
			fmt.Fprintln(stderr, "aos-ensaio: sem pasta do dono conhecida: indique --saida PASTA")
			return SaidaUso
		}
		pastaDaSaida = filepath.Join(amb.PastaDoDono, "relatorios")
	}
	cfgDoNo := CfgDoNo{Bateria: bateria, BaseURL: baseURL, Credencial: segredoDaRota, Contador: cfg.Contador, Perfil: perfil, HostEsperado: o.hostEsperado}
	if cfg.Conselho != nil {
		cfgDoNo.Conselheiro = cfg.Conselho.Conselheiro
	}
	// AS RESPOSTAS GUARDADAS, A PEDIDO (AOS-525). Sem a opção não há receptor, e o texto das
	// respostas não sai do nó de ensaio. Não há recusa «fora da bateria sintética» porque não há
	// outra: `bateria` vem sempre de [CarregarBateria] ou de [CarregarBateriaPorDegraus], as duas
	// embebidas e sintéticas (ver respostas.go).
	var respostas *respostasGuardadas
	if o.guardarRespostas {
		respostas = &respostasGuardadas{}
		cfgDoNo.GuardarRespostas = respostas.daCorrida(CorridaPrincipal)
	}
	if qualifica {
		cfgDoNo.Qualificacao, cfgDoNo.Limite = true, cfg.Qualificacao.Limite
	}
	no, err := NovoNoDeEnsaio(ctx, cfgDoNo)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaErro
	}
	defer no.Fechar()
	cfg.No = no
	if perfilDeControlo != nil {
		// O nó da corrida de controlo: a mesma rota, o mesmo contador e o mesmo tecto, com o
		// perfil que NÃO devolve estado.
		cfgDoNo.Perfil = perfilDeControlo
		if respostas != nil {
			cfgDoNo.GuardarRespostas = respostas.daCorrida(CorridaDeControlo)
		}
		doControlo, cerr := NovoNoDeEnsaio(ctx, cfgDoNo)
		if cerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", cerr)
			return SaidaErro
		}
		defer doControlo.Fechar()
		cfg.Qualificacao.Controlo = doControlo
	}

	// semRelatorio avisa, só com a opção ligada e runs corridos, de que as respostas se perderam:
	// estavam em memória à espera do nome do relatório, e o relatório não chegou a ser escrito.
	semRelatorio := func() {
		if respostas != nil && len(respostas.runs) > 0 {
			fmt.Fprintf(stderr, "aos-ensaio: AVISO — as respostas de %d run(s) NAO ficaram guardadas: o relatorio nao chegou a ser escrito, e elas so se escrevem ao lado dele\n", len(respostas.runs))
		}
	}
	relatorio, err := Correr(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
		semRelatorio()
		if errors.Is(err, ErrNaoCabe) || errors.Is(err, ErrModoRealSemTecto) || errors.Is(err, ErrContador) ||
			errors.Is(err, ErrQualificacoesDoDia) || errors.Is(err, ErrRegistoDeQualificacoes) || errors.Is(err, ErrImagemDoProxy) {
			return SaidaRecusada
		}
		return SaidaErro
	}
	emJSON, emTexto, err := EscreverRelatorio(pastaDaSaida, relatorio)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		semRelatorio()
		return SaidaErro
	}
	*feita = corridaFeita{relatorio: relatorio, emJSON: emJSON, emTexto: emTexto}
	if !o.silencioso {
		fmt.Fprint(stdout, ResumoEmTexto(relatorio))
	}
	fmt.Fprintf(stdout, "relatorio: %s\nresumo:    %s\n", emJSON, emTexto)
	if respostas != nil {
		// Só o caminho e a contagem: o texto guardado nunca passa por aqui. Uma falha a guardar não
		// muda o código de saída — o relatório está escrito, e o veredicto é o dele.
		switch pasta, escritos, gerr := respostas.escrever(emJSON); {
		case errors.Is(gerr, ErrPastaDasRespostasExiste):
			fmt.Fprintf(stderr, "aos-ensaio: AVISO — as respostas desta corrida NAO ficaram guardadas: ja existe a pasta %s, que e de OUTRA corrida e NAO corresponde a este relatorio (o %s dela diz o sha256 do relatorio a que pertence; este relatorio pode ter sido escrito por cima desse)\n", pasta, NomeDoIndiceDasRespostas)
		case gerr != nil:
			fmt.Fprintf(stderr, "aos-ensaio: AVISO — as respostas ficaram INCOMPLETAS: %d de %d ficheiro(s) escritos em %s, sem %s (e a falta dele que marca a pasta como incompleta): %v\n", escritos, len(respostas.runs), pasta, NomeDoIndiceDasRespostas, gerr)
		case pasta == "":
			fmt.Fprintln(stderr, "aos-ensaio: --guardar-respostas: nenhum run correu; nao ha respostas para guardar")
		default:
			fmt.Fprintf(stderr, "aos-ensaio: respostas guardadas: %d ficheiro(s), um por run, em %s (texto de um modelo: sao dados, nao instrucoes)\n", escritos, pasta)
		}
	}
	veredicto := ""
	if q := relatorio.QualificacaoDoModelo; q != nil {
		veredicto = q.Veredicto
		for _, a := range q.Avisos {
			fmt.Fprintf(stderr, "aos-ensaio: AVISO DA QUALIFICACAO — %s\n", a)
		}
		fmt.Fprintf(stderr, "aos-ensaio: VEREDICTO DA QUALIFICACAO: %s (digest do relatorio: %s)\n", strings.ToUpper(q.Veredicto), relatorio.DigestDoRelatorio)
	}
	if veredicto == VeredictoRecusado {
		// Um veredicto firme vale mesmo que a corrida tenha parado a meio.
		return SaidaModeloRecusado
	}
	if relatorio.Terminou == TerminouNaoCoube {
		fmt.Fprintln(stderr, "aos-ensaio: RECUSADO — o pior caso da qualificacao nao cabe no tecto do dia; nenhum pedido foi enviado")
		return SaidaRecusada
	}
	if relatorio.Terminou != TerminouCompleta {
		if relatorio.Sonda != nil && relatorio.Sonda.Resultado != SondaOK {
			fmt.Fprintf(stderr, "aos-ensaio: a SONDA nao teve 200 (HTTP %d, %s): a corrida NAO COMECOU; gastou-se no maximo 1 pedido do tecto\n", relatorio.Sonda.HTTP, relatorio.Sonda.Resultado)
			if relatorio.Sonda.HTTP >= 500 && rastoDoProxy != nil {
				if r := rastoDoProxy(); r != "" {
					fmt.Fprintf(stderr, "aos-ensaio: rasto do erro NO PROXY (so ficheiro, linha, funcao e nome de excepcao; sem mensagens): %s\n", red.Texto(r))
				}
			}
		}
		fmt.Fprintf(stderr, "aos-ensaio: a corrida PAROU A MEIO (%s); o relatorio e parcial\n", relatorio.Terminou)
		if conselho := conselhoDaParagem(relatorio); conselho != "" {
			fmt.Fprintf(stderr, "aos-ensaio: %s\n", conselho)
		}
		return SaidaParouAMeio
	}
	if veredicto == VeredictoInconclusivo {
		return SaidaInconclusivo
	}
	if veredicto == VeredictoQualificado && !relatorio.QualificacaoDoModelo.ValeParaProducao {
		return SaidaQualificadoEmEnsaio
	}
	return SaidaOK
}

// conselhoDaParagem diz ao dono, em frases fixas, o que fazer com uma corrida que parou por um
// erro do fornecedor. Não leva nada do corpo do erro.
func conselhoDaParagem(r *Relatorio) string {
	const (
		semSaldo = "a conta do fornecedor nao tem saldo ou quota (saldo_insuficiente): carregue a conta; repetir a corrida nao adianta e gasta o tecto do dia"
		ritmo    = "limite de ritmo do fornecedor (limite_de_ritmo): repita com --pausa (por exemplo --pausa 5s) para espacar os pedidos"
	)
	switch r.Terminou {
	case TerminouChaveRecusada:
		return "o fornecedor recusou a chave (chave_recusada): confirme que a chave e deste produto e deste destino"
	case TerminouSaldoInsuficiente:
		return semSaldo
	case TerminouLimiteDeRitmo:
		return ritmo
	case TerminouModeloDesconhecido:
		return "o fornecedor nao conhece o modelo (modelo_desconhecido): confirme o nome do modelo no ficheiro de chaves ou em --modelo"
	case TerminouRotaIndisponivel:
		return "o fornecedor conhece o modelo mas nao tem endpoint que sirva este pedido (rota_indisponivel): veja, na conta do fornecedor, a politica de dados e os fornecedores permitidos, e se o modelo suporta tools; repetir a corrida nao adianta"
	case TerminouSondaFalhou:
		return "a rota nao respondeu 200 a sonda por uma causa fora do vocabulario (outro): veja o codigo HTTP acima"
	case TerminouSo429, TerminouSerieDe429:
		if r.Taxas.TiposDeErro[TipoSaldoInsuficiente] > 0 {
			return semSaldo
		}
		return "respostas 429 seguidas; se for limite de ritmo, repita com --pausa (por exemplo --pausa 5s) — a contagem por tipo de erro esta no relatorio"
	}
	return ""
}

// PrefixoDaOpenRouter é o adaptador do proxy para a OpenRouter. A escolha entre ele e o
// adaptador genérico `openai/` com a base da OpenRouter foi MEDIDA atrás da imagem fixada do
// proxy (TestAOS516_ProxyReal_ADevolucaoPelaOpenRouter): ver o README do banco.
const PrefixoDaOpenRouter = "openrouter"

// prefixoDoProxy devolve o adaptador do proxy para a rota real de um fornecedor.
func prefixoDoProxy(f Fornecedor) string {
	switch f {
	case FornecedorAnthropic:
		return "anthropic"
	case FornecedorOpenRouter:
		return PrefixoDaOpenRouter
	}
	return "openai"
}

// perfilEsperado devolve o expected_model de um perfil, ou vazio sem perfil.
func perfilEsperado(p *modelgateway.RouteProfile) string {
	if p == nil {
		return ""
	}
	return p.ExpectedModel
}

func lancadorDe(amb Ambiente, red *Redactor) LancadorDeProxy {
	if amb.Lancador != nil {
		return amb.Lancador
	}
	return &LancadorDocker{Redactor: red}
}

// limpar é o subcomando `limpar`: remove os contentores e as redes `aos512-*` que existirem e,
// se lhe disserem onde está o contador, a trava de um processo que já morreu. Não envia nada.
func limpar(ctx context.Context, o opcoes, amb Ambiente, stdout, stderr io.Writer) int {
	codigo := SaidaOK
	contentores, redes, err := VarrerOrfaos(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		codigo = SaidaSemInfra
	} else {
		fmt.Fprintf(stdout, "limpar: removidos %d contentor(es) e %d rede(s) aos512-*\n", contentores, redes)
	}
	caminho := o.contador
	if caminho == "" {
		chaves := o.chaves
		if chaves == "" {
			chaves = amb.Getenv(EnvDasChaves)
		}
		if chaves != "" {
			caminho = filepath.Join(filepath.Dir(chaves), "contador.json")
			if amb.PastaDoDono != "" {
				caminho = filepath.Join(amb.PastaDoDono, "contador.json")
			}
		}
	}
	if caminho == "" {
		fmt.Fprintln(stdout, "limpar: sem --chaves nem --contador, a trava do contador nao foi verificada")
		return codigo
	}
	removida, terr := RemoverTravaMorta(caminho)
	switch {
	case terr != nil:
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", terr)
		return SaidaRecusada
	case removida:
		fmt.Fprintln(stdout, "limpar: removida a trava do contador (o processo que a criou ja nao existe)")
	default:
		fmt.Fprintln(stdout, "limpar: o contador nao tem trava")
	}
	return codigo
}

// planoDasOpcoes constrói o plano da corrida a partir das opções.
func planoDasOpcoes(b *Bateria, o opcoes) (Plano, error) {
	switch Experiencia(o.experiencia) {
	case ExperienciaSeparadores:
		p := PlanoDosSeparadores(o.semente)
		if o.amostras > 0 {
			p.Amostras = o.amostras
		}
		return p, nil
	case ExperienciaBateria:
		braco, err := LerBraco(o.braco)
		if err != nil {
			return Plano{}, err
		}
		amostras := o.amostras
		if amostras <= 0 {
			amostras = 1
		}
		p := PlanoDaBateria(b, braco, amostras)
		p.Semente = o.semente
		return p, nil
	case ExperienciaQualificacao:
		// A amostra e o braço são os da decisão D2: não se escolhem na linha de comandos.
		if o.amostras != 0 || (o.braco != "" && o.braco != string(BracoA)) {
			return Plano{}, fmt.Errorf("banco-ensaio: a experiencia qualificacao nao aceita --amostras nem --braco (sao %d passagens no braco A)", PassagensDaQualificacao)
		}
		return PlanoDaQualificacao(b, o.semente), nil
	case ExperienciaDegraus:
		// O braço é o A: a experiência mede o modelo por degrau, não as variantes de protocolo.
		if o.braco != "" && o.braco != string(BracoA) {
			return Plano{}, fmt.Errorf("banco-ensaio: a experiencia degraus nao aceita --braco (corre no braco A)")
		}
		amostras := o.amostras
		if amostras <= 0 {
			amostras = 1
		}
		return PlanoDosDegraus(b, amostras, o.semente), nil
	case ExperienciaConselheiro:
		if o.braco != "" && o.braco != string(BracoA) {
			return Plano{}, fmt.Errorf("banco-ensaio: a experiencia conselheiro nao aceita --braco (corre no braco A; os bracos dela sao com e sem conselheiro)")
		}
		amostras := o.amostras
		if amostras <= 0 {
			amostras = 1
		}
		consultas := 0
		if o.conselheiroFalso != "" || o.conselheiroFornecedor != "" {
			consultas = o.maxConsultas
			if consultas == 0 {
				consultas = MaxConsultasPorOmissao
			}
		}
		return PlanoDoConselheiro(b, amostras, o.semente, consultas), nil
	}
	return Plano{}, fmt.Errorf("banco-ensaio: experiencia desconhecida: %q (aceites: bateria, separadores, qualificacao, degraus, conselheiro)", o.experiencia)
}

// servirFalso é o subcomando interno `falso-provider`: o provider falso do banco a ouvir num
// endereço. É o que corre dentro do contentor no modo `proxy`.
func servirFalso(ctx context.Context, o opcoes, stderr io.Writer) int {
	if o.escuta == "" {
		fmt.Fprintln(stderr, "aos-ensaio: falso-provider exige --escuta ENDERECO")
		return SaidaUso
	}
	roteiro := RoteiroPorOmissao()
	if o.roteiro != "" {
		var err error
		if roteiro, err = LerRoteiro(o.roteiro); err != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
			return SaidaUso
		}
	}
	if o.vida > 0 {
		// O prazo de vida do provider falso dentro do contentor: passado ele, sai sozinho.
		var cancelar context.CancelFunc
		ctx, cancelar = context.WithTimeout(ctx, o.vida)
		defer cancelar()
	}
	if o.estado != "" && o.estado != EstadoExige && o.estado != EstadoProibe {
		fmt.Fprintf(stderr, "aos-ensaio: --estado aceita %s ou %s\n", EstadoExige, EstadoProibe)
		return SaidaUso
	}
	if o.formaDoFalso != "" && o.formaDoFalso != FormaOpenRouter {
		fmt.Fprintf(stderr, "aos-ensaio: --forma-do-falso aceita %s\n", FormaOpenRouter)
		return SaidaUso
	}
	if o.imagemDeSaida && o.estado == "" {
		// Sem --estado o handler servido é o do roteiro, que não conhece a imagem de saída: um
		// flag aceite e ignorado em silêncio daria um ensaio que mede o que não ligou.
		fmt.Fprintln(stderr, "aos-ensaio: --imagem-de-saida so vale com --estado")
		return SaidaUso
	}
	doRoteiro, doEstado := NovoProviderFalso(roteiro), &FalsoDeEstado{Proibe: o.estado == EstadoProibe, Turnos: o.turnosDoFalso, ExigeID: o.exigeID, FormaDoEstado: o.formaDoFalso, ImagemDeSaida: o.imagemDeSaida}
	var falso http.Handler = doRoteiro
	if o.estado != "" {
		falso = doEstado
	}
	if o.chaveSha != "" {
		cru, err := hex.DecodeString(strings.TrimSpace(o.chaveSha))
		if err != nil || len(cru) != sha256.Size {
			fmt.Fprintln(stderr, "aos-ensaio: --chave-sha256 nao e um sha256 em hexadecimal")
			return SaidaUso
		}
		var soma [sha256.Size]byte
		copy(soma[:], cru)
		doRoteiro.ExigirChave(soma)
		doEstado.ExigirChave(soma)
	}
	srv := &http.Server{Addr: o.escuta, Handler: falso, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaErro
	}
	return SaidaOK
}

// recusaDasOpcoesDoConselheiro devolve a razão por que as opções do conselheiro (AOS-528) não
// servem a corrida pedida, ou vazio. São só da experiência `conselheiro`; o conselheiro falso só
// do modo `falso` e o real só do modo `real`. O modo `proxy` não o tem: o provider falso em
// contentor não faz de conselheiro.
func recusaDasOpcoesDoConselheiro(modo string, o opcoes, conselho bool) string {
	alguma := o.conselheiroFornecedor != "" || o.conselheiroModelo != "" || o.conselheiroFalso != "" || o.maxConsultas != 0
	switch {
	case !alguma:
		return ""
	case !conselho:
		return "--conselheiro-fornecedor, --conselheiro-modelo, --conselheiro-falso e --max-consultas so valem na experiencia conselheiro"
	case modo == ModoProxy:
		return "o conselheiro mede-se nos modos falso e real: o provider falso em contentor do modo proxy nao faz de conselheiro"
	case o.conselheiroFalso != "" && modo != ModoFalso:
		return "--conselheiro-falso e so do modo falso"
	case o.conselheiroFalso != "" && o.conselheiroFalso != ConselhoCerto && o.conselheiroFalso != ConselhoErrado:
		return fmt.Sprintf("--conselheiro-falso aceita %s ou %s", ConselhoCerto, ConselhoErrado)
	case (o.conselheiroFornecedor != "" || o.conselheiroModelo != "") && modo != ModoReal:
		return "--conselheiro-fornecedor e --conselheiro-modelo sao so do modo real"
	case o.conselheiroModelo != "" && o.conselheiroFornecedor == "":
		return "--conselheiro-modelo exige --conselheiro-fornecedor"
	case o.maxConsultas != 0 && o.conselheiroFalso == "" && o.conselheiroFornecedor == "":
		return "--max-consultas so vale com um conselheiro (--conselheiro-falso ou --conselheiro-fornecedor)"
	case o.maxConsultas < 0 || o.maxConsultas > MaxConsultasAceites:
		return fmt.Sprintf("--max-consultas vai de 1 a %d", MaxConsultasAceites)
	}
	return ""
}

// precoOpcional devolve o preço declarado de um modelo, ou nil sem tabela ou sem preço dele: o
// custo da experiência estima-se só com preço. (O tecto em dólares, esse, exige-o: [AbrirContador].)
func precoOpcional(t *TabelaDePrecos, modelo string) *Preco {
	p, err := t.PrecoDe(modelo)
	if err != nil {
		return nil
	}
	return &p
}

// precosDoConselho lê, nos modos sem modelo real, a tabela de preços (se foi dada) e põe na
// configuração do conselho os preços dos dois modelos falsos.
func precosDoConselho(c *CfgDoConselho, caminho, executor, conselheiro string) error {
	if caminho == "" {
		return nil
	}
	t, err := LerTabelaDePrecos(caminho)
	if err != nil {
		return err
	}
	c.PrecoDoExecutor, c.PrecoDoConselheiro = precoOpcional(t, executor), precoOpcional(t, conselheiro)
	return nil
}

// extraDoConselheiro é o que o conselheiro põe no digest da configuração: a rota dele, o máximo
// de consultas e os digests da tool e do prompt de sistema dele. Só nas corridas com conselheiro:
// as outras mantêm o digest de antes.
func extraDoConselheiro(rota RotaDoRelatorio, maxConsultas int) string {
	soma := sha256.Sum256([]byte(SystemDoConselheiro))
	return fmt.Sprintf("%s/%s/%d/%s/sha256:%s/%d", rota.Fornecedor, rota.Modelo, maxConsultas, digestDaTool(ToolDoConselho()), hex.EncodeToString(soma[:]), TokensDoConselho)
}

// piorCasoPorFornecedor escreve o pior caso de pedidos de cada fornecedor da corrida, com as
// sondas: o do executor e, havendo, o do conselheiro — somados quando são o mesmo.
func piorCasoPorFornecedor(executor Fornecedor, doExecutor int64, conselheiro Fornecedor, consultas int64) string {
	if conselheiro == "" {
		return fmt.Sprintf("%s=%d", executor, doExecutor)
	}
	if conselheiro == executor {
		return fmt.Sprintf("%s=%d (executor %d e conselheiro %d)", executor, doExecutor+consultas+1, doExecutor, consultas+1)
	}
	return fmt.Sprintf("%s=%d (executor), %s=%d (conselheiro)", executor, doExecutor, conselheiro, consultas+1)
}

// verificarContadorDoDia trata o contador do dia que não existe: só é o primeiro uso se não houver
// relatórios de corridas reais de hoje, de nenhum dos fornecedores dados; havendo, a contagem do dia
// perdeu-se e não se recomeça do zero — recusa, ou reconstrói-a com `reconstruir`. Devolve falso
// quando recusou (e já disse porquê).
func verificarContadorDoDia(caminhoDoContador string, pastasDeRelatorios []string, fornecedores []Fornecedor, dia string, reconstruir bool, stderr io.Writer) bool {
	if _, serr := os.Stat(caminhoDoContador); !errors.Is(serr, os.ErrNotExist) {
		return true
	}
	usos, total, n := map[Fornecedor]usoDoDia{}, int64(0), 0
	for _, f := range fornecedores {
		pedidos, microUSD, deste := UsoDosRelatoriosDeHoje(pastasDeRelatorios, f, dia)
		usos[f], total, n = usoDoDia{Pedidos: pedidos, MicroUSD: microUSD}, total+pedidos, n+deste
	}
	// A reconstrução leva também a contagem de hoje dos OUTROS fornecedores que aparecem nos
	// relatórios: o ficheiro é um só, e sem isto a linha de um terceiro fornecedor perdia-se.
	for _, f := range []Fornecedor{FornecedorKimi, FornecedorAnthropic, FornecedorOpenRouter} {
		if _, ja := usos[f]; !ja {
			if pedidos, microUSD, _ := UsoDosRelatoriosDeHoje(pastasDeRelatorios, f, dia); pedidos > 0 || microUSD > 0 {
				usos[f] = usoDoDia{Pedidos: pedidos, MicroUSD: microUSD}
			}
		}
	}
	if n == 0 {
		return true
	}
	if !reconstruir {
		fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", ErrContadorDesaparecido)
		return false
	}
	if rerr := reconstruirContadorDe(caminhoDoContador, dia, usos); rerr != nil {
		fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", rerr)
		return false
	}
	fmt.Fprintf(stderr, "aos-ensaio: contador RECONSTRUIDO a partir de %d relatorio(s) de hoje: %d pedidos\n", n, total)
	return true
}
