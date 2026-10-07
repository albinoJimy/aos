package modelgateway_test

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-509 — A DESCODIFICAÇÃO TOLERANTE, medida contra a linha de base do AOS-508
// (`internal/wirefake/linha_de_base_aos508.json`: o que o gateway fazia a cada caso ANTES deste
// ticket, gerado na base e congelado).

// aos509Excepcao são os casos da ÚNICA excepção declarada ao «byte a byte»: respostas que já
// davam um turno e trazem o raciocínio noutro nome SEM `reasoning_content`. Passam a ter o
// raciocínio (que segue só para a captura), onde antes se perdia; tudo o resto fica igual.
var aos509Excepcao = []string{
	"h2_raciocinio_noutro_campo",
	"rac_blocos_assinados",
	"rac_reasoning_com_tool", "rac_reasoning_content_nulo_e_reasoning", "rac_reasoning_content_vazio",
	"rac_reasoning_details_com_tool", "rac_reasoning_details_content_vazio",
	"rac_reasoning_details_lista", "rac_reasoning_details_objecto", "rac_reasoning_details_string",
	"rac_reasoning_lista", "rac_reasoning_objecto", "rac_reasoning_string",
	"rac_thinking_blocks_com_tool", "rac_thinking_blocks_content_vazio",
	"rac_thinking_blocks_lista", "rac_thinking_blocks_objecto", "rac_thinking_blocks_string",
	"rac_thinking_com_tool", "rac_thinking_content_vazio",
	"rac_thinking_lista", "rac_thinking_objecto", "rac_thinking_string",
	"rac_varios_sem_reasoning_content",
}

// AS RESPOSTAS QUE JÁ PASSAVAM FICAM BYTE A BYTE. Para todo o caso que a linha de base regista
// como turno aceite, o turno de hoje é o mesmo — texto, tool calls (nome e bytes dos
// argumentos), motivo de paragem, `Final`, modelo, tokens e raciocínio. O que o runtime grava
// (`turn.recorded`, tail, `prompt_hash`, veredicto, captura) é função desse turno. A excepção é
// a lista acima, e nela só o raciocínio muda, de vazio para preenchido.
func TestAOS509_OQueJaPassavaFicaByteAByte(t *testing.T) {
	base := wirefake.LinhaDeBase()
	excepcao := map[string]bool{}
	for _, c := range aos509Excepcao {
		excepcao[c] = true
	}
	aceites, mudaram := 0, []string{}
	for _, caso := range wirefake.Nomes() {
		antes, ha := base[caso]
		if !ha {
			t.Fatalf("%s: sem linha de base", caso)
		}
		if antes.Desfecho != wirefake.DesfechoTurno {
			continue
		}
		aceites++
		hoje := wirefakeComportamento(t, caso)
		if reflect.DeepEqual(hoje, antes) {
			if excepcao[caso] {
				t.Errorf("%s: esta na lista da excepcao e nao mudou — a lista esta errada", caso)
			}
			continue
		}
		mudaram = append(mudaram, caso)
		if !excepcao[caso] {
			t.Errorf("%s: uma resposta que ja passava MUDOU, fora da excepcao declarada:\n antes: %+v\n hoje:  %+v", caso, antes, hoje)
			continue
		}
		if antes.Reasoning != "" || hoje.Reasoning == "" {
			t.Errorf("%s: a excepcao e so o raciocinio passar de vazio a preenchido: antes %q, hoje %q", caso, antes.Reasoning, hoje.Reasoning)
		}
		semRaciocinio := hoje
		semRaciocinio.Reasoning = ""
		if !reflect.DeepEqual(semRaciocinio, antes) {
			t.Errorf("%s: fora do raciocinio o turno mudou:\n antes: %+v\n hoje:  %+v", caso, antes, semRaciocinio)
		}
	}
	sort.Strings(mudaram)
	if !reflect.DeepEqual(mudaram, aos509Excepcao) {
		t.Errorf("os casos que mudaram nao sao os da excepcao declarada:\n mudaram:  %v\n excepcao: %v", mudaram, aos509Excepcao)
	}
	if aceites != 75 {
		t.Errorf("a linha de base tem 75 turnos aceites; este teste viu %d", aceites)
	}
}

// AS FORMAS QUE PASSAM A DAR UM TURNO, e as que continuam a recusar — com causa própria.
func TestAOS509_FormasValidasDaoTurno_AsOutrasRecusamComCausa(t *testing.T) {
	base := wirefake.LinhaDeBase()
	args := `{"path":"notas.txt"}`
	for caso, quer := range map[string]wirefake.Comportamento{
		"content_partes_texto":          {Text: "resposta final", Final: true, StopReason: "stop"},
		"content_partes_varias":         {Text: "resposta final", Final: true, StopReason: "stop"},
		"content_partes_parte_vazia":    {Text: "resposta final", Final: true, StopReason: "stop"},
		"content_partes_lista_vazia":    {Text: "", Final: true, StopReason: "stop"},
		"content_partes_texto_com_tool": {Text: "vou ler o ficheiro", StopReason: "tool_calls", ToolCalls: []wirefake.ChamadaDeTool{{Tool: "arquivo", Input: args}}},
		"args_objecto":                  {StopReason: "tool_calls", ToolCalls: []wirefake.ChamadaDeTool{{Tool: "arquivo", Input: args}}},
		"args_objecto_aninhado":         {StopReason: "tool_calls", ToolCalls: []wirefake.ChamadaDeTool{{Tool: "arquivo", Input: `{"filtro":{"de":[1,2,{"x":null}]},"path":"notas.txt"}`}}},
	} {
		if base[caso].Desfecho != wirefake.DesfechoRecusada {
			t.Errorf("%s: a linha de base tinha de o registar como recusado", caso)
		}
		got := wirefakeComportamento(t, caso)
		quer.Desfecho, quer.Model, quer.InputTokens, quer.OutputTokens = wirefake.DesfechoTurno, "modelo-falso", 11, 7
		if !reflect.DeepEqual(got, quer) {
			t.Errorf("%s:\n veio:  %+v\n quero: %+v", caso, got, quer)
		}
	}
	// A MESMA CHAMADA, com os argumentos em string e em objecto, dá o mesmo Input, byte a byte.
	emString, emObjecto := wirefakeComportamento(t, "content_nulo_com_tool"), wirefakeComportamento(t, "args_objecto")
	if !reflect.DeepEqual(emString.ToolCalls, emObjecto.ToolCalls) || emString.ToolCalls[0].Input != args {
		t.Errorf("string e objecto tinham de dar o mesmo Input: %+v e %+v", emString.ToolCalls, emObjecto.ToolCalls)
	}

	for caso, quer := range map[string]struct {
		erro  error
		causa string
	}{
		"content_partes_com_imagem":        {port.ErrContentPartNotText, modelgateway.RejectContentPart},
		"content_partes_com_recusa":        {port.ErrContentPartNotText, modelgateway.RejectContentPart},
		"content_partes_sem_type":          {port.ErrContentPartNotText, modelgateway.RejectContentPart},
		"content_partes_tipo_desconhecido": {port.ErrContentPartNotText, modelgateway.RejectContentPart},
		"content_partes_texto_nao_string":  {port.ErrContentPartNotText, modelgateway.RejectContentPart},
		"content_numero":                   {port.ErrContentForm, modelgateway.RejectContentForm},
		"content_objecto":                  {port.ErrContentForm, modelgateway.RejectContentForm},
		"args_lista":                       {port.ErrArgumentsForm, modelgateway.RejectArgumentsForm},
		"args_numero":                      {port.ErrArgumentsForm, modelgateway.RejectArgumentsForm},
		"choices_vazio":                    {modelgateway.ErrRespostaSemChoices, modelgateway.RejectNoChoices},
		"id_numero_json":                   {nil, modelgateway.RejectJSON},
	} {
		m := wirefakeCompor(t, caso, "")
		var causas []string
		_, err := modelgateway.NewModelClient(m.gw, "gpt-4o", modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu"),
			modelgateway.WithResponseRejectedObserver(func(c string) { causas = append(causas, c) })).
			Call(t.Context(), agentruntime.PromptView{Materialized: []byte("olá")})
		if err == nil || (quer.erro != nil && !errors.Is(err, quer.erro)) {
			t.Errorf("%s: queria a resposta recusada com %v; veio %v", caso, quer.erro, err)
			continue
		}
		if !reflect.DeepEqual(causas, []string{quer.causa}) {
			t.Errorf("%s: causa contada = %v, quero %q", caso, causas, quer.causa)
		}
		for _, proibido := range []string{"resposta final", "image_url", "data:image", "nao posso", "notas.txt", "coisa_nova"} {
			if strings.Contains(err.Error(), proibido) {
				t.Errorf("%s: o erro leva bytes da resposta (%q): %v", caso, proibido, err)
			}
		}
	}
	// Um turno aceite, e um erro que não é da resposta, não contam.
	if c := modelgateway.ResponseRejectionCause(nil); c != "" {
		t.Errorf("nil nao e uma recusa: %q", c)
	}
	if c := modelgateway.ResponseRejectionCause(errors.New("adapters: provider openai devolveu status 500")); c != "" {
		t.Errorf("um status 500 nao e uma resposta recusada: %q", c)
	}
	vocab := map[string]bool{}
	for _, c := range modelgateway.ResponseRejectionCauses() {
		vocab[c] = true
	}
	if len(vocab) != 5 {
		t.Errorf("o vocabulario das causas tem 5 valores: %v", modelgateway.ResponseRejectionCauses())
	}
}

// O RACIOCÍNIO NUNCA É A RESPOSTA (decisão D3). Com `content` nulo, vazio ou só com brancos e o
// raciocínio preenchido — em cada nome de campo e em cada forma —, o texto do turno é o de
// `content` e o run fecha `empty_output`.
func TestAOS509_ORaciocinioNuncaEAResposta(t *testing.T) {
	casos := []string{"h1_raciocinio_em_reasoning_content", "h2_raciocinio_noutro_campo"}
	for _, nome := range []string{"reasoning_content", "reasoning", "reasoning_details", "thinking_blocks", "thinking"} {
		casos = append(casos, "rac_"+nome+"_content_vazio")
	}
	for _, caso := range casos {
		out, err := wirefakeCompor(t, caso, "").turno()
		if err != nil {
			t.Fatalf("%s: %v", caso, err)
		}
		if out.Text != "" {
			t.Errorf("%s: o texto do turno tinha de ser o de `content` (vazio); veio %q", caso, out.Text)
		}
		if out.Reasoning == "" {
			t.Errorf("%s: o raciocinio tinha de ser lido (para a captura)", caso)
		}
		fim, err := agentruntime.TurnEndsRun(out, agentruntime.AssemblyVersion)
		if err != nil || !fim {
			t.Fatalf("%s: o turno tinha de terminar o run: %v %v", caso, fim, err)
		}
		v, err := agentruntime.ConcludeRun(out, &agentruntime.Completion{Mode: agentruntime.CompletionEnforce}, nil)
		if err != nil || v.Verdict == nil || v.Verdict.Fulfilled || v.Verdict.Reason != agentruntime.OutcomeEmptyOutput || !v.Unfulfilled || v.FinalText != "" {
			t.Errorf("%s: o run tinha de fechar empty_output sem texto final; veio %+v (veredicto %+v) %v", caso, v, v.Verdict, err)
		}
	}
	// Com `content` preenchido, em cada nome e forma, o texto é o de `content` e nunca o do raciocínio.
	for _, caso := range wirefake.Nomes() {
		if !strings.HasPrefix(caso, "rac_") {
			continue
		}
		out, err := wirefakeCompor(t, caso, "").turno()
		if err != nil {
			t.Fatalf("%s: %v", caso, err)
		}
		if strings.Contains(out.Text, "penso") || strings.Contains(out.Text, "bloco") || strings.Contains(out.Text, "campo") || strings.Contains(out.Text, "ordem") {
			t.Errorf("%s: o texto do turno leva raciocinio: %q", caso, out.Text)
		}
		if out.Text != "" && out.Text != "resposta final" {
			t.Errorf("%s: texto inesperado: %q", caso, out.Text)
		}
	}
}

// A ORDEM DOS NOMES, e `reasoning_content` a valer sozinho quando está presente.
func TestAOS509_Raciocinio_OrdemFixaDosNomes(t *testing.T) {
	for caso, quer := range map[string]string{
		"rac_varios_nomes":                       "o do campo de sempre",
		"rac_reasoning_content_nulo_e_reasoning": "primeiro penso",
		"rac_varios_sem_reasoning_content":       `[{"type":"reasoning.text","text":"o segundo da ordem"}]`,
		"rac_thinking_objecto":                   `{"text":"primeiro penso; depois concluo"}`,
		"content_texto":                          "",
	} {
		out, err := wirefakeCompor(t, caso, "").turno()
		if err != nil || out.Reasoning != quer {
			t.Errorf("%s: raciocinio = %q (%v), quero %q", caso, out.Reasoning, err, quer)
		}
	}
}
