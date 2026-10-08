package main

// A FORMA DO PEDIDO AO MODELO (AOS-490, ADR-036 §2.4) — `AOS_MODEL_PROJECTION`.
//
// O tail é a forma canónica da conversa de um run; o que o adaptador do Model Gateway envia ao
// provider é uma projecção dele. Há duas, e o nó escolhe uma no arranque:
//
//   - `native` (por omissão) — mensagens nativas: `system`, `user`, e por turno do modelo um
//     `assistant` com `tool_calls` e uma mensagem `tool` por chamada;
//   - `text` — o prompt materializado numa só mensagem de utilizador, a forma anterior ao
//     AOS-490, byte a byte.
//
// A nativa só se aplica a runs no layout 1.4.0: um run começado na 1.3.0 (uma retoma antiga)
// continua em texto único qualquer que seja o valor. O modo usado em cada turno fica no
// manifesto do `turn.recorded`.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// defaultModelProjection é a projecção de um nó que não define AOS_MODEL_PROJECTION.
const defaultModelProjection = modelgateway.ProjectionNative

// ErrBadModelProjection — AOS_MODEL_PROJECTION está definida com um valor fora do vocabulário
// fechado. Fail-closed: o nó não arranca. Cair para um dos modos em silêncio deixaria o
// operador convencido de que o modelo recebe uma forma e a receber a outra — e é pela forma do
// pedido que se lê o comportamento do modelo em produção.
var ErrBadModelProjection = errors.New("aos: AOS_MODEL_PROJECTION invalida — valores aceites: native (mensagens nativas derivadas do tail; por omissao) ou text (o prompt inteiro numa mensagem de utilizador)")

// parseModelProjectionFromEnv lê AOS_MODEL_PROJECTION. Vazia ⇒ [defaultModelProjection]. Um
// valor fora do vocabulário ⇒ [ErrBadModelProjection]. Sem normalização de caixa: `Native` não é
// `native`, pela regra das outras variáveis de vocabulário fechado do nó.
func parseModelProjectionFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_MODEL_PROJECTION"))
	if raw == "" {
		return defaultModelProjection, nil
	}
	mode, err := modelgateway.ParseProjection(raw)
	if err != nil {
		return "", fmt.Errorf("%w (veio %q)", ErrBadModelProjection, raw)
	}
	return mode, nil
}

// modelProjectionOption é a opção do adaptador RT→GW para o modo dado (já validado).
func modelProjectionOption(mode string) modelgateway.RuntimeAdapterOption {
	return modelgateway.WithProjection(mode)
}

// A VERSÃO DA PROJECÇÃO NATIVA (AOS-504, emenda ao ADR-036 §2.4) — `AOS_MODEL_PROJECTION_VERSION`.
//
//   - `1.0.0` (por omissão) — a projecção de sempre, byte a byte;
//   - `1.1.0` — cada segmento de uma mensagem `user` ou `tool` termina com a linha de fim
//     `</kind>`, e o texto do protocolo é reescrito para o objectivo não se confundir com dados;
//   - `1.2.0` (AOS-506) — a 1.1.0 com outro texto de protocolo: uma tool só se pede pelo
//     mecanismo nativo de function calling, nunca escrita no texto da resposta. As mensagens
//     `user`, `assistant` e `tool` são, byte a byte, as da 1.1.0.
//
// A 1.1.0 e a 1.2.0 entram DESLIGADAS: o texto do protocolo é lido por todos os runs, e só passa a omissão
// depois de medida numa série de planos em produção. A versão usada em cada turno fica em
// `manifest.projection_version` do `turn.recorded`. Não muda o layout, o tail nem o
// `prompt_hash`. Só tem efeito com `AOS_MODEL_PROJECTION=native`: em texto único não há
// projecção, e a variável é validada mas não usada.

// defaultModelProjectionVersion é a versão da projecção nativa de um nó que não define
// AOS_MODEL_PROJECTION_VERSION.
const defaultModelProjectionVersion = modelgateway.NativeProjectionVersion

// ErrBadModelProjectionVersion — AOS_MODEL_PROJECTION_VERSION está definida com um valor fora do
// vocabulário fechado. Fail-closed: o nó não arranca. Cair para uma das versões em silêncio
// deixaria o operador a medir uma série de planos convencido de que o modelo recebia um texto de
// protocolo e a receber o outro.
var ErrBadModelProjectionVersion = errors.New("aos: AOS_MODEL_PROJECTION_VERSION invalida — valores aceites: 1.0.0 (a projeccao nativa de sempre; por omissao) 1.1.0 (linha de fim por segmento e texto de protocolo novo, AOS-504) 1.2.0 (a 1.1.0 com o texto de protocolo que diz que uma tool so se pede por function calling, AOS-506) ou 1.3.0 (a 1.2.0 que devolve ao provider o estado opaco de cada turno, so a rota que o produziu e so se o perfil dessa rota o pedir, AOS-515)")

// parseModelProjectionVersionFromEnv lê AOS_MODEL_PROJECTION_VERSION. Vazia ⇒
// [defaultModelProjectionVersion]. Um valor fora do vocabulário ⇒ [ErrBadModelProjectionVersion].
// Sem normalização: `1.1` e `v1.1.0` não são `1.1.0`.
func parseModelProjectionVersionFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_MODEL_PROJECTION_VERSION"))
	if raw == "" {
		return defaultModelProjectionVersion, nil
	}
	version, err := modelgateway.ParseNativeProjectionVersion(raw)
	if err != nil {
		return "", fmt.Errorf("%w (veio %q)", ErrBadModelProjectionVersion, raw)
	}
	return version, nil
}

// modelProjectionVersionOption é a opção do adaptador RT→GW para a versão dada (já validada).
func modelProjectionVersionOption(version string) modelgateway.RuntimeAdapterOption {
	return modelgateway.WithProjectionVersion(version)
}

// modelProjectionBanner declara a projecção com que o nó fala com o modelo. Só sai quando há um
// gateway composto (gatewayComposed): o modelo de referência não fala com provider nenhum.
func modelProjectionBanner(gatewayComposed bool, mode string) []string {
	return modelProjectionBannerFor(gatewayComposed, mode, defaultModelProjectionVersion)
}

// modelProjectionBannerFor é [modelProjectionBanner] com a versão da projecção nativa dada
// (AOS-504). Com a versão por omissão as linhas são, byte a byte, as de antes; com a 1.1.0 a
// linha das mensagens nativas declara-a e segue-se uma que diz o que ela muda e como se repõe a
// de sempre. Em texto único a versão não se aplica, e uma 1.1.0 pedida é declarada sem efeito.
func modelProjectionBannerFor(gatewayComposed bool, mode, version string) []string {
	if !gatewayComposed {
		return nil
	}
	if mode == modelgateway.ProjectionText {
		lines := []string{
			"projeccao do pedido ao modelo (EPIC-06/AOS-490): TEXTO UNICO — AOS_MODEL_PROJECTION=text: o prompt materializado segue inteiro numa mensagem de utilizador (a forma anterior ao AOS-490); o modelo le as suas proprias tool calls como texto. Para mensagens nativas remova a variavel ou defina native",
		}
		if version != defaultModelProjectionVersion {
			lines = append(lines, fmt.Sprintf("versao da projeccao nativa (EPIC-06/AOS-504): AOS_MODEL_PROJECTION_VERSION=%s SEM EFEITO — em texto unico nao ha projeccao nativa", version))
		}
		return lines
	}
	lines := []string{
		fmt.Sprintf("projeccao do pedido ao modelo (EPIC-06/AOS-490): MENSAGENS NATIVAS (versao %s) — system/user/assistant com tool_calls/tool, derivadas do tail; aplica-se a runs no layout 1.4.0 (um run retomado na 1.3.0 segue em texto unico) e o modo de cada turno fica em manifest.projection do turn.recorded. O prompt_hash continua a ser o do tail canonico, nao o dos bytes enviados. AOS_MODEL_PROJECTION=text repoe o texto unico", version),
	}
	if version == modelgateway.NativeProjectionVersion130 {
		return append(lines, fmt.Sprintf("versao da projeccao nativa (EPIC-06/AOS-515, ADR-040): AOS_MODEL_PROJECTION_VERSION=%s — a 1.2.0, com o mesmo texto de protocolo e as mesmas mensagens, que DEVOLVE ao provider o estado opaco de cada turno anterior (raciocinio, blocos assinados e redigidos, assinatura por tool call), byte a byte, na mensagem assistant desse turno. So sai para uma rota cujo perfil declare devolver=opcional ou obrigatorio, e so o estado que essa mesma rota produziu; com devolver=nunca (todos os perfis de hoje) os pedidos sao os da 1.2.0, byte a byte. Exige AOS_MODEL_PROVIDER_STATE=capture e AOS_MODEL_ROUTE_GOVERNANCE=observe ou enforce: sem a captura nao ha estado, e sem a governacao da rota o estado nao diz de que rota e. Numa rota obrigatorio, um turno sem estado devolvivel faz o pedido NAO sair, e o run falha com a causa. O raciocinio nunca e resposta nem texto do tail. Remova a variavel ou defina %s para repor a projeccao de sempre", version, defaultModelProjectionVersion))
	}
	if version == modelgateway.NativeProjectionVersion120 {
		// AOS-506: a 1.2.0 tem a sua linha. As da omissão e da 1.1.0 ficam como estavam.
		return append(lines, fmt.Sprintf("versao da projeccao nativa (EPIC-06/AOS-506): AOS_MODEL_PROJECTION_VERSION=%s — a 1.1.0 (linha de fim </kind> por segmento, escape das quase-forjas) com OUTRO texto de protocolo na mensagem system: uma tool so se pede por uma function call feita pelo mecanismo de function calling, nunca escrita no texto da resposta; as respostas do modelo nao sao feitas de segmentos. O runtime NAO interpreta texto do modelo como tool call. As mensagens user, assistant e tool sao as da 1.1.0; o layout, o tail e o prompt_hash nao mudam, e a versao de cada turno fica em manifest.projection_version. A mensagem system muda: os tokens servidos de cache de prefixo caem na troca. Remova a variavel ou defina %s para repor a projeccao de sempre", version, defaultModelProjectionVersion))
	}
	if version != defaultModelProjectionVersion {
		lines = append(lines, fmt.Sprintf("versao da projeccao nativa (EPIC-06/AOS-504): AOS_MODEL_PROJECTION_VERSION=%s — cada segmento das mensagens user e tool termina com a linha de fim </kind>, e o texto do protocolo (a mensagem system) e o da %s; o layout, o tail e o prompt_hash nao mudam, e a versao de cada turno fica em manifest.projection_version. A mensagem system muda: os tokens servidos de cache de prefixo caem na troca. Remova a variavel ou defina %s para repor a projeccao de sempre", version, version, defaultModelProjectionVersion))
	}
	return lines
}
