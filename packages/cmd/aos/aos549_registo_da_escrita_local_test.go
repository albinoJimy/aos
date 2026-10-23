package main

// AOS-549 — A GUARDA DO REGISTO: `mutation: run_artifact` e a capability `cap:artifact.write` só
// valem na forma fechada da tool `artifact_write`. É a peça que limita a excepção da escrita local
// do run a uma tool — o RM dá `local_run_write` a quem o registo deixar declarar o eixo.

import (
	"errors"
	"testing"
)

func TestAOS549_RegistoSoAceitaAFormaFechada(t *testing.T) {
	t.Parallel()
	boa := func() modelToolSpec {
		return modelToolSpec{Name: artifactWriteToolName, Capability: artifactWriteCapability,
			Egress: "none", Reversibility: "reversible", Mutation: "run_artifact"}
	}
	if v, err := validateMutation(boa()); err != nil || v != "run_artifact" {
		t.Fatalf("a forma fechada foi recusada: %q %v", v, err)
	}
	casos := map[string]func(*modelToolSpec){
		"outro nome":             func(s *modelToolSpec) { s.Name = "grava" },
		"outra capability":       func(s *modelToolSpec) { s.Capability = "cap:fs.write" },
		"egress externo":         func(s *modelToolSpec) { s.Egress = "external" },
		"irreversivel":           func(s *modelToolSpec) { s.Reversibility = "irreversible" },
		"com sandbox":            func(s *modelToolSpec) { s.Sandbox = &sandboxMapping{} },
		"com modelo":             func(s *modelToolSpec) { s.Modelo = &modeloMapping{} },
		"capability noutra tool": func(s *modelToolSpec) { s.Name, s.Mutation = "exporta", "mutates" },
		"capability noutra tool que se diz leitura": func(s *modelToolSpec) { s.Name, s.Mutation = "le", "none" },
	}
	for nome, muda := range casos {
		s := boa()
		muda(&s)
		if _, err := validateMutation(s); !errors.Is(err, ErrBadMutation) {
			t.Errorf("%s: aceite (%v)", nome, err)
		}
	}
}
