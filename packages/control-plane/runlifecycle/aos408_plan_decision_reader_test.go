package runlifecycle_test

// AOS-408 — o predicado que decide se um plano de risco pode correr: `AprovadoPorHumano(hash)`.
//
// Três condições inseparáveis, cada uma testada por si, porque cada uma já foi o furo:
//   - a decisão tem de ser APROVADA (e a primeira decisão terminal vence);
//   - tem de ser DESTE hash — a âncora é o hash da decisão, não o do primeiro `plan.validated`
//     (2.ª decomposição no mesmo plan_id: uma âncora, decisões de hashes diferentes);
//   - tem de ser de um HUMANO (`hitl:`) — a auto-aprovação por nível de autonomia existe para planos
//     sem risco e não pode autorizar um plano de risco.
//
// A primeira versão deste ficheiro não existia: a regra do `hitl:` estava «testada» por um literal de
// string no `aos-orq`, que continuaria verde se o predicado deixasse de a verificar.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/substrate/eventstore"
)

const aos408Plano = "run-aos408-plan"

func aos408Apensar(t *testing.T, store *eventstore.Store, tipo, passo string, corpo any) {
	t.Helper()
	raw, err := json.Marshal(corpo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), aos408Plano, eventstore.EventInput{
		Type: tipo, RunID: aos408Plano, StepID: passo, Payload: raw,
		Producer: eventstore.Producer{NHIID: "nhi:orq"},
	}); err != nil {
		t.Fatalf("append %s: %v", tipo, err)
	}
}

func aos408Retrato(t *testing.T, store *eventstore.Store) *runlifecycle.PlanDecisionSnapshot {
	t.Helper()
	r, err := runlifecycle.NewPlanDecisionReader(store, aos408Plano)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func aos408Validado(t *testing.T, store *eventstore.Store, hash string) {
	t.Helper()
	aos408Apensar(t, store, plannerevents.EventValidated, "planstep:validated", plannerevents.ValidatedPayload{
		PlanID: aos408Plano, PlanHash: hash, NodeCount: 2, SnapshotDigest: "sha256:conteudo",
	})
}

func TestAOS408_AprovadoPorHumanoExigeAsTresCondicoes(t *testing.T) {
	casos := []struct {
		nome     string
		decisao  plannerevents.Decision
		hashDec  string
		ref      string
		consulta string
		quer     bool
	}{
		{"aprovado, mesmo hash, humano", plannerevents.DecisionApproved, "sha256:H1", "hitl:human:alice", "sha256:H1", true},
		{"auto-aprovação da máquina não é humana", plannerevents.DecisionApproved, "sha256:H1", "auto:autonomy:L4", "sha256:H1", false},
		{"aprovação de OUTRO hash", plannerevents.DecisionApproved, "sha256:H2", "hitl:human:alice", "sha256:H1", false},
		{"recusa humana", plannerevents.DecisionRejected, "sha256:H1", "hitl:human:alice", "sha256:H1", false},
		{"referência vazia", plannerevents.DecisionApproved, "sha256:H1", "", "sha256:H1", false},
		{"consulta sem hash", plannerevents.DecisionApproved, "sha256:H1", "hitl:human:alice", "", false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			store := newStore(t)
			aos408Validado(t, store, "sha256:H1")
			tipo := plannerevents.EventApproved
			if c.decisao == plannerevents.DecisionRejected {
				tipo = plannerevents.EventRejected
			}
			aos408Apensar(t, store, tipo, "planstep:decision:"+string(c.decisao), plannerevents.DecisionPayload{
				PlanID: aos408Plano, PlanHash: c.hashDec, Decision: c.decisao, DecisionRef: c.ref,
			})
			if got := aos408Retrato(t, store).AprovadoPorHumano(c.consulta); got != c.quer {
				t.Fatalf("AprovadoPorHumano(%q) = %v, quero %v", c.consulta, got, c.quer)
			}
		})
	}
}

// TestAOS408_AAncoraEADaDecisaoNaoADoValidated reproduz o furo A2 no predicado: o `plan.validated`
// tem um step id fixo, pelo que uma 2.ª decomposição no mesmo plan_id NÃO o actualiza, mas pode
// deixar uma decisão com outro hash. O predicado tem de olhar para o hash da DECISÃO.
func TestAOS408_AAncoraEADaDecisaoNaoADoValidated(t *testing.T) {
	store := newStore(t)
	aos408Validado(t, store, "sha256:PERIGOSO")
	aos408Apensar(t, store, plannerevents.EventApproved, "planstep:decision:approved", plannerevents.DecisionPayload{
		PlanID: aos408Plano, PlanHash: "sha256:INOCUO", Decision: plannerevents.DecisionApproved, DecisionRef: "hitl:human:alice",
	})
	s := aos408Retrato(t, store)
	if s.PlanHash() != "sha256:PERIGOSO" || s.DecidedHash() != "sha256:INOCUO" {
		t.Fatalf("âncoras: validated=%q decidida=%q", s.PlanHash(), s.DecidedHash())
	}
	if s.AprovadoPorHumano("sha256:PERIGOSO") {
		t.Fatal("a aprovação do plano inócuo não pode autorizar o perigoso")
	}
	if !s.AprovadoPorHumano("sha256:INOCUO") {
		t.Fatal("contraprova: o plano inócuo foi aprovado por humano")
	}
}

// TestAOS408_PrimeiraDecisaoTerminalVence: um `rejected` seguido de um `approved` não aprova.
func TestAOS408_PrimeiraDecisaoTerminalVence(t *testing.T) {
	store := newStore(t)
	aos408Validado(t, store, "sha256:H1")
	aos408Apensar(t, store, plannerevents.EventRejected, "planstep:decision:rejected", plannerevents.DecisionPayload{
		PlanID: aos408Plano, PlanHash: "sha256:H1", Decision: plannerevents.DecisionRejected, DecisionRef: "hitl:human:bob",
	})
	aos408Apensar(t, store, plannerevents.EventApproved, "planstep:decision:approved", plannerevents.DecisionPayload{
		PlanID: aos408Plano, PlanHash: "sha256:H1", Decision: plannerevents.DecisionApproved, DecisionRef: "hitl:human:alice",
	})
	if aos408Retrato(t, store).AprovadoPorHumano("sha256:H1") {
		t.Fatal("a primeira decisão terminal (recusa) tinha de vencer")
	}
}

// TestAOS408_OSeloDoSnapshotViajaNoValidated: o digest do conteúdo do snapshot selado no
// `plan.validated` é lido de volta — é a âncora que o `decide` e a materialização confrontam.
func TestAOS408_OSeloDoSnapshotViajaNoValidated(t *testing.T) {
	store := newStore(t)
	aos408Validado(t, store, "sha256:H1")
	if got := aos408Retrato(t, store).SnapshotDigest(); got != "sha256:conteudo" {
		t.Fatalf("SnapshotDigest = %q", got)
	}
}

// TestAOS408_ExpiracaoEDerivadaENaoEscrita: o pendente expira pela idade do `plan.validated` e o
// prazo — sem facto. Um prazo não-positivo não expira nada.
func TestAOS408_ExpiracaoEDerivadaENaoEscrita(t *testing.T) {
	store := newStore(t)
	aos408Validado(t, store, "sha256:H1")
	s := aos408Retrato(t, store)
	agora := s.ValidatedAt().Add(2 * time.Hour)
	if !s.Expirado(agora, time.Hour) || s.Pendente(agora, time.Hour) {
		t.Fatal("duas horas depois, com prazo de uma, o pendente expirou")
	}
	if s.Expirado(agora, 3*time.Hour) || !s.Pendente(agora, 3*time.Hour) {
		t.Fatal("dentro do prazo continua pendente")
	}
	if s.Decision() != "" {
		t.Fatal("expirar não escreve decisão nenhuma")
	}
}
