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
// O confronto é estrutural, sobre TODOS os `.go` não-teste do pacote:
//   - um DRIVER é um tipo com um método `Kind()`. O `Kind()` tem de devolver uma das constantes
//     conhecidas, senão o teste avermelha, e um driver novo tem de entrar na tabela antes de os
//     seus eventos falarem de seccomp;
//   - a leitura de `.Seccomp` é atribuída ao RECEPTOR do método onde aparece. Uma leitura numa
//     função livre não se consegue atribuir e avermelha também;
//   - um driver que lê tem de ser um driver que a tabela diz impor, e vice-versa.
//
// LER NÃO É IMPOR. Este teste vê a leitura; quem prova a imposição é
// `TestWiring_SeccompDefaultDenyOnExecPath` (`wiring_test.go`). Um vermelho aqui manda verificar se
// o driver IMPÕE o perfil antes de mudar a tabela, e não mudá-la às cegas.
//
// LIMITE: o perfil pode chegar ao runtime sem `.Seccomp` aparecer no pacote, entregando a `Spec`
// inteira ao [GuestExecutor]. O executor real vive no `cmd/aos`, fora daqui, e o wire dele tem
// sensor próprio: `TestAOS362_OWireDosExecutoresNaoTransportaOPerfil`.
func TestAOS362_ATabelaDoSeccompConfrontaOsDrivers(t *testing.T) {
	ficheiros, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	porNome := map[string]DriverKind{
		"DriverFake":        DriverFake,
		"DriverFirecracker": DriverFirecracker,
		"DriverGVisor":      DriverGVisor,
	}
	receptor := func(fd *ast.FuncDecl) string {
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return ""
		}
		tipo := fd.Recv.List[0].Type
		if st, ok := tipo.(*ast.StarExpr); ok {
			tipo = st.X
		}
		if id, ok := tipo.(*ast.Ident); ok {
			return id.Name
		}
		return "?"
	}
	leSeccomp := func(n ast.Node) bool {
		achou := false
		ast.Inspect(n, func(x ast.Node) bool {
			if sel, ok := x.(*ast.SelectorExpr); ok && sel.Sel.Name == "Seccomp" {
				achou = true
			}
			return !achou
		})
		return achou
	}
	kindDe := map[string]DriverKind{} // tipo -> kind
	leituras := map[string]bool{}     // tipo -> lê .Seccomp num dos seus métodos
	for _, f := range ficheiros {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		arq, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range arq.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			tipo := receptor(fd)
			if fd.Name.Name == "Kind" && tipo != "" {
				var k DriverKind
				if len(fd.Body.List) == 1 {
					if ret, ok := fd.Body.List[0].(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
						if id, ok := ret.Results[0].(*ast.Ident); ok {
							k = porNome[id.Name]
						}
					}
				}
				if k == "" {
					t.Errorf("%s: %s.Kind() não devolve uma constante que a tabela conheça — um driver novo "+
						"tem de entrar em seccompEnforcementFor (e aqui) antes de os seus eventos falarem de seccomp", f, tipo)
					continue
				}
				kindDe[tipo] = k
			}
			if !leSeccomp(fd.Body) {
				continue
			}
			if tipo == "" {
				t.Errorf("%s: a função livre %s lê .Seccomp — a leitura não se consegue atribuir a um driver; "+
					"leve-a para um método do driver que a usa", f, fd.Name.Name)
				continue
			}
			leituras[tipo] = true
		}
	}
	for _, k := range []DriverKind{DriverFake, DriverFirecracker, DriverGVisor} {
		visto := false
		for _, kk := range kindDe {
			visto = visto || kk == k
		}
		if !visto {
			t.Errorf("nenhum tipo do pacote declara Kind() = %q — o confronto não viu o driver", k)
		}
	}
	for tipo, k := range kindDe {
		impoe := seccompEnforcementFor(k) == SeccompEnforcedByDriver
		if leituras[tipo] != impoe {
			t.Errorf("%s (%s): lê Spec.Seccomp=%v, mas a tabela diz seccomp_enforced_by=%q. Verifique se o "+
				"driver IMPÕE o perfil (o par comportamental é TestWiring_SeccompDefaultDenyOnExecPath) "+
				"antes de mudar a tabela", tipo, k, leituras[tipo], seccompEnforcementFor(k))
		}
	}
}

// TestAOS362_UmCreateFalhadoNaoDeixaOHashNu — AOS-362 (c). O hash do perfil entrava no span antes
// do Create, e a qualificação só depois: um Create que falhasse terminava o span com o hash nu,
// que é exactamente a leitura que o AOS-351 existe para impedir.
//
// Os dois casos têm valores provisórios DIFERENTES, para que uma constante no lugar da derivação
// pelo driver configurado não passe.
func TestAOS362_UmCreateFalhadoNaoDeixaOHashNu(t *testing.T) {
	casos := []struct {
		nome   string
		driver SandboxDriver
		erro   error
		quer   SeccompEnforcement
	}{
		// Firecracker SEM executor: o Create falha com ErrDriverUnavailable.
		{"firecracker sem executor", NewFirecrackerDriver(), ErrDriverUnavailable, SeccompEnforcedByNone},
		// Um driver do kind que IMPÕE o perfil, a falhar no Create.
		{"driver que impoe, a falhar", driverQueFalhaNoCreate{kind: DriverFake}, errCreateDoTeste, SeccompEnforcedByDriver},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			store := newStore(t)
			rt := &recordingTracer{}
			launcher, err := NewLauncher(c.driver, WithEventSink(NewEventStoreSink(store)), WithTracer(rt))
			if err != nil {
				t.Fatalf("NewLauncher: %v", err)
			}
			ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
			if err != nil {
				t.Fatalf("NewMediatedLauncher: %v", err)
			}
			req := ExecRequest{RunID: "run-362-c", StepID: "step-362", Call: ToolCall{ToolID: "t", Command: "echo"}}
			if _, err := ml.Execute(context.Background(), defaultAuthz(), req); !errors.Is(err, c.erro) {
				t.Fatalf("o controlo da premissa falhou: Execute = %v, quero %v do Create", err, c.erro)
			}
			if _, ok := rt.attr(AttrSeccompHash); !ok {
				t.Fatal("o span não tem o hash — o teste não está a medir o caminho do Create falhado")
			}
			v, ok := rt.attr(AttrSeccompEnforcedBy)
			if !ok {
				t.Fatal("o Create falhou e o span terminou com o hash do seccomp NU (AOS-362 c)")
			}
			if v.(string) != string(c.quer) {
				t.Fatalf("AttrSeccompEnforcedBy = %v, quero %q — a qualificação provisória tem de vir do driver configurado", v, c.quer)
			}
		})
	}
}

var errCreateDoTeste = errors.New("create falhado (teste)")

// driverQueFalhaNoCreate é um driver de teste cujo Create falha sempre, com o Kind que o caso
// pedir. Vive num ficheiro de teste, fora do confronto estrutural da tabela.
type driverQueFalhaNoCreate struct{ kind DriverKind }

func (d driverQueFalhaNoCreate) Create(context.Context, capability, Spec) (Instance, error) {
	return Instance{}, errCreateDoTeste
}
func (d driverQueFalhaNoCreate) Exec(context.Context, capability, Instance, ExecRequest) (ExecResult, error) {
	return ExecResult{}, errCreateDoTeste
}
func (d driverQueFalhaNoCreate) Destroy(context.Context, capability, Instance) error { return nil }
func (d driverQueFalhaNoCreate) Kind() DriverKind                                    { return d.kind }

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
