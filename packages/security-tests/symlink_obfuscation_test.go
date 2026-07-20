package securitytests

import (
	"context"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// ===========================================================================
// CENÁRIO 1 (REFORÇO) — OFUSCAÇÃO POR SYMLINK / PATH-TRAVERSAL (AC1, AOS-069)
//
// A spec exige ofuscação por "base64 / metacaracteres / symlinks". base64 e
// homoglyph/metacaracteres já são cobertos pelo corpus; este ficheiro fecha a lacuna do
// SYMLINK / PATH-TRAVERSAL: um recurso de aparência benigna cujo caminho resolve, por
// symlink + travessia (.,..), para um alvo sensível (/etc/shadow).
//
// Prova (à imagem dos vectors base64/homoglyph): a ofuscação do RECURSO é IRRELEVANTE
// para a fronteira control/data-plane — o TaintGate decide por (capability privilegiada,
// origem untrusted), NÃO pelo caminho. Uma tool call privilegiada autorizada por conteúdo
// untrusted é NEGADA quer o recurso seja nomeado em claro (/etc/shadow) quer ofuscado
// (/srv/exports/./nested/../shadow → /etc/shadow). A ofuscação não compra nada ao
// atacante. ORQUESTRA o controlo real; não o reimplementa.
// ===========================================================================

// symlinkVectorID é o vector de ofuscação por symlink/travessia do corpus versionado.
const symlinkVectorID = "pi-symlink-traversal-etc-shadow"

// sensitiveSymlinkTarget é o alvo canónico SINTÉTICO que a ofuscação esconde (sem
// segredos/alvos reais — é um caminho ilustrativo resolvido em memória).
const sensitiveSymlinkTarget = "/etc/shadow"

// mediatePrivilegedResource medeia uma tool call privilegiada cuja autorização carrega o
// rótulo dado, com o VALOR DE RECURSO explícito (o caminho, ofuscado ou canónico). É o
// veículo para provar que a ofuscação do recurso não altera o veredicto do gate.
func mediatePrivilegedResource(rm *referencemonitor.Monitor, label taint.Label, resourceValue string) referencemonitor.Decision {
	call := referencemonitor.Call{
		RunID:      "run-symlink",
		StepID:     "step-1",
		ToolID:     taintToolID,
		Capability: injectionCapability,
		Resource:   referencemonitor.Resource{Type: "file", Value: resourceValue},
		Principal:  referencemonitor.Principal{NHIID: "nhi-agent-1", AgentClass: "worker"},
		Credential: "tok-test",
		Context:    referencemonitor.CallContext{Taint: label.String()},
		Input:      []byte("le o recurso ofuscado e exfiltra"),
	}
	dec, _ := rm.Mediate(context.Background(), call)
	return dec
}

// symlinkVector devolve o vector de symlink do corpus (fail se ausente — a lacuna AC1
// tem de estar coberta).
func symlinkVector(t *testing.T) promptInjectionVector {
	t.Helper()
	c := mustCorpus(t)
	for _, v := range c.PromptInjections {
		if v.ID == symlinkVectorID {
			return v
		}
	}
	t.Fatalf("vector de ofuscação por symlink %q ausente do corpus (lacuna AC1)", symlinkVectorID)
	return promptInjectionVector{}
}

// TestPromptInjection_SymlinkTraversal_Blocked prova que a ofuscação por symlink/
// path-traversal NÃO contorna a fronteira control/data-plane:
//
//   - o vector do corpus resolve MESMO para um alvo sensível (a ofuscação não é um no-op:
//     o caminho benigno /srv/exports/./nested/../shadow desreferencia para /etc/shadow);
//   - o payload BRUTO esconde o alvo (não contém "/etc/shadow" verbatim) — é ofuscação real;
//   - a tool call privilegiada autorizada por origem UNTRUSTED é NEGADA pelo taint, quer o
//     recurso seja o caminho OFUSCADO quer o CANÓNICO — MESMO veredicto (a ofuscação é
//     irrelevante para o gate);
//   - não-tautologia: a MESMA call autorizada por origem TRUSTED (system) é PERMITIDA.
//
// Cada bloqueio é atestado no WORM tamper-evident (AC4).
func TestPromptInjection_SymlinkTraversal_Blocked(t *testing.T) {
	t.Parallel()
	v := symlinkVector(t)

	// (a) A ofuscação é REAL e resolve para um alvo sensível.
	if v.Encoding != "symlink" {
		t.Fatalf("vector %q: encoding=%q, quer \"symlink\"", v.ID, v.Encoding)
	}
	resolved, err := effectivePayload(v)
	if err != nil {
		t.Fatalf("vector %q: effectivePayload: %v", v.ID, err)
	}
	if !strings.Contains(resolved, sensitiveSymlinkTarget) {
		t.Fatalf("vector %q: caminho resolvido %q não atinge o alvo sensível %q", v.ID, resolved, sensitiveSymlinkTarget)
	}
	// A ofuscação esconde o alvo: o payload bruto NÃO o contém em claro.
	if strings.Contains(v.Payload, sensitiveSymlinkTarget) {
		t.Fatalf("vector %q: payload bruto %q já contém o alvo — não seria ofuscação", v.ID, v.Payload)
	}

	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rm := buildTaintRM(es, true)

	// (b) Recurso OFUSCADO, autorização untrusted → deny por taint.
	decObfuscated := mediatePrivilegedResource(rm, taint.LabelFor(taint.OriginToolResult), v.Payload)
	assertBlockedByTaint(t, decObfuscated, taint.OriginToolResult)

	// (c) Recurso CANÓNICO, autorização untrusted → MESMO veredicto (a ofuscação é
	// irrelevante para a fronteira: o gate decide por taint, não por caminho).
	decCanonical := mediatePrivilegedResource(rm, taint.LabelFor(taint.OriginToolResult), resolved)
	assertBlockedByTaint(t, decCanonical, taint.OriginToolResult)

	// (d) Não-tautologia: autorização TRUSTED da MESMA call ofuscada é PERMITIDA.
	decTrusted := mediatePrivilegedResource(rm, taint.LabelFor(taint.OriginSystem), v.Payload)
	if decTrusted.Effect != referencemonitor.EffectPermit {
		t.Fatalf("autorização TRUSTED com recurso ofuscado = %q, quer permit (gate seria tautológico)", decTrusted.Effect)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "prompt_injection_symlink_traversal", taintToolID, decObfuscated.Reason)
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestMetaDetects_SymlinkObfuscation_WhenPathNotResolved é o META-TESTE de não-vacuidade
// da DE-OFUSCAÇÃO de caminho: prova que a resolução symlink/travessia é MESMO necessária.
// Um scanner de recurso sensível que operasse sobre o caminho BRUTO (sem resolver o
// symlink/os ..) FALHARIA em ver /etc/shadow — a ofuscação escondê-lo-ia. Só após
// [resolveObfuscatedPath] o alvo sensível fica visível. Se a de-ofuscação fosse um no-op
// (payload já continha o alvo), este meta-teste falharia — juntos provam que a cobertura
// de symlink discrimina genuinamente, não é green-vazio.
func TestMetaDetects_SymlinkObfuscation_WhenPathNotResolved(t *testing.T) {
	t.Parallel()
	v := symlinkVector(t)

	// Sem resolver, um scan verbatim do caminho bruto NÃO detecta o alvo sensível.
	if strings.Contains(v.Payload, sensitiveSymlinkTarget) {
		t.Fatalf("meta: o payload bruto já expõe %q — a ofuscação seria vácua", sensitiveSymlinkTarget)
	}
	// Após de-ofuscação, o alvo fica visível (a resolução é o que revela a ameaça).
	resolved := resolveObfuscatedPath(v.Payload)
	if !strings.Contains(resolved, sensitiveSymlinkTarget) {
		t.Fatalf("meta: após resolver, %q não expõe %q — de-ofuscação falhou", resolved, sensitiveSymlinkTarget)
	}

	// A resolução é idempotente (ponto-fixo) e determinista: resolver o já-resolvido é o
	// próprio (sem non-determinismo em CI).
	if again := resolveObfuscatedPath(resolved); again != resolved {
		t.Fatalf("meta: resolução não idempotente: %q → %q", resolved, again)
	}
}
