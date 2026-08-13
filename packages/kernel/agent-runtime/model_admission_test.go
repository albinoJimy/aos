package agentruntime

// AOS-260 — o GANCHO da admissão do turno de modelo, no ponto exacto onde tem de estar.
//
// O que se sela aqui é o SEAM e a sua ORDEM, não a política de orçamento (essa vive no
// adaptador, em `integration`): que a admissão corre ANTES da chamada ao modelo e o saldo
// DEPOIS dela com o consumo REAL; que uma negação PÁRA o run UMA VEZ (nunca um deny-loop);
// que uma falha do modelo LIBERTA a provisão; e que sem porta ligada o comportamento de
// AOS-013 é byte-idêntico.

import (
	"context"
	"errors"
	"testing"
)

// fakeAdmission é uma [ModelAdmission] determinista que grava a sequência de eventos por que
// passou. A sequência é o objecto do teste: é ela que prova a ORDEM.
type fakeAdmission struct {
	eventos   []string
	pedidos   []TurnAdmissionRequest
	saldos    []TurnSettlement
	negarNo   int // turno a partir do qual nega (0 = nunca)
	razao     string
	erroNo    int // turno a partir do qual AdmitTurn devolve erro (0 = nunca)
	erro      error
	replayAte int   // turnos <= replayAte são reportados como reproduzidos
	erroSaldo error // erro devolvido por SettleTurn (sempre, se != nil)
}

func (f *fakeAdmission) AdmitTurn(_ context.Context, req TurnAdmissionRequest) (TurnAdmissionVerdict, error) {
	f.eventos = append(f.eventos, "admit")
	f.pedidos = append(f.pedidos, req)
	if f.erro != nil && f.erroNo != 0 && req.Turn >= f.erroNo {
		return TurnAdmissionVerdict{}, f.erro
	}
	if req.Turn <= f.replayAte {
		return TurnAdmissionVerdict{Admitted: true, AlreadyAdmitted: true}, nil
	}
	if f.negarNo != 0 && req.Turn >= f.negarNo {
		return TurnAdmissionVerdict{Reason: f.razao}, nil
	}
	return TurnAdmissionVerdict{Admitted: true}, nil
}

func (f *fakeAdmission) SettleTurn(_ context.Context, s TurnSettlement) error {
	f.eventos = append(f.eventos, "settle")
	f.saldos = append(f.saldos, s)
	return f.erroSaldo
}

// modeloComUso devolve uma resposta final com usage e custo conhecidos, gravando o momento da
// chamada na sequência partilhada — é assim que a ORDEM admit→call→settle fica provada sem
// depender de mocks que se observam uns aos outros.
func modeloComUso(f *fakeAdmission, in, out, custo int64) ModelClientFunc {
	return func(context.Context, PromptView) (ModelResponse, error) {
		f.eventos = append(f.eventos, "call")
		return ModelResponse{
			Text:         "pronto",
			Final:        true,
			Usage:        Usage{InputTokens: in, OutputTokens: out},
			CostMicroUSD: custo,
		}, nil
	}
}

// TestAOS260_ReservaAntesDaChamadaESaldoDepois é o critério de aceitação 1 no seu ponto mais
// simples: a ordem é admit → call → settle, e o saldo leva o consumo MEDIDO da resposta (não
// a estimativa, não zero).
func TestAOS260_ReservaAntesDaChamadaESaldoDepois(t *testing.T) {
	h := newHarness(t, nil)
	fa := &fakeAdmission{}
	rt := New(modeloComUso(fa, 1200, 340, 8700), h.rm, h.recorder, WithModelAdmission(fa))

	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("res=%+v", res)
	}
	if got := fa.eventos; len(got) != 3 || got[0] != "admit" || got[1] != "call" || got[2] != "settle" {
		t.Fatalf("a ordem tem de ser admit→call→settle (a reserva vale de nada DEPOIS do gasto, e o saldo de nada ANTES da medicao), got %v", got)
	}
	if len(fa.saldos) != 1 {
		t.Fatalf("saldos=%v", fa.saldos)
	}
	s := fa.saldos[0]
	if s.Usage.InputTokens != 1200 || s.Usage.OutputTokens != 340 || s.CostMicroUSD != 8700 {
		t.Errorf("o saldo tem de levar o consumo MEDIDO da resposta; got %+v", s)
	}
	if s.Failed {
		t.Error("o turno correu bem: Failed tem de ser false")
	}
	// A admissão vê a chave de dedup e o prompt materializado — sem eles o adaptador não
	// consegue nem deduplicar nem estimar.
	req := fa.pedidos[0]
	if req.RunID == "" || req.StepID == "" || req.Turn != 1 || len(req.View.Materialized) == 0 {
		t.Errorf("o pedido de admissao tem de transportar run_id, step_id, turno e o prompt MATERIALIZADO; got %+v", req)
	}
	if req.StepID != s.StepID || req.RunID != s.RunID {
		t.Errorf("a chave do saldo tem de ser a MESMA da admissao (senao salda-se a reserva de outro turno): admit=%s:%s settle=%s:%s", req.RunID, req.StepID, s.RunID, s.StepID)
	}
}

// TestAOS260_NegacaoParaORunUmaVezSemDenyLoop é o critério 2 no kernel: negar NÃO é retentar.
// O modelo é chamado nos turnos admitidos e NUNCA no turno negado; o run pára com razão
// própria, sem erro, e a admissão é consultada exactamente uma vez nesse turno.
func TestAOS260_NegacaoParaORunUmaVezSemDenyLoop(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fa := &fakeAdmission{negarNo: 3, razao: "orcamento do run esgotado"}
	chamadas := 0
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		chamadas++
		return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
	})
	rt := New(model, h.rm, h.recorder, WithModelAdmission(fa), WithMaxTurns(20))

	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("uma negacao de orcamento NAO e um erro do loop (e uma degradacao declarada): %v", err)
	}
	if !res.BudgetExhausted {
		t.Fatal("res.BudgetExhausted tem de ficar true — sem ele o desfecho seria indistinguivel de uma paragem qualquer")
	}
	if res.BudgetExhaustionReason != "orcamento do run esgotado" {
		t.Errorf("a razao da porta tem de chegar ao Result (e ela que torna a paragem atribuivel): %q", res.BudgetExhaustionReason)
	}
	if res.Turns != 2 {
		t.Errorf("Turns tem de contar os turnos COMPLETOS (o negado nao chegou a existir): got %d, want 2", res.Turns)
	}
	if chamadas != 2 {
		t.Errorf("o modelo nao pode ser chamado no turno negado: got %d chamadas, want 2", chamadas)
	}
	// O DENY-LOOP seria exactamente isto: a admissão consultada N vezes no mesmo turno (ou o
	// loop a continuar até MaxTurns=20 a bater na mesma parede). Três consultas = turnos 1, 2
	// e o negado — e mais nenhuma.
	admits := 0
	for _, e := range fa.eventos {
		if e == "admit" {
			admits++
		}
	}
	if admits != 3 {
		t.Errorf("a admissao tem de ser consultada UMA vez por turno e o loop tem de parar na primeira negacao: got %d admits, want 3", admits)
	}
	if len(fa.saldos) != 2 {
		t.Errorf("so os turnos que chamaram o modelo se saldam (o negado nao reservou nada): got %d", len(fa.saldos))
	}
}

// TestAOS260_TurnoReproduzidoNaoSeSalda: quando a porta admite com AlreadyAdmitted (replay),
// nada foi reservado — e saldar seria libertar/confirmar a reserva de OUTRO turno. O loop
// tem de calar o saldo.
func TestAOS260_TurnoReproduzidoNaoSeSalda(t *testing.T) {
	h := newHarness(t, nil)
	fa := &fakeAdmission{replayAte: 1}
	rt := New(modeloComUso(fa, 10, 10, 100), h.rm, h.recorder, WithModelAdmission(fa))

	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fa.saldos) != 0 {
		t.Fatalf("um turno REPRODUZIDO nao reservou nada, logo nao ha o que saldar: got %+v", fa.saldos)
	}
	if got := fa.eventos; len(got) != 2 || got[0] != "admit" || got[1] != "call" {
		t.Fatalf("esperava admit→call sem settle, got %v", got)
	}
}

// TestAOS260_FalhaDoModeloLibertaAProvisao: o saldo corre TAMBÉM no caminho de erro, com
// Failed=true. Sem isto, um provider intermitente esgotava o tecto de um run com reservas
// que ninguém saldou — e o run seria negado por um consumo que nunca existiu.
func TestAOS260_FalhaDoModeloLibertaAProvisao(t *testing.T) {
	h := newHarness(t, nil)
	fa := &fakeAdmission{}
	falha := errors.New("provider indisponivel")
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		fa.eventos = append(fa.eventos, "call")
		return ModelResponse{}, falha
	})
	rt := New(model, h.rm, h.recorder, WithModelAdmission(fa))

	_, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, ErrModelCall) {
		t.Fatalf("o erro do modelo tem de continuar a subir tal-qual: %v", err)
	}
	if len(fa.saldos) != 1 || !fa.saldos[0].Failed {
		t.Fatalf("a provisao tem de ser saldada com Failed=true no caminho de erro: %+v", fa.saldos)
	}
	if got := fa.eventos; len(got) != 3 || got[2] != "settle" {
		t.Fatalf("esperava admit→call→settle mesmo em falha, got %v", got)
	}
}

// TestAOS260_ErroDaPortaEFatalENaoUmaNegacao: a distinção que o contrato faz. Uma FALHA da
// admissão (≠ negação) é cegueira do tecto e aborta o run — nunca se interpreta como «não
// há orçamento», que faria um defeito de composição parecer uma decisão de política.
func TestAOS260_ErroDaPortaEFatalENaoUmaNegacao(t *testing.T) {
	h := newHarness(t, nil)
	cego := errors.New("sem no de orcamento para o run")
	fa := &fakeAdmission{erro: cego, erroNo: 1}
	rt := New(modeloComUso(fa, 1, 1, 1), h.rm, h.recorder, WithModelAdmission(fa))

	res, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, cego) {
		t.Fatalf("um erro da porta tem de subir: %v", err)
	}
	if res.BudgetExhausted {
		t.Error("uma FALHA da admissao nao pode ser reportada como esgotamento — sao coisas diferentes e a razao errada manda o operador procurar no sitio errado")
	}
	for _, e := range fa.eventos {
		if e == "call" {
			t.Fatal("o modelo NAO pode ser chamado quando a admissao falha (fail-closed)")
		}
	}
}

// TestAOS260_ErroDoSaldoEFatalMasNaoTapaOErroDoModelo: o saldo é fail-closed (um tecto que
// deixou de ser fiável não deixa o run continuar), mas quando o modelo TAMBÉM falhou a causa
// primeira é a dele — carimbar-lhe por cima o erro de contabilidade daria ao operador a
// causa errada.
func TestAOS260_ErroDoSaldoEFatalMasNaoTapaOErroDoModelo(t *testing.T) {
	t.Run("turno bom + saldo mau ⇒ aborta com o erro do saldo", func(t *testing.T) {
		h := newHarness(t, nil)
		mau := errors.New("contabilidade do tecto partida")
		fa := &fakeAdmission{erroSaldo: mau}
		rt := New(modeloComUso(fa, 5, 5, 50), h.rm, h.recorder, WithModelAdmission(fa))
		if _, err := rt.Run(context.Background(), sampleGoal()); !errors.Is(err, mau) {
			t.Fatalf("esperava o erro do saldo: %v", err)
		}
	})
	t.Run("modelo mau + saldo mau ⇒ aborta com o erro do MODELO", func(t *testing.T) {
		h := newHarness(t, nil)
		mau := errors.New("contabilidade do tecto partida")
		fa := &fakeAdmission{erroSaldo: mau}
		model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
			return ModelResponse{}, errors.New("provider indisponivel")
		})
		rt := New(model, h.rm, h.recorder, WithModelAdmission(fa))
		_, err := rt.Run(context.Background(), sampleGoal())
		if !errors.Is(err, ErrModelCall) {
			t.Fatalf("a causa primeira e a do modelo: %v", err)
		}
		if errors.Is(err, mau) {
			t.Fatal("o erro do saldo nao pode substituir o do modelo")
		}
	})
}

// TestAOS260_SemPortaComportamentoInalterado: a retro-compatibilidade que todas as portas do
// loop garantem. Sem [WithModelAdmission] nada é consultado e nenhum campo novo se povoa.
func TestAOS260_SemPortaComportamentoInalterado(t *testing.T) {
	h := newHarness(t, nil)
	rt := New(ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Text: "pronto", Final: true}, nil
	}), h.rm, h.recorder)
	if rt.admission != nil {
		t.Fatal("sem a opcao nao pode haver admissao instalada")
	}
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil || !res.Terminated || res.BudgetExhausted {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	rt2 := New(rt.model, h.rm, h.recorder, WithModelAdmission(nil))
	if rt2.admission != nil {
		t.Fatal("WithModelAdmission(nil) nao pode instalar nada (um nil embrulhado passaria o teste != nil)")
	}
}
