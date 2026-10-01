package jetstream

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// relogioFalso avança só quando `dormir` é chamado (ou quando o teste o avança dentro da
// consulta): a espera é provada sem cluster e sem dormir de verdade.
type relogioFalso struct {
	t      time.Time
	dormiu []time.Duration
}

func novoRelogio() *relogioFalso { return &relogioFalso{t: time.Unix(1_790_000_000, 0)} }

func (r *relogioFalso) agora() time.Time { return r.t }

func (r *relogioFalso) dormir(d time.Duration) { r.dormiu = append(r.dormiu, d); r.t = r.t.Add(d) }

// TestAOS432_EsperaPeloLiderAntesDeDevolver — o caso medido: o grupo existe, ainda sem
// líder, e elege-o à terceira consulta. A espera só pode terminar quando ele aparece.
func TestAOS432_EsperaPeloLiderAntesDeDevolver(t *testing.T) {
	r := novoRelogio()
	respostas := []string{"", "", "aos-2"}
	consultas := 0
	err := esperarLider("S", func(time.Duration) (string, error) {
		l := respostas[consultas]
		consultas++
		return l, nil
	}, time.Second, r.agora, r.dormir)
	if err != nil {
		t.Fatalf("com o líder eleito à 3.ª consulta, a espera falhou: %v", err)
	}
	if consultas != 3 {
		t.Fatalf("consultas = %d, quer 3 — devolveu antes de haver líder, ou consultou a mais", consultas)
	}
	if len(r.dormiu) != 2 || r.dormiu[0] != intervaloLiderInicial || r.dormiu[1] != 2*intervaloLiderInicial {
		t.Fatalf("intervalos = %v, quer [%s %s]", r.dormiu, intervaloLiderInicial, 2*intervaloLiderInicial)
	}
}

// TestAOS432_LiderJaPresenteNaoEspera — o caso comum, e o de um R1 standalone (que o
// servidor real anuncia com `leader` = id do servidor, medido na revisão): líder à
// primeira consulta, zero esperas.
func TestAOS432_LiderJaPresenteNaoEspera(t *testing.T) {
	r := novoRelogio()
	consultas := 0
	err := esperarLider("S", func(time.Duration) (string, error) {
		consultas++
		return "NSERVIDORSTANDALONE", nil
	}, time.Second, r.agora, r.dormir)
	if err != nil || consultas != 1 || len(r.dormiu) != 0 {
		t.Fatalf("líder presente: err=%v consultas=%d esperas=%v — quer nil, 1, nenhuma", err, consultas, r.dormiu)
	}
}

// TestAOS432_CadaConsultaRecebeOPrazoQueResta — o prazo é UM orçamento. Uma consulta lenta
// gasta dele, e a seguinte só pode usar o que sobra; sem isto a espera podia durar ~2× o
// prazo (e o Abrir ~4×), porque cada INFO levava o prazo inteiro.
//
// AOS-455: cada consulta recebe o MENOR entre o prazo de uma consulta
// ([consultaLiderInicial], que só cresce quando uma consulta fica sem resposta) e o que resta.
// Até lá recebia o que restava — tudo, à primeira — e um INFO calado gastava o orçamento
// inteiro. O que este teste fixa continua a ser o mesmo: nenhuma consulta recebe mais do que
// resta, e a espera não passa do prazo.
func TestAOS432_CadaConsultaRecebeOPrazoQueResta(t *testing.T) {
	r := novoRelogio()
	const prazo = time.Second
	inicio := r.agora()
	limite := inicio.Add(prazo)
	var recebidos, restavam []time.Duration
	err := esperarLider("S", func(d time.Duration) (string, error) {
		recebidos = append(recebidos, d)
		restavam = append(restavam, limite.Sub(r.agora()))
		r.t = r.t.Add(300 * time.Millisecond) // cada INFO demora 300 ms
		return "", nil
	}, prazo, r.agora, r.dormir)
	if !errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("sem líder nunca: err=%v, quer ErrStreamSemLider", err)
	}
	if len(recebidos) < 2 {
		t.Fatalf("consultas = %d, quer pelo menos 2", len(recebidos))
	}
	for i := range recebidos {
		if quer := min(consultaLiderInicial, restavam[i]); recebidos[i] != quer {
			t.Fatalf("consulta %d recebeu %s, quer %s (o menor entre o prazo de uma consulta e o que restava, %s): %v",
				i, recebidos[i], quer, restavam[i], recebidos)
		}
	}
	if ultimo := recebidos[len(recebidos)-1]; ultimo >= consultaLiderInicial {
		t.Fatalf("a última consulta recebeu %s — o que restava não a cortou: %v", ultimo, recebidos)
	}
	if gasto := r.agora().Sub(inicio); gasto > prazo+300*time.Millisecond {
		t.Fatalf("a espera gastou %s com prazo %s — o orçamento não é partilhado", gasto, prazo)
	}
	for _, d := range r.dormiu {
		if d <= 0 {
			t.Fatalf("espera não positiva %s: %v", d, r.dormiu)
		}
	}
}

// TestAOS432_PrazoEsgotadoEIndisponibilidadeNaoPosse — um grupo que nunca elege líder é
// o substrato indisponível, e tem de SAIR como tal: ErrNoQuorum (o sentinela canónico da
// porta) e ErrStreamSemLider (a causa). Fail-closed, e nunca um Store que aceitaria
// escritas destinadas a receber 503.
func TestAOS432_PrazoEsgotadoEIndisponibilidadeNaoPosse(t *testing.T) {
	r := novoRelogio()
	inicio := r.agora()
	err := esperarLider("S", func(time.Duration) (string, error) { return "", nil }, time.Second, r.agora, r.dormir)
	if !errors.Is(err, eventstore.ErrNoQuorum) || !errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("prazo esgotado sem líder: err=%v — quer ErrNoQuorum E ErrStreamSemLider", err)
	}
	for _, d := range r.dormiu {
		if d > intervaloLiderMaximo {
			t.Fatalf("intervalo %s acima do tecto %s", d, intervaloLiderMaximo)
		}
	}
	if gasto := r.agora().Sub(inicio); gasto != time.Second {
		t.Fatalf("desistiu ao fim de %s, quer exactamente o prazo de 1s (nem antes, nem depois)", gasto)
	}
}

// TestAOS432_ErroDaConsultaSobeSemRetentar — um INFO que falha (NATS em baixo, sem
// JetStream, sem permissões) não é «ainda sem líder»: sobe na hora, com a causa intacta.
// Re-tentar escondê-lo-ia atrás do prazo e trocá-lo-ia por ErrStreamSemLider.
func TestAOS432_ErroDaConsultaSobeSemRetentar(t *testing.T) {
	r := novoRelogio()
	consultas := 0
	err := esperarLider("S", func(time.Duration) (string, error) {
		consultas++
		return "", natsjs.ErrNoResponders
	}, time.Second, r.agora, r.dormir)
	if !errors.Is(err, natsjs.ErrNoResponders) {
		t.Fatalf("a causa perdeu-se: %v", err)
	}
	if errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("um erro de consulta foi reportado como falta de líder: %v", err)
	}
	if consultas != 1 {
		t.Fatalf("consultas = %d, quer 1 — um erro não se re-tenta às cegas", consultas)
	}
}

// errSilencio é o erro que um INFO sem resposta devolve — o mesmo embrulho de [natsjs.Conn.Request].
var errSilencio = fmt.Errorf("%w: %w ($JS.API.STREAM.INFO.S)", natsjs.ErrIndeterminate, natsjs.ErrTimeout)

// TestAOS455_ConsultaSemRespostaReperguntaComODobro — o servidor não responde ao INFO
// enquanto o grupo R3 se forma (AOS-455). A consulta calada NÃO é um erro que sobe, nem gasta o
// orçamento inteiro: a 1.ª recebe [consultaLiderInicial], cada uma sem resposta dobra a
// seguinte, e o líder anunciado à 3.ª devolve.
func TestAOS455_ConsultaSemRespostaReperguntaComODobro(t *testing.T) {
	r := novoRelogio()
	var recebidos []time.Duration
	err := esperarLider("S", func(d time.Duration) (string, error) {
		recebidos = append(recebidos, d)
		r.t = r.t.Add(d) // um INFO calado gasta o seu prazo, e só esse
		if len(recebidos) < 3 {
			return "", errSilencio
		}
		return "aos-2", nil
	}, 10*time.Second, r.agora, r.dormir)
	if err != nil {
		t.Fatalf("com o líder anunciado à 3.ª consulta (as duas primeiras caladas): %v", err)
	}
	quer := []time.Duration{consultaLiderInicial, 2 * consultaLiderInicial, 4 * consultaLiderInicial}
	if len(recebidos) != 3 || recebidos[0] != quer[0] || recebidos[1] != quer[1] || recebidos[2] != quer[2] {
		t.Fatalf("prazos das consultas = %v, quer %v", recebidos, quer)
	}
}

// TestAOS455_SilencioAtePrazoEsgotadoEIndisponibilidade — um grupo que nunca responde gasta o
// orçamento, e só ele, e sai como o grupo sem líder: ErrNoQuorum + ErrStreamSemLider, sem o
// «indeterminado — a escrita pode ter sido aplicada» de um INFO (que é uma LEITURA).
func TestAOS455_SilencioAtePrazoEsgotadoEIndisponibilidade(t *testing.T) {
	r := novoRelogio()
	inicio := r.agora()
	const prazo = 2 * time.Second
	err := esperarLider("S", func(d time.Duration) (string, error) {
		r.t = r.t.Add(d)
		return "", errSilencio
	}, prazo, r.agora, r.dormir)
	if !errors.Is(err, eventstore.ErrNoQuorum) || !errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("silêncio até ao fim: err=%v — quer ErrNoQuorum E ErrStreamSemLider", err)
	}
	if errors.Is(err, natsjs.ErrIndeterminate) {
		t.Fatalf("um INFO calado saiu como escrita indeterminada: %v", err)
	}
	if gasto := r.agora().Sub(inicio); gasto != prazo {
		t.Fatalf("desistiu ao fim de %s, quer exactamente o prazo %s", gasto, prazo)
	}
}

// TestAOS455_IndeterminadoSemTimeoutSobeSemRetentar — só o SILÊNCIO ([natsjs.ErrTimeout]) é
// re-perguntado. Um [natsjs.ErrIndeterminate] por outra razão — a ligação fechou com o pedido
// em voo — não é o grupo a formar-se: sobe à primeira, com a causa. Sem este caso, tratar todo
// o indeterminado como silêncio passava os outros testes (revisão do AOS-455) e escondia uma
// ligação perdida atrás do prazo inteiro.
func TestAOS455_IndeterminadoSemTimeoutSobeSemRetentar(t *testing.T) {
	r := novoRelogio()
	fechada := fmt.Errorf("%w: %w", natsjs.ErrIndeterminate, natsjs.ErrClosed)
	consultas := 0
	err := esperarLider("S", func(d time.Duration) (string, error) {
		consultas++
		r.t = r.t.Add(d)
		return "", fechada
	}, 10*time.Second, r.agora, r.dormir)
	if !errors.Is(err, natsjs.ErrClosed) {
		t.Fatalf("a causa perdeu-se: %v", err)
	}
	if errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("uma ligação fechada foi reportada como falta de líder: %v", err)
	}
	if consultas != 1 {
		t.Fatalf("consultas = %d, quer 1 — um indeterminado que não é silêncio não se re-pergunta", consultas)
	}
}
