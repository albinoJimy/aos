package integration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// UM PENDENTE QUE EXPIRA E VOLTA A ESCALAR TEM DE VOLTAR À LISTA.
//
// Observado em produção a 2026-08-19: um run escalou, o pendente expirou sem decisão (TTL 15m),
// o run foi retomado, a acção escalou OUTRA VEZ — e nada apareceu na lista do operador. O run
// esteve 10 minutos em `waiting_on_human` com o stream `gov.approvals` a mostrar «expirado» como
// último estado. A cerimónia four-eyes ainda funcionou, porque o grant se liga à acção pela
// PREVIEW e não pelo pendente; mas quem vigia pendências não via o que estava à espera.
//
// Duas causas que se compunham, e é por isso que os testes abaixo são dois:
//
//	1. o Event Store deduplica por (run_id, step_id) e o segundo anúncio usava a MESMA chave —
//	   era engolido em silêncio;
//	2. a listagem retirava por CONJUNTO de chaves, sem ordem: uma vez expirada, aquela chave
//	   ficava escondida para sempre, mesmo que (1) fosse resolvido.
// ---------------------------------------------------------------------------

// pendentesDeTeste reutiliza o fixture existente, que monta o registo sobre um Event Store REAL.
// Um duplo não serviria: a deduplicação por idempotency_key e a ordem do stream são exactamente
// o que estes testes exercitam.
func pendentesDeTeste(t *testing.T) *PendingApprovals {
	t.Helper()
	p, _ := pendingFixture(t)
	return p
}

func registoDeTeste(run, step string) PendingRecord {
	return PendingRecord{
		RunID: run, StepID: step,
		Preview:   []byte("preview-" + step),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// TestPendenteVoltaAListaDepoisDeExpirar é o cenário de produção, reproduzido.
func TestPendenteVoltaAListaDepoisDeExpirar(t *testing.T) {
	ctx := context.Background()
	p := pendentesDeTeste(t)
	rec := registoDeTeste("run-1", "step-1")

	if err := p.Put(ctx, rec); err != nil {
		t.Fatalf("primeiro anuncio: %v", err)
	}
	if n := len(listar(t, p, "run-1")); n != 1 {
		t.Fatalf("depois do primeiro anuncio a lista tem %d, quero 1", n)
	}

	if err := p.Expire(ctx, "run-1", "step-1"); err != nil {
		t.Fatalf("expirar: %v", err)
	}
	// CONTROLO — depois de expirar, a lista fica vazia. Sem este ramo, o teste seguinte
	// passaria mesmo que a expiração nunca tivesse funcionado.
	if n := len(listar(t, p, "run-1")); n != 0 {
		t.Fatalf("depois de expirar a lista tem %d, quero 0", n)
	}

	// A RE-ESCALADA: o run foi retomado e a mesma acção voltou a exigir um humano.
	if err := p.Put(ctx, rec); err != nil {
		t.Fatalf("re-anuncio: %v", err)
	}
	pendentes := listar(t, p, "run-1")
	if len(pendentes) != 1 {
		t.Fatalf("depois da RE-ESCALADA a lista tem %d, quero 1 — o run fica a espera de um "+
			"humano e o operador nao ve nada para decidir", len(pendentes))
	}
	if pendentes[0].StepID != "step-1" {
		t.Errorf("o pendente devolvido e de outro passo: %q", pendentes[0].StepID)
	}
}

// TestSegundaExpiracaoTambemRetira — a correcção não pode valer só uma vez.
//
// Se a expiração continuasse a usar a chave da PRIMEIRA encarnação, o segundo `expired` seria ele
// próprio deduplicado e a segunda encarnação nunca sairia da lista: o defeito de origem, um nível
// abaixo, e mais difícil de ver.
func TestSegundaExpiracaoTambemRetira(t *testing.T) {
	ctx := context.Background()
	p := pendentesDeTeste(t)
	rec := registoDeTeste("run-2", "step-1")

	for volta := 1; volta <= 3; volta++ {
		if err := p.Put(ctx, rec); err != nil {
			t.Fatalf("volta %d, anuncio: %v", volta, err)
		}
		if n := len(listar(t, p, "run-2")); n != 1 {
			t.Fatalf("volta %d: lista tem %d apos anunciar, quero 1", volta, n)
		}
		if err := p.Expire(ctx, "run-2", "step-1"); err != nil {
			t.Fatalf("volta %d, expirar: %v", volta, err)
		}
		if n := len(listar(t, p, "run-2")); n != 0 {
			t.Fatalf("volta %d: lista tem %d apos expirar, quero 0 — a expiracao desta "+
				"encarnacao nao mordeu", volta, n)
		}
	}
}

// TestAnuncioRepetidoNaMesmaEncarnacaoNaoDuplica é o controlo que impede a correcção de destruir
// o que a deduplicação protegia.
//
// O Reference Monitor pode anunciar o mesmo pendente mais do que uma vez dentro da MESMA
// escalada (retentativas, reprodução do turno). Essas continuam a colapsar num só: se cada
// chamada criasse uma encarnação, a lista do operador encheria-se de cópias da mesma decisão.
func TestAnuncioRepetidoNaMesmaEncarnacaoNaoDuplica(t *testing.T) {
	ctx := context.Background()
	p, es := pendingFixture(t)
	rec := registoDeTeste("run-3", "step-1")

	for i := 0; i < 4; i++ {
		if err := p.Put(ctx, rec); err != nil {
			t.Fatalf("anuncio %d: %v", i, err)
		}
	}

	// A verificação é sobre o STREAM, e não sobre a listagem. A primeira versão deste teste
	// contava entradas de `ListForRun` e era VÁCUA: a listagem colapsa duplicados por
	// `chaveDeDeduplicacao`, que NÃO inclui a geração — portanto dava 1 mesmo com o Put a criar
	// uma encarnação por chamada. A mutação apanhou-o; a releitura não teria apanhado.
	events, err := readApprovalStream(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == approvalPendingEventType {
			n++
		}
	}
	if n != 1 {
		t.Errorf("quatro anuncios da MESMA escalada gravaram %d eventos, quero 1 — o log encheria "+
			"de copias da mesma decisao e a deduplicacao que existia teria sido destruida", n)
	}
	// E a listagem continua a mostrar UM.
	if l := len(listar(t, p, "run-3")); l != 1 {
		t.Errorf("a lista do operador tem %d, quero 1", l)
	}
}

// TestPrimeiraEncarnacaoMantemAChaveDeSempre — a chave durável da geração ZERO fica
// BYTE-IDÊNTICA à de antes desta correcção.
//
// O comentário de `chaveDeDeduplicacao` exige-o explicitamente: reescrever a chave durável
// partiria a deduplicação e a expiração de TUDO o que já está no log — incluindo os pendentes
// que este nó já selou em produção.
func TestPrimeiraEncarnacaoMantemAChaveDeSempre(t *testing.T) {
	for _, k := range []struct {
		kind             PendingKind
		run, step, quero string
	}{
		{PendingKindApproval, "run-x", "step-y", "pending-run-x-step-y"},
		{PendingKindExhaustion, "run-x", "step-y", "pending-kind=exhaustion|run-x-step-y"},
	} {
		got := chaveDeGeracao("pending-", k.kind, k.run, k.step, 0)
		if got != k.quero {
			t.Errorf("geracao 0 (%s) deu %q, tinha de ser %q — o log existente deixaria de casar",
				k.kind, got, k.quero)
		}
		// E a geração seguinte TEM de diferir, senão não haveria encarnação nenhuma.
		if g1 := chaveDeGeracao("pending-", k.kind, k.run, k.step, 1); g1 == got || !strings.HasPrefix(g1, got) {
			t.Errorf("geracao 1 (%s) = %q — tem de ser distinta e derivada da base %q", k.kind, g1, got)
		}
	}
}

func listar(t *testing.T, p *PendingApprovals, run string) []PendingRecord {
	t.Helper()
	recs, err := p.ListForRun(context.Background(), run)
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	return recs
}
