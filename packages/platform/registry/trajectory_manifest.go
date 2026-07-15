package registry

import (
	"encoding/json"
	"sort"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// ErrInvalidManifest — construção de um manifesto de dependências mal-formado
// (trajectória, model-id ou hash do prompt em falta). Fail-closed: um manifesto sem
// as âncoras do replay fiel não é construível.
var ErrInvalidManifest = &RegistryError{Code: "E_REG_INVALID_MANIFEST", msg: "manifesto de dependencias mal-formado (trajectoria/model-id/prompt-hash em falta)"}

// DependencyManifest é o MANIFESTO DE DEPENDÊNCIAS IMUTÁVEL por trajectória
// (AOS-052, tecnica/05 §7, ADR-012). Grava, por trajectória, as VERSÕES e DIGESTS
// exactos de todas as skills/tools/servidores usados — JUNTO do model-id e do hash
// do prompt. É a âncora do REPLAY FIEL: sem ela, uma evolução posterior de uma tool
// invalidaria a reprodução do passado.
//
// REUTILIZA o par pinado agentruntime.PinnedDep (o mesmo do manifesto por turno do
// RT, AOS-013) via [ManifestDeps] — NÃO reimplementa o manifesto. É um VALUE TYPE
// IMUTÁVEL: os campos são não-exportados e os acessores devolvem SEMPRE cópias, pelo
// que nenhum chamador consegue mutar um manifesto já construído. O [Fingerprint] é
// um digest determinista sobre a forma canónica — duas construções com o mesmo
// conteúdo produzem o mesmo fingerprint (base tamper-evident do replay).
type DependencyManifest struct {
	trajectoryID string
	modelID      string
	promptHash   string
	deps         []agentruntime.PinnedDep
	fingerprint  string
}

// manifestWire é a forma serializável estável usada para calcular o fingerprint (e
// para exportar o manifesto). A ordem das dependências é normalizada na construção,
// pelo que a serialização é determinista.
type manifestWire struct {
	TrajectoryID string                   `json:"trajectory_id"`
	ModelID      string                   `json:"model_id"`
	PromptHash   string                   `json:"prompt_hash"`
	Deps         []agentruntime.PinnedDep `json:"deps"`
}

// NewDependencyManifest constrói o manifesto imutável a partir das ENTRADAS
// RESOLVIDAS (o tool set congelado da trajectória) mais o model-id e o hash do
// prompt. As dependências são projectadas via [ManifestDeps] (version+digest de
// cada entrada) e ordenadas de forma estável (por nome, depois versão) para que o
// fingerprint seja determinista. Fail-closed: trajectória, model-id ou prompt-hash
// vazios → ErrInvalidManifest.
func NewDependencyManifest(trajectoryID, modelID, promptHash string, entries []domain.Entry) (DependencyManifest, error) {
	if trajectoryID == "" || modelID == "" || promptHash == "" {
		return DependencyManifest{}, ErrInvalidManifest
	}
	deps := ManifestDeps(entries)
	return newManifestFromDeps(trajectoryID, modelID, promptHash, deps)
}

// FromRuntimeManifest constrói o manifesto de dependências da trajectória a partir
// do MANIFESTO POR TURNO do Agent Runtime (agentruntime.Manifest, AOS-013),
// reunindo Tools e Skills nas dependências pinadas e reutilizando o prompt_hash e o
// model-id já lá gravados. É a ponte directa entre o registo por turno do RT e o
// manifesto de dependências imutável do REG — sem reimplementar nenhum dos dois.
func FromRuntimeManifest(trajectoryID string, m agentruntime.Manifest) (DependencyManifest, error) {
	if trajectoryID == "" || m.Model.ModelID == "" || m.PromptHash == "" {
		return DependencyManifest{}, ErrInvalidManifest
	}
	deps := make([]agentruntime.PinnedDep, 0, len(m.Tools)+len(m.Skills))
	deps = append(deps, m.Tools...)
	deps = append(deps, m.Skills...)
	return newManifestFromDeps(trajectoryID, m.Model.ModelID, m.PromptHash, deps)
}

func newManifestFromDeps(trajectoryID, modelID, promptHash string, deps []agentruntime.PinnedDep) (DependencyManifest, error) {
	// Cópia possuída + ordenação estável (o manifesto nunca partilha o slice do
	// chamador, garantindo a imutabilidade mesmo que o chamador mute o seu).
	owned := make([]agentruntime.PinnedDep, len(deps))
	copy(owned, deps)
	sort.SliceStable(owned, func(i, j int) bool {
		if owned[i].Name != owned[j].Name {
			return owned[i].Name < owned[j].Name
		}
		return owned[i].Version < owned[j].Version
	})
	m := DependencyManifest{
		trajectoryID: trajectoryID,
		modelID:      modelID,
		promptHash:   promptHash,
		deps:         owned,
	}
	fp, err := fingerprintOf(m)
	if err != nil {
		return DependencyManifest{}, err
	}
	m.fingerprint = fp
	return m, nil
}

func fingerprintOf(m DependencyManifest) (string, error) {
	raw, err := json.Marshal(manifestWire{
		TrajectoryID: m.trajectoryID,
		ModelID:      m.modelID,
		PromptHash:   m.promptHash,
		Deps:         m.deps,
	})
	if err != nil {
		return "", err
	}
	// Reutiliza o hashing canónico de AOS-047 (ordem de chaves/whitespace
	// irrelevantes) — o mesmo conteúdo produz sempre o mesmo digest.
	return digest.DigestJSON(raw)
}

// TrajectoryID devolve o identificador da trajectória (imutável).
func (m DependencyManifest) TrajectoryID() string { return m.trajectoryID }

// ModelID devolve o model-id pinado (imutável).
func (m DependencyManifest) ModelID() string { return m.modelID }

// PromptHash devolve o hash do prompt da trajectória (imutável).
func (m DependencyManifest) PromptHash() string { return m.promptHash }

// Fingerprint devolve o digest determinista do manifesto — a sua identidade
// tamper-evident. Estável entre construções de conteúdo idêntico.
func (m DependencyManifest) Fingerprint() string { return m.fingerprint }

// Deps devolve uma CÓPIA das dependências pinadas (version+digest de cada
// skill/tool/servidor). A cópia protege a imutabilidade: mutar o slice devolvido
// nunca afecta o manifesto. Ordem estável (nome, versão).
func (m DependencyManifest) Deps() []agentruntime.PinnedDep {
	out := make([]agentruntime.PinnedDep, len(m.deps))
	copy(out, m.deps)
	return out
}

// MarshalJSON serializa o manifesto na sua forma canónica estável (para
// persistência/diagnóstico). Reflecte exactamente o conteúdo coberto pelo
// fingerprint.
func (m DependencyManifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(manifestWire{
		TrajectoryID: m.trajectoryID,
		ModelID:      m.modelID,
		PromptHash:   m.promptHash,
		Deps:         m.deps,
	})
}
