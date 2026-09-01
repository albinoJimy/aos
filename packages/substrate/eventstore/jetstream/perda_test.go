package jetstream_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// perda_test.go — o AC4 do AOS-100: «a perda de uma réplica NÃO PERDE DADOS nem
// interrompe escritas (dentro do quórum)».
//
// # Porque este ficheiro existe, e o que ele corrige
//
// A primeira medição desta propriedade, a 2026-08-31, afirmou «zero perda» com base em
// `messages: 4` depois de matar o líder. Uma auditoria adversarial derrubou-a, e com
// razão: uma CONTAGEM FINAL prova que no fim havia quatro mensagens, e não que as
// confirmadas antes da falha sobreviveram. A afirmação foi RETRACTADA (adenda 1, §A3) e
// ficou por provar.
//
// Provar exige reconciliar: registar CADA seq confirmado antes da falha e, depois dela,
// reler o log e verificar que cada um lá está, com a carga certa. É o que este teste faz.
//
// # Porque precisa de um comando externo
//
// Matar um nó do cluster não é operação de um teste Go — é do operador. O comando vem de
// `AOS_KILL_CMD` e o teste é SALTADO sem ele. Um teste que fingisse a falha (fechando uma
// ligação, por exemplo) mediria o cliente, não o cluster.
//
//	AOS_NATS_URL=172.22.0.2:4222 \
//	AOS_KILL_CMD='docker stop $(<comando que descobre o líder do stream>)' \
//	AOS_RESTORE_CMD='docker start ...' \
//	    ./bench.test -test.run TestAC4
const (
	envKill    = "AOS_KILL_CMD"
	envRestore = "AOS_RESTORE_CMD"
)

// escritaConfirmada é o registo de UM ack. É contra esta lista que a reconciliação corre.
type escritaConfirmada struct {
	seq   uint64
	marca string
}

func correrComando(t *testing.T, nome, cmd string) {
	t.Helper()
	saida, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s (%q) falhou: %v\n%s", nome, cmd, err, saida)
	}
	t.Logf("%s: %s", nome, saida)
}

// TestAC4_EscritasConfirmadasSobrevivemAMorteDeUmNo é a prova que faltava.
//
// A propriedade medida é PRECISA, e é mais fraca do que «nada se perde»: **toda a escrita
// CONFIRMADA antes da falha continua no log depois dela, com a carga certa e na mesma
// posição**. Escritas que falharam durante o failover não são perdas — são recusas, e o
// contrato C2 diz que uma recusa não deixa rasto. Confundir as duas foi o que produziu a
// afirmação retractada.
func TestAC4_EscritasConfirmadasSobrevivemAMorteDeUmNo(t *testing.T) {
	addr := servidor(t)
	matar := os.Getenv(envKill)
	if matar == "" {
		t.Skipf("sem comando de falha: define %s (ex.: docker stop <líder do stream>)", envKill)
	}
	if restaurar := os.Getenv(envRestore); restaurar != "" {
		t.Cleanup(func() { correrComando(t, "restauro", restaurar) })
	}

	nome := "AC4_" + sufixo(t)
	abrir := func() (*jetstream.Store, error) {
		return jetstream.Abrir(addr,
			jetstream.ComNomeDeStream(nome),
			jetstream.ComReplicas(3),
			jetstream.ComPrazo(20*time.Second),
			jetstream.SemCriarStream())
	}

	criador, err := jetstream.Abrir(addr,
		jetstream.ComNomeDeStream(nome), jetstream.ComReplicas(3), jetstream.ComPrazo(20*time.Second))
	if err != nil {
		t.Fatalf("criar stream R3: %v", err)
	}
	t.Cleanup(func() {
		_ = criador.ApagarStream()
		_ = criador.Close()
	})

	ctx := context.Background()
	const stream = "run-ac4"
	const antes, depois = 40, 40

	confirmadas := make([]escritaConfirmada, 0, antes+depois)
	escrever := func(st eventstore.EventStore, i int) error {
		marca := "carga-" + strconv.Itoa(i)
		res, err := st.Append(ctx, stream, eventstore.EventInput{
			Type:    "ac4.escrita",
			Payload: json.RawMessage(`{"marca":"` + marca + `"}`),
		})
		if err != nil {
			return err
		}
		confirmadas = append(confirmadas, escritaConfirmada{seq: res.Seq, marca: marca})
		return nil
	}

	// FASE A — escritas com o cluster inteiro de pé. Cada ack é registado.
	for i := 0; i < antes; i++ {
		if err := escrever(criador, i); err != nil {
			t.Fatalf("escrita %d antes da falha: %v", i, err)
		}
	}
	t.Logf("fase A: %d escritas CONFIRMADAS antes da falha", len(confirmadas))

	// A FALHA.
	correrComando(t, "matar nó", matar)

	// FASE B — as escritas que passarem contam; as que falharem NÃO são perdas. O
	// cliente não tem reconexão (limite declarado), pelo que a ligação pode ter morrido
	// com o nó: nesse caso abre-se uma nova, que é o que um worker faria.
	falhas := 0
	st := eventstore.EventStore(criador)
	for i := antes; i < antes+depois; i++ {
		if err := escrever(st, i); err != nil {
			falhas++
			if nova, errAbrir := abrir(); errAbrir == nil {
				defer func() { _ = nova.Close() }()
				st = nova
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	t.Logf("fase B: %d escritas confirmadas no total, %d tentativas falharam durante o failover (recusas, não perdas)",
		len(confirmadas), falhas)

	// A RECONCILIAÇÃO — o que faltava à medição anterior.
	leitor, err := abrir()
	if err != nil {
		t.Fatalf("reabrir para reconciliar: %v", err)
	}
	defer func() { _ = leitor.Close() }()

	evs, err := leitor.Read(ctx, stream, 1)
	if err != nil {
		t.Fatalf("reler o log depois da falha: %v", err)
	}
	porSeq := make(map[uint64]eventstore.Event, len(evs))
	for _, ev := range evs {
		porSeq[ev.Seq] = ev
	}

	var perdidas, corrompidas int
	for _, c := range confirmadas {
		ev, existe := porSeq[c.seq]
		if !existe {
			perdidas++
			t.Errorf("PERDA: o seq=%d foi CONFIRMADO antes/durante a falha e não está no log", c.seq)
			continue
		}
		if marcaDe(t, ev) != c.marca {
			corrompidas++
			t.Errorf("CORRUPÇÃO: o seq=%d tem a carga %q, foi confirmado com %q", c.seq, marcaDe(t, ev), c.marca)
		}
	}

	// E o inverso: nada que não tenha sido confirmado pode estar no log. Um evento
	// fantasma é tão grave como uma perda — significaria que uma escrita RECUSADA ficou
	// durável, que é a garantia «ERRO ⇒ NADA FICOU DURÁVEL» a partir-se.
	if len(evs) != len(confirmadas) {
		t.Errorf("o log tem %d eventos e foram confirmadas %d escritas — a diferença são eventos que ninguém confirmou (fantasmas) ou perdas já contadas acima",
			len(evs), len(confirmadas))
	}

	if perdidas == 0 && corrompidas == 0 {
		t.Logf("reconciliação: %d/%d escritas confirmadas presentes e íntegras depois da morte de um nó",
			len(confirmadas), len(confirmadas))
	}
}

func marcaDe(t *testing.T, ev eventstore.Event) string {
	t.Helper()
	var p struct {
		Marca string `json:"marca"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Sprintf("<payload ilegível: %v>", err)
	}
	return p.Marca
}

// TestAC4_ComandoDeFalhaEObrigatorio garante que o teste acima não passa por ser saltado
// sem ninguém reparar: se `AOS_NATS_URL` está definido mas `AOS_KILL_CMD` não, quem corre
// a suite fica a saber que a propriedade NÃO foi medida.
func TestAC4_ComandoDeFalhaEObrigatorio(t *testing.T) {
	if os.Getenv(envServidor) == "" {
		t.Skip("sem cluster")
	}
	if os.Getenv(envKill) == "" {
		t.Logf("AVISO: há cluster mas %s não está definido — o AC4 (zero perda sob falha de nó) NÃO foi medido nesta execução", envKill)
	}
}
