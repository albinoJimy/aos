package redaction

import "errors"

var (
	// ErrPolicyIncomplete — a política não cobre uma classe conhecida (fail-closed:
	// uma classe detectável sem decisão remove/tokeniza não pode ser libertada).
	ErrPolicyIncomplete = errors.New("redaction: politica incompleta (classe sem accao remove/tokenize)")
	// ErrInvalidAction — a config nomeia uma acção inválida (nem remove nem tokenize).
	ErrInvalidAction = errors.New("redaction: accao de politica invalida")
	// ErrTokenizeNoKeys — a política exige tokenização mas nenhuma [KeySource] foi
	// injectada no motor (fail-closed: não há como referenciar a chave por titular).
	ErrTokenizeNoKeys = errors.New("redaction: tokenizacao exigida sem KeySource injectada")
	// ErrKeySize — a KeySource devolveu uma chave que não é AES-256 (32 bytes).
	ErrKeySize = errors.New("redaction: chave do titular tem de ter 32 bytes (AES-256)")
	// ErrEmptySubject — tokenização sem titular: o token ficaria sem âncora de chave.
	ErrEmptySubject = errors.New("redaction: tokenizacao exige um titular (subject) nao-vazio")
)
