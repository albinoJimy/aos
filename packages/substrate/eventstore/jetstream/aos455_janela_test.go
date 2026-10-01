package jetstream

// aos455_janela_test.go — a janela entre o `STREAM.CREATE` e o stream SERVIDO, reproduzida
// de forma DETERMINISTA, sem cluster (AOS-455).
//
// O AOS-432 fez o `Abrir` esperar até o INFO anunciar líder. O gate `nats` continuou a flakear,
// e a medição contra um cluster real de quatro nós (nats-server 2.10.22, ver o ticket) mostrou
// DUAS formas da mesma janela, ambas com fonte no servidor:
//
//  1. O INFO SEM RESPOSTA NENHUMA. Logo depois de N `STREAM.CREATE` concorrentes, 32 de 400
//     `STREAM.INFO` ficaram sem resposta (e a consulta seguinte respondeu em 1–2 ms). O
//     `esperarLider` entregava o prazo INTEIRO a essa consulta: 10 s depois o `Abrir` falhava
//     com «indeterminado — sem resposta dentro do prazo», e o perdedor saía 1 em vez de 3.
//  2. O 503 DEPOIS DE HAVER LÍDER. O Raft diz-se líder (e o INFO anuncia-o) antes de o stream
//     subscrever os subjects (`processStreamLeaderChange` → `setLeader` → `subscribeToStream`
//     corre noutra goroutine). Em 100 corridas de 4 ligações, 2 receberam 503 no primeiro CAS
//     DEPOIS de o INFO anunciar líder — e o `Claim` devolvia esse 503 cru.
//
// O servidor de brincar de aos432_abrir_espera_test.go faz as duas coisas por contagem, o que
// torna a janela reprodutível em todas as execuções.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// TestAOS455_AbrirAtravessaUmINFOSemResposta — forma 1. O primeiro INFO depois do CREATE
// não tem resposta; o segundo anuncia líder. O `Abrir` tem de devolver o Store, e depressa:
// um INFO calado não pode gastar o orçamento inteiro.
func TestAOS455_AbrirAtravessaUmINFOSemResposta(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	s.configurar(func(s *servidorEleicao) { s.silencioInfo = 1 })
	const prazo = 3 * time.Second
	inicio := time.Now()
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS455FAKE"), ComPrazo(prazo))
	gasto := time.Since(inicio)
	if err != nil {
		t.Fatalf("Abrir com o 1.º INFO calado e líder no 2.º falhou ao fim de %s: %v — um INFO sem resposta gastou o prazo inteiro", gasto, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, infos := s.contagens(); infos != 2 {
		t.Fatalf("INFO = %d, quer 2 (o calado + o que anuncia o líder)", infos)
	}
	if gasto > prazo/2 {
		t.Fatalf("o Abrir levou %s com prazo %s — o INFO calado consumiu o orçamento em vez de ser re-perguntado", gasto, prazo)
	}
}

// TestAOS455_503NoPrimeiroCASDeUmStreamProprioReTenta — forma 2. O INFO anuncia líder, o
// primeiro PUB recebe 503 e o segundo é aceite. O 503 é a resposta do servidor a dizer que
// NINGUÉM recebeu a escrita — nada ficou durável — e o stream é o que este Store criou, logo
// o subject é dele. A escrita tem de passar, e com UMA só mensagem no log.
func TestAOS455_503NoPrimeiroCASDeUmStreamProprioReTenta(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	s.configurar(func(s *servidorEleicao) { s.pub503 = 1 })
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS455FAKE"), ComPrazo(3*time.Second))
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	r, err := st.Append(context.Background(), "lease:run-455", eventstore.EventInput{
		Type: "lease.claimed", Payload: []byte(`{}`), RunID: "run-455", StepID: "claim-1",
	}, eventstore.WithExpectedSeq(0))
	if err != nil {
		t.Fatalf("Append no primeiro CAS com um 503 de janela: %v — o 503 subiu cru (o Claim devolvia-o e o perdedor saía 1)", err)
	}
	if r.Status != eventstore.StatusCommitted || r.Seq != 1 {
		t.Fatalf("Append = %+v, quer committed com seq 1", r)
	}
	if n := s.publicacoes(); n != 2 {
		t.Fatalf("publicações = %d, quer 2 (a do 503 + a aceite)", n)
	}
}

// TestAOS455_503DeUmSubjectQueOStreamNaoCapturaNaoSeReTenta — o 503 que NÃO é a janela.
// Um Store que não criou o stream e cujo prefixo o stream não captura publica para o vazio:
// é o 503 pela razão que o 503 existe para dizer. Sai na hora, com a causa, sem re-tentar e
// sem se fazer passar por indisponibilidade transitória — o critério 4 do AOS-432, um nível
// abaixo do `integration`.
func TestAOS455_503DeUmSubjectQueOStreamNaoCapturaNaoSeReTenta(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	s.configurar(func(s *servidorEleicao) {
		s.pub503 = -1
		s.subjects = []string{"aos.es.aos455fake.>"}
	})
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS455FAKE"), SemCriarStream(),
		ComPrefixoDeSubject("aos.es.orfao"), ComPrazo(3*time.Second))
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	inicio := time.Now()
	_, err = st.Append(context.Background(), "lease:run-455", eventstore.EventInput{
		Type: "lease.claimed", Payload: []byte(`{}`), RunID: "run-455", StepID: "claim-1",
	}, eventstore.WithExpectedSeq(0))
	if !errors.Is(err, natsjs.ErrNoResponders) {
		t.Fatalf("Append = %v, quer natsjs.ErrNoResponders (a causa real)", err)
	}
	if errors.Is(err, eventstore.ErrNoQuorum) {
		t.Fatalf("um 503 de um subject fora do stream saiu como indisponibilidade transitória: %v", err)
	}
	if n := s.publicacoes(); n != 1 {
		t.Fatalf("publicações = %d, quer 1 — um 503 que não é a janela não se re-tenta", n)
	}
	if gasto := time.Since(inicio); gasto > time.Second {
		t.Fatalf("o 503 verdadeiro levou %s a subir — foi re-tentado", gasto)
	}
}

// TestAOS455_JanelaQueNaoFechaSaiComErroNomeado — o 503 que nunca passa, sobre o stream que
// este Store criou. Re-tenta-se dentro do prazo e, esgotado, sai um erro de TRANSPORTE com nome
// próprio: ErrNoQuorum + ErrStreamNaoServe, com o 503 na cadeia. Nunca sucesso silencioso, e
// nunca um conflito (que o `Claim` leria como «outro venceu»).
func TestAOS455_JanelaQueNaoFechaSaiComErroNomeado(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	s.configurar(func(s *servidorEleicao) { s.pub503 = -1 })
	const prazo = 400 * time.Millisecond
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS455FAKE"), ComPrazo(prazo))
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	inicio := time.Now()
	r, err := st.Append(context.Background(), "lease:run-455", eventstore.EventInput{
		Type: "lease.claimed", Payload: []byte(`{}`), RunID: "run-455", StepID: "claim-1",
	}, eventstore.WithExpectedSeq(0))
	gasto := time.Since(inicio)
	if err == nil {
		t.Fatalf("Append sobre um stream que nunca serve devolveu sucesso: %+v", r)
	}
	if !errors.Is(err, eventstore.ErrNoQuorum) || !errors.Is(err, ErrStreamNaoServe) || !errors.Is(err, natsjs.ErrNoResponders) {
		t.Fatalf("Append = %v — quer ErrNoQuorum E ErrStreamNaoServe E natsjs.ErrNoResponders na cadeia", err)
	}
	if errors.Is(err, eventstore.ErrSeqConflict) || errors.Is(err, eventstore.ErrAppendOnlyViolation) {
		t.Fatalf("a indisponibilidade saiu como CONFLITO — o Claim lê-la-ia como «outro venceu»: %v", err)
	}
	if n := s.publicacoes(); n < 2 {
		t.Fatalf("publicações = %d — a janela não foi re-tentada", n)
	}
	if gasto > prazo+prazo/2+300*time.Millisecond {
		t.Fatalf("a janela durou %s com prazo %s — o limite não é o prazo da operação", gasto, prazo)
	}
}

// TestAOS455_ComStreamAlheioQueCapturaOSubjectReTenta — um Store aberto com SemCriarStream
// não sabe o que o stream captura e PERGUNTA: com a configuração armazenada a capturar o
// subject, o 503 é a janela e re-tenta-se.
func TestAOS455_ComStreamAlheioQueCapturaOSubjectReTenta(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	s.configurar(func(s *servidorEleicao) {
		s.pub503 = 2
		s.subjects = []string{"aos.es.*.>"}
	})
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS455FAKE"), SemCriarStream(), ComPrazo(3*time.Second))
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	r, err := st.Append(context.Background(), "run-455", eventstore.EventInput{
		Type: "x", Payload: []byte(`{}`), RunID: "run-455", StepID: "p-1",
	})
	if err != nil || r.Status != eventstore.StatusCommitted {
		t.Fatalf("Append = %+v, %v — quer committed depois de dois 503 de janela", r, err)
	}
	if n := s.publicacoes(); n != 3 {
		t.Fatalf("publicações = %d, quer 3", n)
	}
}

// TestAOS455_ContextoCanceladoInterrompeAJanela — a espera da janela obedece ao ctx: um
// chamador que desiste não fica preso ao prazo do store.
func TestAOS455_ContextoCanceladoInterrompeAJanela(t *testing.T) {
	s := arrancarServidorEleicao(t, 0)
	s.configurar(func(s *servidorEleicao) { s.pub503 = -1 })
	st, err := Abrir(s.ln.Addr().String(), ComNomeDeStream("AOS455FAKE"), ComPrazo(10*time.Second))
	if err != nil {
		t.Fatalf("Abrir: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancelar := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelar()
	inicio := time.Now()
	_, err = st.Append(ctx, "run-455", eventstore.EventInput{Type: "x", Payload: []byte(`{}`)})
	if err == nil {
		t.Fatal("Append sobre um stream que nunca serve devolveu sucesso")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrStreamNaoServe) {
		t.Fatalf("Append com ctx de 300 ms = %v — quer o prazo do ctx", err)
	}
	if gasto := time.Since(inicio); gasto > 2*time.Second {
		t.Fatalf("a janela ignorou o ctx: %s", gasto)
	}
}

// TestAOS455_SubjectCasa — a regra de curingas do NATS, que decide se um 503 é a janela.
func TestAOS455_SubjectCasa(t *testing.T) {
	for _, c := range []struct {
		filtro, subject string
		quer            bool
	}{
		{"aos.es.x.>", "aos.es.x.run-1", true},
		{"aos.es.x.>", "aos.es.x.lease:run-1", true},
		{"aos.es.x.>", "aos.es.x", false},
		{"aos.es.x.>", "aos.es.y.run-1", false},
		{"aos.es.*.>", "aos.es.y.run-1", true},
		{"aos.*.x", "aos.es.x", true},
		{"aos.*.x", "aos.es.x.run", false},
		{"aos.es.x", "aos.es.x", true},
		{"aos.es.x", "aos.es", false},
		{">", "a", true},
	} {
		if got := subjectCasa(c.filtro, c.subject); got != c.quer {
			t.Errorf("subjectCasa(%q, %q) = %v, quer %v", c.filtro, c.subject, got, c.quer)
		}
	}
}
