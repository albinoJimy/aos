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
	"path/filepath"
	"strings"
	"time"
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
	// SaidaParouAMeio — a corrida parou a meio (tecto atingido, contador, interrupção). O
	// relatório parcial foi escrito e diz a causa.
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
}

const usoDoBanco = `aos-ensaio — banco de ensaio da fronteira runtime-modelo (AOS-512). NAO toca em producao.

  aos-ensaio falso   [opcoes]                     provider falso em processo (sem rede, sem Docker)
  aos-ensaio proxy   --binario-do-falso F [opcoes] imagem real do proxy a frente do provider falso (Docker)
  aos-ensaio real    --chaves F --fornecedor kimi|anthropic [opcoes]
                                                   modelo real pelo proxy efemero (Docker); tectos do ficheiro
  aos-ensaio bateria                               mostra o digest e os casos da bateria

opcoes comuns:
  --experiencia bateria|separadores   (omissao: bateria)
  --amostras N                        passagens pela bateria (omissao 1); nos separadores sao 53 por braco
  --semente N                         fixa a ordem dos bracos (vai no relatorio)
  --braco A|B|C|D                     braco da bateria (omissao A)
  --saida PASTA                       onde escrever o relatorio
  --silencioso                        nao mostra o resumo, so os caminhos
so falso e proxy:   --roteiro cumpre,texto,...   --tecto-pedidos N --contador FICHEIRO
so real:            --modelo M  --precos FICHEIRO  --contador FICHEIRO  --so-plano
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
	cfg := CfgDaCorrida{Modo: modo, Plano: plano, Bateria: bateria, Relogio: amb.Relogio, Extra: map[string]string{}}
	if !o.silencioso {
		cfg.Progresso = stderr
	}
	pastaDaSaida := o.saida
	var (
		baseURL, credencial string
		fechar              = func() {}
	)
	defer func() { fechar() }()

	switch modo {
	case ModoFalso, ModoProxy:
		if o.chaves != "" || o.fornecedor != "" || o.precos != "" || o.soPlano {
			fmt.Fprintln(stderr, "aos-ensaio: --chaves, --fornecedor, --precos e --so-plano sao so do modo real")
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
		}
		if cfg.Contador != nil {
			if err := cfg.Contador.Cabe(previstos); err != nil {
				fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
				return SaidaRecusada
			}
		}
		if modo == ModoFalso {
			falso := NovoProviderFalso(roteiro)
			ouvinte, lerr := net.Listen("tcp", "127.0.0.1:0")
			if lerr != nil {
				fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
				return SaidaErro
			}
			srv := &http.Server{Handler: falso, ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = srv.Serve(ouvinte) }()
			fechar = func() { _ = srv.Close() }
			baseURL, credencial = "http://"+ouvinte.Addr().String()+"/v1", "credencial-do-modo-falso"
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
		vivo, lerr := lancadorDe(amb, red).Lancar(ctx, PedidoDeProxy{
			Prefixo: "openai", Modelo: "modelo-falso-do-banco", BinarioDoFalso: o.binario, Roteiro: roteiro,
		}.ComSegredos(chaveDoFalso, ""))
		if lerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
			return SaidaSemInfra
		}
		red.Acrescentar(vivo.ChaveMestra())
		fechar = vivo.Fechar
		baseURL, credencial = vivo.BaseURL, vivo.ChaveMestra()
		cfg.Rota.Digest = DigestDaRota(cfg.Rota.Fornecedor, cfg.Rota.Modelo, "proxy:"+ImagemDoProxy)

	case ModoReal:
		if o.roteiro != "" || o.tecto != 0 || o.binario != "" {
			fmt.Fprintln(stderr, "aos-ensaio: --roteiro, --tecto-pedidos e --binario-do-falso nao sao do modo real (o tecto vem do ficheiro de chaves)")
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
		rota, rerr := chaves.Rota(fornecedor, o.modelo)
		if rerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", rerr)
			return SaidaRecusada
		}
		red.Acrescentar(rota.Segredos()...)
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
		caminhoDoContador := o.contador
		if caminhoDoContador == "" {
			// Ao lado do ficheiro de chaves: na pasta do dono, fora do repositório.
			caminhoDoContador = filepath.Join(filepath.Dir(caminho), "contador.json")
		}
		// FAIL-CLOSED: sem tecto, sem preço para um tecto em dólares, ou com um contador que
		// não se lê, o contador não abre e a corrida não arranca.
		cfg.Contador, err = AbrirContador(caminhoDoContador, fornecedor, rota.Tectos, precos, rota.Modelo, amb.Relogio)
		if err != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
			return SaidaRecusada
		}
		if err := cfg.Contador.Cabe(previstos); err != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", err)
			return SaidaRecusada
		}
		cfg.Rota = RotaDoRelatorio{Fornecedor: string(fornecedor), Modelo: rota.Modelo, Digest: DigestDaRota(string(fornecedor), rota.Modelo, rota.apiBase)}
		cfg.RegiaoDeclarada = rota.RegiaoDeclarada
		if pastaDaSaida == "" {
			pastaDaSaida = filepath.Join(filepath.Dir(caminho), "relatorios")
		}
		estado, eerr := cfg.Contador.Estado()
		if eerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: RECUSADO — %v\n", eerr)
			return SaidaRecusada
		}
		fmt.Fprintf(stderr, "aos-ensaio: modo REAL — fornecedor %s, modelo %s; a corrida faz ate %d pedidos; hoje (%s UTC) restam %d de %d\n",
			fornecedor, rota.Modelo, previstos, estado.Dia, estado.PedidosRestantes, estado.TectoPedidos)
		if o.soPlano {
			fmt.Fprintln(stderr, "aos-ensaio: --so-plano: nenhum pedido foi enviado")
			return SaidaOK
		}
		prefixo := "openai"
		if fornecedor == FornecedorAnthropic {
			prefixo = "anthropic"
		}
		vivo, lerr := lancadorDe(amb, red).Lancar(ctx, PedidoDeProxy{Prefixo: prefixo, Modelo: rota.Modelo}.ComSegredos(rota.apiKey, rota.apiBase))
		if lerr != nil {
			fmt.Fprintf(stderr, "aos-ensaio: %v\n", lerr)
			return SaidaSemInfra
		}
		red.Acrescentar(vivo.ChaveMestra())
		fechar = vivo.Fechar
		baseURL, credencial = vivo.BaseURL, vivo.ChaveMestra()
	}

	if pastaDaSaida == "" {
		if amb.PastaDoDono == "" {
			fmt.Fprintln(stderr, "aos-ensaio: sem pasta do dono conhecida: indique --saida PASTA")
			return SaidaUso
		}
		pastaDaSaida = filepath.Join(amb.PastaDoDono, "relatorios")
	}
	no, err := NovoNoDeEnsaio(ctx, CfgDoNo{Bateria: bateria, BaseURL: baseURL, Credencial: credencial, Contador: cfg.Contador})
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
		fmt.Fprintf(stderr, "aos-ensaio: a corrida PAROU A MEIO (%s); o relatorio e parcial\n", relatorio.Terminou)
		return SaidaParouAMeio
	}
	return SaidaOK
}

func lancadorDe(amb Ambiente, red *Redactor) LancadorDeProxy {
	if amb.Lancador != nil {
		return amb.Lancador
	}
	return &LancadorDocker{Redactor: red}
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
	falso := NovoProviderFalso(roteiro)
	if o.chaveSha != "" {
		cru, err := hex.DecodeString(strings.TrimSpace(o.chaveSha))
		if err != nil || len(cru) != sha256.Size {
			fmt.Fprintln(stderr, "aos-ensaio: --chave-sha256 nao e um sha256 em hexadecimal")
			return SaidaUso
		}
		var soma [sha256.Size]byte
		copy(soma[:], cru)
		falso.ExigirChave(soma)
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
