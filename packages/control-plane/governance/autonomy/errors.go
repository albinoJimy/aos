package autonomy

import "errors"

var (
	// ErrInvalidLevel — tentativa de fixar um nível fora do domínio L0–L5. Um nível
	// inválido é REJEITADO (nunca aplicado): a fronteira valida antes de mutar o
	// registo, evitando um estado que [Oversight] teria de tratar como fail-closed.
	ErrInvalidLevel = errors.New("autonomy: nivel invalido (fora de L0-L5)")
	// ErrEmptyPair — agente ou domínio vazios num [LevelRegistry.SetLevel]. O par
	// (agente, domínio) é a chave do registo; uma chave parcial é malformada.
	ErrEmptyPair = errors.New("autonomy: par (agente,dominio) incompleto")
	// ErrMissingReason — [LevelRegistry.SetLevel] sem motivo. AC5 exige que uma
	// alteração de nível seja um evento auditável COM motivo; um motivo vazio
	// selaria a alteração sem justificação e é REJEITADO antes de mutar/selar.
	ErrMissingReason = errors.New("autonomy: motivo obrigatorio na alteracao de nivel")
	// ErrMissingActor — [LevelRegistry.SetLevel] sem actor. AC5 exige atribuição de
	// responsabilidade; um actor vazio selaria a alteração de forma anónima
	// (quebrando o não-repúdio) e é REJEITADO antes de mutar/selar.
	ErrMissingActor = errors.New("autonomy: actor obrigatorio na alteracao de nivel")
)
