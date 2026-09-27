package audit

import (
	"crypto/sha256"
	"fmt"
)

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
// stampSchema atribui a versão de formato a um registo NOVO, imediatamente antes de ele
// ser selado. Um produtor que a fixe explicitamente é respeitado — é como se escrevem
// registos numa versão antiga (compatibilidade e testes de regressão do formato).
//
// É deliberado que só o caminho de escrita a atribua: a versão faz parte do CONTEÚDO
// SELADO, pelo que carimbá-la mais tarde mudaria o hash de um registo já na cadeia.
//
// `escrita` é a versão que o STORE escreve por omissão (0 ⇒ [CurrentSchemaVersion]; ver
// [ComVersaoDeEscrita]). Um registo abaixo de [SchemaV4] PERDE o `requested_by` e o `mandate_id`
// antes de ser selado (AOS-439): num v3 eles não entram no hash, e guardá-los ao lado — o ficheiro
// é o JSON do registo inteiro — punha no WORM uma atribuição que parece selada e não é.
func stampSchema(rec *AuditRecord, escrita uint8) {
	if rec.SchemaVersion == 0 {
		rec.SchemaVersion = escrita
		if rec.SchemaVersion == 0 {
			rec.SchemaVersion = CurrentSchemaVersion
		}
	}
	if rec.SchemaVersion < SchemaV4 {
		rec.Principal.RequestedBy = ""
		rec.Principal.MandateID = ""
	}
}

// versaoPrepostaAceite recusa um registo que o PRODUTOR preposte numa versão ACIMA da que o store
// escreve (AOS-439, 2.ª ronda da revisão). O expand/contract só protege o rollback se nenhum
// caminho conseguir pôr um v4 num WORM configurado para v3 — um produtor que fixasse
// `SchemaVersion: 4` passava ao lado do `stampSchema` e cortava o rollback na mesma. Prepor uma
// versão ABAIXO continua aceite (é como se escrevem registos de regressão do formato).
func versaoPrepostaAceite(rec AuditRecord, escrita uint8) error {
	efectiva := escrita
	if efectiva == 0 {
		efectiva = CurrentSchemaVersion
	}
	if rec.SchemaVersion > efectiva {
		return fmt.Errorf("%w: registo preposto em v%d, o store escreve v%d", ErrVersaoAcimaDaEscrita, rec.SchemaVersion, efectiva)
	}
	return nil
}

// versaoDeEscritaValida diz se um store pode ser configurado para escrever a versão `v`.
// Só as duas épocas que um binário desta release escreve: v3 (por omissão, que os binários
// anteriores ainda verificam) e v4.
func versaoDeEscritaValida(v uint8) bool { return v == SchemaV3 || v == SchemaV4 }

func ComputeEntryHash(prevHash []byte, rec AuditRecord) []byte {
	h := sha256.New()
	h.Write(prevHash)
	h.Write(canonicalContent(rec))
	return h.Sum(nil)
}
