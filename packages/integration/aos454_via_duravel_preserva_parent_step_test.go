package integration

import (
	"context"
	"reflect"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-454 — a via DURÁVEL preserva o `parent_step_id` da mediação.
//
// Achado no diagnóstico da fase 1 do AOS-069 (2026-09-26): o [DurableDispatcher] traduz o Call do
// loop numa activity.Activity que NÃO tinha campo para o passo pai, e `Activity.toCall` devolvia-o
// vazio. Num nó com AOS_DURABLE_EXECUTION=1 — produção — o evento `tool.call.mediated` saía sem
// `parent_step_id`: a ligação de auditoria entre a tool call e o turno que a pediu perdia-se no
// WAL. Não é autorização (o RM não decide por ele), é rasto.
//
// É a QUARTA vez que a mesma tradução perde um campo do Call: o Credential (AOS-152), a
// ApprovalEvidence (AOS-021), o taint da autorização (AOS-069) e agora o passo pai. Por isso este
// ficheiro tem dois testes: a paridade medida no evento, e uma auditoria campo a campo que obriga
// o próximo campo do Call a ser propagado ou DECLARADO perdido.

// mediatedParentSteps corre o goal e devolve o parent_step_id de cada evento tool.call.mediated.
func mediatedParentSteps(t *testing.T, g agentruntime.Goal, duravel bool) []string {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var opts []agentruntime.Option
	if duravel {
		opts = append(opts, agentruntime.WithActivityDispatcher(newDurableDispatcher(t, store, rm)))
	}
	rt := agentruntime.New(portModel(nil), rm, agentruntime.NewTurnRecorder(store), opts...)
	if _, err := rt.Run(context.Background(), g); err != nil {
		t.Fatalf("Run: %v", err)
	}
	evs, err := store.Read(context.Background(), g.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var pais []string
	for _, ev := range evs {
		if ev.Type == referencemonitor.EventTypeMediated {
			pais = append(pais, ev.ParentStepID)
		}
	}
	return pais
}

// TestAOS454_ViaDuravelPreservaOParentStepIDNoEvento fixa a PARIDADE entre as duas implementações
// da porta [agentruntime.ActivityDispatcher], medida onde o defeito se via: no evento de
// mediação gravado no Event Store.
func TestAOS454_ViaDuravelPreservaOParentStepIDNoEvento(t *testing.T) {
	directo := mediatedParentSteps(t, portTestGoal(), false)
	duravel := mediatedParentSteps(t, portTestGoal(), true)
	if len(directo) != 1 || directo[0] == "" {
		// Sem isto o teste passaria com as duas vias vazias — igualdade sem nada para comparar.
		t.Fatalf("via directa: esperava 1 evento mediated com parent_step_id, vieram %q", directo)
	}
	if !reflect.DeepEqual(duravel, directo) {
		t.Fatalf("via duravel: parent_step_id %q, via directa %q — a porta durável perde a ligação ao passo pai", duravel, directo)
	}
}

// callRecorder é um hook do RM que guarda uma cópia de cada Call que o RM recebeu, e permite.
type callRecorder struct {
	mu    sync.Mutex
	calls []referencemonitor.Call
}

func (h *callRecorder) Name() string { return "call-rec" }
func (h *callRecorder) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	h.mu.Lock()
	h.calls = append(h.calls, *call)
	h.mu.Unlock()
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// camposNaoPropagados é a DECLARAÇÃO dos campos do Call que a via durável não entrega ao RM tal
// como os recebeu, com a razão. Um campo novo no Call que não esteja aqui TEM de chegar igual —
// é isso que impede a quinta repetição do defeito do AOS-454.
var camposNaoPropagados = map[string]string{
	// Não é perda, é TRADUÇÃO: o AOS-069 (fase 1) leva-o por Activity.AuthorizationTaint com
	// taint.ParseLabel, fail-closed, pelo que um valor fora da forma canónica chega como
	// untrusted e não igual. Tem teste próprio
	// (TestAOS069_ViaDuravelPreservaOTaintDaAutorizacao). Sem essa correcção, `toCall` fixa-o em
	// untrusted — o que também não é igual. Fica fora desta comparação nos dois casos.
	"Context.Taint": "traduzido (AOS-069), não copiado",
	// Perda LATENTE: nenhum chamador da porta o preenche — o loop não põe RequestID na Call
	// (loop.go, construção da call), e o DurableDispatcher só é chamado pelo loop. Se um
	// chamador passar a preenchê-lo, sai daqui e ganha campo na Activity.
	"RequestID": "latente: sem produtor na via do loop",
	// SAÍDAS do RM, não entradas: o RiskGate escreve-as dentro de Mediate (risk_gate.go) e
	// selam-nas no audit. Deixá-las atravessar vindas do chamador permitiria a quem despacha
	// pré-preencher a atribuição (RiskApprover) de uma acção que nenhum humano aprovou, num RM
	// sem RiskGate na cadeia. Descartá-las na via durável é o comportamento correcto.
	"Context.RiskClass":        "saída do RiskGate, não entrada",
	"Context.RiskApprover":     "saída do RiskGate, não entrada",
	"Context.RiskDecisionMode": "saída do RiskGate, não entrada",
}

// preencherComSentinelas põe um valor não-zero em cada campo EXPORTADO de v (recursivo em
// structs), para que um campo perdido na tradução se veja como zero à chegada.
func preencherComSentinelas(t *testing.T, v reflect.Value, caminho string) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		nome := f.Name
		if caminho != "" {
			nome = caminho + "." + f.Name
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("s-" + nome)
		case reflect.Int64, reflect.Int:
			fv.SetInt(int64(1000 + i))
		case reflect.Slice:
			switch fv.Type().Elem().Kind() {
			case reflect.Uint8:
				fv.SetBytes([]byte("b-" + nome))
			case reflect.String:
				fv.Set(reflect.ValueOf([]string{"cap:echo"}))
			default:
				s := reflect.MakeSlice(fv.Type(), 1, 1)
				preencherComSentinelas(t, s.Index(0), nome+"[0]")
				fv.Set(s)
			}
		case reflect.Map:
			fv.Set(reflect.ValueOf(map[string][]string{"s-" + nome: {"cap:echo"}}))
		case reflect.Struct:
			preencherComSentinelas(t, fv, nome)
		default:
			t.Fatalf("campo %s de tipo %s sem sentinela — acrescenta o caso a preencherComSentinelas", nome, fv.Kind())
		}
	}
}

// compararCampos devolve, por caminho, os campos exportados de enviado cujo valor não chegou igual.
func compararCampos(enviado, recebido reflect.Value, caminho string, dif map[string][2]any) {
	for i := 0; i < enviado.NumField(); i++ {
		f := enviado.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		nome := f.Name
		if caminho != "" {
			nome = caminho + "." + f.Name
		}
		e, r := enviado.Field(i), recebido.Field(i)
		if e.Kind() == reflect.Struct {
			compararCampos(e, r, nome, dif)
			continue
		}
		if !reflect.DeepEqual(e.Interface(), r.Interface()) {
			dif[nome] = [2]any{e.Interface(), r.Interface()}
		}
	}
}

// TestAOS454_AuditoriaCampoACampoCallActivityCall passa pelo DurableDispatcher um Call com TODOS
// os campos exportados preenchidos e compara com o que o RM recebeu. Cada campo ou chega igual, ou
// está em [camposNaoPropagados] com a razão — e, nesse caso, tem de chegar DIFERENTE: uma
// declaração de perda que deixou de ser verdade é uma mentira no teste.
func TestAOS454_AuditoriaCampoACampoCallActivityCall(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rec := &callRecorder{}
	rm := referencemonitor.New(referencemonitor.WithHooks(rec), referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("s-ToolID", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var enviado referencemonitor.Call
	preencherComSentinelas(t, reflect.ValueOf(&enviado).Elem(), "")
	if _, err := newDurableDispatcher(t, store, rm).Dispatch(context.Background(), enviado); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("esperava 1 mediação, vieram %d", len(rec.calls))
	}

	dif := map[string][2]any{}
	compararCampos(reflect.ValueOf(enviado), reflect.ValueOf(rec.calls[0]), "", dif)
	for campo, par := range dif {
		if _, declarado := camposNaoPropagados[campo]; !declarado {
			t.Errorf("%s perdeu-se na via durável: enviado %v, o RM recebeu %v — propaga-o pela Activity ou declara-o em camposNaoPropagados", campo, par[0], par[1])
		}
	}
	for campo, razao := range camposNaoPropagados {
		if _, perdido := dif[campo]; !perdido {
			t.Errorf("%s está declarado não-propagado (%s) mas chegou igual ao RM — tira-o da declaração", campo, razao)
		}
	}
}
