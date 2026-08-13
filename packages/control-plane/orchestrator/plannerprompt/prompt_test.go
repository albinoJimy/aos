package plannerprompt

import (
	"errors"
	"strings"
	"testing"
)

// TestCurrentPrompt_CacheStable — o prompt corrente é cache-estável (ADR-009): o
// fingerprint é determinístico (mesmo template ⇒ mesmo hash) e a versão está carimbada.
func TestCurrentPrompt_CacheStable(t *testing.T) {
	if !Current.valid() {
		t.Fatal("prompt corrente devia ter forma (template + versao carimbada)")
	}
	// Determinismo: reconstruir o mesmo prompt da a mesma chave de cache.
	twin := Prompt{Version: Current.Version, Template: Current.Template}
	if Current.CacheKey() != twin.CacheKey() {
		t.Fatalf("cache-key nao determinista: %q != %q", Current.CacheKey(), twin.CacheKey())
	}
	if Current.MetaPromptVersion() != "1.1.0" {
		t.Fatalf("prompt_version canonica esperada 1.1.0, obtive %q", Current.MetaPromptVersion())
	}
	// A regra 6 (AOS-273) nomeia a linha de schema que o documento tem de carimbar. É
	// pinada aqui porque é o lado do PRODUTOR de uma regra cuja imposição vive noutro
	// pacote (`planvalidate`): apagá-la do template deixaria o validador a recusar planos
	// por uma instrução que o prompt nunca deu.
	for _, agulha := range []string{"plan_version", "1.2.0", "conditional_on", "role: verifier"} {
		if !strings.Contains(Current.Template, agulha) {
			t.Fatalf("o template corrente nao nomeia %q", agulha)
		}
	}
	// Conteudos diferentes ⇒ fingerprints diferentes (o hash discrimina).
	other := Prompt{Version: Current.Version, Template: Current.Template + " x"}
	if Current.Fingerprint() == other.Fingerprint() {
		t.Fatal("fingerprint devia mudar com o conteudo")
	}
}

// TestPromptMutation_ADR012Gate — CA: mutar o prompt passa pelo pipeline ADR-012.
//
// FALHA-ANTES: sem o gate, uma mudança de conteúdo sem bump/aprovação passaria e
// partiria a cache-estabilidade. O gate recusa: (a) conteúdo mudado sem bump; (b)
// conteúdo mudado sem aprovação; e admite (c) bump estrito + aprovação.
func TestPromptMutation_ADR012Gate(t *testing.T) {
	old := Current
	ap := PromptApproval{Approver: "owner", ADR012Ref: "ADR-012#42"}

	// (a) conteudo muda, versao NAO sobe ⇒ ErrPromptVersionNotBumped.
	sameVer := Prompt{Version: old.Version, Template: old.Template + "\nnova regra 7"}
	if err := ValidatePromptMutation(old, sameVer, ap); !errors.Is(err, ErrPromptVersionNotBumped) {
		t.Fatalf("mudanca sem bump devia dar ErrPromptVersionNotBumped, obtive %v", err)
	}

	// (b) conteudo muda, versao sobe, mas SEM aprovacao ⇒ ErrPromptUnapproved. A versao
	// bumpada deriva da CORRENTE (e nao de um literal): um bump governado do prompt — como
	// o de AOS-273 — nao pode fazer este teste passar a exercitar o caso (a).
	bumped := Prompt{
		Version:  PromptVersion{Major: old.Version.Major, Minor: old.Version.Minor + 1},
		Template: old.Template + "\nnova regra 7",
	}
	if err := ValidatePromptMutation(old, bumped, PromptApproval{}); !errors.Is(err, ErrPromptUnapproved) {
		t.Fatalf("mudanca sem aprovacao devia dar ErrPromptUnapproved, obtive %v", err)
	}

	// (c) bump estrito + aprovacao ⇒ admite.
	if err := ValidatePromptMutation(old, bumped, ap); err != nil {
		t.Fatalf("mutacao governada devia passar, obtive %v", err)
	}
}

// TestPromptMutation_NoopAndEmptyBump — casos de fronteira do gate ADR-012.
func TestPromptMutation_NoopAndEmptyBump(t *testing.T) {
	old := Current
	ap := PromptApproval{Approver: "owner", ADR012Ref: "ADR-012#42"}

	// No-op: nada muda ⇒ ErrPromptUnchanged.
	if err := ValidatePromptMutation(old, old, ap); !errors.Is(err, ErrPromptUnchanged) {
		t.Fatalf("mutacao no-op devia dar ErrPromptUnchanged, obtive %v", err)
	}

	// Bump vazio: versao sobe mas conteudo igual ⇒ ErrPromptContentReused.
	emptyBump := Prompt{Version: PromptVersion{Major: 2}, Template: old.Template}
	if err := ValidatePromptMutation(old, emptyBump, ap); !errors.Is(err, ErrPromptContentReused) {
		t.Fatalf("bump vazio devia dar ErrPromptContentReused, obtive %v", err)
	}

	// Prompt novo sem forma ⇒ ErrPromptInvalid.
	if err := ValidatePromptMutation(old, Prompt{Version: PromptVersion{Major: 2}}, ap); !errors.Is(err, ErrPromptInvalid) {
		t.Fatalf("prompt sem template devia dar ErrPromptInvalid, obtive %v", err)
	}
}

// TestParsePromptVersion_Strict — reutiliza o SemVer estrito de `plan` (nao ha parsing
// proprio). Uma forma nao-canonica e recusada fail-closed.
func TestParsePromptVersion_Strict(t *testing.T) {
	v, err := ParsePromptVersion("1.2.3")
	if err != nil {
		t.Fatalf("1.2.3 devia parsear, obtive %v", err)
	}
	if v.String() != "1.2.3" {
		t.Fatalf("round-trip esperado 1.2.3, obtive %q", v.String())
	}
	for _, bad := range []string{"v1.2.3", "1.2", "1.2.3.4", "", "1.2.x", " 1.2.3"} {
		if _, err := ParsePromptVersion(bad); err == nil {
			t.Fatalf("%q devia ser recusado pelo SemVer estrito", bad)
		}
	}
}
