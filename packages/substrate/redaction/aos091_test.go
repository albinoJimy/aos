package redaction

import (
	"encoding/json"
	"strings"
	"testing"
)

// PII SINTÉTICA (nunca PII real): valores canónicos de teste, válidos nos algoritmos
// determinísticos (Luhn, mod-97, E.164).
const (
	synthEmail = "alice@example.com"
	synthPhone = "+351 912 345 678"       // 12 dígitos
	synthCard  = "4111 1111 1111 1111"    // Visa de teste, Luhn válido
	synthIBAN  = "DE89370400440532013000" // IBAN de teste, mod-97 válido
	synthIPv4  = "192.168.1.100"
	subject    = "user-42"
)

// seqKeySource devolve uma KeySource determinística: cada titular novo recebe uma
// chave distinta (contador), reutilizada nas chamadas seguintes.
func seqKeySource() *InMemoryKeySource {
	var ctr byte
	return NewInMemoryKeySource(func(p []byte) error {
		ctr++
		for i := range p {
			p[i] = byte(i) + ctr
		}
		return nil
	})
}

func tokenizeAllPolicy(t *testing.T) Policy {
	t.Helper()
	p, err := NewPolicy("v1", map[Class]Action{
		ClassEmail:      ActionTokenize,
		ClassPhone:      ActionTokenize,
		ClassCreditCard: ActionRemove, // PAN nunca necessário ⇒ minimizado
		ClassIBAN:       ActionTokenize,
		ClassIPv4:       ActionRemove,
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return p
}

// --- Detecção: classificadores determinísticos ---

func TestDetectorsMatchSyntheticPII(t *testing.T) {
	e := NewEngine(nil)
	cases := []struct {
		name string
		in   string
		want Class
	}{
		{"email", synthEmail, ClassEmail},
		{"phone", synthPhone, ClassPhone},
		{"card", synthCard, ClassCreditCard},
		{"iban", synthIBAN, ClassIBAN},
		{"ipv4", synthIPv4, ClassIPv4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := e.ScanText(c.in)
			if len(f) != 1 || f[0].Class != c.want {
				t.Fatalf("scan %q = %+v, quer 1x %s", c.in, f, c.want)
			}
		})
	}
}

func TestLuhnRejectsInvalidCard(t *testing.T) {
	e := NewEngine(nil)
	// 16 dígitos que FALHAM Luhn ⇒ não é cartão. Também não tem 9..15 → não telefone.
	if f := e.ScanText("4111 1111 1111 1112"); len(f) != 0 {
		t.Fatalf("numero invalido nao devia casar cartao: %+v", f)
	}
}

func TestIBANRejectsInvalidChecksum(t *testing.T) {
	e := NewEngine(nil)
	if f := e.ScanText("DE00370400440532013000"); len(f) != 0 {
		t.Fatalf("IBAN com checksum invalido nao devia casar: %+v", f)
	}
}

func TestPhoneBounds(t *testing.T) {
	e := NewEngine(nil)
	if f := e.ScanText("chamar 123"); len(f) != 0 {
		t.Fatalf("numero curto nao e telefone: %+v", f)
	}
}

// --- AC: redação/tokenização na ingestão ANTES de persistir ---

func TestRedactTokenizesAndRemoves(t *testing.T) {
	e := NewEngine(seqKeySource())
	pol := tokenizeAllPolicy(t)
	in := "contacto " + synthEmail + " tel " + synthPhone + " cartao " + synthCard
	out, refs, err := e.RedactText(in, subject, pol)
	if err != nil {
		t.Fatalf("RedactText: %v", err)
	}
	if strings.Contains(out, synthEmail) || strings.Contains(out, synthPhone) {
		t.Fatalf("PII tokenizada ainda em claro: %q", out)
	}
	if strings.Contains(out, "4111") {
		t.Fatalf("PAN removido ainda em claro: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:credit_card]") {
		t.Fatalf("PAN devia ser minimizado: %q", out)
	}
	if !strings.Contains(out, "tok:"+subject+":email:") {
		t.Fatalf("email devia ser tokenizado: %q", out)
	}
	// email + phone tokenizados ⇒ 2 refs; cartão removido ⇒ 0 ref.
	if len(refs) != 2 {
		t.Fatalf("esperava 2 TokenRef, obteve %d: %+v", len(refs), refs)
	}
	for _, r := range refs {
		if r.KeyRef != KeyRefFor(subject) {
			t.Fatalf("TokenRef.KeyRef errada: %+v", r)
		}
	}
}

// --- AC: ausência de PII em claro (scan do payload redigido) ---

func TestNoPIIAfterRedaction(t *testing.T) {
	e := NewEngine(seqKeySource())
	pol := tokenizeAllPolicy(t)
	payload := map[string]any{
		"nota":  "contacto " + synthEmail,
		"linha": []any{synthPhone, "texto neutro", synthIBAN},
		"rede":  synthIPv4,
	}
	out, _, err := e.Redact(payload, subject, pol)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if f := e.Scan(out); len(f) != 0 {
		t.Fatalf("scan do payload redigido encontrou PII em claro: %+v", f)
	}
	// E também sobre a serialização (o que iria para span/audit).
	blob, _ := json.Marshal(out)
	if f := e.ScanJSON(blob); len(f) != 0 {
		t.Fatalf("scan do JSON redigido encontrou PII: %+v (%s)", f, blob)
	}
}

// --- Regressão HIGH: PII em CHAVE de mapa é redigida e o Scan não é cego ---

func TestPIIInMapKeyRedactedAllPaths(t *testing.T) {
	e := NewEngine(seqKeySource())
	ing := NewIngestor(e, tokenizeAllPolicy(t))
	// Um tool_result pode devolver um mapa indexado por email (chave = PII).
	mk := func() map[string]any {
		return map[string]any{synthEmail: "logged in", "nota": "ok"}
	}

	paths := []struct {
		name string
		run  func() (Ingested, error)
	}{
		{"user", func() (Ingested, error) { return ing.UserInput(subject, mk()) }},
		{"tool", func() (Ingested, error) { return ing.ToolResult(subject, mk()) }},
		{"memory", func() (Ingested, error) { return ing.Memory(subject, mk()) }},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			got, err := p.run()
			if err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			// A chave em claro não pode sobreviver no payload tratado.
			m := got.Payload.(map[string]any)
			if _, leaked := m[synthEmail]; leaked {
				t.Fatalf("%s: PII em chave sobreviveu: %+v", p.name, m)
			}
			// A parte não-PII mantém-se (utilidade).
			if m["nota"] != "ok" {
				t.Fatalf("%s: parte nao-PII alterada: %+v", p.name, m)
			}
			// O Scan já não é cego à PII em chave: sobre o payload tratado, 0 findings.
			if f := e.Scan(got.Payload); len(f) != 0 {
				t.Fatalf("%s: scan do payload redigido encontrou PII: %+v", p.name, f)
			}
			// E o Scan do payload CRU (com a chave em claro) DEVE encontrá-la (não-cego).
			if f := e.Scan(mk()); len(f) == 0 {
				t.Fatalf("%s: scan cego a PII em chave (falso verde)", p.name)
			}
		})
	}
}

// --- Regressão MEDIUM: número nu (float64/int) via a superfície `any` é tratado ---

func TestBareNumberPANRedactedViaAny(t *testing.T) {
	e := NewEngine(seqKeySource())
	pol := tokenizeAllPolicy(t) // credit_card ⇒ ActionRemove

	// (1) float64 — o que json.Unmarshal padrão (sem UseNumber) produz.
	var viaFloat any
	if err := json.Unmarshal([]byte(`{"pan":4111111111111111}`), &viaFloat); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	outF, _, err := e.Redact(viaFloat, subject, pol)
	if err != nil {
		t.Fatalf("Redact float: %v", err)
	}
	if b, _ := json.Marshal(outF); strings.Contains(string(b), "4111111111111111") {
		t.Fatalf("PAN float64 vazou em claro: %s", b)
	}
	if f := e.Scan(outF); len(f) != 0 {
		t.Fatalf("scan encontrou PAN float64 apos redacao: %+v", f)
	}

	// (2) int nu construído em Go e passado via `any`.
	outI, _, err := e.Redact(map[string]any{"pan": 4111111111111111}, subject, pol)
	if err != nil {
		t.Fatalf("Redact int: %v", err)
	}
	if b, _ := json.Marshal(outI); strings.Contains(string(b), "4111111111111111") {
		t.Fatalf("PAN int vazou em claro: %s", b)
	}

	// (3) número não-PII é preservado INTACTO (utilidade): tipo e valor mantêm-se.
	outN, _, err := e.Redact(map[string]any{"qtd": 4.0, "preco": 19.9}, subject, pol)
	if err != nil {
		t.Fatalf("Redact non-PII: %v", err)
	}
	mn := outN.(map[string]any)
	if mn["qtd"] != 4.0 || mn["preco"] != 19.9 {
		t.Fatalf("numero nao-PII alterado: %+v", mn)
	}
}

// --- AC: utilidade operacional + estabilidade do token ---

func TestUtilityPreservedAndTokenStable(t *testing.T) {
	e := NewEngine(seqKeySource())
	pol := tokenizeAllPolicy(t)
	in := "reservar mesa para 4 pessoas as 20h; contacto " + synthEmail
	out1, _, err := e.RedactText(in, subject, pol)
	if err != nil {
		t.Fatalf("RedactText: %v", err)
	}
	// Parte não-PII intacta (utilidade/reprodutibilidade).
	if !strings.Contains(out1, "reservar mesa para 4 pessoas as 20h") {
		t.Fatalf("parte nao-PII alterada: %q", out1)
	}
	// Token estável para o mesmo valor+titular.
	out2, _, err := e.RedactText(in, subject, pol)
	if err != nil {
		t.Fatalf("RedactText 2: %v", err)
	}
	if out1 != out2 {
		t.Fatalf("token instavel: %q != %q", out1, out2)
	}
	// Titular diferente ⇒ token diferente (chave distinta).
	out3, _, err := e.RedactText(in, "outro-titular", pol)
	if err != nil {
		t.Fatalf("RedactText 3: %v", err)
	}
	if out3 == out1 {
		t.Fatalf("token nao devia coincidir entre titulares")
	}
}

// --- AC/AOS-093: token reversível SÓ com a chave; irresolúvel após shred ---

func TestTokenResolvableThenShredded(t *testing.T) {
	ks := seqKeySource()
	e := NewEngine(ks)
	pol := tokenizeAllPolicy(t)
	out, refs, err := e.RedactText("email "+synthEmail, subject, pol)
	if err != nil {
		t.Fatalf("RedactText: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("esperava 1 token, obteve %d", len(refs))
	}
	token := refs[0].Token
	key, ok := ks.KeyByRef(refs[0].KeyRef)
	if !ok {
		t.Fatalf("chave devia existir antes do shred")
	}
	// Antes do shred: resolve para o valor original.
	got, ok := Resolve(token, key)
	if !ok || got != synthEmail {
		t.Fatalf("Resolve antes do shred = (%q,%v), quer (%q,true)", got, ok, synthEmail)
	}
	// Chave errada ⇒ irresolúvel (binding por AAD + GCM).
	wrong := make([]byte, keySize)
	for i := range wrong {
		wrong[i] = 0xAA
	}
	if _, ok := Resolve(token, wrong); ok {
		t.Fatalf("Resolve com chave errada devia falhar")
	}
	// Crypto-shredding: destruída a chave, o token fica irresolúvel para sempre.
	shredded, existed := ks.Shred(subject)
	if !existed {
		t.Fatalf("Shred devia encontrar a chave")
	}
	if _, ok := ks.KeyByRef(refs[0].KeyRef); ok {
		t.Fatalf("chave nao devia existir apos shred")
	}
	// A chave outrora resolvia; o vault já não a fornece (modela o shred real).
	if _, ok := Resolve(token, shredded); !ok {
		t.Fatalf("a chave destruida ainda decifra (esperado); o shred remove-a do vault")
	}
	_ = out
}

// --- AOS-188: PII de exemplo não persiste em eventos/spans/audits ---
//
// O motor é folha; o teste simula as três superfícies de persistência (Event Store,
// span attributes, audit record body) serializando o payload redigido como JSON e
// varrendo-o por PII em claro. Se o motor falhar, a PII persistiria nesses destinos.
func TestNoPIIPersistsInEventSpanAuditSerialization(t *testing.T) {
	e := NewEngine(seqKeySource())
	ing := NewIngestor(e, tokenizeAllPolicy(t))

	// Payload com PII em valor E em chave de mapa (regressão HIGH anterior).
	raw := map[string]any{
		synthEmail:       "chave-PII",
		"corpo":          "contacto " + synthPhone + " cartao " + synthCard,
		"iban":           synthIBAN,
		"rede":           synthIPv4,
		"nested":         map[string]any{"email": synthEmail},
		"lista":          []any{synthPhone, "texto neutro"},
		"numero_nao_pii": 42,
		"float_nao_pii":  19.9,
	}

	paths := []struct {
		name string
		src  Source
		run  func(any) (Ingested, error)
	}{
		{"event_store", SourceUserInput, func(p any) (Ingested, error) { return ing.UserInput(subject, p) }},
		{"span", SourceToolResult, func(p any) (Ingested, error) { return ing.ToolResult(subject, p) }},
		{"audit", SourceMemory, func(p any) (Ingested, error) { return ing.Memory(subject, p) }},
	}

	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			got, err := p.run(deepCopy(raw))
			if err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			if got.Source != p.src {
				t.Fatalf("%s: source %s != %s", p.name, got.Source, p.src)
			}
			// (1) O payload estruturado redigido não tem PII em claro.
			if f := e.Scan(got.Payload); len(f) != 0 {
				t.Fatalf("%s: payload redigido ainda tem PII: %+v", p.name, f)
			}
			// (2) A serialização JSON (o que efetivamente seria persistido/em span/audit)
			// também não tem PII em claro.
			blob, err := json.Marshal(got.Payload)
			if err != nil {
				t.Fatalf("%s: marshal: %v", p.name, err)
			}
			if f := e.ScanJSON(blob); len(f) != 0 {
				t.Fatalf("%s: JSON serializado ainda tem PII: %+v\n%s", p.name, f, blob)
			}
			// (3) Parte não-PII preservada (utilidade). Note que deepCopy usa json.UseNumber;
			// verificamos os textos decimais para não depender do tipo exacto.
			m := got.Payload.(map[string]any)
			nn, okN := m["numero_nao_pii"].(json.Number)
			fn, okF := m["float_nao_pii"].(json.Number)
			if !okN || nn.String() != "42" || !okF || fn.String() != "19.9" {
				t.Fatalf("%s: parte nao-PII alterada: %+v", p.name, m)
			}
		})
	}
}
