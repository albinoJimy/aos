package integration

// UMA TOOL PERMITIDA QUE FALHA NÃO PODE MATAR O RUN.
//
// O [activity.Dispatcher] sinaliza o erro de runtime de uma tool PERMITIDA pelo ERRO
// ([activity.ErrToolExecution]), porque o [activity.Result] não tem campo para ele: nada é
// memorizado no ledger e o passo fica RETRIÁVEL. Sem tradução no adaptador, esse erro subia até
// `loop.go`, que trata qualquer erro do dispatcher como fatal, e a trajectória terminava — o
// retry que o ledger promete nunca acontecia, porque não ficava quem retentasse.
//
// A assimetria era com o irmão: o `directDispatcher` do kernel preserva o `ToolErr` e o loop
// materializa `tool_error=` no tail. Duas implementações da MESMA porta, comportamentos
// diferentes perante o mesmo evento — e a de produção era a que matava o run.
//
// Os testes exercitam o [DurableDispatcher] REAL, sobre um `activity.Dispatcher` real, com
// duplos só nas duas fronteiras que ele exige (Mediator e Ledger). Testar uma cópia da regra
// provaria a cópia.

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// mediadorQuePermiteComErro devolve PERMIT com um `ToolErr` — o caso de uma tool autorizada
// que falhou a jusante (um 500, um timeout do serviço remoto).
type mediadorQuePermiteComErro struct{ falha error }

func (m mediadorQuePermiteComErro) Mediate(context.Context, referencemonitor.Call) (referencemonitor.Decision, error) {
	return referencemonitor.Decision{Effect: referencemonitor.EffectPermit, ToolErr: m.falha}, nil
}

// mediadorQueFalhaFatal simula uma falha do próprio canal de mediação (não um veredicto).
type mediadorQueFalhaFatal struct{ err error }

func (m mediadorQueFalhaFatal) Mediate(context.Context, referencemonitor.Call) (referencemonitor.Decision, error) {
	return referencemonitor.Decision{}, m.err
}

// ledgerQueCorre executa o efeito e devolve o que ele devolver — sem memorização, que é o que
// interessa: o passo falhado NÃO fica aplicado.
type ledgerQueCorre struct{}

func (ledgerQueCorre) Apply(ctx context.Context, _ string, effect func(context.Context) (durable.Result, error), _ ...durable.ApplyOption) (durable.Result, bool, error) {
	r, err := effect(ctx)
	return r, err == nil, err
}

func despachanteReal(t *testing.T, m activity.Mediator) *DurableDispatcher {
	t.Helper()
	base, err := activity.NewDispatcher(m, ledgerQueCorre{})
	if err != nil {
		t.Fatalf("compor o activity.Dispatcher: %v", err)
	}
	d, err := NewDurableDispatcher(base)
	if err != nil {
		t.Fatalf("compor o DurableDispatcher: %v", err)
	}
	return d
}

func chamadaDeTeste() referencemonitor.Call {
	return referencemonitor.Call{
		RunID: "run-1", StepID: "step-000001-tool-1", ToolID: "doc_read",
		Capability: "cap:fs.read", Principal: referencemonitor.Principal{NHIID: "agent"},
	}
}

// TestToolFalhada_NaoEFatalParaOLoop é o caso que motivou a correcção.
func TestToolFalhada_NaoEFatalParaOLoop(t *testing.T) {
	falhaDaTool := errors.New("HTTP 500 do servico a jusante")
	d := despachanteReal(t, mediadorQuePermiteComErro{falha: falhaDaTool})

	dec, err := d.Dispatch(context.Background(), chamadaDeTeste())

	if err != nil {
		t.Fatalf("um erro de RUNTIME de uma tool permitida NAO pode ser fatal ao loop — o run morria aqui: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Errorf("o RM PERMITIU; o veredicto tem de continuar permit, veio %q", dec.Effect)
	}
	if !errors.Is(dec.ToolErr, falhaDaTool) {
		t.Errorf("o ToolErr perdeu-se: sem ele o loop nao materializa `tool_error=` e o modelo nao "+
			"distingue uma tool falhada de um output vazio legitimo; veio %v", dec.ToolErr)
	}
}

// TestToolFalhada_FalhaDoCanalContinuaFatal é a metade que impede a correcção de engolir o que
// deve mesmo parar o run. Sem ela, devolver `nil` incondicionalmente passaria o teste acima — e
// um run cancelado continuaria a correr.
func TestToolFalhada_FalhaDoCanalContinuaFatal(t *testing.T) {
	for _, c := range []struct {
		nome string
		err  error
	}{
		{"cancelamento de contexto", context.Canceled},
		{"prazo excedido", context.DeadlineExceeded},
		{"falha opaca do canal de mediacao", errors.New("mediador indisponivel")},
	} {
		t.Run(c.nome, func(t *testing.T) {
			d := despachanteReal(t, mediadorQueFalhaFatal{err: c.err})

			if _, err := d.Dispatch(context.Background(), chamadaDeTeste()); err == nil {
				t.Fatalf("%s tem de continuar FATAL ao loop — foi engolido", c.nome)
			}
		})
	}
}
