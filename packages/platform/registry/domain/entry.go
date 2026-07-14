package domain

// Entry é a ENTRADA versionada de catálogo — a unidade primária do REG (tecnica/05
// §3). Expõe os campos essenciais: id + version (SemVer), digest, signature,
// contract, provenance e status. Uma entrada é IMUTÁVEL do ponto de vista do
// catálogo: uma versão nunca é editada in-place; alterações produzem NOVAS versões
// (append-only). Os acessores devolvem sempre clones para preservar essa
// imutabilidade.
type Entry struct {
	// ID é o identificador estável do artefacto (ex.: "tool.http.get").
	ID string `json:"id"`
	// Version é a versão SemVer exacta desta entrada.
	Version Version `json:"version"`
	// Kind é o tipo de artefacto (skill/tool/servidor MCP).
	Kind ArtifactKind `json:"kind"`
	// Digest é o hash do conteúdo canonicalizado. Em AOS-045 é derivado pelo
	// Digester configurado (placeholder determinista); o SHA-256 é AOS-047.
	Digest string `json:"digest"`
	// Signature é a assinatura do publicador sobre (id, version, digest). CAMPO
	// RESERVADO em AOS-045 (pode vir vazio); a verificação de origem é AOS-048.
	Signature string `json:"signature,omitempty"`
	// Contract é o contrato público de capability.
	Contract Contract `json:"contract"`
	// Provenance é a origem auditável e o estado de confiança TOFU.
	Provenance Provenance `json:"provenance"`
	// Status é o estado do ciclo de vida.
	Status Status `json:"status"`
}

// Key é a chave de identidade pinada de uma entrada: (id, version). É o que o
// catálogo indexa e o que a resolução exige — nunca só o id (que seria uma
// referência flutuante).
type Key struct {
	ID      string
	Version Version
}

// Key devolve a chave pinada da entrada.
func (e Entry) Key() Key { return Key{ID: e.ID, Version: e.Version} }

// Clone devolve uma cópia profunda da entrada (contrato incluído). O catálogo
// devolve sempre clones para que nenhum chamador possa mutar o estado guardado.
func (e Entry) Clone() Entry {
	cp := e
	cp.Contract = e.Contract.clone()
	return cp
}

// Validate impõe as invariantes estruturais de uma entrada bem-formada
// (fail-closed). Não valida a assinatura (AOS-048) nem recalcula o digest — valida
// a FORMA: id presente, tipo/estado/egress/confiança canónicos e digest presente.
func (e Entry) Validate() error {
	if e.ID == "" {
		return ErrEmptyID
	}
	if !e.Kind.Valid() {
		return ErrInvalidKind
	}
	if !e.Status.Valid() {
		return ErrInvalidStatus
	}
	if e.Contract.Egress == "" || !e.Contract.Egress.Valid() {
		return ErrInvalidEgress
	}
	if e.Provenance.Trust == "" || !e.Provenance.Trust.Valid() {
		return ErrInvalidTrust
	}
	if e.Digest == "" {
		return ErrMissingDigest
	}
	return nil
}
