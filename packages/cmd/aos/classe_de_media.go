package main

// classe_de_media.go — O EFEITO DE UMA TOOL DE MODELO, ESCRITO UMA SÓ VEZ (AOS-566, fatia 1).
//
// A `ocr_read` (ocr_tool.go) e a `video_read` (video_tool.go) faziam, cada uma no seu ficheiro, a
// MESMA sequência: descodificar a entrada, exigir a credencial NHI antes de tocar no ficheiro, ler
// a media, reservar no tecto do run e chamar o gateway. Duas cópias da mesma ordem são dois sítios
// onde a ordem pode divergir — e a ordem É a propriedade (a identidade antes da leitura, a reserva
// antes do pedido: ADR-042 §2.2 e §2.6). Aqui a sequência existe uma vez, em [executarDeModelo], e
// o que cada tool tem de seu entra por [classeDeMedia].
//
// O QUE É DE CADA TOOL são quatro coisas, e são exactamente os campos de [classeDeMedia]: o schema
// do input, a instrução fixa com o `max_tokens` e o nome do resultado de sucesso, e a preparação da
// media — a leitura, a reserva desta chamada e a leitura da resposta ([pedidoDeMedia]).
//
// A MENSAGEM DEIXOU DE SER ESPECÍFICA. A parte da media vai numa [port.MediaPart] e entra na
// mensagem por [port.Message.AttachMedia] (AOS-546, ADR-043 §2.2), que a põe no campo da CLASSE
// dela — `Images` ou `Videos` — por uma tabela única na porta. Nenhum `switch` por modalidade vive
// aqui: uma modalidade nova é uma linha nessa tabela, não um ramo neste ficheiro.
//
// Esta fatia é só a extracção: [ligacaoOCR] e [ligacaoVideo] continuam a ser duas structs, e o que
// as duas tools fazem não mudou.

import (
	"context"
	"encoding/json"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/platform/model-gateway/port"
)

// classeDeMedia é o que distingue uma tool de modelo da outra no efeito da tool call. É dado, não
// comportamento: cada `executar` declara-o e a sequência fica em [executarDeModelo].
type classeDeMedia struct {
	// schema é a forma do input DEPOIS da reescrita do efeito ([newEfeitoDeModeloRewriter]): o que
	// a entrada tem de declarar para ser desta tool.
	schema string
	// sistema e pedido são a instrução fixa e versionada da tool — a mensagem `system` e o texto da
	// mensagem `user` que leva a media.
	sistema, pedido string
	// maxTokens é o `max_tokens` da chamada e sucesso o nome do resultado quando a tool aceita a
	// resposta (`transcrito`, `descrito`).
	maxTokens int
	sucesso   string
	// preparar lê a media do `doc_id` e apura o que a chamada dela precisa, ou devolve a causa da
	// recusa (vocabulário fechado da tool). Nada do que ela faz chama o modelo nem custa dinheiro.
	preparar func(ctx context.Context, in entradaOCR) (pedidoDeMedia, string)
}

// pedidoDeMedia é o que a leitura de uma media apura para a chamada dela: a parte que vai na
// mensagem, quanto reservar no tecto do run, o que uma resposta NÃO MEDIDA cobra e como se lê a
// resposta. Os factos da media que a leitura apurou — o formato de uma imagem, a duração de um
// vídeo — ficam fechados em `interpretar`, que é o único sítio que os volta a precisar.
type pedidoDeMedia struct {
	parte       port.MediaPart
	maximo      budget.Amount
	cobrar      tectoDaChamada
	interpretar func(resp port.ChatResponse, servido string) ([]byte, string)
}

// executarDeModelo é a [referencemonitor.CostingToolFunc] de uma tool de modelo, menos a classe da
// media: a sequência que a `ocr_read` e a `video_read` partilham, pela ordem que é a propriedade.
// Só o despacho do RM lá chega, depois do permit.
func executarDeModelo(ctx context.Context, l *ligacaoDeModelo, c classeDeMedia, input []byte) ([]byte, int64, error) {
	var in entradaOCR
	if err := json.Unmarshal(input, &in); err != nil || in.Schema != c.schema || in.DocID == "" || in.RunID == "" || in.StepID == "" {
		return l.falhar(ocrCausaEntrada, 0)
	}
	// A IDENTIDADE ANTES DE TUDO: sem o token do run não se lê o ficheiro (ADR-042 §2.2).
	credencial := modelCredentialFromContext(ctx)
	if credencial == "" {
		return l.falhar(ocrCausaSemNHI, 0)
	}
	pedido, causa := c.preparar(ctx, in)
	if causa != "" {
		return l.falhar(causa, 0)
	}
	// A MENSAGEM MONTA-SE ANTES DA RESERVA, e isso é de propósito: uma recusa depois de reservar
	// deixaria a reserva por saldar — quem a liberta é o `defer` de [ligacaoDeModelo.chamar], e aqui
	// ainda não se lá chegou. Montar primeiro mantém o caminho de recusa sem efeito no tecto do run.
	//
	// O ERRO DE [port.Message.AttachMedia] É INALCANÇÁVEL pelas duas tools de hoje — cada uma já
	// validou a parte na leitura, e a do espaço de handles traz o tipo que a porta do AOS-561
	// conferiu PELOS BYTES antes de o run abrir —, mas não se ignora: fail-closed, com a causa de
	// formato, que é a mesma que a leitura daria.
	utilizador := port.Message{Role: port.RoleUser, Content: c.pedido}
	if err := utilizador.AttachMedia(pedido.parte); err != nil {
		return l.falhar(ocrCausaFormato, 0)
	}
	// A RESERVA (ADR-042 §2.6): com a media lida e antes do pedido, o máximo DESTA chamada no tecto
	// do run. Não cabe ⇒ negada sem chamar o modelo.
	reserva, causa := l.reservar(ctx, in.RunID, pedido.maximo)
	if causa != "" {
		return l.falhar(causa, 0)
	}
	return l.chamar(ctx, in, credencial, reserva, pedido.cobrar,
		[]port.Message{{Role: port.RoleSystem, Content: c.sistema}, utilizador},
		c.maxTokens, c.sucesso, pedido.interpretar)
}
