package lru

import "testing"

func TestTectoDespejaOMenosRecente(t *testing.T) {
	m := New[string, int](2)
	m.Put("a", 1)
	m.Put("b", 2)
	if k, ok := m.Put("c", 3); !ok || k != "a" {
		t.Fatalf("esperava despejar a; veio %q ok=%v", k, ok)
	}
	if _, ok := m.Get("a"); ok {
		t.Fatal("a devia ter sido despejada")
	}
	if m.Len() != 2 {
		t.Fatalf("Len=%d, quero 2", m.Len())
	}
}

func TestPutNumaChaveExistenteTocaENaoDespeja(t *testing.T) {
	m := New[string, int](2)
	m.Put("a", 1)
	m.Put("b", 2)
	if _, ok := m.Put("a", 10); ok {
		t.Fatal("regravar uma chave existente nao pode despejar")
	}
	// a foi tocada: a proxima chave nova despeja b.
	if k, ok := m.Put("c", 3); !ok || k != "b" {
		t.Fatalf("esperava despejar b (a foi tocada); veio %q ok=%v", k, ok)
	}
	if v, ok := m.Get("a"); !ok || v != 10 {
		t.Fatalf("a=%d ok=%v, quero 10", v, ok)
	}
}

func TestGetNaoTocaAOrdem(t *testing.T) {
	m := New[string, int](2)
	m.Put("a", 1)
	m.Put("b", 2)
	m.Get("a") // introspecção: não conta como uso
	if k, _ := m.Put("c", 3); k != "a" {
		t.Fatalf("Get nao pode tocar; esperava despejar a, veio %q", k)
	}
}

func TestCapacidadeNaoPositivaViraUm(t *testing.T) {
	m := New[string, int](0)
	if m.Capacidade() != 1 {
		t.Fatalf("Capacidade=%d, quero 1", m.Capacidade())
	}
	m.Put("a", 1)
	m.Put("b", 2)
	if m.Len() != 1 {
		t.Fatalf("Len=%d, quero 1", m.Len())
	}
}
