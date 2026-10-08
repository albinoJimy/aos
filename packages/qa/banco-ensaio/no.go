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

	gw, err := modelgateway.NewProduction(ctx, modelgateway.ProductionConfig{
		Provider:      "openai",
		BaseURL:       base,
		HTTPClient:    &http.Client{Timeout: timeout, Transport: &transporteContado{base: transporte, contador: cfg.Contador}},
		DefaultRegion: regiaoDoEnsaio,
		Authn:         authn.New(verificador, autoridadeDoEnsaio{}, politica),
		Audit:         audit.NewMemStore(),
		Credentials:   credencialEstatica{segredo: cfg.Credencial},
		Accounts:      []modelgateway.InfraAccount{{KeyID: "rota-de-ensaio", Provider: "openai", Region: regiaoDoEnsaio}},
		Allowlist:     pol,
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
		bateria: cfg.Bateria, porta: &portaDeEnsaio{dentro: gw, contador: cfg.Contador},
		mon: mon, recusa: recusa, store: store, emissor: iss, contador: cfg.Contador,
		runtimes: map[Braco]*agentruntime.Runtime{}, motivos: map[string][]string{},
	}
	var doNo []port.Tool
	for _, t := range ToolsDoBanco() {
		doNo = append(doNo, port.Tool{Type: "function", Function: port.FunctionDef{Name: t.Nome, Description: t.Descricao, Parameters: t.Parametros}})
	}
	for _, b := range Bracos() {
		mc := modelgateway.NewModelClient(n.porta, AliasDaRota,
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
			modelgateway.WithToolOfferFromContext(func(ctx context.Context) (func(string) bool, bool) {
				nomes, _ := ctx.Value(chaveDasToolsDoRun).(map[string]bool)
				return func(nome string) bool { return nomes[nome] }, true
			}),
		)
		n.runtimes[b] = agentruntime.New(mc, mon, agentruntime.NewTurnRecorder(store),
			agentruntime.WithAssemblyVersion(agentruntime.AssemblyVersion140),
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

	chamadas := n.porta.fechar(runID)
	n.mu.Lock()
	obs.MotivosDeParagem = n.motivos[runID]
	delete(n.motivos, runID)
	n.mu.Unlock()

	// Os turnos e as tool calls contam-se pelo que o banco VIU passar na porta do gateway, e
	// não pelo [agentruntime.Result]: um run que falha a meio (o provider recusa o segundo
	// pedido) devolve um Result vazio, e o primeiro turno — com a sua tool call — aconteceu.
	obs.UltimoDesfechoDeTool = res.LastToolOutcome
	var ultimoTexto string
	for i, c := range chamadas {
		obs.Pedidos++
		obs.HTTP = append(obs.HTTP, c.status)
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
	erro             string
	ficha            Ficha
	tokensDeEntrada  int64
	tokensDeSaida    int64
	toolCalls        int
	texto            string
}

// portaDeEnsaio é o decorador da porta do gateway: aplica o braço às mensagens que a projecção
// de produção produziu e observa cada pedido. Tudo o resto delega no gateway de produção.
type portaDeEnsaio struct {
	dentro   port.Gateway
	contador *Contador

	mu     sync.Mutex
	porRun map[string][]chamadaObservada
}

func (p *portaDeEnsaio) abrir(runID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.porRun == nil {
		p.porRun = map[string][]chamadaObservada{}
	}
	p.porRun[runID] = nil
}

func (p *portaDeEnsaio) fechar(runID string) []chamadaObservada {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.porRun[runID]
	delete(p.porRun, runID)
	return out
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
	resp, err := p.dentro.Chat(context.WithValue(ctx, chaveDaChamada, pedido), req)
	obs.status = pedido.lerStatus()
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
)

func classeDoErro(err error, status int) string {
	switch {
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
	default:
		return DesfechoErroOutro
	}
}

// pedidoHTTP recebe do transporte o código HTTP do pedido de uma chamada ao gateway.
type pedidoHTTP struct {
	mu     sync.Mutex
	status int
}

func (p *pedidoHTTP) lerStatus() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// transporteContado é o transporte HTTP do nó de ensaio. ANTES de cada pedido reserva-o no
// contador do dia; se a reserva falha, o pedido não sai. Depois, anota o código HTTP.
type transporteContado struct {
	base     http.RoundTripper
	contador *Contador
}

// RoundTrip implementa [http.RoundTripper].
func (t *transporteContado) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.contador != nil {
		// FAIL-CLOSED: a reserva vem primeiro, e qualquer erro dela impede o envio.
		if err := t.contador.Reservar(); err != nil {
			return nil, err
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		if p, ok := req.Context().Value(chaveDaChamada).(*pedidoHTTP); ok {
			p.mu.Lock()
			p.status = resp.StatusCode
			p.mu.Unlock()
		}
	}
	return resp, err
}
