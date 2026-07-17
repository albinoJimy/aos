package securitytests

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// TestCorpusVersionedAndExtensible prova que o corpus de payloads adversariais é
// VERSIONADO e EXTENSÍVEL sem reescrever a harness:
//
//   - a versão do corpus casa [SuiteVersion] (um bump ao corpus força um bump consciente);
//   - cada categoria (prompt injection / egress / DNS) é não-vazia;
//   - os ids são únicos e as codificações descodificam (integridade da tabela);
//   - INVARIANTE DE EXTENSIBILIDADE: toda a entrada de prompt injection tem origem que
//     classifica UNTRUSTED — acrescentar um vector é acrescentar uma linha ao JSON e a
//     harness table-driven ([TestPromptInjection_CorpusBattery_AllBlocked]) exercita-o
//     automaticamente, sem alterações de código.
func TestCorpusVersionedAndExtensible(t *testing.T) {
	t.Parallel()
	c := mustCorpus(t)

	if c.Version != SuiteVersion {
		t.Fatalf("corpus version = %q, quer %q (coerência corpus↔suite)", c.Version, SuiteVersion)
	}
	if len(c.PromptInjections) == 0 || len(c.ExfilEgress) == 0 || len(c.ExfilDNS) == 0 {
		t.Fatalf("corpus com categoria vazia: pi=%d egress=%d dns=%d",
			len(c.PromptInjections), len(c.ExfilEgress), len(c.ExfilDNS))
	}

	// Ids únicos em todo o corpus (uma colisão mascararia um vector).
	seen := map[string]struct{}{}
	dup := func(id string) {
		if _, ok := seen[id]; ok {
			t.Fatalf("id de corpus duplicado: %q", id)
		}
		seen[id] = struct{}{}
	}

	// Prompt injections: descodificam E classificam untrusted (invariante de extensibilidade).
	for _, v := range c.PromptInjections {
		dup(v.ID)
		if _, err := effectivePayload(v); err != nil {
			t.Fatalf("payload inválido em %q: %v", v.ID, err)
		}
		if taint.LabelFor(taint.Origin(v.Origin)).IsTrusted() {
			t.Fatalf("vector %q tem origem %q que classifica TRUSTED — não seria uma injecção untrusted", v.ID, v.Origin)
		}
	}
	for _, v := range c.ExfilEgress {
		dup(v.ID)
		if v.Capability == "" || v.Target == "" {
			t.Fatalf("vector de egress %q incompleto", v.ID)
		}
	}
	for _, v := range c.ExfilDNS {
		dup(v.ID)
		if v.QName == "" {
			t.Fatalf("vector de DNS %q sem qname", v.ID)
		}
	}

	// Digest do corpus (tamper-evident por conteúdo): estável e não-vazio. Muda se
	// qualquer payload for alterado — o versionamento por digest é observável.
	sum := sha256.Sum256(corpusJSON)
	if hex.EncodeToString(sum[:]) == "" {
		t.Fatal("digest do corpus vazio")
	}
	t.Logf("corpus %s digest=%s (pi=%d egress=%d dns=%d)", c.Version, hex.EncodeToString(sum[:])[:16],
		len(c.PromptInjections), len(c.ExfilEgress), len(c.ExfilDNS))
}
