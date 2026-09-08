package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-377, metade (b) — a SIMULAÇÃO deixa de MENTIR sobre a classe de agente.
//
// O DEFEITO QUE ISTO FECHA. `handleAutonomySimular` resolvia o nível com uma classe LITERALMENTE
// vazia (`LevelForAgentOrClass(nhi, "", dominio)`), porque o `audit.AuditRecord` selado NÃO carrega
// a classe do agente. Em produção o PDP resolve pela classe REAL (`Principal.AgentClass`), pelo que
// a simulação salta silenciosamente o degrau `class:` da cascata e prevê OUTRA política. Uma regra
// de classe proposta parecia não mudar nada, quando o que acontecia era que nem era avaliada.
//
// A opção do dono foi a barata e proporcional: a rota PÁRA DE MENTIR / DECLARA a limitação — sem
// migração de SchemaVersion do WORM. Estes testes fixam as duas declarações (por efeito e no topo).

// selarMediacaoSimulavel semeia a residência de um run e uma mediação de tool call que
// [lerMediacoes] recolhe (ToolID e Capability não-vazios), na região do leitor de teste.
func selarMediacaoSimulavel(t *testing.T, worm audit.Store, run, regiao, nhi, capability, recurso string) {
	t.Helper()
	ctx := context.Background()
	if _, err := worm.Append(ctx, audit.AuditRecord{
		Partition: "gov.residency/" + run,
		Resource:  audit.Resource{Type: "run", Value: run, Region: regiao},
	}); err != nil {
		t.Fatalf("selar residencia: %v", err)
	}
	if _, err := worm.Append(ctx, audit.AuditRecord{
		Partition: run, RunID: run, StepID: "s1", ToolID: "doc_read",
		Capability: capability,
		Principal:  audit.Principal{NHIID: nhi},
		Resource:   audit.Resource{Type: "file", Value: recurso, Region: regiao},
	}); err != nil {
		t.Fatalf("selar mediacao: %v", err)
	}
}

// respostaSimular corre a rota e descodifica a resposta JSON.
func respostaSimular(t *testing.T, h *apiHandler, corpo string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h.handleAutonomySimular(w, pedidoComLeitor("POST", "/autonomy/simular", "board:eu", corpo))
	if w.Code != http.StatusOK {
		t.Fatalf("simular: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta ilegivel: %v", err)
	}
	return out
}

// TestAOS377_SimularDeclaraClasseNaoModeladaPorEfeito — AC7: cada efeito declara, por um campo
// PRÓPRIO (`classe_modelada:false`), que a classe do agente não entrou na resolução. Sem regras de
// classe propostas, a resposta não precisa da nota de topo — mas o campo por-efeito está SEMPRE lá.
func TestAOS377_SimularDeclaraClasseNaoModeladaPorEfeito(t *testing.T) {
	h, _, worm := noParaTeste(t)
	comLeitura(h, worm, regioesFixas{"board:eu": "eu"})
	selarMediacaoSimulavel(t, worm, "run-eu", "eu", "agt-eu", "cap:fs.read", "doc://x")

	resp := respostaSimular(t, h, `{"levels":"","default":"L4","max":50}`)

	efeitos, ok := resp["efeitos"].([]any)
	if !ok || len(efeitos) == 0 {
		t.Fatalf("a simulacao devia ter avaliado ao menos um efeito: %v", resp["efeitos"])
	}
	for i, e := range efeitos {
		ef, _ := e.(map[string]any)
		modelada, existe := ef["classe_modelada"]
		if !existe {
			t.Fatalf("efeito #%d nao declara classe_modelada — a resposta mente por omissao: %v", i, ef)
		}
		if modelada != false {
			t.Errorf("efeito #%d: classe_modelada devia ser false (o WORM nao carrega a classe), veio %v", i, modelada)
		}
	}
	// SEM regras de classe propostas, a nota de topo de limitação NÃO aparece — senão treinaria o
	// operador a ignorá-la.
	if _, temLimitacao := resp["limitacao"]; temLimitacao {
		t.Errorf("sem regras `class:` propostas nao devia haver nota de limitacao: %v", resp["limitacao"])
	}
}

// TestAOS377_SimularDeclaraLimitacaoQuandoHaRegraDeClasse — AC8: com uma regra `class:` proposta, a
// simulação é known-incomplete e DECLARA-O no topo (campo `limitacao`), em vez de resolver como se
// as regras de classe não existissem. Contrasta com produção, que resolve pela classe REAL.
func TestAOS377_SimularDeclaraLimitacaoQuandoHaRegraDeClasse(t *testing.T) {
	h, _, worm := noParaTeste(t)
	comLeitura(h, worm, regioesFixas{"board:eu": "eu"})
	selarMediacaoSimulavel(t, worm, "run-eu", "eu", "agt-eu", "cap:fs.read", "doc://x")

	// Uma regra de CLASSE que, em produção, casaria com o agente `agt-eu` se a classe fosse
	// modelada (domínio `fs` = DomainOf("cap:fs.read")). A simulação NÃO a modela.
	resp := respostaSimular(t, h, `{"levels":"class:agent-worker:fs=L4","default":"L0","max":50}`)

	nota, ok := resp["limitacao"].(string)
	if !ok || nota == "" {
		t.Fatalf("com uma regra `class:` proposta a resposta TEM de declarar a limitacao no topo: %v", resp["limitacao"])
	}
	// A nota tem de nomear a causa (classe não selada no WORM) e o contraste com produção.
	for _, exigido := range []string{"class", "classe", "producao"} {
		if !containsCI(nota, exigido) {
			t.Errorf("a nota de limitacao devia nomear %q:\n%s", exigido, nota)
		}
	}
	// E cada efeito continua a declarar classe_modelada:false.
	efeitos, _ := resp["efeitos"].([]any)
	if len(efeitos) == 0 {
		t.Fatal("a simulacao devia ter avaliado ao menos um efeito")
	}
	for _, e := range efeitos {
		ef, _ := e.(map[string]any)
		if ef["classe_modelada"] != false {
			t.Errorf("efeito com classe_modelada != false: %v", ef)
		}
	}

	// NÃO-VACUOSIDADE / CONTRASTE COM PRODUÇÃO. A regra de classe NÃO é decorativa: um registo
	// EFÉMERO com a mesma regra resolve para L4 quando a classe REAL entra (o caminho de
	// pdp.applyAutonomy), e para o piso quando a classe é a vazia que o selo WORM impõe (o caminho
	// da simulação). É a divergência exacta que a rota agora nomeia em vez de esconder.
	reg := autonomy.NewLevelRegistry(autonomy.WithDefaultLevel(autonomy.L0))
	if _, err := reg.SetLevel(context.Background(), autonomy.ClassPrefix+"agent-worker", "fs", autonomy.L4, "teste", "teste"); err != nil {
		t.Fatal(err)
	}
	comClasseReal := reg.LevelForAgentOrClass("agt-eu", "agent-worker", "fs")
	comClasseVazia := reg.LevelForAgentOrClass("agt-eu", classeNaoSeladaNoWORM, "fs")
	if comClasseReal == comClasseVazia {
		t.Fatalf("o teste seria vacuoso: a classe real e a vazia resolvem igual (%s) — a regra de classe nao muda nada e a limitacao nao teria substancia", comClasseReal)
	}
	if comClasseReal != autonomy.L4 || comClasseVazia != autonomy.L0 {
		t.Fatalf("esperava L4 pela classe real e L0 pela vazia, veio %s e %s", comClasseReal, comClasseVazia)
	}
}

// containsCI é um contains case-insensitive minimalista (evita puxar strings.ToLower repetido no
// corpo dos testes e ignora acentos ausentes na prosa PT-PT do banner).
func containsCI(s, sub string) bool {
	return len(sub) == 0 || indexCI(s, sub) >= 0
}

func indexCI(s, sub string) int {
	sl, subl := toLowerASCII(s), toLowerASCII(sub)
	for i := 0; i+len(subl) <= len(sl); i++ {
		if sl[i:i+len(subl)] == subl {
			return i
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
