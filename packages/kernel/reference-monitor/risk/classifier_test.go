package risk_test

import (
	"strings"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// --- (1) Classificação por três eixos (sensibilidade + egress + reversibilidade) ---

func TestClassify_TresEixos(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		action risk.Action
		want   risk.Class
	}{
		// Testes Requeridos (AOS-074): egress de dados SENSÍVEIS → DANGER.
		{
			name: "egress_externo_de_dados_sensiveis_e_danger",
			action: risk.Action{
				Sensitivity:   risk.SensitivitySensitive,
				Egress:        risk.EgressExternal,
				Reversibility: risk.Reversible,
				Taint:         taint.Trusted,
			},
			want: risk.ClassDanger,
		},
		// Testes Requeridos: acção local reversível → SAFE.
		{
			name: "local_reversivel_publico_e_safe",
			action: risk.Action{
				Sensitivity:   risk.SensitivityPublic,
				Egress:        risk.EgressNone,
				Reversibility: risk.Reversible,
				Taint:         taint.Trusted,
			},
			want: risk.ClassSafe,
		},
		{
			name: "local_reversivel_interno_e_safe",
			action: risk.Action{
				Sensitivity:   risk.SensitivityInternal,
				Egress:        risk.EgressNone,
				Reversibility: risk.Reversible,
				Taint:         taint.Trusted,
			},
			want: risk.ClassSafe,
		},
		// Irreversível é sempre DANGER (mesmo sem egress, mesmo público).
		{
			name: "irreversivel_e_danger_mesmo_local_publico",
			action: risk.Action{
				Sensitivity:   risk.SensitivityPublic,
				Egress:        risk.EgressNone,
				Reversibility: risk.Irreversible,
				Taint:         taint.Trusted,
			},
			want: risk.ClassDanger,
		},
		// GRAY: egress interno (algum risco mas agrupável).
		{
			name: "egress_interno_e_gray",
			action: risk.Action{
				Sensitivity:   risk.SensitivityInternal,
				Egress:        risk.EgressInternal,
				Reversibility: risk.Reversible,
				Taint:         taint.Trusted,
			},
			want: risk.ClassGray,
		},
		// GRAY: egress externo de dados NÃO sensíveis (não é exfiltração de sensíveis).
		{
			name: "egress_externo_de_dados_publicos_e_gray",
			action: risk.Action{
				Sensitivity:   risk.SensitivityPublic,
				Egress:        risk.EgressExternal,
				Reversibility: risk.Reversible,
				Taint:         taint.Trusted,
			},
			want: risk.ClassGray,
		},
		// GRAY: dados sensíveis SEM egress e reversíveis (risco residual, não exfil).
		{
			name: "sensivel_sem_egress_reversivel_e_gray",
			action: risk.Action{
				Sensitivity:   risk.SensitivitySensitive,
				Egress:        risk.EgressNone,
				Reversibility: risk.Reversible,
				Taint:         taint.Trusted,
			},
			want: risk.ClassGray,
		},
	}
	pol := risk.DefaultPolicy()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := risk.Classify(pol, tc.action)
			if got.Class != tc.want {
				t.Fatalf("Classify() classe = %v, quer %v (rationale: %q)", got.Class, tc.want, got.Rationale)
			}
			if got.PolicyVersion == "" {
				t.Errorf("PolicyVersion vazia: classificação tem de selar a versão da política")
			}
		})
	}
}

// --- (2) Taint untrusted ELEVA a sensibilidade e pode virar a classe -------

func TestClassify_TaintUntrustedEleva(t *testing.T) {
	t.Parallel()
	pol := risk.DefaultPolicy()

	// Interno + egress externo + reversível: trusted é GRAY (não é exfil de
	// sensível); untrusted ELEVA interno→sensível ⇒ egress externo de sensível ⇒ DANGER.
	base := risk.Action{
		Sensitivity:   risk.SensitivityInternal,
		Egress:        risk.EgressExternal,
		Reversibility: risk.Reversible,
	}

	trusted := base
	trusted.Taint = taint.Trusted
	if got := risk.Classify(pol, trusted); got.Class != risk.ClassGray {
		t.Fatalf("trusted: classe = %v, quer GRAY", got.Class)
	}

	untrusted := base
	untrusted.Taint = taint.Untrusted
	if got := risk.Classify(pol, untrusted); got.Class != risk.ClassDanger {
		t.Fatalf("untrusted: classe = %v, quer DANGER (untrusted deve elevar sensibilidade)", got.Class)
	}
}

// --- (3) Fail-closed pelo tipo: eixos desconhecidos elevam o risco ---------

func TestClassify_FailClosed_EixosDesconhecidos(t *testing.T) {
	t.Parallel()
	pol := risk.DefaultPolicy()

	// Reversibilidade desconhecida (valor-zero) ⇒ tratada como irreversível ⇒ DANGER.
	revUnknown := risk.Action{
		Sensitivity:   risk.SensitivityPublic,
		Egress:        risk.EgressNone,
		Reversibility: risk.ReversibilityUnknown,
		Taint:         taint.Trusted,
	}
	if got := risk.Classify(pol, revUnknown); got.Class != risk.ClassDanger {
		t.Errorf("reversibilidade desconhecida: classe = %v, quer DANGER (fail-closed)", got.Class)
	}

	// Egress desconhecido + sensível + reversível ⇒ egress tratado como externo ⇒
	// exfil de sensível ⇒ DANGER.
	egUnknown := risk.Action{
		Sensitivity:   risk.SensitivitySensitive,
		Egress:        risk.EgressUnknown,
		Reversibility: risk.Reversible,
		Taint:         taint.Trusted,
	}
	if got := risk.Classify(pol, egUnknown); got.Class != risk.ClassDanger {
		t.Errorf("egress desconhecido + sensivel: classe = %v, quer DANGER (fail-closed)", got.Class)
	}

	// Sensibilidade desconhecida (valor-zero) sem egress reversível ⇒ tratada como
	// sensível ⇒ não é SAFE (SAFE exige sensibilidade <= interna) ⇒ GRAY.
	sensUnknown := risk.Action{
		Sensitivity:   risk.SensitivityUnknown,
		Egress:        risk.EgressNone,
		Reversibility: risk.Reversible,
		Taint:         taint.Trusted,
	}
	if got := risk.Classify(pol, sensUnknown); got.Class == risk.ClassSafe {
		t.Errorf("sensibilidade desconhecida: classe = SAFE, não devia (fail-closed trata como sensível)")
	}
}

// --- (4) Política versionada (policy-as-code, digest tamper-evident) --------

func TestPolicy_Versao_Determinista_e_TamperEvident(t *testing.T) {
	t.Parallel()
	a := risk.DefaultPolicy()
	b := risk.DefaultPolicy()

	if a.Version() != b.Version() {
		t.Fatalf("versão não determinista: %q != %q", a.Version(), b.Version())
	}
	if !strings.HasPrefix(a.Version(), risk.DefaultPolicyTag+"#") {
		t.Errorf("versão %q não começa por %q#", a.Version(), risk.DefaultPolicyTag)
	}
	if len(a.Hash()) != 64 {
		t.Errorf("digest sha256 hex deve ter 64 chars, tem %d", len(a.Hash()))
	}

	// Uma política com regras diferentes tem de produzir um digest DIFERENTE
	// (tamper-evident): altera-se a classe de uma regra documentada.
	tampered := risk.NewPolicy(risk.DefaultPolicyTag, []risk.Rule{
		{ID: "safe", Says: risk.ClassDanger, Desc: "adulterada"},
	})
	if tampered.Hash() == a.Hash() {
		t.Errorf("digest não é tamper-evident: política adulterada tem o mesmo digest")
	}
	if tampered.Version() == a.Version() {
		t.Errorf("versão não é tamper-evident: política adulterada tem a mesma versão")
	}

	// Determinismo da classificação: a mesma acção dá sempre a mesma classe/versão.
	action := risk.Action{Sensitivity: risk.SensitivitySensitive, Egress: risk.EgressExternal, Reversibility: risk.Reversible, Taint: taint.Trusted}
	first := risk.Classify(a, action)
	second := risk.Classify(a, action)
	if first != second {
		t.Fatalf("classificação não determinista: %+v != %+v", first, second)
	}
}
