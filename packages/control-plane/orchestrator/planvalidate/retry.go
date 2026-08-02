package planvalidate

import "errors"

// MaxAttempts é o tecto de tentativas de validação de um intake (§3.3): N=3. Ao
// esgotar sem uma proposta válida, o intake FALHA fail-closed — não se materializa
// um plano inválido nem se re-tenta indefinidamente.
const MaxAttempts = 3

// ErrIntakeExhausted é a falha de intake fail-closed: as N tentativas de validação
// esgotaram-se sem um [Verdict] aceite. O intake não prossegue.
var ErrIntakeExhausted = errors.New("planvalidate: intake esgotado — todas as tentativas de validação falharam (fail-closed)")

// Ledger é o CONTADOR de tentativas de validação de um único intake. Não é
// concorrente (um intake é serial). Registra cada veredicto e decide, de forma
// fail-closed, quando o intake está esgotado.
//
// Semântica: cada [Ledger.Record] consome uma tentativa. Um veredicto aceite pára
// o ciclo (sucesso). Um veredicto de rejeição na última tentativa permitida esgota
// o intake.
type Ledger struct {
	max      int
	attempts int
	done     bool // um veredicto aceite já encerrou o ciclo
}

// NewLedger cria um contador com o tecto padrão [MaxAttempts].
func NewLedger() *Ledger { return &Ledger{max: MaxAttempts} }

// NewLedgerN cria um contador com um tecto explícito (>=1). Um n <= 0 cai para 1:
// nunca zero tentativas (isso seria um intake que falha sem sequer validar).
func NewLedgerN(n int) *Ledger {
	if n < 1 {
		n = 1
	}
	return &Ledger{max: n}
}

// Max devolve o tecto de tentativas.
func (l *Ledger) Max() int { return l.max }

// Attempts devolve quantas tentativas foram consumidas.
func (l *Ledger) Attempts() int { return l.attempts }

// Next indica se ainda é permitida outra tentativa de validação (não encerrado por
// sucesso e ainda há orçamento de tentativas). Fail-closed: falso quando esgotado.
func (l *Ledger) Next() bool { return !l.done && l.attempts < l.max }

// Record consome uma tentativa com o veredicto v e devolve o número (1-based) desta
// tentativa e se o intake ficou ESGOTADO. Esgotado ⇔ v rejeitou E esta era a última
// tentativa permitida. Um v aceite encerra o ciclo (sucesso, nunca esgotado).
//
// Chamar Record para além do tecto é um no-op defensivo: devolve o estado corrente
// sem contar tentativas fantasma.
func (l *Ledger) Record(v Verdict) (attempt int, exhausted bool) {
	if l.done || l.attempts >= l.max {
		return l.attempts, l.exhaustedState()
	}
	l.attempts++
	if v.OK {
		l.done = true
		return l.attempts, false
	}
	return l.attempts, l.exhaustedState()
}

// exhaustedState indica se o intake está esgotado: sem sucesso e sem tentativas
// restantes.
func (l *Ledger) exhaustedState() bool { return !l.done && l.attempts >= l.max }

// Exhausted indica se o intake esgotou (todas as tentativas falharam).
func (l *Ledger) Exhausted() bool { return l.exhaustedState() }

// Err devolve [ErrIntakeExhausted] quando o intake esgotou, nil caso contrário. É a
// forma fail-closed de o chamador propagar a falha de intake.
func (l *Ledger) Err() error {
	if l.Exhausted() {
		return ErrIntakeExhausted
	}
	return nil
}
