package audit

import "crypto/sha256"

// genesisPrefix é o domínio determinístico da âncora de génese por partição.
const genesisPrefix = "aos.audit.genesis:"

// GenesisHash devolve o PrevHash fixo do PRIMEIRO registo de uma partição:
// SHA-256("aos.audit.genesis:" + partition). É determinístico e distinto por
// partição, pelo que a cadeia de uma partição não pode ser confundida com a de
// outra nem "iniciada" a partir de um hash arbitrário.
func GenesisHash(partition string) []byte {
	sum := sha256.Sum256([]byte(genesisPrefix + partition))
	return sum[:]
}

// ComputeEntryHash calcula o EntryHash de um registo a partir do prevHash e do
// seu conteúdo canónico:
//
//	EntryHash = SHA-256( prevHash || canonicalContent(rec) )
//
// O prevHash entra explicitamente pela concatenação (não é re-serializado no
// conteúdo), cumprindo a definição do ADR-010. Como o conteúdo inclui AuditSeq,
// Partition e todos os metadados de responsabilização, qualquer mutação de um
// campo — ou do prevHash herdado — altera o EntryHash e propaga-se a todos os
// EntryHash subsequentes da cadeia.
func ComputeEntryHash(prevHash []byte, rec AuditRecord) []byte {
	h := sha256.New()
	h.Write(prevHash)
	h.Write(canonicalContent(rec))
	return h.Sum(nil)
}
