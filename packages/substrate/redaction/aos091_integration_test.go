package redaction

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- AC: aplicação CONSISTENTE nas três vias com o MESMO motor ---

func TestThreeIngestionPathsSameEngine(t *testing.T) {
	e := NewEngine(seqKeySource())
	ing := NewIngestor(e, tokenizeAllPolicy(t))

	payload := map[string]any{"corpo": "contacto " + synthEmail + " ip " + synthIPv4}

	user, err := ing.UserInput(subject, deepCopy(payload))
	if err != nil {
		t.Fatalf("UserInput: %v", err)
	}
	tool, err := ing.ToolResult(subject, deepCopy(payload))
	if err != nil {
		t.Fatalf("ToolResult: %v", err)
	}
	mem, err := ing.Memory(subject, deepCopy(payload))
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}

	for _, got := range []Ingested{user, tool, mem} {
		if f := e.Scan(got.Payload); len(f) != 0 {
			t.Fatalf("via %s deixou PII em claro: %+v", got.Source, f)
		}
	}
	if user.Source != SourceUserInput || tool.Source != SourceToolResult || mem.Source != SourceMemory {
		t.Fatalf("vias mal rotuladas: %s %s %s", user.Source, tool.Source, mem.Source)
	}
	// MESMO motor + MESMO titular ⇒ o mesmo valor produz o mesmo tratamento nas 3 vias.
	ub, _ := json.Marshal(user.Payload)
	tb, _ := json.Marshal(tool.Payload)
	mb, _ := json.Marshal(mem.Payload)
	if string(ub) != string(tb) || string(tb) != string(mb) {
		t.Fatalf("vias divergiram: %s | %s | %s", ub, tb, mb)
	}
}

// --- AC: integração com a obrigação redact do PEP (AOS-087) por COMPOSIÇÃO ---
//
// enforceRedactPII (reference-monitor/obligations.go) redige campos NOMEADOS. Este
// motor acrescenta a DETECÇÃO por padrão. Simula-se a redação por-campo do RM e
// compõe-se com o motor: o campo nomeado é redigido pelo RM; a PII num campo
// NÃO-nomeado é apanhada pela detecção — provando que os dois se compõem.
func TestComposesWithFieldNameRedaction(t *testing.T) {
	e := NewEngine(seqKeySource())
	pol := tokenizeAllPolicy(t)

	raw := map[string]any{
		"ssn":      "123-45-6789",                // campo NOMEADO pela obrigação
		"mensagem": "escreve para " + synthEmail, // PII num campo NÃO-nomeado
	}
	// Passo 1: redação por-campo do RM (obrigação redact_pii NOMEIA "ssn").
	fieldRedactByName(raw, map[string]struct{}{"ssn": {}})
	if raw["ssn"] != "[REDACTED]" {
		t.Fatalf("campo nomeado nao foi redigido pelo RM: %v", raw["ssn"])
	}
	// Passo 2: o motor de detecção apanha a PII do campo não-nomeado.
	out, refs, err := e.Redact(raw, subject, pol)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if f := e.Scan(out); len(f) != 0 {
		t.Fatalf("deteccao nao completou a redacao por-campo: %+v", f)
	}
	if len(refs) != 1 {
		t.Fatalf("email do campo nao-nomeado devia ser tokenizado: %+v", refs)
	}
}

// fieldRedactByName espelha a semântica de redactValue de AOS-087 (redação por NOME
// de campo), para provar a composição sem importar o kernel.
func fieldRedactByName(v any, fields map[string]struct{}) {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if _, ok := fields[k]; ok {
				t[k] = "[REDACTED]"
				continue
			}
			fieldRedactByName(t[k], fields)
		}
	case []any:
		for i := range t {
			fieldRedactByName(t[i], fields)
		}
	}
}

// --- RedactJSON: via de bytes preserva números não-PII e trata os que casam ---

func TestRedactJSONPreservesNonPII(t *testing.T) {
	e := NewEngine(seqKeySource())
	pol := tokenizeAllPolicy(t)
	raw := []byte(`{"qtd":4,"preco":19.9,"email":"` + synthEmail + `","nota":"ok"}`)
	out, refs, err := e.RedactJSON(raw, subject, pol)
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("out invalido: %v", err)
	}
	if m["qtd"].(json.Number).String() != "4" || m["nota"] != "ok" {
		t.Fatalf("parte nao-PII alterada: %v", m)
	}
	if len(refs) != 1 || !strings.HasPrefix(m["email"].(string), "tok:") {
		t.Fatalf("email nao tokenizado: %v / %+v", m["email"], refs)
	}
}

func TestRedactJSONInvalidFailsClosed(t *testing.T) {
	e := NewEngine(seqKeySource())
	if _, _, err := e.RedactJSON([]byte("{nao-json"), subject, tokenizeAllPolicy(t)); err == nil {
		t.Fatalf("JSON invalido devia falhar-fechar")
	}
}

// --- Política: fail-closed ---

func TestPolicyIncompleteFailsClosed(t *testing.T) {
	_, err := NewPolicy("v1", map[Class]Action{ClassEmail: ActionRemove})
	if !errors.Is(err, ErrPolicyIncomplete) {
		t.Fatalf("politica incompleta devia dar ErrPolicyIncomplete, deu %v", err)
	}
}

func TestPolicyFromConfig(t *testing.T) {
	cfg := map[string]string{
		"email": "tokenize", "phone": "remove", "credit_card": "remove",
		"iban": "tokenize", "ip": "remove",
	}
	if _, err := PolicyFromConfig("v2", cfg); err != nil {
		t.Fatalf("PolicyFromConfig valida: %v", err)
	}
	cfg["email"] = "encrypt-somehow" // acção inválida
	if _, err := PolicyFromConfig("v2", cfg); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("acção invalida devia dar ErrInvalidAction, deu %v", err)
	}
}

// --- Fail-closed: tokenizar sem KeySource / titular vazio ---

func TestTokenizeWithoutKeySourceFailsClosed(t *testing.T) {
	e := NewEngine(nil) // sem KeySource
	pol := tokenizeAllPolicy(t)
	if _, _, err := e.RedactText("email "+synthEmail, subject, pol); !errors.Is(err, ErrTokenizeNoKeys) {
		t.Fatalf("tokenizar sem KeySource devia dar ErrTokenizeNoKeys, deu %v", err)
	}
	if _, _, err := e.Redact("x", subject, pol); !errors.Is(err, ErrTokenizeNoKeys) {
		t.Fatalf("Redact sem KeySource devia falhar-fechar, deu %v", err)
	}
}

func TestTokenizeEmptySubjectFailsClosed(t *testing.T) {
	e := NewEngine(seqKeySource())
	pol := tokenizeAllPolicy(t)
	_, _, err := e.RedactText("email "+synthEmail, "  ", pol)
	if err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("titular vazio devia falhar-fechar, deu %v", err)
	}
}

// --- RemoveAllPolicy: minimização pura, sem KeySource ---

func TestRemoveAllPolicyNoKeysNeeded(t *testing.T) {
	e := NewEngine(nil)
	pol := RemoveAllPolicy("v1")
	out, refs, err := e.RedactText("email "+synthEmail+" card "+synthCard, "", pol)
	if err != nil {
		t.Fatalf("RemoveAll sem KeySource: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("RemoveAll nao tokeniza: %+v", refs)
	}
	if strings.Contains(out, "example.com") || strings.Contains(out, "4111") {
		t.Fatalf("PII nao minimizada: %q", out)
	}
}

// --- Extensibilidade: detector à medida ---

func TestCustomDetector(t *testing.T) {
	d, err := NewDetector("badge", 10, `BADGE-\d{4}`, nil)
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	e := NewEngine(nil).WithDetectors([]Detector{d})
	f := e.ScanText("cracha BADGE-0007 emitido")
	if len(f) != 1 || f[0].Class != "badge" {
		t.Fatalf("detector a medida nao casou: %+v", f)
	}
}

func TestNewDetectorInvalidPattern(t *testing.T) {
	if _, err := NewDetector("x", 0, "(", nil); err == nil {
		t.Fatalf("padrao invalido devia falhar")
	}
}

// deepCopy clona um payload JSON-shaped (para reusar o mesmo input em várias vias).
func deepCopy(v any) any {
	b, _ := json.Marshal(v)
	var out any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out
}
