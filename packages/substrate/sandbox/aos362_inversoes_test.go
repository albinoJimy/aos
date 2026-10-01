package sandbox

// aos362_inversoes_test.go — os riscos de inversão que a remediação do EPIC-24 deixou abertos
// no pacote da sandbox (AOS-362 a, c, d).

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestAOS362_ATabelaDoSeccompConfrontaOsDrivers — AOS-362 (a). A tabela [seccompEnforcementFor]
// diz QUEM impõe o perfil, e o WORM sela o que ela diz. Nada a confrontava com os drivers: se um
// driver real passasse a ler [Spec.Seccomp] sem a tabela mudar, o evento SUBdeclarava (o defeito
// do AOS-351 ao contrário), e o teste de manifesto continuava verde a exigir `none`.
//
// O confronto é estrutural: em cada `driver_*.go` do pacote, o tipo que implementa `Kind()`
// declara o seu [DriverKind], e o ficheiro ou lê `.Seccomp` ou não. Ler ⇔ a tabela dizer
// [SeccompEnforcedByDriver]. Um ficheiro de driver novo cujo `Kind()` não se reconheça avermelha
// também: a tabela tem de o conhecer antes de o evento falar dele.
func TestAOS362_ATabelaDoSeccompConfrontaOsDrivers(t *testing.T) {
	ficheiros, err := filepath.Glob("driver_*.go")
	if err != nil {
		t.Fatal(err)
	}
	porNome := map[string]DriverKind{
		"DriverFake":        DriverFake,
		"DriverFirecracker": DriverFirecracker,
		"DriverGVisor":      DriverGVisor,
	}
	vistos := map[DriverKind]bool{}
	for _, f := range ficheiros {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		arq, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		var kind DriverKind
		leSeccomp := false
		ast.Inspect(arq, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Recv != nil && x.Name.Name == "Kind" && x.Body != nil {
					for _, st := range x.Body.List {
						if ret, ok := st.(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
							if id, ok := ret.Results[0].(*ast.Ident); ok {
								kind = porNome[id.Name]
							}
						}
					}
				}
			case *ast.SelectorExpr:
				if x.Sel.Name == "Seccomp" {
					leSeccomp = true
				}
			}
			return true
		})
		if kind == "" {
			t.Errorf("%s: não declara um Kind() que esta tabela reconheça — um driver novo tem de entrar em "+
				"seccompEnforcementFor (e aqui) antes de os seus eventos falarem de seccomp", f)
			continue
		}
		vistos[kind] = true
		impoe := seccompEnforcementFor(kind) == SeccompEnforcedByDriver
		if leSeccomp != impoe {
			t.Errorf("%s (%s): lê Spec.Seccomp=%v, mas a tabela diz seccomp_enforced_by=%q — o WORM sela uma "+
				"afirmação que o driver não cumpre", f, kind, leSeccomp, seccompEnforcementFor(kind))
		}
	}
	for _, k := range []DriverKind{DriverFake, DriverFirecracker, DriverGVisor} {
		if !vistos[k] {
			t.Errorf("nenhum driver_*.go declara %q — o confronto não viu o driver", k)
		}
	}
}

// TestAOS362_UmCreateFalhadoNaoDeixaOHashNu — AOS-362 (c). O hash do perfil entrava no span antes
// do Create, e a qualificação só depois: um Create que falhasse terminava o span com o hash nu,
// que é exactamente a leitura que o AOS-351 existe para impedir.
func TestAOS362_UmCreateFalhadoNaoDeixaOHashNu(t *testing.T) {
	store := newStore(t)
	rt := &recordingTracer{}
	// Firecracker SEM executor: o Create falha com ErrDriverUnavailable.
	launcher, err := NewLauncher(NewFirecrackerDriver(), WithEventSink(NewEventStoreSink(store)), WithTracer(rt))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	req := ExecRequest{RunID: "run-362-c", StepID: "step-362", Call: ToolCall{ToolID: "t", Command: "echo"}}
	if _, err := ml.Execute(context.Background(), defaultAuthz(), req); !errors.Is(err, ErrDriverUnavailable) {
		t.Fatalf("o controlo da premissa falhou: Execute = %v, quero ErrDriverUnavailable do Create", err)
	}
	if _, ok := rt.attr(AttrSeccompHash); !ok {
		t.Fatal("o span não tem o hash — o teste não está a medir o caminho do Create falhado")
	}
	v, ok := rt.attr(AttrSeccompEnforcedBy)
	if !ok {
		t.Fatal("o Create falhou e o span terminou com o hash do seccomp NU (AOS-362 c)")
	}
	if v.(string) != string(SeccompEnforcedByNone) {
		t.Fatalf("AttrSeccompEnforcedBy = %v num firecracker, quero %q", v, SeccompEnforcedByNone)
	}
}

// TestAOS362_OEventoDizOndeAExecucaoCorreu — AOS-362 (d). Um nó de desenvolvimento selava no
// WORM resultados do driver de referência sem nada no evento que os distinguisse de um efeito
// real. Os três eventos do ciclo de vida passam a levar `execution_boundary`, colado por
// construção no sink.
func TestAOS362_OEventoDizOndeAExecucaoCorreu(t *testing.T) {
	casos := []struct {
		nome   string
		driver SandboxDriver
		runID  string
		quer   ExecutionBoundary
	}{
		{"fake", NewFakeDriver(), "run-362-d-fake", BoundaryInProcessReference},
		{"firecracker", NewFirecrackerDriver(WithFirecrackerExecutor(echoExecutor{})), "run-362-d-fc", BoundaryGuestExecutor},
		{"gvisor", NewGVisorDriver(WithGVisorExecutor(echoExecutor{})), "run-362-d-gv", BoundaryGuestExecutor},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			store := newStore(t)
			launcher, err := NewLauncher(c.driver, WithEventSink(NewEventStoreSink(store)))
			if err != nil {
				t.Fatalf("NewLauncher: %v", err)
			}
			ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
			if err != nil {
				t.Fatalf("NewMediatedLauncher: %v", err)
			}
			req := ExecRequest{RunID: c.runID, StepID: "step-362", Call: ToolCall{ToolID: "t", Command: "echo"}}
			if _, err := ml.Execute(context.Background(), defaultAuthz(), req); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			evs := readEvents(t, store, c.runID)
			for _, typ := range []string{EventInstanceCreated, EventExecCompleted, EventInstanceDestroyed} {
				matched := eventsOfType(evs, typ)
				if len(matched) == 0 {
					t.Fatalf("nenhum evento %q", typ)
				}
				for _, e := range matched {
					var p lifecyclePayload
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatalf("unmarshal %q: %v", typ, err)
					}
					if p.ExecutionBoundary != string(c.quer) {
						t.Fatalf("%q execution_boundary = %q, quero %q", typ, p.ExecutionBoundary, c.quer)
					}
				}
			}
		})
	}
	if got := executionBoundaryFor(DriverKind("desconhecido")); got != BoundaryUndeclared {
		t.Fatalf("um driver desconhecido declarou %q — não se presume fronteira nenhuma", got)
	}
}
