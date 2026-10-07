package replay

import (
	"bytes"
	"encoding/json"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-505 — a captura do turno guarda o modelo servido, o resultado da comparação da rota e o
// digest do perfil, e a retoma e o replay devolvem-nos iguais. SÓ num turno cuja rota foi
// comparada: com a governação da rota desligada a captura tem os bytes de sempre.

const aos505Digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func aos505Resposta(check agentruntime.RouteCheck, servido string) agentruntime.ModelResponse {
	return agentruntime.ModelResponse{
		Text:               "feito",
		Final:              true,
		Usage:              agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
		CostMicroUSD:       1200,
		Model:              servido,
		RouteCheck:         check,
		RouteProfileDigest: aos505Digest,
	}
}

// DESLIGADA: os bytes de sempre. O modelo servido NÃO entra na captura (nunca entrou), mesmo
// preenchido na resposta; e um digest sem resultado também não.
func TestAOS505_Captura_RotaNaoComparada_BytesDeSempre(t *testing.T) {
	const antiga = `{"text":"feito","final":true,"input_tokens":10,"output_tokens":5,"cost_micro_usd":1200}`
	bs, err := json.Marshal((&EventStoreCapturer{}).encodeResponse(aos505Resposta(agentruntime.RouteUngoverned, "gpt-4o")))
	if err != nil {
		t.Fatal(err)
	}
	if string(bs) != antiga {
		t.Fatalf("uma resposta sem rota comparada tinha de gravar os bytes de sempre:\n veio:  %s\n quero: %s", bs, antiga)
	}
	// Uma captura gravada antes do ticket descodifica com os três vazios.
	var rc responseCapture
	if err := json.Unmarshal([]byte(antiga), &rc); err != nil {
		t.Fatal(err)
	}
	if got := rc.decode(); got.Model != "" || got.RouteCheck != agentruntime.RouteUngoverned || got.RouteProfileDigest != "" {
		t.Fatalf("uma captura antiga tinha de dar a rota vazia: %q %q %q", got.Model, got.RouteCheck, got.RouteProfileDigest)
	}
}

// COMPARADA: os três valores vão e voltam iguais, em cada resultado do vocabulário — incluindo o
// modelo VAZIO de um turno não reportado, que não é preenchido com nada.
func TestAOS505_Captura_RotaComparada_VoltaIgual(t *testing.T) {
	for _, c := range []struct {
		check   agentruntime.RouteCheck
		servido string
	}{
		{agentruntime.RouteEqual, "openai/k3"},
		{agentruntime.RouteDifferent, "openai/modelo-trocado"},
		{agentruntime.RouteUnreported, ""},
	} {
		raw := aos490Capturar(t, nil, aos505Resposta(c.check, c.servido))
		var p capturePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		got := p.Response.decode()
		if got.Model != c.servido || got.RouteCheck != c.check || got.RouteProfileDigest != aos505Digest {
			t.Fatalf("%s: voltou modelo %q check %q digest %q", c.check, got.Model, got.RouteCheck, got.RouteProfileDigest)
		}
		if c.servido == "" && bytes.Contains(raw, []byte("served_model")) {
			t.Fatalf("um modelo nao reportado nao grava a chave: %s", raw)
		}
	}
}

// SELADO (o modo de PRODUÇÃO): a rota é medição — fica em claro no resumo de consumo, como o
// motivo de paragem, e também dentro do conteúdo selado, que é o que o replay lê.
func TestAOS505_Captura_Selada_RotaNoResumoDeConsumo(t *testing.T) {
	cipher := newCapFakeCipher()
	raw := aos490Capturar(t, []CapturerOption{WithContentSealer(cipher)}, aos505Resposta(agentruntime.RouteDifferent, "openai/modelo-trocado"))
	var p capturePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Response.RouteCheck != "diferente" || p.Response.ServedModel != "openai/modelo-trocado" || p.Response.RouteProfileDigest != aos505Digest || p.Response.Text != "" {
		t.Fatalf("o resumo em claro devia levar a rota e nao o texto: %+v", p.Response)
	}
	got := aos490Abrir(t, cipher, p).Response.decode()
	if got.Model != "openai/modelo-trocado" || got.RouteCheck != agentruntime.RouteDifferent {
		t.Fatalf("o envelope devolve %q %q", got.Model, got.RouteCheck)
	}
}

// VOCABULÁRIO FECHADO nas duas pontas: um resultado fora dele é guardado e devolvido como
// `nao_reportado` (um resultado ilegível não prova igualdade), e um digest malformado sai vazio.
func TestAOS505_Captura_ForaDoVocabulario(t *testing.T) {
	resp := aos505Resposta("RESULTADO-BRUTO", "openai/k3")
	resp.RouteProfileDigest = "nao-e-um-digest"
	raw := aos490Capturar(t, nil, resp)
	if bytes.Contains(raw, []byte("RESULTADO-BRUTO")) || bytes.Contains(raw, []byte("nao-e-um-digest")) || !bytes.Contains(raw, []byte(`"route_check":"nao_reportado"`)) {
		t.Fatalf("um resultado fora do vocabulario tinha de ser guardado como nao_reportado, e o digest malformado fora: %s", raw)
	}
	got := responseCapture{RouteCheck: "OUTRA-COISA", ServedModel: "x", RouteProfileDigest: "sha256:CURTO"}.decode()
	if got.RouteCheck != agentruntime.RouteUnreported || got.RouteProfileDigest != "" {
		t.Fatalf("na leitura: %q %q", got.RouteCheck, got.RouteProfileDigest)
	}
}
