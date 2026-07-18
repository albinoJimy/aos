package pdp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-088 — ciclo de vida da policy-as-code: changelog no audit hash-chained,
// rastreabilidade à versão assinada e fail-closed no reload adulterado.

// writeAllowlist sobrescreve a allowlist de capabilities do bundle com content.
func writeAllowlist(t testing.TB, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, capabilitiesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, capabilitiesDir, "allowlist.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// allowlistV2 adiciona a capability cap:http.get à classe agent-reader face à
// allowlist de referência — produz um diff de capabilities determinista.
const allowlistV2 = `{
  "schema_version": 1,
  "classes": {
    "agent-worker": { "capabilities": [ { "cap": "cap:http.post" }, { "cap": "cap:fs.read" } ] },
    "agent-reader": { "capabilities": [ { "cap": "cap:fs.read" }, { "cap": "cap:http.get" } ] }
  }
}`

// TestAOS088_ReloadAdulteradoMantemAnterior: um bundle adulterado no reload é
// REJEITADO fail-closed, a política ANTERIOR permanece activa (ActiveVersion não
// muda) e NENHUM changelog é selado (sem janela de política ausente). AC4.
func TestAOS088_ReloadAdulteradoMantemAnterior(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	store := audit.NewMemStore()
	p, err := Open(dir, WithReloadAuditSink(AuditReloadSink(store, DefaultPolicyPartition)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Assina uma versão mais recente e DEPOIS adultera o .cedar (content_hash deixa
	// de bater) — o bundle no disco está adulterado.
	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"),
		append(refPolicy(t), []byte("\n// tamper")...), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.Reload(ctx, ReloadRequest{Author: "attacker", Reason: "malicious"}); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("reload adulterado devia dar ErrSignatureInvalid, obtive %v", err)
	}
	// Política anterior mantida — sem janela de política ausente.
	if p.ActiveVersion() != "1.0.0" {
		t.Errorf("ActiveVersion()=%q apos reload adulterado, esperava 1.0.0", p.ActiveVersion())
	}
	// A decisão ainda é servida pela política anterior (fail-closed sem janela vazia).
	if d, err := p.Decide(ctx, httpPost()); err != nil || d.Effect != Permit {
		t.Errorf("politica anterior devia continuar a decidir: effect=%q err=%v", d.Effect, err)
	}
	// Nenhum changelog selado — o reload rejeitado não escreve no audit.
	if head, _ := store.Head(ctx, DefaultPolicyPartition); head != 0 {
		t.Errorf("reload rejeitado NAO devia selar changelog; head=%d", head)
	}
}

// TestAOS088_ChangelogNoAudit: uma alteração de política escreve um evento
// policy.changed na hash-chain WORM com versões (old→new), autor, motivo e o diff
// (ficheiro alterado + capability adicionada). AC2/AC5.
func TestAOS088_ChangelogNoAudit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	store := audit.NewMemStore()
	p, err := Open(dir, WithReloadAuditSink(AuditReloadSink(store, DefaultPolicyPartition)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Nova versão com a allowlist alterada (adiciona cap:http.get a agent-reader).
	writeAllowlist(t, dir, allowlistV2)
	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := p.Reload(ctx, ReloadRequest{Author: "gov@aos", Reason: "concede http.get ao reader"}); err != nil {
		t.Fatalf("Reload 1.1.0: %v", err)
	}

	// Exactamente um registo policy.changed na partição de política.
	head, err := store.Head(ctx, DefaultPolicyPartition)
	if err != nil || head != 1 {
		t.Fatalf("head da particao policy=%d err=%v, esperava 1", head, err)
	}
	rec, ok, err := store.At(ctx, DefaultPolicyPartition, 1)
	if err != nil || !ok {
		t.Fatalf("At(1): ok=%v err=%v", ok, err)
	}
	if rec.Decision != audit.DecisionAllow {
		t.Errorf("Decision=%q, esperava allow", rec.Decision)
	}
	if rec.Principal.NHIID != "gov@aos" {
		t.Errorf("autor (principal)=%q, esperava gov@aos", rec.Principal.NHIID)
	}
	if rec.PolicyVersion != "1.1.0" {
		t.Errorf("PolicyVersion=%q, esperava 1.1.0", rec.PolicyVersion)
	}
	if rec.Resource.Type != PolicyChangedEventType {
		t.Errorf("Resource.Type=%q, esperava %q", rec.Resource.Type, PolicyChangedEventType)
	}
	if len(rec.Obligations) != 1 || rec.Obligations[0].Type != PolicyChangedEventType {
		t.Fatalf("esperava 1 obligation policy.changed, obtive %+v", rec.Obligations)
	}
	ob := rec.Obligations[0]
	if ob.Params["old_version"] != "1.0.0" || ob.Params["new_version"] != "1.1.0" {
		t.Errorf("versoes no changelog=%q→%q, esperava 1.0.0→1.1.0", ob.Params["old_version"], ob.Params["new_version"])
	}
	if ob.Params["author"] != "gov@aos" || ob.Params["reason"] != "concede http.get ao reader" {
		t.Errorf("autor/motivo no changelog=%q/%q", ob.Params["author"], ob.Params["reason"])
	}
	// O diff identifica a capability adicionada e o ficheiro de allowlist alterado.
	if !containsLine(ob.Fields, "cap+ agent-reader: cap:http.get") {
		t.Errorf("diff nao regista a capability adicionada: %v", ob.Fields)
	}
	if !containsLine(ob.Fields, "file~ "+capabilitiesDir+"/allowlist.json") {
		t.Errorf("diff nao regista o ficheiro de allowlist alterado: %v", ob.Fields)
	}
}

// TestAOS088_Rastreabilidade: a política em runtime corresponde SEMPRE à versão
// assinada do bundle carregado (ActiveVersion == Manifest.PolicyVersion). AC3.
func TestAOS088_Rastreabilidade(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "2.3.1", priv)

	p, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rb, err := loadRawBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.ActiveVersion() != rb.Manifest.PolicyVersion || p.ActiveVersion() != "2.3.1" {
		t.Fatalf("ActiveVersion()=%q, esperava %q (Manifest)", p.ActiveVersion(), rb.Manifest.PolicyVersion)
	}

	// Após um reload, a rastreabilidade acompanha a nova versão assinada.
	if _, err := SignBundle(dir, "2.4.0", priv); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(context.Background(), ReloadRequest{Author: "gov@aos", Reason: "bump"}); err != nil {
		t.Fatalf("Reload 2.4.0: %v", err)
	}
	rb2, err := loadRawBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.ActiveVersion() != rb2.Manifest.PolicyVersion || p.ActiveVersion() != "2.4.0" {
		t.Fatalf("apos reload ActiveVersion()=%q, esperava 2.4.0", p.ActiveVersion())
	}
}

// TestAOS088_IntegridadeCadeia: o changelog encadeado no audit passa a
// verificação de hash após vários policy.changed; adulterar um registo selado é
// detectado por audit.Verify. Integridade tamper-evident.
func TestAOS088_IntegridadeCadeia(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	// mutableStore permite adulterar um registo já selado (o MemStore de produção é
	// append-only e clona defensivamente). Sela a cadeia com a MESMA génese/entry-hash
	// do audit real (audit.GenesisHash/ComputeEntryHash).
	store := &mutableStore{}
	p, err := Open(dir, WithReloadAuditSink(AuditReloadSink(store, DefaultPolicyPartition)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Duas alterações de política encadeadas.
	for _, v := range []string{"1.1.0", "1.2.0"} {
		if _, err := SignBundle(dir, v, priv); err != nil {
			t.Fatalf("SignBundle %s: %v", v, err)
		}
		if err := p.Reload(ctx, ReloadRequest{Author: "gov@aos", Reason: "rollout " + v}); err != nil {
			t.Fatalf("Reload %s: %v", v, err)
		}
	}

	head, err := store.Head(ctx, DefaultPolicyPartition)
	if err != nil || head != 2 {
		t.Fatalf("head=%d err=%v, esperava 2", head, err)
	}
	// A cadeia do changelog verifica de ponta a ponta.
	if err := audit.Verify(ctx, store, DefaultPolicyPartition, 1, head); err != nil {
		t.Fatalf("cadeia do changelog devia verificar: %v", err)
	}

	// Sanidade tamper-evident: mutar o conteúdo do 1.º registo selado quebra a
	// verificação (o EntryHash recomputado deixa de bater com o armazenado).
	store.recs[0].PolicyVersion = "9.9.9"
	if err := audit.Verify(ctx, store, DefaultPolicyPartition, 1, head); err == nil {
		t.Error("audit.Verify devia detectar o registo adulterado")
	}
}

// mutableStore é um audit.Store mínimo de teste (partição única) que sela a
// cadeia como o MemStore mas mantém os registos MUTÁVEIS, para exercitar a
// detecção de adulteração de audit.Verify.
type mutableStore struct {
	recs []audit.AuditRecord
}

func (s *mutableStore) Append(_ context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	var prev []byte
	if len(s.recs) == 0 {
		prev = audit.GenesisHash(rec.Partition)
		rec.AuditSeq = 1
	} else {
		last := s.recs[len(s.recs)-1]
		prev = last.EntryHash
		rec.AuditSeq = last.AuditSeq + 1
	}
	rec.PrevHash = prev
	rec.EntryHash = audit.ComputeEntryHash(prev, rec)
	s.recs = append(s.recs, rec)
	return rec, nil
}

func (s *mutableStore) Read(_ context.Context, _ string, from, to uint64) ([]audit.AuditRecord, error) {
	out := make([]audit.AuditRecord, 0, len(s.recs))
	for _, r := range s.recs {
		if r.AuditSeq >= from && r.AuditSeq <= to {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *mutableStore) Head(_ context.Context, _ string) (uint64, error) {
	if len(s.recs) == 0 {
		return 0, nil
	}
	return s.recs[len(s.recs)-1].AuditSeq, nil
}

func (s *mutableStore) At(_ context.Context, _ string, seq uint64) (audit.AuditRecord, bool, error) {
	for _, r := range s.recs {
		if r.AuditSeq == seq {
			return r, true, nil
		}
	}
	return audit.AuditRecord{}, false, nil
}

// recSpan/recTracer registam os spans e atributos emitidos, para asserir a
// instrumentação OTel do reload (DoD) sem um exportador real.
type recSpan struct {
	name  string
	attrs map[string]any
	ended bool
}

func (s *recSpan) SetAttribute(k string, v any)       { s.attrs[k] = v }
func (s *recSpan) SpanContext() otelgenai.SpanContext { return otelgenai.SpanContext{} }
func (s *recSpan) End()                               { s.ended = true }

type recTracer struct{ spans []*recSpan }

func (t *recTracer) StartSpan(ctx context.Context, op string) (context.Context, otelgenai.Span) {
	sp := &recSpan{name: op, attrs: map[string]any{}}
	t.spans = append(t.spans, sp)
	return ctx, sp
}

// TestAOS088_SpanReload: o carregamento/verificação de política emite um span
// "aos.policy.reload" com versões e resultado — applied num reload aceite,
// rejected + error.type num reload rejeitado. Nunca com a chave nem segredos. DoD.
func TestAOS088_SpanReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	tr := &recTracer{}
	p, err := Open(dir, WithTracer(tr))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Reload aceite → span applied com old/new e content_hash.
	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := p.Reload(ctx, ReloadRequest{Author: "gov@aos", Reason: "rollout"}); err != nil {
		t.Fatalf("Reload 1.1.0: %v", err)
	}
	if len(tr.spans) != 1 {
		t.Fatalf("esperava 1 span, obtive %d", len(tr.spans))
	}
	sp := tr.spans[0]
	if sp.name != opPolicyReload || !sp.ended {
		t.Errorf("span name=%q ended=%v", sp.name, sp.ended)
	}
	if sp.attrs[attrPolicyReloadResult] != reloadResultApplied {
		t.Errorf("result=%v, esperava applied", sp.attrs[attrPolicyReloadResult])
	}
	if sp.attrs[attrPolicyVersionOld] != "1.0.0" || sp.attrs[attrPolicyVersionNew] != "1.1.0" {
		t.Errorf("versoes no span=%v→%v", sp.attrs[attrPolicyVersionOld], sp.attrs[attrPolicyVersionNew])
	}
	if _, ok := sp.attrs[attrPolicyContentHash]; !ok {
		t.Error("span sem content_hash")
	}
	// Nenhum atributo pode conter a chave privada/anchor (defesa em profundidade).
	for k, v := range sp.attrs {
		if s, ok := v.(string); ok && len(s) > 0 && (k == "signing.key" || contains(s, "PRIVATE")) {
			t.Errorf("atributo suspeito de segredo: %s=%q", k, s)
		}
	}

	// Reload rejeitado (versão não-crescente) → span rejected + error.type.
	if _, err := SignBundle(dir, "1.0.5", priv); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx, ReloadRequest{}); !errors.Is(err, ErrStalePolicyVersion) {
		t.Fatalf("esperava ErrStalePolicyVersion, obtive %v", err)
	}
	if len(tr.spans) != 2 {
		t.Fatalf("esperava 2 spans, obtive %d", len(tr.spans))
	}
	rej := tr.spans[1]
	if rej.attrs[attrPolicyReloadResult] != reloadResultRejected {
		t.Errorf("result=%v, esperava rejected", rej.attrs[attrPolicyReloadResult])
	}
	if rej.attrs[otelgenai.AttrErrorType] != ErrStalePolicyVersion.Code {
		t.Errorf("error.type=%v, esperava %q", rej.attrs[otelgenai.AttrErrorType], ErrStalePolicyVersion.Code)
	}
}

// TestAOS088_DiffVazioEmReassinatura: re-assinar o MESMO conteúdo com nova versão
// produz um diff vazio (nada mudou nas regras/capabilities), mas ainda regista o
// changelog com a transição de versão.
func TestAOS088_DiffVazioEmReassinatura(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	var ev PolicyChangeEvent
	p, err := Open(dir, WithReloadAudit(func(e PolicyChangeEvent) { ev = e }))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Só a versão muda; regras e allowlist ficam iguais.
	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(context.Background(), ReloadRequest{Author: "gov@aos", Reason: "re-assinatura"}); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !ev.Diff.Empty() {
		t.Errorf("diff devia ser vazio numa re-assinatura sem alteracoes, obtive %+v", ev.Diff)
	}
	if ev.OldVersion != "1.0.0" || ev.NewVersion != "1.1.0" {
		t.Errorf("transicao=%q→%q", ev.OldVersion, ev.NewVersion)
	}
}

// TestAOS088_SinkDefaults cobre os ramos defensivos do adaptador: partição vazia
// resolve para DefaultPolicyPartition e um store nil é no-op (sem panic).
func TestAOS088_SinkDefaults(t *testing.T) {
	t.Parallel()
	ev := PolicyChangeEvent{OldVersion: "1.0.0", NewVersion: "1.1.0", Author: "a", Reason: "r", ContentHash: "h"}
	rec := BuildPolicyChangedRecord(ev, "")
	if rec.Partition != DefaultPolicyPartition {
		t.Errorf("particao=%q, esperava %q", rec.Partition, DefaultPolicyPartition)
	}
	if rec.Capability != policyReloadCapability || rec.PolicyVersion != "1.1.0" {
		t.Errorf("capability/versao=%q/%q", rec.Capability, rec.PolicyVersion)
	}
	// Store nil: o sink não deve entrar em pânico nem falhar (devolve nil).
	if err := AuditReloadSink(nil, "")(ev); err != nil {
		t.Errorf("sink com store nil devia ser no-op sem erro, obtive %v", err)
	}

	// errCode de um erro genérico (não *Error) resolve para "error".
	if got := errCode(errors.New("boom")); got != "error" {
		t.Errorf("errCode(generico)=%q, esperava error", got)
	}
}

// errStore é um audit.Store cujo Append falha SEMPRE — modela um Store WORM
// durável indisponível (I/O, disco cheio, backend em baixo).
type errStore struct{}

func (errStore) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("worm indisponivel")
}
func (errStore) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}
func (errStore) Head(context.Context, string) (uint64, error) { return 0, nil }
func (errStore) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}

// TestAOS088_SelagemFalhadaAnotadaNoSpan: quando a selagem do changelog falha (o
// Store WORM está indisponível), a falha NÃO é engolida — o reload aplica-se (sem
// janela de política ausente, AC4) MAS o span do reload anota audit_sealed=false +
// o erro, tornando detectável uma alteração de política sem changelog selado
// (fecha o fail-open silencioso do AC2/AC5).
func TestAOS088_SelagemFalhadaAnotadaNoSpan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	tr := &recTracer{}
	p, err := Open(dir, WithTracer(tr),
		WithReloadAuditSink(AuditReloadSink(errStore{}, DefaultPolicyPartition)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	// O reload APLICA-SE apesar de a selagem falhar (sem janela vazia).
	if err := p.Reload(ctx, ReloadRequest{Author: "gov@aos", Reason: "rollout"}); err != nil {
		t.Fatalf("Reload devia aplicar-se apesar da selagem falhar, obtive %v", err)
	}
	if p.ActiveVersion() != "1.1.0" {
		t.Errorf("ActiveVersion()=%q, esperava 1.1.0 (reload aplicado)", p.ActiveVersion())
	}
	// Mas a falha de selagem ficou observável no span (não engolida).
	if len(tr.spans) != 1 {
		t.Fatalf("esperava 1 span, obtive %d", len(tr.spans))
	}
	sp := tr.spans[0]
	if sp.attrs[attrPolicyAuditSealed] != false {
		t.Errorf("%s=%v, esperava false (selagem falhou)", attrPolicyAuditSealed, sp.attrs[attrPolicyAuditSealed])
	}
	if _, ok := sp.attrs[attrPolicyAuditSealError]; !ok {
		t.Errorf("span sem %s apos selagem falhada", attrPolicyAuditSealError)
	}
}

// TestAOS088_SelagemBemSucedidaAnotadaNoSpan: o caminho feliz anota
// audit_sealed=true quando o changelog é selado com sucesso.
func TestAOS088_SelagemBemSucedidaAnotadaNoSpan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := newKeypair(t)
	writeSignedDir(t, dir, "1.0.0", priv)

	tr := &recTracer{}
	store := audit.NewMemStore()
	p, err := Open(dir, WithTracer(tr),
		WithReloadAuditSink(AuditReloadSink(store, DefaultPolicyPartition)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	if _, err := SignBundle(dir, "1.1.0", priv); err != nil {
		t.Fatalf("SignBundle 1.1.0: %v", err)
	}
	if err := p.Reload(ctx, ReloadRequest{Author: "gov@aos", Reason: "rollout"}); err != nil {
		t.Fatalf("Reload 1.1.0: %v", err)
	}
	if len(tr.spans) != 1 {
		t.Fatalf("esperava 1 span, obtive %d", len(tr.spans))
	}
	if got := tr.spans[0].attrs[attrPolicyAuditSealed]; got != true {
		t.Errorf("%s=%v, esperava true (selagem OK)", attrPolicyAuditSealed, got)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
