package agentruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-526 — AS TOOLS DO CONTRATO SEGUEM NA VISTA DE CADA TURNO: as que faltam cumprir
// ([PromptView.CompletionPending], pelos mesmos contadores do veredicto) e as que quem faz o
// pedido pode forçar ([PromptView.CompletionToForce]) — as mesmas, só enquanto o run não pediu
// tool nenhuma.

// aos526Turno é o que o modelo de teste viu num turno.
type aos526Turno struct {
	pendentes, aForcar []string
	hash               string
	material           string
}

// aos526Modelo grava, por turno, o que a vista trazia, e responde pelo guião.
type aos526Modelo struct {
	turnos []aos526Turno
	guiao  func(turno int) ModelResponse
}

func (m *aos526Modelo) Call(_ context.Context, view PromptView) (ModelResponse, error) {
	m.turnos = append(m.turnos, aos526Turno{view.CompletionPending, view.CompletionToForce, view.PromptHash, string(view.Materialized)})
	return m.guiao(len(m.turnos)), nil
}

func (m *aos526Modelo) pendentes() [][]string {
	var out [][]string
	for _, t := range m.turnos {
		out = append(out, t.pendentes)
	}
	return out
}

func (m *aos526Modelo) aForcar() [][]string {
	var out [][]string
	for _, t := range m.turnos {
		out = append(out, t.aForcar)
	}
	return out
}

func aos526Chamar(tool string) ModelResponse {
	return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: tool, Capability: "cap:echo", Input: []byte("x")}}}
}

// aos526Guiao: turno 1 chama `echo` (efectiva); turno 2 chama `falha` (erro de tool); turno 3
// chama `falha` de novo e resulta; turno 4 responde em texto.
func aos526Guiao(turno int) ModelResponse {
	switch turno {
	case 1:
		return aos526Chamar("echo")
	case 2, 3:
		return aos526Chamar("falha")
	}
	return ModelResponse{Text: "fim", Final: true}
}

// aos526Run corre um run com o contrato e o guião dados. `echo` resulta sempre; `falha` falha à
// primeira e resulta depois; `sempre_falha` falha sempre; `nega` está no tool set do run mas não
// está registada no Reference Monitor, que a recusa por omissão.
// `saida`, quando dado, recebe o modelo ANTES de o run começar (para um guião que lê as vistas).
func aos526Run(t *testing.T, modo CompletionMode, requires []string, guiao func(int) ModelResponse, saida ...**aos526Modelo) (*aos526Modelo, Result) {
	t.Helper()
	tentativas := 0
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
		"falha": func(_ context.Context, in []byte) ([]byte, error) {
			tentativas++
			if tentativas == 1 {
				return nil, errors.New("falha de tool do teste")
			}
			return in, nil
		},
		"sempre_falha": func(context.Context, []byte) ([]byte, error) { return nil, errors.New("falha de tool do teste") },
	})
	goal := sampleGoal()
	goal.Tools = append(toolSet(),
		ToolSpec{Name: "echo", Version: "1.0.0", Digest: "sha256:dd04"}, ToolSpec{Name: "falha", Version: "1.0.0", Digest: "sha256:ee05"},
		ToolSpec{Name: "sempre_falha", Version: "1.0.0", Digest: "sha256:ff06"}, ToolSpec{Name: "nega", Version: "1.0.0", Digest: "sha256:aa07"})
	goal.CompletionMode, goal.CompletionRequires = modo, requires
	m := &aos526Modelo{guiao: guiao}
	for _, s := range saida {
		*s = m
	}
	res, err := New(m, h.rm, h.recorder).Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return m, res
}

// Com o contrato: a vista do 1.º turno traz as duas tools; depois da chamada efectiva de `echo`
// só fica `falha`; uma chamada que FALHOU não a cumpre; depois da efectiva, a vista vem sem nada.
// A forçar: só no 1.º turno; a partir da primeira tool call do run (a de `echo`, efectiva), nada —
// `falha` continua por cumprir e nunca foi pedida, e mesmo assim não se força.
func TestAOS526_Vista_AsToolsPorCumprirAcompanhamOVeredicto(t *testing.T) {
	for _, modo := range []CompletionMode{CompletionEnforce, CompletionObserve} {
		m, _ := aos526Run(t, modo, []string{"echo", "falha", "echo"}, aos526Guiao)
		if quer := [][]string{{"echo", "falha"}, {"falha"}, {"falha"}, nil}; !reflect.DeepEqual(m.pendentes(), quer) {
			t.Fatalf("modo %s: tools por cumprir por turno = %q, quero %q", modo, m.pendentes(), quer)
		}
		if quer := [][]string{{"echo", "falha"}, nil, nil, nil}; !reflect.DeepEqual(m.aForcar(), quer) {
			t.Fatalf("modo %s: tools a forcar por turno = %q, quero %q", modo, m.aForcar(), quer)
		}
	}
}

// (a) e (b) — UMA TOOL RECUSADA, OU QUE FALHOU, NÃO SE FORÇA OUTRA VEZ. No turno a seguir continua
// por cumprir e já nada se força; o modelo responde, e a RAZÃO do veredicto é a mesma que sem a
// regra (o 1.º turno é que foi forçado).
func TestAOS526_Vista_ToolRecusadaOuFalhadaNaoSeForcaOutraVez(t *testing.T) {
	for _, c := range []struct {
		tool  string
		razao OutcomeReason
	}{
		{"nega", OutcomeContractAfterDenial},
		{"sempre_falha", OutcomeContractAfterToolError},
	} {
		m, res := aos526Run(t, CompletionEnforce, []string{c.tool}, func(turno int) ModelResponse {
			if turno == 1 {
				return aos526Chamar(c.tool)
			}
			return ModelResponse{Text: "nao consegui", Final: true}
		})
		if quer := [][]string{{c.tool}, {c.tool}}; !reflect.DeepEqual(m.pendentes(), quer) {
			t.Fatalf("%s: por cumprir = %q, quero %q", c.tool, m.pendentes(), quer)
		}
		if quer := [][]string{{c.tool}, nil}; !reflect.DeepEqual(m.aForcar(), quer) {
			t.Fatalf("%s: a forcar = %q, quero %q — a tool ja tentada continuaria a ser forcada", c.tool, m.aForcar(), quer)
		}
		if !res.Unfulfilled || res.Verdict == nil || res.Verdict.Reason != c.razao {
			t.Fatalf("%s: o run fechou com %+v; quero nao cumprido com %s", c.tool, res.Verdict, c.razao)
		}
	}
}

// (c) — DUAS TOOLS EXIGIDAS, UMA RECUSADA E OUTRA NUNCA PEDIDA: depois da recusa, nada se força.
func TestAOS526_Vista_DuasExigidasUmaRecusadaNadaMaisSeForca(t *testing.T) {
	m, _ := aos526Run(t, CompletionEnforce, []string{"nega", "echo"}, func(turno int) ModelResponse {
		switch turno {
		case 1:
			return aos526Chamar("nega")
		case 2:
			return aos526Chamar("echo")
		}
		return ModelResponse{Text: "fim", Final: true}
	})
	if quer := [][]string{{"nega", "echo"}, {"nega", "echo"}, {"nega"}}; !reflect.DeepEqual(m.pendentes(), quer) {
		t.Fatalf("por cumprir = %q, quero %q", m.pendentes(), quer)
	}
	if quer := [][]string{{"nega", "echo"}, nil, nil}; !reflect.DeepEqual(m.aForcar(), quer) {
		t.Fatalf("a forcar = %q, quero %q", m.aForcar(), quer)
	}
}

// O LAÇO DA OUTRA TOOL. Sob o campo — o caso realista é `required` com várias tools oferecidas —
// o modelo chama uma tool que NÃO é a exigida (uma oferecida, `echo`, ou um nome inventado,
// `doc_read_v2`). O modelo de teste obedece ao campo enquanto ele vier: com tools a forçar chama
// a outra tool, sem elas responde. No turno a seguir à primeira tool call nada se força, o run
// fecha no 2.º turno com `contract_unmet_no_call`, e não esgota turnos por causa do campo.
func TestAOS526_Vista_OutraToolSobOCampoNaoFazLaco(t *testing.T) {
	for _, outra := range []string{"echo", "doc_read_v2"} {
		var m *aos526Modelo
		m, res := aos526Run(t, CompletionEnforce, []string{"falha"}, func(turno int) ModelResponse {
			if len(m.turnos) > 0 && m.turnos[len(m.turnos)-1].aForcar != nil {
				return aos526Chamar(outra)
			}
			return ModelResponse{Text: "sem a tool exigida", Final: true}
		}, &m)
		if quer := [][]string{{"falha"}, nil}; !reflect.DeepEqual(m.aForcar(), quer) {
			t.Fatalf("%s: a forcar = %q, quero %q", outra, m.aForcar(), quer)
		}
		if !res.Unfulfilled || res.Verdict == nil || res.Verdict.Reason != OutcomeContractNoCall || res.Turns != 2 {
			t.Fatalf("%s: o run fechou com %+v em %d turnos; quero nao cumprido com %s em 2", outra, res.Verdict, res.Turns, OutcomeContractNoCall)
		}
	}
}

// O CONTROLO: sem contrato, ou com o modo do veredicto desligado (o contrato é então ignorado,
// como em todo o resto), nenhuma vista traz tools por cumprir nem por tentar — a vista é a de sempre.
func TestAOS526_Vista_SemContratoOuDesligadoNadaPorCumprir(t *testing.T) {
	for nome, c := range map[string]struct {
		modo     CompletionMode
		requires []string
	}{
		"sem contrato":      {CompletionEnforce, nil},
		"modo desligado":    {CompletionOff, []string{"echo"}},
		"modo por omissao":  {"", []string{"echo"}},
		"contrato so vazio": {CompletionEnforce, []string{""}},
	} {
		m, _ := aos526Run(t, c.modo, c.requires, aos526Guiao)
		for i, turno := range m.turnos {
			if turno.pendentes != nil || turno.aForcar != nil {
				t.Fatalf("%s: o turno %d trouxe tools do contrato: %q %q", nome, i+1, turno.pendentes, turno.aForcar)
			}
		}
	}
}

// A3 — AS LISTAS DO CONTRATO NÃO ENTRAM NO PROMPT. O mesmo run, com o contrato ligado e desligado,
// dá a cada turno o mesmo material e o mesmo `prompt_hash`, e o hash é o do material.
func TestAOS526_Vista_AsListasNaoEntramNoPromptHash(t *testing.T) {
	com, _ := aos526Run(t, CompletionObserve, []string{"echo", "falha"}, aos526Guiao)
	sem, _ := aos526Run(t, CompletionOff, []string{"echo", "falha"}, aos526Guiao)
	if len(com.turnos) != len(sem.turnos) || len(com.turnos) == 0 {
		t.Fatalf("os dois runs tiveram %d e %d turnos", len(com.turnos), len(sem.turnos))
	}
	if com.turnos[0].pendentes == nil || sem.turnos[0].pendentes != nil {
		t.Fatalf("o teste nao compara uma vista com listas com uma sem: %q %q", com.turnos[0].pendentes, sem.turnos[0].pendentes)
	}
	for i := range com.turnos {
		c, s := com.turnos[i], sem.turnos[i]
		if c.material != s.material || c.hash != s.hash || c.hash != sha256Tagged([]byte(c.material)) {
			t.Fatalf("turno %d: as listas do contrato mudaram o prompt (hash %s contra %s)", i+1, c.hash, s.hash)
		}
	}
}

// As funções são a condição do veredicto, e são puras: os mesmos contadores dão as mesmas listas.
func TestAOS526_ContratoPorCumprir_CondicaoDoVeredicto(t *testing.T) {
	c := &Completion{Mode: CompletionEnforce, Requires: []string{"a", "b", "c"}}
	e := NewRunEvidence()
	if got := contratoPorCumprir(c, e); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("sem nada despachado: %q", got)
	}
	if got := contratoAForcar(c, e); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("a forcar, sem nada despachado: %q", got)
	}
	e.Observe("s1", taint.Trusted, []CapturedToolResult{aos493Resultado("a", true, false), aos493Resultado("b", false, false)})
	if got := contratoPorCumprir(c, e); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("com `a` recusada e `b` efectiva: %q", got)
	}
	if got := contratoAForcar(c, e); got != nil {
		t.Fatalf("a forcar, depois da primeira tool call do run: %q", got)
	}
	outra := NewRunEvidence()
	outra.Observe("s1", taint.Trusted, []CapturedToolResult{aos493Resultado("fora-do-contrato", false, false)})
	if got := contratoAForcar(c, outra); got != nil {
		t.Fatalf("a forcar, depois de uma tool call fora do contrato: %q", got)
	}
	e.Observe("s3", taint.Trusted, []CapturedToolResult{aos493Resultado("a", false, false), aos493Resultado("c", false, false)})
	if got := contratoPorCumprir(c, e); got != nil {
		t.Fatalf("com o contrato cumprido: %q", got)
	}
	if contratoPorCumprir(nil, e) != nil || contratoPorCumprir(c, nil) != nil || contratoAForcar(nil, e) != nil || contratoAForcar(c, nil) != nil {
		t.Fatalf("sem contrato ou sem contadores tem de dar nil")
	}
}
