package jetstream

// AOS-354 — `E_NO_QUORUM` NÃO EXISTIA NO BACKEND REPLICADO, E A TOLERÂNCIA QUE O BANNER
// PROMETE NUNCA ERA ARMADA.
//
// [eventstore.ErrNoQuorum] era produzido APENAS pelo store de referência. Em `jetstream/` e
// `natsjs/` havia ZERO ocorrências: o erro canónico de indisponibilidade transitória deste
// substrato é [natsjs.ErrDesligado]. Consequência desigual nos dois consumidores:
//
//   - `cmd/aos/trajectory.go` — cosmética: HTTP 500 em vez de 503;
//   - `cmd/aos/progress_wiring.go` — não: `burndownTransitorio` é a lista FECHADA que decide
//     entre indisponibilidade momentânea e CEGUEIRA. Sobre JetStream, um `ErrDesligado` caía
//     em cegueira e MATAVA O RUN À PRIMEIRA fronteira, em vez de tolerar N consecutivas.
//
// `posture_banner.go` promete por escrito o contrário — e sobre o substrato que AOS-100
// tornou preferencial essa promessa não tinha como ser cumprida.
//
// Estes testes correm SEM cluster: medem a tradução, que é uma função pura da cadeia de
// erros. O comportamento ponta-a-ponta contra um cluster real fica NÃO VERIFICADO (ver
// specs/EPIC-24 §0.6 — nada neste epic foi corrido contra NATS real).

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

func TestAOS354_DesligadoPassaAResponderAoSentinelaCanonico(t *testing.T) {
	casos := []struct {
		nome      string
		err       error
		canonico  bool
		especifco bool
	}{
		{"nil continua nil", nil, false, false},
		{"ErrDesligado cru", natsjs.ErrDesligado, true, true},
		{
			"ErrDesligado embrulhado pelo caminho de leitura",
			fmt.Errorf("jetstream: leitura de %q: %w", "aos.run-1", natsjs.ErrDesligado),
			true, true,
		},
		{
			"outro erro do substrato NÃO é traduzido",
			natsjs.ErrProtocol,
			false, false,
		},
		{
			"ErrStreamNotFound continua a ser ausência de dados, não de disponibilidade",
			eventstore.ErrStreamNotFound,
			false, false,
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := indisponibilidadeTransitoria(c.err)
			if c.err == nil {
				if got != nil {
					t.Fatalf("nil traduzido para %v", got)
				}
				return
			}
			if errors.Is(got, eventstore.ErrNoQuorum) != c.canonico {
				t.Errorf("errors.Is(_, ErrNoQuorum) = %v, quero %v (erro=%v)",
					errors.Is(got, eventstore.ErrNoQuorum), c.canonico, got)
			}
			// A CAUSA NÃO SE PERDE. Um operador que leia o erro tem de continuar a ver
			// que o que houve foi uma desligação, e não uma perda de quórum de réplicas.
			if errors.Is(got, natsjs.ErrDesligado) != c.especifco {
				t.Errorf("errors.Is(_, ErrDesligado) = %v, quero %v (erro=%v)",
					errors.Is(got, natsjs.ErrDesligado), c.especifco, got)
			}
		})
	}
}

// TestAOS354_NaoEmbrulhaDuasVezes guarda a idempotência da tradução. Sem ela, um erro que
// atravessasse duas camadas traduzidas ganharia dois prefixos e a mensagem do operador
// começaria a repetir-se.
func TestAOS354_NaoEmbrulhaDuasVezes(t *testing.T) {
	uma := indisponibilidadeTransitoria(natsjs.ErrDesligado)
	duas := indisponibilidadeTransitoria(uma)
	if uma.Error() != duas.Error() {
		t.Fatalf("tradução não é idempotente:\n  uma = %v\n duas = %v", uma, duas)
	}
}

// TestAOS354_ORunNaoMorreAPrimeiraFronteira é o teste que dá substância ao ticket: replica
// o predicado FECHADO de `cmd/aos/progress_wiring.go` (`burndownTransitorio`) e prova que um
// erro de indisponibilidade do substrato REPLICADO passa a cair no lado «momentânea».
//
// O predicado é replicado e não importado porque vive em `package main` de `cmd/aos` e não é
// alcançável daqui. A réplica é a lista LITERAL desse ficheiro, e a sua fidelidade é a
// hipótese declarada deste teste: se lá a lista mudar, aqui não avermelha. O sensor que
// apanha isso é o próprio `burndownTransitorio`, que continua a ramificar em
// `eventstore.ErrNoQuorum` — e é essa a razão de a correcção ter ido para o BACKEND e não
// para a lista.
func TestAOS354_ORunNaoMorreAPrimeiraFronteira(t *testing.T) {
	// A lista fechada de burndownTransitorio, na parte que este ticket toca.
	transitorio := func(err error) bool { return errors.Is(err, eventstore.ErrNoQuorum) }

	cru := natsjs.ErrDesligado
	if transitorio(cru) {
		t.Fatal("controlo inválido: o erro cru já era transitório antes da tradução")
	}
	traduzido := indisponibilidadeTransitoria(cru)
	if !transitorio(traduzido) {
		t.Fatal("uma indisponibilidade TRANSITÓRIA do substrato replicado continua a ler-se " +
			"como CEGUEIRA — o run morre à primeira fronteira, contra o que o banner de " +
			"postura promete por escrito (AOS-354)")
	}
}

// TestAOS345_ReplyMalformadoNaoDerrubaQuemNaoPrecisaDeAvancar é o teste que a revisão
// adversarial obrigou a escrever. A primeira versão da correcção de AOS-345 derrubava o
// lote na PRIMEIRA mensagem cujo `$JS.ACK` não tivesse 9 ou 12 tokens — dentro do laço de
// entrega, por mensagem, incondicionalmente.
//
// O argumento de proporcionalidade que justificava tolerar um reply AUSENTE aplica-se
// palavra por palavra a um MALFORMADO: nos dois casos o que se tem é «não sei qual é o seq
// físico». Com a versão anterior, uma forma de reply inesperada — uma versão de servidor
// com um token a mais, um leafnode que reescreva o subject — tornava ILEGÍVEL um stream de
// três eventos, e (porque `hidratar` precede as escritas) também INESCREVÍVEL. Estritamente
// pior do que o defeito que AOS-345 fecha, que só aparecia acima de 2048 eventos.
func TestAOS345_ReplyMalformadoNaoDerrubaQuemNaoPrecisaDeAvancar(t *testing.T) {
	malformado := func(total uint64) func(inicio, quantos uint64) (loteLido, error) {
		var entregues uint64
		return func(_, quantos uint64) (loteLido, error) {
			n := total - entregues
			if n > quantos {
				n = quantos
			}
			entregues += n
			// O que `seqDoStreamNaResposta` devolve para um reply de forma inesperada.
			_, causa := seqDoStreamNaResposta("$JS.ACK.stream.consumidor.1.2.3.4.5.EXTRA")
			if causa == nil {
				t.Fatal("controlo inválido: o reply da sonda devia ser rejeitado pelo parser")
			}
			return loteLido{eventos: make([]eventstore.Event, n), porqueDesconhecido: causa}, nil
		}
	}

	t.Run("cabe na janela: lê na mesma", func(t *testing.T) {
		evs, err := lerEmLotes("aos.es.teste.run-x", 3, janelaDeLeitura, malformado(3))
		if err != nil {
			t.Fatalf("um stream de 3 eventos foi derrubado por um $JS.ACK que não usa: %v — "+
				"isto é pior do que o defeito que AOS-345 fecha", err)
		}
		if len(evs) != 3 {
			t.Fatalf("leu %d eventos, quero 3", len(evs))
		}
	})

	t.Run("não cabe: falha, e a causa vai no erro", func(t *testing.T) {
		_, err := lerEmLotes("aos.es.teste.run-x", janelaDeLeitura+1, janelaDeLeitura, malformado(janelaDeLeitura+1))
		if err == nil {
			t.Fatal("a janela avançou às cegas com um seq físico ilegível")
		}
		if !errors.Is(err, natsjs.ErrProtocol) {
			t.Fatalf("erro = %v — a causa (violação de protocolo) tem de viajar até ao operador", err)
		}
	})
}
