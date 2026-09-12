package autonomy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

// aos090_rehydrate_controlador_test.go — AOS-090: os invariantes de DIRECÇÃO que tornam a
// demoção automática (actor [ControllerActor]) durável SEM assinatura, e que FECHAM os dois
// críticos que a revisão adversarial mediu no WIP:
//
//   Crítico 1 — a demoção legítima ELEVA: um registo de INSTÂNCIA a L2 sombreia a entrada de
//               classe a L1 que o PDP lê, subindo o efectivo de L1 para L2.
//   Crítico 2 — o invariante ABRE elevação por FORJA: um registo de instância forjado a L3
//               que o replay não recusa faz saltar o efectivo de L1 para L3, sem assinatura.
//
// A raiz de ambos é medir a direcção contra o piso da INSTÂNCIA e não contra a classe que o
// PDP lê. A correcção: o controlador só governa CLASSES, e só desce face ao nível
// RECONSTRUÍDO. Estes testes provam-no — em particular, o registo de instância que o WIP
// aceitava como «controlo» é aqui RECUSADO.

// selar é um atalho: constrói e apende um registo autonomy.level_changed cru no store.
func selar(t *testing.T, store audit.Store, ch LevelChange) {
	t.Helper()
	if _, err := store.Append(context.Background(), BuildLevelChangedRecord(ch, "")); err != nil {
		t.Fatalf("selar %s:%s->%s: %v", ch.Agent, ch.Domain, ch.New, err)
	}
}

// TestAOS090_Rehydrate_DemocaoDeClasseLegitimaEDuravel — a demoção do controlador sobre uma
// CLASSE, que desce face ao reconstruído, é reidratada sem assinatura e baixa mesmo o efectivo
// que o PDP lê.
func TestAOS090_Rehydrate_DemocaoDeClasseLegitimaEDuravel(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	// Um operador promoveu a classe worker:fs a L4; depois o controlador demoveu-a a L2.
	selar(t, store, LevelChange{Agent: ClassPrefix + "worker", Domain: "fs", Old: L0, New: L4, Reason: "promocao assinada", Actor: "gov-admin", At: at})
	selar(t, store, LevelChange{Agent: ClassPrefix + "worker", Domain: "fs", Old: L4, New: L2, Reason: "anomalia unsafe_action", Actor: ControllerActor, At: at})

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if rep.Applied != 2 || len(rep.Rejeitados) != 0 {
		t.Fatalf("ambos os registos deviam aplicar: %+v", rep)
	}
	// O efectivo que o PDP lê para uma instância da classe é a classe demovida (L2), não L4.
	if got := r.LevelForAgentOrClass("worker-run-7", "worker", "fs"); got != L2 {
		t.Fatalf("efectivo via classe = %s; quer L2 (a demoção do controlador pegou)", got)
	}
}

// TestAOS090_Rehydrate_RecusaRegistoDeInstancia — CRÍTICO 1/2 FECHADO: um registo do
// controlador sobre uma chave de INSTÂNCIA (sem prefixo de classe) é RECUSADO. É exactamente
// o registo que o WIP aceitava como «controlo» e que sombreava a classe para CIMA.
func TestAOS090_Rehydrate_RecusaRegistoDeInstancia(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	// A classe está governada a L1 (mais supervisionada). O PDP lê L1 para as instâncias.
	selar(t, store, LevelChange{Agent: ClassPrefix + "worker", Domain: "fs", Old: L0, New: L1, Reason: "classe sensivel", Actor: "config:node", At: at})
	// FORJA: um registo do controlador sobre a INSTÂNCIA a L3 — que, aceite, sombrearia a
	// classe e elevaria o efectivo de L1 para L3 sem assinatura.
	selar(t, store, LevelChange{Agent: "worker-run-7", Domain: "fs", Old: L4, New: L3, Reason: "forja", Actor: ControllerActor, At: at})

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if rep.Applied != 1 || len(rep.Rejeitados) != 1 {
		t.Fatalf("só a classe devia aplicar; a instância forjada é recusada: %+v", rep)
	}
	if rej := rep.Rejeitados[0]; rej.Actor != ControllerActor {
		t.Fatalf("a recusa devia ser do registo do controlador: %+v", rej)
	}
	// O efectivo mantém-se em L1: a forja NÃO elevou.
	if got := r.LevelForAgentOrClass("worker-run-7", "worker", "fs"); got != L1 {
		t.Fatalf("efectivo = %s; a forja de instância elevou (devia ficar L1)", got)
	}
}

// TestAOS090_Rehydrate_RecusaRegistoDeClasseQueSobe — CRÍTICO 2 FECHADO na chave de classe:
// um registo do controlador que NÃO desce face ao reconstruído (ou que se disfarça de
// demoção mentindo no Old) é recusado. A comparação é contra o replay, não contra o Old.
func TestAOS090_Rehydrate_RecusaRegistoDeClasseQueSobe(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	selar(t, store, LevelChange{Agent: ClassPrefix + "worker", Domain: "fs", Old: L0, New: L1, Reason: "classe sensivel", Actor: "config:node", At: at})
	// FORJA A: sobe abertamente (New=L3 >= reconstruido=L1).
	selar(t, store, LevelChange{Agent: ClassPrefix + "worker", Domain: "fs", Old: L0, New: L3, Reason: "forja sobe", Actor: ControllerActor, At: at})
	// FORJA B: disfarça-se de demoção (Old=L5,New=L3) mas o replay tem a classe em L1 — L3>=L1.
	selar(t, store, LevelChange{Agent: ClassPrefix + "worker", Domain: "fs", Old: L5, New: L3, Reason: "forja disfarcada", Actor: ControllerActor, At: at})

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if rep.Applied != 1 || len(rep.Rejeitados) != 2 {
		t.Fatalf("só a classe legítima aplica; as duas forjas são recusadas: %+v", rep)
	}
	for _, rej := range rep.Rejeitados {
		if !contains(rej.Motivo, "E_CONTROLLER_NOT_DESCENDING") {
			t.Fatalf("motivo da recusa devia ser não-desce: %q", rej.Motivo)
		}
	}
	if got := r.LevelFor(ClassPrefix+"worker", "fs"); got != L1 {
		t.Fatalf("a classe = %s; as forjas não podiam mexer (devia ficar L1)", got)
	}
}

// TestAOS090_ControllerRehydrateOK_Unitario cobre o predicado directamente, incluindo os
// erros nomeados que o banner e os testes de composição usam.
func TestAOS090_ControllerRehydrateOK_Unitario(t *testing.T) {
	classe := ClassPrefix + "worker"
	// Desce sobre classe: OK.
	if err := controllerRehydrateOK(LevelChange{Agent: classe, Domain: "fs", New: L2}, L4); err != nil {
		t.Fatalf("demoção de classe que desce devia passar: %v", err)
	}
	// Instância: recusa com ErrControllerExigeClasse.
	if err := controllerRehydrateOK(LevelChange{Agent: "inst-1", Domain: "fs", New: L2}, L4); !errors.Is(err, ErrControllerExigeClasse) {
		t.Fatalf("instância devia dar ErrControllerExigeClasse; veio %v", err)
	}
	// Classe que não desce (igual): recusa com ErrControllerDeveDescer.
	if err := controllerRehydrateOK(LevelChange{Agent: classe, Domain: "fs", New: L4}, L4); !errors.Is(err, ErrControllerDeveDescer) {
		t.Fatalf("nível igual devia dar ErrControllerDeveDescer; veio %v", err)
	}
	// Classe sem nível estabelecido (reconstruído=piso L0): nada a demover ⇒ recusa.
	if err := controllerRehydrateOK(LevelChange{Agent: classe, Domain: "fs", New: L0}, L0); !errors.Is(err, ErrControllerDeveDescer) {
		t.Fatalf("sobre piso não há para onde descer; veio %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
