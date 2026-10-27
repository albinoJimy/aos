package port

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// O FORNECEDOR FIXADO NA IDA, E O FORNECEDOR A MONTANTE NA VOLTA (AOS-565, ADR-043 §2.4 regra 3).
//
// São DUAS coisas, e o contrato nomeia-as de maneira diferente de propósito:
//
//   - a IDA é [ProviderPolicy]: a ordem de fornecedores e a política de retenção que o PERFIL
//     ASSINADO da rota declara, e que viajam no corpo do pedido na chave de topo `provider`. O
//     spike AOS-558 mediu que o proxy fixado as encaminha ao fornecedor;
//   - a VOLTA é o FORNECEDOR A MONTANTE ([ChatResponse.UpstreamProvider]): quem o proxy
//     encaminhou de facto, lido do corpo cru por [ProbeUpstreamProvider].
//
// O NOME É PRÓPRIO, E A RAZÃO É QUE O OUTRO JÁ ESTÁ OCUPADO. `ResolvedProvider` do pipeline é o
// ADAPTADOR do lado do nó (quem o nó chama), já viaja no evento de variância como `Provider` e
// já tem um `provider_swap`. O fornecedor A MONTANTE é outra coisa — o que está do lado de lá do
// proxy —, e reutilizar o campo poria dois sentidos no mesmo campo de um evento selado.
//
// O QUE ESTÁ MEDIDO, E O QUE NÃO ESTÁ. Medido a 2026-10-27, contra o proxy fixado (LiteLLM
// 1.96.2) e um fornecedor falso, 6/6 em duas formas de rota: uma chave de topo desconhecida no
// corpo da resposta — `provider`, e também uma chave inventada — CHEGA INTACTA ao cliente, e o
// proxy re-serializa o documento sem a perder. Isto é a metade (ii) da pergunta: *o proxy
// deixa passar*. A metade (i) — *se o fornecedor real emite a identidade efectiva, e com que
// nome e forma* — exige fornecedor real e custo, é o AOS-570a, e NÃO ESTÁ MEDIDA. Por isso
// [UpstreamProviderUnreported] é o caso ESPERADO em produção hoje: nada aqui promete que o
// campo venha preenchido.
//
// A SEGUNDA VIA MEDIDA, QUE NÃO SE IMPLEMENTA. Na mesma medição apareceu um caminho que
// ninguém tinha enumerado: o proxy REEMITE todos os cabeçalhos de resposta do fornecedor com o
// prefixo `llm_provider-` (um cabeçalho inventado do falso chegou como
// `llm_provider-x-aos-sonda-fornecedor`). Serve de alternativa se um dia o corpo não servir —
// e fica escrita, não construída: uma via chega, e duas seriam duas superfícies a manter.

// Políticas de retenção de uma rota ([ProviderPolicy.DataCollection]). Vocabulário FECHADO.
//
// Os valores são os do WIRE, e não uma tradução. É deliberado: o perfil declara o que vai no
// corpo, e uma tabela de tradução a meio seria um segundo sítio onde o valor pode ficar errado
// sem ninguém dar por isso. O mesmo critério que [RequestParams] já segue para
// `reasoning_effort`.
const (
	// DataCollectionDeny — o fornecedor não pode reter o conteúdo (é o ZDR do ADR-043).
	DataCollectionDeny = "deny"
	// DataCollectionAllow — a retenção é permitida. Declara-se EXPLICITAMENTE; não é a omissão.
	DataCollectionAllow = "allow"
)

// MaxProviderOrder é o tecto de fornecedores numa ordem declarada. A ordem é uma FIXAÇÃO, não
// uma preferência: uma lista longa seria um failover por outro nome.
const MaxProviderOrder = 8

// maxNomeDeFornecedor é o tecto em bytes do nome de um fornecedor declarado.
const maxNomeDeFornecedor = 64

// ErrBadProviderPolicy — a declaração de fornecedor de um perfil está fora do conjunto fechado.
// A mensagem nomeia o campo e NUNCA repete o valor.
var ErrBadProviderPolicy = errors.New("port: declaracao de fornecedor fora do conjunto fechado")

// ProviderPolicy é o fornecedor FIXADO de uma rota: a ordem de fornecedores e a política de
// retenção que o perfil assinado declara. É o que vai na chave de topo `provider` do corpo do
// pedido.
//
// SEM FALLBACK, E NÃO É DECLARÁVEL. O corpo leva sempre `allow_fallbacks: false` — ver
// [ProviderPolicy.MarshalJSON] —, pelo que a ordem é uma fixação e não uma preferência. Não há
// campo para o contrário: um perfil não pode pedir fallback, e a leitura fechada do perfil
// recusa uma chave `allow_fallbacks` como recusa qualquer outra que não seja um membro.
type ProviderPolicy struct {
	// Order é a ordem de fornecedores, de 1 a [MaxProviderOrder] nomes, sem repetições. Os
	// nomes são os do fornecedor a montante, escritos como ele os escreve: a comparação da
	// volta é EXACTA (ver [ProviderPolicy.Allows]).
	Order []string `json:"order"`
	// DataCollection é a política de retenção: [DataCollectionDeny] ou [DataCollectionAllow].
	// Vazio ⇒ o perfil não a declara e o campo não vai no corpo — o que vale é a omissão do
	// fornecedor, e não uma promessa deste nó.
	DataCollection string `json:"data_collection,omitempty"`
}

// wireProviderPolicy é a forma JSON da declaração no corpo do pedido. `allow_fallbacks` é
// SEMPRE `false` e não vem de campo nenhum: é o que faz da ordem uma fixação.
type wireProviderPolicy struct {
	Order          []string `json:"order"`
	AllowFallbacks bool     `json:"allow_fallbacks"`
	DataCollection string   `json:"data_collection,omitempty"`
}

// MarshalJSON implementa [json.Marshaler]: escreve a ordem, `allow_fallbacks: false` e, quando
// declarada, a retenção. É a MESMA forma no corpo do pedido e no digest do perfil — o que o dono
// assina é exactamente o que sai no wire.
func (p ProviderPolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireProviderPolicy{Order: p.Order, AllowFallbacks: false, DataCollection: p.DataCollection})
}

// IsZero diz se a declaração não declara nada.
func (p *ProviderPolicy) IsZero() bool {
	return p == nil || (len(p.Order) == 0 && p.DataCollection == "")
}

// Validate recusa uma declaração fora do conjunto fechado. Fail-closed: um perfil recusado não
// compõe gateway nenhum.
func (p *ProviderPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if len(p.Order) == 0 {
		return fmt.Errorf("%w: order vazia (uma declaracao de fornecedor sem ordem nao fixa nada)", ErrBadProviderPolicy)
	}
	if len(p.Order) > MaxProviderOrder {
		return fmt.Errorf("%w: order com mais de %d fornecedores", ErrBadProviderPolicy, MaxProviderOrder)
	}
	vistos := map[string]bool{}
	for _, n := range p.Order {
		if !nomeDeFornecedorValido(n) {
			return fmt.Errorf("%w: order tem um nome de fornecedor invalido (1 a %d bytes de letras, digitos, espaco e . _ -)", ErrBadProviderPolicy, maxNomeDeFornecedor)
		}
		if vistos[n] {
			return fmt.Errorf("%w: order tem um fornecedor repetido", ErrBadProviderPolicy)
		}
		vistos[n] = true
	}
	switch p.DataCollection {
	case "", DataCollectionDeny, DataCollectionAllow:
	default:
		return fmt.Errorf("%w: data_collection fora do vocabulario (aceites: %s, %s)", ErrBadProviderPolicy, DataCollectionDeny, DataCollectionAllow)
	}
	return nil
}

// Allows diz se um fornecedor a montante REPORTADO está na ordem declarada. A comparação é
// EXACTA, byte a byte: o nome declarado é texto assinado pelo dono, o reportado é texto de
// terceiros, e aproximar os dois (caixa, espaços) seria alargar uma guarda que existe para
// apertar. Um nome que o saneamento tenha alterado não está no alfabeto de um nome declarado e
// por isso nunca é permitido.
func (p *ProviderPolicy) Allows(upstream string) bool {
	if p == nil {
		return false
	}
	for _, n := range p.Order {
		if n == upstream {
			return true
		}
	}
	return false
}

// Manifest é a forma curta da declaração para o manifesto do turno
// ([ChatResponse.SentParams]). Só nomes do perfil assinado e vocabulário fechado.
func (p *ProviderPolicy) Manifest() string {
	if p.IsZero() {
		return ""
	}
	out := "order=" + strings.Join(p.Order, ",")
	if p.DataCollection != "" {
		out += ";data_collection=" + p.DataCollection
	}
	return out
}

// nomeDeFornecedorValido aceita um nome de fornecedor: de 1 a [maxNomeDeFornecedor] bytes de
// letras, dígitos, espaço e `. _ -`, sem espaço nas pontas. Os nomes reais (`Google`,
// `Together AI`, `amazon-bedrock`) cabem aqui; um valor com outra coisa é erro de quem escreveu
// o perfil, e recusa-se antes de ir para um digest, um corpo ou um evento.
func nomeDeFornecedorValido(v string) bool {
	if v == "" || len(v) > maxNomeDeFornecedor || v != strings.TrimSpace(v) {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-', c == ' ':
		default:
			return false
		}
	}
	return true
}

// UpstreamProviderUnreported é o valor de [ChatResponse.UpstreamProvider] quando a sonda correu
// e o proxy NÃO declarou o fornecedor a montante. Vocabulário fechado, no molde de
// `ServedLabelUnreported`: nunca um vazio ambíguo.
//
// É O CASO ESPERADO EM PRODUÇÃO HOJE. Que o fornecedor real emita a identidade efectiva está
// por medir (AOS-570a).
const UpstreamProviderUnreported = "nao_reportado"

// MaxUpstreamProvider é o tecto em bytes do nome do fornecedor a montante que se aceita do
// corpo da resposta.
const MaxUpstreamProvider = 128

// SanitizeUpstreamProvider saneia o nome do fornecedor a montante antes de ele sair do
// adaptador: retira os caracteres não imprimíveis, apara e corta em [MaxUpstreamProvider] bytes
// sem partir um carácter UTF-8. É texto de terceiros que acaba em claro num span e num selo.
func SanitizeUpstreamProvider(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s))
	if len(s) <= MaxUpstreamProvider {
		return s
	}
	corte := MaxUpstreamProvider
	for corte > 0 && !utf8.RuneStart(s[corte]) {
		corte--
	}
	return s[:corte]
}

// ProbeUpstreamProvider tira o FORNECEDOR A MONTANTE do corpo cru de uma resposta de chat: a
// chave de topo `provider`, quando é uma string. É uma SONDA, no molde de [ProbeResponseShape] e
// de [ProbeProviderState] — corre ao lado da descodificação da resposta, não a pode fazer
// falhar, nunca devolve erro e nunca entra em pânico.
//
// PORQUE É UMA SONDA E NÃO UM CAMPO DO WIRE. [ChatResponse] desserializa sem catch-all, e um
// campo novo no wire faria o contrato prometer uma chave que só um proxy emite. A sonda lê o
// corpo CRU, como as outras duas, e o valor entra na resposta por um campo `json:"-"`.
//
// SÓ UMA STRING CONTA. A forma medida é uma chave de topo com valor de texto. Um objecto, um
// número ou uma lista ali não se interpretam — que forma o fornecedor real usa, se usar alguma,
// é o AOS-570a —, e devolvem [UpstreamProviderUnreported] em vez de um palpite.
//
// Devolve sempre um valor do contrato: o nome saneado, ou [UpstreamProviderUnreported] quando a
// chave falta, não é uma string, está vazia ou o corpo não se lê. Um fornecedor que se chamasse
// literalmente `nao_reportado` seria indistinguível da ausência — e, por ser exacta a comparação
// com a ordem declarada, nunca seria admitido como conforme.
func ProbeUpstreamProvider(data []byte) (nome string) {
	defer func() {
		if recover() != nil {
			nome = UpstreamProviderUnreported
		}
	}()
	topo, ok := paresEmOrdem(data)
	if !ok {
		return UpstreamProviderUnreported
	}
	// A ÚLTIMA ocorrência, que é a que o `encoding/json` leria para um campo de struct.
	raw := ultimo(topo, "provider")
	if len(raw) == 0 {
		return UpstreamProviderUnreported
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return UpstreamProviderUnreported
	}
	if v = SanitizeUpstreamProvider(v); v == "" {
		return UpstreamProviderUnreported
	}
	return v
}
