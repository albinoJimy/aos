package audit

import (
	"bytes"
	"errors"
	"testing"
)

// AOS-093 — cifra POR-TITULAR do CONTEÚDO DOS RUNS reutilizando o envelope DEK/KEK do
// audit ([SealContent]/[OpenContent]). Estes testes provam, ao nível da primitiva:
//   - confidencialidade: o plaintext NÃO aparece no blob selado (vai para o WAL);
//   - recuperabilidade ANTES do shred (não-vácuo): OpenContent devolve o plaintext;
//   - IRRECUPERABILIDADE REAL após o shred: destruir a KEK do titular faz OpenContent
//     falhar com ErrDecrypt (não basta afirmar — prova-se que a decifragem falha);
//   - ISOLAMENTO por-titular: o shred do titular A não afecta o conteúdo do titular B.

// synthPII é texto SINTÉTICO (nunca PII real) usado como conteúdo de run nos testes.
const synthPII = "objective: contactar ACME-SYNTH-7788 sobre o caso #SYNTH-0001"

func TestSealContent_RoundTripAndShred(t *testing.T) {
	vault := NewInMemoryKeyVault(detRand())
	const subject = "nhi:agent-synth-A"

	sealed, err := SealContent(vault, subject, []byte(synthPII), detRand())
	if err != nil {
		t.Fatalf("SealContent: %v", err)
	}

	// Confidencialidade: o texto-claro NÃO está no blob selado (o que iria ao WAL).
	if bytes.Contains(sealed, []byte(synthPII)) {
		t.Fatal("blob selado contém o plaintext em claro — confidencialidade violada")
	}
	if bytes.Contains(sealed, []byte("SYNTH-0001")) {
		t.Fatal("blob selado contém um fragmento do plaintext em claro")
	}

	// Recuperabilidade ANTES do shred (não-vácuo).
	got, err := OpenContent(vault, subject, sealed)
	if err != nil {
		t.Fatalf("OpenContent antes do shred: %v", err)
	}
	if !bytes.Equal(got, []byte(synthPII)) {
		t.Fatalf("plaintext decifrado != original: %q", got)
	}

	// Crypto-shredding: destrói a KEK do titular.
	vault.Delete(subject)

	// IRRECUPERABILIDADE REAL: a decifragem FALHA (não é afirmação — é prova).
	if _, err := OpenContent(vault, subject, sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("após shred, OpenContent devia falhar com ErrDecrypt, deu: %v", err)
	}
}

func TestSealContent_PerSubjectIsolation(t *testing.T) {
	vault := NewInMemoryKeyVault(detRand())
	const subjA, subjB = "nhi:agent-A", "nhi:agent-B"
	const contentA = "prompt-A: dado sintetico AAA-111"
	const contentB = "prompt-B: dado sintetico BBB-222"

	sealedA, err := SealContent(vault, subjA, []byte(contentA), detRand())
	if err != nil {
		t.Fatalf("seal A: %v", err)
	}
	sealedB, err := SealContent(vault, subjB, []byte(contentB), detRand())
	if err != nil {
		t.Fatalf("seal B: %v", err)
	}

	// Shred SÓ do titular A.
	vault.Delete(subjA)

	// A é irrecuperável...
	if _, err := OpenContent(vault, subjA, sealedA); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("A devia ser irrecuperável após o seu shred, deu: %v", err)
	}
	// ...mas B mantém-se intacto (isolamento por-titular).
	gotB, err := OpenContent(vault, subjB, sealedB)
	if err != nil {
		t.Fatalf("B devia continuar decifrável após shred de A: %v", err)
	}
	if !bytes.Equal(gotB, []byte(contentB)) {
		t.Fatalf("conteúdo de B corrompido: %q", gotB)
	}
}

func TestSealContent_WrongSubjectCannotOpen(t *testing.T) {
	vault := NewInMemoryKeyVault(detRand())
	sealed, err := SealContent(vault, "nhi:owner", []byte(synthPII), detRand())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Um titular diferente não tem a KEK que embrulha a DEK ⇒ não decifra.
	if _, err := OpenContent(vault, "nhi:intruder", sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("titular errado devia falhar com ErrDecrypt, deu: %v", err)
	}
}
