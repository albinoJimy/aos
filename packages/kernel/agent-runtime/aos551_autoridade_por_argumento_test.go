package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-551, fatia 2 — A AUTORIDADE POR ARGUMENTO: o piso, o portão e a re-junção.
//
// Três propriedades, e a terceira é a que faz as outras duas valerem:
//
//  1. o PISO é o que a autoridade por argumento não pode levantar, e a sua lista de kinds saiu de
//     enumerar os dez — a memória e o erro de tool são as entradas que a enumeração encontrou;
//  2. o PORTÃO recusa um run num layout por handle sem o colaborador que levanta;
//  3. a RE-JUNÇÃO faz a garantia ser aritmética: a reescrita só consegue subir.

func segDe(kind TailKind, meta ...TailMeta) TailSegment {
	return TailSegment{Kind: kind, Meta: meta, Content: []byte("x")}
}

// porHandle é o piso num layout com as DUAS metades ligadas (a 1.8.0).
func porHandle(seg TailSegment, sob taint.Label) taint.Label {
	return SegmentFloor(seg, sob, true, true)
}

// O piso por kind, num layout POR HANDLE. É a tabela que decide o que se levanta.
func TestAOS551C_PisoPorKind(t *testing.T) {
	casos := []struct {
		nome string
		seg  TailSegment
		quer taint.Label
	}{
		// Escritos pelo submissor, por um humano autenticado ou pelo runtime.
		{"objective", segDe(TailObjective), taint.Trusted},
		{"correction", segDe(TailCorrection), taint.Trusted},
		{"notice", segDe(TailNotice), taint.Trusted},
		// Corpo fixo SÓ com o rótulo do handle, que é o que o diz. Sem ele, untrusted — e essa é
		// a correcção que uma revisão adversarial obrigou: a versão por KIND declarava trusted um
		// `tool_result` de `media_view` (que leva a MediaRef INLINE e não tem rótulo de handle) e
		// um `plan_input` de um layout futuro que pusesse só os resultados por handle.
		{"plan_input por handle", segDe(TailPlanInput, TailMeta{Key: PlanInputHandleLabel, Value: "ref"}), taint.Trusted},
		{"plan_input SEM rótulo", segDe(TailPlanInput), taint.Untrusted},
		{"tool_result por handle", segDe(TailToolResult, TailMeta{Key: ToolResultHandleLabel, Value: "ref"}), taint.Trusted},
		{"tool_result SEM rótulo (media_view inline)", segDe(TailToolResult), taint.Untrusted},
		// AS DUAS ENTRADAS QUE A ENUMERAÇÃO ENCONTROU, e sem as quais a fase tinha um buraco:
		// a memória entra INLINE e a 1.8.0 não a põe atrás de um handle; e o corpo de um
		// `tool_error` é `error.Error()`, texto livre de uma tool.
		{"memory", segDe(TailMemory), taint.Untrusted},
		{"tool_result com erro", segDe(TailToolResult, TailMeta{Key: "tool_error", Value: "1"}), taint.Untrusted},
		// O `timestamp` saiu do caso trusted: o kernel NÃO o escreve — quem o emite é a memória
		// de trabalho, fora do seu controlo. Classificar por nome um kind que não se produz é a
		// excepção que o `default` fail-closed existe para não ter.
		{"timestamp", segDe(TailTimestamp), taint.Untrusted},
		// O `steer` (control.TailSteer) cai no default. Conservador de propósito, e com a
		// consequência declarada: um run esteerado na 1.8.0 não levanta nada.
		{"steer", segDe(TailKind("steer")), taint.Untrusted},
		// A media é projectada PARA o modelo (R1a): num run com media nada se levanta.
		{"media", segDe(TailMedia), taint.Untrusted},
	}
	for _, c := range casos {
		if got := porHandle(c.seg, taint.Trusted); got != c.quer {
			t.Errorf("%s: piso=%v, queria %v", c.nome, got, c.quer)
		}
	}

	// O texto e os argumentos do próprio modelo HERDAM, como em SegmentAuthority: não sujam por
	// si, e não lavam o que já estava sujo.
	for _, kind := range []TailKind{TailHistory, TailToolCall} {
		if got := porHandle(segDe(kind), taint.Trusted); got != taint.Trusted {
			t.Errorf("%v sob trusted: piso=%v", kind, got)
		}
		if got := porHandle(segDe(kind), taint.Untrusted); got != taint.Untrusted {
			t.Errorf("%v sob untrusted: piso=%v — o texto do modelo lavaria o contexto", kind, got)
		}
	}

	// FAIL-CLOSED para um kind que ainda não existe.
	if got := porHandle(segDe(TailKind("kind_que_ainda_nao_existe")), taint.Trusted); got != taint.Untrusted {
		t.Errorf("um kind novo entrou no piso como %v — tem de ser untrusted até alguém decidir o contrário", got)
	}
}

// AS DUAS FLAGS SÃO CONSULTADAS SEPARADAMENTE. Hoje a 1.8.0 liga ambas, mas elas existem
// separadas de propósito: um layout que ligue só uma não pode declarar trusted o segmento que
// ainda leva o payload. Era um GRAVE da revisão — a versão anterior recebia um só booleano, com
// a tabela a assumir a conjunção.
func TestAOS551C_CadaFlagSoLevantaASuaMetade(t *testing.T) {
	entrada := segDe(TailPlanInput, TailMeta{Key: PlanInputHandleLabel, Value: "ref"})
	resultado := segDe(TailToolResult, TailMeta{Key: ToolResultHandleLabel, Value: "ref"})

	// Só as entradas por handle: o `plan_input` levanta, o `tool_result` não.
	if got := SegmentFloor(entrada, taint.Trusted, true, false); got != taint.Trusted {
		t.Errorf("entradas ligadas: o plan_input deu %v", got)
	}
	if got := SegmentFloor(resultado, taint.Trusted, true, false); got != taint.Untrusted {
		t.Errorf("entradas ligadas, resultados NÃO: o tool_result deu %v — o payload ainda vem inline", got)
	}
	// Só os resultados por handle: o inverso.
	if got := SegmentFloor(resultado, taint.Trusted, false, true); got != taint.Trusted {
		t.Errorf("resultados ligados: o tool_result deu %v", got)
	}
	if got := SegmentFloor(entrada, taint.Trusted, false, true); got != taint.Untrusted {
		t.Errorf("resultados ligados, entradas NÃO: o plan_input deu %v — o payload ainda vem inline", got)
	}
}

// FORA de um layout por handle o piso É a autoridade, byte a byte. É o que torna a utilização
// errada inexpressável, em vez de apenas desencorajada.
func TestAOS551C_ForaDeUmLayoutPorHandleOPisoEAAutoridade(t *testing.T) {
	kinds := []TailKind{
		TailObjective, TailCorrection, TailNotice, TailTimestamp, TailPlanInput,
		TailToolResult, TailHistory, TailToolCall, TailMemory, TailMedia,
	}
	for _, kind := range kinds {
		for _, sob := range []taint.Label{taint.Trusted, taint.Untrusted} {
			piso := SegmentFloor(segDe(kind), sob, false, false)
			aut := SegmentAuthority(kind, sob)
			if piso != aut {
				t.Errorf("%v sob %v: piso=%v, autoridade=%v — divergiram fora de um layout por handle", kind, sob, piso, aut)
			}
		}
	}
}

// A janela acumula o piso pelo MESMO ponto único por onde acumula a autoridade: um segmento não
// pode entrar no prompt sem entrar nos dois.
func TestAOS551C_AJanelaAcumulaOsDoisRotulos(t *testing.T) {
	// Num layout por handle: entradas e resultados por handle não sujam; a memória suja.
	w := newAuthorityWindow(&janelaDeContagem{}, true, true)
	w.Append(segDe(TailObjective))
	w.Append(segDe(TailPlanInput, TailMeta{Key: PlanInputHandleLabel, Value: "ref"}))
	w.Append(segDe(TailToolResult, TailMeta{Key: ToolResultHandleLabel, Value: "ref"}))
	if w.floor() != taint.Trusted {
		t.Fatalf("o piso sujou-se com corpos fixos: %v", w.floor())
	}
	if w.authority() != taint.Untrusted {
		t.Fatalf("a autoridade do CONTEXTO tinha de ser untrusted (os segmentos existem): %v", w.authority())
	}
	w.Append(segDe(TailMemory))
	if w.floor() != taint.Untrusted {
		t.Fatal("a memória não sujou o piso")
	}
	// E não desce outra vez: o join é monótono.
	w.Append(segDe(TailObjective))
	if w.floor() != taint.Untrusted {
		t.Fatal("o piso desceu depois de um segmento trusted")
	}
}

// O PORTÃO: um run num layout por handle não arranca sem o colaborador que levanta a autoridade.
//
// Estrutural, e no `Run` — não um banner no nó. Num layout por handle a Call sai com o PISO em vez
// da autoridade do contexto; sem ninguém a levantá-la por argumento, chegaria ao Reference Monitor
// com um rótulo MAIS BAIXO do que o de hoje, isto é, a separação pagar-se-ia com uma perda de
// autoridade. O portão torna isso inarrancável em vez de desaconselhado.
func TestAOS551C_OPortaoRecusaUmRunPorHandleSemOColaborador(t *testing.T) {
	novoModelo := func() ModelClient {
		return ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
			return ModelResponse{Text: "fim", Final: true}, nil
		})
	}
	goalDa180 := func() Goal {
		g := sampleGoal()
		g.AssemblyVersion = AssemblyVersion180
		return g
	}

	identidade := CallRewriter(func(c referencemonitor.Call) (referencemonitor.Call, error) { return c, nil })

	// O portão exige AS DUAS COISAS. Declarar sem compor a reescrita era o buraco que uma revisão
	// adversarial mediu: o run arrancava, a Call saía com o piso, e não havia ninguém para a
	// levantar — o rótulo mais baixo que o portão existe para impedir, pela porta do lado.
	casos := []struct {
		nome string
		opts []Option
	}{
		{"sem nada", nil},
		{"só a declaração", []Option{WithPerArgumentAuthority()}},
		{"só a reescrita", []Option{WithCallRewriter(identidade)}},
	}
	for _, c := range casos {
		h := newHarness(t, nil)
		if _, err := New(novoModelo(), h.rm, h.recorder, c.opts...).Run(context.Background(), goalDa180()); !errors.Is(err, ErrArgumentAuthorityRequired) {
			t.Fatalf("%s: esperado ErrArgumentAuthorityRequired, veio %v", c.nome, err)
		}
	}

	// COM as duas: arranca. Sem este controlo, o teste passava com o portão a recusar tudo.
	h2 := newHarness(t, nil)
	if _, err := New(novoModelo(), h2.rm, h2.recorder, WithPerArgumentAuthority(), WithCallRewriter(identidade)).Run(context.Background(), goalDa180()); err != nil {
		t.Fatalf("com as duas o run tinha de arrancar: %v", err)
	}

	// E o portão é SÓ da 1.8.0: os layouts anteriores não passam a exigi-lo.
	for _, v := range []string{AssemblyVersion130, AssemblyVersion140, AssemblyVersion150, AssemblyVersion160, AssemblyVersion170} {
		hv := newHarness(t, nil)
		g := sampleGoal()
		g.AssemblyVersion = v
		if _, err := New(novoModelo(), hv.rm, hv.recorder).Run(context.Background(), g); err != nil {
			t.Errorf("o layout %s passou a exigir o colaborador: %v", v, err)
		}
	}
}

// despachoEspiao é o [ActivityDispatcher] que REGISTA o taint da call e delega na mediação real.
// É o ponto de observação certo: recebe a Call depois da reescrita e depois da re-junção do piso,
// que é exactamente o que o Reference Monitor vai ver.
type despachoEspiao struct {
	rm     *referencemonitor.Monitor
	taints []string
}

func (d *despachoEspiao) Dispatch(ctx context.Context, call referencemonitor.Call) (referencemonitor.Decision, error) {
	d.taints = append(d.taints, call.Context.Taint)
	return d.rm.Mediate(ctx, call)
}

// A RE-JUNÇÃO: a reescrita só consegue SUBIR acima do piso, nunca descer.
//
// É isto que faz a garantia ser aritmética em vez de uma promessa sobre quem compõe o nó. A
// reescrita é quem calcula a autoridade por argumento e a escreve no `Context.Taint`; o kernel
// junta-lhe o piso OUTRA VEZ depois dela. Aqui a reescrita é MALICIOSA — tenta sempre pôr a call
// como trusted — e medimos o que o Reference Monitor recebe.
func TestAOS551C_AReescritaSoConsegueSubirAcimaDoPiso(t *testing.T) {
	maliciosa := CallRewriter(func(call referencemonitor.Call) (referencemonitor.Call, error) {
		call.Context.Taint = taint.Trusted.String()
		return call, nil
	})
	lixo := CallRewriter(func(call referencemonitor.Call) (referencemonitor.Call, error) {
		call.Context.Taint = "nao-e-um-rotulo"
		return call, nil
	})

	medir := func(t *testing.T, versao string, comMemoria bool, rw CallRewriter) taint.Label {
		t.Helper()
		h := newHarness(t, map[string]referencemonitor.ToolFunc{
			"echo": func(_ context.Context, input []byte) ([]byte, error) { return input, nil },
		})
		espiao := &despachoEspiao{rm: h.rm}
		n := 0
		model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
			n++
			if n == 1 {
				return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte(`{"d":1}`)}}}, nil
			}
			return ModelResponse{Text: "fim", Final: true}, nil
		})
		g := sampleGoal()
		g.AssemblyVersion = versao
		if comMemoria {
			g.MemoryContext = []byte("memoria do run: conteudo de fora, e INLINE")
		}
		rt := New(model, h.rm, h.recorder,
			WithCallRewriter(rw), WithPerArgumentAuthority(), WithActivityDispatcher(espiao))
		if _, err := rt.Run(context.Background(), g); err != nil {
			t.Fatalf("Run(%s): %v", versao, err)
		}
		if len(espiao.taints) != 1 {
			t.Fatalf("Run(%s): o espião viu %d calls, queria 1 — o teste não mediu o que diz medir", versao, len(espiao.taints))
		}
		return taint.ParseLabel(espiao.taints[0])
	}

	// (1) 1.8.0 sem memória nem media: o piso é trusted, logo a reescrita CONSEGUE manter trusted.
	//     É exactamente o limite que a fase existe para levantar — e o controlo positivo de que a
	//     re-junção não está simplesmente a forçar untrusted em tudo.
	if got := medir(t, AssemblyVersion180, false, maliciosa); got != taint.Trusted {
		t.Errorf("1.8.0 sem memória: o RM viu %v, queria trusted — o limite não se levanta", got)
	}

	// (2) 1.8.0 COM memória: o piso está no chão e a reescrita maliciosa NÃO consegue descer.
	if got := medir(t, AssemblyVersion180, true, maliciosa); got != taint.Untrusted {
		t.Errorf("1.8.0 com memória: o RM viu %v — uma reescrita conseguiu DESCER abaixo do piso", got)
	}

	// (3) Lixo no campo também não desce: `ParseLabel` é fail-closed.
	if got := medir(t, AssemblyVersion180, true, lixo); got != taint.Untrusted {
		t.Errorf("1.8.0 com memória e lixo no campo: o RM viu %v", got)
	}

	// (4) Nos layouts anteriores o piso É a autoridade do contexto, pelo que uma reescrita
	//     maliciosa também não consegue lavar um contexto sujo — a protecção de hoje não regride.
	if got := medir(t, AssemblyVersion140, true, maliciosa); got != taint.Untrusted {
		t.Errorf("1.4.0 com memória: o RM viu %v — a reescrita lavou o contexto num layout em produção", got)
	}
}

// janelaDeContagem e a [WindowPort] minima para exercitar o decorador.
type janelaDeContagem struct{ segs []TailSegment }

func (j *janelaDeContagem) Append(seg TailSegment) { j.segs = append(j.segs, seg) }
func (j *janelaDeContagem) Assemble(context.Context, int) PromptView {
	return PromptView{}
}
func (j *janelaDeContagem) SystemHash() string { return "sha256:" + strings.Repeat("0", 64) }

// A PROVENIÊNCIA, MEDIDA DIRECTAMENTE (AOS-556, achado da revisão adversarial).
//
// Esta derivação não tinha teste nenhum: toda a asserção sobre proveniência era feita sobre um
// valor que o próprio teste fornecia. Trocar a ordem dos dois `case` não avermelhava nada — e com a
// política de fluxo em vigor isso inverteria o controlo negativo do dono: um run COM media (piso
// untrusted) passaria a chegar `handle_argument`, e a leitura externa seria PERMITIDA.
//
// É a forma «gate recalculado de entradas do chamador»: o que se mede é o valor que o medidor
// forneceu. Aqui mede-se a função.
func TestAOS556_ProvenienciaDaCall(t *testing.T) {
	for nome, c := range map[string]struct {
		piso, daReescrita taint.Label
		quero             string
	}{
		// O PISO DOMINA, e é isso que preserva «num run com media nada se levanta» sem a política
		// ter de saber o que é media.
		"piso sujo, reescrita suja":   {taint.Untrusted, taint.Untrusted, referencemonitor.ProvenanceContext},
		"piso sujo, reescrita limpa":  {taint.Untrusted, taint.Trusted, referencemonitor.ProvenanceContext},
		"piso limpo, reescrita suja":  {taint.Trusted, taint.Untrusted, referencemonitor.ProvenanceHandleArgument},
		"piso limpo, reescrita limpa": {taint.Trusted, taint.Trusted, referencemonitor.ProvenanceClean},
	} {
		if got := provenienciaDaCall(c.piso, c.daReescrita); got != c.quero {
			t.Errorf("%s: proveniencia=%q, quero %q", nome, got, c.quero)
		}
	}
	// E O VOCABULÁRIO É FECHADO: três valores, e a função nunca devolve outro.
	vistos := map[string]bool{}
	for _, piso := range []taint.Label{taint.Trusted, taint.Untrusted} {
		for _, re := range []taint.Label{taint.Trusted, taint.Untrusted} {
			vistos[provenienciaDaCall(piso, re)] = true
		}
	}
	if len(vistos) != 3 {
		t.Fatalf("a funcao devolve %d valores distintos sobre o dominio todo: %v", len(vistos), vistos)
	}
}
