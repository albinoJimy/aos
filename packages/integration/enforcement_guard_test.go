package integration

import (
	"context"
	"crypto/ed25519"
	"os"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	network "github.com/aos-ref/substrate/sandbox/network"
)

// Este ficheiro é o GUARD-TEST fim-a-fim do enforcement do RM de produção do ápice
// (AOS-161): prova, através do ÚNICO caminho de execução ([referencemonitor.Monitor.Mediate]),
// que a cadeia REAL composta em AOS-154 NEGA — com atribuição ([Decision.DeniedBy]) — cada
// uma das cinco violações que as barreiras de segurança do AOS existem para cortar:
//
//	(a) chamada anónima (Credential vazio)          → nega em "identity" (ADR-003)
//	(b) token de issuer não-confiável / raiz forjada → nega em "identity" (AOS-005)
//	(c) capability privilegiada sob taint untrusted  → nega em "taint"    (ADR-005/AOS-069)
//	(d) egress fora da allowlist                      → nega em "egress"   (AOS-067)
//	(e) capability fora do escopo user∩classe        → nega em "scope"    (AOS-071)
//
// FRONTEIRA DE D4 (identidade demo-emitida). O token NHI é emitido por um [identity.Issuer]
// de TESTE (chave Ed25519 determinística) e o [identity.Verifier] confia nesse issuer via um
// trust anchor local. Isto prova a ESTRUTURA de enforcement (cada barreira nega a sua
// violação e a negação é atribuível), não a NÃO-FORJABILIDADE de produção — um IdP real e
// bundles de política assinados são AOS-156, gated por D4. O WIRING do RM de produção (a cadeia
// real aceite por [referencemonitor.NewProductionSecure], que RECUSA IdentityStub/EgressStub e
// exige ScopeGate+TaintGate activos) já é provado por AOS-154
// (TestSecuredRuntime_RealHookChain_SingleWORM); aqui prova-se o COMPORTAMENTO de negação de
// cada barreira wired.
//
// A cadeia deste guard-test — identity → taint → scope → egress — OMITE deliberadamente a
// revalidação (AOS-051) e o PDP (AOS-004), que fail-close ANTES destas barreiras a jusante
// (o RealChain de AOS-154 já prova a identidade a fail-close primeiro; um bundle de PDP
// assinado que PERMITA para lá alcançar taint/scope/egress fim-a-fim é AOS-156/D4). Isolá-las
// aqui é o que torna cada uma das cinco negações a jusante ALCANÇÁVEL e atribuível a UMA
// barreira. Todos os colaboradores são os construtores REAIS de AOS-154; nenhum é um stub.

const (
	enfIssuerID  = "iss:test-idp"               // issuer confiado pelo verifier do guard-test
	enfRogueID   = "iss:rogue-idp"              // issuer NÃO confiado (raiz forjada, cenário b)
	enfUserID    = "human:alice"                // humano responsável (raiz da cadeia de delegação)
	enfAgentID   = "agt-1"                      // NHI (act-as da raiz)
	enfClass     = "researcher"                 // classe do agente (eixo do escopo user∩classe)
	enfCapHTTP   = "cap:http.get"               // capability de rede (cenário d)
	enfCapVault  = "cap:vault.read"             // capability PRIVILEGIADA (cenário c)
	enfCapDanger = "cap:danger"                 // capability fora do tecto user∩classe (cenário e)
	enfEvilURL   = "https://evil.example/exfil" // destino FORA da allowlist embutida
)

// enfClock é o relógio DETERMINÍSTICO do guard-test (constante), partilhado pelo issuer e
// pelo verifier para que o token nunca seja visto como expirado/ainda-não-válido.
func enfClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// enfKeys deriva um par Ed25519 DETERMINÍSTICO a partir de um byte de seed (reprodutível,
// sem aleatoriedade num caminho de decisão).
func enfKeys(seedByte byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = seedByte
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return priv.Public().(ed25519.PublicKey), priv
}

// enfClasses é a política de classe do issuer: o TECTO-máximo de capabilities que a classe
// "researcher" pode emitir. Superset de todas as caps do teste — cada token estreita-o via a
// UserAuthority do pedido (autoridade = utilizador ∩ classe).
func enfClasses() map[string]identity.ClassPolicy {
	return map[string]identity.ClassPolicy{
		enfClass: {TTL: 5 * time.Minute, Scope: []string{enfCapHTTP, enfCapVault, enfCapDanger}},
	}
}

// enfMint emite um token NHI compacto para uma UserAuthority específica (o escopo do token =
// utilizador ∩ classe). Um token por cenário, escopado ao MÍNIMO: o [ScopeGate] nega como
// escalada qualquer autoridade RECLAMADA (o escopo do token, que o IdentityCheck resolve para
// Principal.Authority) que exceda o tecto user∩classe — logo escopar em excesso faria a
// negação (d) recair em "scope" em vez de "egress". Escopo mínimo mantém cada negação
// atribuível a UMA barreira.
func enfMint(t *testing.T, iss *identity.Issuer, userAuthority []string) string {
	t.Helper()
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID:        enfUserID,
		AgentID:       enfAgentID,
		AgentClass:    enfClass,
		PolicyRef:     "policy://researcher@1",
		UserAuthority: userAuthority,
	})
	if err != nil {
		t.Fatalf("Issue(%v): %v", userAuthority, err)
	}
	return tok.Compact
}

// enfFixture agrega os colaboradores REAIS (verifier + tokens + classificador privilegiado +
// fonte de autoridade user∩classe) partilhados pelo guard-test e pelo poison-test.
type enfFixture struct {
	verifier   *identity.Verifier
	privileged referencemonitor.StaticPrivilegedSet
	authority  *authz.StaticAuthoritySource
	tokVault   string // escopo {cap:vault.read} — cenário c
	tokHTTP    string // escopo {cap:http.get}   — cenário d
	tokDanger  string // escopo {cap:danger}     — cenário e
	forgedTok  string // token de issuer não-confiável — cenário b
}

// newEnfFixture constrói os colaboradores reais. O verifier confia SÓ em enfIssuerID (não no
// rogue), o classificador marca cap:vault.read privilegiada, e a fonte de autoridade dá a cada
// sujeito da cadeia (humano, NHI, classe) o tecto {cap:http.get} — EXCLUINDO cap:danger.
func newEnfFixture(t *testing.T) enfFixture {
	t.Helper()
	pub, priv := enfKeys(0x11)
	iss, err := identity.NewIssuer(enfIssuerID, priv, enfClasses(), identity.WithIssuerClock(enfClock()))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	// Issuer FORJADO (raiz não-confiável): chave/id distintos, ausente do trust anchor.
	_, roguePriv := enfKeys(0x99)
	rogue, err := identity.NewIssuer(enfRogueID, roguePriv, enfClasses(), identity.WithIssuerClock(enfClock()))
	if err != nil {
		t.Fatalf("NewIssuer(rogue): %v", err)
	}

	verifier := identity.NewVerifier(
		identity.WithTrustedIssuer(enfIssuerID, pub), // confia APENAS no issuer legítimo
		identity.WithVerifierClock(enfClock()),
	)

	// Tecto user∩classe: cada sujeito da cadeia de delegação (raiz humana → NHI) e a classe
	// do agente concedem {cap:http.get}. O escopo efectivo (dobra de intersecções) é
	// {cap:http.get}; cap:danger fica DE FORA (cenário e), cap:http.get DENTRO (cenário d).
	authority := authz.NewStaticAuthoritySource().
		Set(enfUserID, enfCapHTTP).
		Set(enfAgentID, enfCapHTTP).
		Set("agent:"+enfClass, enfCapHTTP)

	return enfFixture{
		verifier:   verifier,
		privileged: referencemonitor.NewStaticPrivilegedSet(enfCapVault),
		authority:  authority,
		tokVault:   enfMint(t, iss, []string{enfCapVault}),
		tokHTTP:    enfMint(t, iss, []string{enfCapHTTP}),
		tokDanger:  enfMint(t, iss, []string{enfCapDanger}),
		forgedTok:  enfMint(t, rogue, []string{enfCapHTTP}),
	}
}

// enfEgressHook constrói o hook de egress REAL (AOS-067) sobre a allowlist embutida, selando os
// bloqueios no WORM dado — o MESMO construtor que [NewSecuredRuntime] usa internamente.
func enfEgressHook(t *testing.T, worm audit.Store) *network.EgressHook {
	t.Helper()
	resolver, err := network.NewEmbeddedResolver()
	if err != nil {
		t.Fatalf("NewEmbeddedResolver: %v", err)
	}
	filter, err := network.NewEgressFilter(resolver, network.WithSecurityAuditSink(network.NewWORMSecuritySink(worm)))
	if err != nil {
		t.Fatalf("NewEgressFilter: %v", err)
	}
	hook, err := network.NewEgressHook(filter)
	if err != nil {
		t.Fatalf("NewEgressHook: %v", err)
	}
	return hook
}

// enfCall constrói um [referencemonitor.Call] base do run do guard-test.
func enfCall(step, credential, capability, taintLabel string, res referencemonitor.Resource) referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-enf",
		StepID:     step,
		ToolID:     "tool",
		Capability: capability,
		Credential: credential,
		Resource:   res,
		Context:    referencemonitor.CallContext{Taint: taintLabel},
	}
}

// TestApexEnforcement_FiveDenials é o guard-test central de AOS-161: através do RM de produção
// REAL (via [referencemonitor.NewProductionSecure], a cadeia identity→taint→scope→egress de
// AOS-154), prova as CINCO negações atribuíveis. Determinista e offline (sem rede, sem relógio
// de parede num caminho de decisão).
func TestApexEnforcement_FiveDenials(t *testing.T) {
	ctx := context.Background()
	fx := newEnfFixture(t)
	worm := audit.NewMemStore()

	// RM de produção com a cadeia REAL. NewProductionSecure RECUSA fail-closed uma cadeia com
	// IdentityStub/EgressStub ou sem ScopeGate+TaintGate activos — se algum colaborador fosse um
	// stub, esta construção falharia (é a garantia herdada de AOS-153/154).
	rm, err := referencemonitor.NewProductionSecure(fx.privileged,
		referencemonitor.WithHooks(
			identity.NewIdentityCheck(fx.verifier),       // identity (AOS-005) — resolve Principal
			referencemonitor.NewTaintGate(fx.privileged), // taint (AOS-069)
			referencemonitor.NewScopeGate(fx.authority),  // scope (AOS-071)
			enfEgressHook(t, worm),                       // egress (AOS-067)
		),
		referencemonitor.WithEventSink(audit.NewMediationSink(worm)),
	)
	if err != nil {
		t.Fatalf("NewProductionSecure (cadeia real) recusada: %v", err)
	}

	cases := []struct {
		name     string
		call     referencemonitor.Call
		deniedBy string
	}{
		{
			// (a) anónimo: sem Credential não há autoridade (ADR-003, proibição de round-robin
			// anónimo). Nega na 1ª barreira.
			name:     "anonima_sem_credential",
			call:     enfCall("a", "", enfCapHTTP, taint.StringTrusted, referencemonitor.Resource{}),
			deniedBy: "identity",
		},
		{
			// (b) issuer não-confiável / raiz forjada: token bem-formado mas assinado por um
			// issuer ausente do trust anchor → Verify rejeita (emissor desconhecido).
			name:     "issuer_nao_confiavel",
			call:     enfCall("b", fx.forgedTok, enfCapHTTP, taint.StringTrusted, referencemonitor.Resource{}),
			deniedBy: "identity",
		},
		{
			// (c) capability privilegiada sob taint untrusted: identidade PASSA (cap:vault.read no
			// escopo do token) e o TaintGate corta — só dados trusted originam acção privilegiada
			// (ADR-005). Corre ANTES do scope.
			name:     "privilegiada_sob_untrusted",
			call:     enfCall("c", fx.tokVault, enfCapVault, taint.StringUntrusted, referencemonitor.Resource{}),
			deniedBy: "taint",
		},
		{
			// (d) egress fora da allowlist: identidade + taint + scope PASSAM (cap:http.get no
			// escopo do token E no tecto user∩classe, taint trusted, não-privilegiada); o
			// EgressHook nega o destino ausente da allowlist embutida (AOS-067 default-deny).
			name:     "egress_fora_da_allowlist",
			call:     enfCall("d", fx.tokHTTP, enfCapHTTP, taint.StringTrusted, referencemonitor.Resource{Type: "url", Value: enfEvilURL}),
			deniedBy: "egress",
		},
		{
			// (e) capability fora do escopo user∩classe: identidade PASSA (cap:danger no escopo do
			// token) mas o ScopeGate nega — cap:danger excede o tecto efectivo {cap:http.get} da
			// intersecção utilizador∩classe (confused deputy bloqueado, AOS-071).
			name:     "fora_do_escopo_user_classe",
			call:     enfCall("e", fx.tokDanger, enfCapDanger, taint.StringTrusted, referencemonitor.Resource{}),
			deniedBy: "scope",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := rm.Mediate(ctx, tc.call)
			if err != nil {
				t.Fatalf("Mediate devolveu erro inesperado: %v", err)
			}
			if dec.Effect != referencemonitor.EffectDeny {
				t.Fatalf("efeito=%q, quero deny (a violação devia ser negada fail-closed)", dec.Effect)
			}
			if dec.DeniedBy != tc.deniedBy {
				t.Fatalf("DeniedBy=%q, quero %q (a negação tem de ser atribuível à barreira certa)", dec.DeniedBy, tc.deniedBy)
			}
			if dec.Permitted() {
				t.Fatalf("Permitted()=true numa negação — nenhum Permit devia ser mintado")
			}
		})
	}
}

// TestSelftestApexEnforcementBypassReddensGate é o TESTE-VENENO do enforcement do ápice
// (scripts/ci/selftest.sh, secção K). Só corre com AOS_APEX_SELFTEST=1. Reproduz o cenário (d)
// — egress a um destino fora da allowlist — com o controlo de egress CONTORNADO pelas DUAS
// mutações que o desligam (AOS-355):
//
//   - SUBSTITUIÇÃO: o [network.EgressHook] real trocado pelo [referencemonitor.EgressStub]
//     neutro (slot ocupado por um hook que permite sempre);
//   - OMISSÃO: o slot de egress ausente da cadeia por inteiro — a mutação que a guarda
//     antiga (que testava a PRESENÇA DO STUB) deixava passar.
//
// Ambas passam agora pela costura SANCIONADA, [referencemonitor.NewProductionSecure], e não
// pela via crua [referencemonitor.New]: é a via estrita que tem de as recusar, e é sobre ela
// que o veneno tem de incidir — um poison contra `New` cru nunca poderia detectar uma
// regressão na guarda.
//
// O veneno fica VERMELHO nos DOIS estados do mundo, que é o que o self-test exige:
//
//   - com a guarda intacta, [NewProductionSecure] RECUSA a cadeia mutada e não há Monitor
//     nenhum com que mediar — o t.Fatalf da construção falha o teste;
//   - com a guarda regredida, a construção passa, a mediação ADMITE o egress a
//     evil.example e a asserção (falsa) de que foi negado por "egress" falha o teste.
//
// Fora do self-test é ignorado (não polui a suite verde). Determinista, offline, sem rasto
// no repo.
func TestSelftestApexEnforcementBypassReddensGate(t *testing.T) {
	if os.Getenv("AOS_APEX_SELFTEST") != "1" {
		t.Skip("teste-veneno do self-test (correr com AOS_APEX_SELFTEST=1 via scripts/ci/selftest.sh)")
	}
	ctx := context.Background()
	fx := newEnfFixture(t)

	// As duas mutações que desligam o default-deny AOS-067. A cadeia base é a do guard-test
	// (identity → taint → scope → egress); o que varia é só o slot de egress.
	mutacoes := []struct {
		nome   string
		egress []referencemonitor.Hook // o que ocupa (ou não) o slot de egress
	}{
		{"substituicao_pelo_stub", []referencemonitor.Hook{referencemonitor.EgressStub{}}},
		{"omissao_do_slot", nil},
	}

	for _, mut := range mutacoes {
		t.Run(mut.nome, func(t *testing.T) {
			worm := audit.NewMemStore()
			hooks := append([]referencemonitor.Hook{
				identity.NewIdentityCheck(fx.verifier),
				referencemonitor.NewTaintGate(fx.privileged),
				referencemonitor.NewScopeGate(fx.authority),
			}, mut.egress...) // <-- egress default-deny CONTORNADO

			// Via SANCIONADA com a cadeia mutada. Com a guarda intacta isto RECUSA — e a
			// recusa faz o veneno ficar vermelho aqui mesmo, que é o resultado correcto.
			rm, err := referencemonitor.NewProductionSecure(fx.privileged,
				referencemonitor.WithHooks(hooks...),
				referencemonitor.WithEventSink(audit.NewMediationSink(worm)),
			)
			if err != nil {
				t.Fatalf("NewProductionSecure recusou a cadeia com egress contornado (%v): sem Monitor não há mediação — o gate do ápice fica VERMELHO, como o self-test exige", err)
			}
			// Só se chega aqui se a guarda tiver REGREDIDO. Tool registada para que, sem o
			// corte de egress, a acção alcance efectivamente o permit.
			if err := rm.Register("tool", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
				t.Fatalf("Register: %v", err)
			}
			dec, err := rm.Mediate(ctx, enfCall("d", fx.tokHTTP, enfCapHTTP, taint.StringTrusted, referencemonitor.Resource{Type: "url", Value: enfEvilURL}))
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			// Asserção do self-test: assevera (FALSAMENTE) que o egress a evil.example foi
			// BLOQUEADO. Sem hook de egress real foi ADMITIDO (DeniedBy != "egress") — esta
			// asserção FALHA de propósito, tornando o gate VERMELHO.
			if dec.DeniedBy != "egress" {
				t.Fatalf("egress a %q NÃO foi negado por egress (efeito=%q DeniedBy=%q, esperado no self-test): o default-deny AOS-067 estaria inactivo", enfEvilURL, dec.Effect, dec.DeniedBy)
			}
		})
	}
}
