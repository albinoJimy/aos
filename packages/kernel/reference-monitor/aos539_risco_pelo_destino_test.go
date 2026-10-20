package referencemonitor

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-539 — o egress e a sensibilidade do recurso que o registo de tools declara chegam ao
// classificador de risco. O egress declarado só ELEVA o derivado da capability; a sensibilidade
// declarada substitui o vazio que o loop entrega; sem ela, o vazio continua a ler-se sensível.

// aos539Destinos são os destinos do manifesto de produção (com a sensibilidade da pasta semeada
// declarada) e as formas que a regra tem de tratar: o mesmo OCR sem sensibilidade declarada, uma
// leitura local sem ela, e uma sensibilidade fora do vocabulário.
func aos539Destinos() ToolDestinations {
	return NewToolDestinations(map[string]ToolDestination{
		"doc_read":   {Capability: "cap:fs.read", Egress: "none", Mutation: "none", Reversibility: "reversible", Sensitivity: "public"},
		"ocr_read":   {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible", Sensitivity: "public"},
		"video_read": {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible", Sensitivity: " Public "},
		// O mesmo OCR sem a sensibilidade do recurso: egress externo de um recurso que ninguém
		// classificou ⇒ sensível ⇒ danger.
		"ocr_sem_sensibilidade": {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible"},
		"doc_sem_sensibilidade": {Capability: "cap:fs.read", Egress: "none", Mutation: "none", Reversibility: "reversible"},
		"ocr_interno":           {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible", Sensitivity: "internal"},
		"ocr_sensivel":          {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible", Sensitivity: "sensitive"},
		// Fora do vocabulário (o cmd/aos recusa-o no arranque; aqui prova-se que, se chegasse, não
		// valia como declaração).
		"ocr_ilegivel": {Capability: "cap:fs.read", Egress: "external", Mutation: "none", Reversibility: "reversible", Sensitivity: "publico"},
		// A sensibilidade só vale para uma leitura: numa tool de rede ou que muta é ignorada.
		"web_post":              {Capability: "cap:http.post", Egress: "external", Mutation: "mutates", Sensitivity: "public"},
		"get_dito_leitura":      {Capability: "cap:http.get", Egress: "external", Mutation: "none", Reversibility: "reversible", Sensitivity: "public"},
		"fs_write_publico":      {Capability: "cap:fs.write", Egress: "none", Mutation: "mutates", Reversibility: "reversible", Sensitivity: "public"},
		"doc_mutacao_implicita": {Capability: "cap:fs.read", Egress: "none", Reversibility: "reversible", Sensitivity: "public"},
	})
}

// aos539Classe classifica UMA call (contexto trusted, reversível) com o anotador de risco, com ou
// sem os destinos, e com o texto de sensibilidade dado (o loop entrega-o vazio).
func aos539Classe(t *testing.T, dests *ToolDestinations, tool, capability, sens string) string {
	t.Helper()
	c := NewRiskClassifier(nil)
	if dests != nil {
		c = c.WithToolDestinations(*dests)
	}
	call := &Call{ToolID: tool, Capability: capability, Resource: Resource{Type: "file", Value: "doc://x"},
		Context: CallContext{Taint: taint.StringTrusted, Reversibility: "reversible", Sensitivity: sens}}
	if _, err := c.Evaluate(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	return call.Context.RiskClass
}

func TestAOS539_ClasseDeRiscoPeloDestinoDeclarado(t *testing.T) {
	d := aos539Destinos()
	casos := []struct {
		tool, capability, antes, depois string
	}{
		// Produção: a ocr_read e a video_read deixam de ser locais e ficam gray (não danger), porque
		// a pasta é declarada pública; a doc_read passa a safe (local, pública), como no aos-orq.
		{"ocr_read", "cap:fs.read", "gray", "gray"},
		{"video_read", "cap:fs.read", "gray", "gray"},
		{"doc_read", "cap:fs.read", "gray", "safe"},
		// Sem sensibilidade declarada, egress externo ⇒ danger (fail-closed).
		{"ocr_sem_sensibilidade", "cap:fs.read", "gray", "danger"},
		// Sem sensibilidade declarada e local ⇒ o de antes.
		{"doc_sem_sensibilidade", "cap:fs.read", "gray", "gray"},
		{"ocr_interno", "cap:fs.read", "gray", "gray"},
		{"ocr_sensivel", "cap:fs.read", "gray", "danger"},
		// Valor fora do vocabulário não vale como declaração ⇒ sensível.
		{"ocr_ilegivel", "cap:fs.read", "gray", "danger"},
		// Numa tool de rede, ou que muta, ou com a mutação implícita, a sensibilidade declarada não
		// vale: declarar a pasta pública não baixa um web_post de danger nem faz uma escrita safe.
		{"web_post", "cap:http.post", "danger", "danger"},
		{"get_dito_leitura", "cap:http.get", "danger", "danger"},
		{"fs_write_publico", "cap:fs.write", "gray", "gray"},
		{"doc_mutacao_implicita", "cap:fs.read", "gray", "gray"},
		// Capability diferente da do binding: os eixos de antes.
		{"ocr_read", "cap:mem.read", "gray", "gray"},
		// Tool sem entrada: os eixos de antes.
		{"desconhecida", "cap:fs.read", "gray", "gray"},
	}
	for _, c := range casos {
		if got := aos539Classe(t, nil, c.tool, c.capability, ""); got != c.antes {
			t.Errorf("%s/%s sem destinos: %q, queria %q", c.tool, c.capability, got, c.antes)
		}
		if got := aos539Classe(t, &d, c.tool, c.capability, ""); got != c.depois {
			t.Errorf("%s/%s com destinos: %q, queria %q", c.tool, c.capability, got, c.depois)
		}
	}
}

// O texto de sensibilidade da call, quando NÃO vazio, só ELEVA a declarada: «public» declarado com
// «sensitive» no contexto é sensível; um texto ilegível é o topo; «public» no contexto não baixa
// um recurso declarado sensível.
func TestAOS539_TextoDaCallSoElevaASensibilidadeDeclarada(t *testing.T) {
	d := aos539Destinos()
	for _, c := range []struct{ tool, sens, quer string }{
		{"ocr_read", "sensitive", "danger"},
		{"ocr_read", "lixo", "danger"},
		{"ocr_read", "public", "gray"},
		{"ocr_sensivel", "public", "danger"},
		{"doc_read", "internal", "safe"},
		{"doc_read", "pii", "gray"},
	} {
		if got := aos539Classe(t, &d, c.tool, "cap:fs.read", c.sens); got != c.quer {
			t.Errorf("%s com sensibilidade %q no contexto: %q, queria %q", c.tool, c.sens, got, c.quer)
		}
	}
}

// O egress declarado só ELEVA: um `none` declarado numa capability de rede não a torna local; um
// `internal` declarado numa capability local sobe-a.
func TestAOS539_EgressDeclaradoSoEleva(t *testing.T) {
	d := NewToolDestinations(map[string]ToolDestination{
		"post_dito_local": {Capability: "cap:http.post", Egress: "none", Mutation: "none", Reversibility: "reversible"},
		"leitura_interna": {Capability: "cap:fs.read", Egress: "internal", Mutation: "none", Reversibility: "reversible"},
	})
	if got := aos539Classe(t, &d, "post_dito_local", "cap:http.post", "internal"); got != "gray" {
		t.Fatalf("egress none declarado baixou uma capability de rede: %q", got)
	}
	if got := aos539Classe(t, nil, "leitura_interna", "cap:fs.read", "internal"); got != "safe" {
		t.Fatalf("controlo: leitura local interna sem destinos devia ser safe, veio %q", got)
	}
	if got := aos539Classe(t, &d, "leitura_interna", "cap:fs.read", "internal"); got != "gray" {
		t.Fatalf("egress internal declarado nao elevou: %q", got)
	}
	for _, c := range []struct{ derived, declared, quer risk.Egress }{
		{risk.EgressNone, risk.EgressExternal, risk.EgressExternal},
		{risk.EgressNone, risk.EgressInternal, risk.EgressInternal},
		{risk.EgressNone, risk.EgressUnknown, risk.EgressNone},
		{risk.EgressInternal, risk.EgressNone, risk.EgressInternal},
		{risk.EgressExternal, risk.EgressNone, risk.EgressExternal},
		{risk.EgressUnknown, risk.EgressNone, risk.EgressUnknown},
	} {
		if got := raiseEgress(c.derived, c.declared); got != c.quer {
			t.Errorf("raiseEgress(%v,%v)=%v, queria %v", c.derived, c.declared, got, c.quer)
		}
	}
}

// O RiskGate classifica IGUAL ao anotador com os mesmos destinos (a anotação é reescrita pelo gate a
// jusante e não pode mudar de valor).
func TestAOS539_RiskGateClassificaIgualAoAnotador(t *testing.T) {
	d := aos539Destinos()
	g := NewRiskGate(nil, nil).WithToolDestinations(d)
	for _, tool := range []string{"doc_read", "ocr_read", "ocr_sem_sensibilidade", "doc_sem_sensibilidade"} {
		call := &Call{ToolID: tool, Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://x"},
			Context: CallContext{Taint: taint.StringTrusted, Reversibility: "reversible"}}
		_, _ = g.Evaluate(context.Background(), call)
		if quer := aos539Classe(t, &d, tool, "cap:fs.read", ""); call.Context.RiskClass != quer {
			t.Errorf("%s: o RiskGate anotou %q e o anotador %q", tool, call.Context.RiskClass, quer)
		}
	}
}

// A declaração da sensibilidade só vale para uma leitura: mutação «none» explícita e nem a
// capability nem o tipo de recurso denunciam egress.
func TestAOS539_SensibilidadeSoValeParaUmaLeitura(t *testing.T) {
	for _, c := range []struct {
		d     ToolDestination
		tipo  string
		vale  bool
		razao string
	}{
		{ToolDestination{Capability: "cap:fs.read", Mutation: "none"}, "file", true, "leitura local"},
		{ToolDestination{Capability: "cap:fs.read", Mutation: " NONE "}, "file", true, "normalizada"},
		{ToolDestination{Capability: "cap:fs.read", Mutation: ""}, "file", false, "mutação implícita"},
		{ToolDestination{Capability: "cap:fs.read", Mutation: "mutates"}, "file", false, "muta"},
		{ToolDestination{Capability: "cap:http.get", Mutation: "none"}, "file", false, "capability de rede"},
		{ToolDestination{Capability: "cap:fs.read", Mutation: "none"}, "http", false, "recurso de rede"},
		{ToolDestination{Capability: "cap:s3.upload", Mutation: "none"}, "file", false, "capability indeterminada"},
	} {
		if got := c.d.ResourceSensitivityApplies(c.tipo); got != c.vale {
			t.Errorf("%s: ResourceSensitivityApplies=%v, queria %v", c.razao, got, c.vale)
		}
	}
	// Um recurso de rede na call desfaz a declaração mesmo com o binding local.
	d := aos539Destinos()
	call := &Call{ToolID: "ocr_read", Capability: "cap:fs.read", Resource: Resource{Type: "http", Value: "https://x"},
		Context: CallContext{Taint: taint.StringTrusted, Reversibility: "reversible"}}
	if _, err := NewRiskClassifier(nil).WithToolDestinations(d).Evaluate(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if call.Context.RiskClass != "danger" {
		t.Fatalf("ocr_read com recurso de rede: %q, queria danger (a pasta declarada não classifica um URL)", call.Context.RiskClass)
	}
}

// A matriz completa da sensibilidade declarada × egress declarado × taint (achado da revisão:
// sem ela, a ordem piso→taint e a absorção de um degrau do taint pela declaração não estavam
// fixadas). Com o contexto untrusted o taint eleva UM nível DEPOIS do piso do egress externo:
//   - egress none: public⇒internal (safe), internal⇒sensitive (gray), sensitive (gray);
//   - egress external: tudo ⇒ sensitive ⇒ danger.
//
// A declaração absorve, portanto, um degrau do aumento do taint: a doc_read untrusted era gray
// (vazio já é o topo) e passa a safe; a ocr_read/video_read untrusted passam de gray a danger
// (só endurecimento — o gate de taint já as nega ou escala).
func TestAOS539_MatrizSensibilidadeEgressTaint(t *testing.T) {
	quer := map[string]map[string]map[string]string{ // egress → sensibilidade → taint → classe
		"none": {
			"public":    {"trusted": "safe", "untrusted": "safe"},
			"internal":  {"trusted": "safe", "untrusted": "gray"},
			"sensitive": {"trusted": "gray", "untrusted": "gray"},
			"":          {"trusted": "gray", "untrusted": "gray"},
		},
		"external": {
			"public":    {"trusted": "gray", "untrusted": "danger"},
			"internal":  {"trusted": "gray", "untrusted": "danger"},
			"sensitive": {"trusted": "danger", "untrusted": "danger"},
			"":          {"trusted": "danger", "untrusted": "danger"},
		},
	}
	for eg, porSens := range quer {
		for sens, porTaint := range porSens {
			d := NewToolDestinations(map[string]ToolDestination{
				"t": {Capability: "cap:fs.read", Egress: eg, Mutation: "none", Reversibility: "reversible", Sensitivity: sens},
			})
			for tn, q := range porTaint {
				lbl := taint.StringTrusted
				if tn == "untrusted" {
					lbl = taint.StringUntrusted
				}
				call := &Call{ToolID: "t", Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://x"},
					Context: CallContext{Taint: lbl, Reversibility: "reversible"}}
				if _, err := NewRiskClassifier(nil).WithToolDestinations(d).Evaluate(context.Background(), call); err != nil {
					t.Fatal(err)
				}
				if call.Context.RiskClass != q {
					t.Errorf("egress %s, sensibilidade %q, %s: %q, queria %q", eg, sens, tn, call.Context.RiskClass, q)
				}
			}
		}
	}
}

// Uma call com o nome de uma tool registada mas OUTRA capability (local) fica com os eixos de antes,
// e aqui a classe MUDA com isso: a doc_read declarada pública é safe com a capability do binding e
// gray com `cap:fs.list` (sem destino, vazio ⇒ sensível).
func TestAOS539_CapabilityDivergenteFicaComOsEixosDeAntes(t *testing.T) {
	d := aos539Destinos()
	if got := aos539Classe(t, &d, "doc_read", "cap:fs.read", ""); got != "safe" {
		t.Fatalf("controlo: doc_read com a capability do binding devia ser safe, veio %q", got)
	}
	if got := aos539Classe(t, &d, "doc_read", "cap:fs.list", ""); got != "gray" {
		t.Fatalf("doc_read com capability divergente devia ficar com os eixos de antes (gray), veio %q", got)
	}
	// E a ocr_read com capability local divergente perde o egress declarado: com a sensibilidade de
	// texto «public» é safe (local) e não gray.
	if got := aos539Classe(t, &d, "ocr_read", "cap:fs.list", "public"); got != "safe" {
		t.Fatalf("ocr_read com capability divergente nao pode herdar o egress declarado: %q", got)
	}
}
