package main

// travessia_da_aresta.go — A TRAVESSIA DA ARESTA DO PLANO POR HANDLE (AOS-553, ADR-045).
//
// # O DEFEITO
//
// Com o produtor do `tool_result` composto (AOS-551), um run num layout por handle sela o
// resultado de cada tool call permitida e o kernel designa a PRÓPRIA REFERÊNCIA como origem da
// saída: o digest da âncora é o `sha256` do token, porque é o token que o step-ledger grava. Os
// três passos de conferência do `aos-orq` conferem — os dois lados hasheiam a mesma referência —,
// pelo que o nó consumidor recebia `aos-handle/v1:…` como conteúdo do seu `plan_input` e nunca o
// podia abrir: a pasta do espaço de handles é por run, a cifra leva o run nos dados associados, e
// o `handle.created` que declara o par é do stream de QUEM PRODUZIU.
//
// Até ao AOS-553 isso fechava com a causa nomeada `origem_por_handle` (AOS-556, resíduo (a)). Esta
// é a travessia que a torna desnecessária.
//
// # A FORMA: A AUTORIDADE É O PEDIDO DE PLANO VERIFICADO, NUNCA A REFERÊNCIA DO CORPO
//
// O ADR-038 §3 rejeitou «o nó consumidor resolve a referência» por uma razão pré-nomeada: põe o
// nó a decifrar conteúdo de um titular por conta de uma referência escrita num corpo de pedido,
// dentro do caminho de admissão (*confused deputy*). A travessia existe porque o gatilho que esse
// mesmo parágrafo escreveu disparou — a reabertura do DEF-806 —, e a resposta ao perigo é a do
// ADR-035: **o corpo diz QUAL, o nó verifica SE**.
//
// Nenhum elo da cadeia abaixo vem do corpo sem ser confinado por um facto que o nó selou ou
// verificou:
//
//  1. o run é trabalho de um pedido de plano que o nó PROVOU no `POST /runs` ([origemDoPlano],
//     AOS-439/520: quem chama é o drenador com a reclamação viva daquele pedido). Sem essa prova
//     não há travessia — [tCausaSemPlano];
//  2. o run produtor que o corpo nomeia tem de ser um run DESTE plano e DESTE nó produtor:
//     `<pedido verificado>~<from>` ou a sua tentativa `~<n>` ([runDoNoProdutor]). O pedido vem do
//     que o nó provou; do corpo vêm só o `from`, que já era o rótulo de proveniência da entrada, e
//     o número da tentativa — [tCausaRunForaDoPlano];
//  3. a última transição desse run selou uma âncora VINCULATIVA e DESIGNADA ([state.Outcome],
//     ADR-038 §2.3) — [tCausaSemAncora];
//  4. e essa âncora designa EXACTAMENTE esta referência: `sha256(ref)` e o tamanho batem com o que
//     o kernel selou. É este o passo que faz a travessia ser «pela declaração selada»: o corpo não
//     consegue nomear um handle qualquer do run produtor, só o que o kernel designou como saída —
//     [tCausaNaoDesignada];
//  5. o par `(run, digest)` consta de um `handle.created` do stream do run produtor, e o token
//     RE-CUNHA-SE de `(run produtor, digest do evento, ordinal)` e tem de dar o mesmo
//     ([agentruntime.MintHandleToken]) — [tCausaSemDeclaracao];
//  6. o TITULAR vem do EVENTO (`sealed_subject`), nunca do pedido; e um titular diferente do deste
//     run é recusado EXPLICITAMENTE, antes de uma única decifra — [tCausaOutroTitular].
//
// O PASSO 6 DEPENDE DO PASSO 1, e isso é preciso dizer porque não se vê daqui. O titular DESTE run
// é `goal.Titular()`, e num run de plano ele é `goal.Subject`, que o `POST /runs` deriva do
// submissor lido do log da fila (`api.go`: `goal.Subject = requestedBy`, AOS-440) — não é um campo
// do corpo. Mas num run SEM vínculo verificado `requestedBy` é vazio e o titular cai para
// `Principal.NHIID`, que vem do corpo. É o passo 1 que impede esse caso de chegar ao passo 6: sem
// pedido de plano provado não há travessia, logo a comparação de titulares nunca é feita contra um
// valor que quem submete escolheu.
//
// O QUE A TRAVESSIA ACRESCENTA, E O QUE A CONFINA — seguido até ao fim, porque a primeira versão
// deste comentário afirmava o contrário e estava ERRADA.
//
// A travessia ACRESCENTA poder, e não se deve dizer que não. A afirmação que aqui estava — «o
// drenador já lê a saída designada de qualquer run do plano pelo `GET /runs/{id}`, logo isto não
// lhe dá nada» — é FALSA num run por handle: o que essa rota serve é a REFERÊNCIA, não os bytes
// (é esse o defeito que este ficheiro existe para fechar), e o conteúdo por trás do handle não é
// servido por rota nenhuma nem por tool nenhuma, por desenho. A travessia converte referência em
// texto em claro dentro de um run que o chamador compõe. Isso é novo.
//
// O que o confina não é «já o podia ler»; é QUATRO perguntas a que o chamador não consegue
// responder à sua escolha:
//
//   - QUAL conteúdo — a âncora selada pelo kernel fixa-o (passo 4): é a SAÍDA DESIGNADA desse run,
//     e não um handle qualquer dele. **E não mais do que isso:** a âncora não identifica a aresta
//     do DOCUMENTO aprovado — o nó não lê o documento (ADR-018) —, pelo que o que o passo 4 impõe
//     é «a saída designada», não «a saída que esta aresta do plano declara»;
//   - DE QUE RUN — o pedido de plano que o nó provou fixa a FORMA do id (passos 1 e 2):
//     `<pedido>~<X>`, ou a sua tentativa. **O `X` é o `from` da entrada, que vem do corpo e não é
//     validado contra o documento aprovado** — o nó não lê o documento (ADR-018), pelo que não
//     pode saber que nó produtor a aresta declara. A propriedade imposta é por isso a mais fraca:
//     «a saída designada de ALGUM run deste pedido». É a segunda correcção desta passagem, e a
//     razão de a primeira ter sido escrita duas vezes é o que a torna importante de dizer.
//
//     PORQUE É QUE ISSO BASTA, e não é um buraco: é EXACTAMENTE o alcance que o nó já concede a
//     este chamador. O `submissor_do_plano.go` declara-o em voz alta — «uma reclamação viva serve
//     para `<plano>~<qualquer-coisa>`, todos com o mesmo submissor» (ADR-035 §5) —, pelo que quem
//     detém a reclamação já CRIA qualquer run dessa forma. A travessia não alarga o conjunto de
//     runs que ele alcança; alarga o que ele lê de um run que já alcança, e o passo 4 limita-o à
//     saída que o kernel designou;
//   - DE QUEM — o evento fixa o titular (passo 6), e outro titular é recusado;
//   - EM QUE ESTADO — só um run que CONCLUIU (passo 3), que é a mesma condição com que o
//     `aos-orq` publica.
//
// Ou seja: o chamador obtém, em claro, exactamente o conteúdo que o plano que ele legitimamente
// conduz manda passar nesta aresta, e nada mais. É isso que se pesa contra o perigo que o
// ADR-038 §3 nomeou — não uma equivalência com o que já lhe é servido.
//
// E FICA DECLARADO O QUE ISTO NÃO TEM: a travessia não passa por capability de leitura
// (`read:artifact`) nem sela uma leitura sensível no WORM, ao contrário da via HTTP do armazém de
// artefactos. O confinamento é ESTRUTURAL — o plano, a âncora, o titular —, e não por política.
// Está no ADR-045 §5 como resíduo, e não como propriedade.
//
// # PORQUE NÃO PELO [quarentenaDoArmazem.OpenQuarantined]
//
// Porque o contrato dele é «o par `(run, digest)` mais o titular que o CHAMADOR afirma», e numa
// travessia entre runs o chamador é precisamente quem não pode afirmar o titular. Aqui o titular
// sai do evento e a conferência é mais forte: além do par, re-cunha-se o token. A abertura em si é
// o [armazemDeHandles.abrir], que confere as três coisas de sempre — a chave do titular, os dados
// associados do run produtor e o digest.
//
// # A DECISÃO DE DESENHO: RE-SELAR NA PASTA DO CONSUMIDOR
//
// O conteúdo aberto do run produtor é RE-SELADO no espaço do run consumidor, pela via que já
// existe ([selarEntradasDoPlano], que corre no [NodeService.hostRun] logo depois desta função).
// A alternativa — deixar o blob onde está e abrir a pasta do produtor — foi medida e recusada; a
// decisão e as suas consequências estão no **ADR-045 §2.2 e §3**. Em duas linhas: abrir a pasta do
// produtor obrigava a tornar a amarra do cifrado ao run um PARÂMETRO em vez de uma invariante (é a
// propriedade que o [ErrHandleOutroRun] existe para ter), e punha o replay do consumidor a
// depender do stream e do blob de outro run. O custo da escolha é o conteúdo duplicado em disco,
// limitado pelo tecto por handle, e as posições que a travessia gasta no tecto do run consumidor.
//
// # O QUE ESTA FUNÇÃO NÃO É
//
// Não é um caminho de leitura: não serve bytes a ninguém, não nasce rota HTTP e não nasce tool. A
// resolução é interna ao nó, por emenda do desenho da fase R2 — uma tool daria ao modelo um pedido
// por digest, que é o vector que o guarda do evento fecha.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// Resultado e causas da travessia. Entram no vocabulário FECHADO do espaço de handles
// ([handleResultados]), que é também o dos rótulos da métrica: nenhum valor leva um byte do
// conteúdo, um token, um digest nem um titular.
const (
	// tTravessiaFeita — uma entrada do plano que vinha por referência foi resolvida do armazém do
	// run produtor. O re-selo no run consumidor conta à parte, como `selado`.
	tTravessiaFeita = "travessia_feita"
	// tCausaSemEspaco — o nó não tem espaço de handles. Uma referência não se resolve, e não se
	// entrega o literal.
	//
	// É A ÚNICA CAUSA QUE NÃO PRODUZ AMOSTRA, e por duas razões que vale dizer em vez de deixar
	// quem lê o `/metrics` à espera dela. (1) É INALCANÇÁVEL PELA PORTA: um `inputs[].from_run` num
	// nó sem a travessia é recusado com 400 no `POST /runs` ([anuncioDaTravessiaDoNo], que exige o
	// espaço composto), pelo que uma aresta declarada só chega a um nó que tem espaço. O que esta
	// causa é, então, é o guarda fail-closed de uma composição em que a porta e a travessia
	// discordem — e o guarda do nil que a linha a seguir precisaria de qualquer modo. (2) Quando
	// ela dispara não há armazém, logo não há contador nem família `aos_handle_total` para a
	// publicar: ela vive no ERRO do run, e é lá que se lê.
	tCausaSemEspaco = "travessia_sem_espaco_de_handles"
	// tCausaSemPlano — o run não é trabalho de um pedido de plano VERIFICADO pelo nó. Sem essa
	// prova não há aresta do plano a atravessar.
	tCausaSemPlano = "travessia_sem_pedido_verificado"
	// tCausaRunForaDoPlano — o run produtor nomeado não é um run deste pedido e deste nó produtor.
	tCausaRunForaDoPlano = "travessia_run_fora_do_plano"
	// tCausaSemAncora — a última transição do run produtor não selou uma âncora vinculativa e
	// designada: por ela não se entrega nada (ADR-038 §2.4).
	tCausaSemAncora = "travessia_sem_ancora_vinculativa"
	// tCausaNaoDesignada — a âncora do run produtor não designa ESTA referência. É o passo que
	// impede o corpo de nomear um handle intermédio de um irmão do plano.
	tCausaNaoDesignada = "travessia_referencia_nao_designada"
	// tCausaSemDeclaracao — nenhum `handle.created` do stream do run produtor declara este token
	// com um digest que o re-cunhe.
	tCausaSemDeclaracao = "travessia_sem_declaracao_do_produtor"
	// tCausaOutroTitular — o `handle.created` declara outro titular. RECUSA EXPLÍCITA, antes de
	// qualquer decifra: a cifra por titular bloquearia a abertura de qualquer modo, e o que esta
	// causa acrescenta é que a recusa deixa de ser um erro de decifra.
	tCausaOutroTitular = "travessia_outro_titular"
	// tCausaIndisponivel — a abertura recusou: o ficheiro não está, os dados associados não batem,
	// o digest não bate, ou a custódia não respondeu.
	tCausaIndisponivel = "travessia_conteudo_indisponivel"
	// tCausaComposicao — as arestas que a admissão compôs não correspondem às entradas do Goal. É
	// um defeito de composição do nó, e falha fechado em vez de atravessar por um índice errado.
	// TEM DE SER ZERO: nenhuma via de submissão o produz, porque as duas listas saem do MESMO ciclo
	// de [validarPlanInputs]. Uma via que passe arestas sem as compor desta forma não existe — e se
	// vier a existir, é aqui que se vê.
	tCausaComposicao = "travessia_arestas_incoerentes"
	// tCausaSemReferencia — quem submete DECLAROU a aresta (`inputs[].from_run`) e o conteúdo da
	// entrada não é uma referência de handle. É contradição de quem chama, não ambiguidade: aceitar
	// e ignorar daria a quem submete a ideia de que a aresta foi atravessada quando nada houve a
	// atravessar.
	tCausaSemReferencia = "travessia_entrada_sem_referencia"
)

// causasDaTravessia é a lista fechada, pela ordem em que sai no `/metrics`.
func causasDaTravessia() []string {
	return []string{
		tTravessiaFeita, tCausaSemEspaco, tCausaSemPlano, tCausaRunForaDoPlano, tCausaSemAncora,
		tCausaNaoDesignada, tCausaSemDeclaracao, tCausaOutroTitular, tCausaIndisponivel,
		tCausaComposicao, tCausaSemReferencia,
	}
}

// errArestaNaoAtravessada — uma entrada do plano que vem por referência e não se resolveu. A
// hospedagem RECUSA: entregar o literal ao modelo era o lixo silencioso que este ticket fecha, e
// deixá-lo passar como conteúdo punha um token no `plan_input` de um run que o não abre.
var errArestaNaoAtravessada = errors.New("aos: a aresta do plano nao atravessou: uma entrada vem por referencia de handle e nao se resolveu do armazem do run produtor")

// leitorDoDesfecho é o que a travessia precisa do estado durável do nó: a âncora que a última
// transição do run produtor selou. É o [NodeService.DurableOutcome].
type leitorDoDesfecho interface {
	DurableOutcome(ctx context.Context, runID string) (state.State, state.Outcome, error)
}

// runDoNoProdutor diz se `candidato` é um run do nó `nodeID` do pedido `pedido` — a primeira
// hospedagem (`<pedido>~<nó>`) ou uma das suas tentativas (`<pedido>~<nó>~<n>`, com `n >= 2`).
//
// A FORMA COMPLETA, e não um prefixo: com o prefixo, `<pedido>~<nó>-outro` passava, e um `node_id`
// que o `aos-orq` escapa de forma injectiva deixaria de ser injectivo na conferência. O molde do
// id é o [idDoRunFilho] — a mesma função com que o nó já confere a origem declarada de um run
// filho —, e o da tentativa é o [idDaTentativa].
func runDoNoProdutor(pedido, nodeID, candidato string) bool {
	if pedido == "" || nodeID == "" || candidato == "" {
		return false
	}
	base := idDoRunFilho(pedido, nodeID)
	if candidato == base {
		return true
	}
	sufixo, achou := strings.CutPrefix(candidato, base+separadorDoRunFilho)
	if !achou {
		return false
	}
	// O número da tentativa, na forma que o nó já exige: um inteiro decimal sem zero à esquerda e
	// pelo menos 2 (a tentativa 1 é a base, e `idDaTentativa` não lhe põe sufixo).
	n, err := strconv.Atoi(sufixo)
	return err == nil && n >= 2 && strconv.Itoa(n) == sufixo
}

// atravessarArestasDoPlano resolve as entradas do plano que vêm por REFERÊNCIA de handle, lendo o
// conteúdo do armazém do run produtor sob as seis conferências do cabeçalho, e devolve o Goal com
// os bytes no lugar da referência — para o [selarEntradasDoPlano] os re-selar no run consumidor.
//
// FORA DESTE CASO NÃO TOCA EM NADA: um Goal sem nenhuma ARESTA DECLARADA sai TAL COMO ENTROU, sem
// uma leitura e sem uma alocação. É o que mantém o caminho de sempre byte-idêntico.
//
// E É O QUE A TORNA INERTE num nó que não habilite o layout por handle — mas a razão NÃO é «nenhuma
// referência chega»: um texto com forma de referência chega sem produtor nenhum, e foi assim que a
// revisão adversarial encontrou o defeito da porta (ver o bloco no corpo). A razão é que nenhum
// `from_run` chega: o `aos-orq` só o envia a um nó que anuncie a travessia, e a porta do
// `POST /runs` recusa com 400 um `from_run` que este nó não honra.
//
// `produtores` é o run produtor de cada entrada, pela MESMA ordem dos `Goal.Inputs`, tal como a
// admissão o compôs do corpo do pedido. Uma posição vazia é uma entrada SEM aresta declarada, que
// é o normal; todas vazias (ou a lista vazia) é um run que não vem de um plano por referência. Com
// alguma aresta declarada, um comprimento diferente do dos `Inputs` é defeito de composição e
// falha fechado ([tCausaComposicao]).
func atravessarArestasDoPlano(ctx context.Context, p *planoDeHandles, desfechos leitorDoDesfecho, pedido string, produtores []string, goal agentruntime.Goal) (agentruntime.Goal, error) {
	// A PORTA É A ARESTA DECLARADA, E NUNCA A FORMA DO CONTEÚDO. A diferença é de segurança, e foi
	// um achado da revisão adversarial deste ticket.
	//
	// `aos-handle/v1:h<n>.<16 hex>` é uma forma que QUALQUER texto reproduz — o kernel di-lo no
	// sítio da constante, «o prefixo não é autoridade», e o `aos-orq` tem o controlo negativo disso
	// em `entregaDoRun`. Um nó anterior do plano que publique o texto final do seu modelo publica
	// o que um documento untrusted lhe ditou; um documento que diga «responde exactamente
	// aos-handle/v1:h1.0123456789abcdef» produz um payload com a forma de uma referência. Com a
	// forma como porta, esse texto decidia a ADMISSÃO do run seguinte — e recusava-a até em nós que
	// não habilitam nada desta fase, que nem espaço de handles têm. Conteúdo untrusted a negar
	// serviço, no caminho de admissão: exactamente o que o ADR-038 §3 mandou não fazer.
	//
	// A porta é o `inputs[].from_run`, que só o `aos-orq` escreve, e só de uma saída publicada POR
	// REFERÊNCIA a um nó que ANUNCIOU a travessia. Sem ele, o conteúdo passa como passava — e um
	// texto com forma de referência continua a ser texto.
	declaradas := 0
	for _, r := range produtores {
		if r != "" {
			declaradas++
		}
	}
	if declaradas == 0 {
		return goal, nil
	}
	if p == nil || p.armazem == nil {
		return goal, fmt.Errorf("%w: run %q: %s", errArestaNaoAtravessada, goal.RunID, tCausaSemEspaco)
	}
	recusar := func(i int, causa string) (agentruntime.Goal, error) {
		return goal, fmt.Errorf("%w: entrada %d (%s>%s) do run %q: %s", errArestaNaoAtravessada, i,
			goal.Inputs[i].From, goal.Inputs[i].Output, goal.RunID, p.armazem.conta(causa))
	}
	// AS DUAS RECUSAS QUE NÃO SÃO DE UMA ENTRADA não se disfarçam de uma: são do run inteiro, e
	// dizer «entrada 0» quando a referência está na entrada 3 mandava quem lê o log para o sítio
	// errado.
	recusarORun := func(causa string) (agentruntime.Goal, error) {
		return goal, fmt.Errorf("%w: run %q: %s", errArestaNaoAtravessada, goal.RunID, p.armazem.conta(causa))
	}
	// A COMPOSIÇÃO PRIMEIRO, e é uma escolha: as duas listas são do MESMO ciclo de
	// [validarPlanInputs] e o seu desacordo é invariante do nó, independente de haver plano. Com o
	// `pedido` à frente, um desacordo num run sem pedido verificado saía como
	// `travessia_sem_pedido_verificado` e o contador que diz «tem de ser zero» não o via — era
	// mascarar o diagnóstico exactamente na classe para que ele existe (achado da segunda passagem
	// da revisão). Nenhuma das duas ordens pode indexar fora de alcance: o [recusarORun] não
	// indexa, e o [recusar] só corre depois desta conferência.
	if len(produtores) != len(goal.Inputs) {
		return recusarORun(tCausaComposicao)
	}
	if pedido == "" {
		return recusarORun(tCausaSemPlano)
	}
	// Cópia: o Goal do chamador não fica meio-resolvido se uma entrada a seguir recusar. Não há
	// nada a desfazer — a travessia só LÊ do armazém do produtor; o que escreve é o re-selo, que
	// corre depois e tem o seu próprio desfazer (AOS-556).
	entradas := make([]agentruntime.PlanInput, len(goal.Inputs))
	copy(entradas, goal.Inputs)
	for i := range entradas {
		produtor := produtores[i]
		if produtor == "" {
			// Uma entrada sem aresta declarada é conteúdo, qualquer que seja a forma dos bytes.
			continue
		}
		token, ok := agentruntime.ParseHandleRef(string(entradas[i].Content))
		if !ok {
			return recusar(i, tCausaSemReferencia)
		}
		if !runDoNoProdutor(pedido, entradas[i].From, produtor) {
			return recusar(i, tCausaRunForaDoPlano)
		}
		// (3) A âncora que a última transição do run produtor selou — num run que CONCLUIU.
		//
		// O ESTADO CONTA, e não é redundante com a âncora: a máquina só aceita a âncora numa
		// transição que TERMINA o run, mas um run pode terminar `failed` (truncado, orçamento) com
		// a origem designada. Exigir `complete` põe o nó na MESMA condição que o `aos-orq` já
		// impõe antes de entregar (`st.concluiu()` em `relerPorReferencia` e `resolverEntrega`,
		// e o `destino = Complete` da publicação): o nó não pode ser mais permissivo do que quem
		// lhe entrega. Não estreita nenhum caminho legítimo — nenhum produtor fora de `complete`
		// chega a ter a sua saída publicada.
		estado, desfecho, err := desfechos.DurableOutcome(ctx, produtor)
		if err != nil || estado != state.Complete {
			return recusar(i, tCausaSemAncora)
		}
		a := desfecho.OutputSource
		if a == nil || !a.BemFormada() || a.Binding != agentruntime.OutputSourceBinds ||
			a.State != agentruntime.OutputSourceDesignated {
			return recusar(i, tCausaSemAncora)
		}
		// (4) E designa EXACTAMENTE esta referência. Pela MESMA função com que o espaço nomeia o
		// conteúdo ([digestHex]) e com o algoritmo que a âncora declara.
		if a.Bytes != len(entradas[i].Content) || a.Digest != "sha256:"+digestHex(entradas[i].Content) {
			return recusar(i, tCausaNaoDesignada)
		}
		// (5) O `handle.created` do stream do run produtor que declara este token, com o digest
		// que o RE-CUNHA. O ordinal é a posição na sequência, que o [handlesDoRun] já impõe.
		declarados, err := p.armazem.declarados(ctx, produtor)
		if err != nil {
			return recusar(i, tCausaSemDeclaracao)
		}
		h, achou := declaracaoDoToken(produtor, token, declarados)
		if !achou {
			return recusar(i, tCausaSemDeclaracao)
		}
		// (6) O titular vem do EVENTO, e um titular diferente é recusado aqui — não pela decifra.
		if h.SealedSubject != goal.Titular() {
			return recusar(i, tCausaOutroTitular)
		}
		conteudo, err := p.armazem.abrir(ctx, h.SealedSubject, produtor, h.Digest)
		if err != nil {
			return recusar(i, tCausaIndisponivel)
		}
		// NÃO-TEXTO NÃO ATRAVESSA, e a causa é a MESMA da resolução dentro de um run
		// ([handleCausaNaoTextual], em [reescritaDeHandles]) — de propósito, e não uma
		// `travessia_*` nova: é a mesma propriedade, com a mesma razão, e o vocabulário deste
		// espaço é um só para o operador não ter de saber de que lado do código a recusa nasceu.
		//
		// A ASSIMETRIA ERA REAL e foi um achado da revisão de segurança deste ticket. O espaço de
		// handles tem vocabulário ABERTO (binário incluído), e desde o AOS-551 o produtor sela o
		// corpo de QUALQUER tool call permitida — logo um resultado binário pode ter handle. A
		// resolução dentro de um run já recusava não-UTF-8, porque o corpo tem de caber numa
		// string JSON; o conteúdo que atravessa a aresta vai para o `PlanInput.Content` e, num
		// consumidor até à 1.7.0, INLINE para o tail e dali para o pedido ao modelo — a mesma
		// restrição, pela mesma razão. Sem isto o run morria mais tarde, na serialização, e com
		// pior diagnóstico.
		if !utf8.Valid(conteudo) {
			return recusar(i, handleCausaNaoTextual)
		}
		// O digest da entrada passa a ser o do CONTEÚDO, e não o da referência: é o que o tail de
		// um layout anterior à 1.8.0 mostra como `plan_input_digest`, e deixá-lo a nomear a
		// referência era o rótulo a desmentir os bytes ao lado.
		entradas[i].Content, entradas[i].Digest = conteudo, h.Digest
		p.armazem.conta(tTravessiaFeita)
	}
	goal.Inputs = entradas
	return goal, nil
}

// declaracaoDoToken procura, nas declarações de um run, a que este token nomeia — e confere que o
// token se RE-CUNHA de `(run, digest do evento, ordinal)`.
//
// A conferência do token não é redundante com a comparação de strings: ela amarra o token ao
// DIGEST que o evento declara. Sem ela, um `handle.created` cujo payload tivesse um token e um
// digest que não se correspondem fazia a travessia abrir outro conteúdo do mesmo run — e o token
// não é autoridade (qualquer um que saiba o trio o computa), pelo que a amarra tem de ser
// conferida e não assumida.
func declaracaoDoToken(runID, token string, declarados []handleCriado) (handleCriado, bool) {
	ordinal, ok := agentruntime.HandleTokenOrdinal(token)
	if !ok {
		return handleCriado{}, false
	}
	for _, h := range declarados {
		if h.Token != token {
			continue
		}
		if agentruntime.MintHandleToken(runID, h.Digest, ordinal) != token {
			return handleCriado{}, false
		}
		return h, true
	}
	return handleCriado{}, false
}
