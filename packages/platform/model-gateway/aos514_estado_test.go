package modelgateway_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-514 — O ESTADO OPACO DO PROVIDER, PELO GATEWAY DE PRODUÇÃO (ADR-040). O provider é o falso
// de wire (AOS-508), ou um servidor que devolve o corpo dado.

// aos514Montagem é o gateway de produção à frente de um servidor, com a captura do estado no
// modo dado.
type aos514Montagem struct {
	gw         *modelgateway.Gateway
	pedidos    func() [][]byte
	resultados []string
}

func aos514Compor(t *testing.T, h http.Handler, pedidos func() [][]byte, modo, forma string) *aos514Montagem {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.ProviderState, cfg.ResponseShape = modo, forma
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction(estado=%q): %v", modo, err)
	}
	return &aos514Montagem{gw: gw, pedidos: pedidos}
}

func aos514ComporCaso(t *testing.T, caso, modo, forma string) *aos514Montagem {
	falso := wirefake.NovoServidor(caso)
	return aos514Compor(t, falso, func() (out [][]byte) {
		for _, p := range falso.Pedidos() {
			out = append(out, p.Corpo)
		}
		return out
	}, modo, forma)
}

// aos514Corpo é um servidor que devolve sempre o mesmo corpo.
func aos514ComporCorpo(t *testing.T, corpo, modo string) *aos514Montagem {
	return aos514Compor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(corpo))
	}), func() [][]byte { return nil }, modo, "")
}

// turno faz um turno pelo adaptador do runtime, como o nó o faz. maxBytes <= 0 ⇒ o adaptador
// não tem a opção da captura.
func (m *aos514Montagem) turno(maxBytes int) (agentruntime.ModelResponse, error) {
	opts := []modelgateway.RuntimeAdapterOption{modelgateway.WithPrincipal("tok"), modelgateway.WithRegionBoard("eu", "board-eu")}
	if maxBytes > 0 {
		opts = append(opts, modelgateway.WithProviderStateCapture(maxBytes, func(r string) { m.resultados = append(m.resultados, r) }))
	}
	return modelgateway.NewModelClient(m.gw, "gpt-4o", opts...).Call(context.Background(), agentruntime.PromptView{Materialized: []byte("olá")})
}

// COM A CAPTURA DESLIGADA NADA MUDA — para TODOS os casos do wirefake: a porta não devolve
// estado, o turno do runtime não o leva (mesmo com a opção do adaptador ligada: sem sonda não há
// o que fechar), o pedido ao provider é o mesmo, e o turno é o da captura ligada tirando o
// estado. Em particular o TEXTO e as TOOL CALLS não dependem da captura (decisão D3: o
// raciocínio nunca é resposta).
func TestAOS514_Off_ASondaNaoCorre(t *testing.T) {
	for _, caso := range wirefake.Nomes() {
		ligada := aos514ComporCaso(t, caso, modelgateway.ProviderStateCapture, "")
		comEstado, errCom := ligada.turno(modelgateway.DefaultProviderStateMaxBytes)
		for _, modo := range []string{"", modelgateway.ProviderStateOff} {
			m := aos514ComporCaso(t, caso, modo, "")
			resp, errPorta := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
			if errPorta == nil && resp.State != nil {
				t.Fatalf("%s, modo %q: a porta devolveu estado com a captura desligada", caso, modo)
			}
			for _, max := range []int{0, modelgateway.DefaultProviderStateMaxBytes} {
				out, err := m.turno(max)
				if (err == nil) != (errCom == nil) || (err != nil && err.Error() != errCom.Error()) {
					t.Fatalf("%s, modo %q: o erro mudou com a captura: %v contra %v", caso, modo, err, errCom)
				}
				if out.State != nil || len(m.resultados) != 0 {
					t.Fatalf("%s, modo %q: o turno leva estado (%+v) ou o observador foi chamado (%v)", caso, modo, out.State, m.resultados)
				}
				semEstado := comEstado
				semEstado.State = nil
				if !reflect.DeepEqual(out, semEstado) {
					t.Fatalf("%s, modo %q: fora do estado o turno mudou com a captura:\n off:     %+v\n capture: %+v", caso, modo, out, semEstado)
				}
			}
			pOff, pOn := m.pedidos(), ligada.pedidos()
			if !bytes.Equal(pOff[len(pOff)-1], pOn[len(pOn)-1]) {
				t.Fatalf("%s, modo %q: a captura mudou o pedido ao provider", caso, modo)
			}
		}
	}
}

// COM A CAPTURA LIGADA: cada turno cuja resposta traz estado leva um envelope que, aberto, é o
// que a sonda tirou do corpo, ligado à rota; o digest que o runtime lhe dá não se repete entre
// duas capturas do MESMO corpo (o nonce); e nenhum byte do estado aparece no resto do turno.
func TestAOS514_Capture_EnvelopeEmCadaTurnoComEstado(t *testing.T) {
	comEstado := 0
	for _, caso := range wirefake.Nomes() {
		m := aos514ComporCaso(t, caso, modelgateway.ProviderStateCapture, "")
		out, err := m.turno(modelgateway.DefaultProviderStateMaxBytes)
		if err != nil {
			continue // a resposta foi recusada: não há turno nem estado
		}
		quer := port.ProbeProviderState(wirefake.Corpo(caso))
		if quer == nil {
			if out.State != nil || len(m.resultados) != 0 {
				t.Errorf("%s: a resposta nao traz estado e o turno leva-o: %+v", caso, out.State)
			}
			continue
		}
		comEstado++
		if out.State == nil || out.State.Status != agentruntime.ProviderStateCaptured || !reflect.DeepEqual(m.resultados, []string{modelgateway.ProviderStateResultCaptured}) {
			t.Fatalf("%s: o turno nao leva o estado capturado: %+v (resultados %v)", caso, out.State, m.resultados)
		}
		env, err := port.UnmarshalProviderStateEnvelope(out.State.Bytes)
		if err != nil {
			t.Fatalf("%s: envelope ilegivel: %v", caso, err)
		}
		if !reflect.DeepEqual(env.ProviderState, *quer) {
			t.Fatalf("%s: o envelope nao leva o estado do corpo:\n veio:  %+v\n quero: %+v", caso, env.ProviderState, *quer)
		}
		if env.RequestedModel != "gpt-4o" || env.ServedModel != out.Model || env.RouteProfileDigest != out.RouteProfileDigest || len(env.Nonce) != port.ProviderStateNonceBytes {
			t.Fatalf("%s: o envelope nao esta ligado a rota do turno: %+v", caso, env)
		}
		// O MESMO corpo, outra captura: outro nonce, outros bytes, outro digest.
		outra, err := m.turno(modelgateway.DefaultProviderStateMaxBytes)
		if err != nil || outra.State == nil {
			t.Fatalf("%s: segunda captura: %+v err=%v", caso, outra.State, err)
		}
		if bytes.Equal(outra.State.Bytes, out.State.Bytes) || outra.State.Normalizado().Digest == out.State.Normalizado().Digest {
			t.Fatalf("%s: duas capturas do mesmo corpo deram o mesmo digest — o digest seria confirmavel por tentativas", caso)
		}
	}
	if comEstado < 40 {
		t.Fatalf("so %d casos com estado: o teste nao esta a varrer os casos", comEstado)
	}
}

// D3, POR SENTINELAS. Um corpo com uma sentinela diferente em cada campo de raciocínio, em cada
// assinatura e em cada id: o texto do turno é SÓ o do `content`; o `Reasoning` de sempre é o de
// sempre; e fora do envelope do estado nenhuma sentinela nova aparece — nem no modelo servido,
// nem na ficha, nem nos resultados do observador. Com `content` vazio o texto fica vazio: o
// raciocínio não o substitui.
func TestAOS514_Capture_SentinelasSoNoEnvelope(t *testing.T) {
	sentinelas := []string{"S-RC", "S-REASONING", "S-DETAILS", "S-BLOCO", "S-ASSINATURA", "S-REDIGIDO", "S-THINKING", "S-PSF", "S-ID1", "S-TSIG", "S-FSIG", "S-TCPSF"}
	corpoCom := func(content string) string {
		return `{"id":"x","model":"modelo-servido","choices":[{"index":0,"message":{"role":"assistant","content":` + content + `,` +
			`"reasoning_content":"S-RC","reasoning":"S-REASONING","reasoning_details":[{"text":"S-DETAILS"}],` +
			`"thinking_blocks":[{"type":"thinking","thinking":"S-BLOCO","signature":"S-ASSINATURA"},{"type":"redacted_thinking","data":"S-REDIGIDO"}],` +
			`"thinking":"S-THINKING","provider_specific_fields":{"thinking":"S-PSF"},` +
			`"tool_calls":[{"id":"S-ID1","type":"function","thought_signature":"S-TSIG","function":{"name":"arquivo","arguments":"{\"p\":1}","signature":"S-FSIG"},"provider_specific_fields":{"thought_signature":"S-TCPSF"}}]},` +
			`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`
	}
	for _, c := range []struct{ content, texto string }{{`"TEXTO-DA-RESPOSTA"`, "TEXTO-DA-RESPOSTA"}, {`""`, ""}, {`null`, ""}} {
		m := aos514Compor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(corpoCom(c.content))) }),
			func() [][]byte { return nil }, modelgateway.ProviderStateCapture, modelgateway.ResponseShapeObserve)
		out, err := m.turno(modelgateway.DefaultProviderStateMaxBytes)
		if err != nil || out.State == nil {
			t.Fatalf("content=%s: turno: %+v err=%v", c.content, out.State, err)
		}
		if out.Text != c.texto {
			t.Fatalf("content=%s: o texto do turno e %q; so pode vir do content", c.content, out.Text)
		}
		if out.Reasoning != "S-RC" {
			t.Fatalf("content=%s: o Reasoning de sempre mudou: %q", c.content, out.Reasoning)
		}
		if len(out.ToolCalls) != 1 || out.ToolCalls[0].ToolID != "arquivo" || string(out.ToolCalls[0].Input) != `{"p":1}` {
			t.Fatalf("content=%s: as tool calls mudaram: %+v", c.content, out.ToolCalls)
		}
		// Tudo o que não é o envelope, nem o Reasoning de sempre.
		fora := out
		fora.State, fora.Reasoning = nil, ""
		resto := fmt.Sprintf("%+v ficha=%+v resultados=%v", fora, *out.Shape, m.resultados)
		for _, s := range sentinelas {
			if strings.Contains(resto, s) {
				t.Errorf("content=%s: a sentinela %s esta fora do envelope: %s", c.content, s, resto)
			}
			// O id utilizavel fica tambem descodificado (id_value), em texto: e o unico valor do
			// envelope que nao vai so em base64. O envelope inteiro e selado na captura.
			if s != "S-ID1" && bytes.Contains(out.State.Bytes, []byte(s)) {
				t.Errorf("content=%s: a sentinela %s esta em texto no envelope (tinha de ir em base64)", c.content, s)
			}
		}
		// E estão todas LÁ DENTRO, cada uma no seu sítio.
		env, err := port.UnmarshalProviderStateEnvelope(out.State.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		dentro := ""
		for _, f := range env.Fields {
			dentro += string(f.Raw)
		}
		for _, tc := range env.ToolCalls {
			dentro += string(tc.ID)
			for _, f := range tc.Fields {
				dentro += string(f.Raw)
			}
		}
		for _, s := range sentinelas {
			if !strings.Contains(dentro, s) {
				t.Errorf("content=%s: a sentinela %s nao esta no estado capturado", c.content, s)
			}
		}
		// A ficha conta o estado sem o conter.
		if out.Shape.ProviderState != string(agentruntime.ProviderStateCaptured) || out.Shape.ProviderStateBytes != int64(len(out.State.Bytes)) {
			t.Errorf("content=%s: a ficha nao conta o estado: %+v", c.content, *out.Shape)
		}
	}
}

// O TECTO. Acima dele o estado NÃO é truncado: o turno leva a marca de «não devolvível», sem um
// byte, o observador recebe a causa, a ficha diz o tamanho que tinha — e o turno segue igual.
// No tecto exacto cabe inteiro.
func TestAOS514_Capture_AcimaDoTectoNaoDevolvivelNuncaTruncado(t *testing.T) {
	assinatura := strings.Repeat("QUJD", 600) // 2400 bytes: sozinha já passa o tecto mínimo
	corpo := `{"model":"m","choices":[{"message":{"role":"assistant","content":"resposta","thinking_blocks":[{"type":"thinking","thinking":"","signature":"` + assinatura + `"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

	folgado := aos514ComporCorpo(t, corpo, modelgateway.ProviderStateCapture)
	inteiro, err := folgado.turno(modelgateway.DefaultProviderStateMaxBytes)
	if err != nil || inteiro.State == nil || inteiro.State.Status != agentruntime.ProviderStateCaptured {
		t.Fatalf("com folga o estado cabe: %+v err=%v", inteiro.State, err)
	}
	tamanho := len(inteiro.State.Bytes)
	env, _ := port.UnmarshalProviderStateEnvelope(inteiro.State.Bytes)
	if len(env.Fields) != 1 || !bytes.Contains(env.Fields[0].Raw, []byte(assinatura)) {
		t.Fatalf("o bloco assinado tinha de estar inteiro no envelope")
	}

	apertado := aos514ComporCorpo(t, corpo, modelgateway.ProviderStateCapture)
	for _, c := range []struct {
		max    int
		cabe   bool
		quer   string
		estado agentruntime.ProviderStateStatus
	}{
		{tamanho, true, modelgateway.ProviderStateResultCaptured, agentruntime.ProviderStateCaptured},
		{tamanho - 1, false, modelgateway.ProviderStateResultOverCeiling, agentruntime.ProviderStateNotReturnable},
		{modelgateway.MinProviderStateMaxBytes, false, modelgateway.ProviderStateResultOverCeiling, agentruntime.ProviderStateNotReturnable},
	} {
		apertado.resultados = nil
		out, err := apertado.turno(c.max)
		if err != nil || out.State == nil || out.State.Status != c.estado || !reflect.DeepEqual(apertado.resultados, []string{c.quer}) {
			t.Fatalf("tecto %d: estado %+v, resultados %v, err=%v", c.max, out.State, apertado.resultados, err)
		}
		if c.cabe != (len(out.State.Bytes) == tamanho) || (!c.cabe && len(out.State.Bytes) != 0) {
			t.Fatalf("tecto %d: o estado ou cabe inteiro (%d bytes) ou nao vai nenhum; foram %d", c.max, tamanho, len(out.State.Bytes))
		}
		// O turno segue como seguia: o mesmo texto, o mesmo fim.
		if out.Text != "resposta" || !out.Final || out.StopReason != agentruntime.StopStop {
			t.Fatalf("tecto %d: o turno mudou: %+v", c.max, out)
		}
		if norm := out.State.Normalizado(); !c.cabe && (norm.TailDigest() != "" || norm.Status != agentruntime.ProviderStateNotReturnable) {
			t.Fatalf("tecto %d: um estado nao devolvivel nao tem digest: %+v", c.max, norm)
		}
	}
	// Um tecto fora do intervalo não é aceite por quem configura, e o adaptador cai no de omissão.
	for _, mau := range []int{0, -1, modelgateway.MinProviderStateMaxBytes - 1, modelgateway.MaxProviderStateMaxBytes + 1} {
		if err := modelgateway.ValidateProviderStateMaxBytes(mau); !errors.Is(err, modelgateway.ErrBadProviderStateMaxBytes) {
			t.Errorf("tecto %d devia ser recusado: %v", mau, err)
		}
	}
	if modelgateway.MaxProviderStateMaxBytes != agentruntime.MaxProviderStateBytes {
		t.Fatalf("o maior tecto configuravel tem de ser o tecto absoluto do runtime")
	}
}

// O MODO tem vocabulário fechado, e um valor mal escrito não compõe gateway nenhum.
func TestAOS514_ModoInvalidoRecusaAComposicao(t *testing.T) {
	for _, modo := range []string{"on", "CAPTURE", "observe", "enforce", "1", "capture,off"} {
		cfg := prodConfig(audit.NewMemStore(), "http://127.0.0.1:1", nil, []modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
		cfg.ProviderState = modo
		if gw, err := modelgateway.NewProduction(context.Background(), cfg); !errors.Is(err, modelgateway.ErrBadProviderState) || gw != nil {
			t.Errorf("modo %q: queria ErrBadProviderState e nenhum gateway; veio %v", modo, err)
		}
	}
	if _, err := modelgateway.ParseProviderState(""); err == nil {
		t.Errorf("o vazio nao e aceite pelo parser: quem le a configuracao decide a omissao")
	}
	if port.Version < "1.7.0" {
		t.Errorf("o estado entrou na porta na 1.7.0; a versao e %s", port.Version)
	}
}

// ÀS ESCURAS. O pedido do turno seguinte não muda com o estado do turno anterior, em NENHUMA
// versão publicada da projecção nativa: as mensagens de um tail da 1.5.0 cujos turnos têm todos
// `state_digest` são, byte a byte, as do mesmo run na 1.4.0, e o wire não leva raciocínio,
// assinatura, id do provider nem o digest.
func TestAOS514_AsEscuras_OPedidoNativoNaoMudaComOEstado(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	tail := func(layout string, comEstado bool) []agentruntime.TailSegment {
		seq, err := agentruntime.NewTailSequence(layout)
		if err != nil {
			t.Fatal(err)
		}
		segs := []agentruntime.TailSegment{aos490Objectivo("Le o documento notes e resume")}
		d := ""
		if comEstado {
			d = digest
		}
		for i, turno := range []struct {
			texto string
			res   []agentruntime.CapturedToolResult
		}{
			{"vou ler", []agentruntime.CapturedToolResult{aos490Permitida("doc_read", `{"doc_id":"notes"}`, "conteudo"), aos490Negada("secret_read", `{"path":"/x"}`)}},
			{"", []agentruntime.CapturedToolResult{aos490Permitida("doc_read", `{"doc_id":"mais"}`, "mais conteudo")}},
		} {
			s, _ := seq.TurnWithState(fmt.Sprintf("step-%06d", i+1), turno.texto, d, turno.res)
			segs = append(segs, s...)
		}
		return segs
	}
	wire := func(layout string, comEstado bool, opts ...modelgateway.RuntimeAdapterOption) []byte {
		t.Helper()
		req, _, err := aos490Pedir(t, aos490Vista(t, layout, "You are a careful assistant.", tail(layout, comEstado)), opts...)
		if err != nil {
			t.Fatalf("Call(%s): %v", layout, err)
		}
		cru, err := req.MarshalWire(false)
		if err != nil {
			t.Fatal(err)
		}
		return cru
	}
	for _, versao := range []string{modelgateway.NativeProjectionVersion, modelgateway.NativeProjectionVersion110, modelgateway.NativeProjectionVersion120} {
		opts := []modelgateway.RuntimeAdapterOption{modelgateway.WithProjection(modelgateway.ProjectionNative), modelgateway.WithProjectionVersion(versao)}
		base := wire(agentruntime.AssemblyVersion140, false, opts...)
		for _, comEstado := range []bool{false, true} {
			got := wire(agentruntime.AssemblyVersion150, comEstado, append(opts, modelgateway.WithProviderStateCapture(modelgateway.DefaultProviderStateMaxBytes, nil))...)
			if !bytes.Equal(got, base) {
				t.Fatalf("projeccao %s, estado=%v: o pedido da 1.5.0 nao e o da 1.4.0:\n 1.5.0: %s\n 1.4.0: %s", versao, comEstado, got, base)
			}
			for _, proibido := range []string{"state_digest", digest, "reasoning", "thinking", "signature"} {
				if bytes.Contains(got, []byte(proibido)) {
					t.Fatalf("projeccao %s: o pedido leva %q", versao, proibido)
				}
			}
		}
	}
	// Em TEXTO ÚNICO o prompt materializado é o que se envia, e o rótulo — o digest, nunca o
	// estado — está na linha de delimitação. Sem estado, é o texto da 1.4.0 byte a byte.
	if !bytes.Equal(wire(agentruntime.AssemblyVersion150, false), wire(agentruntime.AssemblyVersion140, false)) {
		t.Fatalf("em texto unico, a 1.5.0 sem estado tem de ser a 1.4.0")
	}
	comRotulo := wire(agentruntime.AssemblyVersion150, true)
	if n := bytes.Count(comRotulo, []byte("state_digest="+digest)); n != 2 {
		t.Fatalf("em texto unico o rotulo aparece uma vez por turno com estado (2); apareceu %d", n)
	}
}

// MarshalWire CONTINUA A RETIRAR O RACIOCÍNIO de todos os pedidos, e o estado não tem por onde
// sair: uma mensagem `assistant` reencaminhada tal como veio de uma resposta com estado
// capturado sai sem raciocínio, sem blocos e sem assinaturas.
func TestAOS514_AsEscuras_MarshalWireNaoLevaEstado(t *testing.T) {
	m := aos514ComporCaso(t, "rac_blocos_assinados", modelgateway.ProviderStateCapture, "")
	resp, err := m.gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
	if err != nil || resp.State == nil {
		t.Fatalf("Chat: estado=%+v err=%v", resp.State, err)
	}
	pedido := aos505Pedido("gpt-4o")
	pedido.Messages = append(pedido.Messages, resp.Choices[0].Message)
	cru, err := pedido.MarshalWire(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, proibido := range []string{"thinking", "signature", "c2ln", "reasoning", "bloco"} {
		if bytes.Contains(cru, []byte(proibido)) {
			t.Fatalf("o pedido leva %q: %s", proibido, cru)
		}
	}
	if _, err := m.gw.Chat(context.Background(), pedido); err != nil {
		t.Fatal(err)
	}
	enviados := m.pedidos()
	if !bytes.Equal(enviados[len(enviados)-1], cru) {
		t.Fatalf("o que chegou ao provider nao e o MarshalWire do pedido")
	}
}

// DESALINHADO ⇒ NÃO DEVOLVÍVEL, COM CAUSA PRÓPRIA (revisão, achado C1). Um corpo em que a sonda
// e o descodificador não vêem a mesma mensagem dá um turno como dava — as tool calls e o texto
// são os do descodificador —, com o estado marcado «não devolvível», sem bytes, e a causa
// contada em vocabulário fechado.
func TestAOS514_Capture_CorpoAnomaloFicaNaoDevolvivel(t *testing.T) {
	corpo := `{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"Reasoning_Content":"SEGREDO-C1","TOOL_CALLS":[{"id":"call_X","type":"function","function":{"name":"arquivo","arguments":"{}"},"thought_signature":"U0lH"}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
	m := aos514ComporCorpo(t, corpo, modelgateway.ProviderStateCapture)
	out, err := m.turno(modelgateway.DefaultProviderStateMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ToolID != "arquivo" {
		t.Fatalf("pre-condicao: o descodificador le a tool call: %+v", out.ToolCalls)
	}
	if out.State == nil || out.State.Status != agentruntime.ProviderStateNotReturnable || len(out.State.Bytes) != 0 {
		t.Fatalf("o estado de um corpo anomalo tinha de ficar nao devolvivel, sem bytes: %+v", out.State)
	}
	if !reflect.DeepEqual(m.resultados, []string{modelgateway.ProviderStateResultMisaligned}) {
		t.Fatalf("a causa tinha de ser contada: %v", m.resultados)
	}
	if got := modelgateway.ProviderStateResults(); len(got) != 4 {
		t.Fatalf("o vocabulario de resultados tem quatro valores: %v", got)
	}
}
