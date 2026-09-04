package autonomy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

// AOS-307 (achado de revisão) — o lado deste pacote da correcção: a PROVA viaja selada, e a
// rehidratação tem um ponto de veto FAIL-CLOSED.
//
// A verificação criptográfica em si NÃO vive aqui de propósito: exige o tuplo canónico do
// canal de controlo (packages/integration) e as pubkeys do nó, e importá-los inverteria a
// fronteira de camadas. O que este pacote garante é que a prova sobrevive ao selo intacta e
// que quem a sabe verificar consegue ABORTAR a rehidratação com ela.

// TestAOS307ProvaSobreviveAoSeloIntacta — round-trip [BuildLevelChangedRecord] →
// [parseLevelChangedObligation]. Se um só byte da assinatura se perdesse aqui, nenhuma
// prova voltaria a verificar no arranque e o nó recusaria os seus próprios registos.
func TestAOS307ProvaSobreviveAoSeloIntacta(t *testing.T) {
	provas := []LevelChangeProof{
		{EmitterID: "op:a", SignatureB64: "c2ln/YQ==", NonceB64: "bm9uY2U=", IssuedAtRFC3339: "2026-09-03T10:11:12.123456789Z"},
		{EmitterID: "op:b", SignatureB64: "c2lnMg==", NonceB64: "bm9uY2Uy", IssuedAtRFC3339: "2026-09-03T10:11:13Z"},
	}
	rec := BuildLevelChangedRecord(LevelChange{
		Agent: "agt-1", Domain: "fs", Old: L0, New: L5,
		Reason: "duas pessoas", Actor: "op:a,op:b", Proofs: provas,
	}, "")

	got, err := parseLevelChangedObligation(rec.Obligations[0], rec.Timestamp)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Proofs, provas) {
		t.Fatalf("provas apos o round-trip = %+v; quero %+v", got.Proofs, provas)
	}
}

// TestAOS307SemProvasOSeloNaoGanhaParams — compatibilidade do FORMATO: um evento sem provas
// (o provisionamento por configuração) tem de produzir exactamente os mesmos Params de
// antes deste campo existir. Um param a mais mudaria o conteúdo canónico, logo o EntryHash,
// logo as cadeias já escritas deixariam de verificar.
func TestAOS307SemProvasOSeloNaoGanhaParams(t *testing.T) {
	rec := BuildLevelChangedRecord(LevelChange{
		Agent: "agt-1", Domain: "fs", Old: L0, New: L1, Reason: "config", Actor: "config:node",
	}, "")
	p := rec.Obligations[0].Params
	if _, existe := p[LevelChangeProofsParam]; existe {
		t.Fatalf("um evento SEM provas nao pode ganhar o param %q: %+v", LevelChangeProofsParam, p)
	}
	if len(p) != 6 {
		t.Fatalf("params = %+v; quero as 6 chaves de sempre", p)
	}
}

// TestAOS307ProvaMalformadaEErroENaoAusencia — JSON corrompido no param é ERRO. Cair para
// «sem prova» deixaria um adversário DESACTIVAR a verificação estragando o campo, que é o
// oposto do que ela existe para fazer.
func TestAOS307ProvaMalformadaEErroENaoAusencia(t *testing.T) {
	ob := audit.Obligation{Type: LevelChangedEventType, Params: map[string]string{
		"agent": "a", "domain": "fs", "old_level": "L0", "new_level": "L5",
		"reason": "r", "actor": "op:a", LevelChangeProofsParam: "{isto nao e json",
	}}
	if _, err := parseLevelChangedObligation(ob, time.Time{}); err == nil {
		t.Fatal("prova malformada devia ser erro, nao «sem prova»")
	}
}

// TestAOS307ValidadorRecusadoSaltaSemAplicarORecusado — o contrato de
// [WithRehydrateValidator]: um veto SALTA aquele registo, declara-o com o AuditSeq e o
// motivo, e deixa os restantes serem aplicados. Não aborta: abortar por causa de um registo
// entregaria um modo de tijolo permanente a quem escreve no ficheiro do WORM, que é o mesmo
// adversário de que a verificação defende.
func TestAOS307ValidadorRecusadoSaltaSemAplicarORecusado(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	semear := NewLevelRegistry(WithSink(NewAuditSink(store, "")))
	if _, err := semear.SetLevel(ctx, "a", "http", L2, "primeira, legitima", "config:node"); err != nil {
		t.Fatal(err)
	}
	if _, err := semear.SetLevel(ctx, "b", "fs", L5, "segunda, a recusar", "op:mallory"); err != nil {
		t.Fatal(err)
	}

	recusado := errors.New("prova em falta (teste)")
	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "", WithRehydrateValidator(func(ch LevelChange) error {
		if ch.Actor == "op:mallory" {
			return recusado
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("um veto SALTA o registo, nao aborta a rehidratacao: %v", err)
	}
	if len(rep.Rejeitados) != 1 || rep.Rejeitados[0].AuditSeq != 2 {
		t.Fatalf("o veto tem de ser declarado com o AuditSeq (seq=2): %+v", rep.Rejeitados)
	}
	if !strings.Contains(rep.Rejeitados[0].Motivo, recusado.Error()) {
		t.Errorf("o motivo do salto tem de transportar a causa do validador: %q", rep.Rejeitados[0].Motivo)
	}
	if rep.Rejeitados[0].Actor != "op:mallory" || rep.Rejeitados[0].Pair != (Pair{Agent: "b", Domain: "fs"}) {
		t.Errorf("o salto tem de nomear o que o registo reclamava: %+v", rep.Rejeitados[0])
	}
	// O recusado NAO foi aplicado...
	if got := r.LevelFor("b", "fs"); got != L0 {
		t.Fatalf("LevelFor(b/fs) = %s; quero L0 — o registo vetado nao pode ser aplicado", got)
	}
	// ...e o ACEITE foi: saltar o mau nao pode descartar o bom (senao «recusar tudo» passava).
	if rep.Applied != 1 {
		t.Errorf("Applied = %d; quero 1 (o registo que o validador aceitou)", rep.Applied)
	}
	if got := r.LevelFor("a", "http"); got != L2 {
		t.Fatalf("LevelFor(a/http) = %s; quero L2 — o registo aceite tem de ser reposto", got)
	}

	// CONTROLO: sem validador (e com um que aceite tudo) reidrata as duas.
	r2 := NewLevelRegistry()
	if rep, err := r2.Rehydrate(ctx, store, "", WithRehydrateValidator(func(LevelChange) error { return nil })); err != nil || rep.Applied != 2 {
		t.Fatalf("validador permissivo: Applied=%d err=%v; quero 2, nil", rep.Applied, err)
	}
	r3 := NewLevelRegistry()
	if rep, err := r3.Rehydrate(ctx, store, ""); err != nil || rep.Applied != 2 {
		t.Fatalf("sem validador o comportamento tem de ser o de antes: Applied=%d err=%v", rep.Applied, err)
	}
}
