package main

// AOS-407 — a soberania por board está LIGADA no caminho de efeito do nó (fecha DEF-909).
//
// FALHA-ANTES: o PDP do nó não tinha registo board→região (WithBoardRegions sem chamador), o hook de
// identidade apagava o board e nenhuma obrigação `region` era emitida — uma tool noutra região
// executava. Aqui a mesma tool call executa na região do board e é negada fora dela.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

type aos407RegiaoModel struct {
	regiao  string
	calls   int32
	mu      sync.Mutex
	prompts []string
}

// tailsDepoisDaCall devolve os prompts materializados a partir do 2.º turno — os que já
// contêm o bloco de negação, se houve negação.
func (m *aos407RegiaoModel) tailsDepoisDaCall() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.prompts) < 2 {
		return nil
	}
	return append([]string(nil), m.prompts[1:]...)
}

func (m *aos407RegiaoModel) Call(_ context.Context, pv agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	// O tail do turno SEGUINTE carrega os rótulos da negação (AOS-013 gap 2: `code` e
	// `denied_by`, nunca a Reason). É por aqui que o teste vê a CAUSA — sem isto afirmava
	// apenas que alguém negou.
	m.mu.Lock()
	m.prompts = append(m.prompts, string(pv.Materialized))
	m.mu.Unlock()
	if atomic.AddInt32(&m.calls, 1) == 1 {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID: "counter", Capability: durCap, ResourceRegion: m.regiao, Input: []byte("tick"),
			}},
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	}
	return agentruntime.ModelResponse{Text: "fim", Final: true, Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func aos407Correr(t *testing.T, regiao string) (execs int64, permits, denials uint64) {
	t.Helper()
	return aos407CorrerComModelo(t, &aos407RegiaoModel{regiao: regiao})
}

func aos407CorrerComModelo(t *testing.T, modelo *aos407RegiaoModel) (execs int64, permits, denials uint64) {
	t.Helper()
	regiao := modelo.regiao
	node, credential := aos220PermitNodeComModelo(t, true, modelo)
	if err := node.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, _, err := node.Runtime.Run(context.Background(), agentruntime.Goal{
		RunID:      "run-aos407-" + regiao,
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: credential,
		Objective:  "AOS-407 soberania por board no efeito",
		MaxTurns:   4,
	}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	permits, denials, _ = node.Runtime.Monitor().Metrics().Snapshot()
	return atomic.LoadInt64(&execs), permits, denials
}

func TestAOS407_ToolNaRegiaoDoBoardExecuta(t *testing.T) {
	execs, permits, denials := aos407Correr(t, "eu")
	if execs != 1 || permits < 1 || denials != 0 {
		t.Fatalf("na região do board (board:aos-demo=eu) a tool executa: execs=%d permits=%d denials=%d", execs, permits, denials)
	}
}

func TestAOS407_ToolForaDaRegiaoDoBoardENegada(t *testing.T) {
	execs, _, denials := aos407Correr(t, "eu-west")
	if execs != 0 || denials < 1 {
		t.Fatalf("fora da região do board a tool é negada pela obrigação region: execs=%d denials=%d", execs, denials)
	}
}

// TestAOS407_ACausaDaNegacaoEAObrigacaoDeRegiao fecha o modo de falha «o teste passa pela razão
// errada»: os dois testes acima contam `execs`/`denials`, e um par permit/deny continuaria verde se
// amanhã outro gate negasse `eu-west` por outro motivo — deixando de provar a obrigação. Aqui
// afirma-se a CAUSA pelos rótulos que o loop devolve ao modelo no tail do turno seguinte: o código
// estável da decisão e o hook atribuível. A Reason não é observável por desenho (nunca chega ao
// modelo), e é por isso que a asserção é sobre `code`/`denied_by` e não sobre texto de motivo.
func TestAOS407_ACausaDaNegacaoEAObrigacaoDeRegiao(t *testing.T) {
	// Fora da região: a negação tem de ser atribuída à OBRIGAÇÃO, com o código estável
	// E_OBLIGATION_UNSATISFIED. Um taint-gate, um scope-gate ou uma revalidação a negar a
	// mesma call dariam `denied_by` diferente e este teste falharia — é o que o par
	// execs/denials não consegue distinguir.
	modeloFora := &aos407RegiaoModel{regiao: "eu-west"}
	aos407CorrerComModelo(t, modeloFora)
	tails := modeloFora.tailsDepoisDaCall()
	if len(tails) == 0 {
		t.Fatal("sem turno depois da call não há bloco de negação para inspeccionar")
	}
	ultimo := tails[len(tails)-1]
	if !strings.Contains(ultimo, referencemonitor.CodeObligationUnsatisfied) || !strings.Contains(ultimo, "obligation") {
		t.Fatalf("a negação tinha de ser atribuída à obrigação de região (code=%s, denied_by=obligation); tail=%q",
			referencemonitor.CodeObligationUnsatisfied, ultimo)
	}
	// Dentro da região: NENHUM bloco de negação — a mesma tool, o mesmo board, só a região
	// do recurso muda. Sem esta metade, um deny universal passaria o teste de cima.
	modeloDentro := &aos407RegiaoModel{regiao: "eu"}
	aos407CorrerComModelo(t, modeloDentro)
	for _, tail := range modeloDentro.tailsDepoisDaCall() {
		if strings.Contains(tail, referencemonitor.CodeObligationUnsatisfied) {
			t.Fatalf("na região do board não podia haver negação por obrigação; tail=%q", tail)
		}
	}
}

func TestAOS407_ToolComRegiaoForaDosBoardsRecusaOArranque(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(path, []byte(`[{"name":"doc_read","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://notes","resource_region":"eu-west"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_MODEL_TOOLS", path)
	if err := validarRegioesDasTools(map[string]string{"board:prod": "eu"}); !errors.Is(err, ErrToolRegionForaDosBoards) {
		t.Fatalf("tool em eu-west com o board em eu tem de recusar o arranque; veio %v", err)
	}
	if err := validarRegioesDasTools(map[string]string{"board:prod": "eu-west"}); err != nil {
		t.Fatalf("região autorizada: %v", err)
	}
	if err := validarRegioesDasTools(nil); err != nil {
		t.Fatalf("sem soberania não há validação: %v", err)
	}
}

// TestAOS407_BoardDeReferenciaNaoEscolhePorTi fixa a REGRA e não a conveniência: a autoridade
// co-localizada cunha para qualquer humano do directório e não sabe a que board cada um pertence,
// pelo que com vários boards NÃO escolhe — sela vazio e o PDP nega fail-closed. Escolher um seria
// dar ao humano de um board a REGIÃO de outro, a travessia que a soberania existe para impedir. Um
// nó multi-board continua a arrancar (é a configuração do read-path soberano); o que fica sem board
// é a cunhagem de referência.
func TestAOS407_BoardDeReferenciaNaoEscolhePorTi(t *testing.T) {
	if got := boardDeReferencia(map[string]string{"board:z": "eu", "board:a": "eu-west"}); got != "" {
		t.Fatalf("com dois boards não podia escolher nenhum, selou %q", got)
	}
	// Mesmo com a MESMA região nos dois: o board vai ASSINADO no token e o read-path também o
	// usa — a ambiguidade é de identidade, não só de região.
	if got := boardDeReferencia(map[string]string{"board:z": "eu", "board:a": "eu"}); got != "" {
		t.Fatalf("dois boards na mesma região continuam ambíguos, selou %q", got)
	}
	if got := boardDeReferencia(map[string]string{"board:demo": "eu"}); got != "board:demo" {
		t.Fatalf("um board: %q", got)
	}
	if got := boardDeReferencia(nil); got != "" {
		t.Fatalf("sem mapa: %q", got)
	}
}
