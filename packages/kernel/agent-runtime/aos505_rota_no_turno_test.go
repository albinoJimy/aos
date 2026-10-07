package agentruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// AOS-505 — o `turn.recorded` guarda o resultado da comparação da rota e o digest do perfil,
// declarados pelo cliente de modelo. Campos aditivos: um turno sem rota comparada grava os bytes
// de sempre.

const aos505DigestDoPerfil = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// aos505Appender guarda o payload do último evento gravado.
type aos505Appender struct{ payload string }

func (a *aos505Appender) Append(_ context.Context, _ string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	a.payload = string(in.Payload)
	return eventstore.AppendResult{Seq: 1}, nil
}

func aos505Gravar(t *testing.T, rec TurnRecord) string {
	t.Helper()
	ap := &aos505Appender{}
	rec.RunID, rec.StepID, rec.Turn = "run-505", "step-000001", 1
	rec.Usage = Usage{InputTokens: 10, OutputTokens: 5}
	if _, err := NewTurnRecorder(ap).Record(context.Background(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}
	return ap.payload
}

func TestAOS505_Turno_SemRotaComparada_BytesDeSempre(t *testing.T) {
	base := aos505Gravar(t, TurnRecord{Manifest: Manifest{Model: ModelManifest{ModelID: "gpt-4o", ServedModelID: "gpt-4o"}}})
	if strings.Contains(base, "route_") {
		t.Fatalf("um turno sem rota comparada nao grava nenhum campo da rota: %s", base)
	}
	const quer = `{"turn":1,"manifest":{"schema_version":"1.0","prompt_hash":"","system_hash":"","assembly_version":"` + AssemblyVersion + `","model":{"model_id":"gpt-4o","served_model_id":"gpt-4o","seed":0}},"input_tokens":10,"output_tokens":5,"cost_micro_usd":0,"tool_calls_requested":0,"final":false}`
	if base != quer {
		t.Fatalf("os bytes de um turno sem rota mudaram:\n veio:  %s\n quero: %s", base, quer)
	}
}

func TestAOS505_Turno_RotaComparada_GravaOsDoisCampos(t *testing.T) {
	for _, check := range []RouteCheck{RouteEqual, RouteDifferent, RouteUnreported} {
		got := aos505Gravar(t, TurnRecord{
			Manifest:   Manifest{Model: ModelManifest{ModelID: "gpt-4o", ServedModelID: "openai/k3", RouteProfileDigest: aos505DigestDoPerfil}},
			RouteCheck: check,
		})
		if !strings.Contains(got, `"served_model_id":"openai/k3","seed":0,"route_profile_digest":"`+aos505DigestDoPerfil+`"}`) ||
			!strings.HasSuffix(got, `,"route_check":"`+string(check)+`"}`) {
			t.Fatalf("%s: o turno nao gravou a rota: %s", check, got)
		}
	}
	// Não reportado: o modelo servido fica AUSENTE.
	got := aos505Gravar(t, TurnRecord{
		Manifest:   Manifest{Model: ModelManifest{ModelID: "gpt-4o", RouteProfileDigest: aos505DigestDoPerfil}},
		RouteCheck: RouteUnreported,
	})
	if strings.Contains(got, "served_model_id") {
		t.Fatalf("um modelo nao reportado nao grava served_model_id: %s", got)
	}
}

// O recorder é exportado: fecha o vocabulário e a forma do digest à entrada.
func TestAOS505_Turno_ForaDoVocabulario(t *testing.T) {
	got := aos505Gravar(t, TurnRecord{
		Manifest:   Manifest{Model: ModelManifest{ModelID: "gpt-4o", RouteProfileDigest: "texto livre de um cliente"}},
		RouteCheck: "RESULTADO-BRUTO",
	})
	if strings.Contains(got, "RESULTADO-BRUTO") || strings.Contains(got, "texto livre") || !strings.Contains(got, `"route_check":"nao_reportado"`) || strings.Contains(got, "route_profile_digest") {
		t.Fatalf("o recorder deixou passar texto livre: %s", got)
	}
}

func TestAOS505_RouteCheck_Normalizado(t *testing.T) {
	for entra, sai := range map[RouteCheck]RouteCheck{
		"": RouteUngoverned, "igual": RouteEqual, "diferente": RouteDifferent, "nao_reportado": RouteUnreported,
		"Igual": RouteUnreported, "equal": RouteUnreported, " igual": RouteUnreported,
	} {
		if got := entra.Normalizado(); got != sai {
			t.Errorf("%q.Normalizado() = %q, quero %q", entra, got, sai)
		}
	}
	for entra, sai := range map[string]string{
		aos505DigestDoPerfil:                  aos505DigestDoPerfil,
		"":                                    "",
		"sha256:invalido":                     "",
		strings.ToUpper(aos505DigestDoPerfil): "",
		"sha512:" + aos505DigestDoPerfil[7:]:  "",
		aos505DigestDoPerfil + "0":            "",
		"sha256:" + strings.Repeat("g", 64):   "",
	} {
		if got := NormalizeRouteProfileDigest(entra); got != sai {
			t.Errorf("NormalizeRouteProfileDigest(%q) = %q, quero %q", entra, got, sai)
		}
	}
}
