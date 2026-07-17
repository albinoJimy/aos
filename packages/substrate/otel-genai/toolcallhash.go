package otelgenai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ToolCallHashPrefix é o prefixo canónico do hash(tool+args) do repo: hex minúsculo
// de um SHA-256, precedido de "sha256:" (a mesma convenção de prompt_hash/prefix_hash).
const ToolCallHashPrefix = "sha256:"

// CanonicalToolCallHash calcula a âncora ESTÁVEL de action-dedup do span execute_tool
// (AOS-076/AOS-081): sha256(tool_id ‖ 0x00 ‖ argsCanónicos), em hex minúsculo com
// prefixo [ToolCallHashPrefix]. O separador nulo evita colisões de fronteira entre o
// tool_id e os args. É uma REFERÊNCIA por hash — os valores dos args NUNCA são gravados
// no span (content-capture por referência; o payload em claro é AOS-079).
//
// # Normalização canónica (o ponto central de AOS-081)
//
// Se args é um documento JSON válido, é RE-SERIALIZADO em forma canónica antes do hash:
// as chaves de objecto são ordenadas deterministicamente (recursivamente) e o espaço
// insignificante é removido. Assim dois payloads SEMANTICAMENTE EQUIVALENTES — as mesmas
// chaves por outra ordem, ou com espaçamento/indentação diferente — produzem o MESMO
// hash, eliminando o falso-negativo de dedup por formatação. A ORDEM DOS ARRAYS É
// PRESERVADA (é semântica): dois arrays com os mesmos elementos por ordens diferentes
// produzem hashes DIFERENTES. Os números são preservados no seu texto original (via
// [json.Decoder.UseNumber]) para não introduzir perda de precisão nem colisões — a
// normalização actua sobre ordenação de chaves e espaço, não sobre a grafia de números.
//
// # Fallback (compatibilidade)
//
// Se args NÃO é JSON válido (ou é vazio), o hash recai sobre os BYTES CRUS — idêntico ao
// comportamento pré-AOS-081. Assim tool calls com args opacos/não-JSON mantêm o mesmo
// hash de sempre (retro-compatível), enquanto os args JSON ganham a âncora estável.
//
// A função é determinista e livre de I/O — segura para replay.
func CanonicalToolCallHash(toolID string, args []byte) string {
	payload := args
	if canonical, ok := canonicalizeJSONArgs(args); ok {
		payload = canonical
	}
	h := sha256.New()
	_, _ = h.Write([]byte(toolID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	return ToolCallHashPrefix + hex.EncodeToString(h.Sum(nil))
}

// canonicalizeJSONArgs devolve a forma canónica de um documento JSON (chaves de objecto
// ordenadas recursivamente, espaço insignificante removido, ordem de arrays preservada)
// e ok=true se args era um ÚNICO documento JSON válido. Caso contrário devolve ok=false
// para o caller recair no hash dos bytes crus.
//
// A ordenação recursiva das chaves é obtida "de graça" pela [encoding/json]: o Marshal de
// um map[string]interface{} emite as chaves por ordem lexicográfica; os arrays ([]interface{})
// mantêm a ordem de decodificação. [json.Decoder.UseNumber] mantém os números como
// [json.Number] (texto), evitando a conversão para float64 (perda de precisão / colisões).
func canonicalizeJSONArgs(args []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	// Recusa conteúdo residual após o primeiro valor: "{}{}" ou "1 2" NÃO é um documento
	// JSON único — trata-se como não-JSON e recai no hash cru (determinismo do fallback).
	if dec.More() {
		return nil, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return out, true
}
