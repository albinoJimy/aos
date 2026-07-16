package messaging

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

// clockT é o instante fixo (determinístico) usado pelos testes como "agora".
var clockT = time.Unix(1_700_000_000, 0).UTC()

// fixture reúne o Verifier ligado e os colaboradores para os testes.
type fixture struct {
	v      *Verifier
	vault  *fakeVault
	reg    *fakeRegistry
	refs   *fakeRefs
	store  *audit.MemStore
	now    time.Time
	refID  string
	refHsh []byte
}

// newFixture constrói um Verifier com uma NHI "nhi:a" (seed 1), autoridade
// {cap:summarize, cap:report} e uma referência autêntica "ref-1". O relógio é fixo
// em clockT para que a janela de frescura seja determinística.
func newFixture(t *testing.T, opts ...VerifierOption) *fixture {
	t.Helper()
	vault := newFakeVault()
	pub := vault.provision("nhi:a", 1)
	reg := newFakeRegistry()
	reg.put("nhi:a", pub, "cap:summarize", "cap:report")
	refs := newFakeRefs()
	hsh := refs.put("ref-1", []byte("sub-resultado do agente A"))
	store := audit.NewMemStore()
	clock := func() time.Time { return clockT }
	all := append([]VerifierOption{WithVerifierClock(clock)}, opts...)
	v, err := NewVerifier(reg, refs, store, all...)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return &fixture{v: v, vault: vault, reg: reg, refs: refs, store: store, now: clockT, refID: "ref-1", refHsh: hsh}
}

// signAs assina uma mensagem por nhi:a com um nonce novo e IssuedAt = issuedAt.
func (f *fixture) signAs(t *testing.T, issuedAt time.Time, mut func(m *Message)) Message {
	t.Helper()
	m := Message{
		Origin:    "nhi:a",
		Authority: []string{"cap:summarize"},
		Action:    "cap:summarize",
		Reference: Reference{ID: f.refID, Hash: append([]byte(nil), f.refHsh...)},
		Payload:   []byte("age sobre o meu resumo"),
		Nonce:     newNonce(t),
		IssuedAt:  issuedAt,
	}
	if mut != nil {
		mut(&m)
	}
	signed, err := SignMessage(context.Background(), f.vault, m)
	if err != nil {
		t.Fatalf("SignMessage: %v", err)
	}
	return signed
}

// validMsg devolve uma mensagem VÁLIDA, fresca e assinada por nhi:a.
func (f *fixture) validMsg(t *testing.T) Message {
	t.Helper()
	return f.signAs(t, f.now, nil)
}

func TestVerify_ValidMessageAccepted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	vm, err := f.v.Verify(ctx, f.validMsg(t))
	if err != nil {
		t.Fatalf("mensagem válida rejeitada: %v", err)
	}
	if vm.Origin != "nhi:a" || vm.Action != "cap:summarize" {
		t.Fatalf("VerifiedMessage inesperada: %+v", vm)
	}
	// A autoridade devolvida é a AUTORITATIVA (do directório), não a auto-declarada.
	if len(vm.Authority) != 2 {
		t.Fatalf("autoridade autoritativa esperada (2 caps), obtida %v", vm.Authority)
	}
	// Nenhuma rejeição selada num caminho de sucesso.
	head, _ := f.store.Head(ctx, "msg-verify:nhi:a")
	if head != 0 {
		t.Fatalf("caminho de sucesso não devia selar nada; head=%d", head)
	}
}

// assertSealedAuth confirma que a última rejeição na partição ATRIBUÍVEL do emissor
// é um deny atribuído ao emissor REAL (origem autenticada), com o motivo esperado.
func assertSealedAuth(t *testing.T, store *audit.MemStore, origin, wantReason, wantRef string) {
	t.Helper()
	ctx := context.Background()
	part := "msg-verify:" + origin
	head, err := store.Head(ctx, part)
	if err != nil || head == 0 {
		t.Fatalf("rejeição não selada em %q (head=%d, err=%v)", part, head, err)
	}
	rec, ok, err := store.At(ctx, part, head)
	if err != nil || !ok {
		t.Fatalf("registo selado ausente: %v", err)
	}
	if rec.Decision != audit.DecisionDeny {
		t.Fatalf("decisão selada %q, quer deny", rec.Decision)
	}
	if rec.Principal.NHIID != origin {
		t.Fatalf("emissor selado %q, quer %q (deve ser atribuível ao emissor real)", rec.Principal.NHIID, origin)
	}
	if wantRef != "" && rec.Resource.Value != wantRef {
		t.Fatalf("referência selada %q, quer %q", rec.Resource.Value, wantRef)
	}
	if got := reasonOf(rec); got != wantReason {
		t.Fatalf("motivo selado %q, quer %q", got, wantReason)
	}
	if err := audit.Verify(ctx, store, part, 1, head); err != nil {
		t.Fatalf("cadeia de audit não verifica: %v", err)
	}
}

// assertSealedUnauth confirma que a rejeição de uma origem NÃO autenticada foi
// selada na partição de QUARENTENA (não na cadeia atribuível da NHI clamada), SEM
// principal autenticado, com a origem clamada apenas como CLAIM.
func assertSealedUnauth(t *testing.T, store *audit.MemStore, claimedOrigin, wantReason, wantRef string) {
	t.Helper()
	ctx := context.Background()
	head, err := store.Head(ctx, partitionUnauth)
	if err != nil || head == 0 {
		t.Fatalf("rejeição não selada na quarentena %q (head=%d, err=%v)", partitionUnauth, head, err)
	}
	rec, ok, err := store.At(ctx, partitionUnauth, head)
	if err != nil || !ok {
		t.Fatalf("registo de quarentena ausente: %v", err)
	}
	if rec.Decision != audit.DecisionDeny {
		t.Fatalf("decisão selada %q, quer deny", rec.Decision)
	}
	// A NHI clamada NÃO pode ser o principal responsável (spoofável).
	if rec.Principal.NHIID != "" {
		t.Fatalf("principal selado %q, quer VAZIO (origem não autenticada)", rec.Principal.NHIID)
	}
	if wantRef != "" && rec.Resource.Value != wantRef {
		t.Fatalf("referência selada %q, quer %q", rec.Resource.Value, wantRef)
	}
	if got := reasonOf(rec); got != wantReason {
		t.Fatalf("motivo selado %q, quer %q", got, wantReason)
	}
	// A origem clamada existe como CLAIM não-autenticado, não como atribuição.
	claimed, authd := claimedOriginOf(rec)
	if claimed != claimedOrigin {
		t.Fatalf("claimed_origin selado %q, quer %q", claimed, claimedOrigin)
	}
	if authd != "false" {
		t.Fatalf("obligation claimed_origin devia marcar authenticated=false, obtido %q", authd)
	}
	// A cadeia atribuível da vítima NÃO foi poluída.
	if wantRef != "" {
		if vh, _ := store.Head(ctx, "msg-verify:"+claimedOrigin); vh != 0 {
			t.Fatalf("cadeia atribuível da vítima %q foi poluída (head=%d)", claimedOrigin, vh)
		}
	}
	if err := audit.Verify(ctx, store, partitionUnauth, 1, head); err != nil {
		t.Fatalf("cadeia de quarentena não verifica: %v", err)
	}
}

// reasonOf extrai o motivo de rejeição da obligation reject_reason.
func reasonOf(rec audit.AuditRecord) string {
	for _, ob := range rec.Obligations {
		if ob.Type == "reject_reason" {
			return ob.Params["reason"]
		}
	}
	return ""
}

// claimedOriginOf extrai (claimed_origin, authenticated) da obligation homónima.
func claimedOriginOf(rec audit.AuditRecord) (string, string) {
	for _, ob := range rec.Obligations {
		if ob.Type == "claimed_origin" {
			return ob.Params["claimed_origin"], ob.Params["authenticated"]
		}
	}
	return "", ""
}

func TestVerify_Rejections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name       string
		mutate     func(t *testing.T, f *fixture, m Message) Message
		wantErr    error
		wantReason string
		unauth     bool // origem ainda NÃO autenticada no ponto da rejeição
	}{
		{
			name:       "assinatura ausente é mensagem inválida",
			mutate:     func(_ *testing.T, _ *fixture, m Message) Message { m.Signature = nil; return m },
			wantErr:    ErrInvalidMessage,
			wantReason: ReasonInvalidMessage,
			unauth:     true,
		},
		{
			name:       "referência em falta é mensagem inválida",
			mutate:     func(_ *testing.T, _ *fixture, m Message) Message { m.Reference = Reference{}; return m },
			wantErr:    ErrInvalidMessage,
			wantReason: ReasonInvalidMessage,
			unauth:     true,
		},
		{
			name:       "nonce ausente é mensagem inválida",
			mutate:     func(_ *testing.T, _ *fixture, m Message) Message { m.Nonce = nil; return m },
			wantErr:    ErrInvalidMessage,
			wantReason: ReasonInvalidMessage,
			unauth:     true,
		},
		{
			name:       "timestamp zero é mensagem inválida",
			mutate:     func(_ *testing.T, _ *fixture, m Message) Message { m.IssuedAt = time.Time{}; return m },
			wantErr:    ErrInvalidMessage,
			wantReason: ReasonInvalidMessage,
			unauth:     true,
		},
		{
			name: "origem desconhecida (NHI sem chave pinada)",
			mutate: func(t *testing.T, f *fixture, _ Message) Message {
				// Assina com uma NHI que NÃO está no directório.
				f.vault.provision("nhi:ghost", 9)
				m := Message{
					Origin: "nhi:ghost", Authority: []string{"cap:summarize"}, Action: "cap:summarize",
					Reference: Reference{ID: f.refID, Hash: f.refHsh}, Nonce: newNonce(t), IssuedAt: f.now,
				}
				signed, err := SignMessage(ctx, f.vault, m)
				if err != nil {
					t.Fatal(err)
				}
				return signed
			},
			wantErr:    ErrUnknownOrigin,
			wantReason: ReasonUnknownOrigin,
			unauth:     true,
		},
		{
			name: "assinatura inválida (byte trocado) é forja",
			mutate: func(_ *testing.T, _ *fixture, m Message) Message {
				m.Signature[0] ^= 0xFF
				return m
			},
			wantErr:    ErrForgedOrigin,
			wantReason: ReasonForgedOrigin,
			unauth:     true,
		},
		{
			name: "payload adulterado após assinar é forja",
			mutate: func(_ *testing.T, _ *fixture, m Message) Message {
				m.Payload = append(m.Payload, '!')
				return m
			},
			wantErr:    ErrForgedOrigin,
			wantReason: ReasonForgedOrigin,
			unauth:     true,
		},
		{
			name: "nonce adulterado após assinar é forja",
			mutate: func(_ *testing.T, _ *fixture, m Message) Message {
				m.Nonce[0] ^= 0xFF // o nonce é coberto pela assinatura
				return m
			},
			wantErr:    ErrForgedOrigin,
			wantReason: ReasonForgedOrigin,
			unauth:     true,
		},
		{
			name: "timestamp adulterado após assinar é forja",
			mutate: func(_ *testing.T, _ *fixture, m Message) Message {
				m.IssuedAt = m.IssuedAt.Add(time.Second) // o timestamp é coberto pela assinatura
				return m
			},
			wantErr:    ErrForgedOrigin,
			wantReason: ReasonForgedOrigin,
			unauth:     true,
		},
		{
			name: "emissor falsificado: clama nhi:a mas assinado por outra chave",
			mutate: func(t *testing.T, f *fixture, _ Message) Message {
				// O atacante tem a SUA própria chave, mas põe Origin=nhi:a (que o
				// directório conhece com OUTRA chave). A assinatura não valida contra a
				// chave pinada de nhi:a.
				attacker := ed25519.NewKeyFromSeed(seedBytes(99))
				m := Message{
					Origin: "nhi:a", Authority: []string{"cap:summarize"}, Action: "cap:summarize",
					Reference: Reference{ID: f.refID, Hash: f.refHsh},
					Payload:   []byte("resumo forjado"), Nonce: newNonce(t), IssuedAt: f.now,
				}
				m.Signature = ed25519.Sign(attacker, canonicalBytes(m))
				return m
			},
			wantErr:    ErrForgedOrigin,
			wantReason: ReasonForgedOrigin,
			unauth:     true,
		},
		{
			name: "autoridade não cobre a acção pedida",
			mutate: func(t *testing.T, f *fixture, _ Message) Message {
				// A NHI a tem {summarize, report} mas pede cap:delete — não coberta.
				m := Message{
					Origin: "nhi:a", Authority: []string{"cap:report"}, Action: "cap:delete",
					Reference: Reference{ID: f.refID, Hash: f.refHsh}, Nonce: newNonce(t), IssuedAt: f.now,
				}
				signed, err := SignMessage(ctx, f.vault, m)
				if err != nil {
					t.Fatal(err)
				}
				return signed
			},
			wantErr:    ErrAuthorityNotCovered,
			wantReason: ReasonAuthorityNotCovered,
		},
		{
			name: "autoridade clamada excede a autoritativa",
			mutate: func(t *testing.T, f *fixture, _ Message) Message {
				// Clama cap:admin que a NHI NÃO tem: auto-concessão de autoridade.
				m := Message{
					Origin: "nhi:a", Authority: []string{"cap:summarize", "cap:admin"}, Action: "cap:summarize",
					Reference: Reference{ID: f.refID, Hash: f.refHsh}, Nonce: newNonce(t), IssuedAt: f.now,
				}
				signed, err := SignMessage(ctx, f.vault, m)
				if err != nil {
					t.Fatal(err)
				}
				return signed
			},
			wantErr:    ErrAuthorityNotCovered,
			wantReason: ReasonAuthorityNotCovered,
		},
		{
			name: "referência inexistente (fabricada)",
			mutate: func(t *testing.T, f *fixture, _ Message) Message {
				m := Message{
					Origin: "nhi:a", Authority: []string{"cap:summarize"}, Action: "cap:summarize",
					Reference: Reference{ID: "ref-fabricada", Hash: contentHash([]byte("inventado"))},
					Nonce:     newNonce(t), IssuedAt: f.now,
				}
				signed, err := SignMessage(ctx, f.vault, m)
				if err != nil {
					t.Fatal(err)
				}
				return signed
			},
			wantErr:    ErrReferenceNotFound,
			wantReason: ReasonReferenceNotFound,
		},
		{
			name: "referência existe mas hash divergente (inautêntica)",
			mutate: func(t *testing.T, f *fixture, _ Message) Message {
				// ref-1 existe, mas a mensagem cobre um hash diferente do autêntico.
				m := Message{
					Origin: "nhi:a", Authority: []string{"cap:summarize"}, Action: "cap:summarize",
					Reference: Reference{ID: f.refID, Hash: contentHash([]byte("conteudo trocado"))},
					Nonce:     newNonce(t), IssuedAt: f.now,
				}
				signed, err := SignMessage(ctx, f.vault, m)
				if err != nil {
					t.Fatal(err)
				}
				return signed
			},
			wantErr:    ErrReferenceInauthentic,
			wantReason: ReasonReferenceInauthentic,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			msg := tc.mutate(t, f, f.validMsg(t))
			_, err := f.v.Verify(ctx, msg)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("erro = %v, quer %v", err, tc.wantErr)
			}
			if tc.unauth {
				assertSealedUnauth(t, f.store, msg.Origin, tc.wantReason, msg.Reference.ID)
			} else {
				assertSealedAuth(t, f.store, msg.Origin, tc.wantReason, msg.Reference.ID)
			}
		})
	}
}

// TestVerify_ReplayRejected é a prova anti-replay (finding replay-no-freshness):
// uma mensagem legítima capturada e REENVIADA é aceite UMA vez e rejeitada em todos
// os reenvios seguintes, sem re-autorizar a mesma acção sobre a mesma referência.
func TestVerify_ReplayRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	msg := f.validMsg(t)

	// Primeira entrega: aceite.
	if _, err := f.v.Verify(ctx, msg); err != nil {
		t.Fatalf("primeira entrega devia ser aceite: %v", err)
	}
	// Reenvios (replay): todos rejeitados por nonce reutilizado.
	for i := 0; i < 3; i++ {
		if _, err := f.v.Verify(ctx, msg); !errors.Is(err, ErrReplayedNonce) {
			t.Fatalf("reenvio %d: erro = %v, quer ErrReplayedNonce", i, err)
		}
	}
	// A rejeição é atribuível ao emissor REAL (origem autenticada antes da dedup).
	assertSealedAuth(t, f.store, "nhi:a", ReasonReplayedNonce, f.refID)
}

// TestVerify_StaleAndFutureRejected cobre a janela de frescura: uma mensagem
// demasiado antiga (replay de captura antiga) OU com timestamp futuro além do skew
// é rejeitada, mesmo com assinatura válida.
func TestVerify_StaleAndFutureRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("demasiado antiga", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		old := f.signAs(t, f.now.Add(-10*time.Minute), nil) // janela default 5min
		if _, err := f.v.Verify(ctx, old); !errors.Is(err, ErrStaleMessage) {
			t.Fatalf("erro = %v, quer ErrStaleMessage", err)
		}
		assertSealedAuth(t, f.store, "nhi:a", ReasonStaleMessage, f.refID)
	})

	t.Run("timestamp futuro além do skew", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		future := f.signAs(t, f.now.Add(10*time.Minute), nil) // skew default 1min
		if _, err := f.v.Verify(ctx, future); !errors.Is(err, ErrStaleMessage) {
			t.Fatalf("erro = %v, quer ErrStaleMessage", err)
		}
		assertSealedAuth(t, f.store, "nhi:a", ReasonStaleMessage, f.refID)
	})

	t.Run("dentro do skew é aceite", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		near := f.signAs(t, f.now.Add(30*time.Second), nil) // < 1min skew
		if _, err := f.v.Verify(ctx, near); err != nil {
			t.Fatalf("mensagem dentro do skew devia ser aceite: %v", err)
		}
	})
}

// TestVerify_FreshnessWindowConfigurable confirma que WithFreshnessWindow aperta a
// janela (uma mensagem que passaria no default é agora rejeitada).
func TestVerify_FreshnessWindowConfigurable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t, WithFreshnessWindow(time.Minute, time.Second))
	msg := f.signAs(t, f.now.Add(-2*time.Minute), nil) // 2min > janela de 1min
	if _, err := f.v.Verify(ctx, msg); !errors.Is(err, ErrStaleMessage) {
		t.Fatalf("erro = %v, quer ErrStaleMessage", err)
	}
}

// TestGateElevation_IDExistsButForged é a prova central da melhoria ao
// hallucination gate: um emissor cujo ID EXISTE no directório e cuja referência
// EXISTE, mas cuja mensagem foi FORJADA (assinada por outra chave), é aceite pelo
// gate ANTIGO ("o ID existe") e REJEITADO pelo novo (origem autêntica + autoridade
// + referência). Não basta o ID.
func TestGateElevation_IDExistsButForged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)

	// Mensagem forjada: Origin=nhi:a (ID que EXISTE), referência ref-1 (que EXISTE),
	// mas assinada pela chave do atacante.
	attacker := ed25519.NewKeyFromSeed(seedBytes(42))
	forged := Message{
		Origin: "nhi:a", Authority: []string{"cap:summarize"}, Action: "cap:summarize",
		Reference: Reference{ID: f.refID, Hash: f.refHsh},
		Payload:   []byte("resumo alucinado, origem forjada"),
		Nonce:     newNonce(t), IssuedAt: f.now,
	}
	forged.Signature = ed25519.Sign(attacker, canonicalBytes(forged))

	// Gate ANTIGO (só verifica que o ID existe): aceitaria.
	if !naiveIDGate(ctx, f.reg, f.refs, forged) {
		t.Fatal("pré-condição: o gate antigo devia ACEITAR (o ID e a referência existem)")
	}

	// Gate NOVO: rejeita por forja e sela a rejeição na QUARENTENA (origem não
	// autenticada), sem poluir a cadeia atribuível de nhi:a.
	if _, err := f.v.Verify(ctx, forged); !errors.Is(err, ErrForgedOrigin) {
		t.Fatalf("gate novo: erro = %v, quer ErrForgedOrigin", err)
	}
	assertSealedUnauth(t, f.store, "nhi:a", ReasonForgedOrigin, f.refID)
}

// naiveIDGate modela o hallucination gate ANTIGO (AOS-012): só verifica que o ID
// do emissor existe e que a referência existe — SEM autenticar a origem. Existe só
// no teste, para contrastar com [Verifier.Verify].
func naiveIDGate(ctx context.Context, reg *fakeRegistry, refs *fakeRefs, m Message) bool {
	if _, _, ok, _ := reg.Lookup(ctx, m.Origin); !ok {
		return false
	}
	_, ok, _ := refs.Resolve(ctx, m.Reference.ID)
	return ok
}

// TestVerify_ForgeFloodDoesNotPolluteVictim é a prova do finding audit-attribution:
// um flood de forjas com Origin=nhi:a NÃO enche a cadeia atribuível de nhi:a — vai
// toda para a quarentena, com a origem apenas como CLAIM.
func TestVerify_ForgeFloodDoesNotPolluteVictim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	attacker := ed25519.NewKeyFromSeed(seedBytes(77))

	const flood = 5
	for i := 0; i < flood; i++ {
		forged := Message{
			Origin: "nhi:a", Authority: []string{"cap:summarize"}, Action: "cap:summarize",
			Reference: Reference{ID: f.refID, Hash: f.refHsh},
			Nonce:     newNonce(t), IssuedAt: f.now,
		}
		forged.Signature = ed25519.Sign(attacker, canonicalBytes(forged))
		if _, err := f.v.Verify(ctx, forged); !errors.Is(err, ErrForgedOrigin) {
			t.Fatalf("forja %d: erro = %v, quer ErrForgedOrigin", i, err)
		}
	}
	// A cadeia atribuível da vítima permanece VAZIA.
	if vh, _ := f.store.Head(ctx, "msg-verify:nhi:a"); vh != 0 {
		t.Fatalf("cadeia da vítima poluída: head=%d, quer 0", vh)
	}
	// Toda a forja foi para a quarentena.
	qh, _ := f.store.Head(ctx, partitionUnauth)
	if qh != flood {
		t.Fatalf("quarentena head=%d, quer %d", qh, flood)
	}
}

// TestVerify_SpanEmitted cobre o item de DoD "Spans OTel": a decisão de verificação
// emite um span [OpMessageVerify] com atributos não-secretos (origem clamada,
// acção, decisão, motivo), e NUNCA o payload.
func TestVerify_SpanEmitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("sucesso ⇒ decision=allow", func(t *testing.T) {
		t.Parallel()
		tr := &captureTracer{}
		f := newFixture(t, WithTracer(tr))
		secret := []byte("PAYLOAD-SECRETO")
		msg := f.signAs(t, f.now, func(m *Message) { m.Payload = secret })
		if _, err := f.v.Verify(ctx, msg); err != nil {
			t.Fatalf("verify: %v", err)
		}
		sp := tr.last()
		if sp == nil || sp.name != OpMessageVerify {
			t.Fatalf("span não emitido (%v)", sp)
		}
		if !sp.ended {
			t.Fatal("span não foi fechado (End)")
		}
		if d, _ := sp.attr(AttrDecision); d != decisionAllow {
			t.Fatalf("decisão do span %v, quer %q", d, decisionAllow)
		}
		if o, _ := sp.attr(AttrClaimedOrigin); o != "nhi:a" {
			t.Fatalf("origem do span %v", o)
		}
		if a, _ := sp.attr(AttrAction); a != "cap:summarize" {
			t.Fatalf("acção do span %v", a)
		}
		// Nenhum atributo transporta o payload secreto.
		sp.mu.Lock()
		defer sp.mu.Unlock()
		for k, v := range sp.attrs {
			if s, ok := v.(string); ok && bytes.Contains([]byte(s), secret) {
				t.Fatalf("payload secreto vazou no atributo %q", k)
			}
		}
	})

	t.Run("forja ⇒ decision=deny + reason", func(t *testing.T) {
		t.Parallel()
		tr := &captureTracer{}
		f := newFixture(t, WithTracer(tr))
		attacker := ed25519.NewKeyFromSeed(seedBytes(5))
		forged := f.validMsg(t)
		forged.Signature = ed25519.Sign(attacker, canonicalBytes(forged))
		if _, err := f.v.Verify(ctx, forged); !errors.Is(err, ErrForgedOrigin) {
			t.Fatalf("erro = %v, quer ErrForgedOrigin", err)
		}
		sp := tr.last()
		if d, _ := sp.attr(AttrDecision); d != decisionDeny {
			t.Fatalf("decisão do span %v, quer %q", d, decisionDeny)
		}
		if r, _ := sp.attr(AttrRejectReason); r != ReasonForgedOrigin {
			t.Fatalf("motivo do span %v, quer %q", r, ReasonForgedOrigin)
		}
	})
}

func TestVerify_SealFailureIsFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	// Substitui o sealer por um que falha sempre no Append.
	v, err := NewVerifier(f.reg, f.refs, failingStore{}, WithVerifierClock(func() time.Time { return clockT }))
	if err != nil {
		t.Fatal(err)
	}
	// Mensagem forjada → rejeitada; a selagem falha → ErrSealFailed JUNTADO, mas a
	// rejeição (ErrForgedOrigin) mantém-se (nunca vira aceitação).
	attacker := ed25519.NewKeyFromSeed(seedBytes(7))
	forged := f.validMsg(t)
	forged.Signature = ed25519.Sign(attacker, canonicalBytes(forged))
	_, err = v.Verify(ctx, forged)
	if !errors.Is(err, ErrForgedOrigin) {
		t.Fatalf("rejeição deve manter-se: %v", err)
	}
	if !errors.Is(err, ErrSealFailed) {
		t.Fatalf("falha de selagem deve ser sinalizada: %v", err)
	}
}

func TestVerify_NilDeps(t *testing.T) {
	t.Parallel()
	store := audit.NewMemStore()
	reg := newFakeRegistry()
	refs := newFakeRefs()
	if _, err := NewVerifier(nil, refs, store); !errors.Is(err, ErrNilDeps) {
		t.Fatalf("registry nil: %v", err)
	}
	if _, err := NewVerifier(reg, nil, store); !errors.Is(err, ErrNilDeps) {
		t.Fatalf("refs nil: %v", err)
	}
	if _, err := NewVerifier(reg, refs, nil); !errors.Is(err, ErrNilDeps) {
		t.Fatalf("sealer nil: %v", err)
	}
	var nilV *Verifier
	if _, err := nilV.Verify(context.Background(), Message{}); !errors.Is(err, ErrNilDeps) {
		t.Fatalf("verifier nil: %v", err)
	}
}

func TestVerify_RegistryBackendErrorFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.reg.err = errors.New("directório indisponível")
	_, err := f.v.Verify(ctx, f.validMsg(t))
	if !errors.Is(err, ErrUnknownOrigin) {
		t.Fatalf("backend em erro deve ser fail-closed: %v", err)
	}
	// Backend em erro ⇒ origem não autenticável ⇒ quarentena.
	assertSealedUnauth(t, f.store, "nhi:a", ReasonUnknownOrigin, f.refID)
}

// TestVerify_NoPayloadOrSecretInSeal garante que o registo selado NÃO contém o
// payload da mensagem (só metadados de responsabilização).
func TestVerify_NoPayloadOrSecretInSeal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	secret := []byte("PAYLOAD-SENSIVEL-NAO-DEVE-SER-SELADO")
	forged := Message{
		Origin: "nhi:a", Authority: []string{"cap:summarize"}, Action: "cap:summarize",
		Reference: Reference{ID: f.refID, Hash: f.refHsh}, Payload: secret,
		Nonce: newNonce(t), IssuedAt: f.now,
	}
	attacker := ed25519.NewKeyFromSeed(seedBytes(3))
	forged.Signature = ed25519.Sign(attacker, canonicalBytes(forged))
	_, _ = f.v.Verify(ctx, forged)

	// Forja ⇒ selada na quarentena (origem não autenticada).
	head, _ := f.store.Head(ctx, partitionUnauth)
	rec, _, _ := f.store.At(ctx, partitionUnauth, head)
	if bytes.Contains(rec.EntryHash, secret) {
		t.Fatal("payload sensível apareceu no hash selado")
	}
	// O payload não é um campo do AuditRecord: a garantia é estrutural. Confirma-se
	// que a referência selada é o id (não o conteúdo).
	if rec.Resource.Value != f.refID {
		t.Fatalf("resource selado %q, quer o id da referência", rec.Resource.Value)
	}
}
