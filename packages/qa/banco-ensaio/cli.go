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
	// recusada, conta sem saldo, limite de ritmo) ou a sonda não a deixou começar. O relatório
	// parcial foi escrito e diz a causa.
	SaidaParouAMeio = 4
	// SaidaEmCI — o modo com modelo real foi pedido em CI. Recusado, sem ler nada.
	SaidaEmCI = 5
	// SaidaSemInfra — falta o Docker ou a imagem do proxy, ou o proxy não arrancou.
	SaidaSemInfra = 6
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
}

const usoDoBanco = `aos-ensaio — banco de ensaio da fronteira runtime-modelo (AOS-512). NAO toca em producao.

  aos-ensaio falso   [opcoes]                     provider falso em processo (sem rede, sem Docker)
  aos-ensaio proxy   --binario-do-falso F [opcoes] imagem real do proxy a frente do provider falso (Docker)
  aos-ensaio real    --chaves F --fornecedor kimi|anthropic [opcoes]
                                                   modelo real pelo proxy efemero (Docker); tectos do ficheiro
  aos-ensaio bateria                               mostra o digest e os casos da bateria
  aos-ensaio limpar  [--chaves F | --contador F]   remove contentores e redes aos512-* que tenham ficado de uma
                                                   corrida anterior (um proxy orfao guarda a chave no ambiente)
                                                   e a trava do contador de um processo que ja morreu

opcoes comuns:
  --experiencia bateria|separadores   (omissao: bateria)
  --amostras N                        passagens pela bateria (omissao 1); nos separadores sao 53 por braco
  --semente N                         fixa a ordem dos bracos (vai no relatorio)
  --braco A|B|C|D                     braco da bateria (omissao A)
  --saida PASTA                       onde escrever o relatorio
  --silencioso                        nao mostra o resumo, so os caminhos
  --pausa DURACAO                     intervalo entre dois passos (ex.: 2s), para um limite de taxa
  --perfil FICHEIRO                   perfil de rota CANDIDATO em JSON (AOS-513): parametros do pedido, versao
                                      da projeccao, classe de estado; o digest vai no relatorio
  --host-esperado HOST                so com um perfil que devolve estado: o host do endpoint que o proxy deve
                                      declarar ter servido (sem ele compara-se so o modelo servido)
so falso e proxy:   --roteiro cumpre,texto,...   --tecto-pedidos N --contador FICHEIRO
                    --estado exige|proibe        o provider falso do ESTADO OPACO (AOS-516) em vez do do roteiro:
                                                 exige de volta o estado que emitiu em cada turno com tools, ou
                                                 recusa qualquer estado; no modo proxy pede --perfil com um
                                                 expected_model anthropic/<modelo> e a rota do proxy e essa
                    --turnos-do-falso N          turnos com tool call do provider falso do estado (omissao 2)
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
	fs.Usage = func() { fmt.Fprint(stderr, usoDoBanco) }
	if err := fs.Parse(resto); err != nil {
		return SaidaUso
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "aos-ensaio: argumento a mais: %q\n", fs.Arg(0))
		return SaidaUso
	}

	bateria, err := CarregarBateria()
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
	if o.pausa < 0 || o.pausa > time.Minute {
		fmt.Fprintln(stderr, "aos-ensaio: --pausa tem de estar entre 0 e 1m")
		return SaidaUso
	}
	cfg := CfgDaCorrida{Modo: modo, Plano: plano, Bateria: bateria, Relogio: amb.Relogio, Extra: map[string]string{}, Pausa: o.pausa}
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
	}
	// A QUALIFICAÇÃO DA DEVOLUÇÃO (AOS-516). O host esperado só tem leitor quando o perfil devolve
	// estado: fora disso seria uma opção sem efeito, e recusa-se em vez de a ignorar.
	devolve := perfil != nil && perfil.StateReturn != modelgateway.StateReturnNever
	o.hostEsperado = strings.ToLower(strings.TrimSpace(o.hostEsperado))
	if o.hostEsperado != "" && (!devolve || !hostAceite(o.hostEsperado)) {
		fmt.Fprintln(stderr, "aos-ensaio: --host-esperado e um nome de host (sem esquema nem caminho) e so vale com um --perfil que devolva estado")
		return SaidaUso
	}
	if o.estado != "" && o.estado != EstadoExige && o.estado != EstadoProibe {
		fmt.Fprintf(stderr, "aos-ensaio: --estado aceita %s ou %s\n", EstadoExige, EstadoProibe)
		return SaidaUso
	}
	if o.turnosDoFalso < 0 || o.turnosDoFalso > 15 || (o.turnosDoFalso != 0 && o.estado == "") || o.exigeID {
		fmt.Fprintln(stderr, "aos-ensaio: --turnos-do-falso (1 a 15) so vale com --estado; --exige-id e so do subcomando interno falso-provider")
		return SaidaUso
	}
	if devolve {
		cfg.Extra["host_esperado"] = fmt.Sprint(o.hostEsperado != "")
	}
	if !o.silencioso {
		cfg.Progresso = stderr
	}
	pastaDaSaida := o.saida
	var (
		baseURL, segredoDaRota string
		fechar                 = func() {}
	)
	defer func() { fechar() }()

	// O prazo máximo de vida dos contentores, derivado do plano: o pior caso de cada pedido (o
	// timeout de 120 s mais a pausa) vezes o máximo de pedidos, com folga para o arranque. É a
	// última rede: antes dele actua o vigia do sinal de vida.
	vida := time.Duration(previstos)*(120*time.Second+o.pausa) + 15*time.Minute
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
		if o.chaves != "" || o.fornecedor != "" || o.precos != "" || o.soPlano || o.foraDaLista != "" || o.reconstruir {
			fmt.Fprintln(stderr, "aos-ensaio: --chaves, --fornecedor, --precos, --so-plano, --destino-fora-da-lista e --reconstruir-contador sao so do modo real")
			return SaidaUso
		}
		if o.estado != "" && o.roteiro != "" {
			fmt.Fprintln(stderr, "aos-ensaio: --estado e --roteiro escolhem o provider falso: so um deles")
			return SaidaUso
		}
		roteiro := RoteiroPorOmissao()
		if o.roteiro != "" {
			if roteiro, err = LerRoteiro(o.roteiro); err != nil {
				fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
				return SaidaUso
			}
		}
		cfg.Extra["roteiro"] = EscreverRoteiro(roteiro)
		if o.estado != "" {
			// O provider falso do estado não tem roteiro: o que decide a corrida é o modo dele.
			delete(cfg.Extra, "roteiro")
			cfg.Extra["falso_do_estado"] = fmt.Sprintf("%s/%d", o.estado, o.turnosDoFalso)
		}
		cfg.Rota = RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco"}
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
			if err := cfg.Contador.Cabe(previstos); err != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
				return SaidaRecusada
			}
		}
		if modo == ModoFalso {
			var falso http.Handler = NovoProviderFalso(roteiro)
			if o.estado != "" {
				// Sem proxy, é o falso que declara o modelo servido — com o nome que o perfil
				// espera. Sem perfil não declara nada.
				doEstado := &FalsoDeEstado{Proibe: o.estado == EstadoProibe, Turnos: o.turnosDoFalso}
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
		pedidoDeProxy := PedidoDeProxy{Prefixo: "openai", Modelo: "modelo-falso-do-banco", BinarioDoFalso: o.binario, Roteiro: roteiro, Vida: vida}
		if o.estado != "" {
			// A rota do proxy é a que o perfil ESPERA — é esse o nome que o proxy vai declarar ter
			// servido —, e tem de ser uma rota anthropic/…: é o caminho de tradução que se mede.
			modelo, eAnthropic := "", false
			if perfil != nil {
				modelo, eAnthropic = strings.CutPrefix(perfil.ExpectedModel, "anthropic/")
			}
			if !eAnthropic || !nomeDeModeloAceite(modelo) {
				fmt.Fprintln(stderr, "aos-ensaio: o modo proxy com --estado exige --perfil com expected_model anthropic/<modelo> (a rota openai/ atras do proxy mede-se em scripts/ci/wire-live.sh)")
				return SaidaUso
			}
			pedidoDeProxy.Prefixo, pedidoDeProxy.Modelo, pedidoDeProxy.Roteiro = "anthropic", modelo, nil
			pedidoDeProxy.Estado, pedidoDeProxy.TurnosDoFalso = o.estado, o.turnosDoFalso
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
		prefixo := "openai"
		if fornecedor == FornecedorAnthropic {
			prefixo = "anthropic"
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
		}
		// Ao lado do ficheiro de chaves: na pasta do dono, fora do repositório. Não há flag.
		caminhoDoContador := filepath.Join(filepath.Dir(caminho), "contador.json")
		if pastaDaSaida == "" {
			pastaDaSaida = filepath.Join(filepath.Dir(caminho), "relatorios")
		}
		if _, serr := os.Stat(caminhoDoContador); errors.Is(serr, os.ErrNotExist) {
			// Um contador que não existe só é o primeiro uso se não houver relatórios de corridas
			// reais de hoje: havendo, a contagem do dia perdeu-se, e não se recomeça do zero.
			dia := amb.Relogio().UTC().Format("2006-01-02")
			pedidos, microUSD, n := UsoDosRelatoriosDeHoje([]string{filepath.Join(filepath.Dir(caminho), "relatorios"), pastaDaSaida}, fornecedor, dia)
			if n > 0 {
				if !o.reconstruir {
					fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", ErrContadorDesaparecido)
					return SaidaRecusada
				}
				if rerr := ReconstruirContador(caminhoDoContador, fornecedor, dia, pedidos, microUSD); rerr != nil {
					fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", rerr)
					return SaidaRecusada
				}
				fmt.Fprintf(stderr, "aos-ensaio: contador RECONSTRUIDO a partir de %d relatorio(s) de hoje: %d pedidos\n", n, pedidos)
			}
		}
		// FAIL-CLOSED: sem tecto, sem preço para um tecto em dólares, com um contador que não
		// se lê ou em uso por outro processo, o contador não abre e a corrida não arranca.
		cfg.Contador, err = AbrirContador(caminhoDoContador, fornecedor, rota.Tectos, precos, rota.Modelo, amb.Relogio)
		if err != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
			return SaidaRecusada
		}
		defer cfg.Contador.Fechar()
		// Mais um: a sonda antes da corrida conta no tecto como um pedido.
		if err := cfg.Contador.Cabe(previstos + 1); err != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
			return SaidaRecusada
		}
		cfg.Rota = RotaDoRelatorio{Fornecedor: string(fornecedor), Modelo: rota.Modelo, Digest: DigestDaRota(string(fornecedor), rota.Modelo, rota.apiBase)}
		cfg.RegiaoDeclarada = rota.RegiaoDeclarada
		cfg.AbortarAposRecusasDeChave = RecusasDeChaveQueAbortam
		cfg.AbortarApos429Iniciais, cfg.AbortarAposSerieDe429 = Respostas429QueAbortam, SerieDe429QueAborta
		cfg.Sondar = true
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
		if o.soPlano {
			fmt.Fprintln(stderr, "aos-ensaio: --so-plano: nenhum pedido foi enviado")
			return SaidaOK
		}
		vivo, lerr := lancadorDe(amb, red).Lancar(ctx, PedidoDeProxy{Prefixo: prefixo, Modelo: rota.Modelo, Vida: vida}.ComSegredos(rota.apiKey, rota.apiBase))
		if lerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
			return SaidaSemInfra
		}
		red.Acrescentar(vivo.ChaveMestra())
		fechar = vivo.Fechar
		baseURL, segredoDaRota = vivo.BaseURL, vivo.ChaveMestra()
	}

	if pastaDaSaida == "" {
		if amb.PastaDoDono == "" {
			fmt.Fprintln(stderr, "aos-ensaio: sem pasta do dono conhecida: indique --saida PASTA")
			return SaidaUso
		}
		pastaDaSaida = filepath.Join(amb.PastaDoDono, "relatorios")
	}
	no, err := NovoNoDeEnsaio(ctx, CfgDoNo{Bateria: bateria, BaseURL: baseURL, Credencial: segredoDaRota, Contador: cfg.Contador, Perfil: perfil, HostEsperado: o.hostEsperado})
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaErro
	}
	defer no.Fechar()
	cfg.No = no

	relatorio, err := Correr(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
		if errors.Is(err, ErrNaoCabe) || errors.Is(err, ErrModoRealSemTecto) || errors.Is(err, ErrContador) {
			return SaidaRecusada
		}
		return SaidaErro
	}
	emJSON, emTexto, err := EscreverRelatorio(pastaDaSaida, relatorio)
	if err != nil {
		fmt.Fprintf(stderr, "aos-ensaio: %v\n", err)
		return SaidaErro
	}
	if !o.silencioso {
		fmt.Fprint(stdout, ResumoEmTexto(relatorio))
	}
	fmt.Fprintf(stdout, "relatorio: %s\nresumo:    %s\n", emJSON, emTexto)
	if relatorio.Terminou != TerminouCompleta {
		if relatorio.Sonda != nil && relatorio.Sonda.Resultado != SondaOK {
			fmt.Fprintf(stderr, "aos-ensaio: a SONDA nao teve 200 (HTTP %d, %s): a corrida NAO COMECOU; gastou-se no maximo 1 pedido do tecto\n", relatorio.Sonda.HTTP, relatorio.Sonda.Resultado)
		}
		fmt.Fprintf(stderr, "aos-ensaio: a corrida PAROU A MEIO (%s); o relatorio e parcial\n", relatorio.Terminou)
		if conselho := conselhoDaParagem(relatorio); conselho != "" {
			fmt.Fprintf(stderr, "aos-ensaio: %s\n", conselho)
		}
		return SaidaParouAMeio
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
	}
	return Plano{}, fmt.Errorf("banco-ensaio: experiencia desconhecida: %q (aceites: bateria, separadores)", o.experiencia)
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
	doRoteiro, doEstado := NovoProviderFalso(roteiro), &FalsoDeEstado{Proibe: o.estado == EstadoProibe, Turnos: o.turnosDoFalso, ExigeID: o.exigeID}
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
