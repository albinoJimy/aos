package agentruntime_test

// OS CICLOS DO LAÇO, TORNADOS OBSERVÁVEIS.
//
// O laço do agente é a peça central do sistema e a mais difícil de ver: os prompts NÃO são
// gravados em lado nenhum por desenho (captura de conteúdo por REFERÊNCIA — só hashes, ADR-005),
// o gateway de modelo regista linhas de acesso sem corpos, e a trajectória no Event Store guarda
// a ESTRUTURA dos turnos, não o texto. Quem quiser perceber o que o modelo vê em cada ciclo não
// tem, em produção, por onde o ler — e isso é uma decisão correcta que este teste NÃO contorna.
//
// O que este teste faz é instrumentar a porta [agentruntime.ModelClient], que é exactamente o
// ponto por onde o prompt passa, e IMPRIMIR cada ciclo:
//
//	prompt materializado do turno  →  saída do modelo  →  tool call mediada pelo RM
//	                                                   →  resultado devolvido ao laço
//	                                                   →  prompt do turno seguinte (já com o resultado)
//
// Corre com `-v` para ver o registo:
//
//	go test ./packages/kernel/agent-runtime/ -run TestCiclosDoLaco -v
//
// NÃO é um teste de demonstração apenas: as asserções trancam quatro propriedades que o laço
// promete e que só se vêem observando dois turnos seguidos — ver o fim da função.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// cicloObservado é o registo de UM turno, tal como o laço o produziu.
type cicloObservado struct {
	turno        int
	promptHash   string
	prefixHash   string
	materialized []byte
	prefixLen    int
	saidaTexto   string
	pediuTool    string
	final        bool
}

// modeloQueRegista implementa [agentruntime.ModelClient] e grava o que RECEBE (a PromptView
// completa) e o que DEVOLVE. É o único ponto de instrumentação necessário: o prompt de cada
// ciclo passa por aqui, e por mais lado nenhum.
type modeloQueRegista struct {
	t       *testing.T
	ciclos  *[]cicloObservado
	guiao   []agentruntime.ModelResponse
	chamado int
}

func (m *modeloQueRegista) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	if m.chamado >= len(m.guiao) {
		m.t.Fatalf("o laço pediu um turno %d que o guião não previa", m.chamado+1)
	}
	resp := m.guiao[m.chamado]
	m.chamado++

	c := cicloObservado{
		turno:        view.Turn,
		promptHash:   view.PromptHash,
		prefixHash:   view.PrefixHash,
		materialized: append([]byte(nil), view.Materialized...),
		prefixLen:    len(view.Prefix),
		saidaTexto:   resp.Text,
		final:        resp.Final,
	}
	if len(resp.ToolCalls) > 0 {
		c.pediuTool = resp.ToolCalls[0].ToolID
	}
	*m.ciclos = append(*m.ciclos, c)
	return resp, nil
}

// TestCiclosDoLaco corre DOIS turnos — o mínimo para que o ciclo se feche e se veja o resultado
// da tool a entrar no prompt seguinte — e imprime o input e o output de cada um.
func TestCiclosDoLaco(t *testing.T) {
	var ciclos []cicloObservado

	// TURNO 1: o modelo pede a tool. TURNO 2: já com o resultado no prompt, responde e termina.
	guiao := []agentruntime.ModelResponse{
		{
			Text: "preciso de ler o documento antes de responder",
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:        "doc_read",
				Capability:    "cap:fs.read",
				ResourceType:  "file",
				ResourceValue: "doc://notas",
				Input:         []byte("doc://notas"),
			}},
		},
		{Text: "o documento diz que a reunião ficou marcada para as 15h", Final: true},
	}

	// O RM com stubs neutros: aqui interessa VER o ciclo, não exercitar a política — isso é o
	// que a bateria de mediação faz. A tool devolve um resultado reconhecível para se poder
	// afirmar que ele REAPARECE no prompt do turno seguinte.
	rm := referencemonitor.New(referencemonitor.WithHooks(
		referencemonitor.IdentityStub{},
		referencemonitor.PolicyStub{},
		referencemonitor.BudgetStub{},
		referencemonitor.EgressStub{},
		referencemonitor.AuditStub{},
	))
	const resultadoDaTool = "REUNIAO-AS-15H"
	if err := rm.Register("doc_read", func(_ context.Context, in []byte) ([]byte, error) {
		return []byte(resultadoDaTool + " (lido de " + string(in) + ")"), nil
	}); err != nil {
		t.Fatalf("registar a tool: %v", err)
	}

	modelo := &modeloQueRegista{t: t, ciclos: &ciclos, guiao: guiao}
	rt := agentruntime.New(modelo, rm, agentruntime.NewTurnRecorder(coletorDeEventos{}))

	res, err := rt.Run(context.Background(), agentruntime.Goal{
		RunID:     "ciclos-1",
		Principal: referencemonitor.Principal{NHIID: "agent-demo"},
		Scope:     []string{"cap:fs.read"},
		System:    "És um assistente que lê documentos antes de responder.",
		Tools: []agentruntime.ToolSpec{
			{Name: "doc_read", Version: "1.0.0", Digest: "sha256:demo"},
		},
		Objective: "Diz a que horas ficou marcada a reunião.",
		MaxTurns:  4,
	})
	if err != nil {
		t.Fatalf("o laço falhou: %v", err)
	}

	imprimirCiclos(t, ciclos, res, resultadoDaTool)

	// ---- O QUE AS ASSERÇÕES TRANCAM -----------------------------------------------------
	//
	// Imprimir sem asserir seria um teste que passa sempre. Cada uma destas quatro só se pode
	// verificar observando o laço a dar MAIS DO QUE UMA volta.

	if len(ciclos) != 2 {
		t.Fatalf("esperava 2 ciclos (pedir a tool, responder com o resultado), houve %d", len(ciclos))
	}

	// (1) O PREFIXO É BYTE-IDÊNTICO ENTRE TURNOS. É a promessa de cache-estabilidade (ADR-009):
	// se o prefixo mudasse a cada turno, cada chamada seria um cache-miss e o custo dispararia.
	if ciclos[0].prefixHash != ciclos[1].prefixHash {
		t.Errorf("o prefixo MUDOU entre turnos (%s -> %s) — cache-miss estrutural",
			ciclos[0].prefixHash, ciclos[1].prefixHash)
	}

	// (2) O PROMPT CRESCE, e cresce só no fim. O turno 2 tem de conter o turno 1 como prefixo —
	// é o append-only da janela. Um prompt reescrito invalidaria o cache e quebraria o replay.
	if !strings.HasPrefix(string(ciclos[1].materialized), string(ciclos[0].materialized)) {
		t.Error("o prompt do turno 2 NÃO estende o do turno 1 — a janela deixou de ser append-only")
	}

	// (3) O RESULTADO DA TOOL REAPARECE NO PROMPT SEGUINTE. É isto que fecha o ciclo: sem esta
	// realimentação o modelo pediria a mesma tool para sempre.
	if !strings.Contains(string(ciclos[1].materialized), resultadoDaTool) {
		t.Errorf("o resultado da tool NÃO entrou no prompt do turno 2 — o ciclo não fecha")
	}

	// (4) O PROMPT-HASH MUDA quando o conteúdo muda. Se não mudasse, o replay determinístico
	// não conseguiria detectar divergência — o hash é a âncora de AOS-016.
	if ciclos[0].promptHash == ciclos[1].promptHash {
		t.Error("dois prompts DIFERENTES com o mesmo hash — a âncora do replay deixou de ancorar")
	}
}

// imprimirCiclos escreve o registo legível. Vai para o log do teste (visível com -v).
func imprimirCiclos(t *testing.T, ciclos []cicloObservado, res agentruntime.Result, resultadoDaTool string) {
	t.Helper()
	t.Log("")
	t.Log("=========================== CICLOS DO LAÇO ===========================")
	for i, c := range ciclos {
		t.Logf("")
		t.Logf("┌─ CICLO %d ─────────────────────────────────────────────────────", c.turno)
		t.Logf("│ INPUT — prompt materializado (%d bytes; prefixo imutável = %d)", len(c.materialized), c.prefixLen)
		t.Logf("│   prompt_hash = %s", c.promptHash)
		t.Logf("│   prefix_hash = %s   (igual em todos os turnos)", c.prefixHash)
		for _, linha := range recortar(string(c.materialized)) {
			t.Logf("│     %s", linha)
		}
		if i > 0 {
			novo := len(c.materialized) - len(ciclos[i-1].materialized)
			t.Logf("│   (cresceu %d bytes face ao ciclo anterior — append-only)", novo)
		}
		t.Logf("│ OUTPUT — resposta do modelo")
		t.Logf("│     texto: %q", c.saidaTexto)
		switch {
		case c.pediuTool != "":
			t.Logf("│     pede tool: %s  ⇒ vai ser MEDIADA pelo Reference Monitor", c.pediuTool)
			t.Logf("│     resultado devolvido ao laço: %q", resultadoDaTool+" (lido de doc://notas)")
			t.Logf("│     ⇒ este resultado entra no prompt do ciclo seguinte")
		case c.final:
			t.Logf("│     final=true ⇒ o laço TERMINA aqui")
		}
		t.Logf("└───────────────────────────────────────────────────────────────")
	}
	t.Log("")
	t.Logf("DESFECHO: turnos=%d  terminado=%v  texto_final=%q", res.Turns, res.Terminated, res.FinalText)
	t.Log("======================================================================")
}

// recortar parte o prompt em linhas legíveis, truncando as muito longas — o objectivo é ler a
// ESTRUTURA (system, tools, objectivo, resultados), não despejar bytes.
func recortar(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		if len(l) > 110 {
			l = l[:110] + fmt.Sprintf(" …(+%d)", len(l)-110)
		}
		out = append(out, l)
	}
	return out
}

// coletorDeEventos satisfaz o [agentruntime.EventAppender] do TurnRecorder sem substrato: este
// teste observa o LAÇO, não a durabilidade (essa é a bateria do Event Store).
type coletorDeEventos struct{}

func (coletorDeEventos) Append(context.Context, string, eventstore.EventInput, ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, nil
}
