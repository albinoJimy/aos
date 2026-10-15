package jetstream

import (
	"errors"
	"fmt"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// ErrStreamSemLider — o stream existe mas o seu grupo Raft não elegeu líder dentro do
// prazo. Sai embrulhado em [eventstore.ErrNoQuorum], que é o que isto é: o substrato não
// está disponível para escrita neste momento.
var ErrStreamSemLider = errors.New("jetstream: o stream não elegeu líder dentro do prazo")

// Intervalos da espera pelo líder. A janela MEDIDA no AOS-432 foi de ~100 ms num cluster
// local; o primeiro intervalo é curto para não a alongar, e o tecto impede que um cluster
// lento seja martelado com INFO.
const (
	intervaloLiderInicial = 5 * time.Millisecond
	intervaloLiderMaximo  = 100 * time.Millisecond
)

// consultaLiderInicial é o prazo da PRIMEIRA consulta de INFO da espera; cada consulta que fica
// sem resposta dobra o da seguinte (sempre cortado ao que resta do orçamento).
//
// AOS-455: o servidor NÃO RESPONDE a um `STREAM.INFO` enquanto o grupo R3 de um stream acabado
// de criar não tem quem responda (nats-server 2.10.22, `jsStreamInfoRequest`: nem líder, nem o
// membro preferido com o grupo já criado → `return` sem resposta). Medido contra o cluster do
// gate: 32 de 400 INFO logo a seguir a CREATE concorrentes ficaram calados, e a consulta
// seguinte respondeu em 1–2 ms. Uma resposta normal leva milissegundos; 250 ms é duas ordens
// de grandeza acima disso, e um cluster lento a sério ganha prazo a cada consulta.
const consultaLiderInicial = 250 * time.Millisecond

// esperarLider bloqueia até o stream ter líder, ou até o prazo acabar.
//
// # O defeito que isto fecha (AOS-432)
//
// No JetStream em cluster, só o LÍDER do stream subscreve os subjects dele. Entre o
// `STREAM.CREATE` e a eleição do líder do grupo R3 há uma janela em que o stream EXISTE
// (o INFO responde, sem erro) mas uma publicação recebe 503 — ninguém serve o subject.
//
// Medido contra o cluster do gate `nats` a 2026-09-26: quatro ligações a criar o MESMO
// stream em paralelo recebem TODAS o CREATE com sucesso, mas três recebem-no em ~40 ms com
// `cluster.leader` vazio e uma em ~106 ms, já com líder. As três primeiras publicam logo a
// seguir com CAS 0 e recebem 503; a quarta recebe `seq:1`. Uma ligação a publicar em ciclo
// recebe 503 durante ~100 ms e depois `seq:1`. Contra o mesmo stream COM líder, a mesma
// corrida dá zero 503: 1 ack e 3 × `10071 wrong last sequence`.
//
// Era esta a sequência de `aos-orq serve --nats` em N processos sobre um stream fresco: o
// perdedor publicava o `lease.claimed` na janela, recebia 503 em vez da recusa do CAS, e
// o `Claim` nunca chegava a ver o lease do vencedor — saía com um erro de transporte e o
// código genérico em vez de `exitPosseNegada`.
//
// # Porque a correcção é aqui, e não no mapeamento do 503
//
// Tratar o 503 como «conflito» no `Claim` punha o sistema a responder «outro processo
// detém o run» a um NATS sem stream, sem JetStream ou sem permissões — as três causas
// REAIS do 503 — e mandaria o operador procurar o dono de um run que ninguém detém. O 503
// continua a significar o que significa; o que muda é que [Abrir] só devolve um Store
// cujo stream está EM CONDIÇÕES de receber escritas.
//
// # Contrato
//
// `consultar` recebe o prazo DESTA consulta e devolve o líder corrente. O prazo total é um só:
// cada consulta e cada espera gastam do mesmo orçamento, pelo que a espera nunca excede
// `prazo`, por mais lenta que seja cada resposta. O prazo de cada consulta é
// [consultaLiderInicial], dobrado a cada consulta sem resposta, e nunca mais do que o que resta.
//
// # Uma consulta SEM RESPOSTA não é um erro de consulta (AOS-455)
//
// Até ao AOS-455 a consulta recebia o prazo que RESTAVA — todo ele, à primeira. Um INFO a que
// o servidor não responde (ver [consultaLiderInicial]) gastava assim o orçamento inteiro, e o
// `Abrir` falhava ao fim de 10 s com «indeterminado — sem resposta dentro do prazo» sobre um
// stream que tinha líder 2 ms depois. Era essa a falha do gate `nats` (4 em 50 corridas, nos
// dois testes de disputa do lease): o perdedor saía 1 em vez de 3. O silêncio
// ([natsjs.ErrTimeout]) é agora o que ele é durante a formação do grupo — «ainda ninguém
// responde» — e pergunta-se de novo, dentro do MESMO orçamento. Um INFO é uma leitura, e
// re-perguntar não escreve nada.
//
// Um líder vazio significa «ainda sem líder», e nada mais. Medido pela revisão do AOS-432
// contra `nats:2.10-alpine` standalone: um stream R1 fora de cluster TAMBÉM traz bloco
// `cluster`, com `leader` igual ao id do servidor — não há ramo «sem grupo Raft» a tratar,
// e um servidor que não anunciasse líder nenhum seria tratado como não pronto
// (fail-closed), que é o lado seguro.
//
// # «stream not found» logo depois do NOSSO CREATE também não é um erro de consulta (AOS-528)
//
// Esta espera só corre depois de o servidor ter ACEITE o `STREAM.CREATE` desta ligação: o
// stream existe. Um 404 aqui é a outra face da mesma janela, e o servidor declara-o no código
// (nats-server 2.10.29, `jsStreamInfoRequest`): o membro preferido de um grupo NOVO responde
// ao INFO mesmo sem líder; se ainda não criou o stream localmente, dorme 10 ms («This could be
// inflight») e responde `stream not found` (code=404 err_code=10059). Com N CREATE idênticos e
// concorrentes é o caso normal, não o raro. Medido: o gate `nats` do PR #489 (run 38397155492)
// perdeu 3 de 10 repetições do `TestAOS100_NServeEmParaleloSobreOSubstratoReplicado`, sempre
// com os TRÊS perdedores a sair `1` com este 404 — e o mesmo deu-se 1 vez em 21 contra o
// cluster local de 4 nós. Re-pergunta-se, com o intervalo de quem viu «ainda sem líder» e do
// MESMO orçamento. Um stream que continuasse a não existir até ao fim do prazo sai como os
// outros casos esgotados — indisponível, nunca «pronto» —, e a mensagem conta os 404.
//
// Qualquer OUTRO erro de `consultar` (sem JetStream, ligação perdida, sem permissões) é
// devolvido tal qual — fail-closed, sem re-tentar às cegas. O prazo esgotado — sem líder, sem
// resposta, ou sem o stream ainda servido — devolve [ErrStreamSemLider] embrulhado em
// [eventstore.ErrNoQuorum].
//
// Relógio e espera são injectados para que a lógica seja provada sem cluster e sem
// dormir de verdade (lider_test.go).
func esperarLider(stream string, consultar func(resta time.Duration) (lider string, err error),
	prazo time.Duration, agora func() time.Time, dormir func(time.Duration)) error {
	limite := agora().Add(prazo)
	calados, inexistentes := 0, 0
	esgotado := func() error {
		var detalhe string
		if calados > 0 {
			detalhe += fmt.Sprintf(", %d consulta(s) sem resposta", calados)
		}
		if inexistentes > 0 {
			detalhe += fmt.Sprintf(", %d consulta(s) com «stream not found» depois do CREATE aceite", inexistentes)
		}
		return fmt.Errorf("%w: %w (%q, prazo %s%s)", eventstore.ErrNoQuorum, ErrStreamSemLider, stream, prazo, detalhe)
	}
	intervalo := intervaloLiderInicial
	porConsulta := consultaLiderInicial
	for {
		resta := limite.Sub(agora())
		if resta <= 0 {
			return esgotado()
		}
		lider, err := consultar(min(porConsulta, resta))
		switch {
		case errors.Is(err, natsjs.ErrTimeout):
			// Silêncio: o grupo ainda não tem quem responda. Pergunta-se de novo, com o
			// dobro do prazo, depois do mesmo intervalo de quem viu «ainda sem líder».
			calados++
			porConsulta *= 2
		case streamAindaNaoServido(err):
			// O CREATE foi aceite: o membro que respondeu ainda não o criou localmente.
			inexistentes++
		case err != nil:
			return fmt.Errorf("jetstream: esperar pelo líder do stream %q: %w", stream, err)
		case lider != "":
			return nil
		}
		resta = limite.Sub(agora())
		if resta <= 0 {
			return esgotado()
		}
		dormir(min(intervalo, resta))
		if intervalo *= 2; intervalo > intervaloLiderMaximo {
			intervalo = intervaloLiderMaximo
		}
	}
}

// streamAindaNaoServido diz se o erro de uma consulta é o «stream not found» da janela do
// CREATE (ver [esperarLider]). Só o código estruturado do servidor conta: uma mensagem que
// por acaso dissesse «not found» não é o servidor a afirmá-lo.
func streamAindaNaoServido(err error) bool {
	var js *natsjs.JSError
	return errors.As(err, &js) && js.ErrCode == natsjs.CodeStreamNotFound
}
