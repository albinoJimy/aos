package redaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Engine é o motor de redação/tokenização partilhado. Compõe os detectores
// (imutáveis, compilados uma vez) e a porta [KeySource] das chaves por-titular. É
// seguro para concorrência: não guarda estado mutável entre chamadas ([Engine.Redact]
// é uma função pura sobre o payload).
type Engine struct {
	detectors []Detector
	keys      KeySource
}

// NewEngine constrói o motor com os detectores de referência. keys pode ser nil
// quando a política só REMOVE (minimização pura); se a política tokenizar sem
// KeySource, [Engine.Redact] falha-fecha ([ErrTokenizeNoKeys]).
func NewEngine(keys KeySource) *Engine {
	return &Engine{detectors: defaultDetectors(), keys: keys}
}

// WithDetectors devolve um motor com um conjunto de detectores à medida (o mecanismo
// extensível de classes). Preserva a [KeySource]. Um conjunto vazio não detecta nada.
func (e *Engine) WithDetectors(detectors []Detector) *Engine {
	cp := make([]Detector, len(detectors))
	copy(cp, detectors)
	return &Engine{detectors: cp, keys: e.keys}
}

// marker devolve o substituto de minimização de uma classe: [REDACTED:<classe>]. Não
// revela comprimento nem qualquer fragmento do valor original.
func marker(c Class) string { return "[REDACTED:" + string(c) + "]" }

// Redact aplica a detecção e a política a um payload (string, ou JSON já
// desserializado: map[string]any, []any, aninhados) e devolve o payload TRATADO mais
// os [TokenRef] produzidos. É recursivo e puro: não muta o input (clona ao descer),
// percorre chaves de mapa por ordem determinística (tokens estáveis e refs
// reprodutíveis). Fail-closed: tokenização sem [KeySource] ⇒ [ErrTokenizeNoKeys].
//
// A parte NÃO-PII do payload mantém-se intacta (utilidade operacional): só as regiões
// detectadas são substituídas. A detecção alcança valores E chaves de mapa (uma chave
// pode carregar PII) e números nus (json.Number, float64, int/int64) — um PAN
// desserializado como número é tratado como o seu texto decimal.
func (e *Engine) Redact(payload any, subject string, policy Policy) (any, []TokenRef, error) {
	if policy.requiresTokenization() && e.keys == nil {
		return nil, nil, ErrTokenizeNoKeys
	}
	var refs []TokenRef
	out, err := e.redactAny(payload, subject, policy, &refs)
	if err != nil {
		return nil, nil, err
	}
	return out, refs, nil
}

// redactAny é a recursão sobre um valor JSON-shaped.
func (e *Engine) redactAny(v any, subject string, policy Policy, refs *[]TokenRef) (any, error) {
	switch t := v.(type) {
	case string:
		return e.redactString(t, subject, policy, refs)
	case json.Number:
		// Um número pode carregar PII (ex. um PAN serializado como número). Trata-se o
		// texto; se nada casar, devolve-se o json.Number intacto (utilidade preservada).
		s, changed, err := e.redactStringChanged(t.String(), subject, policy, refs)
		if err != nil {
			return nil, err
		}
		if !changed {
			return t, nil
		}
		return s, nil
	case float64:
		// A superfície pública Redact aceita `any`: um consumidor que desserialize com
		// json.Unmarshal padrão (sem UseNumber) obtém float64, e um PAN de 16 dígitos
		// (<2^53) é EXATAMENTE representável — sem este case vazaria em claro. Trata-se
		// o texto decimal; se nada casar, devolve-se o float64 intacto (utilidade).
		s, changed, err := e.redactStringChanged(numberToText(t), subject, policy, refs)
		if err != nil {
			return nil, err
		}
		if !changed {
			return t, nil
		}
		return s, nil
	case int:
		return e.redactIntLike(int64(t), t, subject, policy, refs)
	case int64:
		return e.redactIntLike(t, t, subject, policy, refs)
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // ordem determinística ⇒ refs reprodutíveis
		for _, k := range keys {
			rv, err := e.redactAny(t[k], subject, policy, refs)
			if err != nil {
				return nil, err
			}
			// A CHAVE também pode carregar PII (ex. um mapa indexado por email vindo de
			// um tool_result). Sem tratar a chave, a PII vazava em claro E o Scan era
			// cego a ela (falso verde). Redige-se a chave com a MESMA política.
			rk, err := e.redactString(k, subject, policy, refs)
			if err != nil {
				return nil, err
			}
			out[rk] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i := range t {
			rv, err := e.redactAny(t[i], subject, policy, refs)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		// bool, nil e outros escalares não-textuais/não-numéricos: nada a redigir.
		return v, nil
	}
}

// numberToText devolve a representação decimal de um float64 SEM notação científica
// (ex. 4111111111111111, não 4.111111111111111e+15), para que a detecção de PII veja a
// sequência de dígitos que também apareceria na serialização.
func numberToText(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// redactIntLike trata um inteiro nu (int/int64) passado via a superfície `any`: corre a
// detecção sobre o texto decimal e, se nada casar, devolve o valor ORIGINAL intacto
// (preserva o tipo/utilidade); se casar, devolve o texto tratado.
func (e *Engine) redactIntLike(n int64, orig any, subject string, policy Policy, refs *[]TokenRef) (any, error) {
	s, changed, err := e.redactStringChanged(strconv.FormatInt(n, 10), subject, policy, refs)
	if err != nil {
		return nil, err
	}
	if !changed {
		return orig, nil
	}
	return s, nil
}

// redactString substitui cada ocorrência de PII num texto pelo marcador (remove) ou
// pelo token (tokenize), numa única passagem da esquerda para a direita.
func (e *Engine) redactString(s, subject string, policy Policy, refs *[]TokenRef) (string, error) {
	out, _, err := e.redactStringChanged(s, subject, policy, refs)
	return out, err
}

// redactStringChanged é como redactString mas indica se houve alteração (para
// preservar tipos originais, ex. json.Number, quando nada casa).
func (e *Engine) redactStringChanged(s, subject string, policy Policy, refs *[]TokenRef) (string, bool, error) {
	matches := scanString(e.detectors, s)
	if len(matches) == 0 {
		return s, false, nil
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m.start])
		switch policy.action(m.class) {
		case ActionTokenize:
			token, ref, err := tokenize(e.keys, subject, m.class, m.value)
			if err != nil {
				return "", false, fmt.Errorf("tokenizar %s: %w", m.class, err)
			}
			b.WriteString(token)
			*refs = append(*refs, ref)
		default: // ActionRemove
			b.WriteString(marker(m.class))
		}
		last = m.end
	}
	b.WriteString(s[last:])
	return b.String(), true, nil
}

// RedactJSON aplica a redação a um payload JSON em bytes: desserializa preservando
// números (UseNumber), redige recursivamente e re-serializa. É a via para os
// consumidores que passam bytes (input de utilizador, tool result, ingestão de
// memória). Fail-closed: JSON inválido ⇒ erro (não se persiste um blob por tratar).
func (e *Engine) RedactJSON(raw []byte, subject string, policy Policy) ([]byte, []TokenRef, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, fmt.Errorf("redaction: payload JSON invalido (fail-closed): %w", err)
	}
	out, refs, err := e.Redact(v, subject, policy)
	if err != nil {
		return nil, nil, err
	}
	blob, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return blob, refs, nil
}

// RedactText aplica a redação a um texto simples (não-JSON), ex. um prompt de
// utilizador ou uma string de tool result. Devolve o texto tratado e os tokens.
func (e *Engine) RedactText(s, subject string, policy Policy) (string, []TokenRef, error) {
	if policy.requiresTokenization() && e.keys == nil {
		return "", nil, ErrTokenizeNoKeys
	}
	var refs []TokenRef
	out, err := e.redactString(s, subject, policy, &refs)
	if err != nil {
		return "", nil, err
	}
	return out, refs, nil
}

// Finding é uma ocorrência de PII encontrada por [Engine.Scan]: a classe e a região.
// NÃO expõe o valor em claro — é para verificação de AUSÊNCIA de PII (o teste falha
// se o scan do payload redigido devolver findings), não para exfiltrar o valor.
type Finding struct {
	Class Class
	Start int
	End   int
}

// Scan percorre um payload (string ou JSON-shaped) e devolve as ocorrências de PII em
// claro que os detectores encontram. Serve o critério "nenhuma PII em claro em
// spans/logs/audit": aplicado ao payload JÁ redigido tem de devolver vazio.
func (e *Engine) Scan(payload any) []Finding {
	var out []Finding
	e.scanAny(payload, &out)
	return out
}

// ScanText é o [Engine.Scan] sobre um texto simples.
func (e *Engine) ScanText(s string) []Finding {
	out := make([]Finding, 0)
	for _, m := range scanString(e.detectors, s) {
		out = append(out, Finding{Class: m.class, Start: m.start, End: m.end})
	}
	return out
}

func (e *Engine) scanAny(v any, out *[]Finding) {
	switch t := v.(type) {
	case string:
		for _, m := range scanString(e.detectors, t) {
			*out = append(*out, Finding{Class: m.class, Start: m.start, End: m.end})
		}
	case json.Number:
		for _, m := range scanString(e.detectors, t.String()) {
			*out = append(*out, Finding{Class: m.class, Start: m.start, End: m.end})
		}
	case float64:
		for _, m := range scanString(e.detectors, numberToText(t)) {
			*out = append(*out, Finding{Class: m.class, Start: m.start, End: m.end})
		}
	case int:
		for _, m := range scanString(e.detectors, strconv.FormatInt(int64(t), 10)) {
			*out = append(*out, Finding{Class: m.class, Start: m.start, End: m.end})
		}
	case int64:
		for _, m := range scanString(e.detectors, strconv.FormatInt(t, 10)) {
			*out = append(*out, Finding{Class: m.class, Start: m.start, End: m.end})
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// A chave também é rastreada — de outro modo o Scan seria cego a PII em
			// chaves (o falso verde do achado HIGH).
			for _, m := range scanString(e.detectors, k) {
				*out = append(*out, Finding{Class: m.class, Start: m.start, End: m.end})
			}
			e.scanAny(t[k], out)
		}
	case []any:
		for i := range t {
			e.scanAny(t[i], out)
		}
	}
}

// ScanJSON desserializa e faz [Engine.Scan]. Um JSON inválido é tratado como texto
// bruto (o scan é conservador: prefere procurar PII a ignorá-la).
func (e *Engine) ScanJSON(raw []byte) []Finding {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return e.ScanText(string(raw))
	}
	return e.Scan(v)
}
