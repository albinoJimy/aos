package integration

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// aos363BaseConfig devolve uma SecuredConfig VÁLIDA mínima (os colaboradores da cadeia
// real caem para defaults demo-grade fail-closed; ver TestNewSecuredRuntime_FailClosed).
// Só o campo Privileged varia entre os casos deste ficheiro.
func aos363BaseConfig(t *testing.T) (SecuredConfig, func()) {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	// AOS-381: WORM único — a revalidação sela no MESMO store que cfg.WORM.
	worm := audit.NewMemStore()
	cfg := SecuredConfig{
		Model:       &scriptedModel{},
		Recorder:    agentruntime.NewTurnRecorder(store),
		Catalog:     &fakeCatalog{},
		Revalidator: newRevalidator(t, newTrust(t, context.Background(), worm, testSigner(t)), worm, NoopQuarantinerForTest{}, NoopAlerterForTest{}),
		Policy:      StaticPolicy{},
		WORM:        worm,
	}
	return cfg, func() { store.Close() }
}

// TestAOS363_TaintGateOptIn é a prova dos dois sentidos do AOS-363 na composição do
// ápice (SecuredConfig → NewSecuredRuntime):
//
//   - conjunto Privileged NÃO-VAZIO ⇒ o nó arranca (via NewProductionHardenedTaint) e o
//     RM composto reporta HasActiveTaintGate() == true — a barreira control/data-plane
//     está EFICAZ, não só presente;
//   - conjunto ausente (nil ⇒ default vazio) ⇒ o nó arranca EXACTAMENTE como antes desta
//     mudança (via NewProductionSecure) e HasActiveTaintGate() == false — a perna
//     RETRO-COMPATÍVEL: nenhum deployment que não defina AOS_PRIVILEGED_CAPS regride.
//
// CADEIA DE EVIDÊNCIA (honesta sobre o que cada camada prova):
//   - que um TaintGate EFICAZ barra uma tool call privilegiada+untrusted é provado ao nível do
//     kernel — `TestTaintGateBlocksUntrustedPrivileged` e `TestTaintGateUnitEvaluate`
//     (packages/kernel/reference-monitor/taint_gate_test.go), com deny atribuído a "taint";
//   - que o TaintGate está SEMPRE na cadeia composta, alimentado por ESTE mesmo `privileged`,
//     é `secured.go` (`NewTaintGate(privileged)`, invariante pré-AOS-363);
//   - o que ESTE teste acrescenta é a ligação nova: o conjunto vindo da config torna o gate
//     composto EFICAZ (não-vazio) ou INERTE (vazio), observável por `HasActiveTaintGate()`.
//
// A composição das três prova a propriedade; um teste de run completo com injecção de taint
// untrusted fica por escrever (declarado em §Limites do relatório) — entrelaçaria a ordem dos
// hooks (autonomia escala antes do taint), que o AOS-363 não altera, e duplicaria a prova do kernel.
//
// NOTA sobre a selecção de construtor: para um StaticPrivilegedSet ela NÃO tem efeito
// observável — Hardened e Secure produzem runtimes idênticos, porque a eficácia usa o mesmo
// sinal HasPrivileged nos dois lados, e o ápice roteia o inerte para Secure (D1). O Hardened é
// defesa-em-profundidade para um autorizador custom; a recusa ErrTaintGateInert é provada no
// kernel (production_efficacy_test.go), não aqui.
func TestAOS363_TaintGateOptIn(t *testing.T) {
	t.Run("conjunto nao-vazio ⇒ arranca com TaintGate EFICAZ", func(t *testing.T) {
		cfg, cleanup := aos363BaseConfig(t)
		defer cleanup()
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet("cap:fs.write", "cap:net.connect")

		rt, err := NewSecuredRuntime(cfg)
		if err != nil {
			t.Fatalf("conjunto não-vazio devia arrancar (via endurecida), falhou: %v", err)
		}
		if !rt.rm.HasActiveTaintGate() {
			t.Fatal("HasActiveTaintGate()==false com conjunto não-vazio — o TaintGate ficou inerte quando devia estar eficaz")
		}
	})

	t.Run("conjunto ausente ⇒ arranca INERTE (retro-compat)", func(t *testing.T) {
		cfg, cleanup := aos363BaseConfig(t)
		defer cleanup()
		// Privileged deixado nil de propósito — é o estado de TODO deployment que não
		// define AOS_PRIVILEGED_CAPS, incluindo o cluster em produção.

		rt, err := NewSecuredRuntime(cfg)
		if err != nil {
			t.Fatalf("conjunto ausente TEM de arrancar (retro-compat) — não pode regredir: %v", err)
		}
		if rt.rm.HasActiveTaintGate() {
			t.Fatal("HasActiveTaintGate()==true sem conjunto — a perna retro-compatível ficou activa por engano")
		}
	})

	// Controlo do predicado: um conjunto EXPLICITAMENTE vazio comporta-se como o nil
	// (inerte, arranca). O ápice não força ErrTaintGateInert aqui — encaminha o inerte
	// para a via estrita por desenho (D1). O operador que QUER endurecer e engana o
	// valor é apanhado a montante, no parsing de AOS_PRIVILEGED_CAPS (cmd/aos), não aqui.
	t.Run("conjunto explicitamente vazio ⇒ inerte, arranca (nao ErrTaintGateInert no apice)", func(t *testing.T) {
		cfg, cleanup := aos363BaseConfig(t)
		defer cleanup()
		cfg.Privileged = referencemonitor.NewStaticPrivilegedSet() // vazio

		rt, err := NewSecuredRuntime(cfg)
		if err != nil {
			t.Fatalf("conjunto vazio no ápice devia arrancar inerte (não endurecer), falhou: %v", err)
		}
		if rt.rm.HasActiveTaintGate() {
			t.Fatal("conjunto vazio produziu gate activo — impossível")
		}
	})
}
