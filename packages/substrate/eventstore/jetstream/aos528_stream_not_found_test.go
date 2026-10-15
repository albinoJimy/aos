package jetstream

// aos528_stream_not_found_test.go — a terceira face da janela do stream fresco (AOS-528).
//
// O AOS-455 fechou o INFO calado e o 503 no primeiro CAS. Ficou um terceiro desfecho da mesma
// janela, que o gate `nats` voltou a apanhar no PR #489 (run 38397155492, 3 de 10 repetições do
// `TestAOS100_NServeEmParaleloSobreOSubstratoReplicado`, sempre os três perdedores): o INFO a
// seguir a um CREATE ACEITE responde `stream not found` (code=404 err_code=10059). A fonte está
// no servidor (nats-server 2.10.29, `jsStreamInfoRequest`): o membro preferido de um grupo novo
// responde mesmo sem líder e, se ainda não criou o stream localmente, dorme 10 ms («This could
// be inflight») e devolve o 404. O `esperarLider` subia-o cru, e o perdedor saía `1` em vez de
// `3`. Contra o cluster local de 4 nós, com o código antes da correcção: 15 de 600 `Abrir`
// (4 ligações × 150 corridas) falharam assim.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// erro404 é o erro estruturado que o servidor real devolveu no CI (resposta literal).
var erro404 = &natsjs.JSError{Code: 404, ErrCode: natsjs.CodeStreamNotFound, Description: "stream not found"}

// TestAOS528_StreamNotFoundDepoisDoCreateRepergunta — o 404 da janela não sobe: re-pergunta-se
// com o intervalo de quem viu «ainda sem líder», e o líder anunciado à 3.ª consulta devolve.
func TestAOS528_StreamNotFoundDepoisDoCreateRepergunta(t *testing.T) {
	r := novoRelogio()
	consultas := 0
	err := esperarLider("S", func(time.Duration) (string, error) {
		consultas++
		if consultas < 3 {
			return "", erro404
		}
		return "aos-2", nil
	}, 10*time.Second, r.agora, r.dormir)
	if err != nil {
		t.Fatalf("com o stream servido à 3.ª consulta (as duas primeiras 404): %v — o 404 da janela subiu cru", err)
	}
	if consultas != 3 {
		t.Fatalf("consultas = %d, quer 3", consultas)
	}
	if len(r.dormiu) != 2 || r.dormiu[0] != intervaloLiderInicial {
		t.Fatalf("esperas = %v, quer duas a começar em %s — o 404 dorme como «ainda sem líder»", r.dormiu, intervaloLiderInicial)
	}
}

// TestAOS528_StreamNotFoundAtePrazoEsgotadoEIndisponibilidade — um stream que nunca chega a ser
// servido gasta o orçamento, e só ele, e sai INDISPONÍVEL: ErrNoQuorum + ErrStreamSemLider, com
// os 404 contados na mensagem. Nunca «pronto».
func TestAOS528_StreamNotFoundAtePrazoEsgotadoEIndisponibilidade(t *testing.T) {
	r := novoRelogio()
	inicio := r.agora()
	const prazo = 2 * time.Second
	err := esperarLider("S", func(time.Duration) (string, error) { return "", erro404 }, prazo, r.agora, r.dormir)
	if !errors.Is(err, eventstore.ErrNoQuorum) || !errors.Is(err, ErrStreamSemLider) {
		t.Fatalf("404 até ao fim: err=%v — quer ErrNoQuorum E ErrStreamSemLider", err)
	}
	if !strings.Contains(err.Error(), "stream not found") {
		t.Fatalf("a mensagem não conta os 404 — o operador não sabe que o stream não chegou a ser servido: %v", err)
	}
	if gasto := r.agora().Sub(inicio); gasto != prazo {
		t.Fatalf("desistiu ao fim de %s, quer exactamente o prazo %s", gasto, prazo)
	}
}

// TestAOS528_OutroErroEstruturadoSobeSemRetentar — só o 10059 é a janela. Outro erro do
// servidor (aqui, JetStream desligado para a conta) continua a subir à primeira e com a causa.
func TestAOS528_OutroErroEstruturadoSobeSemRetentar(t *testing.T) {
	r := novoRelogio()
	semJS := &natsjs.JSError{Code: 503, ErrCode: 10039, Description: "jetstream not enabled for account"}
	consultas := 0
	err := esperarLider("S", func(time.Duration) (string, error) {
		consultas++
		return "", semJS
	}, time.Second, r.agora, r.dormir)
	var js *natsjs.JSError
	if !errors.As(err, &js) || js.ErrCode != 10039 {
		t.Fatalf("a causa perdeu-se: %v", err)
	}
	if consultas != 1 {
		t.Fatalf("consultas = %d, quer 1 — só o «stream not found» da janela se re-pergunta", consultas)
	}
}

// TestAOS528_AbrirAtravessaOStreamNotFound — o caminho inteiro do `Abrir`, contra o servidor de
// brincar: CREATE aceite, dois INFO com 404, o terceiro com líder. Antes da correcção o `Abrir`
// falhava com «esperar pelo líder do stream …: stream not found», que é a linha do CI.
func TestAOS528_AbrirAtravessaOStreamNotFound(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	s.configurar(func(s *servidorEleicao) { s.info404 = 2 })
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS528FAKE"), ComPrazo(3*time.Second))
	if err != nil {
		t.Fatalf("Abrir com dois INFO 404 depois do CREATE aceite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if creates, infos := s.contagens(); creates != 1 || infos != 3 {
		t.Fatalf("CREATE=%d INFO=%d, quer 1 e 3 (dois 404 + o que anuncia o líder)", creates, infos)
	}
}
