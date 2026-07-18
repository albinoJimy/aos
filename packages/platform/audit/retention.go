package audit

import (
	"sync"
	"time"
)

// DataClass é a classe de retenção de um dado (tecnica/09 §5): cada classe tem um
// período de retenção próprio. Diagnósticos efémeros expiram cedo; o audit
// tamper-evident retém-se mais tempo — e sob legal hold não expira de todo.
type DataClass string

const (
	// ClassDiagnostic — diagnósticos efémeros (AOS-082): retenção curta, perda inócua.
	ClassDiagnostic DataClass = "diagnostic"
	// ClassTrajectory — trajectórias de execução.
	ClassTrajectory DataClass = "trajectory"
	// ClassAudit — o audit tamper-evident permanente.
	ClassAudit DataClass = "audit"
	// ClassPIIOperational — PII operacional cifrada por titular (alvo do shredding).
	ClassPIIOperational DataClass = "pii_operational"
)

// RetentionPolicy é a política de retenção por CLASSE (período configurável). É
// imutável após construída (a cópia do mapa é interna), pelo que pode ser
// partilhada sem sincronização.
type RetentionPolicy struct {
	periods map[DataClass]time.Duration
}

// NewRetentionPolicy constrói a política a partir de um mapa classe→período. Um
// período <= 0 ou uma classe ausente significam "sem retenção definida": nunca
// considerada expirada (fail-closed — não se auto-purga o que não tem período).
func NewRetentionPolicy(periods map[DataClass]time.Duration) RetentionPolicy {
	cp := make(map[DataClass]time.Duration, len(periods))
	for k, v := range periods {
		cp[k] = v
	}
	return RetentionPolicy{periods: cp}
}

// Period devolve o período de retenção de uma classe e se está definido (>0).
func (r RetentionPolicy) Period(class DataClass) (time.Duration, bool) {
	d, ok := r.periods[class]
	if !ok || d <= 0 {
		return 0, false
	}
	return d, true
}

// Expired indica se um dado da classe com a idade dada já ultrapassou a retenção.
// Uma classe sem período definido NUNCA expira (fail-closed): o purge automático
// não age sobre dados cujo período de retenção não foi configurado.
func (r RetentionPolicy) Expired(class DataClass, age time.Duration) bool {
	d, ok := r.Period(class)
	if !ok {
		return false
	}
	return age >= d
}

// LegalHold é o conjunto (por titular e/ou por partição) sob obrigação de
// preservação. Um legal hold SUSPENDE o TTL e o crypto-shredding: enquanto vigora,
// nem a expiração de retenção nem um DSAR podem destruir os dados retidos
// (ADR-010/011, tecnica/09 §5). Seguro para concorrência.
type LegalHold struct {
	mu         sync.RWMutex
	subjects   map[string]bool
	partitions map[string]bool
}

// NewLegalHold constrói um legal hold sem qualquer retenção activa.
func NewLegalHold() *LegalHold {
	return &LegalHold{
		subjects:   make(map[string]bool),
		partitions: make(map[string]bool),
	}
}

// HoldSubject coloca um titular sob legal hold (impede o seu shred/purge).
func (h *LegalHold) HoldSubject(subjectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subjects[subjectID] = true
}

// ReleaseSubject levanta o legal hold de um titular.
func (h *LegalHold) ReleaseSubject(subjectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subjects, subjectID)
}

// HeldSubject indica se um titular está sob legal hold.
func (h *LegalHold) HeldSubject(subjectID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.subjects[subjectID]
}

// HoldPartition coloca uma partição inteira sob legal hold. Para o [Shredder] o
// fazer valer, o titular tem de ser mapeado à partição via [SubjectPartitionIndex]
// (alimentado na ingestão e ligado ao shredder por [WithShredderSubjectIndex]);
// sem esse índice, só o hold por-titular ([HoldSubject]) é executável.
func (h *LegalHold) HoldPartition(partition string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.partitions[partition] = true
}

// ReleasePartition levanta o legal hold de uma partição.
func (h *LegalHold) ReleasePartition(partition string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.partitions, partition)
}

// HeldPartition indica se uma partição está sob legal hold.
func (h *LegalHold) HeldPartition(partition string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.partitions[partition]
}

// HasPartitionHolds indica se existe ALGUM legal hold por-partição activo. O
// [Shredder] usa-o como salvaguarda fail-closed: se há holds por-partição mas o
// [SubjectPartitionIndex] não está ligado ao shredder, o shred não consegue
// verificar a preservação por-partição e é recusado (em vez de a violar em
// silêncio) — fecha a lacuna de fail-open por wiring em falta.
func (h *LegalHold) HasPartitionHolds() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.partitions) > 0
}
