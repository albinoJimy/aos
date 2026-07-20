package testkit

import (
	"sync"
	"time"
)

// CanonicalInstant é o instante fixo canónico das fixtures do testkit. É estável
// e arbitrário (nunca "agora"): qualquer carimbo temporal observacional derivado
// dele é reproduzível entre corridas, pelo que uma cadeia de audit/eventos selada
// com este relógio é byte-idêntica em cada execução. Escolhido dentro do horizonte
// do EPIC-11 (Julho de 2026) por legibilidade.
var CanonicalInstant = time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)

// FixedClock devolve um relógio determinista PARADO em [CanonicalInstant]. É o
// clock canónico a injectar via as opções WithClock dos módulos de produção
// (quase todos as expõem) para tornar os timestamps de teste reproduzíveis.
//
// Nunca deve participar numa decisão de correcção (as decisões dos controlos são
// puras); só carimba tempo observacional. Devolver uma função (e não um valor)
// espelha a assinatura `func() time.Time` que os módulos aceitam.
func FixedClock() func() time.Time {
	return func() time.Time { return CanonicalInstant }
}

// ManualClock é um relógio determinista com AVANÇO EXPLÍCITO: começa em
// [CanonicalInstant] (ou no instante dado a [NewManualClock]) e só avança quando o
// teste chama [ManualClock.Advance]. Serve cenários que precisam de tempo a
// progredir de forma controlada — ex.: expiração de TTL de uma lease (BRK),
// timeouts de HITL — sem depender do relógio de parede (sem flakiness).
//
// É seguro para uso concorrente (-race): Now e Advance serializam no mesmo mutex.
type ManualClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewManualClock constrói um [ManualClock] a começar em `start`. Um `start` zero
// (time.Time{}) cai no [CanonicalInstant] canónico.
func NewManualClock(start time.Time) *ManualClock {
	if start.IsZero() {
		start = CanonicalInstant
	}
	return &ManualClock{now: start}
}

// Now devolve o instante corrente do relógio. Tem a assinatura `func() time.Time`
// esperada pelas opções WithClock — passe [ManualClock.Now] directamente.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance faz o relógio avançar `d` e devolve o novo instante. Uma duração
// negativa é ignorada (o tempo nunca recua — monotonicidade), devolvendo o
// instante corrente inalterado.
func (c *ManualClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.now = c.now.Add(d)
	}
	return c.now
}

// Set posiciona o relógio num instante absoluto (>= corrente; um instante no
// passado é ignorado para preservar a monotonicidade) e devolve o instante
// efectivo.
func (c *ManualClock) Set(t time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.now) {
		c.now = t
	}
	return c.now
}
