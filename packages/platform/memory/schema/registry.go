package schema

import (
	"sync"

	"github.com/aos-ref/platform/memory/domain"
)

// ClassRegistry detém a versão de schema CORRENTE de cada uma das quatro classes
// de memória e impõe a evolução MONÓTONA (SemVer estritamente crescente). É o
// custódio in-process do "schema versionado por classe" (AOS-041): o motor de
// migração avança a versão corrente de uma classe SÓ quando a nova forma passa a
// ser canónica (fase migrate), e a regra monótona garante que a linhagem de
// versões nunca regride nem re-adopta a mesma versão.
//
// É seguro para concorrência.
type ClassRegistry struct {
	mu      sync.RWMutex
	current map[domain.MemoryClass]Version
}

// NewClassRegistry constrói um registo vazio (nenhuma classe tem versão ainda).
func NewClassRegistry() *ClassRegistry {
	return &ClassRegistry{current: make(map[domain.MemoryClass]Version)}
}

// DefaultClassRegistry constrói um registo com as quatro classes ancoradas em
// 1.0.0 — a versão de schema base estabelecida por AOS-035 (os registos existentes
// já carregam schema_version "1.0.0"). É o ponto de partida da evolução.
func DefaultClassRegistry() *ClassRegistry {
	r := NewClassRegistry()
	base := Version{Major: 1}
	for _, c := range domain.AllClasses() {
		r.current[c] = base
	}
	return r
}

// Current devolve a versão de schema corrente da classe. O segundo retorno é false
// se a classe ainda não tem versão registada.
func (r *ClassRegistry) Current(class domain.MemoryClass) (Version, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.current[class]
	return v, ok
}

// Anchor fixa a versão INICIAL de uma classe sem impor monotonicidade (só é
// permitido quando a classe ainda não tem versão). Serve para inicializar uma
// classe numa versão base; a evolução subsequente passa por Register. Fail-closed:
// classe inválida é rejeitada; re-ancorar uma classe já registada é rejeitado como
// não-monótono (usa Register para avançar).
func (r *ClassRegistry) Anchor(class domain.MemoryClass, v Version) error {
	if !class.Valid() {
		return ErrInvalidClass
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.current[class]; ok {
		return ErrNonMonotonic
	}
	r.current[class] = v
	return nil
}

// Register avança a versão de schema de uma classe. Impõe a evolução MONÓTONA:
// v tem de ser ESTRITAMENTE mais recente que a corrente (fail-closed — uma versão
// igual ou anterior devolve ErrNonMonotonic e a corrente mantém-se). Se a classe
// ainda não tem versão, qualquer versão válida é aceite como inicial.
func (r *ClassRegistry) Register(class domain.MemoryClass, v Version) error {
	if !class.Valid() {
		return ErrInvalidClass
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.current[class]; ok && v.Compare(cur) <= 0 {
		return ErrNonMonotonic
	}
	r.current[class] = v
	return nil
}

// Revert regride, de forma CONTROLADA e guardada, a versão de schema corrente de
// uma classe para `to`. É a operação inversa de Register, usada EXCLUSIVAMENTE no
// contexto de um rollback/revert de migração (o motor não a expõe fora daí): ao
// contrário de Register — estritamente monótono — permite regredir, mas só quando
//   - a classe tem versão registada e a corrente é EXACTAMENTE `expectedCurrent`
//     (compare-and-swap: nunca reverte sobre um estado que outra migração já mudou), e
//   - `to` não é mais recente que a corrente (Revert nunca AVANÇA; para isso existe
//     Register).
//
// Fail-closed: qualquer divergência devolve ErrRevertMismatch e a corrente
// mantém-se. Assim a autoridade de versão da classe fica sempre coerente com a fase
// efectiva da migração (sem estado híbrido após rollback).
func (r *ClassRegistry) Revert(class domain.MemoryClass, to, expectedCurrent Version) error {
	if !class.Valid() {
		return ErrInvalidClass
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.current[class]
	if !ok || cur.Compare(expectedCurrent) != 0 {
		return ErrRevertMismatch
	}
	if to.Compare(expectedCurrent) > 0 {
		return ErrRevertMismatch
	}
	r.current[class] = to
	return nil
}
