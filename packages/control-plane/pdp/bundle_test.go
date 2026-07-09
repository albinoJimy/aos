package pdp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// refPolicy devolve a fonte Cedar da política de referência committada.
func refPolicy(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("policies", "aos_authz.cedar"))
	if err != nil {
		t.Fatalf("ler politica de referencia: %v", err)
	}
	return b
}

// refAllowlist devolve a allowlist de capabilities de referência committada
// (AOS-007).
func refAllowlist(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("policies", capabilitiesDir, "allowlist.json"))
	if err != nil {
		t.Fatalf("ler allowlist de referencia: %v", err)
	}
	return b
}

// newKeypair gera um par ed25519 para os testes.
func newKeypair(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// signedRaw constrói um RawBundle válido em memória, assinado com priv.
func signedRaw(t testing.TB, priv ed25519.PrivateKey, version string, files map[string][]byte) RawBundle {
	t.Helper()
	m := Manifest{
		PolicyVersion: version,
		ContentHash:   canonicalContentHash(files),
		CreatedAt:     "2026-07-09T00:00:00Z",
	}
	return RawBundle{
		PolicyFiles: files,
		Manifest:    m,
		Signature:   ed25519.Sign(priv, signingMessage(m)),
	}
}

// TestVerify_BundleValido: um bundle bem assinado verifica contra o seu anchor.
func TestVerify_BundleValido(t *testing.T) {
	t.Parallel()
	pub, priv := newKeypair(t)
	rb := signedRaw(t, priv, "1.0.0", map[string][]byte{"aos_authz.cedar": refPolicy(t)})
	if err := rb.Verify(pub); err != nil {
		t.Fatalf("bundle valido devia verificar: %v", err)
	}
}

// TestVerify_FailClosed cobre a rejeição fail-closed de bundles não-assinados e
// adulterados (conteúdo, versão, assinatura, anchor).
func TestVerify_FailClosed(t *testing.T) {
	t.Parallel()
	pub, priv := newKeypair(t)
	otherPub, _ := newKeypair(t)
	base := map[string][]byte{"aos_authz.cedar": refPolicy(t)}

	tests := []struct {
		name    string
		mutate  func(rb *RawBundle) ed25519.PublicKey // devolve o anchor a usar
		wantErr error
	}{
		{
			name: "conteudo_adulterado",
			mutate: func(rb *RawBundle) ed25519.PublicKey {
				// Muda o .cedar após assinar: content_hash deixa de bater.
				rb.PolicyFiles["aos_authz.cedar"] = append(rb.PolicyFiles["aos_authz.cedar"], []byte("\npermit(principal,action,resource);")...)
				return pub
			},
			wantErr: ErrSignatureInvalid,
		},
		{
			name: "versao_adulterada",
			mutate: func(rb *RawBundle) ed25519.PublicKey {
				// A assinatura cobre a policy_version: mudá-la invalida.
				rb.Manifest.PolicyVersion = "9.9.9"
				return pub
			},
			wantErr: ErrSignatureInvalid,
		},
		{
			name: "content_hash_adulterado",
			mutate: func(rb *RawBundle) ed25519.PublicKey {
				rb.Manifest.ContentHash = "deadbeef"
				return pub
			},
			wantErr: ErrSignatureInvalid,
		},
		{
			name: "nao_assinado",
			mutate: func(rb *RawBundle) ed25519.PublicKey {
				rb.Signature = nil
				return pub
			},
			wantErr: ErrSignatureInvalid,
		},
		{
			name: "assinatura_lixo",
			mutate: func(rb *RawBundle) ed25519.PublicKey {
				rb.Signature = make([]byte, ed25519.SignatureSize) // zeros
				return pub
			},
			wantErr: ErrSignatureInvalid,
		},
		{
			name: "anchor_errado",
			mutate: func(rb *RawBundle) ed25519.PublicKey {
				return otherPub // assinado por priv, verificado com outra chave
			},
			wantErr: ErrSignatureInvalid,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Recria os ficheiros para não partilhar slices entre subtestes.
			files := map[string][]byte{"aos_authz.cedar": append([]byte(nil), base["aos_authz.cedar"]...)}
			rb := signedRaw(t, priv, "1.0.0", files)
			anchor := tc.mutate(&rb)
			err := rb.Verify(anchor)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Verify: obtive %v, esperava %v", err, tc.wantErr)
			}
		})
	}
}

// writeSignedDir escreve um bundle assinado num directório (regras Cedar +
// allowlist de capabilities do AOS-007) e o trust anchor. A allowlist entra no
// bundle assinado, pelo que os testes de round-trip/reload exercitam também o
// gate default-deny sobre uma allowlist verificada.
func writeSignedDir(t testing.TB, dir, version string, priv ed25519.PrivateKey) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), refPolicy(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, capabilitiesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, capabilitiesDir, "allowlist.json"), refAllowlist(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SignBundle(dir, version, priv); err != nil {
		t.Fatalf("SignBundle: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	if err := os.WriteFile(filepath.Join(dir, trustAnchorFile), []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOpen_MissingBundle: directório sem bundle → ErrPolicyUnavailable.
func TestOpen_MissingBundle(t *testing.T) {
	t.Parallel()
	_, err := Open(t.TempDir())
	if !errors.Is(err, ErrPolicyUnavailable) {
		t.Fatalf("esperava ErrPolicyUnavailable, obtive %v", err)
	}
}

// TestOpen_TamperedOnDisk: adulterar o .cedar depois de assinado faz o Open
// falhar fail-closed com ErrSignatureInvalid.
func TestOpen_TamperedOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	// Sanity: carrega antes de adulterar.
	if _, err := Open(dir); err != nil {
		t.Fatalf("bundle intacto devia carregar: %v", err)
	}
	// Adultera o ficheiro de política sem re-assinar.
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), append(refPolicy(t), []byte("\n// tamper")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("bundle adulterado devia dar ErrSignatureInvalid, obtive %v", err)
	}
}

// TestOpen_SignBundleRoundTrip: SignBundle + Open + Decide num directório temp.
func TestOpen_SignBundleRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "2.3.1", priv)

	p, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if p.Version() != "2.3.1" {
		t.Errorf("Version()=%q, esperava 2.3.1", p.Version())
	}
	d, err := p.Decide(context.Background(), httpPost())
	if err != nil || d.Effect != Permit {
		t.Errorf("Decide: effect=%q err=%v", d.Effect, err)
	}
}

// TestReload_SoAceitaVersaoNovaAssinada valida o hot-reload: só versão
// estritamente mais recente E assinada é aceite; versão igual/anterior e bundle
// adulterado são rejeitados sem alterar o estado em vigor.
func TestReload_SoAceitaVersaoNovaAssinada(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	p, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Re-assina versão mais recente com a MESMA chave: hot-reload aceita.
	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload para 1.1.0 devia aceitar: %v", err)
	}
	if p.Version() != "1.1.0" {
		t.Errorf("apos reload Version()=%q, esperava 1.1.0", p.Version())
	}

	// Versão anterior: rejeitada com ErrStalePolicyVersion (não regride política
	// em vigor) — sentinela DEDICADA, distinta de ErrSignatureInvalid: o bundle
	// está validamente assinado, apenas não é mais recente.
	if _, err := SignBundle(dir, "1.0.5", priv); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(); !errors.Is(err, ErrStalePolicyVersion) {
		t.Errorf("reload para versao anterior devia dar ErrStalePolicyVersion, obtive %v", err)
	}
	if errors.Is(p.Reload(), ErrSignatureInvalid) {
		t.Error("rejeicao por versao nao-crescente NAO deve conflacionar com ErrSignatureInvalid")
	}
	if p.Version() != "1.1.0" {
		t.Errorf("versao em vigor mudou indevidamente: %q", p.Version())
	}

	// Bundle assinado por chave DIFERENTE (não o trust anchor): rejeitado.
	_, other := newKeypair(t)
	if _, err := SignBundle(dir, "2.0.0", other); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("reload de bundle nao-confiavel devia ser rejeitado, obtive %v", err)
	}
	if p.Version() != "1.1.0" {
		t.Errorf("versao em vigor mudou indevidamente apos reload nao-confiavel: %q", p.Version())
	}
}

// TestReload_AuditTrail assevera que um hot-reload bem-sucedido emite um evento
// de alteração de política (old_version→new_version + content_hash) ao callback
// registado — o registo de PRIMEIRA CLASSE da alteração de política, verificável
// no audit trail (AC#4). Um reload rejeitado (versão não-crescente) NÃO emite.
func TestReload_AuditTrail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	var events []PolicyChangeEvent
	p, err := Open(dir, WithReloadAudit(func(ev PolicyChangeEvent) {
		events = append(events, ev)
	}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Reload para versão mais recente: emite exactamente um evento com a transição.
	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload 1.1.0: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("esperava 1 evento de alteracao, obtive %d", len(events))
	}
	ev := events[0]
	if ev.OldVersion != "1.0.0" || ev.NewVersion != "1.1.0" {
		t.Errorf("transicao=%q→%q, esperava 1.0.0→1.1.0", ev.OldVersion, ev.NewVersion)
	}
	rb, err := loadRawBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ContentHash != rb.Manifest.ContentHash || ev.ContentHash == "" {
		t.Errorf("content_hash=%q, esperava %q", ev.ContentHash, rb.Manifest.ContentHash)
	}
	if ev.At.IsZero() {
		t.Error("timestamp do evento nao devia ser zero")
	}

	// Reload rejeitado (versão não-crescente): NÃO emite evento de audit.
	if _, err := SignBundle(dir, "1.0.5", priv); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(); !errors.Is(err, ErrStalePolicyVersion) {
		t.Fatalf("esperava ErrStalePolicyVersion, obtive %v", err)
	}
	if len(events) != 1 {
		t.Errorf("reload rejeitado nao devia emitir evento; total=%d", len(events))
	}
}

// TestOpen_WithTrustAnchor: com o anchor fornecido out-of-band, o Open verifica
// contra ESSE anchor confiável e ignora o trust_anchor.pub do dir do bundle —
// mesmo que um adversário o tenha substituído. Um anchor errado rejeita
// fail-closed.
func TestOpen_WithTrustAnchor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pub, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	// Adversário substitui o trust_anchor.pub no dir por uma chave sua.
	advPub, _ := newKeypair(t)
	if err := os.WriteFile(filepath.Join(dir, trustAnchorFile),
		[]byte(base64.StdEncoding.EncodeToString(advPub)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Com o anchor confiável fornecido out-of-band, o bundle (assinado por priv)
	// ainda verifica — o anchor adulterado no dir é ignorado.
	p, err := Open(dir, WithTrustAnchor(pub))
	if err != nil {
		t.Fatalf("Open com anchor confiavel devia verificar: %v", err)
	}
	if p.Version() != "1.0.0" {
		t.Errorf("Version()=%q, esperava 1.0.0", p.Version())
	}

	// Anchor errado fornecido explicitamente: rejeição fail-closed.
	if _, err := Open(dir, WithTrustAnchor(advPub)); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("Open com anchor errado devia dar ErrSignatureInvalid, obtive %v", err)
	}
}

// TestReferenceBundle_Assinado assevera que o bundle COMMITTADO em policies/
// verifica contra o trust anchor committado — o gate de que a governação não
// regride (nenhum segredo no repo, só a chave pública).
func TestReferenceBundle_Assinado(t *testing.T) {
	t.Parallel()
	anchor, err := loadTrustAnchor("policies")
	if err != nil {
		t.Fatalf("loadTrustAnchor: %v", err)
	}
	rb, err := loadRawBundle("policies")
	if err != nil {
		t.Fatalf("loadRawBundle: %v", err)
	}
	if err := rb.Verify(anchor); err != nil {
		t.Fatalf("bundle de referencia committado devia verificar: %v", err)
	}
}
