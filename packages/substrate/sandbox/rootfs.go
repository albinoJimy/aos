package sandbox

// RootFS modela o sistema de ficheiros da microVM tal como AOS-066 o exige: a RAIZ
// é o snapshot base IMUTÁVEL (read-only) e TODA a escrita vai para um [Overlay] CoW
// EFÉMERO (AOS-065), descartado no destroy. Compõe — não reimplementa — o Overlay
// de AOS-065: o RootFS é a MONTAGEM (raiz read-only + camada de escrita efémera)
// sobre esse overlay.
//
// # Duas superfícies de escrita, uma read-only
//
// Em overlayfs a camada inferior (lower) é read-only e a superior (upper/overlay) é
// escrevível; qualquer escrita faz copy-up para o overlay. O RootFS expõe isto
// EXPLICITAMENTE para o tornar demonstrável:
//
//   - [RootFS.WriteOverlay] — copy-up para o overlay efémero: FUNCIONA e é
//     descartada no destroy.
//   - [RootFS.WriteRoot] — tentativa de escrita DIRECTA na raiz read-only (fora do
//     overlay): FALHA de forma controlada com [ErrReadOnlyRoot], sem panic, e o
//     base NUNCA é mutado (o [Overlay] não expõe mutação do base — a garantia é
//     estrutural, verificada por [RootFS.BaseDigest] estável).
//
// # Isolamento entre execuções
//
// Como a raiz é o base imutável partilhado e o overlay de N é [Overlay.Discard]-ado
// no destroy (nunca reciclado), a execução N+1 — montada a partir de um restore
// NOVO do mesmo base — não observa ficheiro nenhum escrito por N. É a invariante de
// AOS-065 elevada à MONTAGEM read-only + overlay de AOS-066.
//
// Concorrente-seguro: delega no [Overlay], que serializa Read/Write/Discard com o
// seu próprio mutex.
type RootFS struct {
	overlay *Overlay
}

// MountReadOnly monta um [RootFS] read-only sobre o overlay efémero dado (obtido de
// [Snapshot.Restore] / [Lease.Overlay]). Fail-closed se o overlay for nil.
func MountReadOnly(overlay *Overlay) (*RootFS, error) {
	if overlay == nil {
		return nil, ErrNilOverlay
	}
	return &RootFS{overlay: overlay}, nil
}

// ReadOnlyRoot é sempre verdadeiro: a raiz do RootFS é read-only por construção
// (AOS-066). Existe para tornar a propriedade observável em audit/testes.
func (r *RootFS) ReadOnlyRoot() bool { return true }

// Read lê um caminho: a escrita do overlay (copy-up) tem precedência; caso
// contrário cai na raiz read-only (base imutável). ok é falso se o caminho não
// existe em nenhuma camada ou se o overlay já foi descartado.
func (r *RootFS) Read(pathKey string) ([]byte, bool) {
	return r.overlay.Read(pathKey)
}

// WriteOverlay escreve no overlay EFÉMERO (copy-up). FUNCIONA e as escritas
// desaparecem no [RootFS.Discard]. NUNCA toca a raiz read-only. Falha com
// [ErrOverlayDiscarded] se o overlay já foi descartado.
func (r *RootFS) WriteOverlay(pathKey string, data []byte) error {
	return r.overlay.Write(pathKey, data)
}

// WriteRoot modela uma tentativa de escrita DIRECTA na raiz READ-ONLY (fora do
// overlay). FALHA SEMPRE de forma controlada com [ErrReadOnlyRoot] — nunca panic —
// e o base NUNCA é mutado. É o caminho que uma exploração usaria para persistir na
// imagem base; o RootFS impõe o read-only rejeitando-o fail-closed.
func (r *RootFS) WriteRoot(pathKey string, data []byte) error {
	// Nota: não há sequer um caminho que mutaria o base — o [Overlay] não o expõe.
	// A rejeição é a semântica observável do read-only; a imutabilidade é estrutural.
	_ = pathKey
	_ = data
	return ErrReadOnlyRoot
}

// Discard descarta o overlay efémero (idempotente): as escritas desta execução
// desaparecem e o RootFS deixa de ser escrevível/legível. É o passo do destroy que
// garante que a execução seguinte não observa ficheiros desta (AOS-066).
func (r *RootFS) Discard() { r.overlay.Discard() }

// Discarded reporta se o overlay efémero já foi descartado.
func (r *RootFS) Discarded() bool { return r.overlay.Discarded() }

// Dirty reporta se o overlay tem escritas locais (estado desta execução). Um
// RootFS recém-montado é limpo.
func (r *RootFS) Dirty() bool { return r.overlay.Dirty() }

// ImageVersion devolve a versão da imagem base read-only (para o manifesto).
func (r *RootFS) ImageVersion() ImageVersion { return r.overlay.ImageVersion() }

// BaseDigest devolve o digest do snapshot base read-only — prova, entre execuções,
// que a raiz é a MESMA imagem imutável (nunca mutada por uma escrita).
func (r *RootFS) BaseDigest() string { return r.overlay.BaseDigest() }

// OverlayID devolve o id do overlay efémero desta montagem (único por restore).
func (r *RootFS) OverlayID() string { return r.overlay.ID() }
