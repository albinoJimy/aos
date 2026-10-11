package jetstream

// AOS-520 (revisão adversarial, ponto 4) — DEFEITO CONHECIDO DESTE BACKEND, reproduzido e ainda por
// corrigir. Ticket próprio por abrir; este ficheiro é a reprodução.
//
// O QUE ESTÁ MAL. O `Append` com `WithExpectedSeq(n)` compara `n` com a VISTA LOCAL do stream
// (`estado.aosSeq`), que só é lida do servidor uma vez (`hidratar`) e depois só avança com as
// escritas DESTE handle. Quando outro handle — outra réplica do nó — escreve no mesmo stream, a
// vista local fica atrás. Um chamador que faça concorrência optimista como deve (lê o stream do
// servidor com `Read`, e escreve com o `seq` que leu) apresenta então um `n` MAIOR do que a vista
// local, e o `Append` responde `ErrSeqConflict` sem publicar nada e SEM refrescar a vista. O
// chamador relê (o `Read` vai ao servidor, mas não toca na vista do `Append`), volta a apresentar
// o mesmo `n`, e recebe o mesmo erro — para sempre, até o processo reiniciar.
//
// QUEM É APANHADO. Todo o ciclo «ler, decidir, escrever com CAS» sobre um stream partilhado entre
// réplicas: o livro do canary do AOS-520 (`cmd/aos/modelo_canary_livro.go`) e a quota por principal
// (`cmd/aos/quota_por_principal.go`). Com duas réplicas, a que tem a vista atrasada esgota o tecto
// de tentativas em toda a escrita. Com uma só réplica não acontece: a vista é a única escritora.
//
// PORQUE É QUE ESTE TESTE AFIRMA O COMPORTAMENTO DE HOJE. O repositório não tem uma forma de marcar
// um teste como «esperado a falhar», e um teste saltado em silêncio não diz nada a ninguém. Este
// passa ENQUANTO O DEFEITO EXISTIR e descreve-o com números; quem o corrigir vai vê-lo avermelhar
// e troca as afirmações pelas do comportamento certo, que estão escritas no fim de cada bloco.
//
// O servidor é o de brincar deste pacote (`servidorEleicao`), que aceita qualquer publicação e diz
// sempre que o stream está vazio: chega, porque o defeito está na decisão que o `Append` toma
// ANTES de falar com o servidor.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

func TestAOS520_DefeitoConhecido_CASComAVistaLocalAtrasadaNaoPublicaNemRefresca(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS520FAKE"), ComPrazo(3*time.Second))
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	const stream = "aos-internal/livro-de-teste"
	facto := func(passo string) eventstore.EventInput {
		return eventstore.EventInput{Type: "model.canary.plan_assigned", Payload: []byte(`{}`), RunID: "aos-internal/livro", StepID: passo}
	}

	// ESTA réplica escreve o primeiro evento: a sua vista do stream fica em 1.
	if r, err := st.Append(ctx, stream, facto("um"), eventstore.WithExpectedSeq(0)); err != nil || r.Seq != 1 {
		t.Fatalf("primeira escrita: %+v %v", r, err)
	}
	publicadas := s.publicacoes()
	_, infosAntes := s.contagens()

	// OUTRA réplica escreveu o evento 2 (não passa por este handle). Esta réplica leu o stream do
	// servidor, viu o último seq a 2, e escreve com o seq que leu.
	for tentativa := 1; tentativa <= 8; tentativa++ {
		_, err := st.Append(ctx, stream, facto("tres"), eventstore.WithExpectedSeq(2))
		// HOJE: conflito, em todas as tentativas.
		if !errors.Is(err, eventstore.ErrSeqConflict) {
			t.Fatalf("tentativa %d: veio %v. Se este teste avermelhou porque o Append passou a refrescar a vista "+
				"antes de recusar, o defeito foi corrigido: troque as afirmacoes deste bloco pelas do comportamento certo "+
				"(ver o comentario no fim do teste)", tentativa, err)
		}
	}
	// HOJE: nenhuma das oito tentativas publicou, e nenhuma voltou a perguntar ao servidor pelo
	// estado do stream. A vista local continua em 1, e continuará até o processo reiniciar.
	if n := s.publicacoes(); n != publicadas {
		t.Fatalf("publicacoes = %d, eram %d: o Append com a vista atrasada chegou ao servidor (o defeito mudou de forma)", n, publicadas)
	}
	if _, infos := s.contagens(); infos != infosAntes {
		t.Fatalf("o Append voltou a perguntar ao servidor (%d INFO, eram %d): a vista passou a ser refrescada — o defeito foi corrigido, actualize este teste", infos, infosAntes)
	}

	// O COMPORTAMENTO CERTO, para quem corrigir: um `expected` à frente da vista local não é, por
	// si, um conflito — é a vista que pode estar atrás. O Append tem de reler o stream do servidor
	// antes de decidir (ou comparar o `expected` com o estado do servidor, e não com a memória), e
	// só então responder: aceitar, se o `expected` for o último seq do servidor; ErrSeqConflict ou
	// ErrAppendOnlyViolation, se não for. Com o servidor real: duas réplicas a escrever
	// alternadamente no mesmo stream, cada uma com o seq que acabou de ler, nunca ficam presas.
}
