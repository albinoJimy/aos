package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-489 — O LAYOUT DO PROMPT É FIXADO POR RUN, E SOBREVIVE À RETOMA.
//
// A retoma (aprovação humana, AOS-021) e a recuperação de crash (AOS-253) re-hospedam o run desde
// o turno 1 e REPRODUZEM os turnos já dados. Um run começado num binário anterior ao AOS-489 —
// layout 1.3.0 — e continuado depois do deploy tem de continuar em 1.3.0: noutro layout os turnos
// reproduzidos seriam remontados com outros bytes do que os gravados, o turno seguinte veria um
// prompt que o run nunca teve, e o log ficava misto.
//
// A versão viaja no registo de retoma. Estes testes seguem-na pelo nó REAL — a mesma cadeia de
// `submit` → `hostRun` → `Runtime.Run` que a produção usa — nos dois sentidos:
//
//   - um registo SEM versão (o que os binários anteriores escreveram) é um run 1.3.0;
//   - um run NOVO grava 1.4.0 no registo, e é em 1.4.0 que continua.
//
// O molde é o do AOS-486 (aos486_retoma_reproduz_o_filtro_test.go): o cliente de modelo é o
// adaptador real sobre um gateway que grava os pedidos.

// aos489RegistoComoOBinarioAntigo devolve o registo de retoma tal como um binário ANTERIOR ao
// AOS-489 o escreveu: sem o campo do layout. Confirma que a serialização o omite de facto — o
// registo que se semeia tem os bytes que o código antigo produzia.
func aos489RegistoComoOBinarioAntigo(t *testing.T, goal agentruntime.Goal) integration.ResumeRecord {
	t.Helper()
	rec := resumeRecordFromGoal(goal)
	rec.AssemblyVersion = ""
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "AssemblyVersion") || strings.Contains(string(raw), "assembly_version") {
		t.Fatalf("um registo sem layout tem de serializar SEM o campo (omitempty): %s", raw)
	}
	return rec
}

// aos489Replay reproduz o run do log do nó (capturas seladas por-titular) e devolve o resultado.
func aos489Replay(t *testing.T, node *Node, runID string, spec replay.TrajectorySpec) replay.ReplayResult {
	t.Helper()
	eng, err := replay.NewEngine(node.EventStore, replay.WithContentOpener(node.contentOpener, replay.Accessor{
		Principal: "nhi:leitor-aos489", Scopes: []string{replay.DefaultSovereignContentScope},
	}))
	if err != nil {
		t.Fatalf("replay.NewEngine: %v", err)
	}
	res, err := eng.Replay(context.Background(), runID, replay.Options{Spec: spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return res
}

// aos489VersoesGravadas lê a `assembly_version` de cada `turn.recorded` do run.
func aos489VersoesGravadas(t *testing.T, store *eventstore.Store, runID string) []string {
	t.Helper()
	var out []string
	for _, tr := range aos486TurnosDoRun(t, store, runID) {
		var m agentruntime.Manifest
		if err := json.Unmarshal(tr.manifesto, &m); err != nil {
			t.Fatalf("manifesto ilegivel: %v", err)
		}
		out = append(out, m.AssemblyVersion)
	}
	return out
}

func TestAOS489_RetomaContinuaNoLayoutEmQueORunComecou(t *testing.T) {
	casos := []struct {
		nome   string
		layout string
		// antigo: o run começou num binário anterior ao AOS-489 — o turno 1 foi montado em
		// 1.3.0 e o registo de retoma NÃO tem o campo do layout.
		antigo bool
	}{
		{"run anterior ao AOS-489 (registo sem versao) continua em 1.3.0", agentruntime.AssemblyVersion130, true},
		{"run novo continua em 1.4.0", agentruntime.AssemblyVersion140, false},
	}
	for _, c := range casos {
		for _, via := range []string{"crash-resume", "retoma"} {
			t.Run(c.nome+"/"+via, func(t *testing.T) {
				pinBreakerEnv(t, "0", "0", "0", "0")
				ctx := context.Background()
				const runID = "plan-489~retomado"
				approvers := crashResumeApprovers(t)
				lista := []string{"beta", "counter"}
				goal := agentruntime.Goal{
					RunID:        runID,
					Principal:    referencemonitor.Principal{NHIID: durAgent},
					Model:        agentruntime.ModelConfig{ModelID: "modelo-486"},
					Objective:    "o trabalho de um no do plano",
					MaxTurns:     4,
					AllowedTools: lista,
				}

				// ===== REFERÊNCIA: o mesmo run, sem interrupção, no layout do caso.
				storeRef, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				var contaRef int64
				ref := aos486Incarnar(t, storeRef, audit.NewInMemoryKeyVault(nil), approvers, &contaRef)
				t.Cleanup(func() { _ = ref.node.Close() })
				svcRef := aos486Servico(t, ref.node)
				inteiro := goal
				inteiro.Credential = ref.cred
				if c.antigo {
					inteiro.AssemblyVersion = agentruntime.AssemblyVersion130
				}
				if err := svcRef.Submit(ctx, inteiro); err != nil {
					t.Fatalf("Submit (referencia): %v", err)
				}
				aos486Esperar(t, svcRef, runID)
				pedidosRef, turnosRef := ref.gw.vistos(), aos486TurnosDoRun(t, storeRef, runID)
				if len(pedidosRef) != 2 || len(turnosRef) != 2 {
					t.Fatalf("a referencia tinha de ter 2 pedidos e 2 turnos: %d e %d", len(pedidosRef), len(turnosRef))
				}
				// O hostRun de produção gravou o registo de retoma no arranque, COM o layout.
				recRef, ok, err := ref.node.ResumeRecords.Get(ctx, runID)
				if err != nil || !ok || recRef.AssemblyVersion != c.layout {
					t.Fatalf("o registo de retoma do run de referencia devia ter o layout %q: rec=%q ok=%v err=%v", c.layout, recRef.AssemblyVersion, ok, err)
				}

				// ===== INCARNAÇÃO 1: dá o turno 1 e "crasha" (molde do AOS-253).
				store, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				vault := audit.NewInMemoryKeyVault(nil)
				var conta int64
				inc1 := aos486Incarnar(t, store, vault, approvers, &conta)
				prod := goal
				prod.Credential = inc1.cred
				prod.MaxTurns = 1
				if c.antigo {
					// O binário antigo só montava a 1.3.0.
					prod.AssemblyVersion = agentruntime.AssemblyVersion130
				}
				if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); rerr != nil && !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
					t.Fatalf("turno 1 antes do crash: %v", rerr)
				}
				m, err := state.NewMachine(store, runID)
				if err != nil {
					t.Fatalf("NewMachine: %v", err)
				}
				if _, err := m.Rebuild(ctx); err != nil {
					t.Fatalf("Rebuild: %v", err)
				}
				if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
					t.Fatalf("claim do crash simulado: %v", err)
				}
				// O registo de retoma: o do binário antigo (sem layout), ou o que o hostRun de
				// produção escreve hoje para um run novo.
				rec := resumeRecordFromGoal(goal)
				if c.antigo {
					rec = aos489RegistoComoOBinarioAntigo(t, goal)
				}
				if err := inc1.node.ResumeRecords.Put(ctx, rec); err != nil {
					t.Fatalf("semear o registo de retoma: %v", err)
				}
				_ = inc1.node.Close()

				// ===== INCARNAÇÃO 2 (o binário corrente): re-hospeda o run pela via em teste.
				inc2 := aos486Incarnar(t, store, vault, approvers, &conta)
				t.Cleanup(func() { _ = inc2.node.Close() })
				// O serviço com o log À VISTA: o layout em uso tem de se ver sem ler o WAL.
				registo := &syncBuf{}
				svc2, err := NewNodeService(inc2.node, WithDeadlineSweepInterval(0), WithServiceLog(registo))
				if err != nil {
					t.Fatalf("NewNodeService: %v", err)
				}
				t.Cleanup(func() {
					sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = svc2.Shutdown(sc)
				})
				switch via {
				case "crash-resume":
					scanned, resumed, err := svc2.ResumeInterruptedRuns(ctx)
					if err != nil || scanned != 1 || resumed != 1 {
						t.Fatalf("a varredura devia ver 1 orfao e retomar 1: scanned=%d resumed=%d err=%v", scanned, resumed, err)
					}
				case "retoma":
					svc2.mu.Lock()
					svc2.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
					svc2.mu.Unlock()
					if err := svc2.Resume(ctx, runID, inc2.cred); err != nil {
						t.Fatalf("Resume: %v", err)
					}
				}
				aos486Esperar(t, svc2, runID)

				// (0) O LAYOUT EM USO VÊ-SE: a hospedagem conta no layout do run, e um run que não
				// está no layout dos runs novos é anunciado no log, com o run e o layout.
				outro := map[string]string{agentruntime.AssemblyVersion130: agentruntime.AssemblyVersion140, agentruntime.AssemblyVersion140: agentruntime.AssemblyVersion130}[c.layout]
				if svc2.layouts.lido(c.layout) != 1 || svc2.layouts.lido(outro) != 0 {
					t.Fatalf("runs hospedados por layout: %s=%d %s=%d; queria 1 e 0", c.layout, svc2.layouts.lido(c.layout), outro, svc2.layouts.lido(outro))
				}
				registo.mu.Lock()
				linhas := registo.b.String()
				registo.mu.Unlock()
				anuncio := `run "` + runID + `" hospedado no layout de prompt 1.3.0, e nao no dos runs novos (1.4.0)`
				if c.antigo != strings.Contains(linhas, anuncio) {
					t.Fatalf("anuncio do layout antigo no log: presente=%v, queria %v\n%s", strings.Contains(linhas, anuncio), c.antigo, linhas)
				}
				// E a medição de tool calls está ligada ao runtime do nó: o run de referência
				// despachou UMA tool call, sem repetições.
				if d, r := ref.node.toolCalls.despachadas.Load(), ref.node.toolCalls.repetidas.Load(); d != 1 || r != 0 {
					t.Fatalf("medicao de tool calls do no de referencia: despachadas=%d repetidas=%d; queria 1 e 0", d, r)
				}

				// (1) Os DOIS turnos estão gravados no layout em que o run começou.
				if got, quero := aos489VersoesGravadas(t, store, runID), []string{c.layout, c.layout}; !reflect.DeepEqual(got, quero) {
					t.Fatalf("assembly_version por turno = %v, quero %v — o run mudou de layout na re-hospedagem", got, quero)
				}

				// (2) O turno 2, o único que chegou ao modelo, tem a FORMA do layout do run.
				vivos := inc2.gw.vistos()
				if len(vivos) != 1 {
					t.Fatalf("a re-hospedagem devia interrogar o modelo 1 vez (turno 2), interrogou %d", len(vivos))
				}
				prompt := vivos[0].Messages[0].Content
				temPreambulo := strings.HasPrefix(prompt, "=== PROTOCOL ===\n")
				temChamada := strings.Contains(prompt, "\n<tool_call taint=untrusted id=")
				resultadoIdentificado := strings.Contains(prompt, "\n<tool_result taint=untrusted id=")
				resultadoAntigo := strings.Contains(prompt, "\n<tool_result taint=untrusted>\n")
				if c.antigo {
					if temPreambulo || temChamada || resultadoIdentificado || !resultadoAntigo || !strings.HasPrefix(prompt, "=== SYSTEM ===\n") {
						t.Fatalf("um run 1.3.0 retomado ganhou forma da 1.4.0:\n%s", prompt)
					}
				} else if !temPreambulo || !temChamada || !resultadoIdentificado || resultadoAntigo {
					t.Fatalf("um run novo retomado perdeu a forma da 1.4.0:\n%s", prompt)
				}

				// (3) E é, byte a byte, o prompt do run sem interrupção — com o mesmo prompt_hash
				// e o mesmo manifesto em cada turno.
				if quer := pedidosRef[1].Messages[0].Content; prompt != quer {
					t.Fatalf("turno 2 re-hospedado: o prompt nao e o do run sem interrupcao:\n veio: %s\n quero: %s", prompt, quer)
				}
				turnos := aos486TurnosDoRun(t, store, runID)
				if len(turnos) != 2 {
					t.Fatalf("o run re-hospedado tinha de ter 2 turnos gravados, tem %d", len(turnos))
				}
				for i := range turnos {
					if string(turnos[i].manifesto) != string(turnosRef[i].manifesto) {
						t.Fatalf("turno %d: manifesto diferente do do run sem interrupcao:\n veio: %s\n quero: %s", i+1, turnos[i].manifesto, turnosRef[i].manifesto)
					}
				}

				// (4) O log reproduz-se com fidelidade 1.0, com a âncora na versão do run.
				res := aos489Replay(t, inc2.node, runID, replay.TrajectorySpec{
					Objective:       goal.Objective,
					Tools:           turnos[0].specs,
					Model:           agentruntime.ModelConfig{ModelID: "modelo-486"},
					AssemblyVersion: c.layout,
				})
				if res.Divergence != nil || res.Fidelity != 1.0 || len(res.Steps) != 2 {
					t.Fatalf("o run re-hospedado nao se reproduz: fidelidade=%v turnos=%d divergencia=%+v", res.Fidelity, len(res.Steps), res.Divergence)
				}
			})
		}
	}
}

// TestAOS489_LayoutDesconhecidoNoRegistoNaoERetomado: um registo de retoma que fixa o run num
// layout que este binário não conhece (o que um rollback do binário produz) NÃO é retomado — nem
// noutro layout, nem até falhar. O run fica como estava: órfão para a varredura, suspenso para a
// retoma por aprovação. Sem a guarda, a re-hospedagem chegava ao runtime, falhava fechada lá, e o
// run ficava gravado como FALHADO.
func TestAOS489_LayoutDesconhecidoNoRegistoNaoERetomado(t *testing.T) {
	for _, via := range []string{"crash-resume", "retoma"} {
		t.Run(via, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			ctx := context.Background()
			const runID = "plan-489~do-futuro"
			approvers := crashResumeApprovers(t)
			goal := agentruntime.Goal{
				RunID:        runID,
				Principal:    referencemonitor.Principal{NHIID: durAgent},
				Model:        agentruntime.ModelConfig{ModelID: "modelo-486"},
				Objective:    "o trabalho de um no do plano",
				MaxTurns:     4,
				AllowedTools: []string{"beta", "counter"},
			}
			store, err := eventstore.New()
			if err != nil {
				t.Fatalf("eventstore.New: %v", err)
			}
			vault := audit.NewInMemoryKeyVault(nil)
			var conta int64
			inc1 := aos486Incarnar(t, store, vault, approvers, &conta)
			prod := goal
			prod.Credential = inc1.cred
			prod.MaxTurns = 1
			if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); rerr != nil && !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
				t.Fatalf("turno 1: %v", rerr)
			}
			m, err := state.NewMachine(store, runID)
			if err != nil {
				t.Fatalf("NewMachine: %v", err)
			}
			if _, err := m.Rebuild(ctx); err != nil {
				t.Fatalf("Rebuild: %v", err)
			}
			if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
				t.Fatalf("claim do crash simulado: %v", err)
			}
			// O registo que um binário FUTURO teria escrito.
			rec := resumeRecordFromGoal(goal)
			rec.AssemblyVersion = "9.9.9"
			if err := inc1.node.ResumeRecords.Put(ctx, rec); err != nil {
				t.Fatalf("semear o registo de retoma: %v", err)
			}
			_ = inc1.node.Close()

			inc2 := aos486Incarnar(t, store, vault, approvers, &conta)
			t.Cleanup(func() { _ = inc2.node.Close() })
			svc2 := aos486Servico(t, inc2.node)
			switch via {
			case "crash-resume":
				scanned, resumed, err := svc2.ResumeInterruptedRuns(ctx)
				if err != nil || scanned != 1 || resumed != 0 {
					t.Fatalf("a varredura devia ver 1 orfao e NAO o retomar: scanned=%d resumed=%d err=%v", scanned, resumed, err)
				}
			case "retoma":
				svc2.mu.Lock()
				svc2.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
				svc2.mu.Unlock()
				if err := svc2.Resume(ctx, runID, inc2.cred); !errors.Is(err, ErrResumeLayoutDesconhecido) || !errors.Is(err, agentruntime.ErrUnknownAssemblyVersion) {
					t.Fatalf("Resume: err=%v, quero ErrResumeLayoutDesconhecido sobre ErrUnknownAssemblyVersion", err)
				}
				svc2.mu.Lock()
				_, continuaSuspenso := svc2.suspended[runID]
				svc2.mu.Unlock()
				if !continuaSuspenso {
					t.Fatal("a retoma recusada tirou o run do balde de suspensos")
				}
			}
			// O run NÃO foi re-hospedado: o modelo não foi interrogado, não há turno novo, e o
			// estado durável continua o de antes — nada o gravou como falhado.
			if vivos := inc2.gw.vistos(); len(vivos) != 0 {
				t.Fatalf("o modelo foi interrogado %d vez(es) por um run de layout desconhecido", len(vivos))
			}
			if got := aos489VersoesGravadas(t, store, runID); len(got) != 1 {
				t.Fatalf("o run tinha 1 turno gravado e ficou com %d: %v", len(got), got)
			}
			st, err := svc2.DurableState(ctx, runID)
			if err != nil || st != state.Running {
				t.Fatalf("o estado duravel devia continuar `running` (orfao a espera de um binario que conheca o layout): %v err=%v", st, err)
			}
		})
	}
}

// TestAOS489_FixarLayout: um Goal sem versão é um run novo e fica na dos runs novos; um Goal que
// já a traz — o de uma retoma — não é tocado; e o registo de retoma leva-a SEMPRE explícita.
func TestAOS489_FixarLayout(t *testing.T) {
	if got := fixarLayout(agentruntime.Goal{}).AssemblyVersion; got != agentruntime.AssemblyVersion {
		t.Fatalf("um Goal sem layout devia ficar em %q, ficou em %q", agentruntime.AssemblyVersion, got)
	}
	if got := fixarLayout(agentruntime.Goal{AssemblyVersion: agentruntime.AssemblyVersion130}).AssemblyVersion; got != agentruntime.AssemblyVersion130 {
		t.Fatalf("um Goal fixado em 1.3.0 foi movido para %q", got)
	}
	if got := resumeRecordFromGoal(agentruntime.Goal{RunID: "r"}).AssemblyVersion; got != agentruntime.AssemblyVersion {
		t.Fatalf("o registo de retoma de um run novo tem de levar o layout explicito; levou %q", got)
	}
	antigo := resumeRecordFromGoal(agentruntime.Goal{RunID: "r", AssemblyVersion: agentruntime.AssemblyVersion130})
	if antigo.AssemblyVersion != agentruntime.AssemblyVersion130 || antigo.GoalWith("cred").AssemblyVersion != agentruntime.AssemblyVersion130 {
		t.Fatalf("o layout 1.3.0 nao sobreviveu a ida e volta pelo registo: %+v", antigo)
	}
}

// TestAOS489_MetricasDoLayoutEDasRepeticoes: as duas famílias chegam ao `/metrics` do nó — os
// runs hospedados por layout (vocabulário fechado: só os layouts suportados, sempre presentes) e
// as tool calls despachadas e repetidas.
func TestAOS489_MetricasDoLayoutEDasRepeticoes(t *testing.T) {
	h := noComTodasAsFamilias(t)
	corpo := metricasDe(t, h)
	for _, quero := range []string{
		`aos_runs_hosted_total{assembly_version="1.3.0"} 0`,
		`aos_runs_hosted_total{assembly_version="1.4.0"} 0`,
		"aos_tool_calls_total 0",
		"aos_tool_calls_repeated_total 0",
	} {
		if !strings.Contains(corpo, quero+"\n") {
			t.Fatalf("faltou %q no /metrics de um no acabado de arrancar:\n%s\n%s", quero, amostrasDe(corpo, "aos_runs_hosted_total"), amostrasDe(corpo, "aos_tool_calls"))
		}
	}

	if !h.svc.layouts.contar(agentruntime.AssemblyVersion130) || !h.svc.layouts.contar(agentruntime.AssemblyVersion140) || !h.svc.layouts.contar(agentruntime.AssemblyVersion140) {
		t.Fatal("os layouts suportados tem de contar")
	}
	// Um layout que o assembler não conhece NÃO vira rótulo.
	if h.svc.layouts.contar(`9.9.9"} 1` + "\n" + `aos_ready{x="`) {
		t.Fatal("um layout desconhecido foi contado")
	}
	h.node.toolCalls.observar("run-a", 5, 3)
	h.node.toolCalls.observar("run-b", 2, 0)
	corpo = metricasDe(t, h)
	for _, quero := range []string{
		`aos_runs_hosted_total{assembly_version="1.3.0"} 1`,
		`aos_runs_hosted_total{assembly_version="1.4.0"} 2`,
		"aos_tool_calls_total 7",
		"aos_tool_calls_repeated_total 3",
	} {
		if !strings.Contains(corpo, quero+"\n") {
			t.Fatalf("faltou %q:\n%s\n%s", quero, amostrasDe(corpo, "aos_runs_hosted_total"), amostrasDe(corpo, "aos_tool_calls"))
		}
	}
	if n := strings.Count(corpo, "aos_runs_hosted_total{"); n != len(agentruntime.SupportedAssemblyVersions()) {
		t.Fatalf("a familia tem %d amostras; queria uma por layout suportado", n)
	}
}
