package main

// ENTRADAS POR HANDLE: A RESOLUÇÃO NO `CallRewriter` (AOS-550, fase R2 do ADR-043).
//
// Um argumento de uma tool pode ser uma REFERÊNCIA a conteúdo em quarentena
// ([agentruntime.HandleRefPrefix], `aos-handle/v1:h1.<16 hex>`) em vez dos bytes. O modelo emite a
// referência; o nó troca-a pelos bytes **no `CallRewriter`, ANTES da mediação**; e é essa Call — a
// que já tem os bytes — que o Reference Monitor vê, decide, audita e sela.
//
// # PORQUE ANTES DA MEDIAÇÃO, E NÃO DEPOIS
//
// `loop.go:1207-1216` é o registo de um defeito OBSERVADO AO VIVO em produção: com a reescrita a
// correr no dispatcher, «o loop descrevia ao mundo o efeito PRÉ-reescrita e o RM mediava o
// PÓS-reescrita: as duas previews divergiam e a aprovação humana, embora emitida e consumida, nunca
// casava com a acção». A troca handle→bytes É uma reescrita do efeito. A jusante do RM, repunha-o:
//
//   - a pessoa do AOS-541 aprovaria `{"body":"h3…"}` — a `ApprovalPreview` leva `sha256(c.Input)` —
//     e o nó publicaria os bytes;
//   - o PDP decidiria e o WORM selaria um recurso que nomeia o token, quando o
//     [resolveResourceValue] existe precisamente para que o PDP decida, e o WORM sele, sobre o que
//     a execução vai mesmo tocar;
//   - o SAROC-04 passaria sem fazer o seu trabalho: só recusa um `Resource.Value` VAZIO, e um valor
//     que nomeia o token não é vazio nem diz nada;
//   - o `AttrToolCallHash` do span colidiria entre runs que publicassem cada um o seu `h1`.
//
// # O QUE CONTINUA A NÃO VER OS BYTES
//
// O corpo do segmento `tool_call` do tail é `inv.Input`, os argumentos TAL COMO O MODELO OS EMITIU,
// «antes de qualquer reescrita do efeito» (`layout.go`): leva o token. A captura do turno guarda a
// resposta do modelo: leva o token. O selo de mediação não leva o input em campo nenhum, e o span
// leva um hash. Logo os bytes resolvidos entram na Call e param aí — e é exactamente onde têm de
// entrar para que o que se aprova, audita e sela descreva o efeito executado.
//
// # ONDE SE PROCURA UM HANDLE, FIXADO
//
// SÓ nos ESCALARES DE TOPO do documento JSON dos argumentos, como o [resolveResourceValue] faz com
// os slots `{arg}`. E com dois guardas que fecham a divergência que a revisão nomeou:
//
//  1. uma referência em QUALQUER outro sítio do documento — aninhada, dentro de um array, numa
//     chave — RECUSA a call. Não se resolve (o slot do recurso veria a estrutura crua, e o recurso
//     auditado divergiria do recurso tocado) e não se deixa passar como literal;
//  2. um argumento que PREENCHE um slot do `resource_value` da tool não pode ser uma referência:
//     o recurso é calculado dos argumentos do modelo, antes da reescrita, pelo que resolver ali
//     faria o selo nomear o token e a execução tocar outra coisa. Recusa.
//
// # UM TOKEN QUE NÃO RESOLVE RECUSA A CALL
//
// Fail-closed e sem meio-termo: nunca se passa o literal adiante, e nunca se trata um token com
// forma de handle como argumento trusted. O `CallRewriter` devolve erro, e o loop materializa isso
// como um Deny (`E_EFFECT_REWRITE`) sem efeito nenhum — a mesma mecânica de uma tool call
// malformada.
//
// # O QUE FECHA UM HANDLE FORJADO, DITO COM PRECISÃO
//
// O ticket pede «duas condições independentes», a segunda das quais a regra do `media.go:26-32`.
// O que esta implementação tem, e o que não tem — as duas revisões adversariais do AOS-550
// mediram-no, e vale mais escrito do que prometido:
//
//	(1) QUEM PODE CRIAR um handle é só código do nó. Um token entra na quarentena de um run
//	    exclusivamente a partir de um `handle.created` que o espaço de handles do nó selou, com o
//	    emissor dele ([handleNHI]); o modelo não tem tool que crie handles, e um evento de outro
//	    emissor não cunha nada. Logo o modelo não consegue fazer existir uma referência para
//	    conteúdo que o nó não produziu.
//	(2) O QUE SE RESOLVE tem de constar do stream do run: o par `(run, digest)` vem de um
//	    `handle.created`, conferido na derivação dos tokens e à cabeça de cada leitura.
//
// AS DUAS SAEM DA MESMA FONTE DURÁVEL — os eventos do run —, conferida em dois pontos. É defesa em
// profundidade, NÃO independência, e o banner não diz o contrário.
//
// O QUE NÃO EXISTE AQUI, e é a diferença para o `media.go`: lá, a fronteira é o NOME DA TOOL cujo
// resultado trouxe a referência, porque o adaptador vê de onde o valor veio. O `CallRewriter` só vê
// a `Call`: um token legítimo resolve qualquer que seja a ORIGEM DO TEXTO que o nomeia — o modelo
// tê-lo escolhido, ou um documento untrusted tê-lo ditado e o modelo copiado. Na R2 isso vale
// pouco (o único produtor sela conteúdo que o próprio modelo escreveu); no layout 1.8.0, com cada
// `tool_result` por handle, um documento injectado pode nomear o handle de algo lido antes. Quem
// governa esse fluxo é a POLÍTICA DE FLUXO DE DADOS do AOS-551, por (capability, destino,
// proveniência), e não este guarda.
//
// E NENHUMA DAS DUAS DECIDE O TAINT: a `artifact_write` não é privilegiada mesmo com o contexto
// untrusted (ADR-034) e emite evento legítimo, pelo que um run injectado cunha um handle VÁLIDO
// sobre conteúdo que escolheu. Um handle é sempre untrusted ([quarantineOrigin] nunca promove).
//
// # O CUSTO DO GUARDA, MEDIDO
//
// O ticket pedia para medir o custo de levantar o guarda ao runtime, com o receio de que trouxesse
// consigo o selo da leitura no WORM (a via HTTP chega aos eventos por
// [apiHandler.portasDaLeituraDeConteudo], que o tem como pré-condição). NÃO traz: o que se levanta
// é o [handlesDoRun], a projecção dos eventos, lida com um `EventStore.Read` directo do stream do
// run. Custo: UMA leitura do stream por CALL que traga referências (a quarentena constrói-se uma
// vez e as declarações que ela leu seguem para o adaptador), mais uma por selagem; zero escritas no
// WORM, e sem as portas da leitura soberana.
//
// O QUE ISSO CUSTA EM TRILHO, DECLARADO: a mesma classe de conteúdo lida por HTTP exige a
// capability `read:artifact` e um selo da leitura no WORM; por esta via não há capability própria
// nem facto selado por leitura — só o contador agregado `aos_handle_total{resultado="resolvido"}`.
// Não existe, portanto, trilho de quem leu o quê e quando. E a decisão de abrir é tomada ANTES de
// o RM decidir a call: quem a provoca é o modelo, pela escolha dos argumentos, pelo que o conteúdo
// de um handle pode ser decifrado numa call que o RM vai negar a seguir.
//
// # CANAIS QUE FICAM ABERTOS, DECLARADOS
//
//   - DISPONIBILIDADE PELO TEXTO DE TERCEIROS. Um documento untrusted que contenha o prefixo de uma
//     referência, copiado pelo modelo para um argumento, faz a call ser recusada (`handle_parcial`).
//     É fail-closed, e é um canal de negação de serviço que quem escreveu o documento controla.
//     Preferimos recusar a deixar passar o literal.
//   - CONTEÚDO DECIFRADO NUMA CALL RECUSADA. A resolução corre antes da mediação, e uma call com
//     duas referências pode ter a primeira resolvida e ser recusada pela segunda. O conteúdo foi
//     lido do armazém e descartado; nenhum byte sai, mas a decifra aconteceu.
//   - A CAUSA DA RECUSA NÃO SOBREVIVE NO RASTO DURÁVEL. O loop materializa a recusa como
//     `E_EFFECT_REWRITE` + `denied_by=effect_rewriter` e DESCARTA a mensagem (é o que impede a
//     causa de ser um canal para o modelo). No WAL, uma recusa por `handle_no_slot_do_recurso` é
//     indistinguível de argumentos malformados; a causa vive no `/metrics` do processo.
//
// # A RETOMA PRECISA DO ESPAÇO E DO STREAM
//
// A reescrita corre em TODAS as construções de Call, também numa retoma ou numa call que o
// dispatcher durável vai deduplicar do step-ledger. Logo, uma retoma de um run que use handles
// precisa da pasta do espaço e do stream do run: ambos são duráveis (o volume e o Event Store),
// pelo que uma retoma no MESMO nó resolve. Noutro nó, sem a pasta, a reescrita falha e a call é
// recusada — fail-closed, e é a mesma propriedade que a `media_view` já declara («uma RETOMA ao
// vivo precisa dos bytes»).
//
// # A LINHAGEM (AOS-552)
//
// A reescrita é o único sítio do nó que sabe de que handles uma tool call se alimentou. Anota o par
// `(argumento, digest)` no [bilheteDaCall] da construção desta Call, e quem produz o efeito sela o
// que lhe interessa no evento de proveniência dele. Ver [bilheteDaCall] para o que isto substituiu
// e porque é que a versão anterior — um mapa partilhado — perdia linhagens sob carga.
//
// # ENTRE RUNS, NÃO
//
// A quarentena é a de UM run. Um token de outro run não resolve, e o cifrado de outro run não abre:
// o run vai nos dados associados da selagem. A travessia da aresta do plano é o AOS-553, e é pela
// declaração selada do plano (ADR-038) — nunca por um digest que o modelo forneça.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/substrate/eventstore"
)

// Causas de recusa da resolução. Vocabulário FECHADO: são também os rótulos da métrica, pelo que
// nenhuma leva um byte do conteúdo, um token nem um digest.
const (
	handleCausaDesconhecido = "handle_nao_reconhecido"
	handleCausaSemEvento    = "handle_sem_proveniencia"
	handleCausaNaoTextual   = "handle_nao_textual"
	handleCausaAninhado     = "handle_aninhado"
	handleCausaParcial      = "handle_parcial"
	handleCausaSlot         = "handle_no_slot_do_recurso"
	handleCausaArgs         = "argumentos_ilegiveis"
	handleCausaDuplicadas   = "argumentos_com_chave_repetida"
	handleCausaDemasiadas   = "referencias_demasiadas_na_call"
	handleCausaBytesDaCall  = "bytes_resolvidos_acima_do_tecto_da_call"
	handleCausaSemRun       = "handle_sem_run"
	handleCausaIlegivel     = "handle_ilegivel"
)

// Tectos de UMA call. Nasceram de um achado medido na revisão de segurança do AOS-550: 8,5 KB de
// saída do modelo — um handle de 200 KiB referenciado em 200 chaves de topo — produziam 40,9 MB de
// `Call.Input`, com 200 leituras do stream e 200 decifras, ANTES de o Reference Monitor negar a
// call. A memória já tinha sido gasta, e dois ou três runs em paralelo levavam o processo a OOM.
//
// O caso de uso precisa de uma referência, ou duas. O tecto é generoso e finito.
const (
	// handleMaxRefsPorCall é o nº de referências que uma call pode levar.
	handleMaxRefsPorCall = 4
	// handleMaxBytesPorCall é o total de bytes resolvidos numa call. É o tecto do corpo de um
	// `tool_result`: o que uma call recebe por handle não passa do que um resultado pode ter.
	handleMaxBytesPorCall = agentruntime.MaxToolResultBytes
)

// handleResolveTimeout é o prazo de UMA resolução (a leitura do stream, a abertura do ficheiro e a
// decifra).
//
// EXISTE PORQUE O `CallRewriter` NÃO RECEBE CONTEXTO. A porta é
// `func(referencemonitor.Call) (referencemonitor.Call, error)`: o ctx do run não chega aqui, e
// alargá-la mudava o kernel, o composition root e todas as reescritas do nó — fora do âmbito deste
// ticket. Sem prazo, a decifra por um Vault remoto que não respondesse pendurava a tool call para
// sempre; com prazo, falha fechada e a call é recusada. O cancelamento do run continua a não se
// propagar para dentro de uma resolução em curso, e isso fica declarado.
const handleResolveTimeout = 10 * time.Second

var (
	// ErrHandleSemEventStore — o espaço de handles ligado sem Event Store: sem ele não há
	// `handle.created` para ler, e o guarda (b) não se consegue verificar.
	ErrHandleSemEventStore = errors.New("aos: AOS_HANDLE_DIR exige o Event Store do no: o par (run, digest) de um handle tem de constar de um handle.created do stream do run")
)

// planoDeHandles é o plano de handles do nó ligado: o espaço por baixo e os slots do
// `resource_value` de cada tool, que dizem onde uma referência é proibida.
type planoDeHandles struct {
	armazem *armazemDeHandles
	// slots é toolID → nomes dos argumentos que preenchem o `resource_value` da tool. Um deles
	// com uma referência recusa a call.
	slots map[string]map[string]bool
}

// comporPlanoDeHandles compõe o plano sobre um espaço de handles já aberto. Espaço nil ⇒ nil, e o
// nó é o de sempre. Fail-closed: espaço sem Event Store aborta o arranque.
func comporPlanoDeHandles(armazem *armazemDeHandles, es eventstore.EventStore, specs []modelToolSpec) (*planoDeHandles, error) {
	if armazem == nil {
		return nil, nil
	}
	if es == nil {
		return nil, ErrHandleSemEventStore
	}
	armazem.registar = registoDeHandle(es)
	armazem.declarados = declaracoesDeHandles(es, armazem)
	return &planoDeHandles{armazem: armazem, slots: slotsPorTool(specs)}, nil
}

// slotsPorTool extrai, de cada tool do registo, os nomes dos argumentos que preenchem o
// `resource_value` — os slots `{arg}` que o [resolveResourceValue] lê dos argumentos DO MODELO.
// Uma tool cujo template não parseie fica sem slots: o arranque já falhou antes por isso
// ([validateResourceSlots]), e aqui a ausência é a postura segura — nenhum argumento é
// resolvível... o que não se aplica, porque sem slots não há divergência possível.
func slotsPorTool(specs []modelToolSpec) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, s := range specs {
		nomes, err := resourceSlots(s.ResourceValue)
		if err != nil || len(nomes) == 0 {
			continue
		}
		m := make(map[string]bool, len(nomes))
		for _, n := range nomes {
			m[n] = true
		}
		out[s.Name] = m
	}
	return out
}

// declaracoesDeHandles lê o stream do run e devolve os handles que os seus eventos declaram, pela
// ordem — o guarda (b), disponível ao runtime por uma leitura directa do stream. Ver «O CUSTO DO
// GUARDA (b)» no cabeçalho.
func declaracoesDeHandles(es eventstore.EventStore, armazem *armazemDeHandles) func(context.Context, string) ([]handleCriado, error) {
	return func(ctx context.Context, runID string) ([]handleCriado, error) {
		if runID == "" {
			return nil, errors.New("aos: handles declarados sem run_id")
		}
		events, err := es.Read(ctx, runID, 1)
		if err != nil {
			if errors.Is(err, eventstore.ErrStreamNotFound) {
				return nil, nil
			}
			return nil, err
		}
		declarados, ignorados := handlesDoRun(events)
		if ignorados > 0 {
			// A sequência do run ficou truncada: deixa rasto num contador em vez de o operador
			// ver só `handle_nao_reconhecido` sem causa (achado da revisão do AOS-550).
			armazem.conta(hCausaSequencia)
		}
		return declarados, nil
	}
}

// quarentenaDoArmazem é o [agentruntime.QuarantineStore] do nó: o espaço de handles, com o guarda
// (b) à cabeça de cada leitura.
type quarentenaDoArmazem struct {
	plano *planoDeHandles
	// tool e step são os da tool call que está a selar, e entram na proveniência e no nome que o
	// nó cunha. Vazios ⇒ a selagem recusa (um handle sem passo não é rastreável).
	tool, step string
	// declarados são os `handle.created` do run JÁ LIDOS por quem construiu esta quarentena. O
	// guarda (b) continua a ser imposto nos dois pontos — na derivação dos tokens e aqui, à cabeça
	// da leitura —, mas sobre a MESMA leitura do stream em vez de uma por resolução: a segunda
	// leitura não acrescentava independência (a fonte é a mesma) e custava uma varredura do stream
	// por handle resolvido (achados M-1 e A-1 da revisão de segurança). nil ⇒ lê do stream.
	declarados []handleCriado
}

// SealQuarantined sela o conteúdo no espaço de handles e devolve o token cunhado pelo nó e o
// digest. A causa da recusa vai no erro, do vocabulário fechado do espaço.
func (s quarentenaDoArmazem) SealQuarantined(ctx context.Context, subject, runID string, content []byte, facts agentruntime.HandleFacts) (string, string, agentruntime.HandleFacts, error) {
	if s.tool == "" || s.step == "" {
		return "", "", agentruntime.HandleFacts{}, errors.New("aos: selar um handle exige a tool e o passo da tool call")
	}
	p, causa := s.plano.armazem.selar(ctx, subject, runID, s.step, s.tool, facts.MediaType, content)
	if causa != "" {
		return "", "", agentruntime.HandleFacts{}, errors.New("aos: " + causa)
	}
	// Os factos que VOLTAM são os que o nó cunhou e selou no evento — os mesmos que a reconstrução
	// a partir do stream dá. Ver [agentruntime.QuarantineStore].
	return p.Token, p.Digest, factosDoHandle(p), nil
}

// factosDoHandle projecta o evento de proveniência nos factos de nível-esquema do handle. É a
// ÚNICA projecção: usam-na a selagem (ao vivo) e a reconstrução (de uma retoma ou de um replay),
// pelo que os dois caminhos não podem divergir.
func factosDoHandle(p handleCriado) agentruntime.HandleFacts {
	return agentruntime.HandleFacts{MediaType: p.MediaType, Bytes: int64(p.Bytes), Name: p.Name, WellFormed: p.WellFormed}
}

// OpenQuarantined lê e decifra o conteúdo que `(titular, run, digest)` nomeia, DEPOIS de confirmar
// que um `handle.created` do stream do run o declara com esse titular. Sem o evento não há bytes: é
// o guarda (b), no ponto onde os bytes sairiam.
func (s quarentenaDoArmazem) OpenQuarantined(ctx context.Context, subject, runID, digest string) ([]byte, error) {
	declarados := s.declarados
	if declarados == nil {
		var err error
		if declarados, err = s.plano.armazem.declarados(ctx, runID); err != nil {
			return nil, err
		}
	}
	for _, h := range declarados {
		if h.Digest == digest && h.SealedSubject == subject {
			return s.plano.armazem.abrir(ctx, subject, runID, digest)
		}
	}
	return nil, fmt.Errorf("aos: o run %s nao declara o conteudo deste handle", runID)
}

// quarentenaDoRun constrói a quarentena de um run: um handle por `handle.created` do stream, PELA
// ORDEM DOS EVENTOS, com os factos que o nó cunhou e selou no evento.
//
// É a função ÚNICA de derivação dos TOKENS: a SELAGEM parte daqui (e é por isso que o ordinal do
// token novo é o da posição seguinte), e a RECONSTRUÇÃO — uma retoma, um replay — também. Os FACTOS
// têm a sua própria projecção única ([factosDoHandle]), usada pelos dois caminhos.
//
// `tool` e `step` só são precisos para SELAR; um caminho que apenas resolve passa-os vazios.
func (p *planoDeHandles) quarentenaDoRun(ctx context.Context, subject, runID, tool, step string) (*agentruntime.Quarantine, error) {
	declarados, err := p.armazem.declarados(ctx, runID)
	if err != nil {
		return nil, err
	}
	// A quarentena fica com as declarações que acabámos de ler: o guarda (b) continua imposto à
	// cabeça de cada leitura, sem uma varredura do stream por resolução.
	q, err := agentruntime.NewQuarantine(quarentenaDoArmazem{plano: p, tool: tool, step: step, declarados: declarados}, subject, runID)
	if err != nil {
		return nil, err
	}
	for _, h := range declarados {
		if _, err := q.Adopt(h.Token, h.Digest, taint.OriginToolResult, factosDoHandle(h)); err != nil {
			// Fail-closed: um evento que não adopta deixa a quarentena deste run incompleta, e os
			// tokens seguintes seriam os de outra posição. Nenhuma resolução passa.
			return nil, err
		}
	}
	return q, nil
}

// selarEmQuarentena sela conteúdo no espaço de handles pela quarentena do run e devolve a forma
// TEXTUAL do handle — o que o nó mostra ao modelo. É o caminho que uma tool de código do nó usa
// depois de produzir conteúdo.
func (p *planoDeHandles) selarEmQuarentena(ctx context.Context, subject, runID, stepID, tool, tipo string, conteudo []byte) (string, string) {
	h, causa := p.selarEmQuarentenaComHandle(ctx, subject, runID, stepID, tool, tipo, conteudo)
	if causa != "" {
		return "", causa
	}
	return h.Ref(), ""
}

// selarEmQuarentenaComHandle é [planoDeHandles.selarEmQuarentena] a devolver o [Handle] inteiro —
// com os FACTOS que o nó cunhou, que são os mesmos que a reconstrução a partir do stream dá (ver
// [factosDoHandle]).
func (p *planoDeHandles) selarEmQuarentenaComHandle(ctx context.Context, subject, runID, stepID, tool, tipo string, conteudo []byte) (agentruntime.Handle, string) {
	q, err := p.quarentenaDoRun(ctx, subject, runID, tool, stepID)
	if err != nil {
		return agentruntime.Handle{}, p.armazem.conta(handleCausaSemRun)
	}
	h, err := q.Seal(ctx, taint.FromOrigin(taint.OriginToolResult, conteudo), agentruntime.HandleFacts{MediaType: tipo})
	if err != nil {
		// A causa da selagem já foi contada pelo espaço; a do tecto do kernel conta-se aqui.
		if errors.Is(err, agentruntime.ErrHandleBodyTooLarge) {
			return agentruntime.Handle{}, p.armazem.conta(hCausaGrande)
		}
		if causa, achou := strings.CutPrefix(err.Error(), "aos: "); achou && p.armazem.contadores[causa] != nil {
			return agentruntime.Handle{}, causa
		}
		return agentruntime.Handle{}, p.armazem.conta(handleCausaIlegivel)
	}
	return h, ""
}

// reescritaDeHandles é o [referencemonitor.Call] rewriter que troca as referências pelos bytes,
// ANTES da mediação. Vale para TODAS as tools: a referência é um argumento, não uma tool.
//
// O CAMINHO DE SEMPRE É BYTE-IDÊNTICO: sem o prefixo de uma referência nos argumentos, a Call sai
// tal como entrou, sem ser parseada nem re-serializada.
func reescritaDeHandles(p *planoDeHandles) reescritaDaCall {
	if p == nil {
		return nil
	}
	return func(call referencemonitor.Call, bilhete *bilheteDaCall) (referencemonitor.Call, error) {
		// OS ARGUMENTOS LÊEM-SE DESCODIFICADOS, SEMPRE QUE SÃO UM OBJECTO JSON. Decidir pela forma
		// serializada era um diferencial de parser medido na revisão de segurança (achado M-2):
		// `aos-handle\/v1:` é um escape JSON legal, não contém o prefixo em bytes, e fazia os
		// guardas abaixo cegos — uma call saía PARCIALMENTE resolvida, com uma referência aninhada
		// preservada como literal, que é exactamente o estado que este ficheiro declara impossível.
		var args map[string]json.RawMessage
		dec := json.NewDecoder(bytes.NewReader(call.Input))
		if err := dec.Decode(&args); err != nil || args == nil {
			// Não é um objecto JSON. Nenhuma referência se resolve aqui; se os bytes trouxerem a
			// forma de uma, a call é recusada em vez de a deixar passar como literal.
			if bytes.Contains(call.Input, []byte(agentruntime.HandleRefPrefix)) {
				return referencemonitor.Call{}, p.recusar(handleCausaArgs)
			}
			return call, nil
		}
		if _, err := dec.Token(); err != io.EOF {
			return referencemonitor.Call{}, p.recusar(handleCausaArgs)
		}
		// CHAVE REPETIDA É RECUSA. O `json.Unmarshal` para mapa fica com o ÚLTIMO valor: um
		// `{"a":"<ref>","a":"benigno"}` passava com os bytes originais e a referência literal lá
		// dentro (achado M-2). Contar as chaves do documento e comparar com o mapa apanha-o.
		if n, err := chavesDeTopo(call.Input); err != nil || n != len(args) {
			return referencemonitor.Call{}, p.recusar(handleCausaDuplicadas)
		}
		subject := call.Principal.Titular()
		if call.RunID == "" || subject == "" {
			if referenciaEmAlgumSitio(args) {
				return referencemonitor.Call{}, p.recusar(handleCausaSemRun)
			}
			return call, nil
		}
		// ORDEM FIXA dos argumentos: a iteração de um mapa é aleatória, e com duas referências
		// más a causa da recusa dependeria do sorteio. A mesma Call tem de dar sempre a mesma
		// recusa.
		nomes := make([]string, 0, len(args))
		for nome := range args {
			nomes = append(nomes, nome)
		}
		sort.Strings(nomes)
		// PRIMEIRA PASSAGEM — onde uma referência NÃO pode estar. Corre inteira antes de se
		// resolver o que quer que seja: uma call com uma referência fora de um escalar de topo é
		// recusada, não parcialmente resolvida.
		var aResolver []string
		for _, nome := range nomes {
			// A CHAVE também é lugar nenhum: o nome de um argumento não é um argumento.
			if strings.Contains(nome, agentruntime.HandleRefPrefix) {
				return referencemonitor.Call{}, p.recusar(handleCausaAninhado)
			}
			var valor any
			if err := json.Unmarshal(args[nome], &valor); err != nil {
				return referencemonitor.Call{}, p.recusar(handleCausaArgs)
			}
			texto, escalar := valor.(string)
			if !escalar {
				if referenciaEmAlgumSitio(valor) {
					return referencemonitor.Call{}, p.recusar(handleCausaAninhado)
				}
				continue
			}
			if _, ok := agentruntime.ParseHandleRef(texto); ok {
				aResolver = append(aResolver, nome)
				continue
			}
			if strings.Contains(texto, agentruntime.HandleRefPrefix) {
				return referencemonitor.Call{}, p.recusar(handleCausaParcial)
			}
		}
		if len(aResolver) == 0 {
			return call, nil
		}
		// O TECTO DO FAN-OUT, antes de se ler um byte do armazém.
		if len(aResolver) > handleMaxRefsPorCall {
			return referencemonitor.Call{}, p.recusar(handleCausaDemasiadas)
		}
		ctx, cancel := context.WithTimeout(context.Background(), handleResolveTimeout)
		defer cancel()
		var q *agentruntime.Quarantine
		// vistos memoiza por token: o mesmo handle em duas chaves resolve-se UMA vez.
		vistos := map[string][]byte{}
		// vistosDigest guarda o digest por token, para o anotar no bilhete também quando a
		// resolução foi memoizada (AOS-552).
		vistosDigest := map[string]string{}
		total := 0
		// SEGUNDA PASSAGEM — a resolução.
		for _, nome := range aResolver {
			var texto string
			if err := json.Unmarshal(args[nome], &texto); err != nil {
				return referencemonitor.Call{}, p.recusar(handleCausaArgs)
			}
			token, _ := agentruntime.ParseHandleRef(texto)
			// O argumento que preenche um slot do `resource_value` não pode ser uma referência.
			if p.slots[call.ToolID][nome] {
				return referencemonitor.Call{}, p.recusar(handleCausaSlot)
			}
			corpo, memoizado := vistos[token]
			if !memoizado {
				if q == nil {
					var err error
					if q, err = p.quarentenaDoRun(ctx, subject, call.RunID, "", ""); err != nil {
						return referencemonitor.Call{}, p.recusar(handleCausaIlegivel)
					}
				}
				h, achou := q.Lookup(token)
				if !achou {
					return referencemonitor.Call{}, p.recusar(handleCausaDesconhecido)
				}
				v, err := q.Resolve(ctx, h)
				switch {
				case errors.Is(err, agentruntime.ErrHandleBodyTooLarge):
					return referencemonitor.Call{}, p.recusar(hCausaGrande)
				case errors.Is(err, agentruntime.ErrHandleUnknown):
					return referencemonitor.Call{}, p.recusar(handleCausaDesconhecido)
				case err != nil:
					return referencemonitor.Call{}, p.recusar(handleCausaSemEvento)
				}
				corpo = v.Payload()
				// Um handle BINÁRIO não se resolve para uma string JSON: a serialização trocaria
				// os bytes inválidos por U+FFFD, e o que a tool receberia não seria o que o digest
				// nomeia — nem o que o RM vai autorizar. Fail-closed, e é o vocabulário aberto do
				// espaço de handles a encontrar aqui o seu limite.
				if !utf8.Valid(corpo) {
					return referencemonitor.Call{}, p.recusar(handleCausaNaoTextual)
				}
				vistos[token] = corpo
				vistosDigest[token] = h.Digest()
				total += len(corpo)
				if total > handleMaxBytesPorCall {
					return referencemonitor.Call{}, p.recusar(handleCausaBytesDaCall)
				}
			}
			claro, jerr := json.Marshal(string(corpo))
			if jerr != nil {
				return referencemonitor.Call{}, p.recusar(handleCausaNaoTextual)
			}
			args[nome] = claro
			// A LINHAGEM, POR ARGUMENTO (AOS-552). Quem sela a proveniência escolhe depois os
			// argumentos que TRANSPORTAM CONTEÚDO, e não todos: uma referência resolvida num
			// argumento que só nomeia o tipo de media não é uma derivação do conteúdo — a revisão
			// adversarial mediu exactamente isso, um evento a afirmar uma derivação inexistente.
			// O bilhete morre no fim desta cadeia: não há mapa, chave, tecto nem concorrência.
			bilhete.anotarDigest(nome, vistosDigest[token])
			// A AUTORIDADE conta à parte, e SEM filtro (AOS-551): o guarda de [anotarDigest] sobre
			// o nome vazio é legítimo para a linhagem e seria um buraco para a autoridade — o nome
			// do argumento é escolhido pelo modelo, e `{"": "<referência>"}` é JSON válido.
			bilhete.anotarResolvido()
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return referencemonitor.Call{}, p.recusar(handleCausaArgs)
		}
		// O CONTADOR CONTA O QUE CHEGOU À TOOL, e só agora. Somar dentro do ciclo contava a
		// primeira referência de uma call que a segunda vinha a fazer recusar: o contador afirmava
		// uma troca que nunca chegou a tool nenhuma (achado da revisão do AOS-550). O conteúdo
		// decifrado de uma call recusada é um resíduo declarado — ver o cabeçalho.
		for range aResolver {
			p.armazem.conta(hResolvido)
		}
		call.Input = raw
		return call, nil
	}
}

// chavesDeTopo conta as chaves do objecto JSON de topo, pela ordem em que aparecem — incluindo as
// repetidas, que o `json.Unmarshal` para um mapa funde ficando com a última.
func chavesDeTopo(b []byte) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return 0, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return 0, errors.New("aos: os argumentos nao sao um objecto JSON")
	}
	n := 0
	for dec.More() {
		if _, err := dec.Token(); err != nil { // a chave
			return 0, err
		}
		n++
		var descartar json.RawMessage
		if err := dec.Decode(&descartar); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// referenciaEmAlgumSitio diz se alguma string do valor DESCODIFICADO — a qualquer profundidade, em
// chave ou em valor — contém o prefixo de uma referência. É o guarda do «aninhado»: não se resolve
// ali, e não se deixa passar como literal.
func referenciaEmAlgumSitio(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, agentruntime.HandleRefPrefix)
	case []any:
		for _, e := range t {
			if referenciaEmAlgumSitio(e) {
				return true
			}
		}
	case map[string]any:
		for k, e := range t {
			if strings.Contains(k, agentruntime.HandleRefPrefix) || referenciaEmAlgumSitio(e) {
				return true
			}
		}
	case map[string]json.RawMessage:
		for k, raw := range t {
			if strings.Contains(k, agentruntime.HandleRefPrefix) {
				return true
			}
			var qualquer any
			if json.Unmarshal(raw, &qualquer) == nil && referenciaEmAlgumSitio(qualquer) {
				return true
			}
		}
	}
	return false
}

// recusar conta a recusa pela causa, no MESMO contador fechado da selagem, e devolve o erro que o
// loop materializa como Deny. A MENSAGEM não sai para o modelo (o loop guarda só efeito, código e
// `denied_by`), e não leva conteúdo.
func (p *planoDeHandles) recusar(causa string) error {
	return fmt.Errorf("aos: handle nos argumentos: %s", p.armazem.conta(causa))
}

// handlesBanner é a linha de arranque do plano. Sem plano não sai linha nenhuma.
func handlesBanner(p *planoDeHandles) []string {
	if p == nil {
		return nil
	}
	return []string{fmt.Sprintf("entradas por handle (AOS-550, ADR-043 §2.6): um argumento de tool pode ser uma referencia %s<token> e o no troca-a pelos bytes NO CallRewriter, ANTES da mediacao — a Call que o RM decide, audita e sela e a que ja tem os bytes (a preview da aprovacao e o hash do span descrevem o efeito executado; o resource_value e calculado dos argumentos do MODELO, antes da reescrita, e por isso uma referencia num argumento que preencha um slot dele RECUSA a call). Procura-se SO nos escalares de topo do documento DESCODIFICADO: uma referencia aninhada, parcial, numa chave, ou numa chave repetida, RECUSA a call; ate %d referencias e %d bytes resolvidos por call. Um token que nao resolve recusa a call (E_EFFECT_REWRITE), nunca passa como literal. Handle forjado: so codigo do no CRIA um handle (emissor %s) e so o que o stream do run declara se RESOLVE — duas conferencias sobre a MESMA fonte duravel, defesa em profundidade e nao independencia; o texto que NOMEIA um token nao tem proveniencia verificavel nesta porta, e quem governa esse fluxo e a politica de dados do AOS-556. NENHUM dos dois guardas decide o taint: um handle e sempre untrusted. Tokens cunhados pelo no, escopados ao run, ligados a posicao e NAO ESCRIVEIS por quem nao conhece os bytes (hash sem chave de run+digest+ordinal, nao um segredo), com a sequencia pela ordem dos eventos (sem relogio, sem rand). Nesta fase o unico produtor de handles e a tool %s, que valida o conteudo antes de selar; sem AOS_ARTIFACT_DIR nenhum handle e cunhado",
		agentruntime.HandleRefPrefix, handleMaxRefsPorCall, handleMaxBytesPorCall, handleNHI, artifactWriteToolName)}
}

// autoridadeBanner e a linha de arranque da AUTORIDADE POR ARGUMENTO (AOS-551). Sem plano de
// handles nao sai linha nenhuma: sem handles nao ha nada para resolver nem para levantar.
//
// DECLARA A POSTURA, NAO A IMPOE. Quem a impoe e o kernel, em dois sitios: o `Run` recusa um run
// num layout por handle sem a declaracao, e re-junta o piso depois da reescrita. Esta linha existe
// para o operador saber o que o no faz — e, desde que o portao vive no Run, deixou de ser a unica
// defesa.
func autoridadeBanner(p *planoDeHandles) []string {
	if p == nil {
		return nil
	}
	return []string{fmt.Sprintf("autoridade por argumento (AOS-551, ADR-043 §2.6, fase R2): a reescrita deste no calcula o taint de cada tool call a partir do que ela CONSOME, e nao do que o modelo viu — join(piso, untrusted se a call resolveu algum handle). O PISO vem do kernel e e o que isto nao pode levantar: um `plan_input` ou um `tool_result` so NAO contribuem quando levam o ROTULO do handle, que e o que diz que o corpo e o texto fixo — logo contribuem o `tool_result` da media_view (que leva a MediaRef inline, por desenho da R1a), o de um `tool_error` (texto livre de uma tool) e os de um layout que ponha so uma das metades por handle. Contribuem sempre a MEMORIA do run (que entra inline), a MEDIA projectada para o modelo (R1a), o `timestamp` (que o kernel nao escreve) e o `steer` — logo num run com memoria ou com media NADA se levanta. Uma call que nao consome handle nenhum, num run sem memoria nem media, fica TRUSTED mesmo depois de o run ter lido: e esse o limite que a fase R2 levanta. A GARANTIA NAO E ESTA LINHA: o kernel recusa arrancar um run em %s sem esta declaracao (%s) e RE-JUNTA o piso depois desta reescrita, pelo que um erro aqui so consegue SUBIR acima do piso, nunca descer. Esta reescrita nao muda nenhum run nos layouts anteriores a %s, onde o piso E a autoridade do contexto. A politica que decide SOBRE este rotulo e o AOS-556, e sem ela uma call que consome um handle untrusted continua negada pela regra binaria de hoje",
		agentruntime.AssemblyVersion180, "ErrArgumentAuthorityRequired", agentruntime.AssemblyVersion180)}
}
