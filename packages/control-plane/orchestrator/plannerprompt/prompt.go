package plannerprompt

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// PromptVersion é o SemVer PRÓPRIO do prompt de decomposição (`prompt_version`,
// tecnica/18 §3.6) — DISTINTO do [plan.PlanVersion] (versão do SCHEMA do documento)
// e do `capabilities_hash`. É um alias do value type SemVer estrito de `plan`: NÃO
// se reimplementa parsing/ordenação de SemVer aqui — reutilizam-se
// [plan.ParsePlanVersion], [plan.PlanVersion.Compare] e [plan.PlanVersion.String].
//
// Semântica ADR-012 aplicada por [ValidatePromptMutation]: MAJOR = mudança de
// comportamento que pode partir golden-sets (exige re-baseline); MINOR = aditivo;
// PATCH = clarificação. O zero-value {0,0,0} é o sentinela "não carimbado".
type PromptVersion = plan.PlanVersion

// ParsePromptVersion parseia um `prompt_version` na forma canónica estrita "X.Y.Z".
// Delega em [plan.ParsePlanVersion] (fail-closed) — a mesma grammar do schema.
func ParsePromptVersion(s string) (PromptVersion, error) { return plan.ParsePlanVersion(s) }

// Prompt é o ARTEFACTO COMPORTAMENTAL versionado: o texto ESTÁTICO do prompt de
// decomposição mais o seu SemVer. É cache-estável (ADR-009) POR CONSTRUÇÃO — o
// [Prompt.Template] é dado imutável e o [Prompt.Fingerprint] é uma função pura dele,
// pelo que a mesma (versão, template) produz sempre a mesma [Prompt.CacheKey].
type Prompt struct {
	Version  PromptVersion
	Template string
}

// Fingerprint é o hash SHA-256 (hex) do template — a IMPRESSÃO cache-estável do
// conteúdo (ADR-009). Puro e determinístico: é a base do binding "mesma versão ⇒
// mesmo conteúdo" que [ValidatePromptMutation] impõe.
func (p Prompt) Fingerprint() string {
	sum := sha256.Sum256([]byte(p.Template))
	return hex.EncodeToString(sum[:])
}

// CacheKey é a chave de cache estável do prompt: versão canónica + fingerprint. Uma
// cache keyed por esta chave nunca serve conteúdo obsoleto — porque uma mudança de
// conteúdo SEM bump de versão é recusada por [ValidatePromptMutation] (o que tornaria
// a chave ambígua). Puro.
func (p Prompt) CacheKey() string {
	return p.Version.String() + ":" + p.Fingerprint()
}

// MetaPromptVersion devolve a string canónica que vai no campo
// [plan.PlannerMeta].PromptVersion do documento produzido sob este prompt
// (proveniência/reprodutibilidade, §3.6).
func (p Prompt) MetaPromptVersion() string { return p.Version.String() }

// valid confere a forma mínima: template não-vazio e versão carimbada (não o
// sentinela {0,0,0}). Fail-closed.
func (p Prompt) valid() bool { return p.Template != "" && !p.Version.IsZero() }

// PromptApproval é o SINAL de aprovação ADR-012 exigido para MUTAR o prompt: um
// aprovador e a referência ao registo de decisão. É modelado como um flag verificado
// ([ValidatePromptMutation]) — não há CI real aqui; o handoff para o pipeline ADR-012
// vivo é a fronteira. Fail-closed: ambos os campos têm de estar presentes.
type PromptApproval struct {
	Approver  string
	ADR012Ref string
}

func (a PromptApproval) present() bool { return a.Approver != "" && a.ADR012Ref != "" }

// Erros do gate de mutação do prompt (ADR-012) — comparáveis por errors.Is.
var (
	// ErrPromptInvalid — o prompt novo não tem forma (template vazio / versão não carimbada).
	ErrPromptInvalid = errors.New("plannerprompt: prompt invalido (template vazio ou prompt_version nao carimbado)")
	// ErrPromptUnchanged — mutação no-op: nem o conteúdo nem a versão mudaram. Não há
	// nada a governar; distingue-se de uma mudança real fail-closed.
	ErrPromptUnchanged = errors.New("plannerprompt: mutacao no-op (conteudo e versao inalterados)")
	// ErrPromptVersionNotBumped — o conteúdo mudou mas a versão NÃO subiu. Partiria a
	// cache-estabilidade (ADR-009): a mesma versão passaria a mapear conteúdo diferente.
	ErrPromptVersionNotBumped = errors.New("plannerprompt: conteudo mudou sem bump de prompt_version (ADR-009/012)")
	// ErrPromptUnapproved — mudança de conteúdo sem aprovação ADR-012 (aprovador+ref).
	ErrPromptUnapproved = errors.New("plannerprompt: mutacao de prompt sem aprovacao ADR-012")
	// ErrPromptContentReused — a versão subiu mas o conteúdo é IGUAL ao anterior: um
	// bump vazio corrompe o histórico de proveniência (duas versões, mesmo fingerprint).
	ErrPromptContentReused = errors.New("plannerprompt: bump de versao sem mudanca de conteudo")
)

// ValidatePromptMutation é o GATE ADR-012 de mutação do prompt. Governa a transição
// old→new fail-closed:
//
//   - new tem de ter forma ([Prompt.valid]);
//   - se conteúdo E versão inalterados ⇒ [ErrPromptUnchanged] (não é mutação);
//   - se o CONTEÚDO mudou: a versão TEM de subir estritamente ([ErrPromptVersionNotBumped])
//     E a mudança tem de vir aprovada ([ErrPromptUnapproved]);
//   - se a VERSÃO subiu mas o conteúdo é igual ⇒ [ErrPromptContentReused] (bump vazio).
//
// É a modelação do pipeline ADR-012 — sem CI real. A aprovação é um sinal
// ([PromptApproval]); o handoff para o pipeline vivo é a fronteira. Puro.
func ValidatePromptMutation(old, new Prompt, ap PromptApproval) error {
	if !new.valid() {
		return ErrPromptInvalid
	}
	contentChanged := old.Fingerprint() != new.Fingerprint()
	versionCmp := new.Version.Compare(old.Version)

	if !contentChanged {
		if versionCmp == 0 {
			return ErrPromptUnchanged
		}
		// Conteúdo igual mas versão diferente: bump vazio (ou downgrade) — corrompe a
		// proveniência. Fail-closed.
		return fmt.Errorf("%w: %s -> %s", ErrPromptContentReused, old.Version, new.Version)
	}
	// Conteúdo mudou: exige bump ESTRITO e aprovação.
	if versionCmp <= 0 {
		return fmt.Errorf("%w: %s -> %s", ErrPromptVersionNotBumped, old.Version, new.Version)
	}
	if !ap.present() {
		return ErrPromptUnapproved
	}
	return nil
}
