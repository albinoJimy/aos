package bancoensaio

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/identity"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline/authn"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/substrate/eventstore"
)

// O NÓ DE ENSAIO. Compõe, com as peças de produção, o caminho que um turno de modelo percorre
// no nó `aos`:
//
//	Agent Runtime (loop, tail, veredicto) → adaptador RT→GW (projecção nativa do tail)
//	  → Model Gateway de produção (identidade, allowlist, roteamento, adaptador HTTP, ficha)
//	  → HTTP → a rota (provider falso, proxy real, ou o fornecedor por trás do proxy)
//
// e as tool calls pelo Reference Monitor. O que é do banco fica em dois pontos, à volta e não
// no meio: [portaDeEnsaio] (decorador da porta do gateway: variantes e observação) e
// [transporteContado] (transporte HTTP: o tecto do dia, antes de cada pedido).

const (
	// regiaoDoEnsaio e boardDoEnsaio são a fronteira de soberania do nó de ensaio. São nomes
	// do banco: a allowlist que os admite é efémera, criada e assinada no arranque do nó de
	// ensaio, e não é a de produção.
	regiaoDoEnsaio = "eu"
	boardDoEnsaio  = "board-ensaio"
	// capacidadeDeModelo é a capability que o estágio de identidade do gateway exige.
	capacidadeDeModelo = "model:invoke"
	classeDoEnsaio     = "banco-ensaio"
	emissorDoEnsaio    = "iss:banco-ensaio"
)

// AliasDaRota é o nome de modelo que o nó de ensaio PEDE. No modo `falso` o provider ignora-o;
// atrás do proxy é o `model_name` da rota efémera, que o proxy traduz no modelo do fornecedor.
// Não tem pontos: um nome de modelo entra num nome de stream do Event Store (AOS-425).
const AliasDaRota = "rota-de-ensaio"

// CfgDoNo configura o nó de ensaio.
type CfgDoNo struct {
	Bateria *Bateria
	// BaseURL é a raiz da API compatível OpenAI da rota (`http://host:porta/v1`).
	BaseURL string
	// Credencial é o que o gateway apresenta à rota como `Bearer`. É um segredo.
	Credencial string
	// Contador, quando presente, conta cada pedido contra o tecto do dia antes de o enviar.
	// Obrigatório no modo com modelo real (quem o exige é [Correr]).
	Contador *Contador
	// Timeout de cada pedido HTTP. Zero ⇒ 120 s.
	Timeout time.Duration
	// Transporte é o transporte HTTP de base. nil ⇒ [http.DefaultTransport].
	Transporte http.RoundTripper
	// Perfil é um PERFIL DE ROTA CANDIDATO (AOS-513): o que se quer qualificar antes de o dono o
	// assinar para produção — parâmetros do pedido, versão da projecção, classe de estado. O seu
	// nome pedido tem de ser [AliasDaRota]. nil ⇒ a rota de ensaio não tem perfil, como sempre.
	// Não precisa de existir na tabela de perfis do nó: é esse o ponto.
	Perfil *modelgateway.RouteProfile
	// HostEsperado é o host do endpoint que o proxy deve declarar ter servido, para a governação
	// da rota (AOS-505). Só é lido quando o perfil devolve estado (AOS-516). Vem de quem corre o
	// ensaio (`--host-esperado`); vazio ⇒ o endpoint não é comparado, só o modelo servido.
	HostEsperado string
}

// ErrPerfilDoEnsaio — o perfil candidato não serve o nó de ensaio.
var ErrPerfilDoEnsaio = errors.New("banco-ensaio: o perfil candidato tem de ter como nome pedido o alias da rota de ensaio (" + AliasDaRota + ")")

// LerPerfilCandidato lê um perfil de rota candidato de JSON, pela leitura fechada do gateway
// ([modelgateway.ParseRouteProfile]: uma chave ou um valor fora do conjunto recusa), e confere
// que é o da rota de ensaio.
func LerPerfilCandidato(doc []byte) (*modelgateway.RouteProfile, error) {
	p, err := modelgateway.ParseRouteProfile(doc)
	if err != nil {
		return nil, err
	}
	if p.Requested != AliasDaRota {
		return nil, ErrPerfilDoEnsaio
	}
	return &p, nil
}

// NoDeEnsaio é o nó de ensaio montado.
type NoDeEnsaio struct {
	bateria  *Bateria
	porta    *portaDeEnsaio
	mon      *referencemonitor.Monitor
	recusa   *recusaDeDocumento
	store    *eventstore.Store
	runtimes map[Braco]*agentruntime.Runtime
	emissor  *identity.Issuer
	contador *Contador
	// base, credencial e cliente servem a sonda ([NoDeEnsaio.Sondar]): a mesma rota, a mesma
	// credencial e o mesmo transporte contado dos pedidos da corrida.
	base, credencial string
	cliente          *http.Client
	// devolucao é a composição do estado opaco (AOS-516); nil quando o perfil não devolve estado.
	devolucao *ComposicaoDoEstado

	mu      sync.Mutex
	motivos map[string][]string
	ordem   int
}

type chaveDeContexto int

const (
	chaveDoBraco chaveDeContexto = iota
	chaveDasToolsDoRun
	chaveDaChamada
	chaveDoToken
)

type credencialEstatica struct{ segredo string }

func (c credencialEstatica) Fetch(context.Context, string, string) (string, error) {
	if c.segredo == "" {
		return "", errors.New("banco-ensaio: credencial da rota vazia")
	}
	return c.segredo, nil
}

type autoridadeDoEnsaio struct{}

func (autoridadeDoEnsaio) UserAuthority(context.Context, string) ([]string, error) {
	return []string{capacidadeDeModelo}, nil
}

func (autoridadeDoEnsaio) ClassAuthority(context.Context, string) ([]string, error) {
	return []string{capacidadeDeModelo}, nil
}

// allowlistDoEnsaio cria e assina, com uma chave efémera, a allowlist regional que admite o
// alias da rota de ensaio. A governação é a de produção (default-deny, assinatura verificada,
// activação selada); só o catálogo é do banco.
func allowlistDoEnsaio() (*allowlist.Policy, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	doc, err := json.Marshal(map[string]any{
		"version": "banco-ensaio/v1",
		"default": "deny",
		"rules": []any{map[string]any{
			"id": "rota-de-ensaio", "board": boardDoEnsaio,
			"models": []string{AliasDaRota}, "regions": []string{regiaoDoEnsaio},
		}},
	})
	if err != nil {
		return nil, err
	}
	digest, err := allowlist.Digest(doc)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, []byte(digest))
	return allowlist.LoadSignedPolicy(doc, base64.StdEncoding.EncodeToString(sig), base64.StdEncoding.EncodeToString(pub))
}

// NovoNoDeEnsaio monta o nó de ensaio. Fail-closed: qualquer peça que não se componha ⇒ erro,
// e nenhum pedido sai.
func NovoNoDeEnsaio(ctx context.Context, cfg CfgDoNo) (*NoDeEnsaio, error) {
	if cfg.Bateria == nil {
		return nil, errors.New("banco-ensaio: no de ensaio sem bateria")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, errors.New("banco-ensaio: no de ensaio sem endereco da rota")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	transporte := cfg.Transporte
	if transporte == nil {
		transporte = http.DefaultTransport
	}

	// Identidade: um emissor efémero cunha o token NHI do nó de ensaio, com `model:invoke`. O
	// estágio de identidade do gateway é o de produção e verifica-o em cada pedido.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	iss, err := identity.NewIssuer(emissorDoEnsaio, priv, map[string]identity.ClassPolicy{
		classeDoEnsaio: {TTL: time.Hour, Scope: []string{capacidadeDeModelo}},
	})
	if err != nil {
		return nil, fmt.Errorf("banco-ensaio: emissor de identidade: %w", err)
	}
	politica, err := authn.LoadPolicy()
	if err != nil {
		return nil, fmt.Errorf("banco-ensaio: politica do estagio de identidade: %w", err)
	}
	verificador := identity.NewVerifier(identity.WithTrustedIssuer(emissorDoEnsaio, iss.PublicKey()))
	pol, err := allowlistDoEnsaio()
	if err != nil {
		return nil, fmt.Errorf("banco-ensaio: allowlist do no de ensaio: %w", err)
	}

	// O perfil candidato (AOS-513): o MESMO conjunto para o gateway (os parâmetros do pedido) e
	// para o adaptador do runtime (a versão da projecção).
	var candidatos []modelgateway.RouteProfile
	if cfg.Perfil != nil {
		if cfg.Perfil.Requested != AliasDaRota {
			return nil, ErrPerfilDoEnsaio
		}
		candidatos = append(candidatos, *cfg.Perfil)
	}
	perfis, err := modelgateway.NewRouteProfileSet(candidatos...)
	if err != nil {
		return nil, fmt.Errorf("banco-ensaio: perfil candidato: %w", err)
	}

	porta := &portaDeEnsaio{contador: cfg.Contador}
	// Com `devolver_em: topo` o perfil manda repor no topo da mensagem o estado que veio no saco
	// do proxy: esse estado volta onde o fornecedor o lê, e a guarda do saco não se aplica.
	porta.sacoVoltaNoTopo = cfg.Perfil != nil && cfg.Perfil.StateReturnAt == modelgateway.StateReturnAtTop
	// A DEVOLUÇÃO DO ESTADO OPACO (AOS-516). Só quando o perfil candidato a declara: sem isso os
	// três campos abaixo ficam a zero e o gateway, o adaptador e o layout são os de sempre.
	devolucao := composicaoDoEstado(cfg.Perfil, cfg.HostEsperado)
	var (
		rota     modelgateway.RouteGovernance
		captura  string
		verSaida func(modelgateway.StateReturnObservation)
	)
	if devolucao != nil {
		rota = modelgateway.RouteGovernance{Mode: devolucao.GovernacaoDaRota, ExpectedAPIHost: cfg.HostEsperado}
		captura, verSaida = devolucao.Captura, porta.verDevolucao
	}

	cliente := &http.Client{Timeout: timeout, Transport: &transporteContado{base: transporte, contador: cfg.Contador}}
	gw, err := modelgateway.NewProduction(ctx, modelgateway.ProductionConfig{
		Route:               rota,
		ProviderState:       captura,
		StateReturnObserver: verSaida,
		RouteProfiles:       candidatos,
		Provider:            "openai",
		BaseURL:             base,
		HTTPClient:          cliente,
		DefaultRegion:       regiaoDoEnsaio,
		Authn:               authn.New(verificador, autoridadeDoEnsaio{}, politica),
		Audit:               audit.NewMemStore(),
		Credentials:         credencialEstatica{segredo: cfg.Credencial},
		Accounts:            []modelgateway.InfraAccount{{KeyID: "rota-de-ensaio", Provider: "openai", Region: regiaoDoEnsaio}},
		Allowlist:           pol,
		// A ficha da forma da resposta (AOS-507) vem do adaptador de produção, sem cópia.
		ResponseShape: modelgateway.ResponseShapeObserve,
	})
	if err != nil {
		return nil, fmt.Errorf("banco-ensaio: compor o Model Gateway: %w", err)
	}

	recusa := novaRecusaDeDocumento()
	mon := referencemonitor.New(referencemonitor.WithHooks(append(referencemonitor.DefaultHooks(), recusa)...))
	if err := registarTools(mon, cfg.Bateria); err != nil {
		return nil, fmt.Errorf("banco-ensaio: registar as tools: %w", err)
	}
	store, err := eventstore.New()
	if err != nil {
		return nil, err
	}

	n := &NoDeEnsaio{
		bateria: cfg.Bateria, porta: porta, devolucao: devolucao,
		mon: mon, recusa: recusa, store: store, emissor: iss, contador: cfg.Contador,
		base: base, credencial: cfg.Credencial, cliente: cliente,
		runtimes: map[Braco]*agentruntime.Runtime{}, motivos: map[string][]string{},
	}
	porta.dentro = gw
	layout := agentruntime.AssemblyVersion140
	var doEstado []modelgateway.RuntimeAdapterOption
	if devolucao != nil {
		layout = devolucao.Layout
		doEstado = append(doEstado, modelgateway.WithProviderStateCapture(modelgateway.DefaultProviderStateMaxBytes, porta.verCaptura))
	}
	var doNo []port.Tool
	for _, t := range ToolsDoBanco() {
		doNo = append(doNo, port.Tool{Type: "function", Function: port.FunctionDef{Name: t.Nome, Description: t.Descricao, Parameters: t.Parametros}})
	}
	for _, b := range Bracos() {
		mc := modelgateway.NewModelClient(n.porta, AliasDaRota, append([]modelgateway.RuntimeAdapterOption{
			modelgateway.WithTools(doNo),
			// O token NHI é cunhado POR RUN (o emissor limita o TTL a uma hora, e uma corrida
			// pode durar mais): chega ao estágio de identidade do gateway pelo contexto da chamada.
			modelgateway.WithPrincipalFromContext(func(ctx context.Context) string {
				tok, _ := ctx.Value(chaveDoToken).(string)
				return tok
			}),
			modelgateway.WithRegionBoard(regiaoDoEnsaio, boardDoEnsaio),
			modelgateway.WithProjection(modelgateway.ProjectionNative),
			modelgateway.WithProjectionVersion(VersaoPublicadaDoBraco(b)),
			// AOS-513: a versão que o perfil candidato declare prevalece sobre a do braço.
			modelgateway.WithRouteProfileSet(perfis),
			modelgateway.WithToolOfferFromContext(func(ctx context.Context) (func(string) bool, bool) {
				nomes, _ := ctx.Value(chaveDasToolsDoRun).(map[string]bool)
				return func(nome string) bool { return nomes[nome] }, true
			}),
		}, doEstado...)...)
		n.runtimes[b] = agentruntime.New(mc, mon, agentruntime.NewTurnRecorder(store),
			agentruntime.WithAssemblyVersion(layout),
			agentruntime.WithStopReasonStats(n.contarMotivo),
		)
	}
	return n, nil
}

// Fechar liberta o Event Store em memória do nó de ensaio.
func (n *NoDeEnsaio) Fechar() { _ = n.store.Close() }

func (n *NoDeEnsaio) contarMotivo(runID string, motivo agentruntime.StopReason) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.motivos[runID] = append(n.motivos[runID], string(motivo.Normalizado()))
}

// PedidoDeRun é um run a correr no nó de ensaio: um nó de um caso, num braço.
type PedidoDeRun struct {
	Caso      Caso
	No        No
	Braco     Braco
	Amostra   int
	Tentativa int
	// Entrada é o `plan_input` do run (a saída de um nó anterior, ou um documento da bateria).
	Entrada       []byte
	OrigemEntrada string
	// AvisoDeNovaTentativa põe no tail o aviso de nova tentativa do kernel (AOS-506).
	AvisoDeNovaTentativa bool
	// MaxTurnos limita os turnos do run (e, por isso, os pedidos). Zero ⇒ [MaxTurnosPorOmissao].
	MaxTurnos int
}

// MaxTurnosPorOmissao é o tecto de turnos de um run da bateria: o caso mais longo precisa de
// dois (pedir as tools, responder), e quatro deixam margem para o modelo repetir uma leitura.
const MaxTurnosPorOmissao = 4

// Correr corre um run e devolve a sua observação e a saída do run. A saída é texto do modelo:
// fica em memória, serve para alimentar o nó seguinte do caso e para a verificação de factos,
// e NUNCA vai para o relatório.
func (n *NoDeEnsaio) Correr(ctx context.Context, p PedidoDeRun) (Observacao, string) {
	obs := Observacao{
		Caso: p.Caso.ID, No: p.No.ID, Braco: string(p.Braco), Amostra: p.Amostra, Tentativa: p.Tentativa,
		ExigeTool: len(p.No.Exige) > 0, Factos: FactosNaoAplicavel,
	}
	rt, ok := n.runtimes[p.Braco]
	if !ok {
		obs.Desfecho = DesfechoErroOutro
		return obs, ""
	}
	// O número de ordem torna o RunID único neste nó de ensaio mesmo que o mesmo passo corra
	// duas vezes (duas corridas sobre o mesmo nó): cada run tem o seu stream no Event Store.
	n.mu.Lock()
	n.ordem++
	ordem := n.ordem
	n.mu.Unlock()
	runID := fmt.Sprintf("ensaio-%s-%s-%s-%05d-t%d-r%d", p.Caso.ID, p.No.ID, p.Braco, p.Amostra, p.Tentativa, ordem)
	doRun := map[string]bool{}
	var specs []agentruntime.ToolSpec
	for _, t := range ToolsDoBanco() {
		for _, nome := range p.No.Tools {
			if nome == t.Nome {
				doRun[nome] = true
				specs = append(specs, agentruntime.ToolSpec{Name: t.Nome, Version: "1.0.0", Digest: digestDaTool(t)})
			}
		}
	}
	if p.No.Nega != "" {
		n.recusa.negar(runID, p.No.Nega)
	}
	tok, terr := n.emissor.Issue(ctx, identity.IssueRequest{
		UserID: "human:banco-ensaio", AgentID: "agt-banco-ensaio", AgentClass: classeDoEnsaio,
		UserAuthority: []string{capacidadeDeModelo},
	})
	if terr != nil {
		obs.Desfecho = DesfechoErroOutro
		return obs, ""
	}
	maxTurnos := p.MaxTurnos
	if maxTurnos <= 0 {
		maxTurnos = MaxTurnosPorOmissao
	}
	goal := agentruntime.Goal{
		RunID:              runID,
		Principal:          referencemonitor.Principal{NHIID: "nhi:banco-ensaio"},
		Credential:         tok.Compact,
		Model:              agentruntime.ModelConfig{ModelID: AliasDaRota},
		System:             n.bateria.System,
		Tools:              specs,
		Objective:          p.No.Objectivo,
		MaxTurns:           maxTurnos,
		CompletionRequires: p.No.Exige,
		CompletionMode:     agentruntime.CompletionEnforce,
	}
	if len(p.Entrada) > 0 {
		soma := sha256.Sum256(p.Entrada)
		goal.Inputs = []agentruntime.PlanInput{{
			From: p.OrigemEntrada, Output: "saida",
			Digest: "sha256:" + hex.EncodeToString(soma[:]), Content: p.Entrada,
		}}
	}
	if p.AvisoDeNovaTentativa {
		goal.RetryNotice = agentruntime.RetryNoticeNoFunctionCall
	}

	n.porta.abrir(runID)
	ctx = context.WithValue(ctx, chaveDoBraco, p.Braco)
	ctx = context.WithValue(ctx, chaveDoToken, tok.Compact)
	ctx = context.WithValue(ctx, chaveDasToolsDoRun, doRun)
	res, err := rt.Run(ctx, goal)

	chamadas, capturas := n.porta.fechar(runID)
	// haviaRaciocinio: algum turno ANTERIOR do run trouxe raciocínio ou assinatura (AOS-516).
	haviaRaciocinio := false
	n.mu.Lock()
	obs.MotivosDeParagem = n.motivos[runID]
	delete(n.motivos, runID)
	n.mu.Unlock()

	// Os turnos e as tool calls contam-se pelo que o banco VIU passar na porta do gateway, e
	// não pelo [agentruntime.Result]: um run que falha a meio (o provider recusa o segundo
	// pedido) devolve um Result vazio, e o primeiro turno — com a sua tool call — aconteceu.
	obs.UltimoDesfechoDeTool = res.LastToolOutcome
	if n.devolucao != nil {
		obs.Estado = novoEstadoDoRun(capturas)
	}
	var ultimoTexto string
	for i, c := range chamadas {
		obs.Pedidos++
		obs.HTTP = append(obs.HTTP, c.status)
		if c.tipoDeErro != "" {
			obs.TiposDeErro = append(obs.TiposDeErro, c.tipoDeErro)
		}
		obs.Fichas = append(obs.Fichas, c.ficha)
		obs.TokensDeEntrada += c.tokensDeEntrada
		obs.TokensDeSaida += c.tokensDeSaida
		obs.ToolCalls += c.toolCalls
		if c.erro == "" {
			obs.Turnos++
		}
		if c.levaMensagemTool && obs.SegundoTurno == SegundoTurnoNaoTentado {
			obs.SegundoTurno = SegundoTurnoRecusado
			if c.erro == "" && c.status == http.StatusOK {
				obs.SegundoTurno = SegundoTurnoAceite
			}
		}
		if obs.Estado != nil {
			obs.Estado.contarPedido(c, haviaRaciocinio)
		}
		haviaRaciocinio = haviaRaciocinio || c.classeDoEstado == estadoComRaciocinio
		if i == len(chamadas)-1 {
			ultimoTexto = c.texto
			obs.erroDaUltimaChamada = c.erro
		}
	}
	semChamada := obs.ExigeTool && err == nil && obs.ToolCalls == 0 && res.ToolCallsRequested == 0 &&
		len(obs.MotivosDeParagem) > 0 && obs.MotivosDeParagem[len(obs.MotivosDeParagem)-1] == string(agentruntime.StopStop)
	obs.SemToolCall = semChamada
	if semChamada {
		// A HEURÍSTICA DECLARADA, só para contagem: o texto do turno contém o nome exacto de uma
		// tool oferecida. Não é um parser e nada corre por causa dela — o runtime continua a não
		// ler tool calls em texto.
		for nome := range doRun {
			if strings.Contains(ultimoTexto, nome) {
				obs.NomeDaToolNoTexto = true
			}
		}
	}

	switch {
	case errors.Is(err, ErrTectoAtingido):
		obs.Desfecho = DesfechoTectoAtingido
	case errors.Is(err, ErrContador):
		obs.Desfecho = DesfechoContador
	case errors.Is(err, agentruntime.ErrMaxTurnsExceeded):
		obs.Desfecho = DesfechoTurnosEsgotados
	case err != nil:
		obs.Desfecho = desfechoDoErro(obs.erroDaUltimaChamada)
	case res.Unfulfilled && res.Verdict != nil && res.Verdict.Reason.NoVocabulario() && res.Verdict.Reason != agentruntime.OutcomeFulfilled:
		obs.Desfecho = string(res.Verdict.Reason)
	case res.Terminated:
		obs.Desfecho = DesfechoCumprido
		if len(p.No.Factos) > 0 {
			obs.Factos = FactosPresentes
			for _, f := range p.No.Factos {
				if !strings.Contains(res.FinalText, f) {
					obs.Factos = FactosAusentes
				}
			}
		}
		return obs, res.FinalText
	default:
		obs.Desfecho = DesfechoErroOutro
	}
	return obs, ""
}

// chamadaObservada é o que o banco guarda de UM pedido ao gateway. O texto fica em memória, só
// para a heurística do nome da tool; nunca sai daqui para um relatório.
type chamadaObservada struct {
	levaMensagemTool bool
	status           int
	// tipoDeErro é o tipo, em vocabulário fechado, de um pedido com resposta HTTP sem 200.
	tipoDeErro      string
	erro            string
	ficha           Ficha
	tokensDeEntrada int64
	tokensDeSaida   int64
	toolCalls       int
	texto           string
	// devolucao e causaDaDevolucao são o que o gateway reportou da devolução do estado opaco
	// neste pedido ([modelgateway.StateReturnObservation]); vazios quando não reportou nada.
	devolucao, causaDaDevolucao string
	// tentativas são os códigos HTTP de CADA tentativa de transporte do pedido, pela ordem (0 =
	// sem resposta HTTP); naoEnviados, as tentativas que o tecto do dia não deixou sair.
	tentativas  []int
	naoEnviados int
	// classeDoEstado é o que a resposta trouxe de estado opaco ([classeDoEstadoDaResposta]).
	classeDoEstado string
}

// portaDeEnsaio é o decorador da porta do gateway: aplica o braço às mensagens que a projecção
// de produção produziu e observa cada pedido. Tudo o resto delega no gateway de produção.
type portaDeEnsaio struct {
	dentro   port.Gateway
	contador *Contador

	mu     sync.Mutex
	porRun map[string][]chamadaObservada
	// O banco corre um run de cada vez: emCurso é o pedido que está no gateway, e capturas o
	// resultado da captura do estado de cada turno do run aberto.
	emCurso  *chamadaObservada
	capturas *capturasDoRun
	// classeDaUltima é a classe do estado da última resposta: a captura desse turno é reportada
	// pelo adaptador logo a seguir, e é por ela que se sabe o que foi capturado.
	classeDaUltima string
	// soNoSacoDaUltima diz se a última resposta trouxe estado só no saco do proxy.
	soNoSacoDaUltima bool
	// sacoVoltaNoTopo diz que o perfil da corrida declara `devolver_em: topo`.
	sacoVoltaNoTopo bool
}

func (p *portaDeEnsaio) abrir(runID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.porRun == nil {
		p.porRun = map[string][]chamadaObservada{}
	}
	p.porRun[runID] = nil
	p.capturas, p.classeDaUltima, p.soNoSacoDaUltima = &capturasDoRun{porResultado: map[string]int{}}, "", false
}

// fechar devolve os pedidos do run e as capturas do estado dos seus turnos.
func (p *portaDeEnsaio) fechar(runID string) ([]chamadaObservada, capturasDoRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.porRun[runID]
	delete(p.porRun, runID)
	var capturas capturasDoRun
	if p.capturas != nil {
		capturas = *p.capturas
	}
	p.capturas = nil
	return out, capturas
}

// verCaptura é o [modelgateway.ProviderStateObserver] do nó de ensaio: conta, no run aberto, o
// resultado da captura do estado de um turno. Só vocabulário fechado.
func (p *portaDeEnsaio) verCaptura(resultado string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.capturas == nil {
		return
	}
	p.capturas.porResultado[doVocabulario(resultado, modelgateway.ProviderStateResults())]++
	if resultado != modelgateway.ProviderStateResultCaptured {
		return
	}
	switch p.classeDaUltima {
	case estadoComRaciocinio:
		p.capturas.comRaciocinio++
	case estadoSoComIDs:
		p.capturas.soComIDs++
	}
	if p.soNoSacoDaUltima {
		p.capturas.soNoSaco++
	}
}

// verDevolucao recebe do gateway o resultado da devolução do estado no pedido em curso.
func (p *portaDeEnsaio) verDevolucao(o modelgateway.StateReturnObservation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.emCurso == nil {
		return
	}
	p.emCurso.devolucao = doVocabulario(o.Result, modelgateway.StateReturnResults())
	p.emCurso.causaDaDevolucao = ""
	if o.Cause != "" {
		p.emCurso.causaDaDevolucao = doVocabulario(o.Cause, modelgateway.StateReturnCauses())
	}
}

// PortVersion implementa [port.Gateway].
func (p *portaDeEnsaio) PortVersion() string { return p.dentro.PortVersion() }

// ChatStream implementa [port.Gateway]. O banco não usa streaming.
func (p *portaDeEnsaio) ChatStream(ctx context.Context, req port.ChatRequest) (port.ChatStream, error) {
	return p.dentro.ChatStream(ctx, req)
}

// Embeddings implementa [port.Gateway]. O banco não usa embeddings.
func (p *portaDeEnsaio) Embeddings(ctx context.Context, req port.EmbeddingsRequest) (port.EmbeddingsResponse, error) {
	return p.dentro.Embeddings(ctx, req)
}

// Chat implementa [port.Gateway].
func (p *portaDeEnsaio) Chat(ctx context.Context, req port.ChatRequest) (port.ChatResponse, error) {
	braco, _ := ctx.Value(chaveDoBraco).(Braco)
	if braco == "" {
		braco = BracoA
	}
	msgs, err := TransformarMensagens(braco, req.Messages)
	if err != nil {
		return port.ChatResponse{}, err
	}
	req.Messages = msgs
	obs := chamadaObservada{}
	for _, m := range msgs {
		if m.Role == port.RoleTool {
			obs.levaMensagemTool = true
		}
	}
	pedido := &pedidoHTTP{}
	p.mu.Lock()
	p.emCurso = &obs
	p.mu.Unlock()
	resp, err := p.dentro.Chat(context.WithValue(ctx, chaveDaChamada, pedido), req)
	p.mu.Lock()
	p.emCurso = nil
	p.mu.Unlock()
	obs.status, obs.tipoDeErro = pedido.lerStatus(), pedido.lerTipo()
	obs.tentativas, obs.naoEnviados = pedido.lerTentativas()
	obs.classeDoEstado = classeDoEstadoDaResposta(resp.State)
	p.mu.Lock()
	p.classeDaUltima, p.soNoSacoDaUltima = obs.classeDoEstado, !p.sacoVoltaNoTopo && estadoSoNoSaco(resp.State)
	p.mu.Unlock()
	if err != nil {
		obs.erro = classeDoErro(err, obs.status)
		obs.ficha = Ficha{Classe: FichaSemResposta}
	} else {
		obs.ficha = fichaDaResposta(resp.Shape)
		obs.tokensDeEntrada, obs.tokensDeSaida = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		if len(resp.Choices) > 0 {
			obs.texto = resp.Choices[0].Message.Content
			obs.toolCalls = len(resp.Choices[0].Message.ToolCalls)
		}
		if p.contador != nil {
			// O gasto estimado conta-se com os tokens que o fornecedor devolveu. Se o contador
			// não o conseguir gravar, o erro sobe: o pedido seguinte já não sairia de qualquer
			// maneira (a reserva lê o mesmo ficheiro).
			if uerr := p.contador.RegistarUso(resp.Usage.PromptTokens, resp.Usage.CompletionTokens); uerr != nil {
				return port.ChatResponse{}, uerr
			}
		}
	}
	p.mu.Lock()
	if p.porRun != nil {
		p.porRun[req.RunID] = append(p.porRun[req.RunID], obs)
	}
	p.mu.Unlock()
	return resp, err
}

// As classes de erro de um pedido ao gateway. Vocabulário fechado: o texto do erro — que pode
// trazer o corpo devolvido pela rota — nunca é guardado.
const (
	erroHTTP             = "http"
	erroRespostaRecusada = "resposta_recusada"
	erroTecto            = "tecto"
	erroContador         = "contador"
	erroTempo            = "tempo_esgotado"
	erroOutro            = "outro"
	// erroEstado — o gateway NÃO enviou o pedido: a rota exige o estado opaco de volta e o de
	// um turno não se pode devolver ([modelgateway.StateReturnError], AOS-515).
	erroEstado = "estado_nao_devolvido"
)

func classeDoErro(err error, status int) string {
	switch {
	case errors.Is(err, modelgateway.ErrStateReturnRequired):
		return erroEstado
	case errors.Is(err, ErrTectoAtingido):
		return erroTecto
	case errors.Is(err, ErrContador):
		return erroContador
	case modelgateway.ResponseRejectionCause(err) != "":
		return erroRespostaRecusada
	case status != 0 && status != http.StatusOK:
		return erroHTTP
	case errors.Is(err, context.DeadlineExceeded):
		return erroTempo
	default:
		return erroOutro
	}
}

func desfechoDoErro(classe string) string {
	switch classe {
	case erroHTTP:
		return DesfechoErroHTTP
	case erroRespostaRecusada:
		return DesfechoRespostaRecusada
	case erroTempo:
		return DesfechoTempoEsgotado
	case erroEstado:
		return DesfechoEstadoNaoDevolvido
	default:
		return DesfechoErroOutro
	}
}

// pedidoHTTP recebe do transporte o código HTTP do pedido de uma chamada ao gateway e, quando
// não é 200, o tipo do erro em vocabulário fechado.
type pedidoHTTP struct {
	mu     sync.Mutex
	status int
	tipo   string
	// tentativas são os códigos de todas as tentativas de transporte (0 = sem resposta HTTP);
	// naoEnviados, as que a reserva do tecto recusou antes de sair.
	tentativas  []int
	naoEnviados int
}

func (p *pedidoHTTP) lerTentativas() ([]int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.tentativas...), p.naoEnviados
}

// anotar regista uma tentativa de transporte: o código (0 = sem resposta), ou a recusa do tecto.
func (p *pedidoHTTP) anotar(status int, naoEnviado bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if naoEnviado {
		p.naoEnviados++
		return
	}
	p.tentativas = append(p.tentativas, status)
}

func (p *pedidoHTTP) lerTipo() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tipo
}

func (p *pedidoHTTP) lerStatus() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// transporteContado é o transporte HTTP do nó de ensaio. ANTES de cada pedido reserva-o no
// contador do dia; se a reserva falha, o pedido não sai. Depois, anota o código HTTP e o tipo
// do erro.
type transporteContado struct {
	base     http.RoundTripper
	contador *Contador
}

// RoundTrip implementa [http.RoundTripper].
func (t *transporteContado) RoundTrip(req *http.Request) (*http.Response, error) {
	doPedido, _ := req.Context().Value(chaveDaChamada).(*pedidoHTTP)
	if t.contador != nil {
		// FAIL-CLOSED: a reserva vem primeiro, e qualquer erro dela impede o envio.
		if err := t.contador.Reservar(); err != nil {
			if doPedido != nil {
				doPedido.anotar(0, true)
			}
			return nil, err
		}
	}
	resp, err := t.base.RoundTrip(req)
	if doPedido != nil {
		// CADA tentativa de transporte fica anotada: se o gateway repetir o pedido, um erro
		// intermédio não se perde atrás do código da última.
		if err != nil {
			doPedido.anotar(0, false)
		} else {
			doPedido.anotar(resp.StatusCode, false)
		}
	}
	if err == nil {
		if p, ok := req.Context().Value(chaveDaChamada).(*pedidoHTTP); ok {
			// O corpo de um erro lê-se AQUI, só para o classificar: o texto não sai do transporte.
			tipo := ""
			if resp.StatusCode != http.StatusOK {
				tipo = tipoDaResposta(resp)
			}
			p.mu.Lock()
			p.status, p.tipo = resp.StatusCode, tipo
			p.mu.Unlock()
		}
	}
	return resp, err
}
