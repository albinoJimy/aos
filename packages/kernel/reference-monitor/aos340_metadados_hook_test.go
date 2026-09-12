package referencemonitor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// AOS-340 — o canal ESTRUTURADO da decisão de um hook.
//
// Antes disto, um hook que negava tinha dois canais: o `Reason`, texto livre, e o
// `PolicyVersion`, que é do hook de política. As obrigações não servem — são vocabulário
// FECHADO de imposição, e uma obrigação anexada só para documentar uma decisão negaria a
// call num permit. O AOS-332 bateu nisto e selou a postura do broker num sufixo de texto.
// ---------------------------------------------------------------------------

// hookComMetadata devolve a decisão dada, com metadados anexados.
type hookComMetadata struct {
	decisao HookDecision
	meta    map[string]string
}

func (hookComMetadata) Name() string { return "hook-com-metadata" }
func (h hookComMetadata) Evaluate(context.Context, *Call) (HookResult, error) {
	return HookResult{Decision: h.decisao, Reason: "razao original", Metadata: h.meta}, nil
}

func posturaDeTeste() map[string]string {
	return map[string]string{"provider_policy": "enforced", "resource_binding": "unset"}
}

// TestAOS340_NegacaoSelaOsMetadadosDoHook é o AC central: o hook que nega anexa pares
// chave/valor e o RM sela-os no registo.
func TestAOS340_NegacaoSelaOsMetadadosDoHook(t *testing.T) {
	m, sink := monitorComHook(t, hookComMetadata{decisao: HookDeny, meta: posturaDeTeste()})

	dec, err := m.Mediate(context.Background(), chamadaDeTeste())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectDeny {
		t.Fatalf("preparacao: queria deny, veio %v", dec.Effect)
	}
	if got := sink.ultimo.Metadata["provider_policy"]; got != "enforced" {
		t.Errorf("metadata[provider_policy] = %q, esperado %q (veio %+v)", got, "enforced", sink.ultimo.Metadata)
	}
	if got := sink.ultimo.Metadata["resource_binding"]; got != "unset" {
		t.Errorf("metadata[resource_binding] = %q, esperado %q", got, "unset")
	}
	// CONTROLO: o `Reason` continua a levar a razão em texto livre. O canal acrescenta uma
	// leitura por máquina, não substitui a leitura por pessoa.
	if sink.ultimo.Reason != "razao original" {
		t.Errorf("o registo perdeu a razao em texto livre: %q", sink.ultimo.Reason)
	}
	// CONTROLO: metadados NÃO são obrigações. Selar um não pode encher o outro.
	if len(sink.ultimo.Obligations) != 0 {
		t.Errorf("os metadados vazaram para Obligations: %+v", sink.ultimo.Obligations)
	}
}

// TestAOS340_EscaladaTambemSela: a regra é «o resultado do hook que TERMINOU a mediação», e a
// escalada termina-a tanto quanto a negação. Sem isto a regra teria uma excepção por explicar.
func TestAOS340_EscaladaTambemSela(t *testing.T) {
	m, sink := monitorComHook(t, hookComMetadata{decisao: HookEscalate, meta: posturaDeTeste()})

	dec, err := m.Mediate(context.Background(), chamadaDeTeste())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectEscalate {
		t.Fatalf("preparacao: queria escalate, veio %v", dec.Effect)
	}
	if sink.ultimo.Metadata["provider_policy"] != "enforced" {
		t.Errorf("a escalada nao selou os metadados: %+v", sink.ultimo.Metadata)
	}
}

// TestAOS340_ChaveDesconhecidaNaoNegaUmPermit é o controlo que separa este canal das
// obrigações, e é a razão de ele existir em vez de se reusar [Obligation].
//
// `enforceObligations` nega fail-closed qualquer `Type` que não saiba cumprir. Se os metadados
// passassem por lá — ou fossem obrigações disfarçadas — um hook que documentasse a sua decisão
// com uma chave inventada NEGARIA a call. Aqui a chave é tão inventada quanto possível e a call
// tem de PASSAR.
func TestAOS340_ChaveDesconhecidaNaoNegaUmPermit(t *testing.T) {
	s := &sinkQueGuarda{}
	h := hookComMetadata{decisao: HookAllow, meta: map[string]string{"chave_completamente_inventada": "x"}}
	m := New(WithHooks(h), WithEventSink(s))
	if err := m.Register("doc_read", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	dec, err := m.Mediate(context.Background(), chamadaDeTeste())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != EffectPermit {
		t.Fatalf("uma chave de metadados desconhecida NEGOU um permit (%s): o canal esta a passar "+
			"por enforcement e nao devia", dec.Reason)
	}
}

// TestAOS340_ErroDeHookNaoSelaMetadados fixa a excepção declarada: um erro não é uma decisão do
// hook, e um resultado a meio não ganha autoridade por vir acompanhado de erro.
func TestAOS340_ErroDeHookNaoSelaMetadados(t *testing.T) {
	m, sink := monitorComHook(t, hookQueErraComMetadata{})

	dec, err := m.Mediate(context.Background(), chamadaDeTeste())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Code != CodeHookError {
		t.Fatalf("preparacao: queria %s, veio %s", CodeHookError, dec.Code)
	}
	if len(sink.ultimo.Metadata) != 0 {
		t.Errorf("um hook que ERROU selou metadados: %+v", sink.ultimo.Metadata)
	}
}

type hookQueErraComMetadata struct{}

func (hookQueErraComMetadata) Name() string { return "hook-que-erra" }
func (hookQueErraComMetadata) Evaluate(context.Context, *Call) (HookResult, error) {
	return HookResult{Metadata: map[string]string{"nao": "devia-selar"}}, errFalhaDeTeste
}

var errFalhaDeTeste = &MonitorError{Code: "E_TESTE", msg: "falha de teste"}

// TestAOS340_MetadadosChegamAoPayloadDoEvento fecha o AC do lado do que fica GRAVADO: o campo tem
// de aparecer no JSON de `tool.call.denied`, e um registo sem metadados tem de continuar a
// produzir um payload SEM o campo (é o que torna o acréscimo MINOR no `port_version`).
func TestAOS340_MetadadosChegamAoPayloadDoEvento(t *testing.T) {
	comMeta := payloadDeMediacao(t, MediationRecord{
		Effect: EffectDeny, ToolID: "doc_read", Metadata: posturaDeTeste(),
	})
	if comMeta.Metadata["provider_policy"] != "enforced" {
		t.Errorf("o payload do evento nao leva os metadados: %+v", comMeta.Metadata)
	}

	semMeta := payloadDeMediacao(t, MediationRecord{Effect: EffectDeny, ToolID: "doc_read"})
	if semMeta.Metadata != nil {
		t.Errorf("um registo sem metadados produziu o campo no payload: %+v", semMeta.Metadata)
	}
	if bruto := payloadBruto(t, MediationRecord{Effect: EffectDeny, ToolID: "doc_read"}); strings.Contains(bruto, "metadata") {
		t.Errorf("`omitempty` nao funcionou — o payload de quem nada acrescenta mudou: %s", bruto)
	}
}

// sinkQueCapturaPayload materializa o payload JSON que o [NewEventStoreSink] gravaria.
type storeQueCaptura struct{ ultimo []byte }

func (s *storeQueCaptura) Append(_ context.Context, _ string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	s.ultimo = in.Payload
	return eventstore.AppendResult{Seq: 1}, nil
}

func payloadBruto(t *testing.T, rec MediationRecord) string {
	t.Helper()
	cap := &storeQueCaptura{}
	sink := &eventStoreSink{store: cap}
	if _, err := sink.RecordMediation(context.Background(), rec); err != nil {
		t.Fatalf("RecordMediation: %v", err)
	}
	return string(cap.ultimo)
}

func payloadDeMediacao(t *testing.T, rec MediationRecord) mediationPayload {
	t.Helper()
	var p mediationPayload
	if err := json.Unmarshal([]byte(payloadBruto(t, rec)), &p); err != nil {
		t.Fatalf("payload ilegivel: %v", err)
	}
	return p
}
