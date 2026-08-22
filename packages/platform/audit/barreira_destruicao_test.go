package audit

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// UM LEGAL HOLD RESPONDIDO 200 PROTEGE — INCLUSIVE DURANTE A JANELA DO SELO.
//
// Achado da varredura adversarial de 2026-08-21, MEDIDO e não estimado:
//
//	selo do retention.expired : ~30 ms   ← janela held()→destruição, POR REGISTO
//	POST /dsar/hold           : 21–58 ms ← hold pedido, ainda NÃO em vigor
//
// As duas janelas compunham-se no pior sentido, porque o handler do hold SELA primeiro e só
// depois aplica. Demonstrado: hold selado no WORM, 200 devolvido ao operador, material destruído
// na mesma — e o relatório da passagem a declarar `Held=0`, AFIRMANDO que nenhuma preservação foi
// desrespeitada.
//
// A invariante que a barreira estabelece, e é o que estes testes exercem:
//
//	um 200 do /dsar/hold significa que NENHUMA destruição posterior deixa de ver este hold.
//
// Não promete que um registo já em destruição sobreviva. Promete que o operador não recebe a
// confirmação antes de isso ficar resolvido — uma preservação confirmada nunca chega tarde.
// ---------------------------------------------------------------------------------------------

// sinkBloqueante é um [ExpirationSink] que ANUNCIA quando entra e ESPERA por ordem para sair.
// É o que torna o teste determinista: em vez de correr atrás de uma janela de 30 ms, prende-a
// aberta e observa o que consegue (ou não consegue) entrar nela.
type sinkBloqueante struct {
	entrou   chan struct{}
	libertar chan struct{}
	apagados chan string
}

func novoSinkBloqueante() *sinkBloqueante {
	return &sinkBloqueante{
		entrou:   make(chan struct{}, 1),
		libertar: make(chan struct{}),
		apagados: make(chan string, 8),
	}
}

func (s *sinkBloqueante) Expire(_ context.Context, rec ExpirableRecord) error {
	s.entrou <- struct{}{}
	<-s.libertar
	s.apagados <- rec.ID
	return nil
}

func jobParaBarreira(t *testing.T, holds *LegalHold, sink ExpirationSink) *ExpirationJob {
	t.Helper()
	cfg, err := NewRetentionConfig("1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	agora := time.Unix(1_700_000_000, 0).UTC()
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "reg-1", Class: ClassDiagnostic, SubjectID: "nhi:titular-1", CreatedAt: agora.Add(-2 * time.Hour)},
	}}
	return NewExpirationJob(cfg, holds, src, sink,
		WithExpirationClock(func() time.Time { return agora }),
		WithExpirationAudit(NewMemStore(), DefaultRetentionPartition),
	)
}

// TestOHoldNAOConfirmaEnquantoHaDestruicaoEmVoo é o teste central.
//
// FALSIFICÁVEL, e é assim que se sabe que não é decorativo: sem a barreira,
// [LegalHold.BeginPlacement] devolve de imediato e o operador recebe a confirmação COM uma
// destruição a decorrer — que foi exactamente o que a varredura demonstrou.
func TestOHoldNAOConfirmaEnquantoHaDestruicaoEmVoo(t *testing.T) {
	holds := NewLegalHold()
	sink := novoSinkBloqueante()
	job := jobParaBarreira(t, holds, sink)

	varrimentoAcabou := make(chan struct{})
	go func() {
		defer close(varrimentoAcabou)
		_, _ = job.Run(context.Background())
	}()

	// Espera que uma destruição esteja MESMO em voo. Sem isto, o resto do teste mediria uma
	// corrida em vez de uma janela presa.
	select {
	case <-sink.entrou:
	case <-time.After(5 * time.Second):
		t.Fatal("o sink nunca foi chamado — o cenario nao esta montado e o resto seria vacuo")
	}

	holdConfirmado := make(chan struct{})
	go func() {
		defer close(holdConfirmado)
		fim := holds.BeginPlacement()
		holds.HoldSubject("nhi:titular-1")
		fim()
	}()

	// A PROPRIEDADE: com a destruição presa, a confirmação NÃO pode sair.
	select {
	case <-holdConfirmado:
		t.Fatal("o hold foi CONFIRMADO com uma destruicao em voo — o operador recebe um 200 e o " +
			"material morre na mesma, que e exactamente o defeito que esta barreira fecha")
	case <-time.After(250 * time.Millisecond):
		// Espera-se este ramo: bloqueado, como tem de estar. 250 ms é uma ordem de grandeza
		// acima dos ~30 ms da janela real.
	}

	close(sink.libertar)

	// CONTROLO: e depois de a destruição sair, a confirmação SAI. Sem este ramo, uma barreira que
	// nunca largasse passaria no teste acima — e um hold que nunca confirma é tão inútil como um
	// que confirma cedo de mais.
	select {
	case <-holdConfirmado:
	case <-time.After(5 * time.Second):
		t.Fatal("o hold NUNCA confirmou depois de a destruicao terminar — a barreira nao larga")
	}
	<-varrimentoAcabou
}

// TestOHoldConfirmaDeIMEDIATOQuandoNadaEstaAAcontecer é o CONTROLO da barreira inteira.
//
// Sem ele, a implementação mais simples que passa o teste acima é «bloquear sempre», e um legal
// hold que só confirma quando calha não serve a ninguém.
func TestOHoldConfirmaDeIMEDIATOQuandoNadaEstaAAcontecer(t *testing.T) {
	holds := NewLegalHold()

	pronto := make(chan struct{})
	go func() {
		defer close(pronto)
		fim := holds.BeginPlacement()
		holds.HoldSubject("nhi:titular-1")
		fim()
	}()

	select {
	case <-pronto:
	case <-time.After(2 * time.Second):
		t.Fatal("sem destruicao nenhuma em voo, o hold demorou mais de 2s a confirmar")
	}
	if !holds.HeldSubject("nhi:titular-1") {
		t.Fatal("o hold confirmou e NAO ficou em vigor")
	}
}

// TestAPassagemSeguinteRESPEITAOHoldQueEntrouPelaBarreira liga as duas pontas: não basta que a
// confirmação espere — o hold tem de VIGORAR para o que vier a seguir.
//
// É o ramo que apanharia uma barreira correcta a proteger uma aplicação que nunca acontecesse.
func TestAPassagemSeguinteRESPEITAOHoldQueEntrouPelaBarreira(t *testing.T) {
	holds := NewLegalHold()
	sink := novoSinkBloqueante()
	close(sink.libertar) // esta passagem não precisa de ficar presa

	fim := holds.BeginPlacement()
	holds.HoldSubject("nhi:titular-1")
	fim()

	job := jobParaBarreira(t, holds, sink)
	rep, err := job.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Held != 1 {
		t.Errorf("Held = %d, queria 1 — o hold confirmado nao suspendeu a expiracao", rep.Held)
	}
	if rep.Expired != 0 {
		t.Errorf("Expired = %d, queria 0 — material sob preservacao foi destruido", rep.Expired)
	}
	// CONTROLO: e o relatório NÃO pode declarar Held=0, que era a segunda metade do achado — a
	// passagem afirmava que nenhuma preservação tinha sido desrespeitada.
	select {
	case id := <-sink.apagados:
		t.Errorf("o sink apagou %q apesar do hold", id)
	default:
	}
}
