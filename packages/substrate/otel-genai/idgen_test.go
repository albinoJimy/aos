package otelgenai

import (
	"bytes"
	"testing"
)

func TestSequentialIDGeneratorDeterministic(t *testing.T) {
	g := &SequentialIDGenerator{}
	t1, t2 := g.NewTraceID(), g.NewTraceID()
	if t1 == t2 {
		t.Fatal("trace ids sequenciais deviam diferir")
	}
	s1, s2 := g.NewSpanID(), g.NewSpanID()
	if s1 == s2 {
		t.Fatal("span ids sequenciais deviam diferir")
	}
	// Determinismo: um gerador novo reproduz a mesma sequência.
	g2 := &SequentialIDGenerator{}
	if g2.NewTraceID() != t1 {
		t.Fatal("sequência de trace não é determinista")
	}
	// Não-zero (validade).
	if t1 == ([16]byte{}) || s1 == ([8]byte{}) {
		t.Fatal("id sequencial não devia ser todo-zero")
	}
}

func TestCryptoIDGeneratorFromReader(t *testing.T) {
	// Fonte controlada: prova que lê os bytes da io.Reader injectada.
	src := bytes.Repeat([]byte{0xAB}, 64)
	g := NewCryptoIDGenerator(bytes.NewReader(src))
	tr := g.NewTraceID()
	if tr != ([16]byte{0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB}) {
		t.Errorf("trace id da fonte injectada errado: %x", tr)
	}
	sp := g.NewSpanID()
	if sp != ([8]byte{0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB}) {
		t.Errorf("span id da fonte injectada errado: %x", sp)
	}
}

func TestCryptoIDGeneratorNilReaderDefaults(t *testing.T) {
	// nil → crypto/rand.Reader; ids não-zero e (quase de certeza) distintos.
	g := NewCryptoIDGenerator(nil)
	a, b := g.NewTraceID(), g.NewTraceID()
	if a == ([16]byte{}) || b == ([16]byte{}) {
		t.Fatal("trace id de crypto/rand não devia ser todo-zero")
	}
	if a == b {
		t.Fatal("dois trace ids de crypto/rand não deviam colidir")
	}
}

func TestCryptoIDGeneratorShortReaderNeverZero(t *testing.T) {
	// Fonte esgotada (reader vazio): o fallback garante ids válidos (não-zero).
	g := NewCryptoIDGenerator(bytes.NewReader(nil))
	if g.NewTraceID() == ([16]byte{}) {
		t.Fatal("fallback devia evitar trace id todo-zero")
	}
	if g.NewSpanID() == ([8]byte{}) {
		t.Fatal("fallback devia evitar span id todo-zero")
	}
}
