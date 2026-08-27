package durable

// A CHAVE É POSICIONAL — A IMPRESSÃO DIGITAL É QUE DIZ SE É A MESMA ACÇÃO.
//
// A chave de idempotência é `f(run_id, step_id)` e o step_id é função pura de (turno, índice):
// nada da acção entra nele. A premissa de [StepSequencer.StepID] — «o mesmo passo lógico recebe
// SEMPRE o mesmo step_id» — é falsa numa retoma em que o turno não tenha captura: o modelo é
// re-interrogado ao vivo, pode emitir OUTRA tool call, e essa call recebe o step_id já aplicado.
//
// Sem a impressão, o dedup devolvia-lhe o resultado da acção ANTERIOR: a acção nova nunca
// executava, nunca passava pelo Reference Monitor, e o laço não distinguia.
//
// A chave NÃO muda — [IdempotencyKey] é uma bijecção declarada com [SplitKey] como inversa, e a
// forma está fixada no ADR-001. O que muda é o que se COMPARA antes de devolver o memorizado. É o
// mesmo desenho que o `mesmoGrant` do four-eyes já usa uma camada acima.

import (
	"context"
	"errors"
	"testing"
)

// ledgerDeTeste constrói um ledger sobre um store em memória local a este pacote.
func ledgerDeTeste(t *testing.T) *StepLedger {
	t.Helper()
	l, err := NewStepLedger(newStore(t))
	if err != nil {
		t.Fatalf("compor o ledger: %v", err)
	}
	return l
}

func efeitoQueDevolve(payload string) func(context.Context) (Result, error) {
	return func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte(payload)}, nil
	}
}

// TestImpressao_MesmaAccaoDeduplicaNormalmente é a propriedade que NÃO pode regredir: a
// idempotência tem de continuar a funcionar. Sem este caso, uma correcção que recusasse tudo
// passaria o teste do defeito e transformaria dedup em re-execução de efeitos externos.
func TestImpressao_MesmaAccaoDeduplicaNormalmente(t *testing.T) {
	l := ledgerDeTeste(t)
	const chave, impressao = "run-1:step-000001-tool-1", "sha256:accao-A"

	if _, aplicou, err := l.Apply(context.Background(), chave, efeitoQueDevolve("primeiro"), WithActionFingerprint(impressao)); err != nil || !aplicou {
		t.Fatalf("a primeira aplicacao devia correr o efeito: aplicou=%v err=%v", aplicou, err)
	}

	res, aplicou, err := l.Apply(context.Background(), chave, efeitoQueDevolve("SEGUNDO"), WithActionFingerprint(impressao))
	if err != nil {
		t.Fatalf("a MESMA accao tem de deduplicar, nao recusar: %v", err)
	}
	if aplicou {
		t.Error("a MESMA accao correu o efeito outra vez — a idempotencia partiu-se")
	}
	if string(res.Payload) != "primeiro" {
		t.Errorf("o dedup devia devolver o resultado memorizado, veio %q", res.Payload)
	}
}

// TestImpressao_AccaoDiferenteERecusada é o defeito. A segunda submissão traz OUTRA acção com o
// mesmo step_id — o cenário da retoma com re-interrogação do modelo.
func TestImpressao_AccaoDiferenteERecusada(t *testing.T) {
	l := ledgerDeTeste(t)
	const chave = "run-1:step-000001-tool-1"

	if _, _, err := l.Apply(context.Background(), chave, efeitoQueDevolve("resultado da accao A"), WithActionFingerprint("sha256:accao-A")); err != nil {
		t.Fatalf("primeira aplicacao: %v", err)
	}

	res, aplicou, err := l.Apply(context.Background(), chave, efeitoQueDevolve("nunca corre"), WithActionFingerprint("sha256:accao-B"))

	if !errors.Is(err, ErrActionMismatch) {
		t.Fatalf("uma accao DIFERENTE com o mesmo step_id tem de ser RECUSADA — senao recebe o "+
			"resultado da accao anterior sem executar e sem passar pelo RM.\nerr=%v aplicou=%v payload=%q",
			err, aplicou, res.Payload)
	}
	if string(res.Payload) != "" {
		t.Errorf("a recusa nao pode devolver payload nenhum, veio %q", res.Payload)
	}
}

// TestImpressao_AusenteEAceite é o residual DECLARADO: entradas anteriores a este campo não têm
// impressão, e a ausência é «não verificável», nunca «diferente».
//
// Recusar aqui fecharia a janela por completo mas tornaria IRRETOMÁVEL qualquer run cujo ledger
// seja anterior à mudança — trocaria uma correcção de segurança por perda de disponibilidade.
func TestImpressao_AusenteEAceite(t *testing.T) {
	t.Run("registo antigo sem impressao", func(t *testing.T) {
		l := ledgerDeTeste(t)
		const chave = "run-1:step-000001-tool-1"

		// Primeira aplicação SEM impressão — simula uma entrada anterior à mudança.
		if _, _, err := l.Apply(context.Background(), chave, efeitoQueDevolve("legado")); err != nil {
			t.Fatalf("primeira aplicacao: %v", err)
		}

		res, _, err := l.Apply(context.Background(), chave, efeitoQueDevolve("x"), WithActionFingerprint("sha256:accao-B"))
		if err != nil {
			t.Fatalf("uma entrada SEM impressao nao pode recusar — os runs antigos ficariam "+
				"irretomaveis: %v", err)
		}
		if string(res.Payload) != "legado" {
			t.Errorf("devia devolver o memorizado, veio %q", res.Payload)
		}
	})

	t.Run("chamador que nao declara accao", func(t *testing.T) {
		l := ledgerDeTeste(t)
		const chave = "run-1:step-000001-tool-1"

		if _, _, err := l.Apply(context.Background(), chave, efeitoQueDevolve("A"), WithActionFingerprint("sha256:accao-A")); err != nil {
			t.Fatalf("primeira aplicacao: %v", err)
		}

		// A saga e outros chamadores não passam impressão. Não podem partir.
		if _, _, err := l.Apply(context.Background(), chave, efeitoQueDevolve("x")); err != nil {
			t.Fatalf("um chamador que nao declara accao tem de continuar a funcionar: %v", err)
		}
	})
}
