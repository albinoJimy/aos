package main

// ENTRADAS POR HANDLE: A RESOLUÇÃO A JUSANTE DO RM (AOS-550, fase R2 do ADR-043) —
// `AOS_HANDLE_NOS_ARGUMENTOS`.
//
// Um argumento de uma tool pode ser uma REFERÊNCIA a conteúdo em quarentena
// ([agentruntime.HandleRefPrefix], `aos-handle/v1:h1`) em vez dos bytes. O modelo emite a
// referência; o REFERENCE MONITOR DECIDE COM ELA; e só depois — dentro do executor da tool, que
// corre apenas em `permit` — o nó troca a referência pelos bytes, lidos do armazém de
// artefactos do run (AOS-548) por `(titular, run, digest)`.
//
//   - `off` (por omissão): desligado. Nenhuma referência é reconhecida, nenhum handle é
//     cunhado, e o nó é o de sempre — byte a byte.
//   - `on` (exige `AOS_ARTIFACT_DIR`): a `artifact_write` devolve, com a referência do
//     artefacto, o handle que lhe corresponde; e o `content` de uma `artifact_write` pode ser
//     exactamente um handle, que o nó resolve depois da decisão.
//
// OS BYTES NÃO ATRAVESSAM NADA DO QUE SE REGISTA. O que vai no tail, no `prompt_hash`, no
// hash canónico do span `execute_tool`, no selo de mediação e na mensagem de uma recusa é a
// REFERÊNCIA — um id opaco de 2 ou 3 bytes. A troca pelos bytes acontece depois de tudo isso,
// dentro do executor, e os bytes não voltam para cima: o resultado da `artifact_write` é a
// referência do artefacto, como sempre foi.
//
// PORQUE É SEGURO O RM DECIDIR SOBRE A REFERÊNCIA E A TOOL RECEBER OUTROS BYTES. A referência
// é CONTENT-ADDRESSED: o id resolve num digest que o nó guardou, e o armazém, depois de
// decifrar, confere que os bytes batem com esse digest ([armazemDeArtefactos.abrir]). O que a
// tool recebe é exactamente o conteúdo que a referência nomeia — não há folga onde pôr outro.
// É o mesmo argumento com que a `media_view` (AOS-546) deixa o adaptador resolver os bytes de
// uma imagem depois de o RM ter autorizado a referência.
//
// UM HANDLE FORJADO É RECUSADO POR DUAS CONDIÇÕES INDEPENDENTES:
//
//	(a) SÓ UMA REFERÊNCIA PRODUZIDA POR UMA TOOL DE CÓDIGO DO NÓ É RECONHECIDA — a regra que o
//	    `media.go:26-32` escreveu: o nome da tool é a fronteira de confiança. Um handle só entra
//	    na quarentena de um run a partir de um `artifact.created` que o armazém do nó selou; uma
//	    referência escrita no VALOR de outra tool (um documento lido, uma resposta remota) é
//	    conteúdo de terceiros e não cunha nada.
//	(b) O PAR `(run, digest)` TEM DE CONSTAR DE UM `artifact.created` DO STREAM DO RUN — a
//	    verificação que o [artefactosDoRun] já fazia no plano HTTP, aqui disponível ao runtime.
//	    É imposta DUAS vezes e em pontos distintos: na derivação dos ids (um artefacto sem
//	    evento não tem id nenhum) e à cabeça de cada leitura ([quarentenaDoArmazem.OpenQuarantined]).
//	    Um ficheiro na pasta do armazém sem evento não é resolúvel, como não é servível.
//
// OS IDS SÃO UMA FUNÇÃO PURA DO STREAM. `h1`, `h2`, … pela ordem dos eventos
// `artifact.created` do run — sem relógio e sem `rand`. A derivação é ÚNICA: tanto o handle
// que a tool devolve ao vivo como o handle que uma retoma ou um replay reconstroem saem do
// mesmo fold sobre os mesmos eventos duráveis, pelo que não há dois caminhos que possam
// divergir.
//
// ENTRE RUNS, NÃO. A quarentena é a de UM run: um digest de outro run não resolve, mesmo que
// esteja no mesmo volume. A travessia da aresta do plano é o AOS-553, e é pela declaração
// selada do plano (ADR-038) — nunca por um digest que o modelo forneça.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/substrate/eventstore"
)

// Os dois valores de AOS_HANDLE_NOS_ARGUMENTOS.
const (
	handleNosArgumentosOff = "off"
	handleNosArgumentosOn  = "on"
)

// Causas de recusa da resolução de um handle. Vocabulário FECHADO, como o das outras tools do
// nó: nenhuma delas leva um byte do conteúdo nem um digest.
const (
	handleCausaDesconhecido = "handle_nao_reconhecido"
	handleCausaSemEvento    = "handle_sem_proveniencia"
	handleCausaTecto        = "handle_acima_do_tecto"
	handleCausaIlegivel     = "handle_ilegivel"
	handleCausaRun          = "handle_sem_run"
)

var (
	// ErrBadHandleNosArgumentos — AOS_HANDLE_NOS_ARGUMENTOS fora do vocabulário. O nó recusa arrancar.
	ErrBadHandleNosArgumentos = errors.New("aos: AOS_HANDLE_NOS_ARGUMENTOS invalida — valores aceites: off (por omissao; nenhuma referencia a handle e reconhecida) e on (a artifact_write devolve o handle do artefacto e aceita um handle como content, resolvido a jusante da decisao do RM)")
	// ErrHandleSemArmazem — `on` sem AOS_ARTIFACT_DIR: a quarentena não tem armazém por baixo.
	ErrHandleSemArmazem = errors.New("aos: AOS_HANDLE_NOS_ARGUMENTOS=on exige AOS_ARTIFACT_DIR — o conteudo de um handle vive no armazem de artefactos do run (cifrado por titular), nao em memoria")
	// ErrHandleSemEventStore — `on` sem Event Store: sem ele não há `artifact.created` para ler, e
	// a condição (b) do handle forjado não se consegue verificar.
	ErrHandleSemEventStore = errors.New("aos: AOS_HANDLE_NOS_ARGUMENTOS=on exige o Event Store do no: o par (run, digest) de um handle tem de constar de um artifact.created do stream do run")
	// ErrSelarEmQuarentenaPorLigar — o caminho de ENTRADA directa em quarentena (um `tool_result`
	// ou um `plan_input` selado pelo kernel) não está ligado neste nó, e é fail-closed de
	// propósito: uma gravação sem o evento de proveniência produziria um handle que a condição
	// (b) recusa a seguir. No AOS-550 o único conteúdo que entra em quarentena é o que a
	// `artifact_write` grava, com o seu próprio `artifact.created`. A separação de planos — quem
	// sela `tool_result` e `plan_input` — é o AOS-551.
	ErrSelarEmQuarentenaPorLigar = errors.New("aos: selar conteudo em quarentena sem evento de proveniencia nao e permitido neste no (a separacao de planos entra com o AOS-551)")
)

// parseHandleNosArgumentosFromEnv lê AOS_HANDLE_NOS_ARGUMENTOS. Vazia ⇒ false (`off`).
func parseHandleNosArgumentosFromEnv() (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv("AOS_HANDLE_NOS_ARGUMENTOS")); raw {
	case "", handleNosArgumentosOff:
		return false, nil
	case handleNosArgumentosOn:
		return true, nil
	default:
		return false, fmt.Errorf("%w (veio %q)", ErrBadHandleNosArgumentos, raw)
	}
}

// planoDeHandles é o plano de handles do nó ligado: o armazém por baixo, a leitura dos eventos
// de proveniência, e os contadores.
type planoDeHandles struct {
	armazem *armazemDeArtefactos
	// declarados devolve os artefactos que os eventos do stream do run declaram, pela ordem dos
	// eventos. É a condição (b) na sua forma legível.
	declarados func(ctx context.Context, runID string) ([]artefactoCriado, error)

	// cunhados conta os handles devolvidos ao modelo; resolvidos, as trocas de referência por
	// bytes; recusados, as resoluções que a recusa fechou.
	cunhados, resolvidos, recusados atomic.Int64
}

// comporPlanoDeHandles compõe o plano quando o interruptor está `on`. `off` ⇒ nil, sem ler
// nada. Fail-closed: `on` sem armazém ou sem Event Store aborta o arranque.
func comporPlanoDeHandles(ligado bool, armazem *armazemDeArtefactos, es eventstore.EventStore) (*planoDeHandles, error) {
	if !ligado {
		return nil, nil
	}
	if armazem == nil {
		return nil, ErrHandleSemArmazem
	}
	if es == nil {
		return nil, ErrHandleSemEventStore
	}
	return &planoDeHandles{armazem: armazem, declarados: declaracoesDeArtefactos(es)}, nil
}

// declaracoesDeArtefactos lê o stream do run e devolve os artefactos que os seus eventos
// declaram, pela ordem dos eventos — o MESMO [artefactosDoRun] que o plano HTTP usa para só
// servir um digest que o próprio run declara, agora disponível ao runtime.
func declaracoesDeArtefactos(es eventstore.EventStore) func(context.Context, string) ([]artefactoCriado, error) {
	return func(ctx context.Context, runID string) ([]artefactoCriado, error) {
		if runID == "" {
			return nil, errors.New("aos: artefactos declarados sem run_id")
		}
		events, err := es.Read(ctx, runID, 1)
		if err != nil {
			if errors.Is(err, eventstore.ErrStreamNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return artefactosDoRun(events), nil
	}
}

// quarentenaDoArmazem é o [agentruntime.QuarantineStore] do nó: o armazém de artefactos do run,
// com a condição (b) à cabeça de cada leitura.
type quarentenaDoArmazem struct {
	plano *planoDeHandles
}

// SealQuarantined — ver [ErrSelarEmQuarentenaPorLigar]. Fail-closed, e nomeado.
func (s quarentenaDoArmazem) SealQuarantined(context.Context, string, string, []byte) (string, error) {
	return "", ErrSelarEmQuarentenaPorLigar
}

// OpenQuarantined lê e decifra o conteúdo que `(titular, run, digest)` nomeia, DEPOIS de
// confirmar que um `artifact.created` do stream do run o declara com esse titular. Sem o
// evento não há bytes: é a condição (b), no ponto onde os bytes sairiam.
func (s quarentenaDoArmazem) OpenQuarantined(ctx context.Context, subject, runID, digest string) ([]byte, error) {
	declarados, err := s.plano.declarados(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, a := range declarados {
		if a.Digest == digest && a.SealedSubject == subject {
			return s.plano.armazem.abrir(ctx, subject, runID, digest)
		}
	}
	return nil, fmt.Errorf("aos: o run %s nao declara o artefacto deste handle", runID)
}

// quarentenaDoRun constrói a quarentena de um run: um handle por `artifact.created` do stream,
// PELA ORDEM DOS EVENTOS. Os factos de nível-esquema de cada handle são os que o evento sela —
// tipo, bytes e nome, afirmados pelo nó no momento da gravação —, e `WellFormed` é verdadeiro
// porque o armazém não grava conteúdo que não tenha passado o validador do tipo
// ([validarConteudo]).
//
// É a função ÚNICA de derivação dos ids: ao vivo e em reconstrução sai daqui, pelo que não há
// dois caminhos que possam divergir.
func (p *planoDeHandles) quarentenaDoRun(ctx context.Context, subject, runID string) (*agentruntime.Quarantine, error) {
	q, err := agentruntime.NewQuarantine(quarentenaDoArmazem{plano: p}, subject, runID)
	if err != nil {
		return nil, err
	}
	declarados, err := p.declarados(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, a := range declarados {
		// Um evento ilegível já ficou de fora no [artefactosDoRun]; um digest malformado fica de
		// fora aqui. Em qualquer dos casos NÃO se consome um id: os ids são função dos eventos
		// que declaram um artefacto resolúvel, e a mesma função corre em todos os caminhos.
		if _, err := q.Adopt(a.Digest, taint.OriginToolResult, agentruntime.HandleFacts{
			MediaType: a.MediaType, Bytes: int64(a.Bytes), Name: a.Name, WellFormed: true,
		}); err != nil {
			continue
		}
	}
	return q, nil
}

// handleDoArtefacto devolve a forma textual do handle do artefacto que o run acabou de gravar —
// o ÚLTIMO handle com este digest, que é o do evento mais recente (o mesmo conteúdo gravado
// duas vezes no run tem dois eventos e dois handles, e o que se devolve é o desta gravação).
// Vazio quando o evento ainda não está legível no stream: o nó não inventa um handle.
func (p *planoDeHandles) handleDoArtefacto(ctx context.Context, subject, runID, digest string) (string, string) {
	q, err := p.quarentenaDoRun(ctx, subject, runID)
	if err != nil {
		return "", handleCausaIlegivel
	}
	var ref string
	for _, h := range q.Handles() {
		if h.Digest() == digest {
			ref = h.Ref()
		}
	}
	if ref == "" {
		return "", handleCausaSemEvento
	}
	p.cunhados.Add(1)
	return ref, ""
}

// resolverArgumento troca uma referência a handle pelos bytes que ela nomeia. É o ponto de
// resolução A JUSANTE DA DECISÃO DO RM: só se chama de dentro do executor de uma tool, que o
// RM invoca apenas em `permit`.
//
// A REFERÊNCIA É O VALOR INTEIRO DO ARGUMENTO, nunca um pedaço dele. Um valor que a contenha
// no meio de outro texto NÃO é uma referência — é texto. A regra fecha duas coisas de uma vez:
// não há como enxertar bytes de um handle dentro de um documento forjado pelo modelo, e o que
// o RM viu no argumento é, byte a byte, ou a referência ou o conteúdo.
//
// Devolve (bytes, usou, causa): `usou` falso e causa vazia significa que o valor não é uma
// referência e segue tal-qual — o caminho de todos os runs que não usam handles.
func (p *planoDeHandles) resolverArgumento(ctx context.Context, subject, runID, valor string) ([]byte, bool, string) {
	id, ok := agentruntime.ParseHandleRef(valor)
	if !ok {
		return nil, false, ""
	}
	if runID == "" || subject == "" {
		return nil, true, p.recusar(handleCausaRun)
	}
	q, err := p.quarentenaDoRun(ctx, subject, runID)
	if err != nil {
		return nil, true, p.recusar(handleCausaIlegivel)
	}
	h, achou := q.Lookup(id)
	if !achou {
		return nil, true, p.recusar(handleCausaDesconhecido)
	}
	v, err := q.Resolve(ctx, h)
	switch {
	case errors.Is(err, agentruntime.ErrHandleBodyTooLarge):
		return nil, true, p.recusar(handleCausaTecto)
	case errors.Is(err, agentruntime.ErrHandleUnknown):
		return nil, true, p.recusar(handleCausaDesconhecido)
	case err != nil:
		return nil, true, p.recusar(handleCausaIlegivel)
	}
	p.resolvidos.Add(1)
	return v.Payload(), true, ""
}

// recusar conta a recusa e devolve a causa, para o chamador a pôr no erro da tool.
func (p *planoDeHandles) recusar(causa string) string {
	p.recusados.Add(1)
	return causa
}

// handlesBanner é a linha de arranque. Sem plano não sai linha nenhuma.
func handlesBanner(p *planoDeHandles) []string {
	if p == nil {
		return nil
	}
	return []string{fmt.Sprintf("entradas por handle (AOS-550, ADR-043 §2.6): AOS_HANDLE_NOS_ARGUMENTOS=on — a tool %s devolve, com a referencia do artefacto, o handle %s<id> que lhe corresponde, e aceita como `content` EXACTAMENTE um handle: o modelo emite a referencia, o REFERENCE MONITOR DECIDE COM ELA, e so depois o no a troca pelos bytes, lidos do armazem por (titular, run, digest). Os bytes nao entram no tail, no prompt_hash, nos spans, nos selos nem nos erros. Handle forjado: recusado por duas condicoes independentes — so um artifact.created selado pelo no cunha um handle, e o par (run, digest) tem de constar do stream do run. Ids sequenciais pela ordem dos eventos (sem relogio, sem rand), funcao unica em todos os caminhos. Tecto do corpo: %d bytes (%s). Entre runs nao resolve. Recuo: off",
		artifactWriteToolName, agentruntime.HandleRefPrefix, agentruntime.MaxToolResultBytes, handleCausaTecto)}
}
