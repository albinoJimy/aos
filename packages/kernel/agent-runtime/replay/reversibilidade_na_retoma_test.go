package replay

import (
	"encoding/json"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ---------------------------------------------------------------------------
// A REVERSIBILIDADE TEM DE SOBREVIVER À CAPTURA.
//
// Observado em produção a 2026-08-19, ao exercitar a cerimónia four-eyes: a MESMA tool call foi
// selada `autonomia L0 x gray` na primeira execução e `autonomia L0 x danger` quando o run foi
// RETOMADO. Os selos do WORM mostram porquê:
//
//	20:39  "Context":{"Taint":"untrusted","Reversibility":"reversible","Sensitivity":""}
//	21:07  "Context":{"Taint":"untrusted","Reversibility":"","Sensitivity":""}
//
// `toolCallCapture` não guardava o campo. Na retoma voltava vazio, e `IsIrreversible()` devolve
// true para o desconhecido — logo `danger`.
//
// A direcção é fail-closed (mais severo, nunca menos), e foi por isso que ninguém reparou. Mas a
// consequência é real: quem assina uma perna de aprovação com a classe de risco da PRIMEIRA
// execução vê a assinatura recusada na segunda, e o trilho de auditoria mostra duas
// classificações diferentes para a mesma acção.
// ---------------------------------------------------------------------------

// TestReversibilidadeSobreviveAoCicloDeCaptura é o teste directo: serializar e desserializar não
// pode perder a declaração.
func TestReversibilidadeSobreviveAoCicloDeCaptura(t *testing.T) {
	original := agentruntime.ModelResponse{
		ToolCalls: []agentruntime.ToolInvocation{{
			ToolID:         "doc_read",
			Capability:     "cap:fs.read",
			ResourceType:   "file",
			ResourceValue:  "doc://notes",
			ResourceRegion: "eu",
			Reversibility:  "reversible",
			Input:          []byte(`{"doc_id":"notes"}`),
		}},
	}

	c := &EventStoreCapturer{}
	rc := c.encodeResponse(original)
	bs, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	var decodificado responseCapture
	if err := json.Unmarshal(bs, &decodificado); err != nil {
		t.Fatal(err)
	}
	reconstruido := decodificado.decode()

	if len(reconstruido.ToolCalls) != 1 {
		t.Fatalf("perdeu-se a tool call: %d", len(reconstruido.ToolCalls))
	}
	got := reconstruido.ToolCalls[0].Reversibility
	if got != "reversible" {
		t.Errorf("reversibilidade depois da captura = %q, quero \"reversible\".\n"+
			"Vazio significa DESCONHECIDO, e o classificador trata o desconhecido como "+
			"IRREVERSIVEL: a mesma tool call passa de `gray` a `danger` so por ter sido retomada.", got)
	}

	// CONTROLO — os outros campos continuam a atravessar. Sem isto, um `decodeResponse` que
	// devolvesse tudo preenchido por acaso passaria no teste acima sem provar nada sobre a
	// reversibilidade em particular.
	tc := reconstruido.ToolCalls[0]
	if tc.ToolID != "doc_read" || tc.Capability != "cap:fs.read" || tc.ResourceValue != "doc://notes" {
		t.Errorf("a captura perdeu campos que ja funcionavam: %+v", tc)
	}
}

// TestReversibilidadeNaoERedigidaComoPII é a segunda metade, e sem ela a correcção podia ser
// desfeita sem que nada avisasse.
//
// O capturer redige `Input` e `ResourceValue` no modo sensível — são os campos que transportam
// os ARGUMENTOS do modelo, e esses podem ter PII. A reversibilidade não é um argumento: é uma
// declaração do REGISTRY sobre a tool, igual para todas as invocações. Redigi-la reporia
// exactamente o defeito, e só no ambiente onde a privacidade está ligada — o pior sítio para um
// comportamento divergir.
func TestReversibilidadeNaoERedigidaComoPII(t *testing.T) {
	resp := agentruntime.ModelResponse{
		ToolCalls: []agentruntime.ToolInvocation{{
			ToolID:        "doc_read",
			Capability:    "cap:fs.read",
			ResourceValue: "doc://segredo-do-titular",
			Reversibility: "reversible",
			Input:         []byte(`{"doc_id":"segredo"}`),
		}},
	}
	c := &EventStoreCapturer{sensitive: true}
	rc := c.encodeResponse(resp)
	if len(rc.ToolCalls) != 1 {
		t.Fatalf("tool calls: %d", len(rc.ToolCalls))
	}

	if rc.ToolCalls[0].Reversibility != "reversible" {
		t.Errorf("a reversibilidade foi REDIGIDA no modo sensivel (%q) — a retoma voltaria a "+
			"classificar tudo como danger, e so nos nos onde a privacidade esta ligada",
			rc.ToolCalls[0].Reversibility)
	}
	// CONTROLO: o que É PII continua redigido. Se este ramo falhar, a "correcção" acima teria
	// sido obtida desligando a redacção — que é um defeito muito pior do que o corrigido.
	//
	// A redacção SUBSTITUI por uma referência `sha256:…`, não esvazia — a primeira versão deste
	// controlo exigia campo vazio e acusou o sistema de não redigir quando redigia. O que se
	// verifica é o que importa: o segredo não sobrevive em lado nenhum.
	if v := rc.ToolCalls[0].ResourceValue; strings.Contains(v, "segredo-do-titular") || !strings.HasPrefix(v, "sha256:") {
		t.Errorf("ResourceValue nao foi redigido: %q", v)
	}
	if in := string(rc.ToolCalls[0].Input); strings.Contains(in, "segredo") {
		t.Errorf("o Input manteve o argumento em claro: %q", in)
	}
}
