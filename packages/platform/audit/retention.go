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
	mu sync.RWMutex
	// destruicao e a BARREIRA DE DESTRUICAO — ver BeginDestruction. NAO e o mu.
	destruicao sync.RWMutex
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

// ---------------------------------------------------------------------------------------------
// A BARREIRA DE DESTRUIÇÃO.
//
// PORQUE EXISTE. O `mu` acima protege o MAPA — cada leitura e cada escrita são atómicas entre si.
// Não protegia o que interessa: a JANELA entre «perguntei se está retido» e «destruí».
//
// Medido, não estimado (varredura adversarial de 2026-08-21):
//
//	selo do retention.expired : ~30 ms   ← janela held()→destruição, POR REGISTO
//	POST /dsar/hold           : 21–58 ms ← hold pedido, ainda NÃO em vigor
//
// As duas janelas são `fsync` e não encolhem. E compunham-se no pior sentido: o handler do hold
// SELA primeiro e só depois aplica, portanto durante a sua própria selagem o hold não vigora; o
// varredor avalia `held()` no topo do ciclo e destrói ~30 ms depois. Demonstrado: hold selado no
// WORM, 200 devolvido ao operador, material destruído na mesma — e o relatório da passagem a
// declarar `Held=0`, ou seja, a AFIRMAR que nenhuma preservação foi desrespeitada.
//
// A INVARIANTE QUE ESTA BARREIRA ESTABELECE, e é a única formulação defensável:
//
//	um 200 do /dsar/hold significa que NENHUMA destruição posterior deixa de ver este hold.
//
// Repare-se no que ela NÃO promete: um registo cuja destruição já estava em curso quando o pedido
// chegou pode ser destruído na mesma. O que fica impossível é o operador RECEBER o 200 antes
// disso — a resposta espera pelo passo em voo. Uma preservação confirmada nunca chega tarde;
// quando muito, a confirmação demora mais um passo.
//
// PORQUÊ UM SEGUNDO MUTEX e não o `mu`. Porque `held()` faz `mu.RLock()` por dentro, e um
// `RLock` aninhado noutro `RLock` do MESMO `sync.RWMutex` bloqueia para sempre assim que um
// escritor fique à espera — é um deadlock documentado do Go. A ordem de aquisição é sempre
// `destruicao` → `mu`, nunca a inversa.
//
// GRANULARIDADE. A barreira é tomada POR REGISTO, não por passagem: o hold espera no máximo por
// um passo de destruição (~30 ms), não pelo varrimento inteiro.
// ---------------------------------------------------------------------------------------------

// PERIGO LATENTE, verificado hoje e não garantido para sempre: NUNCA aninhar um
// `BeginDestruction` dentro de outro. Um `RLock` recursivo do mesmo [sync.RWMutex] bloqueia para
// sempre assim que um escritor fique à espera entre os dois. Hoje não acontece — o
// `cryptoShredSink.Expire` do nó chama `vault.Delete` directamente e não [Shredder.Shred], e o
// `PurgeExpired` chama `Shred` a UM nível. Um sink futuro que delegue no shredder reintroduz o
// aninhamento; se isso for preciso, a resposta é uma variante de `Shred` SEM barreira, não um
// segundo `BeginDestruction`.
//
// BeginDestruction adquire a barreira em modo PARTILHADO — vários passos de destruição podem
// correr entre si, o que fica excluído é uma COLOCAÇÃO de hold a meio de um deles. Devolve a
// função que a larga; o chamador tem de a invocar em todos os caminhos de saída.
//
// Nil-safe: um `*LegalHold` nil devolve um no-op, para que um shredder ou um job compostos sem
// barreira de preservação continuem a correr (é a postura que já tinham).
func (h *LegalHold) BeginDestruction() func() {
	if h == nil {
		return func() {}
	}
	h.destruicao.RLock()
	return h.destruicao.RUnlock
}

// BeginPlacement adquire a barreira em modo EXCLUSIVO, para colocar ou levantar um hold. Devolve
// a função que a larga.
//
// TAMBÉM PARA O LEVANTAMENTO, e é deliberado: sem isso, um release concorrente com um passo que
// já tinha visto o hold deixaria a cadeia a afirmar uma preservação que a destruição respeitou
// por uma ordem que ninguém controla. A simetria custa nada — colocar e levantar holds são actos
// raros de um operador — e torna a invariante enunciável nos dois sentidos.
func (h *LegalHold) BeginPlacement() func() {
	if h == nil {
		return func() {}
	}
	h.destruicao.Lock()
	return h.destruicao.Unlock
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
