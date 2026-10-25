package activity_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// AOS-551 — O PRODUTOR do `tool_result` por handle, no seu assento.
//
// O que esta suite fixa é o ASSENTO, e não só o efeito: o selo cai DEPOIS do permit do Reference
// Monitor e ANTES da escrita no step-ledger. É isso que faz o ledger, o loop, a captura, o replay
// e a rota que serve os bytes designados verem os MESMOS bytes — e é por isso que a âncora da
// origem da saída, que os seus verificadores confirmam re-hasheando o payload do ledger, confere.
//
// COMO SE MEDE O ASSENTO sem ler o log cifrado: pelo DEDUP. O segundo despacho da mesma chave
// devolve o payload que o LEDGER guardou, sem re-executar e sem re-selar. Se o selo corresse
// depois da escrita — o que um envelope a chamar `Dispatch` faria —, o ledger ficava com o
// conteúdo e o dedup devolvia conteúdo. É a verificação que faltava às duas tentativas anteriores
// desta fase.

// seloDeTeste é um [activity.ResultSealer] determinista: conta as invocações, grava o que lhe
// deram, e devolve uma referência com o ORDINAL da invocação — para que um segundo selo seja
// visivelmente outro token, como o do espaço de handles do nó.
type seloDeTeste struct {
	chamadas atomic.Int64
	// vistos é o que cada invocação recebeu, pela ordem.
	vistos   [][]byte
	contexto []string
	erro     error
	// corpo, quando != "", é o que o selo devolve em vez de uma referência bem formada.
	corpo string
}

func (s *seloDeTeste) selar(_ context.Context, subject, runID, stepID, toolID string, conteudo []byte) ([]byte, error) {
	n := s.chamadas.Add(1)
	s.vistos = append(s.vistos, append([]byte(nil), conteudo...))
	s.contexto = append(s.contexto, subject+"|"+runID+"|"+stepID+"|"+toolID)
	if s.erro != nil {
		return nil, s.erro
	}
	if s.corpo != "" {
		return []byte(s.corpo), nil
	}
	return []byte(agentruntime.HandleRefPrefix + agentruntime.MintHandleToken(runID, "sha256:aa", int(n))), nil
}

// ctx180 é o contexto de um run fixado na 1.8.0 — o que o [agentruntime.Runtime.Run] põe.
func ctx180() context.Context {
	return agentruntime.ContextWithAssemblyVersion(context.Background(), agentruntime.AssemblyVersion180)
}

// O CORPO QUE O LOOP RECEBE É A REFERÊNCIA, e o que o LEDGER guardou é a MESMA referência.
func TestAOS551_SeloCaiAntesDaEscritaNoLedger(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	selo := &seloDeTeste{}
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithResultSealer(selo.selar))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := ctx180()
	res, err := d.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	ref, ok := agentruntime.ParseHandleRef(string(res.Output.Value))
	if !ok {
		t.Fatalf("o corpo que o loop recebeu não é uma referência: %q", res.Output.Value)
	}
	if !res.Output.IsUntrusted() {
		t.Fatalf("a referência continua untrusted: taint=%q", res.Output.Taint)
	}
	// O selo viu o OUTPUT DA TOOL, não a referência nem os argumentos.
	if n := selo.chamadas.Load(); n != 1 {
		t.Fatalf("o selo correu %d vezes, quero 1", n)
	}
	if got := string(selo.vistos[0]); got != "out:payload" {
		t.Fatalf("o selo recebeu %q — tinha de receber o output da tool", got)
	}
	// E viu o contexto POR RUN que o assento lhe dá.
	if quero := "|" + testRun + "|" + testStep + "|" + testTool; selo.contexto[0] != quero {
		t.Fatalf("o contexto do selo é %q, quero %q", selo.contexto[0], quero)
	}

	// O ASSENTO: o dedup devolve o que o LEDGER guardou. Se o selo corresse depois da escrita, o
	// ledger tinha o conteúdo e isto devolvia "out:payload".
	dedup, err := d.Dispatch(ctx, baseActivity())
	if err != nil {
		t.Fatalf("Dispatch (dedup): %v", err)
	}
	if !dedup.Deduplicated {
		t.Fatal("o segundo despacho da mesma chave tinha de deduplicar")
	}
	if string(dedup.Output.Value) != string(res.Output.Value) {
		t.Fatalf("o ledger guardou %q e o loop viu %q — o selo não está antes da escrita", dedup.Output.Value, res.Output.Value)
	}
	// E não re-selou: um segundo selo dava outro ordinal, logo outro token, logo outro
	// `prompt_hash` numa retoma.
	if n := selo.chamadas.Load(); n != 1 {
		t.Fatalf("o selo correu %d vezes em dedup — re-selar muda o token do run", n)
	}
	if h.spy.calls.Load() != 1 {
		t.Fatalf("o efeito correu %d vezes", h.spy.calls.Load())
	}
	_ = ref
}

// O PAYLOAD SELADO VAI MARCADO COMO REFERÊNCIA para o ledger — é o vocabulário DELE
// ([durable.Result.Reference]) e é a guarda de escrita do modo sensível, que recusa um payload
// não-vazio não marcado quando não há cifrador.
//
// Mede-se pelo EFEITO da marca: sem ela, um nó em modo sensível sem o cifrador por-titular
// recusava TODA a tool call de um run da 1.8.0 com [durable.ErrClearResultInSensitiveMode], por
// não saber que o payload já é uma referência. O contraste com o mesmo ledger SEM selo composto
// (que tem de recusar, porque aí o payload é conteúdo em claro) é o que prova que a marca mede.
func TestAOS551_OPayloadSeladoVaiMarcadoComoReferencia(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		nome     string
		comSelo  bool
		queroErr error
	}{
		{"com selo: o payload é referência e o passo passa", true, nil},
		{"sem selo: o payload é conteúdo em claro e o modo sensível recusa", false, durable.ErrClearResultInSensitiveMode},
	} {
		t.Run(c.nome, func(t *testing.T) {
			h := newHarness(t, false, nil)
			sensivel, err := durable.NewStepLedger(h.store, durable.WithSensitiveResults())
			if err != nil {
				t.Fatalf("NewStepLedger sensível: %v", err)
			}
			var opts []activity.Option
			if c.comSelo {
				opts = append(opts, activity.WithResultSealer((&seloDeTeste{}).selar))
			}
			d, err := activity.NewDispatcher(h.rm, sensivel, opts...)
			if err != nil {
				t.Fatalf("NewDispatcher: %v", err)
			}
			_, err = d.Dispatch(ctx180(), baseActivity())
			if !errors.Is(err, c.queroErr) {
				t.Fatalf("Dispatch = %v; quero %v", err, c.queroErr)
			}
		})
	}
}

// FORA DE UM LAYOUT POR HANDLE nada se sela, mesmo com o selo composto: o nó compõe UM dispatcher
// para todos os runs, e é a versão do layout do run que decide.
func TestAOS551_ForaDa180NadaSeSela(t *testing.T) {
	t.Parallel()
	for _, versao := range []string{
		agentruntime.AssemblyVersion140, agentruntime.AssemblyVersion150,
		agentruntime.AssemblyVersion160, agentruntime.AssemblyVersion170,
	} {
		h := newHarness(t, false, nil)
		selo := &seloDeTeste{}
		d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithResultSealer(selo.selar))
		if err != nil {
			t.Fatalf("NewDispatcher: %v", err)
		}
		res, err := d.Dispatch(agentruntime.ContextWithAssemblyVersion(context.Background(), versao), baseActivity())
		if err != nil {
			t.Fatalf("%s: Dispatch: %v", versao, err)
		}
		if string(res.Output.Value) != "out:payload" {
			t.Fatalf("%s: o output mudou: %q", versao, res.Output.Value)
		}
		if n := selo.chamadas.Load(); n != 0 {
			t.Fatalf("%s: o selo correu %d vezes num layout que não põe os resultados por handle", versao, n)
		}
	}
}

// SEM VERSÃO NO CONTEXTO, RECUSA — nunca «assume o layout antigo». Assumir é o fail-open exacto
// que esta fase existe para não ter: um run da 1.8.0 cujo contexto perdesse a versão gravava
// conteúdo cru no ledger, e o tail mostrava ao modelo um erro de tool enquanto o `aos-orq`
// entregava esse conteúdo a jusante.
//
// E recusa FAIL-CLOSED: nada é memorizado, pelo que o passo fica retriável e nenhum byte de
// conteúdo chega ao loop.
func TestAOS551_SemVersaoNoContextoRecusa(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	selo := &seloDeTeste{}
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithResultSealer(selo.selar))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	res, err := d.Dispatch(context.Background(), baseActivity())
	if !errors.Is(err, activity.ErrSealWithoutAssemblyVersion) {
		t.Fatalf("Dispatch sem versão = %+v, %v; quero ErrSealWithoutAssemblyVersion", res, err)
	}
	if len(res.Output.Value) != 0 {
		t.Fatalf("a recusa devolveu bytes: %q", res.Output.Value)
	}
	// Nada memorizado: o despacho seguinte NÃO deduplica.
	segundo, err := d.Dispatch(ctx180(), baseActivity())
	if err != nil {
		t.Fatalf("Dispatch (2.º): %v", err)
	}
	if segundo.Deduplicated {
		t.Fatal("a recusa memorizou o passo no ledger")
	}
}

// A `MEDIA_VIEW` É EXCEPÇÃO, e pela MESMA função que o tail pergunta. O seu resultado é lido CRU
// para resolver a `MediaRef` e anexar a imagem (a funcionalidade da R1a): selá-lo fazia a imagem
// deixar SILENCIOSAMENTE de chegar ao modelo.
func TestAOS551_MediaViewNaoSeSelaNa180(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	// A tool da media_view registada no MESMO RM, para o despacho ser o real.
	if err := h.rm.Register(agentruntime.MediaViewTool, func(context.Context, []byte) ([]byte, error) {
		return []byte("media-ref-crua"), nil
	}); err != nil {
		t.Fatalf("Register(%s): %v", agentruntime.MediaViewTool, err)
	}
	selo := &seloDeTeste{}
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithResultSealer(selo.selar))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	act := baseActivity()
	act.ToolID = agentruntime.MediaViewTool
	res, err := d.Dispatch(ctx180(), act)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if string(res.Output.Value) != "media-ref-crua" {
		t.Fatalf("a media_view foi selada: %q", res.Output.Value)
	}
	if n := selo.chamadas.Load(); n != 0 {
		t.Fatalf("o selo correu %d vezes sobre a media_view", n)
	}
	// E a excepção é a MESMA que o tail aplica — uma segunda cópia dela divergiria sem erro.
	if agentruntime.SealsToolResult(agentruntime.AssemblyVersion180, agentruntime.MediaViewTool) {
		t.Fatal("SealsToolResult diz que a media_view sela — o tail diz o contrário")
	}
	if !agentruntime.SealsToolResult(agentruntime.AssemblyVersion180, testTool) {
		t.Fatal("SealsToolResult diz que uma tool normal não sela na 1.8.0")
	}
}

// UM SELO QUE FALHA, E UM SELO QUE DEVOLVE LIXO, RECUSAM — e não memorizam.
//
// O lixo tem de recusar aqui e não só no tail: o tail materializá-lo-ia como erro de tool (negando
// a mais, que é seguro), mas a designação da origem da saída digeria-o e o `aos-orq` entregá-lo-ia
// a jusante como saída do run.
func TestAOS551_SeloQueFalhaOuDevolveLixoRecusa(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome  string
		selo  *seloDeTeste
		quero error
		// toolErr diz que o erro leva um [activity.ToolError] na cadeia, que é o que faz o
		// adaptador da via durável materializar `tool_error=` no tail em vez de matar o run.
		toolErr bool
	}{
		{"recusa do espaço", &seloDeTeste{erro: errors.New("handle_tecto_do_run")}, activity.ErrResultSeal, true},
		{"corpo que não é referência", &seloDeTeste{corpo: "o conteudo cru"}, activity.ErrSealedResultNotRef, false},
		{"referência embrulhada", &seloDeTeste{corpo: `{"handle":"` + agentruntime.HandleRefPrefix + `h1.00"}`}, activity.ErrSealedResultNotRef, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			h := newHarness(t, false, nil)
			d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithResultSealer(c.selo.selar))
			if err != nil {
				t.Fatalf("NewDispatcher: %v", err)
			}
			res, err := d.Dispatch(ctx180(), baseActivity())
			if !errors.Is(err, c.quero) {
				t.Fatalf("Dispatch = %+v, %v; quero %v", res, err, c.quero)
			}
			if len(res.Output.Value) != 0 {
				t.Fatalf("a recusa devolveu bytes ao loop: %q", res.Output.Value)
			}
			// A RECUSA OPERACIONAL NÃO MATA O RUN, e a de composição mata.
			//
			// O que decide é haver um [activity.ToolError] na cadeia: o adaptador da via durável
			// converte-o em `Decision{Permit, ToolErr}` e o loop materializa `tool_error=` no
			// tail. Sem ele o erro sobe até ao loop e aborta o run — e com o tecto de handles por
			// run isso é alcançável (a 65.ª tool call de um run da 1.8.0). Um corpo que não é
			// referência é defeito de COMPOSIÇÃO, e aí o aborto é a postura certa.
			var te *activity.ToolError
			if got := errors.As(err, &te); got != c.toolErr {
				t.Fatalf("ToolError na cadeia = %v, quero %v (erro: %v)", got, c.toolErr, err)
			}
			// O erro NÃO leva o conteúdo: a mensagem é do vocabulário de quem sela.
			if msg := err.Error(); msg != "" && len(c.selo.vistos) > 0 && contem(msg, string(c.selo.vistos[0])) {
				t.Fatalf("o erro leva o conteúdo: %q", msg)
			}
			// Nada memorizado.
			selo2 := &seloDeTeste{}
			d2, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithResultSealer(selo2.selar))
			if err != nil {
				t.Fatalf("NewDispatcher (2.º): %v", err)
			}
			segundo, err := d2.Dispatch(ctx180(), baseActivity())
			if err != nil {
				t.Fatalf("Dispatch (2.º): %v", err)
			}
			if segundo.Deduplicated {
				t.Fatal("a recusa do selo memorizou o passo no ledger")
			}
		})
	}
}

// Um output VAZIO não se sela: não há nada para selar, e o tail de um permit vazio materializa os
// mesmos bytes que no layout anterior.
func TestAOS551_OutputVazioNaoSeSela(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	h.spy.output = []byte{}
	selo := &seloDeTeste{}
	d, err := activity.NewDispatcher(h.rm, h.ledger, activity.WithResultSealer(selo.selar))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	res, err := d.Dispatch(ctx180(), baseActivity())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(res.Output.Value) != 0 {
		t.Fatalf("um output vazio ganhou corpo: %q", res.Output.Value)
	}
	if n := selo.chamadas.Load(); n != 0 {
		t.Fatalf("o selo correu %d vezes sobre um output vazio", n)
	}
}

// SEM SELO COMPOSTO, a 1.8.0 não muda um byte do despacho. É o estado de todos os nós antes de
// alguém ligar o produtor, e é o que mantém o risco desta fase a zero.
func TestAOS551_SemSeloCompostoODespachoEODeSempre(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	res, err := d.Dispatch(ctx180(), baseActivity())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if string(res.Output.Value) != "out:payload" {
		t.Fatalf("output inesperado sem selo composto: %q", res.Output.Value)
	}
}

// contem é `strings.Contains` sem o import (a agulha vazia nunca conta).
func contem(palheiro, agulha string) bool {
	if agulha == "" {
		return false
	}
	for i := 0; i+len(agulha) <= len(palheiro); i++ {
		if palheiro[i:i+len(agulha)] == agulha {
			return true
		}
	}
	return false
}
