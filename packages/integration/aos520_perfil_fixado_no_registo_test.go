package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-520 (PR 0) — o digest do perfil da rota em que o run está fixado, no REGISTO DE RETOMA.

// SEM PERFIL FIXADO o registo serializa os bytes de sempre (o campo não existe), e o Goal de
// retoma traz a MARCA de «não fixado» — nunca vazio, que seria lido como um run novo e fixado a
// meio. COM perfil fixado, o digest vai para o registo, sobrevive ao Put/Get e volta no Goal.
func TestAOS520_RegistoDeRetoma_ODigestFixadoSobreviveEAAusenciaEAMarca(t *testing.T) {
	sem := sampleResume("run-520-sem")
	cru, err := json.Marshal(sem)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cru), "RouteProfileDigest") {
		t.Fatalf("o registo de um run sem perfil fixado ganhou o campo: %s", cru)
	}
	if got := sem.GoalWith("cred").RouteProfileDigest; got != agentruntime.RouteProfileDigestUnpinned {
		t.Fatalf("um run que comecou sem perfil fixado volta com a marca; veio %q", got)
	}
	// Um registo escrito por um binário ANTERIOR ao campo decodifica sem ele, e dá a marca.
	var antigo ResumeRecord
	if err := json.Unmarshal([]byte(`{"RunID":"run-antigo","Objective":"o","MaxTurns":3}`), &antigo); err != nil {
		t.Fatal(err)
	}
	if antigo.RouteProfileDigest != "" || antigo.PerfilDoRun() != agentruntime.RouteProfileDigestUnpinned {
		t.Fatalf("registo antigo: campo %q, perfil do run %q", antigo.RouteProfileDigest, antigo.PerfilDoRun())
	}

	const digest = "sha256:0cbc100e54038f013a6c84867ff08510095f47dd17b2dd07f68b5dc74f090d0f"
	fixado := sampleResume("run-520-fixado")
	fixado.RouteProfileDigest = digest
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewResumeRecords(es, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, rec := range []ResumeRecord{sem, fixado} {
		if err := r.Put(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := r.Get(ctx, "run-520-fixado")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.RouteProfileDigest != digest || got.PerfilDoRun() != digest || got.GoalWith("cred").RouteProfileDigest != digest {
		t.Fatalf("o digest fixado nao sobreviveu: registo %q, Goal %q", got.RouteProfileDigest, got.GoalWith("cred").RouteProfileDigest)
	}
	got, ok, err = r.Get(ctx, "run-520-sem")
	if err != nil || !ok || got.RouteProfileDigest != "" || got.GoalWith("cred").RouteProfileDigest != agentruntime.RouteProfileDigestUnpinned {
		t.Fatalf("o registo sem perfil fixado: ok=%v err=%v campo=%q", ok, err, got.RouteProfileDigest)
	}
}

// O DIGEST FAZ PARTE DO QUE SE COMPARA: dois registos do mesmo run com digests diferentes são
// DIVERGENTES — quem tivesse escrita crua no stream não troca o perfil fixado de um run pondo
// outro registo ao lado.
func TestAOS520_RegistoDeRetoma_OutroDigestEDivergente(t *testing.T) {
	a, b := sampleResume("run-520-d"), sampleResume("run-520-d")
	a.RouteProfileDigest = "sha256:" + strings.Repeat("a", 64)
	b.RouteProfileDigest = "sha256:" + strings.Repeat("b", 64)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) == string(jb) {
		t.Fatal("dois registos com digests diferentes serializam igual: o digest nao entra na comparacao")
	}
	semDigest := sampleResume("run-520-d")
	if js, _ := json.Marshal(semDigest); string(js) == string(ja) {
		t.Fatal("o registo com digest serializa como o sem digest")
	}
}
