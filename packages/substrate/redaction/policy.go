package redaction

import "strings"

// Action é a decisão de tratamento de uma classe de PII detectada.
type Action int

const (
	// ActionRemove — MINIMIZAÇÃO: a ocorrência é substituída por [REDACTED:<classe>]
	// e o valor original é descartado (não persistido, irrecuperável por desenho).
	ActionRemove Action = iota
	// ActionTokenize — a ocorrência é substituída por um token reversível
	// tok:<titular>:<classe>:<opaco> ancorado na chave por-titular. Reversível SÓ com
	// essa chave (prepara o crypto-shredding, AOS-093).
	ActionTokenize
)

// Policy decide, por classe, entre remover e tokenizar. É imutável após construída e
// versionável (Version) — molde das outras configs do repo. Fail-closed: uma classe
// conhecida sem decisão explícita faz [NewPolicy] falhar, e uma classe imprevista em
// tempo de execução é tratada como ActionRemove (a decisão mais conservadora).
type Policy struct {
	Version string
	actions map[Class]Action
}

// knownClasses é o conjunto de classes que uma política TEM de cobrir. Deriva dos
// detectores de referência; acrescentar uma classe obriga a cobri-la na política.
func knownClasses() []Class {
	return []Class{ClassEmail, ClassPhone, ClassCreditCard, ClassIBAN, ClassIPv4}
}

// NewPolicy constrói uma política a partir de um mapa classe→acção. Fail-closed:
// devolve [ErrPolicyIncomplete] se alguma classe conhecida não estiver coberta. O
// mapa é copiado (a política é imutável).
func NewPolicy(version string, actions map[Class]Action) (Policy, error) {
	cp := make(map[Class]Action, len(actions))
	for k, v := range actions {
		cp[k] = v
	}
	for _, c := range knownClasses() {
		if _, ok := cp[c]; !ok {
			return Policy{}, ErrPolicyIncomplete
		}
	}
	return Policy{Version: version, actions: cp}, nil
}

// PolicyFromConfig constrói uma política a partir de uma config textual
// (versionável/serializável), ex. {"email":"tokenize","phone":"remove",...}.
// Fail-closed: uma acção não-reconhecida devolve [ErrInvalidAction]; uma classe
// conhecida em falta devolve [ErrPolicyIncomplete].
func PolicyFromConfig(version string, cfg map[string]string) (Policy, error) {
	actions := make(map[Class]Action, len(cfg))
	for k, v := range cfg {
		a, err := parseAction(v)
		if err != nil {
			return Policy{}, err
		}
		actions[Class(k)] = a
	}
	return NewPolicy(version, actions)
}

// parseAction traduz a acção textual (case-insensitive) num [Action].
func parseAction(s string) (Action, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "remove", "redact", "minimize":
		return ActionRemove, nil
	case "tokenize", "token", "pseudonymize":
		return ActionTokenize, nil
	default:
		return 0, ErrInvalidAction
	}
}

// RemoveAllPolicy é a política de MINIMIZAÇÃO máxima: remove todas as classes. É o
// default mais seguro (nada é tokenizado, nada é retido) e não exige [KeySource].
func RemoveAllPolicy(version string) Policy {
	actions := make(map[Class]Action, len(knownClasses()))
	for _, c := range knownClasses() {
		actions[c] = ActionRemove
	}
	// knownClasses cobre tudo por construção: NewPolicy nunca falha aqui.
	p, _ := NewPolicy(version, actions)
	return p
}

// action devolve a decisão para uma classe. Fail-closed: uma classe não mapeada
// (imprevista) cai em ActionRemove — nunca liberta PII por omissão.
func (p Policy) action(c Class) Action {
	if a, ok := p.actions[c]; ok {
		return a
	}
	return ActionRemove
}

// requiresTokenization indica se alguma classe da política tokeniza — usado para
// validar (fail-closed) que uma [KeySource] foi injectada quando é preciso.
func (p Policy) requiresTokenization() bool {
	for _, a := range p.actions {
		if a == ActionTokenize {
			return true
		}
	}
	return false
}
