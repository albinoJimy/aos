package main

// A ROTA SOB GOVERNAÇÃO (AOS-505, emenda ao ADR-036 §2.8) — `AOS_MODEL_ROUTE_GOVERNANCE` e
// `AOS_MODEL_ROUTE_API_HOST`.
//
// A allowlist assinada governa que NOME o nó pode pedir ao proxy de modelos. O que esse nome
// significa — que modelo e que endpoint o servem — decide-se na configuração do proxy. Com a
// governação ligada, o gateway compara em cada turno o que o proxy DECLARA ter servido (nos
// cabeçalhos da resposta) com o perfil da rota, que vive em código:
//
//   - `off` (por omissão) — nada é comparado, e o nó comporta-se byte a byte como antes;
//   - `observe` — cada turno é comparado; uma variância fica no `turn.recorded`
//     (`route_check`), num selo do audit de governação do gateway e no contador do `/metrics`, e
//     o turno segue;
//   - `enforce` — um turno cuja rota não se prove igual à do perfil falha, com causa em
//     vocabulário fechado.
//
// O QUE DETECTA: uma troca de configuração no proxy. O QUE NÃO DETECTA: uma troca feita pelo
// provider por trás do mesmo nome e do mesmo endpoint. Os cabeçalhos não são atestação — valem
// enquanto o canal entre o nó e o proxy for de confiança.
//
// O QUE FICA DE FORA: as chamadas ao modelo feitas pelo `aos-orq` (o planeador). Esse binário
// compõe o seu próprio gateway, sem governação da rota, e as suas chamadas passam pelo mesmo
// proxy sem serem comparadas — em nenhum dos modos. Estas variáveis só governam o nó.
//
// O ENDPOINT: `enforce` exige `AOS_MODEL_ROUTE_API_HOST` — sem ela, uma troca só de endpoint
// passaria por `igual` num modo que promete falhar. `observe` aceita-a por definir, e declara no
// arranque que o endpoint não é comparado. Com `off` a variável não é usada: um valor inválido é
// ignorado com um aviso, e não aborta o arranque de um nó que não liga a governação.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// defaultModelRouteGovernance é o modo de um nó que não define AOS_MODEL_ROUTE_GOVERNANCE.
const defaultModelRouteGovernance = modelgateway.RouteGovernanceOff

// ErrBadModelRouteGovernance — AOS_MODEL_ROUTE_GOVERNANCE está definida com um valor fora do
// vocabulário fechado. Fail-closed: o nó não arranca. Cair para `off` em silêncio deixaria o
// operador convencido de que a rota está a ser comparada.
var ErrBadModelRouteGovernance = errors.New("aos: AOS_MODEL_ROUTE_GOVERNANCE invalida — valores aceites: off (por omissao; nada e comparado), observe (compara cada turno e regista a variancia) ou enforce (um turno cuja rota nao se prove igual a do perfil falha)")

// ErrBadModelRouteAPIHost — AOS_MODEL_ROUTE_API_HOST não é um host (`nome` ou `nome:porta`). O
// valor NÃO vai na mensagem: quem colou ali um URL com credenciais não as vê repetidas num log.
var ErrBadModelRouteAPIHost = errors.New("aos: AOS_MODEL_ROUTE_API_HOST invalida — tem de ser so o host do endpoint que o proxy deve declarar (nome ou nome:porta), sem esquema, credenciais, caminho nem query; o valor recebido nao e repetido aqui")

// ErrModelRouteEnforceWithoutHost — AOS_MODEL_ROUTE_GOVERNANCE=enforce sem
// AOS_MODEL_ROUTE_API_HOST. Fail-closed: o nó não arranca. Sem o host esperado, uma troca só de
// endpoint no proxy daria `igual` em todos os turnos, num modo cuja promessa é falhar o que não se
// prove igual.
var ErrModelRouteEnforceWithoutHost = errors.New("aos: AOS_MODEL_ROUTE_GOVERNANCE=enforce exige AOS_MODEL_ROUTE_API_HOST — sem o host esperado do endpoint, uma troca so de endpoint no proxy passaria por igual; defina AOS_MODEL_ROUTE_API_HOST (nome ou nome:porta, tal como o proxy o declara) ou use observe")

// ErrModelRouteWithoutProfile — a governação da rota está ligada e o modelo do nó
// (AOS_MODEL_NAME) não tem perfil de rota. Fail-closed: sem perfil não há esperado com que
// comparar, e cada turno seria uma variância.
var ErrModelRouteWithoutProfile = errors.New("aos: AOS_MODEL_ROUTE_GOVERNANCE ligada e o modelo do no nao tem perfil de rota — a comparacao nao tem esperado; desligue-a (off) ou use um modelo com perfil")

// parseModelRouteGovernanceFromEnv lê AOS_MODEL_ROUTE_GOVERNANCE. Vazia ⇒
// [defaultModelRouteGovernance]. Um valor fora do vocabulário ⇒ [ErrBadModelRouteGovernance].
// Sem normalização de caixa: `Observe` não é `observe`.
func parseModelRouteGovernanceFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_MODEL_ROUTE_GOVERNANCE"))
	if raw == "" {
		return defaultModelRouteGovernance, nil
	}
	mode, err := modelgateway.ParseRouteGovernance(raw)
	if err != nil {
		return "", fmt.Errorf("%w (veio %q)", ErrBadModelRouteGovernance, raw)
	}
	return mode, nil
}

// parseModelRouteAPIHostFromEnv lê AOS_MODEL_ROUTE_API_HOST: o host do endpoint que o proxy deve
// declarar. Vazia ⇒ "" (o endpoint não é comparado). Tem de ser só um host; qualquer outra coisa
// ⇒ [ErrBadModelRouteAPIHost]. Sai normalizado como o que o proxy declara
// ([port.NormalizeAPIHost]): minúsculas e sem ponto final. A PORTA compara-se como está — se o
// proxy declara `nome:443`, a variável leva `nome:443`.
func parseModelRouteAPIHostFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_MODEL_ROUTE_API_HOST"))
	if raw == "" {
		return "", nil
	}
	host := port.NormalizeAPIHost(raw)
	if host == "" {
		return "", ErrBadModelRouteAPIHost
	}
	return host, nil
}

// modelRouteFromEnv lê e valida a governação da rota para o modelo do nó. Devolve a configuração
// para o gateway e os contadores para o `/metrics` — nil com a governação desligada.
func modelRouteFromEnv(model string) (modelgateway.RouteGovernance, *contadoresDaRota, error) {
	mode, err := parseModelRouteGovernanceFromEnv()
	if err != nil {
		return modelgateway.RouteGovernance{}, nil, err
	}
	if mode == modelgateway.RouteGovernanceOff {
		// Desligada: a configuração a zero é a de um gateway anterior ao AOS-505. O host não é
		// lido — um valor inválido numa variável que não é usada não aborta o arranque; fica
		// declarado no arranque ([modelRouteBannerFromEnv]).
		return modelgateway.RouteGovernance{}, nil, nil
	}
	host, err := parseModelRouteAPIHostFromEnv()
	if err != nil {
		return modelgateway.RouteGovernance{}, nil, err
	}
	if mode == modelgateway.RouteGovernanceEnforce && host == "" {
		return modelgateway.RouteGovernance{}, nil, ErrModelRouteEnforceWithoutHost
	}
	if _, ok := modelgateway.RouteProfileFor(model); !ok {
		return modelgateway.RouteGovernance{}, nil, fmt.Errorf("%w (modelo %q)", ErrModelRouteWithoutProfile, model)
	}
	contadores := novosContadoresDaRota()
	return modelgateway.RouteGovernance{Mode: mode, ExpectedAPIHost: host, Observer: contadores.observar}, contadores, nil
}

// modelRouteBanner declara a governação da rota. Com `off` não sai linha nenhuma: o arranque de
// um nó que não a liga é o de antes. Só sai com um gateway composto.
func modelRouteBanner(gatewayComposed bool, mode, model string, hostDefinido bool) []string {
	if !gatewayComposed || mode == modelgateway.RouteGovernanceOff {
		return nil
	}
	perfil, _ := modelgateway.RouteProfileFor(model)
	endpoint := "o endpoint NAO e comparado (AOS_MODEL_ROUTE_API_HOST por definir) — uma troca so de endpoint no proxy passa por igual"
	if hostDefinido {
		endpoint = "o host do endpoint declarado e comparado com AOS_MODEL_ROUTE_API_HOST (minusculas e sem ponto final dos dois lados; a porta compara-se como esta)"
	}
	efeito := "OBSERVACAO — uma variancia fica em route_check do turn.recorded, num selo do audit de governacao do gateway e em aos_model_route_checks_total; o turno segue"
	if mode == modelgateway.RouteGovernanceEnforce {
		efeito = "IMPOSICAO — um turno cuja rota nao se prove igual a do perfil (diferente ou nao reportada) FALHA com causa em vocabulario fechado, depois de selada a variancia"
	}
	return []string{
		fmt.Sprintf("rota do modelo sob governacao (EPIC-06/AOS-505): AOS_MODEL_ROUTE_GOVERNANCE=%s — %s. Perfil da rota %q: modelo esperado %q, digest %s; %s. Detecta uma troca de CONFIGURACAO no proxy; NAO detecta uma troca feita pelo provider por tras do mesmo nome e endpoint, e os cabecalhos do proxy nao sao atestacao. As chamadas ao modelo feitas pelo aos-orq (planeador) NAO sao comparadas: so o no e governado. Remova a variavel ou defina off para repor o comportamento anterior",
			mode, efeito, model, perfil.ExpectedModel, perfil.Digest(), endpoint),
	}
}

// modelRouteBannerFromEnv relê o ambiente e devolve as linhas do arranque sobre a governação da
// rota. As variáveis já foram validadas em [parseModelFromEnv]; aqui só se declara.
//
// Com `off`, sai linha nenhuma — a não ser que AOS_MODEL_ROUTE_API_HOST esteja definida com um
// valor que não é um host: a variável não é usada e o nó arranca, mas o operador fica a saber que
// o valor não serviria se ligasse a governação. O valor NÃO é repetido.
func modelRouteBannerFromEnv(gatewayComposed bool, model string) []string {
	mode, err := parseModelRouteGovernanceFromEnv()
	if err != nil {
		return nil
	}
	host, herr := parseModelRouteAPIHostFromEnv()
	if mode == modelgateway.RouteGovernanceOff {
		if herr != nil {
			return []string{"AVISO (EPIC-06/AOS-505): AOS_MODEL_ROUTE_API_HOST esta definida com um valor que nao e um host (nome ou nome:porta) e foi IGNORADA — a governacao da rota esta desligada (AOS_MODEL_ROUTE_GOVERNANCE=off) e a variavel nao e usada. Com observe ou enforce este valor recusaria o arranque. O valor recebido nao e repetido aqui"}
		}
		return nil
	}
	if herr != nil {
		return nil
	}
	return modelRouteBanner(gatewayComposed, mode, model, host != "")
}

// contadoresDaRota conta, por processo, as comparações da rota por resultado e por modelo
// servido. Os dois eixos são vocabulários FECHADOS — o resultado é `igual`, `diferente` ou
// `nao_reportado`, e o modelo servido é um dos nomes esperados dos perfis, `outro` ou
// `nao_reportado` —, pelo que o texto que o proxy declarou nunca chega a um rótulo.
//
// Conta chamadas AO VIVO ao gateway: um turno reproduzido de uma captura (retoma, replay) não
// volta a contar. Em imposição, o turno recusado conta — foi comparado.
type contadoresDaRota struct {
	resultados []string
	servidos   []string
	total      map[string]map[string]*atomic.Int64
}

func novosContadoresDaRota() *contadoresDaRota {
	c := &contadoresDaRota{
		resultados: []string{port.RouteCheckEqual, port.RouteCheckDifferent, port.RouteCheckUnreported},
		servidos:   modelgateway.ServedModelLabels(),
		total:      map[string]map[string]*atomic.Int64{},
	}
	for _, r := range c.resultados {
		c.total[r] = map[string]*atomic.Int64{}
		for _, s := range c.servidos {
			c.total[r][s] = new(atomic.Int64)
		}
	}
	return c
}

// observar é o [modelgateway.RouteGovernance.Observer]. Um par fora dos dois vocabulários não é
// contado com o texto recebido: cai em (`nao_reportado`, `outro`).
func (c *contadoresDaRota) observar(o modelgateway.RouteObservation) {
	if c == nil {
		return
	}
	porServido, ok := c.total[o.Check]
	if !ok {
		porServido = c.total[port.RouteCheckUnreported]
	}
	n, ok := porServido[o.Served]
	if !ok {
		n = porServido[modelgateway.ServedLabelOther]
	}
	n.Add(1)
}

// lido devolve o total de um par (resultado, modelo servido) dos vocabulários.
func (c *contadoresDaRota) lido(resultado, servido string) int64 {
	if c == nil || c.total[resultado] == nil || c.total[resultado][servido] == nil {
		return 0
	}
	return c.total[resultado][servido].Load()
}

// clienteComRota é o cliente de modelo do nó com os contadores da rota à mão. Não muda nenhuma
// chamada: existe para o [Bootstrap] encontrar os contadores no cliente que recebe
// ([rotaDoCliente]) e os publicar no `/metrics`, sem um canal lateral entre quem lê o ambiente
// e quem compõe o nó.
type clienteComRota struct {
	inner      agentruntime.ModelClient
	contadores *contadoresDaRota
}

// Call implementa [agentruntime.ModelClient], sem tocar em nada.
func (c clienteComRota) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return c.inner.Call(ctx, view)
}

func (c clienteComRota) rotaDoModelo() *contadoresDaRota { return c.contadores }

// fonteDaRota é o que um cliente de modelo (ou um decorador dele) expõe para os contadores da
// rota serem encontrados.
type fonteDaRota interface {
	rotaDoModelo() *contadoresDaRota
}

// rotaDoCliente devolve os contadores da rota do cliente de modelo dado, ou nil — a governação
// desligada, o modelo de referência, ou um cliente injectado que não os tem.
func rotaDoCliente(c agentruntime.ModelClient) *contadoresDaRota {
	if f, ok := c.(fonteDaRota); ok {
		return f.rotaDoModelo()
	}
	return nil
}
