package durable

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// keyDelimiter é o separador canónico entre run_id e step_id na idempotency key.
// TEM de coincidir com o que o Event Store usa internamente (event.go:
// idempotencyKey = run_id + ":" + step_id), para que a chave que o downstream vê
// e a chave por que o ES deduplica sejam a MESMA string. AOS-014 não reinventa a
// dedup do ES — ancora-se nela.
const keyDelimiter = ":"

// IdempotencyKey deriva a chave de idempotência canónica de um passo lógico a
// partir de (run_id, step_id). É PURA e DETERMINÍSTICA: a mesma entrada produz
// sempre a mesma chave e nunca tem efeitos colaterais.
//
// # Construção exacta
//
//	key = run_id + ":" + step_id
//
// É deliberadamente a MESMA forma que o Event Store calcula internamente
// (eventstore/event.go). Assim, a chave que uma activity propaga ao downstream
// (AOS-021) é byte-idêntica à chave por que o Event Store deduplica o evento de
// TURNO (turn.recorded) e o header downstream.
//
// # "Espaço único" é conceptual, não literal para o write do ledger
//
// A dedup do ES é por idempotency_key = run_id + ":" + step_id. Há, por passo, ATÉ
// TRÊS chaves de ES distintas — o "espaço único" refere-se à convenção de derivação,
// não a uma única string:
//   - run_id:step_id — casa o turn.recorded (AOS-013) com o header de idempotência
//     do downstream. É esta a chave que a activity propaga.
//   - run_id:step_id-tool-n — o sub-passo (n-ésima tool call do turno), via
//     [StepSequencer.SubStepID], para não colidir com o turno homónimo.
//   - run_id:ledger-step_id — o REGISTO DURÁVEL do ledger ([StepLedger]), namespaced
//     com o prefixo "ledger-". É DISTINTA da chave que o downstream vê: "commitar o
//     ledger" NÃO dá dedup do ES na MESMA chave que o downstream deduplica — são dois
//     domínios de dedup separados que apenas partilham parcialmente a string.
//
// # Injectividade e espaço de colisão
//
// A função é INJECTIVA no seu domínio válido. A única forma de duas entradas
// distintas colidirem seria por deslocamento do delimitador — p.ex. ("a","bc") e
// ("ab","c") produziriam ambas a string "a:bc" vs "ab:c" se o ':' fosse livre nos
// inputs. Para fechar essa colisão, IdempotencyKey REJEITA qualquer run_id ou
// step_id que contenha ':' (e qualquer input vazio). Com o delimitador proibido
// nos inputs, cada chave tem exactamente UMA decomposição (split no único ':') —
// a função é uma bijecção entre pares válidos (run_id, step_id) e chaves, e
// [SplitKey] é a sua inversa exacta.
//
// Devolve ([ErrEmptyRunID] | [ErrEmptyStepID] | [ErrDelimiterInInput]) e a string
// vazia se algum input for inválido — nunca uma chave silenciosamente ambígua.
func IdempotencyKey(runID, stepID string) (string, error) {
	if runID == "" {
		return "", ErrEmptyRunID
	}
	if stepID == "" {
		return "", ErrEmptyStepID
	}
	if strings.Contains(runID, keyDelimiter) || strings.Contains(stepID, keyDelimiter) {
		return "", ErrDelimiterInInput
	}
	return runID + keyDelimiter + stepID, nil
}

// SplitKey é a inversa exacta de [IdempotencyKey]: decompõe uma chave canónica no
// par (run_id, step_id) que a gerou. Como IdempotencyKey proíbe ':' nos inputs, a
// chave tem exactamente um ':' e a decomposição é única e sem ambiguidade.
//
// Devolve [ErrMalformedKey] se a chave não tiver exactamente um ':' com ambos os
// lados não-vazios (i.e. não for produto de IdempotencyKey). É usada pelo
// [StepLedger] para derivar o envelope do Event Store (stream_id/step_id) a partir
// da chave opaca que Apply recebe.
func SplitKey(key string) (runID, stepID string, err error) {
	i := strings.Index(key, keyDelimiter)
	if i <= 0 || i != strings.LastIndex(key, keyDelimiter) || i == len(key)-1 {
		return "", "", ErrMalformedKey
	}
	return key[:i], key[i+1:], nil
}

// OpaqueKey devolve uma forma OPACA e estável da chave — o SHA-256 hex de
// [IdempotencyKey](run_id, step_id) — adequada a logs, spans e contadores de
// observabilidade onde a chave em claro poderia revelar identificadores sensíveis.
// É determinística (mesmo par ⇒ mesmo hash) mas NÃO é a chave de deduplicação: a
// dedup usa sempre a forma canónica que casa com o Event Store. Propaga o erro de
// validação de [IdempotencyKey].
func OpaqueKey(runID, stepID string) (string, error) {
	key, err := IdempotencyKey(runID, stepID)
	if err != nil {
		return "", err
	}
	return HashKey(key), nil
}

// HashKey devolve o SHA-256 hex de uma chave já construída. Útil para derivar a
// forma opaca a partir da chave opaca que o [StepLedger] recebe, sem re-split.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
