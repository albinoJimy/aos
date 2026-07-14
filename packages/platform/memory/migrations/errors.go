// Package migrations implementa o MOTOR de migração de schema de memória do AOS
// (AOS-041): o padrão expand → migrate → contract (parallel change), com cada fase
// aplicável E reversível de forma independente, dual-write/dual-read na fase
// expand (sem downtime), rollback de migração falhada sem perda nem corrupção, um
// registo de migrações durável e idempotente (reaplicar = no-op) e uma porta de
// eval-gate que RECUSA mudanças MAJOR sem aprovação (fail-closed, ADR-012).
//
// O motor opera sobre um SNAPSHOT de registos de uma classe e nunca muta o estado
// corrente até uma fase concluir com sucesso — é isto que torna o rollback trivial
// e não-destrutivo: uma fase que falha a meio deixa o estado byte-idêntico ao
// inicial. A durabilidade e a idempotência entre processos vêm do Event Store
// append-only (o mesmo substrato de idempotência do resto do AOS, ADR-001).
//
// Determinismo: sem time.Now/rand no caminho de decisão; os transforms de migração
// são funções puras (mesma entrada → mesma saída); o namespace de idempotência é
// injectável. A observabilidade passa pela porta Tracer zero-dep do Agent Runtime.
package migrations

// MigrationError é o erro sentinela do motor de migração. Código estável,
// comparável com errors.Is. Fail-closed em toda a validação.
type MigrationError struct {
	Code string
	msg  string
}

func (e *MigrationError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrInvalidMigration — a definição de migração é inválida (ID vazio, classe
	// inválida, transforms nil, ou From == To).
	ErrInvalidMigration = &MigrationError{Code: "E_MIG_INVALID", msg: "definicao de migracao invalida"}

	// ErrMigrationDenied — a mudança MAJOR (quebra de contrato) não foi aprovada
	// pelo eval-gate. Fail-closed: nada é aplicado; o estado mantém-se.
	ErrMigrationDenied = &MigrationError{Code: "E_MIG_DENIED", msg: "migracao MAJOR recusada pelo eval-gate (sem aprovacao)"}

	// ErrPhaseOrder — uma fase foi pedida fora de ordem (ex.: migrate antes de
	// expand, ou contract antes de migrate). Fail-closed: a fase não é aplicada.
	ErrPhaseOrder = &MigrationError{Code: "E_MIG_PHASE_ORDER", msg: "fase de migracao pedida fora de ordem"}

	// ErrRecordSchemaMismatch — um registo inicial não está na versão de schema de
	// origem (From) declarada pela migração. Fail-closed na construção do runner.
	ErrRecordSchemaMismatch = &MigrationError{Code: "E_MIG_RECORD_SCHEMA_MISMATCH", msg: "registo inicial nao esta na versao de schema de origem"}

	// ErrTransformFailed — um transform (Up/Down) devolveu erro para um registo. A
	// fase inteira é abortada e o estado mantém-se inalterado (rollback implícito).
	ErrTransformFailed = &MigrationError{Code: "E_MIG_TRANSFORM_FAILED", msg: "transform de migracao falhou; estado inalterado"}

	// ErrUnknownRecord — pediu-se leitura de um id inexistente no snapshot.
	ErrUnknownRecord = &MigrationError{Code: "E_MIG_UNKNOWN_RECORD", msg: "registo inexistente no snapshot de migracao"}

	// ErrSchemaConsistency — um transform devolveu um registo cujo schema_version
	// não corresponde à versão-alvo da direcção aplicada (Up→To, Down→From).
	// Fail-closed: um transform que não estampa a versão correcta é um bug de
	// migração e é rejeitado antes de contaminar o estado.
	ErrSchemaConsistency = &MigrationError{Code: "E_MIG_SCHEMA_CONSISTENCY", msg: "transform nao estampou o schema_version alvo"}

	// ErrIrreversibleMigration — backstop SEMÂNTICO (independente do rótulo
	// MAJOR/MINOR/PATCH): Down(Up(old)) NÃO reproduz old, logo a migração perde ou
	// corrompe dados e não é um inverso exacto. Fail-closed: uma quebra de contrato
	// disfarçada de mudança retrocompatível é RECUSADA no expand antes de qualquer
	// escrita, mesmo que o eval-gate a tenha deixado passar por o rótulo ser MINOR/PATCH.
	ErrIrreversibleMigration = &MigrationError{Code: "E_MIG_IRREVERSIBLE", msg: "migracao nao e inverso exacto (round-trip Down(Up(x)) != x): perda/corrupcao de dados"}

	// ErrMigrationRedefined — colisão de identidade: um ID de migração já registado
	// de forma durável para uma fase é reutilizado com From/To (ou Kind) DIFERENTES.
	// Fail-closed: a idempotência assenta na estabilidade da definição por ID; uma
	// redefinição in-place silenciosa faria o log durável descrever incorrectamente a
	// migração aplicada, por isso é rejeitada.
	ErrMigrationRedefined = &MigrationError{Code: "E_MIG_REDEFINED", msg: "ID de migracao reutilizado com definicao (From/To/Kind) divergente da ja registada"}
)
