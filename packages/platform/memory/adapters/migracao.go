package adapters

// migracao.go — AS QUATRO CLASSES DE MEMÓRIA MUDAM DE NOME SEM RESSUSCITAR O QUE FOI APAGADO
// (AOS-424).
//
// # PORQUÊ
//
// O prefixo era `memory.`, e formava `memory.episodic`, `memory.semantic`, `memory.procedural`
// e `memory.working`. O ponto não é representável num subject NATS — o
// `jetstream.Store.subjectDe` recusa-o —, pelo que sobre JetStream estes quatro streams eram
// inutilizáveis.
//
// E o modo de falha aqui é SILENCIOSO, que é o que o torna pior do que o das aprovações: o
// `rebuild` trata `ErrStreamNotFound` como «classe vazia, não é erro». Sobre um backend que
// recusa o nome, a memória do nó não dava erro nenhum — **desaparecia**. Degradação da
// qualidade do agente sem uma única linha de log.
//
// # O QUE NÃO PODE FALHAR NESTA MIGRAÇÃO: O TOMBSTONE
//
// A migração das aprovações tinha o marcador `used-` como o facto que não podia ficar para
// trás. Aqui o análogo é o **tombstone** (`memory.record.deleted`): apagar uma memória é um
// evento NOVO, e o `rebuild` reconstrói o estado por replay — «written» fixa o registo,
// «deleted» remove-o.
//
// Se um tombstone não atravessar, **o registo que ele apagava RESSUSCITA**. É por isso que a
// cópia preserva `(RunID, StepID)` — a idempotency-key — e a ORDEM, que é o que faz o
// «deleted» continuar a vir depois do «written» que ele apaga.
//
// # CALIBRAÇÃO HONESTA: HOJE NÃO EXISTEM TOMBSTONES EM PRODUÇÃO
//
// A primeira versão deste ficheiro dizia «uma memória que alguém (ou um `/dsar/erase`) mandou
// apagar». **As duas metades estavam erradas**, e uma revisão adversarial mediu-o:
//
//   - o `/dsar/erase` faz crypto-shred da KEK POR-TITULAR, e os registos de memória são JSON
//     em claro; o `subjectOf` (`cmd/aos/retention.go`) só reconhece `replay.captured` e
//     `step.ledger.applied`, pelo que o DSAR **não alcança** estes streams;
//   - `MemoryPort.Delete` **não tem um único chamador** na árvore fora de testes, e não há rota
//     HTTP que apague memória. Nenhum tombstone é escrito em produção hoje.
//
// O invariante continua a ser o certo a preservar — é o que torna o apagamento POSSÍVEL quando
// alguém o compuser —, mas quem calibrar a severidade pela frase antiga fica com o número
// errado. A perna de PRIVACIDADE é latente; a que está viva é outra, e é a de baixo.
//
// Nota sobre o `StepID` do tombstone: ele embebe o `seq` do registo apagado
// (`<class>:del:<id>:<seq>`), e os `seq` do stream novo são outros. **Não é problema**, e
// verificou-se: o `rebuild` obtém o id a apagar do PAYLOAD, nunca do `StepID`. O `seq` ali é
// só o que torna a chave única por apagamento.
//
// # ROLLBACK — A POSTURA, QUE A PRIMEIRA VERSÃO NÃO TINHA
//
// A migração copia mas **não carimba a origem**: os dois streams coexistem, e o legado nunca
// mais recebe nada. Nada no código impede um binário ANTIGO de voltar a escrever lá.
//
// **O que está VIVO hoje:** um `Put` feito na janela de rollback aterra no stream legado; no
// roll-forward é copiado e, por chegar DEPOIS, ganha por last-write-wins a escritas do binário
// novo que são cronologicamente posteriores. **Dado velho vence dado recente**, em silêncio.
//
// **O que está LATENTE** (e só se materializa quando alguém compuser um apagamento de memória):
// um tombstone escrito pelo binário novo fica só no stream novo; o binário antigo lê o legado,
// não o encontra, e o registo volta a estar legível — e se o roll-forward copiar depois um
// `written` do legado, a ressurreição torna-se PERMANENTE.
//
// **Postura:** depois desta migração, ninguém escreve na origem. O rollback para um binário que
// ainda lá escreve não é seguro para a memória, e não há aqui código que o torne seguro — um
// log append-only não desfaz. Quem precisar de reverter tem de o fazer sabendo isto.

import (
	"context"
	"fmt"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// streamPrefixLegado é o prefixo ANTIGO, e existe só para a migração o conseguir ler.
//
// Nada escreve nele: o `streamPrefix` em vigor é o que forma os nomes actuais. Fica na baseline
// do gate `stream-names` enquanto esta constante existir.
const streamPrefixLegado = "memory."

// streamLegadoDe devolve o nome ANTIGO do stream de uma classe.
func streamLegadoDe(class domain.MemoryClass) string { return streamPrefixLegado + string(class) }

// MigrarStreamsDeMemoria copia os factos das QUATRO classes dos streams legados para os
// actuais.
//
// Devolve o total copiado NESTA passagem, para que o chamador o possa declarar no arranque.
// FAIL-CLOSED: um erro em qualquer classe aborta e propaga — o chamador não pode compor a
// MemoryPort sobre uma migração parcial, porque um tombstone por copiar é uma memória apagada
// que volta.
func MigrarStreamsDeMemoria(ctx context.Context, store eventstore.CopiadorDeStream) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("adapters: migracao dos streams de memoria exige o Event Store (nil)")
	}

	total := 0
	// AS QUATRO CLASSES, e não uma lista escrita à mão: `domain.AllClasses()` é a fonte de
	// verdade do conjunto. Se uma classe nova nascer, migra com as outras sem ninguém se
	// lembrar — que é a diferença entre um conjunto fechado e uma lista que se esquece.
	for _, class := range domain.AllClasses() {
		n, err := eventstore.CopiarStream(ctx, store, streamLegadoDe(class), streamFor(class))
		if err != nil {
			return total, fmt.Errorf("adapters: migrar a classe %q: %w", class, err)
		}
		total += n
	}
	return total, nil
}
