package port

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A ROTA SERVIDA (AOS-505, contrato 1.4.0).
//
// O campo `model` do corpo de uma resposta não identifica o modelo que serviu: um proxy
// OpenAI-compatível à frente do provider carimba nele o nome que o cliente pediu (medido com o
// LiteLLM 1.96.2 — o corpo devolve sempre o alias). O que o proxy sabe sobre a rota vem nos
// CABEÇALHOS da resposta, e é isso que [ServedRoute] transporta.
//
// O QUE ISTO É E O QUE NÃO É. Os cabeçalhos dizem o que o proxy está CONFIGURADO para pedir: o
// modelo e o endpoint do deployment que escolheu. Mudam quando a configuração do proxy muda. Não
// dizem o que o provider serviu de facto por trás desse nome e desse endpoint, e não são
// atestação: quem os emite é o proxy, sem prova de origem, e valem enquanto o canal entre o nó e
// o proxy for de confiança.

// Cabeçalhos de resposta lidos. São os do LiteLLM; um proxy que não os emita deixa a rota por
// reportar ([ServedRoute] vazio), e nunca se preenche com o que foi pedido.
const (
	// HeaderServedModel — o `litellm_params.model` do deployment que serviu (ex.:
	// `openai/kimi-for-coding`): o nome que o proxy pede ao provider, com o prefixo da classe.
	HeaderServedModel = "x-litellm-model-name"
	// HeaderServedAPIBase — o `api_base` do deployment que serviu. Lê-se SÓ o host.
	HeaderServedAPIBase = "x-litellm-model-api-base"
)

// O cabeçalho de identificador do deployment que o mesmo proxy emite NÃO É LIDO, de propósito, e
// não tem constante aqui. É um SHA-256 sem sal de todos os parâmetros do deployment, incluindo a
// chave do provider: é um derivado de segredo. Não se grava, não se regista e não entra em
// métricas — e a maneira de o garantir é nenhum código do gateway lhe tocar.

// MaxServedModel é o tecto em bytes do nome de modelo servido que se aceita de um cabeçalho.
const MaxServedModel = 256

// maxServedHost é o tecto em bytes de um host (253 de um nome DNS, mais a porta).
const maxServedHost = 260

// ServedRoute é a rota que o proxy declarou para uma resposta, mais o resultado da comparação
// com o perfil esperado quando o gateway a governa.
type ServedRoute struct {
	// Model é o nome do modelo servido tal como o proxy o declarou ([HeaderServedModel]),
	// saneado ([SanitizeServedModel]). Vazio ⇒ NÃO REPORTADO.
	Model string
	// APIHost é o HOST (com a porta, se vier) do endpoint que o proxy declarou
	// ([HeaderServedAPIBase]): sem esquema, sem credenciais, sem caminho e sem query. Vazio ⇒
	// não reportado. Serve só para comparar dentro do gateway, que o apaga antes de devolver a
	// resposta: não chega ao runtime, a eventos, a métricas nem a logs.
	APIHost string
	// Check é o resultado da comparação com o perfil da rota, no vocabulário fechado
	// RouteCheck*. Vazio ⇒ a rota NÃO está sob governação (o interruptor está desligado), e
	// quem lê a resposta trata-a como sempre tratou.
	Check string
	// ProfileDigest é o digest do perfil da rota com que a comparação foi feita. Vazio quando a
	// rota não está sob governação ou não tem perfil.
	ProfileDigest string
}

// Resultados da comparação do modelo servido com o esperado ([ServedRoute.Check]). Vocabulário
// FECHADO: são também os valores do rótulo da métrica e do campo `route_check` do turno.
const (
	// RouteCheckEqual — o proxy reportou a rota e ela é a do perfil.
	RouteCheckEqual = "igual"
	// RouteCheckDifferent — o proxy reportou uma rota que não é a do perfil, ou a rota pedida
	// não tem perfil.
	RouteCheckDifferent = "diferente"
	// RouteCheckUnreported — o proxy não reportou o que era preciso para comparar.
	RouteCheckUnreported = "nao_reportado"
)

// ServedRouteFromHeaders lê a rota servida dos cabeçalhos de uma resposta. `get` é a leitura de
// um cabeçalho pelo nome (a de [net/http.Header.Get]). Um cabeçalho ausente, vazio ou ilegível
// deixa o campo correspondente vazio.
func ServedRouteFromHeaders(get func(name string) string) ServedRoute {
	if get == nil {
		return ServedRoute{}
	}
	return ServedRoute{
		Model:   SanitizeServedModel(get(HeaderServedModel)),
		APIHost: HostOfAPIBase(get(HeaderServedAPIBase)),
	}
}

// SanitizeServedModel saneia um nome de modelo servido antes de ele sair do adaptador: retira os
// caracteres não imprimíveis, apara e corta em [MaxServedModel] bytes sem partir um carácter
// UTF-8. É texto de terceiros que acaba em claro num evento.
func SanitizeServedModel(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s))
	if len(s) <= MaxServedModel {
		return s
	}
	corte := MaxServedModel
	for corte > 0 && !utf8.RuneStart(s[corte]) {
		corte--
	}
	return s[:corte]
}

// HostOfAPIBase devolve SÓ o host (em minúsculas, com a porta se vier) de um `api_base`. O
// esquema, as credenciais (`user:pass@`), o caminho, a query e o fragmento são deitados fora
// aqui, à entrada: nada a jusante os chega a ver. Um valor que não seja um URL com host, ou
// cujo host tenha caracteres fora de um nome DNS, de um IP ou de uma porta, devolve vazio.
func HostOfAPIBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return ""
	}
	if !strings.Contains(raw, "://") {
		// Sem esquema (`host:8080/v1`), o url.Parse leria o host como esquema ou como caminho.
		raw = "//" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return NormalizeAPIHost(u.Host)
}

// NormalizeAPIHost valida e normaliza um host já isolado (`nome`, `nome:porta`, `[ipv6]:porta`):
// minúsculas, e só caracteres de um nome DNS, de um IP ou de uma porta. Qualquer outra coisa —
// um esquema, uma barra, um `@`, um espaço — devolve vazio.
func NormalizeAPIHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || len(host) > maxServedHost {
		return ""
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == ':', r == '[', r == ']':
		default:
			return ""
		}
	}
	return host
}
