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
// Qualquer OUTRO erro de `consultar` (stream inexistente, sem JetStream, ligação perdida) é
// devolvido tal qual — fail-closed, sem re-tentar às cegas. O prazo esgotado — sem líder, ou
// sem resposta — devolve [ErrStreamSemLider] embrulhado em [eventstore.ErrNoQuorum].
//
// Relógio e espera são injectados para que a lógica seja provada sem cluster e sem
// dormir de verdade (lider_test.go).
func esperarLider(stream string, consultar func(resta time.Duration) (lider string, err error),
	prazo time.Duration, agora func() time.Time, dormir func(time.Duration)) error {
	limite := agora().Add(prazo)
	calados := 0
	esgotado := func() error {
		if calados > 0 {
			return fmt.Errorf("%w: %w (%q, prazo %s, %d consulta(s) sem resposta)",
				eventstore.ErrNoQuorum, ErrStreamSemLider, stream, prazo, calados)
		}
		return fmt.Errorf("%w: %w (%q, prazo %s)", eventstore.ErrNoQuorum, ErrStreamSemLider, stream, prazo)
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
