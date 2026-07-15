package semver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
)

func ctr(egress domain.EgressClass, scopes []string, in, out string) domain.Contract {
	return domain.Contract{
		InputSchema:      json.RawMessage(in),
		OutputSchema:     json.RawMessage(out),
		CredentialScopes: scopes,
		Egress:           egress,
	}
}

const (
	objA    = `{"type":"object","properties":{"a":{"type":"string"}}}`
	objAReq = `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`
	objAB   = `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}}}`
	objABrq = `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}},"required":["a","b"]}`
	objANum = `{"type":"object","properties":{"a":{"type":"number"}}}`

	// Schemas ANINHADOS e CONSTRANGIDOS: exercitam a classificação estrutural
	// recursiva e por keyword de constrangimento (AOS-052-Q1). Uma análise só-de-topo
	// classificava todas estas quebras como MINOR.
	nestObj    = `{"type":"object","properties":{"foo":{"type":"object","properties":{"bar":{"type":"string"}}}}}`
	nestObjNum = `{"type":"object","properties":{"foo":{"type":"object","properties":{"bar":{"type":"number"}}}}}`
	nestObjDel = `{"type":"object","properties":{"foo":{"type":"object","properties":{}}}}`
	objAEnum   = `{"type":"object","properties":{"a":{"type":"string","enum":["x","y"]}}}`
	objAMinLen = `{"type":"object","properties":{"a":{"type":"string","minLength":10}}}`
	objAClosed = `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`
)

// TestClassifyContract cobre o núcleo: a classificação da mudança de contrato →
// bump exigido, ancorada a cada eixo do contrato público (schema I/O, scopes,
// egress) mais a quebra semântica declarada.
func TestClassifyContract(t *testing.T) {
	t.Parallel()
	base := ctr(domain.EgressInternal, []string{"vault:db.read"}, objA, objA)

	cases := []struct {
		name      string
		old, new  domain.Contract
		semBroken bool
		want      domain.ChangeKind
	}{
		{
			name: "contrato identico -> none",
			old:  base, new: base,
			want: domain.ChangeNone,
		},
		{
			name: "input opcional adicionado -> minor",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objAB, objA),
			want: domain.ChangeMinor,
		},
		{
			name: "input obrigatorio adicionado -> major",
			old:  ctr(domain.EgressInternal, nil, objAReq, objA),
			new:  ctr(domain.EgressInternal, nil, objABrq, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "input removido -> major",
			old:  ctr(domain.EgressInternal, nil, objAB, objA),
			new:  ctr(domain.EgressInternal, nil, objA, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "input tipo alterado -> major",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objANum, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "output adicionado -> minor",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objA, objAB),
			want: domain.ChangeMinor,
		},
		{
			name: "output removido -> major",
			old:  ctr(domain.EgressInternal, nil, objA, objAB),
			new:  ctr(domain.EgressInternal, nil, objA, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "scopes acrescentados -> major",
			old:  ctr(domain.EgressInternal, []string{"vault:db.read"}, objA, objA),
			new:  ctr(domain.EgressInternal, []string{"vault:db.read", "vault:db.write"}, objA, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "scopes removidos -> minor",
			old:  ctr(domain.EgressInternal, []string{"vault:db.read", "vault:db.write"}, objA, objA),
			new:  ctr(domain.EgressInternal, []string{"vault:db.read"}, objA, objA),
			want: domain.ChangeMinor,
		},
		{
			name: "egress elevado -> major",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressExternal, nil, objA, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "egress reduzido -> minor",
			old:  ctr(domain.EgressExternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objA, objA),
			want: domain.ChangeMinor,
		},
		{
			name: "semantica quebrada declarada (contrato igual) -> major",
			old:  base, new: base, semBroken: true,
			want: domain.ChangeMajor,
		},
		{
			name: "precedencia: minor+major -> major",
			old:  ctr(domain.EgressInternal, []string{"a"}, objA, objA),
			new:  ctr(domain.EgressExternal, nil, objAB, objA), // egress elevado (major) + scope removido (minor) + input opcional (minor)
			want: domain.ChangeMajor,
		},
		// --- AOS-052-Q1: quebras ANINHADAS e por CONSTRANGIMENTO (antes MINOR) ---
		{
			name: "input tipo aninhado alterado -> major",
			old:  ctr(domain.EgressInternal, nil, nestObj, objA),
			new:  ctr(domain.EgressInternal, nil, nestObjNum, objA), // foo.bar string->number
			want: domain.ChangeMajor,
		},
		{
			name: "input propriedade aninhada removida -> major",
			old:  ctr(domain.EgressInternal, nil, nestObj, objA),
			new:  ctr(domain.EgressInternal, nil, nestObjDel, objA), // remove foo.bar
			want: domain.ChangeMajor,
		},
		{
			name: "input enum acrescentado -> major",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objAEnum, objA), // aperta valores aceites
			want: domain.ChangeMajor,
		},
		{
			name: "input minLength acrescentado -> major",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objAMinLen, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "input additionalProperties:false -> major",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objAClosed, objA),
			want: domain.ChangeMajor,
		},
		{
			name: "input enum removido (relaxa) -> minor",
			old:  ctr(domain.EgressInternal, nil, objAEnum, objA),
			new:  ctr(domain.EgressInternal, nil, objA, objA), // remove a restrição enum
			want: domain.ChangeMinor,
		},
		{
			name: "output enum acrescentado (garantia extra) -> minor",
			old:  ctr(domain.EgressInternal, nil, objA, objA),
			new:  ctr(domain.EgressInternal, nil, objA, objAEnum), // OUTPUT: reforço, compatível
			want: domain.ChangeMinor,
		},
		{
			name: "output enum removido (garantia enfraquecida) -> major",
			old:  ctr(domain.EgressInternal, nil, objA, objAEnum),
			new:  ctr(domain.EgressInternal, nil, objA, objA), // OUTPUT: enfraquece
			want: domain.ChangeMajor,
		},
		// --- fail-closed conservador: schema opaco/malformado/não-objecto ---
		{
			name: "input schema malformado difere -> major (opaque)",
			old:  ctr(domain.EgressInternal, nil, `{"type":"object"`, objA), // JSON malformado
			new:  ctr(domain.EgressInternal, nil, `{"type":"array"`, objA),  // outro malformado distinto
			want: domain.ChangeMajor,
		},
		{
			name: "input schema nao-objecto difere -> major (opaque)",
			old:  ctr(domain.EgressInternal, nil, `[1,2,3]`, objA), // array, não object-schema
			new:  ctr(domain.EgressInternal, nil, `[1,2,4]`, objA),
			want: domain.ChangeMajor,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reasons := ClassifyContract(tc.old, tc.new, tc.semBroken)
			if got != tc.want {
				t.Fatalf("ClassifyContract = %v, quer %v (reasons=%v)", got, tc.want, reasons)
			}
			if tc.want != domain.ChangeNone && len(reasons) == 0 {
				t.Fatalf("mudanca %v sem razoes contribuintes", tc.want)
			}
			if tc.want == domain.ChangeNone && len(reasons) != 0 {
				t.Fatalf("mudanca none com razoes inesperadas: %v", reasons)
			}
		})
	}
}

// TestClassifySchemaOpaque blinda o ramo fail-closed conservador de classifySchema
// (AOS-052-Q4 + cobertura): um schema malformado ou não-objecto que DIFIRA do outro
// lado é ChangeMajor com a razão opaque_change do papel; dois documentos malformados
// BYTE-IDÊNTICOS não emitem sinal (ChangeNone).
func TestClassifySchemaOpaque(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		old, new   domain.Contract
		wantKind   domain.ChangeKind
		wantReason string // "" = não exigir razão específica
	}{
		{
			name:       "input malformado distinto -> major opaque",
			old:        ctr(domain.EgressInternal, nil, `{"type":"object"`, objA),
			new:        ctr(domain.EgressInternal, nil, `{"type":"array"`, objA),
			wantKind:   domain.ChangeMajor,
			wantReason: "input_schema_opaque_change",
		},
		{
			name:       "output nao-objecto distinto -> major opaque",
			old:        ctr(domain.EgressInternal, nil, objA, `[1,2,3]`),
			new:        ctr(domain.EgressInternal, nil, objA, `[9,9,9]`),
			wantKind:   domain.ChangeMajor,
			wantReason: "output_schema_opaque_change",
		},
		{
			name:       "input malformado unilateral -> major opaque",
			old:        ctr(domain.EgressInternal, nil, objA, objA),
			new:        ctr(domain.EgressInternal, nil, `{"type":`, objA), // só o novo é malformado
			wantKind:   domain.ChangeMajor,
			wantReason: "input_schema_opaque_change",
		},
		{
			name:     "ambos malformados IDENTICOS -> none (sem sinal)",
			old:      ctr(domain.EgressInternal, nil, `{"type":`, objA),
			new:      ctr(domain.EgressInternal, nil, `{"type":`, objA),
			wantKind: domain.ChangeNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reasons := ClassifyContract(tc.old, tc.new, false)
			if got != tc.wantKind {
				t.Fatalf("ClassifyContract = %v, quer %v (reasons=%v)", got, tc.wantKind, reasons)
			}
			if tc.wantReason != "" {
				found := false
				for _, r := range reasons {
					if r == tc.wantReason {
						found = true
					}
				}
				if !found {
					t.Fatalf("razoes = %v, quer conter %q", reasons, tc.wantReason)
				}
			}
		})
	}
}

// TestClassifyDeterministic garante que a classificação é DETERMINISTA (mesma
// entrada → mesmo veredicto e mesmas razões, ordem estável) em execuções repetidas.
func TestClassifyDeterministic(t *testing.T) {
	t.Parallel()
	old := ctr(domain.EgressInternal, []string{"a", "b"}, objAReq, objAB)
	new := ctr(domain.EgressExternal, []string{"a"}, objABrq, objA)
	k0, r0 := ClassifyContract(old, new, false)
	for i := 0; i < 50; i++ {
		k, r := ClassifyContract(old, new, false)
		if k != k0 {
			t.Fatalf("iteracao %d: kind instavel %v != %v", i, k, k0)
		}
		if len(r) != len(r0) {
			t.Fatalf("iteracao %d: nº razoes instavel", i)
		}
		for j := range r {
			if r[j] != r0[j] {
				t.Fatalf("iteracao %d: ordem de razoes instavel", i)
			}
		}
	}
}

// TestValidateBump é o gate fail-closed: uma mudança de contrato quebrada que NÃO
// incremente o suficiente é REJEITADA (ErrIncompatibleBump); um bump não-monotónico
// é rejeitado; a sobre-declaração é permitida.
func TestValidateBump(t *testing.T) {
	t.Parallel()
	v := func(s string) domain.Version {
		ver, err := domain.ParseVersion(s)
		if err != nil {
			t.Fatalf("versao invalida %q: %v", s, err)
		}
		return ver
	}
	compatIn := ctr(domain.EgressInternal, nil, objA, objA)
	minorIn := ctr(domain.EgressInternal, nil, objAB, objA)   // adiciona opcional -> minor
	majorIn := ctr(domain.EgressInternal, nil, objANum, objA) // tipo alterado -> major

	cases := []struct {
		name     string
		old, new domain.Contract
		semBrk   bool
		from, to string
		wantErr  error
	}{
		{"patch sem mudanca ok", compatIn, compatIn, false, "1.0.0", "1.0.1", nil},
		{"minor como minor ok", compatIn, minorIn, false, "1.0.0", "1.1.0", nil},
		{"minor como patch REJEITADO", compatIn, minorIn, false, "1.0.0", "1.0.1", ErrIncompatibleBump},
		{"major como major ok", compatIn, majorIn, false, "1.0.0", "2.0.0", nil},
		{"major como minor REJEITADO", compatIn, majorIn, false, "1.0.0", "1.1.0", ErrIncompatibleBump},
		{"major como patch REJEITADO", compatIn, majorIn, false, "1.0.0", "1.0.1", ErrIncompatibleBump},
		{"semantica quebrada como patch REJEITADO", compatIn, compatIn, true, "1.0.0", "1.0.1", ErrIncompatibleBump},
		{"semantica quebrada como major ok", compatIn, compatIn, true, "1.0.0", "2.0.0", nil},
		{"sobre-declaracao (minor como major) permitida", compatIn, minorIn, false, "1.0.0", "2.0.0", nil},
		{"nao-monotonico (downgrade) REJEITADO", compatIn, majorIn, false, "2.0.0", "1.0.0", ErrNonMonotonicBump},
		{"nao-monotonico (igual) REJEITADO", compatIn, compatIn, false, "1.0.0", "1.0.0", ErrNonMonotonicBump},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl, err := ValidateBump(ChangeRequest{
				Kind: domain.KindTool, OldContract: tc.old, NewContract: tc.new,
				From: v(tc.from), To: v(tc.to), SemanticsBroken: tc.semBrk,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateBump err = %v, quer %v (cl=%+v)", err, tc.wantErr, cl)
			}
		})
	}
}

// TestGateEmitsSpan verifica que o gate instrumentado emite um span com a decisão e
// os níveis de mudança (observabilidade sem alterar a semântica).
func TestGateEmitsSpan(t *testing.T) {
	t.Parallel()
	tr := &agentruntime.RecordingTracer{}
	g := NewGate(WithTracer(tr))
	from, _ := domain.ParseVersion("1.0.0")
	to, _ := domain.ParseVersion("1.0.1")
	_, err := g.Validate(context.Background(), ChangeRequest{
		Kind: domain.KindTool, From: from, To: to,
		OldContract: ctr(domain.EgressInternal, nil, objA, objA),
		NewContract: ctr(domain.EgressInternal, nil, objAB, objA), // minor como patch -> rejeitado
	})
	if !errors.Is(err, ErrIncompatibleBump) {
		t.Fatalf("esperava ErrIncompatibleBump, obteve %v", err)
	}
	spans := tr.SpansByOperation(opValidateBump)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span %q, obteve %d", opValidateBump, len(spans))
	}
	if spans[0].Attributes[attrDecision] != "rejected" {
		t.Fatalf("decisao do span = %v, quer rejected", spans[0].Attributes[attrDecision])
	}
	if !spans[0].Ended {
		t.Fatalf("span nao foi fechado")
	}
}
