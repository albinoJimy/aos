package planmigrate

import (
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// Erros de admissão — todos FAIL-CLOSED. Um plano aprovado só materializa/reproduz
// se o seu `plan_version` CONGELADO passar por estes gates; caso contrário volta a
// planeamento. Nunca há auto-migração silenciosa.
var (
	// ErrOutsideSupportWindow — o MAJOR do `plan_version` está FORA da janela de
	// suporte declarada (o reader já não é retido, ADR-012). O run é INADMISSÍVEL:
	// tratado como um payload perdido (a mesma inadmissibilidade de AOS-016) — não
	// se adivinha nem se auto-migra.
	ErrOutsideSupportWindow = errors.New("planmigrate: plan_version fora da janela de suporte de MAJORs — inadmissível (reader não retido, AOS-016)")
	// ErrRetired — o `plan_version` exacto foi RETIRADO (ex.: retirada de segurança)
	// antes da materialização. Invalida o run: exige re-plano + re-aprovação.
	ErrRetired = errors.New("planmigrate: plan_version retirada — exige re-plano e re-aprovacao")
	// ErrInvalidWindow — a janela de suporte é incoerente (MinMajor > MaxMajor).
	ErrInvalidWindow = errors.New("planmigrate: janela de suporte invalida (MinMajor > MaxMajor)")
)

// SupportWindow é o intervalo INCLUSIVO de MAJORs de `plan_version` cujos readers
// estão RETIDOS (ADR-012). É a forma executável da regra "bump de MAJOR passa por
// ADR-012 com reader retido OU deprecação documentada":
//
//   - RETER o reader antigo = manter MinMajor baixo o suficiente para o cobrir.
//   - DEPRECAR (documentado) = avançar MinMajor, o que torna os runs desse MAJOR
//     INADMISSÍVEIS de forma explícita ([ErrOutsideSupportWindow]).
//
// Um MAJOR fora de [MinMajor, MaxMajor] não tem reader — o run é inadmissível.
type SupportWindow struct {
	MinMajor int
	MaxMajor int
}

// DeclaredWindow é a JANELA DE SUPORTE DECLARADA da linha corrente do PlanDocument —
// `tecnica/18` §3.6.1: só o MAJOR 1 tem reader retido. É a FONTE ÚNICA da regra em
// código: antes de existir, o valor vivia só na prosa da §3.6.1 e numa variável de um
// ficheiro `_test.go`, pelo que documento e código podiam divergir sem nada avermelhar
// (um operador que avançasse o MinMajor num sítio e não no outro deprecava readers — e
// com eles a admissibilidade de runs aprovados — por omissão, quando a §3.6.1 exige que
// a deprecação seja EXPLÍCITA).
//
// NÃO é derivada de [plan.CurrentPlanVersion] de propósito: a janela é uma decisão de
// OPERAÇÃO (o que se retém, o que se depreca), não uma consequência do schema — o schema
// diz o que se emite, a janela diz o que ainda se lê. O que TEM de valer é a coerência
// mínima entre as duas, e essa é um teste: a linha corrente tem de estar coberta pela
// janela que este módulo declara.
//
// ALCANCE HONESTO: nenhuma composição de produção constrói hoje uma [Policy] a partir
// dela — o wiring do ciclo-de-vida do run é AOS-238. Até lá os dois gates de MAJOR são
// CAPACIDADE DE CONTRATO, não facto de fim-a-fim (registado em EPIC-20 §AOS-273).
var DeclaredWindow = SupportWindow{MinMajor: 1, MaxMajor: 1}

// Valid indica se a janela é coerente (Min <= Max). Uma janela inválida faz
// [NewPolicy] falhar fail-closed — nunca se admite um run contra um gate incoerente.
func (w SupportWindow) Valid() bool { return w.MinMajor <= w.MaxMajor }

// Covers indica se o MAJOR de v está dentro da janela (reader retido).
func (w SupportWindow) Covers(v plan.PlanVersion) bool {
	return v.Major >= w.MinMajor && v.Major <= w.MaxMajor
}

// Policy é o gate soberano de admissão de um `plan_version` congelado: a janela de
// suporte de MAJORs mais o conjunto de versões RETIRADAS. Construir com [NewPolicy].
// É imutável após a construção exceto por [Policy.Retire], que regista retiradas
// pontuais (dentro ou fora da janela).
type Policy struct {
	window  SupportWindow
	retired map[plan.PlanVersion]struct{}
}

// NewPolicy constrói uma Policy sobre a janela de suporte. Fail-closed: uma janela
// incoerente devolve [ErrInvalidWindow] — não se constrói um gate que admitiria
// contra um intervalo impossível.
func NewPolicy(window SupportWindow) (Policy, error) {
	if !window.Valid() {
		return Policy{}, fmt.Errorf("%w: [%d, %d]", ErrInvalidWindow, window.MinMajor, window.MaxMajor)
	}
	return Policy{window: window, retired: map[plan.PlanVersion]struct{}{}}, nil
}

// Retire marca uma versão EXACTA como retirada. Um run nessa versão passa a ser
// invalidado por [Policy.Admit] com [ErrRetired], mesmo que o seu MAJOR ainda esteja
// dentro da janela de suporte — a retirada pontual (ex.: um 1.2.0 com falha de
// segurança) precede a janela de MAJORs.
func (p Policy) Retire(v plan.PlanVersion) {
	p.retired[v] = struct{}{}
}

// Window devolve a janela de suporte configurada (para observabilidade/auditoria).
func (p Policy) Window() SupportWindow { return p.window }

// Admit é o gate FAIL-CLOSED sobre um `plan_version` congelado, aplicado ANTES de
// materializar e ANTES de reproduzir. Ordem deliberada:
//
//  1. RETIRADA primeiro — uma versão retirada é invalidada ([ErrRetired]) mesmo que
//     o MAJOR esteja na janela: a retirada é a decisão mais forte (re-plano).
//  2. JANELA depois — um MAJOR fora da janela é inadmissível ([ErrOutsideSupportWindow]).
//
// Sucesso (nil) exige que a versão NÃO esteja retirada E que o MAJOR esteja na
// janela. Nunca há um caminho de "admite por omissão".
func (p Policy) Admit(v plan.PlanVersion) error {
	if _, ok := p.retired[v]; ok {
		return fmt.Errorf("%w: %s", ErrRetired, v)
	}
	if !p.window.Covers(v) {
		return fmt.Errorf("%w: MAJOR %d fora de [%d, %d] (%s)",
			ErrOutsideSupportWindow, v.Major, p.window.MinMajor, p.window.MaxMajor, v)
	}
	return nil
}
