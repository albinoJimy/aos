package redaction

import (
	"regexp"
	"sort"
)

// Class é a CLASSE de PII de uma ocorrência detectada. É o eixo por que a política
// decide remover vs tokenizar e o rótulo que aparece no marcador [REDACTED:<classe>]
// e no token tok:<titular>:<classe>:<opaco>. As classes são um conjunto fechado e
// versionável; acrescentar uma classe é registar um [Detector] novo.
type Class string

const (
	// ClassEmail — endereço de correio electrónico (RFC-ish, heurístico).
	ClassEmail Class = "email"
	// ClassPhone — número de telefone (>=9 dígitos, com separadores comuns).
	ClassPhone Class = "phone"
	// ClassCreditCard — PAN de cartão de crédito (13–19 dígitos, Luhn válido).
	ClassCreditCard Class = "credit_card"
	// ClassIBAN — IBAN (país+dígitos de controlo+BBAN, mod-97 válido).
	ClassIBAN Class = "iban"
	// ClassIPv4 — endereço IPv4 pontuado (octetos 0–255).
	ClassIPv4 Class = "ip"
)

// Detector é um classificador determinístico de UMA classe de PII: uma regex
// compilada uma vez mais uma validação opcional (ex. Luhn, mod-97) que rejeita
// falsos-positivos sintácticos. A extensibilidade do motor é acrescentar Detectors
// — nunca ML nem dependências externas (AOS-091: detecção pura e testável).
//
// A prioridade desempata sobreposições: quando duas classes casam a mesma região do
// texto, vence a de MENOR Priority (ex. um PAN de cartão vence um telefone no mesmo
// span). Isto garante que a classe mais específica/sensível rotula a ocorrência.
type Detector struct {
	Class    Class
	Priority int
	re       *regexp.Regexp
	validate func(string) bool
}

// NewDetector constrói um [Detector] a partir de um padrão. Devolve erro se o padrão
// não compilar (fail-closed: um detector inválido nunca entra no motor). validate
// pode ser nil (a regex é suficiente).
func NewDetector(class Class, priority int, pattern string, validate func(string) bool) (Detector, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Detector{}, err
	}
	return Detector{Class: class, Priority: priority, re: re, validate: validate}, nil
}

// defaultDetectors devolve o conjunto de classificadores determinísticos de
// referência, por ordem de prioridade. Compilado uma vez por chamada de
// [NewEngine]; as regexes são estáticas e válidas por construção (mustDetector).
func defaultDetectors() []Detector {
	return []Detector{
		// Cartão primeiro: um PAN de 13–19 dígitos poderia também casar o telefone;
		// a validação Luhn separa PAN real de mera sequência de dígitos.
		mustDetector(ClassCreditCard, 0, `(?:\d[ -]?){13,19}`, validCreditCard),
		mustDetector(ClassIBAN, 1, `\b[A-Z]{2}\d{2}[A-Z0-9]{10,30}\b`, validIBAN),
		mustDetector(ClassEmail, 2, `[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`, nil),
		mustDetector(ClassIPv4, 3, `\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`, nil),
		// Telefone por último: só apanha o que os detectores mais específicos não
		// reclamaram. Exige 9–15 dígitos para não confundir com números curtos.
		mustDetector(ClassPhone, 4, `\+?\d[\d\s().\-]{7,}\d`, validPhone),
	}
}

// mustDetector é o construtor interno para padrões estáticos garantidamente válidos.
func mustDetector(class Class, priority int, pattern string, validate func(string) bool) Detector {
	d, err := NewDetector(class, priority, pattern, validate)
	if err != nil {
		panic("redaction: padrao de detector invalido: " + err.Error())
	}
	return d
}

// match é uma ocorrência de PII localizada num texto: a região [start,end), a
// classe e o valor em claro (usado para tokenizar). Interno ao motor.
type match struct {
	start, end int
	class      Class
	priority   int
	value      string
}

// scanString localiza todas as ocorrências de PII num texto, resolvendo
// sobreposições: colhe os matches de cada detector (descartando os que a validação
// rejeita), ordena-os e escolhe gulosamente um conjunto NÃO-sobreposto, preferindo,
// em empate de início, a maior região e a menor prioridade. O resultado vem ordenado
// por posição, pronto para uma reescrita de uma passagem.
func scanString(detectors []Detector, s string) []match {
	var all []match
	for _, d := range detectors {
		for _, loc := range d.re.FindAllStringIndex(s, -1) {
			val := s[loc[0]:loc[1]]
			if d.validate != nil && !d.validate(val) {
				continue
			}
			all = append(all, match{start: loc[0], end: loc[1], class: d.Class, priority: d.Priority, value: val})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].start != all[j].start {
			return all[i].start < all[j].start
		}
		if li, lj := all[i].end-all[i].start, all[j].end-all[j].start; li != lj {
			return li > lj // região maior primeiro
		}
		return all[i].priority < all[j].priority
	})
	picked := make([]match, 0, len(all))
	lastEnd := 0
	for _, m := range all {
		if m.start < lastEnd {
			continue // sobrepõe uma ocorrência já escolhida
		}
		picked = append(picked, m)
		lastEnd = m.end
	}
	return picked
}

// digitsOf extrai apenas os dígitos de s (descarta separadores).
func digitsOf(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return out
}

// validCreditCard aceita 13–19 dígitos que passem o algoritmo de Luhn.
func validCreditCard(s string) bool {
	d := digitsOf(s)
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	return luhn(d)
}

// luhn aplica o algoritmo de Luhn (checksum mod-10) a uma sequência de dígitos ASCII.
func luhn(d []byte) bool {
	sum := 0
	double := false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

// validPhone aceita 9–15 dígitos (limites E.164 aproximados). Evita confundir
// números curtos (quantidades, horas) com telefones.
func validPhone(s string) bool {
	n := len(digitsOf(s))
	return n >= 9 && n <= 15
}

// validIBAN aplica o mod-97 (ISO 13616): move os 4 primeiros caracteres para o fim,
// converte letras em 10–35 e verifica que o resto por 97 é 1. Rejeita cadeias
// alfanuméricas aleatórias que só casam a forma sintáctica.
func validIBAN(s string) bool {
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	rearranged := s[4:] + s[:4]
	rem := 0
	for i := 0; i < len(rearranged); i++ {
		c := rearranged[i]
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'A' && c <= 'Z':
			v = int(c-'A') + 10
		default:
			return false
		}
		if v >= 10 {
			rem = (rem*100 + v) % 97
		} else {
			rem = (rem*10 + v) % 97
		}
	}
	return rem == 1
}
