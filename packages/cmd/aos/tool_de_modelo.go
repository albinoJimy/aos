package main

// tool_de_modelo.go — O QUE AS TOOLS CUJO EFEITO É UMA CHAMADA A UM MODELO PARTILHAM (ADR-042): a
// `ocr_read` (AOS-530, ocr_tool.go) e a `video_read` (AOS-536 parte 2, video_tool.go).
//
// O molde é o da `ocr_read`, e o que aqui está é o código dela, generalizado ao mínimo para servir
// as duas sem mudar o que a `ocr_read` faz — os testes do AOS-530 correm sem alteração:
//
//   - A LIGAÇÃO ([ligacaoDeModelo]): o alias, a fronteira de soberania, o perfil assinado, a pasta
//     semeada aberta com [os.Root], o gateway do nó e o audit de governação dele, o prazo, o digest
//     da instrução, a métrica por resultado e o custo (o orçamento por-run e a escrita durável).
//   - O ARRANQUE ([conferirArranqueDeModelo], [abrirPastaSemeada]): o alias na allowlist em vigor,
//     um registo assinado de uso `tool` DA MEDIA da tool para ele, o preço do par com a contabilidade
//     composta e a pasta. Cada tool tem os seus erros de arranque ([errosDeArranque]).
//   - A LEITURA CONFINADA ([lerConfinado], em ocr_tool.go): só o nível de topo, sem seguir
//     ligações simbólicas, a decisão sobre o descritor, até ao tecto de bytes da media.
//   - O CUSTO (ocr_custo.go): a reserva no tecto do run antes do pedido, o saldo pelo medido e o
//     evento durável [EventTypeToolModelCost] depois — o MESMO evento para as duas tools, com o
//     nome da tool no payload.
//   - A CHAMADA ([ligacaoDeModelo.chamar]): UMA chamada `Gateway.Chat`, com o token do run, o run e
//     o passo da tool call e a tool de origem; as falhas com causa de vocabulário fechado; a recusa
//     paga gravada; a resposta interpretada pela tool.
//
// O que é de cada tool fica no ficheiro dela: o formato da media, a instrução, o `max_tokens`, o
// prazo, a reserva por chamada e a forma do resultado.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/integration"
	audit "github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
)

// ligacaoDeModelo é a parte comum de uma tool composta cujo efeito é uma chamada ao gateway do nó.
type ligacaoDeModelo struct {
	// tool é o nome da tool (`ocr_read`, `video_read`): a tool de origem do pedido, o prefixo do
	// erro e o nome no evento de custo. rotulo é como os logs a nomeiam (com o ticket).
	tool, rotulo         string
	alias, region, board string
	// perfilDigest é o digest do perfil do registo assinado de uso `tool` do alias.
	perfilDigest string
	// raiz é a pasta semeada, aberta com [os.Root] no arranque ([raizOS]); pasta é o caminho, para
	// o arranque. raizAberta é o [os.Root] que [ligacaoDeModelo.fechar] fecha.
	raiz       raizDoOCR
	raizAberta *os.Root
	pasta      string
	// gw é o gateway do nó — o mesmo dos turnos. Ligado na composição do gateway.
	gw gatewayDoOCR
	// govAudit é o audit de governação desse gateway: onde a chamada da tool fica selada (o selo
	// por chamada da allowlist, com o principal, o run e o passo). Ligado com o gateway.
	govAudit audit.Store
	// prazo é o prazo da chamada ao gateway.
	prazo time.Duration
	// digestDaInstrucao é o digest da instrução fixa da tool.
	digestDaInstrucao string
	// contagem é a métrica da tool por resultado.
	contagem map[string]*atomic.Int64
	// reservaMax é o máximo que uma chamada pode custar, composto no arranque: o que a `ocr_read`
	// reserva em cada chamada, e o que a `video_read` reserva para o vídeo mais longo que aceita.
	reservaMax budget.Amount
	// orcamento é o orçamento por-run do nó (nil ⇒ sem orçamento composto, sem reserva) e
	// registarCusto a escrita durável do custo (nil ⇒ sem Event Store). Ligados no Bootstrap
	// ([ligacaoDeModelo.ligarCusto]); logf é o log do nó para as degradações declaradas.
	orcamento     *integration.RunBudget
	registarCusto func(context.Context, string, custoDeToolPayload) error
	logf          func(string, ...any)
}

// novaLigacaoDeModelo monta a parte comum, com a métrica a zero para cada resultado dado.
func novaLigacaoDeModelo(tool, rotulo, alias, region, board, perfilDigest, pasta string, raiz *os.Root, prazo time.Duration, digestDaInstrucao string, resultados []string) ligacaoDeModelo {
	l := ligacaoDeModelo{
		tool: tool, rotulo: rotulo,
		alias: alias, region: region, board: board, perfilDigest: perfilDigest,
		raiz: raizOS{raiz}, raizAberta: raiz, pasta: pasta, prazo: prazo,
		digestDaInstrucao: digestDaInstrucao,
		contagem:          map[string]*atomic.Int64{},
		logf:              func(string, ...any) {},
	}
	for _, r := range resultados {
		l.contagem[r] = new(atomic.Int64)
	}
	return l
}

// fechar liberta a pasta. Idempotente.
func (l *ligacaoDeModelo) fechar() {
	if l.raizAberta != nil {
		_ = l.raizAberta.Close()
	}
}

// contar soma um ao resultado dado. Um resultado fora do vocabulário não é contado.
func (l *ligacaoDeModelo) contar(resultado string) {
	if n := l.contagem[resultado]; n != nil {
		n.Add(1)
	}
}

// lido devolve o total de um resultado.
func (l *ligacaoDeModelo) lido(resultado string) int64 {
	if l.contagem[resultado] == nil {
		return 0
	}
	return l.contagem[resultado].Load()
}

// falhar conta a causa e devolve o erro da tool, com o custo medido (zero antes do provider).
func (l *ligacaoDeModelo) falhar(causa string, custo int64) ([]byte, int64, error) {
	l.contar(causa)
	// AOS-550: a causa é conferida contra o vocabulário FECHADO da tool. A mensagem já era
	// construída de duas partes conhecidas; o que se acrescenta é recusar uma causa que não esteja
	// declarada — a mensagem de erro vai no CORPO do segmento `tool_result` (erro_de_tool.go).
	if !vocabularioDeErro()[l.tool][causa] {
		return nil, custo, &falhaDeTool{tool: l.tool, causa: causaNaoDeclarada}
	}
	return nil, custo, &falhaDeTool{tool: l.tool, causa: causa}
}

// falhaDeTool é o erro de uma tool de modelo: o nome dela e só a causa.
type falhaDeTool struct{ tool, causa string }

func (f *falhaDeTool) Error() string { return f.tool + ": falha=" + f.causa }

// causaDaFalhaDeTool devolve a causa de um erro de uma tool de modelo, ou "" se não é uma falha
// de uma delas.
func causaDaFalhaDeTool(err error) string {
	var f *falhaDeTool
	if errors.As(err, &f) {
		return f.causa
	}
	return ""
}

// errosDeArranque são os erros de arranque de uma tool de modelo, um por condição.
type errosDeArranque struct {
	foraDaAllowlist, semRegisto, semPreco, semPasta error
}

// conferirArranqueDeModelo confere as condições de ARRANQUE de uma tool de modelo que não abrem
// nada, pela ordem: o alias na allowlist EM VIGOR (externa, ou nil ⇒ a embebida) — a mesma que o
// gateway vai aplicar a cada pedido —; um registo assinado de uso `tool` DA MEDIA dada para o alias;
// e, com a contabilidade composta, o preço do par (alias, região). Devolve o alias, o digest do
// perfil e a tabela de preços (nil sem contabilidade).
func conferirArranqueDeModelo(spec *modelToolSpec, media string, assinados *perfisAssinados, externa *allowlist.Policy, region, board string, comCusto bool, e errosDeArranque) (string, string, *pricing.Table, error) {
	alias := strings.TrimSpace(spec.Modelo.Alias)
	// 1. O alias na allowlist EM VIGOR.
	emVigor := externa
	if emVigor == nil {
		var err error
		if emVigor, err = allowlist.LoadPolicy(); err != nil {
			return "", "", nil, fmt.Errorf("%w: %w", ErrBadModelAllowlist, err)
		}
	}
	if emVigor.Evaluate(allowlist.Input{Board: board, Model: alias, Region: region}) != allowlist.EffectAllow {
		return "", "", nil, fmt.Errorf("%w: alias %q, board %q, regiao %q", e.foraDaAllowlist, alias, board, region)
	}
	// 2. Um registo assinado de uso `tool` DA MEDIA DA TOOL para o alias. Um registo de imagem cita
	// uma medida de OCR e um de vídeo uma medida de vídeo (AOS-536): um não liga a tool do outro.
	perfilDigest := ""
	if assinados != nil {
		for _, r := range assinados.entradas {
			if r.ForTool() && r.Media == media && r.Profile.Requested == alias {
				perfilDigest = r.ProfileDigest
				break
			}
		}
	}
	if perfilDigest == "" {
		return "", "", nil, fmt.Errorf("%w: alias %q", e.semRegisto, alias)
	}
	// 3. Com a contabilidade composta, o preço do par (alias, região) — e é dele que sai o custo
	// máximo por chamada que a tool reserva no tecto do run.
	var tabela *pricing.Table
	if comCusto {
		t, postura, err := resolveModelPricing(alias, region)
		if err != nil {
			return "", "", nil, err
		}
		if !postura.Armed {
			return "", "", nil, fmt.Errorf("%w: alias %q, regiao %q", e.semPreco, alias, region)
		}
		tabela = t
	}
	return alias, perfilDigest, tabela, nil
}

// abrirPastaSemeada abre, confinada, a pasta semeada da variável [ocrSeedDirEnv] — a MESMA para as
// duas tools de modelo (ver video_tool.go): o `doc://{doc_id}` que o RM decide e o WORM sela nomeia
// o mesmo ficheiro qualquer que seja a tool.
func abrirPastaSemeada(semPasta error) (*os.Root, string, error) {
	pasta := strings.TrimSpace(os.Getenv("AOS_OCR_SEED_DIR")) // o literal: o gate da superficie de ambiente le-o daqui
	if pasta == "" {
		return nil, "", semPasta
	}
	raiz, err := os.OpenRoot(pasta)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", semPasta, err)
	}
	return raiz, pasta, nil
}

// tectoDaChamada é o que uma resposta NÃO MEDIDA cobra, em tokens: os tectos de entrada e de saída
// da chamada ([ligacaoDeModelo.fecharPago]).
type tectoDaChamada struct{ entrada, saida int64 }

// chamar faz a chamada da tool ao gateway do nó e fecha-a, DEPOIS de a media lida e a reserva feita.
// É o efeito da tool call: UMA chamada `Gateway.Chat`, com o token do run, o run e o passo da call e
// a tool de origem; sem tools oferecidas ao modelo; sem nova tentativa e sem failover.
//
// A resposta — aceite ou recusada, pela tool ou pelo gateway depois de o provider responder — salda a
// reserva e grava o custo ([ligacaoDeModelo.fecharPago]); uma chamada sem resposta liberta-a (o
// `defer`, também num panic dentro do gateway). `interpretar` decide o que a resposta é: o
// resultado e `sucesso`, ou a causa da recusa.
func (l *ligacaoDeModelo) chamar(ctx context.Context, in entradaOCR, credencial string, reserva *integration.ReservaDeEfeito, cobrar tectoDaChamada,
	mensagens []port.Message, maxTokens int, sucesso string, interpretar func(resp port.ChatResponse, servido string) ([]byte, string)) ([]byte, int64, error) {
	// A reserva que não for saldada volta ao run: é o caminho de uma chamada sem resposta (erro antes
	// do provider, prazo, cancelamento) e de um panic dentro do gateway. Depois de saldada, libertar
	// é um no-op (a reserva resolve-se uma vez).
	defer l.libertar(ctx, reserva, in)
	cctx, cancel := context.WithTimeout(ctx, l.prazo)
	defer cancel()
	resp, err := l.gw.Chat(cctx, port.ChatRequest{
		Model:      l.alias,
		Messages:   mensagens,
		MaxTokens:  maxTokens,
		Principal:  credencial,
		Region:     l.region,
		Board:      l.board,
		RunID:      in.RunID,
		StepID:     in.StepID,
		OriginTool: l.tool,
	})
	if err != nil {
		var causa string
		switch {
		case errors.Is(err, modelgateway.ErrRouteVariance):
			causa = ocrCausaModeloServido
		case ctx.Err() != nil:
			causa = ocrCausaCancelado
		case cctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
			causa = ocrCausaTempo
		default:
			causa = ocrCausaGateway
		}
		// Uma recusa DEPOIS de o provider responder (o modelo servido diferente em `enforce`, uma
		// resposta sem usage ou ilegível) custou dinheiro: o gateway devolve o custo — ou a marca de
		// não medido — com o erro ([modelgateway.PaidRejectionError]), e grava-se. Sem resposta não há
		// custo conhecido e a reserva volta ao run (o `defer` acima).
		if usage, servido, pago := modelgateway.PaidRejectionOf(err); pago {
			return l.falhar(causa, l.fecharPago(ctx, reserva, in, cobrar, usage, servido, causa))
		}
		return l.falhar(causa, 0)
	}
	// O custo medido pelo gateway (tabela de preços; zero sem contabilidade composta). Volta pelo
	// canal do RegisterCosting e grava-se no evento durável — também quando a tool recusa a
	// resposta: o provider respondeu.
	servido := resp.Route.Model
	if servido == "" {
		// Sem a governação da rota o gateway não guarda o cabeçalho do proxy: fica o modelo que o
		// corpo da resposta declara.
		servido = port.SanitizeServedModel(resp.Model)
	}
	out, resultado := interpretar(resp, servido)
	custo := l.fecharPago(ctx, reserva, in, cobrar, resp.Usage, servido, resultado)
	if resultado != sucesso {
		return l.falhar(resultado, custo)
	}
	l.contar(sucesso)
	return out, custo, nil
}

// textoDaResposta é a parte comum de interpretar uma resposta: o texto da primeira escolha, ou a
// causa da recusa — `vazio` sem escolhas ou com o texto em branco, [ocrCausaRespostaForma] com uma
// tool call (o modelo não tem tools oferecidas), `cortado` com o motivo de paragem `length` (cortado
// não é completo: ADR-037, a lição do Gemini na medição).
func textoDaResposta(resp port.ChatResponse, cortado, vazio string) (string, string) {
	if len(resp.Choices) == 0 {
		return "", vazio
	}
	escolha := resp.Choices[0]
	if len(escolha.Message.ToolCalls) > 0 {
		return "", ocrCausaRespostaForma
	}
	if escolha.FinishReason == port.FinishLength {
		return "", cortado
	}
	if strings.TrimSpace(escolha.Message.Content) == "" {
		return "", vazio
	}
	return escolha.Message.Content, ""
}
