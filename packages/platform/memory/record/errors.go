package record

// RecordError é o tipo de erro sentinela do registo de trajectória, com um código
// estável comparável por errors.Is. Toda a validação resolve-se pelo lado seguro
// (fail-closed): um turno sem o manifesto mínimo por turno é sempre rejeitado.
type RecordError struct {
	Code string
	msg  string
}

func (e *RecordError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrIncompleteTurnManifest — o turno não traz o manifesto mínimo por turno
	// (prompt_hash + model-id + assembly_version + manifest_schema_version). O
	// registo por turno do hash do prompt materializado, model-id/params e versões
	// é obrigatório (AOS-036 / ADR-010); a sua ausência falha-fecha.
	ErrIncompleteTurnManifest = &RecordError{
		Code: "E_REC_INCOMPLETE_TURN_MANIFEST",
		msg:  "manifesto por turno incompleto (prompt_hash/model-id/versoes obrigatorios)",
	}

	// ErrNilRecord — foi passado um registo nil à via persist.
	ErrNilRecord = &RecordError{
		Code: "E_REC_NIL_RECORD",
		msg:  "registo de trajectoria nil",
	}
)
