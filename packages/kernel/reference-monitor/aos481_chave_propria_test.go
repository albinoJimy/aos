package referencemonitor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// aos481_chave_propria_test.go — AOS-481: cada decisão e o desfecho de uma tool call ficam no
// stream do run como eventos distintos. Tudo passa pelo Monitor real e pelos dois sinks reais
// sobre o Event Store de referência, e conta-se o que FICOU no stream (não o que os sinks
// receberam): um evento engolido como duplicado só se vê aí.

// aos481Hook escala (ou nega) a call que chega sem evidência de aprovação e decide `depois` a
// que chega com ela — a forma do ciclo escalada → aprovação → retoma no MESMO passo.
type aos481Hook struct {
	antes, depois HookDecision
}

func (aos481Hook) Name() string { return "policy" }

func (h aos481Hook) Evaluate(_ context.Context, c *Call) (HookResult, error) {
	d := h.antes
	if len(c.ApprovalEvidence) > 0 {
		d = h.depois
	}
	return HookResult{Decision: d, Reason: "aos481"}, nil
}

// aos481Monitor compõe o RM com o canal de mediação e o de desfecho no MESMO Event Store, como
// o nó (integration/secured.go, AOS-379 + ADR-025).
func aos481Monitor(t *testing.T, h Hook) (*Monitor, eventstore.EventStore) {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	m := New(WithHooks(h), WithEventSink(NewEventStoreSink(es)), WithOutcomeSink(NewEventStoreOutcomeSink(es)))
	for _, tool := range []string{"tool.echo", "tool.outra"} {
		if err := m.Register(tool, func(context.Context, []byte) ([]byte, error) { return []byte("ok"), nil }); err != nil {
			t.Fatal(err)
		}
	}
	return m, es
}

func aos481Mediar(t *testing.T, m *Monitor, c Call, quer Effect) Decision {
	t.Helper()
	d, err := m.Mediate(context.Background(), c)
	if err != nil || d.Effect != quer {
		t.Fatalf("Mediate: eff=%q err=%v, quer %q", d.Effect, err, quer)
	}
	return d
}

// aos481Tipos conta os tipos no stream do run e verifica a correlação: o envelope leva a chave do
// tipo, e o payload o `step_id` da call quando o envelope não é ele (no `mediated` fica omitido:
// o payload do selo fica byte-a-byte o de antes).
func aos481Tipos(t *testing.T, es eventstore.EventStore, c Call) (map[string]int, []eventstore.Event) {
	t.Helper()
	evs, err := es.Read(context.Background(), c.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	n := map[string]int{}
	for _, ev := range evs {
		n[ev.Type]++
		var p struct {
			StepID string `json:"step_id"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("payload de %s: %v", ev.Type, err)
		}
		quer := c.StepID
		if ev.Type == EventTypeMediated {
			quer = ""
		}
		if p.StepID != quer {
			t.Errorf("%s: step_id no payload %q, quer %q", ev.Type, p.StepID, quer)
		}
		if want := EventStepID(ev.Type, c.StepID); ev.StepID != want {
			t.Errorf("%s: step_id do envelope %q, quer %q", ev.Type, ev.StepID, want)
		}
	}
	return n, evs
}

func aos481Igual(t *testing.T, caso string, got, want map[string]int) {
	t.Helper()
	for tipo, w := range want {
		if got[tipo] != w {
			t.Errorf("%s: %s = %d no stream, quer %d (tipos no stream: %v)", caso, tipo, got[tipo], w, got)
		}
	}
	for tipo, g := range got {
		if _, ok := want[tipo]; !ok {
			t.Errorf("%s: tipo inesperado no stream: %s = %d", caso, tipo, g)
		}
	}
}

// TestAOS481_PermitDirecto (caso 1): o selo e o desfecho. Antes, só o selo.
func TestAOS481_PermitDirecto(t *testing.T) {
	m, es := aos481Monitor(t, aos481Hook{antes: HookAllow, depois: HookAllow})
	c := baseCall()
	aos481Mediar(t, m, c, EffectPermit)
	n, _ := aos481Tipos(t, es, c)
	aos481Igual(t, "permit directo", n, map[string]int{EventTypeMediated: 1, EventTypeOutcome: 1})
}

// TestAOS481_EscaladaDepoisPermit (caso 2): escalada, aprovação, permit no MESMO passo. Antes, só
// a escalada. E o MediationSeq do permit aponta para o selo do permit, não para a escalada.
func TestAOS481_EscaladaDepoisPermit(t *testing.T) {
	m, es := aos481Monitor(t, aos481Hook{antes: HookEscalate, depois: HookAllow})
	c := baseCall()
	esc := aos481Mediar(t, m, c, EffectEscalate)
	aprovada := c
	aprovada.ApprovalEvidence = []byte("grant-481")
	perm := aos481Mediar(t, m, aprovada, EffectPermit)

	n, evs := aos481Tipos(t, es, c)
	aos481Igual(t, "escalada → permit", n, map[string]int{EventTypeEscalated: 1, EventTypeMediated: 1, EventTypeOutcome: 1})

	if perm.MediationSeq == esc.MediationSeq {
		t.Fatalf("MediationSeq do permit (%d) é o da escalada", perm.MediationSeq)
	}
	for _, ev := range evs {
		if ev.Seq == perm.MediationSeq && ev.Type != EventTypeMediated {
			t.Fatalf("MediationSeq do permit (%d) aponta para %s", perm.MediationSeq, ev.Type)
		}
		if ev.Seq == esc.MediationSeq && ev.Type != EventTypeEscalated {
			t.Fatalf("MediationSeq da escalada (%d) aponta para %s", esc.MediationSeq, ev.Type)
		}
	}
}

// TestAOS481_EscaladaDepoisDeny (caso 2, deny): a negação pós-aprovação fica no log. Antes, só a
// escalada. O MediationSeq da negação aponta para ela.
func TestAOS481_EscaladaDepoisDeny(t *testing.T) {
	m, es := aos481Monitor(t, aos481Hook{antes: HookEscalate, depois: HookDeny})
	c := baseCall()
	esc := aos481Mediar(t, m, c, EffectEscalate)
	aprovada := c
	aprovada.ApprovalEvidence = []byte("grant-481")
	neg := aos481Mediar(t, m, aprovada, EffectDeny)

	n, evs := aos481Tipos(t, es, c)
	aos481Igual(t, "escalada → deny", n, map[string]int{EventTypeEscalated: 1, EventTypeDenied: 1})
	if neg.MediationSeq == esc.MediationSeq {
		t.Fatalf("MediationSeq da negação (%d) é o da escalada", neg.MediationSeq)
	}
	for _, ev := range evs {
		if ev.Seq == neg.MediationSeq && ev.Type != EventTypeDenied {
			t.Fatalf("MediationSeq da negação (%d) aponta para %s", neg.MediationSeq, ev.Type)
		}
	}
}

// TestAOS481_RecusaPelaListaDepoisPermit (caso 4): a recusa pela lista-branca do run e, na mesma
// posição (retoma sem captura, modelo re-interrogado), o permit de uma tool que a lista admite.
// Antes, o log mostrava só a recusa.
func TestAOS481_RecusaPelaListaDepoisPermit(t *testing.T) {
	m, es := aos481Monitor(t, aos481Hook{antes: HookAllow, depois: HookAllow})
	recusada := baseCall()
	recusada.AllowedTools = []string{"tool.outra"}
	d := aos481Mediar(t, m, recusada, EffectDeny)
	if d.Code != CodeToolOutsideRunAllowlist {
		t.Fatalf("código da recusa %q, quer %q", d.Code, CodeToolOutsideRunAllowlist)
	}
	permitida := recusada
	permitida.ToolID = "tool.outra"
	aos481Mediar(t, m, permitida, EffectPermit)

	n, _ := aos481Tipos(t, es, recusada)
	aos481Igual(t, "recusa → permit", n, map[string]int{EventTypeDenied: 1, EventTypeMediated: 1, EventTypeOutcome: 1})
}

// TestAOS481_ReexecucaoDeduplica: a re-execução da MESMA decisão no mesmo passo continua a
// deduplicar, em cada tipo — a chave é f(passo, tipo), sem sufixo de tentativa.
func TestAOS481_ReexecucaoDeduplica(t *testing.T) {
	casos := []struct {
		nome string
		hook aos481Hook
		quer Effect
		tipo map[string]int
	}{
		{"permit", aos481Hook{antes: HookAllow}, EffectPermit, map[string]int{EventTypeMediated: 1, EventTypeOutcome: 1}},
		{"deny", aos481Hook{antes: HookDeny}, EffectDeny, map[string]int{EventTypeDenied: 1}},
		{"escalate", aos481Hook{antes: HookEscalate}, EffectEscalate, map[string]int{EventTypeEscalated: 1}},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			m, es := aos481Monitor(t, tc.hook)
			c := baseCall()
			primeira := aos481Mediar(t, m, c, tc.quer)
			segunda := aos481Mediar(t, m, c, tc.quer)
			if tc.quer != EffectPermit && primeira.MediationSeq != segunda.MediationSeq {
				t.Errorf("a re-execução gravou um segundo selo: seq %d e %d", primeira.MediationSeq, segunda.MediationSeq)
			}
			n, _ := aos481Tipos(t, es, c)
			aos481Igual(t, "re-execução "+tc.nome, n, tc.tipo)
		})
	}
}

// TestAOS481_ChavesNaoColidem: as chaves dos quatro tipos para o mesmo passo são distintas entre
// si e de todos os domínios de dedup do stream do run. É o que faz um prefixo vazio — ou dois
// prefixos iguais — avermelhar, além dos testes do stream acima.
func TestAOS481_ChavesNaoColidem(t *testing.T) {
	const passo = "step-000003-tool-1"
	vistas := map[string]string{}
	for _, tipo := range []string{EventTypeMediated, EventTypeDenied, EventTypeEscalated, EventTypeOutcome} {
		k := EventStepID(tipo, passo)
		if outro, ok := vistas[k]; ok {
			t.Fatalf("%s e %s partilham a chave %q", tipo, outro, k)
		}
		vistas[k] = tipo
		if !strings.HasSuffix(k, passo) {
			t.Errorf("%s: a chave %q não é função do passo %q", tipo, k, passo)
		}
	}
	if EventStepID(EventTypeMediated, passo) != passo {
		t.Errorf("o selo pré-efeito tem de manter o passo nu (retro-compatibilidade dos logs gravados)")
	}
	for _, dominio := range []string{"step-", "ledger-", "ckpt-", "cap-", "custo-modelo-", "consumo-", "ctrl-", "state-", "comp-", "lease-"} {
		for _, p := range []string{StepPrefixDenied, StepPrefixEscalated, StepPrefixOutcome} {
			if strings.HasPrefix(p, dominio) || strings.HasPrefix(dominio, p) {
				t.Errorf("o prefixo %q colide com o domínio de dedup %q", p, dominio)
			}
		}
	}
	for _, tipo := range []string{EventTypeDenied, EventTypeEscalated, EventTypeOutcome} {
		if EventStepID(tipo, "") != "" {
			t.Errorf("%s: um passo vazio tem de ficar vazio", tipo)
		}
	}
}

// TestAOS481_PermitAprovadoLevaAMarcaHumana — o permit destravado por uma aprovação humana
// VERIFICADA (ApprovalGate) diz `human_approved` no payload; o permit directo não o leva, e o seu
// payload fica o de antes. É o sinal com que a fiabilidade da promoção conta a call como «precisou
// de humano» mesmo sem ver a escalada (emenda de 2026-10-18 ao ADR-025).
func TestAOS481_PermitAprovadoLevaAMarcaHumana(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	m := New(WithHooks(NewApprovalGate(&aos478Verificador{}), &spyHook{name: "policy", result: HookResult{Decision: HookAllow}}),
		WithEventSink(NewEventStoreSink(es)))
	if err := m.Register("tool.echo", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	directo := baseCall()
	aprovado := baseCall()
	aprovado.StepID = "step-2"
	aprovado.ApprovalEvidence = []byte("grant-1")
	aos481Mediar(t, m, directo, EffectPermit)
	aos481Mediar(t, m, aprovado, EffectPermit)
	evs, err := es.Read(context.Background(), directo.RunID, 1)
	if err != nil || len(evs) != 2 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if strings.Contains(string(evs[0].Payload), "human_approved") {
		t.Fatalf("o permit directo leva a marca: %s", evs[0].Payload)
	}
	if !strings.Contains(string(evs[1].Payload), `"human_approved":true`) {
		t.Fatalf("o permit aprovado nao leva a marca: %s", evs[1].Payload)
	}
}
