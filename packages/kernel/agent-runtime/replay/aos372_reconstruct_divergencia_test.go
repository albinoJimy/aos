package replay

// AOS-372 — RECONSTRUCT RECUSA A DIVERGÊNCIA turn.recorded SEM replay.captured.
//
// O defeito: `Reconstruct` constrói `order` a partir de `caps` (o conjunto dos replay.captured).
// Um turno com `turn.recorded` mas com o `replay.captured` INTEIRAMENTE ausente nunca entrava em
// `order` — desaparecia em silêncio. `Reconstruct` devolvia MENOS turnos com err=nil, e o
// read-path soberano (GET /runs/{id}/reconstruct) respondia 200 com uma trajectória curta: um
// auditor via menos turnos, sem aviso.
//
// `admit()` (só em [ReplayEngine.Replay]) já recusava exactamente este caso com
// [ErrIncompleteCapture]. O fix dá a `Reconstruct` a MESMA recusa fail-closed, mantendo os dois
// caminhos simétricos. Ver a divergência-check em sovereign_content.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// TestAOS372_ReconstructRecusaTurnoSemCaptura é a AC2.
//
// Uma trajectória de 3 turnos (runOriginal emite AMBOS turn.recorded e replay.captured por turno);
// o captureDroppingReader suprime o replay.captured do turno 2 MAS mantém o turn.recorded. Antes do
// fix, `Reconstruct` devolvia nil + 2 turnos [1 3] em silêncio. Agora recusa fail-closed.
func TestAOS372_ReconstructRecusaTurnoSemCaptura(t *testing.T) {
	or := runOriginal(t, "run_aos372_gap")
	reader := &captureDroppingReader{inner: or.store, dropTurn: 2}
	e, err := NewEngine(reader)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	turnos, err := e.Reconstruct(context.Background(), or.goal.RunID)
	if !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("um turno com turn.recorded mas SEM replay.captured devia ser INADMISSÍVEL (ErrIncompleteCapture) — em vez de desaparecer em silêncio; obtive err=%v turnos=%d", err, len(turnos))
	}
	if turnos != nil {
		t.Fatalf("uma reconstrução recusada não pode devolver turnos: %d", len(turnos))
	}
}

// TestAOS372_ReconstructIntactoContinuaAdmissivel é o controlo de NÃO-VACUIDADE do AC2: sem
// supressão, a mesma trajectória de 3 turnos reconstrói TODOS os 3 sem erro. Sem isto, uma
// divergência-check avariada que recusasse tudo passaria no teste acima.
func TestAOS372_ReconstructIntactoContinuaAdmissivel(t *testing.T) {
	or := runOriginal(t, "run_aos372_intacto")
	e := mustEngine(t, or)
	turnos, err := e.Reconstruct(context.Background(), or.goal.RunID)
	if err != nil {
		t.Fatalf("uma trajectória intacta tem de reconstruir sem erro: %v", err)
	}
	if len(turnos) != 3 {
		t.Fatalf("esperava 3 turnos reconstruídos, obtive %d", len(turnos))
	}
}

// TestAOS372_ReconstructSemTurnRecordedNaoRecusa é a AC3 — a divergência-check NÃO é
// indiscriminada.
//
// Uma captura antiga, anterior ao evento turn.recorded, produz replay.captured SEM qualquer
// turn.recorded (Capturer.Capture directo ⇒ só o EventTypeCaptured). Aí `stepByTurn` é povoado
// pelo fallback que deriva o step do envelope de cada captura, pelo que fica IGUAL a `caps`
// (caps ⊆ stepByTurn): os dois conjuntos concordam e a check é um no-op. A recusa é sobre a
// DIVERGÊNCIA entre os dois conjuntos, nunca sobre a ausência do sinal turn.recorded. Sem esta
// prova, a check poderia estar a recusar toda a captura legada.
func TestAOS372_ReconstructSemTurnRecordedNaoRecusa(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const runID = "run_aos372_sem_recorded"
	cap, err := NewCapturer(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	tc := sampleTurnCapture()
	tc.RunID = runID
	if err := cap.Capture(context.Background(), tc); err != nil {
		t.Fatalf("Capture: %v", err)
	}

	e, err := NewEngine(store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	turnos, err := e.Reconstruct(context.Background(), runID)
	if err != nil {
		t.Fatalf("uma captura SEM turn.recorded (caps ⊆ stepByTurn) não pode ser recusada: %v", err)
	}
	if len(turnos) != 1 {
		t.Fatalf("esperava 1 turno reconstruído, obtive %d", len(turnos))
	}
}

// TestAOS372_TrailingResumableTolera fixa a fronteira crítica que a revisão adversarial apanhou:
// um turn.recorded TRAILING (o último turno) sem replay.captured NÃO é o defeito — é um crash a
// meio do último turno (o turn.recorded é gravado ANTES da captura, com a dispatch de tools no
// meio). O crash-resume (modo RESUMABLE) existe para recuperar exactamente isto: reconstrói o
// prefixo capturado e corre o turno interrompido ao vivo. Recusá-lo deixaria o run órfão em
// `running` para sempre — uma regressão pior do que a truncatura silenciosa que AOS-372 fecha.
// Aqui: 3 turnos, suprime-se a captura do ÚLTIMO (turno 3); ReconstructResumable TOLERA e devolve o
// prefixo [1 2]. Contrasta com TestAOS372_ReconstructRecusaTurnoSemCaptura (buraco no turno 2 do
// MEIO ⇒ recusa nos DOIS modos), provando que a fronteira é o maior turno capturado.
func TestAOS372_TrailingResumableTolera(t *testing.T) {
	or := runOriginal(t, "run_aos372_trailing")
	reader := &captureDroppingReader{inner: or.store, dropTurn: 3} // o ÚLTIMO turno
	e, err := NewEngine(reader)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	turnos, err := e.ReconstructResumable(context.Background(), or.goal.RunID)
	if err != nil {
		t.Fatalf("um turn.recorded TRAILING sem captura (crash a meio do último turno) tem de ser TOLERADO pela retoma — senão o crash-resume deixa o run órfão; obtive err=%v", err)
	}
	if len(turnos) != 2 {
		t.Fatalf("esperava o prefixo capturado [1 2] (2 turnos), obtive %d", len(turnos))
	}
}

// TestAOS372_TrailingStrictRecusa: o modo STRICT (Reconstruct, usado pelo read-path soberano)
// recusa TAMBÉM o trailing — uma trajectória incompleta não se serve a um auditor, ainda que a
// incompletude seja só o último turno. É o par de TestAOS372_TrailingResumableTolera: a MESMA
// supressão (turno 3), erro no strict, tolerada no resumable.
func TestAOS372_TrailingStrictRecusa(t *testing.T) {
	or := runOriginal(t, "run_aos372_trailing_strict")
	reader := &captureDroppingReader{inner: or.store, dropTurn: 3}
	e, err := NewEngine(reader)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.Reconstruct(context.Background(), or.goal.RunID); !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("o modo strict (read-path) tem de recusar o trailing incompleto; obtive err=%v", err)
	}
}

// TestAOS372_ResumableRecusaMidTrajectory: mesmo o modo tolerante recusa um buraco MID-trajectory
// (corrupção genuína, com captura DEPOIS do buraco) — a tolerância é SÓ para o último turno de um
// crash, nunca para um turno do meio que desapareceu.
func TestAOS372_ResumableRecusaMidTrajectory(t *testing.T) {
	or := runOriginal(t, "run_aos372_mid_resumable")
	reader := &captureDroppingReader{inner: or.store, dropTurn: 2} // buraco do MEIO (turno 3 capturado)
	e, err := NewEngine(reader)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := e.ReconstructResumable(context.Background(), or.goal.RunID); !errors.Is(err, ErrIncompleteCapture) {
		t.Fatalf("mesmo o modo resumable tem de recusar um buraco MID-trajectory (corrupção); obtive err=%v", err)
	}
}
