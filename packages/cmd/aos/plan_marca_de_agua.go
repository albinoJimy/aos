package main

// plan_marca_de_agua.go — A PROJECÇÃO DA FILA DEIXA DE PAGAR O HISTÓRICO INTEIRO (AOS-429).
//
// # O CUSTO QUE ISTO CORTA, MEDIDO E NÃO SUPOSTO
//
// `pendentesNaFila` corre a CADA `POST /plans` (o tecto de pendentes é verificado antes de
// escrever) e lê o stream desde o `seq` 1. `reclamarUm` faz o mesmo a cada reclamação. O tecto de
// 1000 limita os PENDENTES; não limita o trabalho de os contar.
//
// A consequência é que a fila fica mais lenta com a IDADE do nó, não com a carga: um nó que
// tenha servido cem mil pedidos, todos terminados há meses, relê cem mil eventos para descobrir
// que há três pendentes. E não há retenção que resolva isto — o crypto-shredding torna o
// conteúdo ilegível, **não apaga o evento**.
//
// # PORQUE É QUE NÃO SE APAGA NADA, E ISSO NÃO É UMA LACUNA
//
// O contrato do Event Store tem seis métodos — `Append`, `Read`, `Subscribe`, `Close`, `Healthy`,
// `Streams`. Não há `Delete`, `Truncate`, `Purge` nem `Compact`, e **não é omissão**: um log
// append-only encadeado por hash de que se pudessem remover entradas deixava de ser
// tamper-evident, e é a evidência de adulteração que sustenta tudo o resto — o WORM, a cadeia de
// auditoria, o replay determinista.
//
// Logo a resposta certa não é encolher o log. É deixar de o reler.
//
// # A MARCA DE ÁGUA
//
// O maior `seq` tal que TODOS os pedidos submetidos até ele estão terminados. Abaixo dela não há
// nada que a projecção possa concluir de diferente: um pedido terminado já é filtrado da fila.
//
// As reclamações e desfechos desses pedidos que fiquem ACIMA da marca tornam-se órfãos quando o
// `submitted` deixa de ser lido — e a projecção já os ignora («não se serve o que nunca foi
// pedido»). É por isso que cortar aqui é seguro: o resultado é igual, não aproximado, e há teste
// a compará-los evento a evento.
//
// # O QUE ISTO NÃO RESOLVE
//
// A marca vive em MEMÓRIA. Um restart volta a pagar a leitura completa, uma vez, e daí em diante
// fica na mesma. Persisti-la exigiria ou um stream de marcas (que cresce, só mais devagar) ou
// uma leitura para trás que o contrato não tem — e o custo de arranque, uma vez por processo, não
// justifica nenhuma das duas. Fica declarado no AOS-429, com o número: em regime, a projecção
// passa a ser linear nos pedidos NÃO TERMINADOS, que o tecto limita a 1000.

import (
	"sync/atomic"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// marcaDeAgua é o `seq` a partir do qual vale a pena ler a fila.
//
// `atomic` e não mutex porque o único movimento é subir, e duas subidas concorrentes com valores
// diferentes têm de convergir para o MAIOR — um `Store` cego deixaria a marca recuar se duas
// projecções terminassem fora de ordem, e uma marca que recua só custa uma leitura a mais; uma
// marca que avança de mais SALTA pedidos vivos. Daí o CAS.
type marcaDeAgua struct{ v atomic.Uint64 }

// avancar sobe a marca para `n` se `n` for maior. Nunca desce.
func (m *marcaDeAgua) avancar(n uint64) {
	for {
		atual := m.v.Load()
		if n <= atual {
			return
		}
		if m.v.CompareAndSwap(atual, n) {
			return
		}
	}
}

// desde devolve o `fromSeq` para a próxima leitura: o primeiro evento AINDA relevante.
func (m *marcaDeAgua) desde() uint64 { return m.v.Load() + 1 }

// projectarFilaComMarca é a projecção, mais o `seq` até ao qual já não é preciso reler.
//
// A marca sai DAQUI, e não de uma função própria, por uma razão que este eixo já pagou duas
// vezes: a definição de «terminado» estaria em dois sítios, e dois sítios derivam. Quem mudar a
// regra de filtragem muda a marca com ela, sem ter de se lembrar.
func projectarFilaComMarca(eventos []eventstore.Event, agora time.Time) ([]pedidoNaFila, uint64) {
	fila, terminados := projectarComTerminados(eventos, agora)

	// A MARCA É O PREFIXO CONTÍGUO DE TERMINADOS, e não «todos os terminados».
	//
	// Um pedido terminado com `seq` 900 não autoriza cortar em 900 se houver um pendente em 5:
	// a leitura seguinte deixaria de ver o `submitted` do 5 e a projecção passaria a ignorar um
	// pedido VIVO. Por isso caminha-se por ordem de chegada e pára-se no primeiro que não
	// terminou.
	var marca uint64
	for _, t := range terminados {
		if !t.terminado {
			break
		}
		marca = t.seq
	}
	return fila, marca
}

// estadoTerminal é o par (seq do `submitted`, terminou?) por ordem de chegada.
type estadoTerminal struct {
	seq       uint64
	terminado bool
}
