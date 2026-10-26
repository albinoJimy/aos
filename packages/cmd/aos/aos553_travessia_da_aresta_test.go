package main

// aos553_travessia_da_aresta_test.go — A TRAVESSIA DA ARESTA DO PLANO POR HANDLE (AOS-553, ADR-045).
//
// Cada uma das SEIS conferências do ADR-045 §2.1 tem controlo negativo próprio, e as duas que
// decidem o desenho têm teste dedicado: o handle INTERMÉDIO do produtor (que a âncora não designa)
// e o CROSS-TITULAR recusado por nome e não por erro de decifra.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

const (
	aos553Pedido   = "plan-553"
	aos553NoProd   = "ler_documento"
	aos553NoCons   = "extrair_accoes"
	aos553Titular  = "titular-553"
	aos553Conteudo = "o documento que o no 1 leu"
)

// aos553Desfechos é o [leitorDoDesfecho] da bancada: um desfecho por run, posto à mão. Determinista
// e sem relógio.
type aos553Desfechos struct {
	por map[string]state.Outcome
	err error
	// estado é o que o fake devolve para um run que conhece. Vazio ⇒ [state.Complete], que é o
	// único em que o `aos-orq` entrega.
	estado state.State
}

func (d aos553Desfechos) DurableOutcome(_ context.Context, runID string) (state.State, state.Outcome, error) {
	if d.err != nil {
		return "", state.Outcome{}, d.err
	}
	oc, ok := d.por[runID]
	if !ok {
		return "", state.Outcome{}, nil
	}
	if d.estado != "" {
		return d.estado, oc, nil
	}
	return state.Complete, oc, nil
}

// aos553Ancora é a âncora que o kernel sela num run da 1.8.0: designada, vinculativa, e a hashear a
// PRÓPRIA REFERÊNCIA — que é o que o step-ledger grava quando o produtor sela o resultado.
func aos553Ancora(ref string) state.Outcome {
	return state.Outcome{OutputSource: &agentruntime.OutputSource{
		Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceDesignated,
		StepID: "step-000001-tool-1", Digest: "sha256:" + digestHex([]byte(ref)), Bytes: len(ref),
	}}
}

// aos553Bancada sela `aos553Conteudo` no run produtor e devolve tudo o que a travessia precisa: o
// plano de handles, o espaço, a pasta, o run produtor, a referência selada e os desfechos.
func aos553Bancada(t *testing.T) (*planoDeHandles, *armazemDeHandles, string, string, string, *aos553Desfechos) {
	t.Helper()
	plano, esp, _, pasta := aos550Plano(t)
	produtor := idDoRunFilho(aos553Pedido, aos553NoProd)
	p, causa := esp.selar(context.Background(), aos553Titular, produtor, "step-000001-tool-1", "doc_read", "", []byte(aos553Conteudo))
	if causa != "" {
		t.Fatalf("pre-condicao: o produtor nao selou: %s", causa)
	}
	ref := agentruntime.HandleRefPrefix + p.Token
	d := &aos553Desfechos{por: map[string]state.Outcome{produtor: aos553Ancora(ref)}}
	return plano, esp, pasta, produtor, ref, d
}

// aos553Goal é o Goal do run CONSUMIDOR, com uma entrada que chega por REFERÊNCIA — o que o
// `aos-orq` entrega quando o produtor correu num layout por handle.
func aos553Goal(ref string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID:   idDoRunFilho(aos553Pedido, aos553NoCons),
		Subject: aos553Titular,
		Inputs: []agentruntime.PlanInput{{
			From: aos553NoProd, Output: "documento",
			Digest: "sha256:" + digestHex([]byte(ref)), Content: []byte(ref),
		}},
	}
}

// TestAOS553_ATravessiaEntregaOConteudoDoProdutorEReSelaNoConsumidor é o caso bom, ponta a ponta no
// nó: a entrada chega como referência, a travessia resolve-a do armazém do run produtor, e o selo
// das entradas (AOS-551) re-sela os bytes no espaço DESTE run com o SEU token.
//
// E prova que as três barreiras continuam a valer depois da travessia: o token do produtor NÃO
// resolve no run consumidor, e o blob do produtor fica onde estava.
func TestAOS553_ATravessiaEntregaOConteudoDoProdutorEReSelaNoConsumidor(t *testing.T) {
	ctx := context.Background()
	plano, esp, pasta, produtor, ref, d := aos553Bancada(t)
	goal := aos553Goal(ref)

	depois, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, goal)
	if err != nil {
		t.Fatalf("a travessia recusou o caso bom: %v", err)
	}
	if string(depois.Inputs[0].Content) != aos553Conteudo {
		t.Fatalf("o conteudo entregue = %q; quero %q", depois.Inputs[0].Content, aos553Conteudo)
	}
	// O DIGEST PASSA A SER O DO CONTEÚDO: deixá-lo a nomear a referência era o rótulo
	// `plan_input_digest` a desmentir os bytes ao lado.
	if quero := "sha256:" + digestHex([]byte(aos553Conteudo)); depois.Inputs[0].Digest != quero {
		t.Errorf("o digest da entrada = %q; quero o do CONTEUDO %q", depois.Inputs[0].Digest, quero)
	}
	if esp.lido(tTravessiaFeita) != 1 {
		t.Errorf("travessia_feita = %d; quero 1", esp.lido(tTravessiaFeita))
	}
	// O Goal do chamador não foi mexido: a travessia copia.
	if string(goal.Inputs[0].Content) != ref {
		t.Error("a travessia mexeu no Goal do chamador")
	}

	// O SELO DAS ENTRADAS re-sela no run consumidor, com o SEU token.
	depois.AssemblyVersion = agentruntime.AssemblyVersion180
	selado, serr := selarEntradasDoPlano(ctx, plano, depois)
	if serr != nil {
		t.Fatalf("o selo das entradas recusou depois da travessia: %v", serr)
	}
	tokenDoConsumidor, ok := agentruntime.ParseHandleRef(selado.Inputs[0].Handle)
	if !ok {
		t.Fatalf("a entrada ficou sem handle do consumidor: %q", selado.Inputs[0].Handle)
	}
	tokenDoProdutor, _ := agentruntime.ParseHandleRef(ref)
	if tokenDoConsumidor == tokenDoProdutor {
		t.Fatal("o token foi REUSADO: MintHandleToken liga-o a (run, digest, ordinal), pelo que o do consumidor tem de ser outro")
	}
	// O consumidor abre o SEU, e o run do consumidor NÃO abre pelo token do produtor.
	q, qerr := plano.quarentenaDoRun(ctx, aos553Titular, depois.RunID, "", "")
	if qerr != nil {
		t.Fatal(qerr)
	}
	if h, ok := q.Lookup(tokenDoProdutor); ok {
		t.Errorf("o token do PRODUTOR existe na quarentena do CONSUMIDOR (%v): a barreira do evento caiu", h.Ref())
	}
	if h, ok := q.Lookup(tokenDoConsumidor); !ok {
		t.Fatal("o token do consumidor nao esta na sua propria quarentena")
	} else if v, rerr := q.Resolve(ctx, h); rerr != nil || string(v.Payload()) != aos553Conteudo {
		t.Errorf("o consumidor nao resolve o SEU handle: %q %v", v.Payload(), rerr)
	}
	// E o blob do produtor continua lá, intacto: a travessia só lê.
	if b, aerr := esp.abrir(ctx, aos553Titular, produtor, "sha256:"+digestHex([]byte(aos553Conteudo))); aerr != nil || string(b) != aos553Conteudo {
		t.Errorf("o blob do produtor nao sobreviveu a travessia: %q %v", b, aerr)
	}
	// Duas cópias em disco, uma por run — a consequência declarada do ADR-045 §2.2.
	hexd := digestHex([]byte(aos553Conteudo))
	for _, run := range []string{produtor, depois.RunID} {
		if _, serr := os.Stat(filepath.Join(pasta, pastaDoRun(run), hexd)); serr != nil {
			t.Errorf("falta o blob do run %s: %v", run, serr)
		}
	}
}

// TestAOS553_ForaDoCasoNaoTocaEmNada: um Goal sem entradas, ou com entradas de CONTEÚDO, sai tal
// como entrou — sem uma leitura e sem uma alocação. É o que mantém o caminho de sempre inalterado,
// e o que torna a travessia inerte num nó que não habilite o layout por handle.
func TestAOS553_ForaDoCasoNaoTocaEmNada(t *testing.T) {
	ctx := context.Background()
	plano, esp, _, _, _, _ := aos553Bancada(t)
	// Sem plano de handles e sem desfechos: se a travessia os tocasse, isto estourava.
	for nome, goal := range map[string]agentruntime.Goal{
		"sem entradas": {RunID: "run-x", Subject: aos553Titular},
		"com conteudo": {RunID: "run-x", Subject: aos553Titular, Inputs: []agentruntime.PlanInput{
			{From: "a", Output: "o", Digest: "sha256:" + digestHex([]byte("texto")), Content: []byte("texto")}}},
		// O CONTROLO NEGATIVO DA FORMA: começa pelo prefixo e NÃO é um token. O prefixo não é
		// autoridade (o kernel di-lo), e um documento lido pode começar por aqueles bytes: isto é
		// conteúdo legítimo e tem de atravessar a função sem ser tocado.
		"comeca pelo prefixo": {RunID: "run-x", Subject: aos553Titular, Inputs: []agentruntime.PlanInput{
			{From: "a", Output: "o", Content: []byte(agentruntime.HandleRefPrefix + "isto nao e um token")}}},
	} {
		depois, err := atravessarArestasDoPlano(ctx, nil, nil, "", nil, goal)
		if err != nil {
			t.Fatalf("%s: a travessia recusou o que nao e seu: %v", nome, err)
		}
		if len(depois.Inputs) != len(goal.Inputs) {
			t.Fatalf("%s: a travessia mexeu nas entradas", nome)
		}
		for i := range goal.Inputs {
			if string(depois.Inputs[i].Content) != string(goal.Inputs[i].Content) || depois.Inputs[i].Digest != goal.Inputs[i].Digest {
				t.Errorf("%s: entrada %d mudou: %+v", nome, i, depois.Inputs[i])
			}
		}
	}
	// E nenhum contador do espaço se mexeu.
	for _, c := range causasDaTravessia() {
		if n := esp.lido(c); n != 0 {
			t.Errorf("%s = %d num caminho que nao e da travessia", c, n)
		}
	}
	_ = plano
}

// TestAOS553_UmTextoComFormaDeReferenciaNaoDECIDE_NADA é a correcção do achado mais sério da
// revisão adversarial deste ticket, e o seu controlo negativo.
//
// A primeira versão abria a travessia pela FORMA do conteúdo. Mas `aos-handle/v1:h<n>.<16 hex>` é
// uma forma que qualquer texto reproduz — o kernel di-lo no sítio da constante —, e um nó anterior
// do plano publica o texto final do seu modelo, que um documento untrusted pode ter ditado. Com a
// forma como porta, esse texto RECUSAVA a admissão do run seguinte: conteúdo untrusted a negar
// serviço, e até em nós sem espaço de handles, que não habilitam nada desta fase.
//
// A porta é a aresta declarada (`inputs[].from_run`). Sem ela, um texto com forma de referência é
// texto, e passa como passava.
func TestAOS553_UmTextoComFormaDeReferenciaNaoDecideNada(t *testing.T) {
	ctx := context.Background()
	plano, esp, _, produtor, ref, d := aos553Bancada(t)

	// O TEXTO PERIGOSO: um token BEM FORMADO — `ParseHandleRef` aceita-o —, e que o produtor deste
	// plano até selou de facto. É o pior caso: nada na forma o distingue de uma aresta real.
	if _, ok := agentruntime.ParseHandleRef(ref); !ok {
		t.Fatal("pre-condicao: o texto do caso tem de ter a forma COMPLETA de uma referencia")
	}

	// SEM ARESTA DECLARADA, nas configurações de nó em que a versão anterior recusava.
	//
	// AS TRÊS LINHAS SAEM TODAS NO MESMO SÍTIO — o retorno antecipado de `declaradas == 0` —, e é
	// isso que elas provam: que esse retorno vem ANTES de tudo o que as distingue. A primeira
	// linha é a mais forte, e por isso passa `nil` no plano E nos desfechos: se o retorno não
	// fosse o primeiro, ela não daria uma recusa — daria um nil dereference. A segunda e a
	// terceira provam que nem a falta do pedido nem a presença dele mudam nada.
	//
	// E a primeira fixa também a ORDEM das duas recusas do run: ela tem
	// `len(produtores)=0 != len(Inputs)=1`, logo uma implementação que conferisse a composição
	// antes do retorno antecipado avermelhava aqui. Era coberta por acidente, e fica dita.
	for nome, caso := range map[string]struct {
		plano      *planoDeHandles
		desfechos  leitorDoDesfecho
		pedido     string
		produtores []string
	}{
		"nem plano nem desfechos (o retorno e o primeiro)": {nil, nil, "", nil},
		"com espaco, sem pedido verificado":                {plano, d, "", []string{""}},
		"com espaco e com pedido verificado":               {plano, d, aos553Pedido, []string{""}},
	} {
		depois, err := atravessarArestasDoPlano(ctx, caso.plano, caso.desfechos, caso.pedido, caso.produtores, aos553Goal(ref))
		if err != nil {
			t.Errorf("%s: um TEXTO com forma de referencia recusou a hospedagem: %v", nome, err)
			continue
		}
		if string(depois.Inputs[0].Content) != ref {
			t.Errorf("%s: o texto foi tocado: %q", nome, depois.Inputs[0].Content)
		}
	}
	// E NADA FOI CONTADO: a travessia não correu.
	for _, c := range causasDaTravessia() {
		if n := esp.lido(c); n != 0 {
			t.Errorf("%s = %d: um texto sem aresta declarada nao e da travessia", c, n)
		}
	}
	// NÃO-VACUIDADE: o MESMO texto, COM a aresta declarada, atravessa. Sem isto o teste acima
	// passaria contra uma travessia que nunca fizesse nada.
	comAresta, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, aos553Goal(ref))
	if err != nil || string(comAresta.Inputs[0].Content) != aos553Conteudo {
		t.Fatalf("com a aresta declarada tinha de atravessar: %q %v", comAresta.Inputs[0].Content, err)
	}
}

// TestAOS553_CadaConferenciaTemControloNegativo — as seis conferências do ADR-045 §2.1, cada uma
// com a sua causa nomeada. Cada caso parte do CASO BOM e muda UMA coisa: é isso que o torna um
// controlo negativo e não um teste que passaria contra qualquer implementação.
func TestAOS553_CadaConferenciaTemControloNegativo(t *testing.T) {
	ctx := context.Background()
	casos := map[string]struct {
		// muda recebe a bancada e devolve o que entra na chamada.
		muda  func(t *testing.T, plano *planoDeHandles, esp *armazemDeHandles, pasta, produtor, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal)
		causa string
	}{
		// (1) Sem pedido de plano verificado não há aresta a atravessar.
		"sem pedido verificado": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, d, "", []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaSemPlano,
		},
		// (2) O run nomeado tem de ser deste pedido e deste nó produtor.
		"run de outro plano": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, _, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, d, aos553Pedido, []string{idDoRunFilho("plan-outro", aos553NoProd)}, aos553Goal(ref)
			},
			causa: tCausaRunForaDoPlano,
		},
		"run de outro no do plano": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, _, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, d, aos553Pedido, []string{idDoRunFilho(aos553Pedido, "outro_no")}, aos553Goal(ref)
			},
			causa: tCausaRunForaDoPlano,
		},
		"run com sufixo que nao e tentativa": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, d, aos553Pedido, []string{produtor + "-extra"}, aos553Goal(ref)
			},
			causa: tCausaRunForaDoPlano,
		},
		// SEM `from_run` NÃO HÁ RECUSA, e por isso não há caso aqui: com a porta corrigida, uma
		// entrada sem aresta declarada é CONTEÚDO e passa — é
		// [TestAOS553_UmTextoComFormaDeReferenciaNaoDecideNada] que o fixa. Este caso existia
		// enquanto a porta era a forma dos bytes, e a sua remoção é parte da correcção.
		// (3) A âncora tem de ser vinculativa e designada.
		"sem ancora": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, _ *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, &aos553Desfechos{por: map[string]state.Outcome{}}, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaSemAncora,
		},
		"ancora em so medicao": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, _ *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				oc := aos553Ancora(ref)
				oc.OutputSource.Binding = agentruntime.OutputSourceMeasure
				return p, &aos553Desfechos{por: map[string]state.Outcome{produtor: oc}}, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaSemAncora,
		},
		"ancora nao designada": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, _ *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				oc := aos553Ancora(ref)
				// BEM FORMADA, de propósito: os campos da chamada só existem em `designated`
				// (ADR-038 §2.3), e deixar lá o `step_id` fazia a recusa vir do `!BemFormada()` —
				// o caso media a conferência do ESTADO, e tem de ser essa a falhar.
				oc.OutputSource.State = agentruntime.OutputSourceMissing
				oc.OutputSource.StepID, oc.OutputSource.Digest, oc.OutputSource.Bytes = "", "", 0
				return p, &aos553Desfechos{por: map[string]state.Outcome{produtor: oc}}, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaSemAncora,
		},
		// O run produtor terminou, com a origem designada, mas NÃO `complete`: a máquina aceita a
		// âncora em qualquer transição terminal, e o `aos-orq` só entrega de um run que concluiu.
		// O nó não pode ser mais permissivo do que quem lhe entrega.
		"produtor terminou failed": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, _ *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, &aos553Desfechos{por: map[string]state.Outcome{produtor: aos553Ancora(ref)}, estado: state.Failed}, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaSemAncora,
		},
		"log ilegivel": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, _ *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, &aos553Desfechos{err: os.ErrPermission}, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaSemAncora,
		},
		// (4) E designa EXACTAMENTE esta referência.
		"ancora designa outra referencia": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, _ *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				outra := agentruntime.HandleRefPrefix + agentruntime.MintHandleToken(produtor, "sha256:"+strings.Repeat("b", 64), 1)
				return p, &aos553Desfechos{por: map[string]state.Outcome{produtor: aos553Ancora(outra)}}, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaNaoDesignada,
		},
		// (5) O par (run, digest) consta de um `handle.created` do stream do produtor.
		"token que nenhum evento declara": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, _ string, _ *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				// Um token BEM FORMADO que o produtor nunca selou, com a âncora a designá-lo: a
				// âncora passa e o guarda do evento recusa.
				fantasma := agentruntime.HandleRefPrefix + agentruntime.MintHandleToken(produtor, "sha256:"+strings.Repeat("c", 64), 9)
				return p, &aos553Desfechos{por: map[string]state.Outcome{produtor: aos553Ancora(fantasma)}}, aos553Pedido, []string{produtor}, aos553Goal(fantasma)
			},
			causa: tCausaSemDeclaracao,
		},
		// (6) O titular vem do evento, e outro titular é recusado POR NOME.
		"outro titular": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				goal := aos553Goal(ref)
				goal.Subject = "titular-alheio"
				return p, d, aos553Pedido, []string{produtor}, goal
			},
			causa: tCausaOutroTitular,
		},
		// A abertura: o blob não está.
		"blob apagado": {
			muda: func(t *testing.T, p *planoDeHandles, _ *armazemDeHandles, pasta, produtor, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				if err := os.Remove(filepath.Join(pasta, pastaDoRun(produtor), digestHex([]byte(aos553Conteudo)))); err != nil {
					t.Fatal(err)
				}
				return p, d, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaIndisponivel,
		},
		// A coerência das arestas que a admissão compôs.
		// O comprimento só se confere com ALGUMA aresta declarada: com a lista vazia não há nada a
		// atravessar e a função retorna antes (é o que a linha 1 do teste do texto fixa). Duas
		// arestas contra uma entrada é o desacordo que o contador existe para ver.
		"arestas de comprimento diferente": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return p, d, aos553Pedido, []string{produtor, produtor}, aos553Goal(ref)
			},
			causa: tCausaComposicao,
		},
		// Quem submete DECLAROU a aresta e o conteúdo não é uma referência: contradição de quem
		// chama, e não ambiguidade.
		"aresta declarada sobre conteudo": {
			muda: func(_ *testing.T, p *planoDeHandles, _ *armazemDeHandles, _, produtor, _ string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				goal := aos553Goal("isto e conteudo, nao uma referencia")
				return p, d, aos553Pedido, []string{produtor}, goal
			},
			causa: tCausaSemReferencia,
		},
		// E o nó sem espaço de handles: uma referência não se resolve, e não se entrega o literal.
		"sem espaco de handles": {
			muda: func(_ *testing.T, _ *planoDeHandles, _ *armazemDeHandles, _, produtor, ref string, d *aos553Desfechos) (*planoDeHandles, leitorDoDesfecho, string, []string, agentruntime.Goal) {
				return nil, d, aos553Pedido, []string{produtor}, aos553Goal(ref)
			},
			causa: tCausaSemEspaco,
		},
	}
	for nome, c := range casos {
		t.Run(nome, func(t *testing.T) {
			plano, esp, pasta, produtor, ref, d := aos553Bancada(t)
			// PRÉ-CONDIÇÃO: o caso bom passa nesta bancada. Sem isto, um caso podia estar a
			// recusar por uma razão que não é a que ele nomeia.
			if _, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, aos553Goal(ref)); err != nil {
				t.Fatalf("pre-condicao: o caso bom tem de passar nesta bancada: %v", err)
			}
			antes := map[string]int64{}
			for _, v := range causasDaTravessia() {
				antes[v] = esp.lido(v)
			}
			p2, d2, pedido, produtores, goal := c.muda(t, plano, esp, pasta, produtor, ref, d)
			depois, err := atravessarArestasDoPlano(ctx, p2, d2, pedido, produtores, goal)
			if err == nil {
				t.Fatalf("a travessia ACEITOU: queria %s", c.causa)
			}
			if !strings.Contains(err.Error(), c.causa) {
				t.Fatalf("erro = %v; queria a causa %s", err, c.causa)
			}
			// A entrada NÃO foi resolvida: o Goal devolvido é o que entrou.
			if len(goal.Inputs) > 0 && string(depois.Inputs[0].Content) != string(goal.Inputs[0].Content) {
				t.Error("com causa nao ha travessia, e a entrada tem de ficar como estava")
			}
			// E o contador da causa subiu — menos no caso sem espaço, que não tem onde contar.
			if c.causa != tCausaSemEspaco {
				if esp.lido(c.causa) != antes[c.causa]+1 {
					t.Errorf("%s nao foi contado (%d -> %d)", c.causa, antes[c.causa], esp.lido(c.causa))
				}
				if esp.lido(tTravessiaFeita) != antes[tTravessiaFeita] {
					t.Error("travessia_feita subiu numa recusa")
				}
			}
		})
	}
}

// TestAOS553_NaoTextoNaoAtravessa — o espaço de handles tem vocabulário ABERTO (binário incluído),
// e desde o AOS-551 o produtor sela o corpo de qualquer tool call permitida. A resolução dentro de
// um run já recusava não-UTF-8 (`handle_nao_textual`), porque o corpo tem de caber numa string
// JSON; a travessia NÃO recusava, e a assimetria foi um achado da revisão de segurança deste
// ticket. O conteúdo que atravessa vai para o `PlanInput.Content` e, num consumidor até à 1.7.0,
// inline para o tail e dali para o pedido ao modelo — a mesma restrição, pela mesma razão.
func TestAOS553_NaoTextoNaoAtravessa(t *testing.T) {
	ctx := context.Background()
	plano, esp, _, produtor, _, _ := aos553Bancada(t)
	// O produtor sela BINÁRIO — que o espaço aceita —, e o kernel designa-o.
	binario := []byte{0x00, 0xff, 0xfe, 'a'}
	p, causa := esp.selar(ctx, aos553Titular, produtor, "step-000002-tool-1", "doc_read", "", binario)
	if causa != "" {
		t.Fatalf("pre-condicao: o espaco de handles tem vocabulario ABERTO e tem de aceitar binario: %s", causa)
	}
	ref := agentruntime.HandleRefPrefix + p.Token
	d := &aos553Desfechos{por: map[string]state.Outcome{produtor: aos553Ancora(ref)}}

	depois, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, aos553Goal(ref))
	if err == nil {
		t.Fatal("conteudo binario atravessou a aresta: o run morria mais tarde, na serializacao do prompt")
	}
	if !strings.Contains(err.Error(), handleCausaNaoTextual) {
		t.Fatalf("erro = %v; queria a causa da resolucao dentro de um run (%s), e nao uma travessia_* nova", err, handleCausaNaoTextual)
	}
	if string(depois.Inputs[0].Content) != ref {
		t.Error("a entrada recusada nao ficou como estava")
	}
	// NÃO-VACUIDADE: o MESMO caminho com conteúdo de texto atravessa. Sem isto o teste passava
	// contra uma travessia que recusasse tudo.
	bom, berr := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, aos553Goal(ref))
	if berr == nil {
		t.Fatal("pre-condicao do controlo: este caso tem de continuar a recusar")
	}
	_ = bom
	textoRef := agentruntime.HandleRefPrefix + agentruntime.MintHandleToken(produtor, "sha256:"+digestHex([]byte(aos553Conteudo)), 1)
	dTexto := &aos553Desfechos{por: map[string]state.Outcome{produtor: aos553Ancora(textoRef)}}
	comTexto, terr := atravessarArestasDoPlano(ctx, plano, dTexto, aos553Pedido, []string{produtor}, aos553Goal(textoRef))
	if terr != nil || string(comTexto.Inputs[0].Content) != aos553Conteudo {
		t.Fatalf("conteudo de TEXTO tem de atravessar pelo mesmo caminho: %q %v", comTexto.Inputs[0].Content, terr)
	}
}

// TestAOS553_UmHandleIntermedioDoProdutorNaoAtravessa é o teste do passo que DECIDE: a âncora tem
// de designar exactamente esta referência. Sem ele, o corpo do pedido podia nomear qualquer handle
// do run produtor — incluindo um resultado de tool intermédio que o plano nunca publicou — e o nó
// decifrava-o e entregava-o ao modelo do consumidor.
//
// É a diferença entre «o drenador lê o que já lhe é servido» e «o drenador lê tudo o que o run
// produtor selou».
func TestAOS553_UmHandleIntermedioDoProdutorNaoAtravessa(t *testing.T) {
	ctx := context.Background()
	plano, esp, _, produtor, refDoPrimeiro, _ := aos553Bancada(t)
	// O produtor sela um SEGUNDO handle — a saída designada —, e o primeiro fica intermédio.
	const saida = "a lista de accoes que o no 1 produziu"
	p2, causa := esp.selar(ctx, aos553Titular, produtor, "step-000002-tool-1", "doc_read", "", []byte(saida))
	if causa != "" {
		t.Fatal(causa)
	}
	refDaSaida := agentruntime.HandleRefPrefix + p2.Token
	d := &aos553Desfechos{por: map[string]state.Outcome{produtor: aos553Ancora(refDaSaida)}}

	// A SAÍDA DESIGNADA atravessa.
	bom, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, aos553Goal(refDaSaida))
	if err != nil || string(bom.Inputs[0].Content) != saida {
		t.Fatalf("a saida designada tem de atravessar: %q %v", bom.Inputs[0].Content, err)
	}
	// O HANDLE INTERMÉDIO não — e a causa é a do passo 4, não a do evento: o evento EXISTE.
	mau, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, aos553Goal(refDoPrimeiro))
	if err == nil {
		t.Fatal("um handle INTERMEDIO do produtor atravessou: o corpo passa a poder ler tudo o que o produtor selou")
	}
	if !strings.Contains(err.Error(), tCausaNaoDesignada) {
		t.Fatalf("erro = %v; queria %s (o evento existe; o que falha e a ancora)", err, tCausaNaoDesignada)
	}
	if string(mau.Inputs[0].Content) != refDoPrimeiro {
		t.Error("a entrada recusada nao ficou como estava")
	}
	// NÃO-VACUIDADE: o handle intermédio é abrível pelo nó quando é ELE o designado. O que o
	// recusa é a âncora, e não uma indisponibilidade.
	if b, aerr := esp.abrir(ctx, aos553Titular, produtor, "sha256:"+digestHex([]byte(aos553Conteudo))); aerr != nil || string(b) != aos553Conteudo {
		t.Errorf("pre-condicao: o handle intermedio existe e abre-se: %q %v", b, aerr)
	}
}

// TestAOS553_CrossTitularRecusadoPorNomeENaoPelaDecifra é o critério 4 do ticket, com o controlo
// negativo que o torna uma afirmação e não uma esperança: a cifra por titular TAMBÉM bloqueia — e é
// essa a propriedade que se quer —, mas o que a travessia acrescenta é o NOME, porque um erro de
// decifra não distingue «outro titular» de «disco corrompido».
func TestAOS553_CrossTitularRecusadoPorNomeENaoPelaDecifra(t *testing.T) {
	ctx := context.Background()
	plano, esp, _, produtor, ref, d := aos553Bancada(t)
	goal := aos553Goal(ref)
	goal.Subject = "titular-alheio"

	_, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, goal)
	if err == nil || !strings.Contains(err.Error(), tCausaOutroTitular) {
		t.Fatalf("erro = %v; queria a recusa nomeada %s", err, tCausaOutroTitular)
	}
	// A RECUSA É ANTES DA DECIFRA: nenhum dos contadores da abertura se mexeu.
	if esp.lido(tCausaIndisponivel) != 0 {
		t.Error("a recusa passou pela abertura: tinha de ser decidida ANTES de qualquer decifra")
	}
	// CONTROLO NEGATIVO — a cifra bloquearia de qualquer modo. É isto que separa «a propriedade
	// existe» de «a propriedade tem nome».
	if _, aerr := esp.abrir(ctx, "titular-alheio", produtor, "sha256:"+digestHex([]byte(aos553Conteudo))); aerr == nil {
		t.Fatal("pre-condicao: a cifra por titular tem de bloquear a abertura sob outro titular")
	}
}

// TestAOS553_RunDoNoProdutor — a forma do id, com os vectores que o `aos-orq` produz. Inclui o
// `node_id` com `~`, que ele escapa de forma injectiva, e a TENTATIVA, que é a razão de o run
// produtor não ser derivável do `node_id` sozinho.
func TestAOS553_RunDoNoProdutor(t *testing.T) {
	base := idDoRunFilho(aos553Pedido, aos553NoProd)
	casos := []struct {
		nome      string
		candidato string
		quero     bool
	}{
		{"a primeira hospedagem", base, true},
		{"a tentativa 2", base + separadorDoRunFilho + "2", true},
		{"a tentativa 10", base + separadorDoRunFilho + "10", true},
		{"a tentativa 1 nao tem sufixo", base + separadorDoRunFilho + "1", false},
		{"tentativa com zero a esquerda", base + separadorDoRunFilho + "02", false},
		{"tentativa que nao e numero", base + separadorDoRunFilho + "x", false},
		{"tentativa vazia", base + separadorDoRunFilho, false},
		{"sufixo que nao e tentativa", base + "-extra", false},
		{"prefixo do no, nao o no", idDoRunFilho(aos553Pedido, aos553NoProd[:3]), false},
		{"outro plano", idDoRunFilho("plan-outro", aos553NoProd), false},
		{"vazio", "", false},
		{"o proprio pedido", aos553Pedido, false},
	}
	for _, c := range casos {
		if got := runDoNoProdutor(aos553Pedido, aos553NoProd, c.candidato); got != c.quero {
			t.Errorf("%s: runDoNoProdutor(%q) = %v; quero %v", c.nome, c.candidato, got, c.quero)
		}
	}
	// O `~` DENTRO DO node_id: o `aos-orq` escapa-o, e a conferência tem de usar a MESMA função —
	// senão um nó chamado `a~b` casava com o run do nó `a`.
	comTilde := "a" + separadorDoRunFilho + "b"
	if !runDoNoProdutor(aos553Pedido, comTilde, idDoRunFilho(aos553Pedido, comTilde)) {
		t.Error("um node_id com ~ tem de casar com o seu proprio run")
	}
	if runDoNoProdutor(aos553Pedido, "a", idDoRunFilho(aos553Pedido, comTilde)) {
		t.Error("o run do no `a~b` casou com o no `a`: o escape deixou de ser injectivo na conferencia")
	}
	// Pedido ou nó vazios não casam com nada.
	if runDoNoProdutor("", aos553NoProd, base) || runDoNoProdutor(aos553Pedido, "", base) {
		t.Error("pedido ou no vazios nao podem casar")
	}
}

// TestAOS553_OVocabularioDaTravessiaEntraNoDoEspaco: as causas da travessia são rótulos da métrica
// `aos_handle_total`, pelo que têm de estar no vocabulário FECHADO do espaço — senão o
// [armazemDeHandles.conta] não as conta e o operador vê uma recusa sem série.
func TestAOS553_OVocabularioDaTravessiaEntraNoDoEspaco(t *testing.T) {
	no := map[string]bool{}
	for _, r := range handleResultados() {
		if no[r] {
			t.Errorf("valor repetido no vocabulario: %q", r)
		}
		no[r] = true
	}
	for _, c := range causasDaTravessia() {
		if !no[c] {
			t.Errorf("a causa %q da travessia nao esta em handleResultados(): nao e contada nem publicada", c)
		}
		if strings.ContainsAny(c, " \t\n\"") || c != strings.ToLower(c) {
			t.Errorf("a causa %q nao e um rotulo de metrica", c)
		}
	}
	// E são contáveis: o contador existe no armazém composto.
	_, esp, _, _, _, _ := aos553Bancada(t)
	for _, c := range causasDaTravessia() {
		if esp.contadores[c] == nil {
			t.Errorf("o armazem nao tem contador para %q", c)
		}
	}
}

// TestAOS553_OAnuncioExigeAsDuasCoisas: o `GET /tools` só anuncia a travessia com o layout por
// handle HABILITADO e o espaço de handles composto. É o que impede o `aos-orq` de entregar uma
// referência a um nó que a não resolve.
func TestAOS553_OAnuncioExigeAsDuasCoisas(t *testing.T) {
	plano, _, _, _, _, _ := aos553Bancada(t)
	casos := map[string]struct {
		no    *Node
		quero bool
	}{
		"nil":                     {nil, false},
		"sem habilitacao":         {&Node{planoHandles: plano}, false},
		"sem espaco":              {&Node{conteudoPorHandle: true}, false},
		"habilitado e com espaco": {&Node{conteudoPorHandle: true, planoHandles: plano}, true},
	}
	for nome, c := range casos {
		a := anuncioDaTravessiaDoNo(c.no)
		if (a != nil) != c.quero {
			t.Errorf("%s: anuncio=%v; quero presente=%v", nome, a, c.quero)
		}
		if a != nil && !a.PlanInput {
			t.Errorf("%s: o anuncio existe e diz false — um no sem as duas condicoes nao o envia", nome)
		}
	}
}

// TestAOS553_ATravessiaNaoReCorreNumaRetoma: uma entrada que já traz handle veio do registo de
// retoma, e os seus bytes já são conteúdo. A travessia não tem nada a fazer, e o selo não re-sela —
// é o que mantém o `prompt_hash` da retoma igual ao do run que ela continua.
func TestAOS553_ATravessiaNaoReCorreNumaRetoma(t *testing.T) {
	ctx := context.Background()
	plano, esp, _, produtor, ref, d := aos553Bancada(t)
	goal := aos553Goal(ref)
	goal.AssemblyVersion = agentruntime.AssemblyVersion180

	// Primeira hospedagem: travessia + selo.
	primeira, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, goal)
	if err != nil {
		t.Fatal(err)
	}
	primeira, err = selarEntradasDoPlano(ctx, plano, primeira)
	if err != nil {
		t.Fatal(err)
	}
	handleDaPrimeira := primeira.Inputs[0].Handle

	// A RETOMA: o Goal vem do registo, com os bytes resolvidos e o handle. A travessia é inerte, e
	// o selo não re-sela.
	//
	// `produtores` É NIL, e é o que a produção faz: a retoma entra por [NodeService.submit] →
	// `submitObservando(…, nil, nil)`, pelo que nenhuma aresta é declarada e a função retorna no
	// primeiro passo. Passar a aresta OUTRA VEZ sobre bytes já resolvidos seria a contradição que o
	// `travessia_entrada_sem_referencia` nomeia — o caso de baixo mede-a, e separá-los é o que
	// torna este teste a medição da RETOMA e não de duas coisas ao mesmo tempo.
	retomado, err := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, nil, primeira)
	if err != nil {
		t.Fatalf("a travessia recusou uma retoma: %v", err)
	}
	if esp.lido(tTravessiaFeita) != 1 {
		t.Errorf("travessia_feita = %d: a retoma re-atravessou", esp.lido(tTravessiaFeita))
	}
	retomado, err = selarEntradasDoPlano(ctx, plano, retomado)
	if err != nil {
		t.Fatal(err)
	}
	if retomado.Inputs[0].Handle != handleDaPrimeira {
		t.Errorf("o handle da retoma = %q; quero o da primeira %q (outro ordinal = outro prompt_hash)", retomado.Inputs[0].Handle, handleDaPrimeira)
	}
	if string(retomado.Inputs[0].Content) != aos553Conteudo {
		t.Errorf("o conteudo da retoma mudou: %q", retomado.Inputs[0].Content)
	}
	// E A CONTRADIÇÃO, para o caso ficar fechado dos dois lados: quem RE-DECLARASSE a aresta sobre
	// os bytes já resolvidos é recusado por nome, e não silenciosamente ignorado.
	if _, cerr := atravessarArestasDoPlano(ctx, plano, d, aos553Pedido, []string{produtor}, primeira); cerr == nil {
		t.Error("re-declarar a aresta sobre bytes ja resolvidos tinha de ser recusado")
	} else if !strings.Contains(cerr.Error(), tCausaSemReferencia) {
		t.Errorf("erro = %v; queria %s", cerr, tCausaSemReferencia)
	}
}

// TestAOS553_UmEventoComTokenEDigestQueNaoSeCorrespondemNaoResolve — o passo 5 tem DUAS metades, e
// esta é a que não é uma comparação de strings: o token re-cunha-se de `(run, digest, ordinal)`.
// Sem a re-cunhagem, um `handle.created` cujo payload fosse manipulado fazia a travessia abrir
// OUTRO conteúdo do mesmo run.
func TestAOS553_UmEventoComTokenEDigestQueNaoSeCorrespondemNaoResolve(t *testing.T) {
	produtor := idDoRunFilho(aos553Pedido, aos553NoProd)
	digestBom := "sha256:" + digestHex([]byte(aos553Conteudo))
	token := agentruntime.MintHandleToken(produtor, digestBom, 1)

	// O evento bom resolve.
	if h, ok := declaracaoDoToken(produtor, token, []handleCriado{{Token: token, Digest: digestBom, SealedSubject: aos553Titular}}); !ok || h.Digest != digestBom {
		t.Fatal("o evento bom tem de resolver")
	}
	// O MESMO token com OUTRO digest não: o token deixa de se re-cunhar.
	outro := "sha256:" + strings.Repeat("d", 64)
	if _, ok := declaracaoDoToken(produtor, token, []handleCriado{{Token: token, Digest: outro, SealedSubject: aos553Titular}}); ok {
		t.Fatal("um evento cujo token nao se re-cunha do seu digest resolveu: a travessia abriria outro conteudo do run")
	}
	// O mesmo trio noutro RUN não: a re-cunhagem leva o run.
	if _, ok := declaracaoDoToken(produtor+separadorDoRunFilho+"2", token, []handleCriado{{Token: token, Digest: digestBom}}); ok {
		t.Fatal("o token re-cunhou-se noutro run")
	}
	// Token mal formado, e lista vazia.
	if _, ok := declaracaoDoToken(produtor, "nao-e-token", []handleCriado{{Token: "nao-e-token", Digest: digestBom}}); ok {
		t.Fatal("um token sem ordinal resolveu")
	}
	if _, ok := declaracaoDoToken(produtor, token, nil); ok {
		t.Fatal("resolveu sem declaracoes")
	}
	// E o ordinal conta: o MESMO digest na posição 2 dá outro token.
	if agentruntime.MintHandleToken(produtor, digestBom, 2) == token {
		t.Fatal("pre-condicao: o ordinal tem de entrar na cunhagem")
	}
}
