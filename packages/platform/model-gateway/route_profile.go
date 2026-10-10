package modelgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// O QUE O PERFIL DA ROTA PASSA A PODER DECLARAR (AOS-513, emenda ao ADR-036 §2.8).
//
// Três coisas, todas opcionais, e todas INERTES enquanto nenhum perfil as declarar:
//
//   - os PARÂMETROS DO PEDIDO a enviar a essa rota ([RouteProfile.Params]), de um conjunto
//     fechado e com tipo ([port.RequestParams]): `thinking`, `reasoning_effort`, `max_tokens`
//     e, desde o AOS-516, `reasoning`;
//   - a VERSÃO DA PROJECÇÃO nativa a usar nos runs dessa rota ([RouteProfile.ProjectionVersion]),
//     que prevalece sobre o interruptor do nó;
//   - a CLASSE DE ESTADO ([RouteProfile.StateReturn]): se o estado opaco de um turno (ADR-040)
//     se devolve ao provider. Quem a consome é a projecção 1.3.0 (AOS-515).
//
// ONDE VIVE O PERFIL. Em código, na tabela [routeProfiles], como desde o AOS-505: muda com uma
// imagem nova do nó. O perfil como artefacto assinado do registo é da fase A3. O nó não lê
// perfis de ficheiro, de ambiente, de um plano, de um run nem de um pedido HTTP.
//
// O PERFIL CANDIDATO. Quem compõe um gateway fora do nó — o banco de ensaio (AOS-512), que
// qualifica um perfil ANTES de ele entrar na tabela — pode acrescentar perfis candidatos
// ([ProductionConfig.RouteProfiles], lidos por [ParseRouteProfile]). Um candidato passa pela
// mesma validação da tabela e substitui a entrada com o mesmo nome pedido.
//
// O PROXY REENCAMINHA TUDO. Medido atrás da imagem de produção do proxy: um parâmetro que o
// provider não conheça chega-lhe e pode dar 4xx em todos os turnos da rota. Por isso um perfil
// com parâmetros qualifica-se no banco antes de ser ligado; um 4xx num turno com parâmetros
// conta em métrica própria ([RouteParamsRejection]); e o gateway NUNCA retira o parâmetro para
// repetir sozinho.

// Classes de estado de uma rota ([RouteProfile.StateReturn]). Vocabulário FECHADO.
const (
	// StateReturnNever — o estado opaco nunca é devolvido a esta rota. É a omissão (o campo
	// vazio), e é também a classe de uma rota que o PROÍBE.
	StateReturnNever = ""
	// StateReturnNeverName é a forma escrita de [StateReturnNever] num perfil lido de JSON.
	StateReturnNeverName = "nunca"
	// StateReturnOptional — o estado é devolvido quando existe e é desta rota; sem ele, o
	// pedido segue.
	StateReturnOptional = "opcional"
	// StateReturnRequired — o provider exige o estado de volta: sem ele, o pedido não é enviado.
	StateReturnRequired = "obrigatorio"
)

// Sítios onde volta o estado que veio no saco do proxy ([RouteProfile.StateReturnAt], AOS-516).
// Vocabulário FECHADO. É o perfil — e não o nome de um fornecedor — que diz como a rota recebe o
// estado: um modelo novo, servido por outro agregador, entra por aqui.
const (
	// StateReturnAtOrigin — cada campo volta ao sítio de onde veio (ADR-040 §2.9). É a omissão.
	StateReturnAtOrigin = port.StatePlacementOrigin
	// StateReturnAtOriginName é a forma escrita de [StateReturnAtOrigin] num perfil lido de JSON.
	StateReturnAtOriginName = "origem"
	// StateReturnAtTop — os campos de raciocínio que vieram em
	// `message.provider_specific_fields` voltam no topo da mensagem `assistant`, e esse objecto
	// não volta ([port.StatePlacementTop]).
	StateReturnAtTop = port.StatePlacementTop
)

// ErrBadRouteProfile — um perfil de rota não passa na validação. Fail-closed: um perfil
// recusado não compõe gateway nenhum. A mensagem nomeia o campo e nunca repete o valor.
var ErrBadRouteProfile = errors.New("model-gateway: perfil de rota invalido")

// maxNomeNoPerfil é o tecto em bytes de um nome (pedido ou esperado) num perfil.
const maxNomeNoPerfil = 128

// Validate verifica um perfil: os quatro campos de sempre, os parâmetros no conjunto fechado, a
// versão da projecção entre as publicadas, a classe de estado no vocabulário, e nenhum texto com
// forma de credencial.
func (p RouteProfile) Validate() error {
	for campo, v := range map[string]string{"requested": p.Requested, "expected_model": p.ExpectedModel} {
		if err := nomeDePerfilValido(v); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBadRouteProfile, campo, err)
		}
	}
	if p.WireClass != WireOpenAIChat {
		return fmt.Errorf("%w: wire_class desconhecida (aceite: %s)", ErrBadRouteProfile, WireOpenAIChat)
	}
	for _, c := range p.Capabilities {
		if c != CapabilityTools {
			return fmt.Errorf("%w: capacidade desconhecida (aceite: %s)", ErrBadRouteProfile, CapabilityTools)
		}
	}
	if p.Params != nil {
		if err := p.Params.Validate(); err != nil {
			return fmt.Errorf("%w: params: %w", ErrBadRouteProfile, err)
		}
	}
	if p.ProjectionVersion != "" {
		if _, err := ParseNativeProjectionVersion(p.ProjectionVersion); err != nil {
			return fmt.Errorf("%w: projection_version nao e uma versao publicada da projeccao nativa", ErrBadRouteProfile)
		}
	}
	switch p.StateReturn {
	case StateReturnNever, StateReturnOptional, StateReturnRequired:
	default:
		return fmt.Errorf("%w: devolver fora do vocabulario (aceites: %s, %s, %s)", ErrBadRouteProfile, StateReturnNeverName, StateReturnOptional, StateReturnRequired)
	}
	// UMA ROTA QUE DEVOLVE ESTADO DECLARA A PROJECÇÃO QUE O DEVOLVE (revisão). Com `devolver`
	// diferente de `nunca`, o perfil tem de nomear uma versão da projecção que agarre o estado ao
	// turno — hoje só a 1.3.0. Sem versão, ou com uma anterior, a rota dependia do interruptor do
	// nó: numa `obrigatorio` todos os runs falhavam com `projeccao_sem_estado`, e numa `opcional`
	// o perfil prometia uma devolução que nunca acontecia.
	if p.StateReturn != StateReturnNever && p.ProjectionVersion != NativeProjectionVersion130 {
		return fmt.Errorf("%w: devolver diferente de %s exige projection_version %s", ErrBadRouteProfile, StateReturnNeverName, NativeProjectionVersion130)
	}
	switch p.ToolCallID {
	case ToolCallIDRuntime:
	case ToolCallIDProvider:
		if p.StateReturn == StateReturnNever {
			return fmt.Errorf("%w: tool_call_id em %s exige uma classe de estado que nao seja %s (o id do provider faz parte do estado)", ErrBadRouteProfile, ToolCallIDProvider, StateReturnNeverName)
		}
	default:
		return fmt.Errorf("%w: tool_call_id fora do vocabulario (aceites: %s, %s)", ErrBadRouteProfile, ToolCallIDRuntimeName, ToolCallIDProvider)
	}
	switch p.StateReturnAt {
	case StateReturnAtOrigin:
	case StateReturnAtTop:
		// O sítio só tem leitor numa rota que devolve estado (e essa, acima, exige a 1.3.0).
		if p.StateReturn == StateReturnNever {
			return fmt.Errorf("%w: devolver_em em %s exige uma classe de estado que nao seja %s", ErrBadRouteProfile, StateReturnAtTop, StateReturnNeverName)
		}
	default:
		return fmt.Errorf("%w: devolver_em fora do vocabulario (aceites: %s, %s)", ErrBadRouteProfile, StateReturnAtOriginName, StateReturnAtTop)
	}
	return nil
}

// nomeDePerfilValido aceita um nome de modelo: de 1 a [maxNomeNoPerfil] bytes de letras, dígitos
// e `. _ - / :`, e sem forma de credencial. Um perfil não transporta segredos; um texto com
// essa forma num campo de nome é um erro de quem o escreveu, e recusa-se antes de o perfil ir
// para um digest, um banner ou um evento.
func nomeDePerfilValido(v string) error {
	if v == "" || len(v) > maxNomeNoPerfil {
		return fmt.Errorf("vazio ou com mais de %d bytes", maxNomeNoPerfil)
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-', c == '/', c == ':':
		default:
			return errors.New("caracter fora do alfabeto de um nome de modelo")
		}
	}
	if formaDeChave(v) {
		return errors.New("tem a forma de uma credencial")
	}
	return nil
}

// prefixosDeChave são os princípios das credenciais de API mais comuns.
var prefixosDeChave = []string{"sk-", "sk_", "pk-", "rk-", "xoxb-", "xoxp-", "ghp_", "gho_", "AKIA", "AIza", "Bearer", "eyJ"}

// formaDeChave diz se v, ou um dos seus troços entre `/` e `:`, parece uma credencial: abre por
// um prefixo conhecido, ou é uma sequência de 32 ou mais caracteres com letras E dígitos (os
// nomes de modelo reais têm troços curtos, com hífenes).
func formaDeChave(v string) bool {
	for _, troco := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == ':' }) {
		for _, pre := range prefixosDeChave {
			if strings.HasPrefix(troco, pre) {
				return true
			}
		}
		for _, bloco := range strings.FieldsFunc(troco, func(r rune) bool { return r == '-' || r == '.' }) {
			if len(bloco) >= 32 && strings.ContainsAny(bloco, "0123456789") && strings.ContainsAny(strings.ToLower(bloco), "abcdefghijklmnopqrstuvwxyz") {
				return true
			}
		}
	}
	return false
}

// ParseRouteProfile lê UM perfil de rota de JSON, na forma dos campos de [RouteProfile]. É a
// leitura FECHADA: uma chave que não seja um campo — do perfil ou dos seus parâmetros — é erro
// (não há «parâmetros livres»), um valor de tipo errado é erro, e o perfil lido passa por
// [RouteProfile.Validate]. `devolver` aceita também a forma escrita `nunca`, e `tool_call_id` a
// forma escrita `runtime`, e `devolver_em` a forma escrita `origem`.
func ParseRouteProfile(data []byte) (RouteProfile, error) {
	if err := chavesEstritas(data); err != nil {
		return RouteProfile{}, fmt.Errorf("%w: %w", ErrBadRouteProfile, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p RouteProfile
	if err := dec.Decode(&p); err != nil {
		return RouteProfile{}, fmt.Errorf("%w: %w", ErrBadRouteProfile, erroDeLeituraSemValor(err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return RouteProfile{}, fmt.Errorf("%w: ha conteudo depois do perfil", ErrBadRouteProfile)
	}
	if p.StateReturn == StateReturnNeverName {
		p.StateReturn = StateReturnNever
	}
	if p.ToolCallID == ToolCallIDRuntimeName {
		p.ToolCallID = ToolCallIDRuntime
	}
	if p.StateReturnAt == StateReturnAtOriginName {
		p.StateReturnAt = StateReturnAtOrigin
	}
	if err := p.Validate(); err != nil {
		return RouteProfile{}, err
	}
	return p, nil
}

// chavesEstritas recusa um documento em que algum objecto tenha uma chave REPETIDA ou uma chave
// que não esteja em minúsculas. O `encoding/json` aceita as duas coisas em silêncio: de duas
// chaves iguais fica a última, e `"Params"` casa com o campo `params`. Num perfil, as duas são
// uma forma de o que se lê não ser o que se julga ter escrito (ou revisto).
func chavesEstritas(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	type nivel struct {
		objecto bool
		chave   bool // num objecto: o próximo token é uma chave
		vistas  map[string]bool
	}
	var pilha []*nivel
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("nao e JSON valido de um perfil")
		}
		topo := func() *nivel {
			if len(pilha) == 0 {
				return nil
			}
			return pilha[len(pilha)-1]
		}
		if t := topo(); t != nil && t.objecto && t.chave {
			if d, fecha := tok.(json.Delim); fecha && d == '}' {
				pilha = pilha[:len(pilha)-1]
				if p := topo(); p != nil && p.objecto {
					p.chave = true
				}
				continue
			}
			k, _ := tok.(string)
			if k != strings.ToLower(k) {
				return errors.New("tem uma chave que nao esta em minusculas")
			}
			if t.vistas[k] {
				return errors.New("tem uma chave repetida")
			}
			t.vistas[k] = true
			t.chave = false
			continue
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				pilha = append(pilha, &nivel{objecto: true, chave: true, vistas: map[string]bool{}})
				continue
			case '[':
				pilha = append(pilha, &nivel{})
				continue
			default: // '}' de um objecto vazio já foi tratado acima; aqui é ']'
				pilha = pilha[:len(pilha)-1]
			}
		}
		// Acabou um valor: se o nível de cima é um objecto, segue-se uma chave.
		if t := topo(); t != nil && t.objecto {
			t.chave = true
		}
	}
}

// erroDeLeituraSemValor reduz um erro do descodificador ao que NÃO repete conteúdo do ficheiro:
// o nome do campo de um tipo errado, ou a frase fixa de uma chave desconhecida.
func erroDeLeituraSemValor(err error) error {
	var tipo *json.UnmarshalTypeError
	if errors.As(err, &tipo) {
		return fmt.Errorf("o campo %q tem um valor de tipo errado", tipo.Field)
	}
	if strings.Contains(err.Error(), "unknown field") {
		return errors.New("tem uma chave fora do conjunto fechado dos campos de um perfil")
	}
	return errors.New("nao e JSON valido de um perfil")
}

// RouteProfileSet é o conjunto dos perfis com que um gateway trabalha: a tabela em código, com
// os candidatos por cima. Um ponteiro nil é a tabela em código, sem candidatos.
type RouteProfileSet struct {
	perfis []RouteProfile
}

// NewRouteProfileSet valida a tabela em código e os candidatos, e devolve o conjunto. Um
// candidato com o nome pedido de uma entrada da tabela SUBSTITUI-A; os outros acrescentam-se.
// Dois candidatos com o mesmo nome pedido são erro.
func NewRouteProfileSet(candidatos ...RouteProfile) (*RouteProfileSet, error) {
	set := &RouteProfileSet{perfis: RouteProfiles()}
	vistos := map[string]bool{}
	for _, c := range candidatos {
		if vistos[c.Requested] {
			return nil, fmt.Errorf("%w: dois perfis candidatos com o mesmo nome pedido", ErrBadRouteProfile)
		}
		vistos[c.Requested] = true
		substituiu := false
		for i := range set.perfis {
			if set.perfis[i].Requested == c.Requested {
				set.perfis[i], substituiu = c, true
			}
		}
		if !substituiu {
			set.perfis = append(set.perfis, c)
		}
	}
	for _, p := range set.perfis {
		if err := p.Validate(); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// For devolve o perfil do nome pedido ao proxy. Comparação exacta.
func (s *RouteProfileSet) For(requested string) (RouteProfile, bool) {
	if s == nil {
		return RouteProfileFor(requested)
	}
	for _, p := range s.perfis {
		if p.Requested == requested {
			return p, true
		}
	}
	return RouteProfile{}, false
}

// WithParams devolve os nomes pedidos dos perfis do conjunto que declaram parâmetros, pela ordem
// da tabela. É o conjunto FECHADO de valores do rótulo `rota` de [RouteParamsRejection].
func (s *RouteProfileSet) WithParams() []string {
	perfis := routeProfiles
	if s != nil {
		perfis = s.perfis
	}
	var out []string
	for _, p := range perfis {
		if p.Params != nil && !p.Params.IsZero() {
			out = append(out, p.Requested)
		}
	}
	return out
}

// parametros devolve os parâmetros do pedido que o perfil declara; o valor-zero quando nenhum.
func (p RouteProfile) parametros() port.RequestParams {
	if p.Params == nil {
		return port.RequestParams{}
	}
	return *p.Params
}

// WithRouteProfiles dá ao gateway o conjunto de perfis com que trabalha (AOS-513). Sem a opção —
// ou com nil — é a tabela em código.
func WithRouteProfiles(set *RouteProfileSet) Option {
	return func(g *Gateway) { g.perfis = set }
}

// RouteParamsRejection é o que o gateway reporta quando o provider responde 4xx a um pedido em
// que o perfil da rota enviou parâmetros. Os dois campos são de vocabulário fechado, e nenhum
// byte do corpo do erro sai daqui.
type RouteParamsRejection struct {
	// Route é o nome pedido da rota — um dos de [RouteProfileSet.WithParams].
	Route string
	// Status é o código HTTP, num dos valores de [RouteParamsRejectionStatuses].
	Status string
}

// RouteParamsRejectionOther é o rótulo de um 4xx fora da lista de [RouteParamsRejectionStatuses].
const RouteParamsRejectionOther = "4xx"

// RouteParamsRejectionStatuses devolve o vocabulário FECHADO do rótulo do código.
func RouteParamsRejectionStatuses() []string {
	return []string{"400", "401", "403", "404", "408", "409", "413", "422", "429", RouteParamsRejectionOther}
}

// WithRouteParamsObserver liga quem conta os 4xx dos turnos com parâmetros (AOS-513). nil ⇒
// ninguém conta.
func WithRouteParamsObserver(obs func(RouteParamsRejection)) Option {
	return func(g *Gateway) { g.paramsObs = obs }
}

// aplicarPerfilDaRota escreve no pedido que vai sair os parâmetros do perfil da rota a que ele
// vai (req.Model já é o nome resolvido), e devolve-os na forma do manifesto. Sem perfil, ou com
// um perfil que não os declare, o pedido fica sem parâmetros de raciocínio — os que um chamador
// lhe tivesse posto são apagados — e a devolução é nil.
func (g *Gateway) aplicarPerfilDaRota(req *port.ChatRequest) map[string]string {
	perfil, _ := g.perfis.For(req.Model)
	params := perfil.parametros()
	params.Apply(req)
	return params.Manifest()
}

// contarRecusaDeParametros conta um 4xx do provider num turno em que o perfil enviou parâmetros.
// O gateway NÃO retira o parâmetro nem repete o pedido: o erro sobe como subia.
func (g *Gateway) contarRecusaDeParametros(rota string, enviados map[string]string, err error) {
	if g.paramsObs == nil || len(enviados) == 0 {
		return
	}
	var se *adapters.StatusError
	if !errors.As(err, &se) || se.Status < 400 || se.Status > 499 {
		return
	}
	codigo := strconv.Itoa(se.Status)
	conhecido := false
	for _, c := range RouteParamsRejectionStatuses() {
		conhecido = conhecido || c == codigo
	}
	if !conhecido {
		codigo = RouteParamsRejectionOther
	}
	g.paramsObs(RouteParamsRejection{Route: rota, Status: codigo})
}
