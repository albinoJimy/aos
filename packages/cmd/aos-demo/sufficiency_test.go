package main

// AOS-151 — o critério de aceitação AC4 do ápice mínimo ("o ápice mínimo chega?")
// como TESTE REFUTÁVEL, não juízo. Cada invariante do ápice é ou PROVADA (uma
// assercão real corre contra a composição) ou DIFERIDA (t.Log + a razão, NOMEANDO o
// seam de produção em falta). O teste FALHA se alguma invariante não for NEM provada
// NEM diferida-com-seam — proíbe o *vacuous pass* (uma resposta "chega" por omissão).
// O balanço provado-vs-diferido impresso é a resposta operacional ao AC4.
//
// Ligado ao self-test do CI (scripts/ci/selftest.sh, secção J) por um teste-veneno
// que prova que uma invariante vacuosa avermelharia o gate.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	surfaceadapter "github.com/aos-ref/control-plane/governance/surface-adapter"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/eventstore"
)

// demoPausingSteer é um [agentruntime.SteerSource] que pausa na 1ª fronteira de
// fim-de-turno — para a invariante AC4 provar que o loop consome o steer.
type demoPausingSteer struct{}

func (demoPausingSteer) GracefulPause(context.Context, string) (bool, error)      { return true, nil }
func (demoPausingSteer) PendingCorrection(context.Context, string) ([]byte, bool) { return nil, false }

// invariantKind classifica uma invariante do ápice. kindUnset é o valor-zero: uma
// invariante não classificada é um DEFEITO (vacuous pass), nunca um "chega".
type invariantKind int

const (
	kindUnset invariantKind = iota
	kindProven
	kindDeferred
)

// apexInvariant é uma linha da tabela do AC4.
type apexInvariant struct {
	name  string
	kind  invariantKind
	seam  string             // obrigatório sse kindDeferred: NOMEIA o seam de produção
	prove func(t *testing.T) // corre sse kindProven
}

// classify é o "gate" refutável: devolve erro se uma invariante for vacuosa (kindUnset),
// PROVADA sem assercão, ou DIFERIDA sem seam nomeado. É a MESMA função que o teste real
// e o teste-veneno exercitam — a prova de não-vacuidade.
func classify(inv apexInvariant) error {
	switch inv.kind {
	case kindProven:
		if inv.prove == nil {
			return fmt.Errorf("invariante %q PROVADA sem assercão", inv.name)
		}
		return nil
	case kindDeferred:
		if inv.seam == "" {
			return fmt.Errorf("invariante %q DIFERIDA sem seam de produção nomeado", inv.name)
		}
		return nil
	default:
		return fmt.Errorf("invariante %q NÃO classificada — vacuous pass proibido (AC4)", inv.name)
	}
}

// apexInvariants é a tabela do AC4 do ápice mínimo (cmd/aos-demo). PROVADAS: as
// garantias que o ápice mínimo genuinamente entrega (composição zero-rede, reflexão
// verídica a frio, default-deny do canal de controlo, render da superfície). DIFERIDAS:
// as garantias de ENFORCEMENT DE PRODUÇÃO que o ápice mínimo NÃO entrega, cada uma a
// nomear o seam (ticket) que a fecha.
func apexInvariants() []apexInvariant {
	return []apexInvariant{
		{
			name: "os pilares compõem e um run corre in-process, zero-rede",
			kind: kindProven,
			prove: func(t *testing.T) {
				if err := runDemo(io.Discard); err != nil {
					t.Fatalf("runDemo (composição+run in-process): %v", err)
				}
			},
		},
		{
			name: "reflexão de estado verídica A FRIO (backfill do backlog, AOS-150)",
			kind: kindProven,
			prove: func(t *testing.T) {
				ctx := context.Background()
				st, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				defer st.Close()
				m, err := state.NewMachine(st, "run-ac4")
				if err != nil {
					t.Fatalf("NewMachine: %v", err)
				}
				if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
					t.Fatalf("ready→running: %v", err)
				}
				// Projector construído DEPOIS da transição: só vê "running" se fizer backfill.
				p, err := controlsurface.NewStateProjector(ctx, st, "run-ac4")
				if err != nil {
					t.Fatalf("NewStateProjector: %v", err)
				}
				defer p.Close()
				if p.Current() != state.Running {
					t.Fatalf("reflexão a frio: Current()=%q, quero running", p.Current())
				}
			},
		},
		{
			name: "canal de controlo é DEFAULT-DENY (assinatura forjada recusada, ADR-013)",
			kind: kindProven,
			prove: func(t *testing.T) {
				ctx := context.Background()
				st, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				defer st.Close()
				auth := control.NewHMACAuthenticator()
				auth.Register("operator:demo", []byte("chave-registada"))
				ch, err := control.NewChannel(st, auth)
				if err != nil {
					t.Fatalf("NewChannel: %v", err)
				}
				// Emissor com assinatura FORJADA (não produzida pela chave registada).
				forged := control.Emitter{ID: "operator:demo", Signature: []byte("forjada")}
				err = ch.Steer(ctx, "run-ac4", []byte("corrige"), forged)
				if !errors.Is(err, control.ErrUnauthenticated) {
					t.Fatalf("steer forjado: err=%v, quero ErrUnauthenticated (escalada não impedida)", err)
				}
			},
		},
		{
			name: "a superfície renderiza o approval-card (paridade desktop)",
			kind: kindProven,
			prove: func(t *testing.T) {
				card, err := approvalcard.BuildCard(risk.ConfirmationRequest{
					Class: risk.ClassDanger, Irreversible: true,
					Preview: "cap:demo.publish -> desktop", Principal: "agent-demo",
					Capability: "cap:demo.publish", Resource: "surface:desktop",
				}, approvalcard.WithRequestID("card-ac4"))
				if err != nil {
					t.Fatalf("BuildCard: %v", err)
				}
				r, err := surfaceadapter.RendererFor(surfaceadapter.PlatformDesktop)
				if err != nil {
					t.Fatalf("RendererFor(desktop): %v", err)
				}
				rendered, err := r.Render(card)
				if err != nil {
					t.Fatalf("Render: %v", err)
				}
				if rendered.DesktopComponent == nil {
					t.Fatalf("render desktop sem DesktopComponent")
				}
			},
		},

		// ---- DIFERIDAS: enforcement de produção que o ápice mínimo NÃO entrega ------
		{
			name: "o loop consome o SteerChannel — pausa graciosa ATRAVÉS do loop (AOS-158)",
			kind: kindProven,
			prove: func(t *testing.T) {
				ctx := context.Background()
				store, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				defer store.Close()
				rm := referencemonitor.New(referencemonitor.WithHooks(
					referencemonitor.IdentityStub{}, referencemonitor.PolicyStub{},
					referencemonitor.BudgetStub{}, referencemonitor.EgressStub{}, referencemonitor.AuditStub{},
				))
				if err := rm.Register("noop", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
					t.Fatalf("Register: %v", err)
				}
				// Modelo multi-turno (nunca Final): só o steer pára o loop.
				model := agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
					return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{ToolID: "noop", Capability: "cap:noop", Input: []byte("x")}}}, nil
				})
				rt := agentruntime.New(model, rm, agentruntime.NewTurnRecorder(store), agentruntime.WithSteerSource(demoPausingSteer{}))
				res, err := rt.Run(ctx, agentruntime.Goal{
					RunID:     "run-ac4-steer",
					Principal: referencemonitor.Principal{NHIID: "nhi:x", AgentID: "a", AgentClass: "c"},
					System:    "s", Objective: "o",
				})
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if !res.Paused {
					t.Fatal("o loop não pausou através do steer (invariante de steer não provada)")
				}
			},
		},
		{
			name: "o RM COMPÕE o hook de identidade real (IdentityCheck, não IdentityStub)",
			kind: kindDeferred,
			// A wiring do kernel (Goal.Credential → Call.Credential) ficou feita em
			// AOS-152; o que falta é o apex compor o IdentityCheck real (não o stub).
			seam: "AOS-153/154 (NewProductionSecure compõe o IdentityCheck; kernel Call.Credential já feito em AOS-152)",
		},
		{
			name: "a via sancionada recusa IdentityStub/EgressStub (fail-closed)",
			kind: kindDeferred,
			seam: "AOS-153 (NewProductionSecure; hoje referencemonitor.New com stubs neutros)",
		},
		{
			name: "cadeia real de hooks (identity→reval→PDP→taint→scope→budget→egress→audit) com um único WORM",
			kind: kindDeferred,
			seam: "AOS-154 (composição da cadeia real + audit.Store WORM partilhado)",
		},
		{
			name: "identidade NÃO-FORJÁVEL (espinha de token real via vault+authn)",
			kind: kindDeferred,
			seam: "AOS-156 / decisão D4 (issuer via vault EPIC-07 + AOS-057; hoje demo-only)",
		},
	}
}

// TestApexMinimalSufficiency é o AC4 refutável. Cada invariante é classificada
// (fail-closed contra vacuous) e, se PROVADA, a sua assercão corre. Imprime o balanço
// provado-vs-diferido — a resposta operacional a "o ápice mínimo chega?".
func TestApexMinimalSufficiency(t *testing.T) {
	invs := apexInvariants()
	var proven, deferred int
	for _, inv := range invs {
		if err := classify(inv); err != nil {
			t.Fatalf("classificação AC4: %v", err) // vacuous pass proibido
		}
		switch inv.kind {
		case kindProven:
			t.Run("PROVADA/"+inv.name, func(t *testing.T) { inv.prove(t) })
			proven++
		case kindDeferred:
			t.Logf("DIFERIDA: %s — seam: %s", inv.name, inv.seam)
			deferred++
		}
	}
	// Balanço operacional (o output do AC4). Exige pelo menos uma de cada — um ápice
	// que "provasse tudo" ou "diferisse tudo" seria suspeito.
	if proven == 0 || deferred == 0 {
		t.Fatalf("balanço AC4 degenerado: provadas=%d diferidas=%d", proven, deferred)
	}
	t.Logf("AC4 do ápice mínimo: %d invariantes PROVADAS, %d DIFERIDAS (enforcement de produção, PR-0.c). O ápice mínimo CHEGA para de-riscar composição/reflexão/controlo; o enforcement fica gated nos seams nomeados.", proven, deferred)
}

// TestSelftestApexSufficiencyReddensGate é o teste-veneno (scripts/ci/selftest.sh,
// secção J). Só corre com AOS_APEX_SELFTEST=1. Injecta uma invariante VACUOSA (kindUnset
// — o que o AC4 proíbe) e assevera (falsamente) que a classificação a ACEITOU; como o
// gate a DETECTA (classify devolve erro), a assercão FALHA de propósito — provando que
// uma invariante não-classificada avermelha o gate (fail-closed, não-vacuoso). Se
// alguma vez PASSAR (exit 0), o self-test marca-o como falha do gate.
func TestSelftestApexSufficiencyReddensGate(t *testing.T) {
	if os.Getenv("AOS_APEX_SELFTEST") != "1" {
		t.Skip("teste-veneno do self-test (correr com AOS_APEX_SELFTEST=1 via scripts/ci/selftest.sh)")
	}
	poison := apexInvariant{name: "invariante-fantasma (não classificada)", kind: kindUnset}
	if err := classify(poison); err != nil {
		// Correcto: o gate detectou a invariante vacuosa. O teste-veneno FALHA aqui de
		// propósito, provando que o AC4 avermelharia.
		t.Fatalf("gate detectou a invariante vacuosa (AC4 fail-closed, esperado): %v", err)
	}
	// Só chega aqui se classify NÃO detectasse — o que seria um AC4 vacuoso (mau).
}
