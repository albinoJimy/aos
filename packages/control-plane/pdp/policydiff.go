package pdp

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// AOS-088 — diff determinista do ciclo de vida da política.
//
// O changelog `policy.changed` (AC2) tem de identificar O QUE mudou entre o
// bundle assinado anterior e o novo. Este ficheiro calcula uma impressão digital
// ([bundleFingerprint]) de cada bundle e um [PolicyDiff] DETERMINISTA (sem mapas
// não-ordenados na saída — todas as slices são ordenadas) entre duas: ficheiros
// adicionados/removidos/alterados (por hash) e capabilities adicionadas/removidas
// na allowlist assinada. Não é um diff textual da fonte Cedar (que pode ser
// grande) — é um resumo estável, selável na hash-chain, do que mudou.

// Constantes do span "aos.policy.reload" (DoD). Os valores anotados são versões,
// content_hash, contagem de alterações e resultado — NUNCA a chave nem segredos.
const (
	opPolicyReload         = "aos.policy.reload"
	attrPolicyVersionOld   = "aos.policy.version.old"
	attrPolicyVersionNew   = "aos.policy.version.new"
	attrPolicyContentHash  = "aos.policy.content_hash"
	attrPolicyReloadResult = "aos.policy.reload.result"
	attrPolicyDiffChanged  = "aos.policy.diff.changed"
	// attrPolicyAuditSealed indica se o changelog `policy.changed` do reload foi
	// selado com sucesso no audit (true) ou se a selagem falhou (false). Só é
	// anotado quando há um sink de audit ligado ([WithReloadAuditSink]).
	attrPolicyAuditSealed = "aos.policy.audit_sealed"
	// attrPolicyAuditSealError transporta o erro OPERACIONAL de uma selagem falhada
	// (ex. Store WORM indisponível) — nunca a chave nem segredos.
	attrPolicyAuditSealError = "aos.policy.audit_seal_error"

	reloadResultApplied  = "applied"
	reloadResultRejected = "rejected"
)

// capSep separa a agent_class da capability na chave interna do conjunto de
// capabilities (um byte de controlo, improvável em nomes de classe/capability),
// para uma comparação de conjuntos inequívoca antes da renderização legível.
const capSep = "\x1f"

// bundleFingerprint é a impressão digital determinista de um bundle: o hash de
// cada ficheiro (nome → sha256 hex) e o conjunto de concessões da allowlist
// (agent_class × capability). É materializada uma vez por bundle carregado e
// comparada em [diffFingerprints]. Imutável após construção.
type bundleFingerprint struct {
	// files mapeia nome-de-ficheiro → sha256 hex do conteúdo (regras .cedar E
	// capabilities/*.json — as mesmas chaves do content_hash canónico).
	files map[string]string
	// caps é o conjunto "<class><capSep><cap>" das concessões da allowlist
	// (wildcards justificados aparecem como "<class><capSep>*").
	caps map[string]struct{}
}

// computeFingerprint deriva a [bundleFingerprint] dos ficheiros JÁ verificados
// pela assinatura. Reutiliza [parseAllowlist] (a mesma fusão determinista do gate
// default-deny), pelo que o diff de capabilities reflecte EXACTAMENTE as
// concessões que o PDP passa a impor.
func computeFingerprint(files map[string][]byte) (bundleFingerprint, error) {
	fp := bundleFingerprint{
		files: make(map[string]string, len(files)),
		caps:  make(map[string]struct{}),
	}
	for n, b := range files {
		sum := sha256.Sum256(b)
		fp.files[n] = hex.EncodeToString(sum[:])
	}
	al, err := parseAllowlist(files)
	if err != nil {
		return bundleFingerprint{}, err
	}
	for class, set := range al.byClass {
		for c := range set {
			fp.caps[class+capSep+c] = struct{}{}
		}
	}
	for class := range al.wildcard {
		fp.caps[class+capSep+wildcardCap] = struct{}{}
	}
	return fp, nil
}

// PolicyDiff é o resumo DETERMINISTA da alteração entre dois bundles assinados
// (AC2). Todas as slices são ordenadas para uma saída estável (golden/hash).
type PolicyDiff struct {
	// FilesAdded/FilesRemoved/FilesModified são nomes de ficheiro (ordenados) que
	// passaram a existir, deixaram de existir, ou mudaram de conteúdo (hash).
	FilesAdded    []string
	FilesRemoved  []string
	FilesModified []string
	// CapsAdded/CapsRemoved são concessões da allowlist adicionadas/removidas,
	// renderizadas "<class>: <cap>" (ordenadas).
	CapsAdded   []string
	CapsRemoved []string
}

// ChangedCount é o número total de alterações (soma das cinco categorias) —
// anotado no span e útil como sinal rápido de "mudou muito?".
func (d PolicyDiff) ChangedCount() int {
	return len(d.FilesAdded) + len(d.FilesRemoved) + len(d.FilesModified) +
		len(d.CapsAdded) + len(d.CapsRemoved)
}

// Empty indica se nada mudou entre os dois bundles (ex. re-assinatura sem
// alterar regras nem capabilities).
func (d PolicyDiff) Empty() bool { return d.ChangedCount() == 0 }

// Summary renderiza o diff numa lista de linhas DETERMINISTA e legível, própria
// para selar no changelog (Fields do registo `policy.changed`). Prefixos:
// "file+" adicionado, "file-" removido, "file~" alterado, "cap+"/"cap-"
// concessão adicionada/removida. A ordem é fixa por categoria e, dentro dela,
// pela ordenação já aplicada.
func (d PolicyDiff) Summary() []string {
	out := make([]string, 0, d.ChangedCount())
	for _, f := range d.FilesAdded {
		out = append(out, "file+ "+f)
	}
	for _, f := range d.FilesRemoved {
		out = append(out, "file- "+f)
	}
	for _, f := range d.FilesModified {
		out = append(out, "file~ "+f)
	}
	for _, c := range d.CapsAdded {
		out = append(out, "cap+ "+c)
	}
	for _, c := range d.CapsRemoved {
		out = append(out, "cap- "+c)
	}
	return out
}

// diffFingerprints calcula o [PolicyDiff] determinista entre o bundle anterior
// (old) e o novo. Um ficheiro presente em ambos com hash distinto é "alterado";
// as concessões da allowlist comparam-se como conjuntos.
func diffFingerprints(old, next bundleFingerprint) PolicyDiff {
	var d PolicyDiff
	for n, h := range next.files {
		oh, ok := old.files[n]
		switch {
		case !ok:
			d.FilesAdded = append(d.FilesAdded, n)
		case oh != h:
			d.FilesModified = append(d.FilesModified, n)
		}
	}
	for n := range old.files {
		if _, ok := next.files[n]; !ok {
			d.FilesRemoved = append(d.FilesRemoved, n)
		}
	}
	for c := range next.caps {
		if _, ok := old.caps[c]; !ok {
			d.CapsAdded = append(d.CapsAdded, renderCap(c))
		}
	}
	for c := range old.caps {
		if _, ok := next.caps[c]; !ok {
			d.CapsRemoved = append(d.CapsRemoved, renderCap(c))
		}
	}
	sort.Strings(d.FilesAdded)
	sort.Strings(d.FilesRemoved)
	sort.Strings(d.FilesModified)
	sort.Strings(d.CapsAdded)
	sort.Strings(d.CapsRemoved)
	return d
}

// renderCap converte a chave interna "<class><capSep><cap>" na forma legível
// "<class>: <cap>" usada no changelog.
func renderCap(key string) string {
	if i := strings.Index(key, capSep); i >= 0 {
		return key[:i] + ": " + key[i+len(capSep):]
	}
	return key
}
