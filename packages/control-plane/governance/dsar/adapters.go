package dsar

import audit "github.com/aos-ref/platform/audit"

// auditStore adapta o [*audit.Shredder] (crypto-shredding do KeyVault por-titular,
// AOS-083) à porta [ShreddableKeyStore]. O mesmo Shredder pode servir também de
// [HoldOracle] do fluxo (implementa Held), centralizando a lógica de legal hold
// por-partição no audit sem a duplicar aqui.
type auditStore struct {
	name string
	sh   *audit.Shredder
}

// AuditStore liga o crypto-shredding do audit ao fluxo DSAR sob o rótulo dado.
func AuditStore(name string, sh *audit.Shredder) ShreddableKeyStore {
	return auditStore{name: name, sh: sh}
}

func (a auditStore) Name() string { return a.name }

// Shred delega no audit.Shredder: destrói a KEK do titular (idempotente) e é ele
// próprio fail-closed sob legal hold (devolve audit.ErrLegalHold), pelo que o store
// nunca destrói uma chave retida mesmo que o fluxo o invocasse.
func (a auditStore) Shred(subjectID string) error { return a.sh.Shred(subjectID) }

// redactionShredder é o subconjunto do redaction.InMemoryKeySource (ou de um KMS
// equivalente) que o adaptador precisa: destruir a chave de tokenização por-titular.
// A chave devolvida é DELIBERADAMENTE ignorada — o segredo nunca é exposto (ADR-006).
type redactionShredder interface {
	Shred(subject string) (key []byte, existed bool)
}

// redactionStore adapta um KeySource shreddable (tokenização de PII, AOS-091) à
// porta [ShreddableKeyStore].
type redactionStore struct {
	name string
	ks   redactionShredder
}

// RedactionStore liga o crypto-shredding da redação ao fluxo DSAR sob o rótulo dado.
func RedactionStore(name string, ks redactionShredder) ShreddableKeyStore {
	return redactionStore{name: name, ks: ks}
}

func (r redactionStore) Name() string { return r.name }

// Shred destrói a chave de tokenização do titular. A chave devolvida é IGNORADA
// (nunca logada nem propagada); existed=false (chave ausente/já apagada) é um no-op
// idempotente sem erro — após o shred qualquer token do titular fica irresolúvel.
func (r redactionStore) Shred(subjectID string) error {
	_, _ = r.ks.Shred(subjectID)
	return nil
}
