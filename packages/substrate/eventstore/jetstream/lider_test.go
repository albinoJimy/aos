package jetstream

import (
	"errors"
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
func TestAOS432_CadaConsultaRecebeOPrazoQueResta(t *testing.T) {
	r := novoRelogio()
	const prazo = time.Second
	inicio := r.agora()
	var recebidos []time.Duration
	err := esperarLider("S", func(resta time.Duration) (string, error) {
		recebidos = append(recebidos, resta)
		r.t = r.t.Add(300 * time.Millisecond) // cada INFO demora 300 ms
		return "", nil
	}, prazo, r.agora, r.dormir)
	if !errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("sem líder nunca: err=%v, quer ErrStreamSemLider", err)
	}
	if len(recebidos) < 2 {
		t.Fatalf("consultas = %d, quer pelo menos 2", len(recebidos))
	}
	if recebidos[0] != prazo {
		t.Fatalf("1.ª consulta recebeu %s, quer o prazo inteiro %s", recebidos[0], prazo)
	}
	for i := 1; i < len(recebidos); i++ {
		if recebidos[i] >= recebidos[i-1] {
			t.Fatalf("consulta %d recebeu %s, não menos do que a anterior (%s): %v", i, recebidos[i], recebidos[i-1], recebidos)
		}
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
