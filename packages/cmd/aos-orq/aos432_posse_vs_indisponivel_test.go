package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// AOS-432, critério 4: «o lease foi negado» e «o NATS está em baixo» têm de continuar
// distinguíveis. A correcção do AOS-432 NÃO remapeou nenhum erro — fez o `Abrir` esperar
// pelo líder do stream —, e estes testes fixam que nenhuma das formas da indisponibilidade
// do substrato sai com o código da posse.

// TestAOS432_IndisponibilidadeDoSubstratoNaoEPosseNegada — a classificação, sem cluster e
// determinista. Cada forma da indisponibilidade, embrulhada como o `serve` a embrulha, sai
// com o código genérico; só o ErrLeaseHeld sai com 3.
func TestAOS432_IndisponibilidadeDoSubstratoNaoEPosseNegada(t *testing.T) {
	casos := []struct {
		nome string
		err  error
	}{
		{"503 no_responders (sem stream, sem JetStream, sem permissões)", natsjs.ErrNoResponders},
		{"sem ligação neste momento", natsjs.ErrDesligado},
		{"timeout de request", fmt.Errorf("%w: %w", natsjs.ErrIndeterminate, natsjs.ErrTimeout)},
		{"stream sem líder dentro do prazo", fmt.Errorf("%w: %w", eventstore.ErrNoQuorum, jetstream.ErrStreamSemLider)},
	}
	for _, c := range casos {
		err := fmt.Errorf("posse do run %q: %w", "r", c.err)
		if got := codigoDe(err); got == exitPosseNegada || got != exitErro {
			t.Errorf("%s: código %d, quer %d (erro) — a indisponibilidade do substrato NÃO é posse negada",
				c.nome, got, exitErro)
		}
	}
	posse := fmt.Errorf("posse do run %q: %w", "r", fmt.Errorf("%w: detido por %q", durable.ErrLeaseHeld, "p1"))
	if got := codigoDe(posse); got != exitPosseNegada {
		t.Fatalf("ErrLeaseHeld embrulhado com o dono: código %d, quer %d", got, exitPosseNegada)
	}
}

// TestAOS432_NATSEmBaixoSaiComErroENaoComPosse — o mesmo, ponta-a-ponta num processo real:
// um `serve --nats` contra um endereço onde nada escuta sai com o código genérico e diz
// que o que falhou foi o Event Store — não que outro processo detém o run.
func TestAOS432_NATSEmBaixoSaiComErroENaoComPosse(t *testing.T) {
	bin := construir(t)
	r := correr(t, bin, "serve", "--nats", "127.0.0.1:1", "--run", "run-aos432", "--nodes", "a")
	if r.code != exitErro {
		t.Fatalf("NATS inalcançável: saiu %d, quer %d (erro)\nstderr:\n%s", r.code, exitErro, r.stderr)
	}
	if !strings.Contains(r.stderr, "event store replicado") {
		t.Fatalf("a mensagem não nomeia o substrato como o que falhou:\n%s", r.stderr)
	}
	if strings.Contains(r.stderr, "posse do run") || strings.Contains(r.stderr, "detido por") {
		t.Fatalf("um NATS em baixo foi reportado como disputa de posse:\n%s", r.stderr)
	}
}
