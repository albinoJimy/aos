package main

// aos553_entrega_da_referencia_test.go — A METADE DO `aos-orq` DA TRAVESSIA (AOS-553, ADR-045 §2.5).
//
// O `aos-orq` não resolve nada: o que ele decide é SE entrega a referência, e a condição é o
// anúncio do nó. Os dois sentidos têm teste, e o controlo negativo do AOS-556 — «o prefixo não é
// autoridade» — continua a valer por cima da travessia.

import (
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos553Referencia é a referência que o kernel designa num run da 1.8.0: o corpo do resultado foi
// selado e substituído por ela, pelo que a âncora hasheia a PRÓPRIA referência.
func aos553Referencia(run string) string {
	return agentruntime.HandleRefPrefix + agentruntime.MintHandleToken(run, "sha256:"+strings.Repeat("a", 64), 1)
}

// aos553Estado é a resposta de um nó a um run concluído que designou a referência.
func aos553Estado(servido string, mexer func(*agentruntime.OutputSource)) estadoDoRun {
	a := &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds,
		State: agentruntime.OutputSourceDesignated, StepID: "step-000001-tool-1",
		Digest: digestDoConteudo(servido), Bytes: len(servido)}
	if mexer != nil {
		mexer(a)
	}
	return estadoDoRun{RunID: aos501RunDoLeitor, Status: "completed", Terminated: true,
		FinalText: aos501TextoFinal, OutputSource: a, Output: &servido}
}

// TestAOS553_AEntregaDaReferenciaSegueOAnuncioDoNo: a MESMA resposta do nó dá dois desfechos, e o
// que os separa é o anúncio. Sem ele, a causa nomeada do AOS-556; com ele, a referência entregue
// INTEIRA, para o consumidor a resolver.
func TestAOS553_AEntregaDaReferenciaSegueOAnuncioDoNo(t *testing.T) {
	ref := aos553Referencia(aos501RunDoLeitor)
	st := aos553Estado(ref, nil)

	// SEM o anúncio: o comportamento do AOS-556, byte a byte.
	ent, causa := entregaDoRun("doc_read", aos501RunDoLeitor, st, false)
	if causa != causaOrigemPorHandle {
		t.Fatalf("sem anuncio: causa=%q (entrega %+v); quero %q", causa, ent, causaOrigemPorHandle)
	}
	if ent != (entregaPorReferencia{}) {
		t.Error("com causa nao ha entrega")
	}

	// COM o anúncio: entrega-se a referência inteira, com a forma própria e a âncora do produtor.
	ent, causa = entregaDoRun("doc_read", aos501RunDoLeitor, st, true)
	if causa != "" {
		t.Fatalf("com anuncio: causa=%q", causa)
	}
	if ent.conteudo != ref {
		t.Fatalf("o que se entrega = %q; quero a referencia INTEIRA %q (o consumidor confere-a contra a ancora)", ent.conteudo, ref)
	}
	if ent.extraccao != plannerevents.PayloadExtractionHandleRef {
		t.Errorf("extraccao = %q; quero %q — quem audita tem de distinguir uma aresta atravessada de uma entrega de conteudo",
			ent.extraccao, plannerevents.PayloadExtractionHandleRef)
	}
	if ent.digestDaAncora != digestDoConteudo(ref) || ent.bytesDaAncora != len(ref) || ent.passo != "step-000001-tool-1" {
		t.Errorf("a ancora da entrega nao e a selada: %+v", ent)
	}
	// O TEXTO FINAL NÃO ENTRA, como em qualquer entrega por referência.
	if strings.Contains(ent.conteudo, aos501TextoFinal) {
		t.Error("o texto final entrou na entrega")
	}
	// E O DIGEST DO PAYLOAD É O DA ÂNCORA — é o que o evento de publicação exige desta forma.
	if digestDoConteudo(ent.conteudo) != ent.digestDaAncora {
		t.Error("o digest do payload entregue tem de ser o da ancora")
	}
	// A causa continua no vocabulário fechado, e a forma nova também.
	var achou bool
	for _, c := range causasDaOrigem {
		achou = achou || c == causaOrigemPorHandle
	}
	if !achou {
		t.Error("causaOrigemPorHandle saiu do vocabulario fechado")
	}
}

// TestAOS553_OAnuncioNaoRelaxaAsOutrasGarantias: o anúncio só decide o passo da referência. Uma
// resposta que falha um passo ANTERIOR continua a falhar por ele — o anúncio não é porta de serviço.
func TestAOS553_OAnuncioNaoRelaxaAsOutrasGarantias(t *testing.T) {
	ref := aos553Referencia(aos501RunDoLeitor)
	casos := map[string]struct {
		mexer    func(*agentruntime.OutputSource)
		outroRun bool
		causa    string
	}{
		"outro run":              {nil, true, causaOrigemSemVinculo},
		"so medicao":             {func(a *agentruntime.OutputSource) { a.Binding = agentruntime.OutputSourceMeasure }, false, causaOrigemSemVinculo},
		"outra tool":             {func(a *agentruntime.OutputSource) { a.Tool = "outra_tool" }, false, causaOrigemSemVinculo},
		"ancora que nao confere": {func(a *agentruntime.OutputSource) { a.Bytes = len(ref) + 1 }, false, causaOrigemNaoConfere},
		// OS CAMPOS DA CHAMADA SÓ EXISTEM EM `designated` (ADR-038 §2.3): uma âncora `missing` que
		// os levasse não é bem formada, e a causa seria a do vínculo. A fixture tem de os tirar,
		// senão o caso media outra coisa.
		"origem em falta": {func(a *agentruntime.OutputSource) {
			a.State, a.StepID, a.Digest, a.Bytes = agentruntime.OutputSourceMissing, "", "", 0
		}, false, causaOrigemEmFalta},
		"origem ambigua": {func(a *agentruntime.OutputSource) {
			a.State, a.StepID, a.Digest, a.Bytes = agentruntime.OutputSourceAmbiguous, "", "", 0
		}, false, causaOrigemAmbigua},
	}
	for nome, c := range casos {
		st := aos553Estado(ref, c.mexer)
		if c.outroRun {
			st.RunID = "outro-run"
		}
		if _, causa := entregaDoRun("doc_read", aos501RunDoLeitor, st, true); causa != c.causa {
			t.Errorf("%s COM o anuncio: causa=%q; quero %q", nome, causa, c.causa)
		}
	}
	// E uma resposta sem bytes servidos: o anúncio não os inventa.
	st := aos553Estado(ref, nil)
	st.Output, st.OutputOmitted = nil, "unavailable"
	if _, causa := entregaDoRun("doc_read", aos501RunDoLeitor, st, true); causa != causaOrigemIndisponivel {
		t.Errorf("sem bytes servidos: causa=%q; quero %q", causa, causaOrigemIndisponivel)
	}
}

// TestAOS553_OPrefixoContinuaANaoSerAutoridade é o controlo negativo do AOS-556 por cima da
// travessia: um documento legítimo que COMECE pelos bytes do prefixo e não seja um token não é uma
// referência, e entrega-se como CONTEÚDO — com o anúncio ligado ou desligado. Tratá-lo como
// referência mandava-o atravessar, e o consumidor fechava o nó do plano sobre conteúdo válido.
func TestAOS553_OPrefixoContinuaANaoSerAutoridade(t *testing.T) {
	documento := agentruntime.HandleRefPrefix + "isto nao e um token, e um documento que comeca assim"
	for _, travessia := range []bool{false, true} {
		ent, causa := entregaDoRun("doc_read", aos501RunDoLeitor, aos553Estado(documento, nil), travessia)
		if causa != "" {
			t.Fatalf("travessia=%v: um documento que comeca pelo prefixo deu causa %q", travessia, causa)
		}
		if ent.conteudo != documento || ent.extraccao != plannerevents.PayloadExtractionRaw {
			t.Errorf("travessia=%v: entregou-se %+v; quero o documento cru com extraccao raw", travessia, ent)
		}
	}
}

// TestAOS553_FromRunSegueAsTresCondicoes — o campo `from_run` abre a travessia do outro lado, pelo
// que o que o põe lá tem de ser medido. As DUAS primeiras condições são achados da revisão
// adversarial deste ticket: sem a do anúncio, um `aos-orq` novo contra um nó antigo manda um campo
// que o `POST /runs` dele recusa com 400; sem a da publicação por referência, o `from_run` ia numa
// aresta cujo payload é o texto final de um modelo — que um documento untrusted pode ter ditado.
func TestAOS553_FromRunSegueAsTresCondicoes(t *testing.T) {
	// A bancada: três arestas, uma por caso. O run produtor do mapa é DIFERENTE do que o
	// `runDoNo` daria, de propósito — é isso que prova que o valor vem do EVENTO e não de uma
	// re-derivação (a primeira versão deste teste comparava `runDoNo` com ele mesmo).
	const runDoEvento = "plan-553~produtor_ref~7"
	bancada := func(anuncia bool) *executorDeNos {
		e := &executorDeNos{runID: "plan-553", tentativas: map[string]int{}, travessiaPorHandle: anuncia,
			payloads: map[chaveDePayload]string{}, runsPorReferencia: map[chaveDePayload]string{}}
		// (a) por referência E com forma de referência.
		e.payloads[chaveDePayload{no: "produtor_ref", output: "doc"}] = aos553Referencia(runDoEvento)
		e.runsPorReferencia[chaveDePayload{no: "produtor_ref", output: "doc"}] = runDoEvento
		// (b) por referência, mas o conteúdo é CONTEÚDO (produtor fora de um layout por handle).
		e.payloads[chaveDePayload{no: "produtor_cru", output: "doc"}] = "o documento, em claro"
		e.runsPorReferencia[chaveDePayload{no: "produtor_cru", output: "doc"}] = "plan-553~produtor_cru"
		// (c) NÃO por referência, e o texto tem forma de referência — o caso perigoso.
		e.payloads[chaveDePayload{no: "produtor_txt", output: "texto"}] = aos553Referencia("plan-553~produtor_txt")
		return e
	}
	no := plan.Node{NodeID: "consumidor", Consumes: []plan.PayloadEdge{
		{From: "produtor_ref", Output: "doc"},
		{From: "produtor_cru", Output: "doc"},
		{From: "produtor_txt", Output: "texto"},
	}}

	// COM o anúncio: só a aresta (a) leva `from_run`, e leva o run DO EVENTO.
	entradas, err := bancada(true).entradasDe(no)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 3 {
		t.Fatalf("entradas = %d", len(entradas))
	}
	if entradas[0].FromRun != runDoEvento {
		t.Errorf("from_run da aresta por referencia = %q; quero o run DO EVENTO %q", entradas[0].FromRun, runDoEvento)
	}
	if entradas[1].FromRun != "" {
		t.Errorf("uma aresta por referencia que transporta CONTEUDO nao leva from_run; veio %q", entradas[1].FromRun)
	}
	if entradas[2].FromRun != "" {
		t.Errorf("um TEXTO com forma de referencia nao abre a travessia; veio from_run=%q", entradas[2].FromRun)
	}
	// O CAMPO NOVO É ADITIVO: o resto da entrada é byte a byte o de antes em TODAS as arestas — o
	// `digest` continua a ser o do conteúdo que viaja, e o conteúdo não é tocado.
	for i, en := range entradas {
		if quero := digestDoConteudo(en.Content); en.Digest != quero {
			t.Errorf("entrada %d: digest=%q; quero o do conteudo que viaja %q", i, en.Digest, quero)
		}
		if en.From == "" || en.Output == "" {
			t.Errorf("entrada %d: o contrato da aresta perdeu-se: %+v", i, en)
		}
	}
	if entradas[0].Content != aos553Referencia(runDoEvento) {
		t.Error("o conteudo da aresta por referencia foi tocado")
	}

	// SEM o anúncio: nenhuma leva o campo, e o corpo do pedido é byte a byte o de antes.
	entradas, err = bancada(false).entradasDe(no)
	if err != nil {
		t.Fatal(err)
	}
	for i, en := range entradas {
		if en.FromRun != "" {
			t.Errorf("sem o anuncio do no, a entrada %d levou from_run=%q — o POST /runs dele responde 400", i, en.FromRun)
		}
	}
}

// TestAOS553_OAnuncioDoNoLeSeDoGETTools: o anúncio é opcional e aditivo — um nó anterior a este
// ticket não o envia, e a postura fica a do AOS-556.
func TestAOS553_OAnuncioDoNoLeSeDoGETTools(t *testing.T) {
	casos := map[string]struct {
		corpo map[string]any
		quero bool
	}{
		"no anterior":                    {map[string]any{"tools": []any{}}, false},
		"anuncio presente":               {map[string]any{"tools": []any{}, "handle_traversal": map[string]bool{"plan_input": true}}, true},
		"anuncio presente a dizer falso": {map[string]any{"tools": []any{}, "handle_traversal": map[string]bool{"plan_input": false}}, false},
	}
	for nome, c := range casos {
		if got := aos542Anuncio(t, c.corpo).travessiaPorHandle; got != c.quero {
			t.Errorf("%s: travessiaPorHandle=%v; quero %v", nome, got, c.quero)
		}
	}
}
