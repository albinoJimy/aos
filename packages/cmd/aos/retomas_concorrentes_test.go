package main

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// DUAS RETOMAS CONCORRENTES NÃO DEIXAM FANTASMA.
//
// Achado 1.12 da varredura adversarial de 2026-08-21:
//
//	`delete` sem CAS e reposição incondicional do estado velho. O `GET /runs/{id}` responde
//	`waiting_on_human` para um run `complete`, e o `RunID` fica bloqueado. A única escotilha
//	exige estado durável `waiting_on_human` — o do fantasma é `complete`, logo responde 409.
//
// Afinação honesta do refutador, preservada aqui: o fantasma é PERMANENTE durante a vida do
// processo; um restart limpa-o. Não é corrupção durável — é um run que ninguém consegue destrancar
// sem reiniciar o nó.
//
// A MECÂNICA. Duas chamadas passam a verificação de suspensão com o MESMO `rs`. A primeira apaga,
// submete, e o run corre. A segunda apaga um nada, o `submit` recusa-a (o run já existe), e o ramo
// de erro REPÕE o `rs` velho por cima de um run que já avançou.
//
// A CORRECÇÃO é fazer da remoção uma RECLAMAÇÃO: quem não a reclamou não submete.
// ---------------------------------------------------------------------------------------------

// TestRetomasConcorrentesNaoDeixamFantasma dispara N retomas em paralelo sobre um run realmente
// suspenso e exige que, no fim, ou o run está a correr/terminado sem entrada de suspensão, ou
// ninguém o retomou — nunca as duas coisas.
//
// É um teste de CONCORRÊNCIA, no molde do TestExpireRouteConcurrentPassesDoNotDoubleSeal que já
// existe neste pacote. A sua força foi MEDIDA e está declarada no PR: sem a reclamação, a mutação
// tem de o fazer cair de forma repetível — um teste de corrida que não cai sobre o código partido
// não é um teste, é uma decoração.
func TestRetomasConcorrentesNaoDeixamFantasma(t *testing.T) {
	ctx := context.Background()
	h := newACNHarness(t)
	h.submete(t)
	if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
		t.Fatal("o run devia ter suspendido — o cenario nao esta montado")
	}
	h.aprovaPendente(t, "req-concorrente-1")

	// O ponteiro ANTES da retoma: e ele que um fantasma reporia.
	h.svc.mu.Lock()
	antes := h.svc.suspended[acnRunID]
	h.svc.mu.Unlock()
	if antes == nil {
		t.Fatal("o run devia estar no balde de suspensos antes da retoma")
	}

	const disparadas = 8
	tok := h.token(t)
	var wg sync.WaitGroup
	erros := make([]error, disparadas)
	arranca := make(chan struct{})
	for i := 0; i < disparadas; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-arranca // alinha o arranque para maximizar a sobreposicao
			erros[idx] = h.svc.Resume(ctx, acnRunID, tok)
		}(i)
	}
	close(arranca)
	wg.Wait()

	var okN, colisoes int
	for _, e := range erros {
		switch {
		case e == nil:
			okN++
		case errors.Is(e, ErrRunAlreadyInProgress):
			colisoes++
		}
	}

	// A ASSERÇÃO PRINCIPAL, e é DETERMINISTA: quem não reclamou a suspensão NÃO submete.
	//
	// Sem a reclamação, várias retomas apagam-a (um no-op para todas menos uma), seguem para o
	// `submit` e recebem [ErrRunAlreadyInProgress] — e é no ramo de erro dessas que o `rs` VELHO
	// era reposto por cima de um run que já avançou. Uma colisão aqui é a assinatura exacta do
	// caminho que produz o fantasma, e não depende de o apanhar antes de ser sobrescrito.
	if colisoes > 0 {
		t.Errorf("%d retoma(s) chegaram ao submit sem serem donas da transicao (ErrRunAlreadyInProgress) "+
			"— e no ramo de erro dessas que o estado velho e reposto por cima de um run que ja "+
			"avancou, que e como nasce o fantasma", colisoes)
	}
	// NÃO-VÁCUO: pelo menos uma retoma tem de ter passado. Sem este ramo, uma implementacao que
	// recusasse TODAS passaria na asercao seguinte.
	if okN == 0 {
		t.Fatalf("nenhuma das %d retomas passou: %v", disparadas, erros)
	}
	h.esperaFim(t, acnRunID)

	// A PROPRIEDADE, e o discriminador é a IDENTIDADE DO PONTEIRO.
	//
	// A primeira versão deste teste exigia o balde VAZIO — e falhava mesmo com a correcção, pela
	// razão certa: este run tem DOIS ciclos de aprovação, portanto volta a suspender no turno 2
	// legitimamente. A entrada que lá está não é um fantasma; é uma suspensão nova. Um teste que
	// não distingue as duas acusa o código de um defeito que ele não tem.
	//
	// Cada submissão cria um `runState` NOVO (ver `service.go`), pelo que uma suspensão legítima
	// traz um ponteiro DIFERENTE. O fantasma é, por construção, o ponteiro VELHO reposto.
	h.svc.mu.Lock()
	atual, presente := h.svc.suspended[acnRunID]
	h.svc.mu.Unlock()
	if presente && atual == antes {
		t.Errorf("FANTASMA: %d retoma(s) passaram e o balde de suspensos ainda tem o MESMO "+
			"runState de antes da retoma — e o estado velho reposto por cima de um run que ja "+
			"avancou. GET /runs/{id} responde waiting_on_human, e o RunID fica trancado ate o "+
			"processo reiniciar", okN)
	}
	// CONTROLO: e o run AVANÇOU mesmo. Sem este ramo, um run que nunca arrancou — e cujo balde
	// nunca mudou — passaria na asercao acima por não haver ponteiro nenhum a comparar.
	if !presente {
		return // retomou e concluiu: nao ha fantasma possivel
	}
	if atual == nil {
		t.Fatal("entrada de suspensao nula")
	}
}

// ---------------------------------------------------------------------------------------------
// A RECLAMAÇÃO, PROVADA SEM CORRIDA.
//
// O teste concorrente acima é uma SENTINELA, não uma prova: mediu-se, e não matou as mutações que
// removem o guarda por-run nem a que torna a reposição incondicional (10 corridas, 10 passagens).
// Apanhou o defeito UMA vez, o que basta para o valer como canário de regressão e não chega para
// afirmar que a propriedade está garantida.
//
// Estes dois provam-na de forma determinista.
// ---------------------------------------------------------------------------------------------

func TestReclamarRetomaDaATransicaoAUmSo(t *testing.T) {
	svc := &NodeService{}

	libertar, dono := svc.reclamarRetoma("run-x")
	if !dono {
		t.Fatal("a primeira reclamacao devia ser concedida")
	}
	if _, dono2 := svc.reclamarRetoma("run-x"); dono2 {
		t.Error("a SEGUNDA reclamacao do MESMO run foi concedida — duas retomas do mesmo run " +
			"chegam ambas ao submit, e e no ramo de erro da perdedora que nasce o fantasma")
	}
	// CONTROLO (1): outro run NÃO compete. Sem este ramo, um guarda global — que serializasse
	// TODAS as retomas do nó — passaria no teste acima e estrangularia o serviço.
	if _, outro := svc.reclamarRetoma("run-y"); !outro {
		t.Error("a reclamacao de OUTRO run foi recusada — retomas de runs distintos nao competem")
	}
	// CONTROLO (2): e liberta-se. Sem este ramo, um guarda que nunca soltasse passaria no teste
	// principal e tornaria o run IRRETOMÁVEL para sempre — trocar um fantasma por um bloqueio.
	libertar()
	if _, outra := svc.reclamarRetoma("run-x"); !outra {
		t.Error("depois de libertada, a reclamacao continuou recusada — o run ficou irretomavel")
	}
}

// TestOResumeUSAAReclamacao é a CABLAGEM, e prova-se sem corrida: o teste fica com a reclamação e
// exige que o `Resume` recuse.
//
// Sem ela, `reclamarRetoma` pode estar perfeita e o `Resume` não a chamar — que é exactamente o
// estado em que o código estava antes desta correcção.
func TestOResumeUSAAReclamacao(t *testing.T) {
	ctx := context.Background()
	h := newACNHarness(t)
	h.submete(t)
	if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
		t.Fatal("o run devia ter suspendido — o cenario nao esta montado")
	}
	h.aprovaPendente(t, "req-cablagem")

	// O TESTE fica com a transição. Um `Resume` que não consulte a reclamação não dá por isso.
	libertar, dono := h.svc.reclamarRetoma(acnRunID)
	if !dono {
		t.Fatal("o teste devia conseguir reclamar")
	}
	if err := h.svc.Resume(ctx, acnRunID, h.token(t)); !errors.Is(err, ErrRunNotSuspended) {
		t.Fatalf("com a transicao reclamada por outrem, o Resume devia recusar com "+
			"ErrRunNotSuspended; veio %v — o guarda existe e ninguem lhe pergunta", err)
	}
	// CONTROLO: libertada a reclamação, a MESMA chamada passa. Sem este ramo, um `Resume` que
	// recusasse SEMPRE passaria no teste acima.
	libertar()
	if err := h.svc.Resume(ctx, acnRunID, h.token(t)); err != nil {
		t.Fatalf("libertada a reclamacao, a retoma devia passar: %v", err)
	}
	h.esperaFim(t, acnRunID)
}
