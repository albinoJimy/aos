package referencemonitor

// AOS-549 — A ESCRITA LOCAL DO RUN (emenda de 2026-10-22 ao ADR-034, decisão do dono): uma tool que
// se declara `mutation: run_artifact` (egress none, reversível) com uma capability local não é
// privilegiada no gate de taint, e `cap:artifact.write` (só ela) é local no risco.

import (
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

func aos549Destinos() ToolDestinations {
	return NewToolDestinations(map[string]ToolDestination{
		"artifact_write": {Capability: "cap:artifact.write", Egress: "none", Mutation: MutationRunArtifact, Reversibility: "reversible"},
		// Os mesmos eixos numa capability de rede ou indeterminada: não ganham a excepção.
		"post_dito_artefacto":    {Capability: "cap:http.post", Egress: "none", Mutation: MutationRunArtifact, Reversibility: "reversible"},
		"s3_dito_artefacto":      {Capability: "cap:s3.upload", Egress: "none", Mutation: MutationRunArtifact, Reversibility: "reversible"},
		"artefacto_irreversivel": {Capability: "cap:artifact.write", Egress: "none", Mutation: MutationRunArtifact, Reversibility: "irreversible"},
		"artefacto_com_egress":   {Capability: "cap:artifact.write", Egress: "external", Mutation: MutationRunArtifact, Reversibility: "reversible"},
		"escrita_comum":          {Capability: "cap:fs.write", Egress: "none", Mutation: "mutates", Reversibility: "reversible"},
		// Revisão do AOS-549: o kernel fixa a capability — uma local que não a de artefactos não ganha
		// a excepção, mesmo com os três eixos certos e sem passar pelo validador do registo.
		"fs_dita_artefacto": {Capability: "cap:fs.write", Egress: "none", Mutation: MutationRunArtifact, Reversibility: "reversible"},
	})
}

func TestAOS549_GateDeixaAEscritaLocalDoRun(t *testing.T) {
	caps := []string{"cap:http.post", "cap:fs.read", "cap:fs.write"}
	casos := []struct {
		nome, tool, capability string
		querDeny               bool
		querRazao              string
	}{
		{"artifact_write-untrusted-passa", "artifact_write", "cap:artifact.write", false, TaintPrivilegeLocalRunWrite},
		{"rede-dita-artefacto-negada", "post_dito_artefacto", "cap:http.post", true, TaintPrivilegeCapability},
		{"indeterminada-dita-artefacto-nao-e-local", "s3_dito_artefacto", "cap:s3.upload", true, TaintPrivilegeDeclaredEffect},
		{"irreversivel-nao-e-escrita-local", "artefacto_irreversivel", "cap:artifact.write", true, TaintPrivilegeDeclaredEffect},
		{"com-egress-nao-e-escrita-local", "artefacto_com_egress", "cap:artifact.write", true, TaintPrivilegeDeclaredEffect},
		{"escrita-comum-continua-negada", "escrita_comum", "cap:fs.write", true, TaintPrivilegeCapability},
		{"local-dita-artefacto-nao-ganha-a-excepcao", "fs_dita_artefacto", "cap:fs.write", true, TaintPrivilegeCapability},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			dec, p := aos538Mediar(t, caps, aos549Destinos(), c.tool, c.capability, taint.StringUntrusted, "")
			if (dec.Effect == EffectDeny) != c.querDeny {
				t.Fatalf("efeito %s (%s), quer deny=%v", dec.Effect, dec.DeniedBy, c.querDeny)
			}
			if p.Context.TaintPrivilege != c.querRazao {
				t.Fatalf("razao %q, quer %q", p.Context.TaintPrivilege, c.querRazao)
			}
		})
	}
	priv := NewDestinationPrivileged(NewStaticPrivilegedSet(caps...), aos549Destinos())
	if got := priv.LocalRunWrites(); len(got) != 1 || got[0] != "artifact_write" {
		t.Fatalf("LocalRunWrites = %v", got)
	}
	for _, n := range priv.LocalReads() {
		if n == "artifact_write" {
			t.Fatal("a escrita local do run apareceu como leitura local")
		}
	}
}

func TestAOS549_ArtefactoELocalNoRisco(t *testing.T) {
	t.Parallel()
	if egressForCall(&Call{Capability: "cap:artifact.write", Resource: Resource{Type: "file"}}) != risk.EgressNone {
		t.Fatal("cap:artifact.write nao resolve egress none")
	}
	if capabilityIrreversible("cap:artifact.write") {
		t.Fatal("cap:artifact.write e irreversivel por derivacao")
	}
	// Só a capability exacta é local: outro verbo da família continua indeterminado (fail-closed).
	for _, c := range []string{"cap:artifact.export", "cap:artifact.share", "cap:artifact.writeall"} {
		if egressForCall(&Call{Capability: c, Resource: Resource{Type: "file"}}) == risk.EgressNone {
			t.Fatalf("%s resolveu egress none", c)
		}
	}
}
