package signing

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// harness reúne um REG real com o AdmissionVerifier de assinatura ligado, mais o
// trust store, o audit store e o assinante legítimo — o cenário completo de
// admissão fim-a-fim (staging→active gated por assinatura).
type harness struct {
	reg     *registry.Registry
	trust   *TrustStore
	verif   *Verifier
	audit   *audit.MemStore
	signer  *Signer
	digestr digest.SHA256Digester
}

const legitPublisher = "pub:acme"

func newHarness(t *testing.T, opts ...VerifierOption) *harness {
	t.Helper()
	auditStore := audit.NewMemStore()
	trust, err := NewTrustStore(auditStore, WithTrustClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewTrustStore: %v", err)
	}
	signer, err := NewSigner(legitPublisher, keyFromSeed(42))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	if err := trust.Add(context.Background(), legitPublisher, signer.PublicKey()); err != nil {
		t.Fatalf("trust.Add: %v", err)
	}
	vopts := append([]VerifierOption{WithVerifierClock(fixedClock())}, opts...)
	verif, err := NewVerifier(trust, auditStore, vopts...)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	reg, err := registry.New(es,
		registry.WithAdmissionVerifier(verif),
		registry.WithClock(fixedClock()),
	)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return &harness{reg: reg, trust: trust, verif: verif, audit: auditStore, signer: signer}
}

// contractC1 e contractC2 sao dois contratos DISTINTOS (scopes diferentes) com
// digests coerentes mas diferentes — a base do cenario rug-pull.
func contractC1() domain.Contract {
	return domain.Contract{Egress: domain.EgressExternal, CredentialScopes: []string{"vault:db.read"}}
}
func contractC2() domain.Contract {
	return domain.Contract{Egress: domain.EgressExternal, CredentialScopes: []string{"vault:db.read", "vault:admin.write"}}
}

// publishSigned publica uma entrada assinada por signer sobre o digest que o REG
// vai calcular para (kind, contract). Devolve a entrada em staging.
func (h *harness) publishSigned(t *testing.T, s *Signer, id string, v domain.Version, c domain.Contract) domain.Entry {
	t.Helper()
	dig := h.digestr.Digest(domain.KindTool, c)
	req := registry.PublishRequest{
		ID:        id,
		Version:   v,
		Kind:      domain.KindTool,
		Contract:  c,
		Origin:    "git+https://example/tools",
		Publisher: s.KeyID(),
		Signature: s.Sign(id, v, dig),
	}
	e, err := h.reg.Publish(context.Background(), req)
	if err != nil {
		t.Fatalf("Publish(%s@%s): %v", id, v, err)
	}
	return e
}

// --- Admissão legítima: assinatura válida promove a active -----------------

func TestAdmission_ValidSignaturePromotes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id, v := "tool.db", ver(1, 0, 0)
	h.publishSigned(t, h.signer, id, v, contractC1())

	e, err := h.reg.SetStatus(context.Background(), id, v, domain.StatusActive)
	if err != nil {
		t.Fatalf("SetStatus active com assinatura valida: %v", err)
	}
	if e.Status != domain.StatusActive {
		t.Fatalf("estado = %s, quer active", e.Status)
	}
	// A decisao ACEITE tem de aparecer no audit WORM com id/version/digest/resultado.
	assertVerificationRecorded(t, h.audit, id, v, e.Digest, audit.DecisionAllow)
}

// --- RUG-PULL: conteudo re-hasheado sem assinatura legitima e BLOQUEADO -----

// TestAdmission_RugPullBlocked é o teste de DOMÍNIO central de AOS-048: um
// artefacto cujo conteúdo foi ALTERADO e re-hasheado (novo digest coerente) mas
// SEM assinatura válida do publicador legítimo NÃO passa de staging a active.
func TestAdmission_RugPullBlocked(t *testing.T) {
	t.Parallel()
	attacker, err := NewSigner("pub:attacker", keyFromSeed(99)) // chave NAO-confiavel
	if err != nil {
		t.Fatalf("NewSigner attacker: %v", err)
	}
	id := "tool.db"

	cases := []struct {
		name      string
		version   domain.Version
		contract  domain.Contract
		publisher string
		// signWith produz a assinatura (o vector de ataque). nil => sem assinatura.
		signWith func(h *harness, dig string, v domain.Version) string
		wantIs   error // sentinela de signing esperada em VerifyEntry
	}{
		{
			name:      "digest re-hasheado, assinatura do publicador legitimo sobre digest ANTIGO",
			version:   ver(2, 0, 0),
			contract:  contractC2(), // conteudo alterado => digest novo
			publisher: legitPublisher,
			signWith: func(h *harness, _ string, v domain.Version) string {
				// O atacante REUTILIZA uma assinatura legitima sobre o digest de C1,
				// mas o digest pinado agora e o de C2 => nao valida.
				oldDig := h.digestr.Digest(domain.KindTool, contractC1())
				return h.signer.Sign(id, v, oldDig)
			},
			wantIs: ErrSignatureInvalid,
		},
		{
			name:      "digest re-hasheado, assinado por chave NAO-confiavel mas publisher forjado como legitimo",
			version:   ver(3, 0, 0),
			contract:  contractC2(),
			publisher: legitPublisher, // finge ser o publicador legitimo...
			signWith: func(h *harness, dig string, v domain.Version) string {
				return attacker.Sign(id, v, dig) // ...mas assina com a chave do atacante
			},
			wantIs: ErrSignatureInvalid,
		},
		{
			name:      "digest re-hasheado, assinado e publicado pela identidade NAO-confiavel do atacante",
			version:   ver(4, 0, 0),
			contract:  contractC2(),
			publisher: "pub:attacker", // key id nao esta no trust store
			signWith: func(h *harness, dig string, v domain.Version) string {
				return attacker.Sign(id, v, dig)
			},
			wantIs: ErrUntrustedKey,
		},
		{
			name:      "conteudo alterado sem QUALQUER assinatura",
			version:   ver(5, 0, 0),
			contract:  contractC2(),
			publisher: legitPublisher,
			signWith:  nil,
			wantIs:    ErrSignatureMissing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			dig := h.digestr.Digest(domain.KindTool, tc.contract)
			sig := ""
			if tc.signWith != nil {
				sig = tc.signWith(h, dig, tc.version)
			}
			req := registry.PublishRequest{
				ID:        id,
				Version:   tc.version,
				Kind:      domain.KindTool,
				Contract:  tc.contract,
				Origin:    "git+https://evil/tools",
				Publisher: tc.publisher,
				Signature: sig,
			}
			if _, err := h.reg.Publish(context.Background(), req); err != nil {
				t.Fatalf("Publish (staging deve aceitar): %v", err)
			}
			// A PROMOCAO a active tem de ser RECUSADA (fica em staging).
			_, err := h.reg.SetStatus(context.Background(), id, tc.version, domain.StatusActive)
			if !errors.Is(err, registry.ErrAdmissionDenied) {
				t.Fatalf("SetStatus active = %v, quer ErrAdmissionDenied (rug-pull nao bloqueado!)", err)
			}
			// O artefacto TEM de continuar em staging (nunca saltou para active).
			e, rerr := h.reg.Resolve(context.Background(), id, tc.version)
			if rerr != nil {
				t.Fatalf("Resolve: %v", rerr)
			}
			if e.Status != domain.StatusStaging {
				t.Fatalf("estado = %s, quer staging (bloqueio anti rug-pull)", e.Status)
			}
			// A decisao RECUSADA tem de estar selada no audit WORM.
			assertVerificationRecorded(t, h.audit, id, tc.version, dig, audit.DecisionDeny)

			// E o sentinela de signing directo tem de bater certo (VerifyEntry reusavel).
			if _, verr := h.verif.VerifyEntry(context.Background(), e); !errors.Is(verr, tc.wantIs) {
				t.Fatalf("VerifyEntry = %v, quer %v", verr, tc.wantIs)
			}
		})
	}
}

// --- Chave revogada deixa de admitir em runtime (reutilizacao AOS-051) ------

func TestAdmission_RevokedKeyRefusesInRuntime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id, v := "tool.db", ver(1, 0, 0)
	e := h.publishSigned(t, h.signer, id, v, contractC1())

	// Antes da revogacao: verifica.
	if _, err := h.verif.VerifyEntry(context.Background(), e); err != nil {
		t.Fatalf("pre-condicao: assinatura devia validar antes da revogacao: %v", err)
	}
	// Revoga a chave do publicador legitimo.
	if err := h.trust.Revoke(context.Background(), legitPublisher); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Agora a MESMA entrada ja nao valida (chave nao-confiavel) — o RM recusaria.
	if _, err := h.verif.VerifyEntry(context.Background(), e); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("VerifyEntry apos revogacao = %v, quer ErrUntrustedKey", err)
	}
	// E a promocao a active tambem passa a ser recusada.
	if _, err := h.reg.SetStatus(context.Background(), id, v, domain.StatusActive); !errors.Is(err, registry.ErrAdmissionDenied) {
		t.Fatalf("SetStatus apos revogacao = %v, quer ErrAdmissionDenied", err)
	}
}

// --- Reactivacao re-verifica: fecha a janela de revogacao (AOS-048 Q1) ------

// TestAdmission_ReactivationReverifiesAfterRevoke prova que a reactivacao
// deprecated->active atravessa o MESMO gate de assinatura: uma versao previamente
// activa por uma chave DEPOIS revogada NAO volta a active sem re-verificacao. Sem
// isto, a revogacao seria inefectiva na aresta de reactivacao (a chave comprometida
// re-promoveria o artefacto). O conteudo por (id,version) e imutavel, mas a CONFIANCA
// na origem nao — e a reactivacao re-avalia a confianca.
func TestAdmission_ReactivationReverifiesAfterRevoke(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	id, v := "tool.db", ver(1, 0, 0)
	h.publishSigned(t, h.signer, id, v, contractC1())

	// staging->active com assinatura valida (primeira promocao passa o gate).
	if _, err := h.reg.SetStatus(ctx, id, v, domain.StatusActive); err != nil {
		t.Fatalf("SetStatus active (assinatura valida): %v", err)
	}
	// active->deprecated (sem gate).
	if _, err := h.reg.SetStatus(ctx, id, v, domain.StatusDeprecated); err != nil {
		t.Fatalf("SetStatus deprecated: %v", err)
	}
	// A chave do publicador e REVOGADA (compromisso).
	if err := h.trust.Revoke(ctx, legitPublisher); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// A reactivacao deprecated->active TEM de ser recusada: o gate re-verifica e a
	// chave ja nao e confiavel. A janela de revogacao esta fechada.
	if _, err := h.reg.SetStatus(ctx, id, v, domain.StatusActive); !errors.Is(err, registry.ErrAdmissionDenied) {
		t.Fatalf("reactivacao apos revogacao = %v, quer ErrAdmissionDenied", err)
	}
	// O artefacto permanece em deprecated (nao voltou a active).
	e, rerr := h.reg.Resolve(ctx, id, v)
	if rerr != nil {
		t.Fatalf("Resolve: %v", rerr)
	}
	if e.Status != domain.StatusDeprecated {
		t.Fatalf("estado = %s, quer deprecated (reactivacao bloqueada)", e.Status)
	}
	// A decisao de recusa da reactivacao esta selada no audit WORM.
	assertVerificationRecorded(t, h.audit, id, v, e.Digest, audit.DecisionDeny)
}

// --- Invariante dos scopes de credencial (ADR-006) -------------------------

// TestAdmission_AuthorizedScopesExposedNotSecret prova que a verificacao expoe os
// scopes AUTORIZADOS (declaracao do contract, ligados a assinatura via digest) e
// que sao os UNICOS — nenhum segredo e exposto.
func TestAdmission_AuthorizedScopesExposedNotSecret(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	id, v := "tool.db", ver(1, 0, 0)
	// Scopes fora de ordem e com duplicado: a forma exposta e canonica (=digest).
	c := domain.Contract{
		Egress:           domain.EgressExternal,
		CredentialScopes: []string{"vault:z", "vault:a", "vault:z"},
	}
	e := h.publishSigned(t, h.signer, id, v, c)

	res, err := h.verif.VerifyEntry(context.Background(), e)
	if err != nil {
		t.Fatalf("VerifyEntry: %v", err)
	}
	want := []string{"vault:a", "vault:z"}
	if len(res.AuthorizedScopes) != len(want) {
		t.Fatalf("AuthorizedScopes = %v, quer %v", res.AuthorizedScopes, want)
	}
	for i, s := range want {
		if res.AuthorizedScopes[i] != s {
			t.Fatalf("AuthorizedScopes[%d] = %q, quer %q (canonico: ordenado+dedup)", i, res.AuthorizedScopes[i], s)
		}
	}
	if res.KeyID != legitPublisher {
		t.Fatalf("Result.KeyID = %q, quer %q", res.KeyID, legitPublisher)
	}
	// Alterar os scopes muda o digest => a assinatura sobre o digest ANTIGO deixa de
	// validar: os scopes estao CRIPTOGRAFICAMENTE ligados a assinatura (nao ha como
	// o broker conceder scopes nao assinados).
	tampered := e.Clone()
	tampered.Contract.CredentialScopes = append(tampered.Contract.CredentialScopes, "vault:injected")
	tampered.Digest = h.digestr.Digest(domain.KindTool, tampered.Contract)
	if _, err := h.verif.VerifyEntry(context.Background(), tampered); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("scopes adulterados: VerifyEntry = %v, quer ErrSignatureInvalid", err)
	}
}

// --- Observabilidade: spans sem segredos -----------------------------------

func TestAdmission_SpanCarriesPublicAttrsNoSecret(t *testing.T) {
	t.Parallel()
	tr := &agentruntime.RecordingTracer{}
	h := newHarness(t, WithTracer(tr))
	id, v := "tool.db", ver(1, 0, 0)
	e := h.publishSigned(t, h.signer, id, v, contractC1())
	if _, err := h.verif.VerifyEntry(context.Background(), e); err != nil {
		t.Fatalf("VerifyEntry: %v", err)
	}
	spans := tr.SpansByOperation(opVerifySignature)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span %q, tem %d", opVerifySignature, len(spans))
	}
	attrs := spans[0].Attributes
	if attrs[attrDecision] != "admitted" {
		t.Fatalf("span decision = %v, quer admitted", attrs[attrDecision])
	}
	if attrs[attrArtifactDigest] != e.Digest {
		t.Fatalf("span digest = %v, quer %q", attrs[attrArtifactDigest], e.Digest)
	}
	// NENHUM atributo pode conter a assinatura em claro (segredo-adjacente).
	for k, val := range attrs {
		if s, ok := val.(string); ok && s == e.Signature && e.Signature != "" {
			t.Fatalf("span expos a assinatura no atributo %q", k)
		}
	}
}

// assertVerificationRecorded procura na particao de admissao um registo selado
// com o id/version/digest/resultado exigidos por AOS-048.
func assertVerificationRecorded(t *testing.T, store *audit.MemStore, id string, v domain.Version, dig string, want audit.Decision) {
	t.Helper()
	ctx := context.Background()
	head, err := store.Head(ctx, DefaultAdmissionPartition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head == 0 {
		t.Fatal("nenhuma decisao de verificacao selada no audit WORM")
	}
	recs, err := store.Read(ctx, DefaultAdmissionPartition, 1, head)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, r := range recs {
		if r.ToolID == id && r.PolicyVersion == v.String() && r.Resource.Value == dig && r.Decision == want {
			// A cadeia tem de estar integra (tamper-evident).
			if verr := audit.Verify(ctx, store, DefaultAdmissionPartition, 1, head); verr != nil {
				t.Fatalf("audit.Verify: %v", verr)
			}
			return
		}
	}
	t.Fatalf("nao encontrei registo de verificacao (id=%s version=%s digest=%s decision=%s) em %+v",
		id, v, dig, want, recs)
}
