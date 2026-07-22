// Command aos é o NÓ de PRODUÇÃO do AOS (AOS-163, EPIC-15): gradua o ápice mínimo
// single-process de cmd/aos-demo para a via de PRODUÇÃO. Onde o demo compõe o
// Reference Monitor com STUBS NEUTROS (sem enforcement) e o canal de controlo com o
// HMACAuthenticator DEMO-GRADE (replayável, sem frescura), o nó `aos` compõe:
//
//   - a CADEIA REAL via integration.NewSecuredRuntime (RM de produção, WORM único),
//     com o VERIFIER REAL da autoridade de identidade AOS-156
//     (integration.NewVerifierFromAuthority) — NUNCA o identity.NewVerifier() sem
//     anchors nem o referencemonitor.IdentityStub do demo;
//   - o SteerChannel sobre o Ed25519Authenticator de AOS-160 (assinatura ed25519 +
//     anti-replay durável + frescura) — NUNCA o control.HMACAuthenticator do demo;
//   - (opcionalmente) o FourEyesGate de AOS-162 para as aprovações irreversíveis.
//
// FRONTEIRA de escopo (AOS-163). Este ticket é o BOOTSTRAP: compõe a via de segurança/
// identidade REAL e falha-fechado. O loop de serviço long-running (hospedar N runs,
// shutdown gracioso) é AOS-164; o substrato DURÁVEL (Event Store/WORM persistentes) é
// AOS-170 — aqui o substrato de referência é in-memory (declarado no arranque). A
// attestation física (AAGUID/WebAuthn) e o Model Gateway real ficam FORA (EPIC-06).
//
// DOIS MODOS de identidade (a chave nunca entra na CADEIA DE SEGURANÇA em nenhum):
//
//   - REFERÊNCIA (single-process, default): a autoridade de identidade AOS-156 é
//     CO-LOCALIZADA no processo (detém a chave, encapsulada e nunca devolvida). A
//     não-forjabilidade relativa ao nó é só PARCIAL — código comprometido in-process pode
//     mintar. O banner AVISA-o explicitamente e aponta o modo endurecido.
//   - ENDURECIDO (trust-anchor-only, Config.IssuerPubKey): o nó recebe APENAS o trust
//     anchor (issuerID + pubkey); a autoridade/chave vivem FORA do processo. NENHUMA chave
//     de assinatura entra no runtime do nó ⇒ fecha DE FACTO a não-forjabilidade relativa
//     ao nó de AOS-156. Node.Authority é nil (o mint corre out-of-process).
//
// REGRAS honradas: SEM segredos hardcoded (as chaves de assinatura vêm de config/vault
// ou, no modo de referência, são geradas por CSPRNG e nunca impressas); FAIL-CLOSED
// (um colaborador de identidade obrigatório em falta ABORTA o arranque; o modo endurecido
// recusa uma IssuerSigningKey in-process); a chave de assinatura do issuer/operador NUNCA
// entra na cadeia de segurança do nó — só o verifier (pubkey) e as pubkeys dos operadores.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// IdentityModeReal é o modo de identidade EM VIGOR do nó de produção de REFERÊNCIA: o
// verifier tem como trust anchor a PUBKEY da autoridade AOS-156, mas a autoridade é
// CO-LOCALIZADA no processo (detém a chave de assinatura). A não-forjabilidade relativa
// ao nó só é PARCIAL neste modo — código comprometido in-process pode mintar. É declarado
// no arranque e nos logs (AC3), com AVISO explícito.
const IdentityModeReal = "real"

// IdentityModeRealHardened é o modo de identidade ENDURECIDO (trust-anchor-only): o nó
// recebe APENAS o trust anchor (issuerID + pubkey) e compõe só o verifier; NENHUMA chave
// de assinatura entra no processo do nó e não há autoridade in-process (o mint acontece
// FORA do processo). Fecha de facto a não-forjabilidade RELATIVA AO NÓ de AOS-156 (AC3b).
const IdentityModeRealHardened = "real-trust-anchor-only"

// Erros de bootstrap (todos fail-closed). A ausência de um colaborador de IDENTIDADE
// obrigatório ABORTA o arranque — o nó nunca sobe num modo em que a identidade real
// não estaria ligada.
var (
	// ErrNoIssuerID — sem IssuerID não há trust anchor da autoridade AOS-156, logo o
	// verifier real não pode ser construído. Fail-closed.
	ErrNoIssuerID = errors.New("aos: config sem IssuerID (a identidade real exige o trust anchor do issuer AOS-156)")
	// ErrNoHumans — sem humanos autorizados a autoridade de identidade não teria quem
	// autenticar na raiz da cadeia de delegação: nenhum token legítimo seria mintável.
	// Fail-closed (não se sobe um nó de identidade sem principal humano algum). Só se
	// aplica ao modo de REFERÊNCIA (autoridade co-localizada); no modo endurecido
	// (trust-anchor-only) o directório de humanos vive FORA do processo com a autoridade.
	ErrNoHumans = errors.New("aos: config sem humanos autorizados (a autoridade de identidade nao teria quem autenticar)")
	// ErrConflictingIssuerKey — config que pede o modo endurecido (IssuerPubKey definida)
	// MAS também fornece uma IssuerSigningKey. Fail-closed: no trust-anchor-only NENHUMA
	// chave de assinatura pode entrar no processo do nó — uma chave presente derrotaria a
	// própria propriedade (não-forjabilidade relativa ao nó) que o modo garante.
	ErrConflictingIssuerKey = errors.New("aos: modo endurecido (IssuerPubKey) nao pode receber IssuerSigningKey — nenhuma chave de assinatura entra no runtime do no")
)

// ApproverConfig regista UM aprovador do FourEyesGate (AOS-162): o principal, a sua
// PUBKEY pinada (a privada vive no dispositivo do humano — nunca aqui) e a autoridade
// autoritativa (capabilities approve:<classe> que detém).
type ApproverConfig struct {
	Principal string
	PubKey    ed25519.PublicKey
	Authority []string
}

// Config é a configuração MÍNIMA e EXPLÍCITA do nó `aos` (AC2). SEM segredos em código:
// as chaves de assinatura (issuer/operadores) vêm de config/vault pelo chamador; o nó
// só detém verifier + pubkeys. Os colaboradores de IDENTIDADE são obrigatórios
// (fail-closed); os NÃO-identidade têm defaults de REFERÊNCIA para o nó arrancar.
type Config struct {
	// --- Identidade REAL (AOS-156) — OBRIGATÓRIA -------------------------------
	// IssuerID identifica a autoridade de identidade (== trust anchor do verifier).
	IssuerID string
	// IssuerClasses é a política por classe de agente (TTL + escopo-máximo).
	IssuerClasses map[string]identity.ClassPolicy
	// Humans é a allowlist de humanos autorizados (a porta de autenticação humana de
	// referência — DEMO-GRADE-AUTH; a real OIDC/WebAuthn é a porta a preencher). Pelo
	// menos um é exigido (fail-closed).
	Humans []string
	// IssuerSigningKey é a chave de assinatura ed25519 do issuer, tipicamente carregada
	// de config/vault. nil ⇒ GERADA por CSPRNG (modo de referência) — NUNCA hardcoded.
	// Em qualquer caso, a chave vive encapsulada na autoridade e NUNCA entra na cadeia
	// de segurança do nó (que só recebe o verifier/pubkey). Ignorada (e proibida:
	// [ErrConflictingIssuerKey]) quando IssuerPubKey activa o modo endurecido.
	IssuerSigningKey ed25519.PrivateKey
	// IssuerPubKey, quando fornecida, activa o modo ENDURECIDO (trust-anchor-only): o nó
	// compõe o verifier a partir de APENAS (IssuerID + esta pubkey), SEM autoridade de
	// emissão in-process. NENHUMA chave de assinatura do issuer entra no runtime do nó —
	// fecha DE FACTO a não-forjabilidade relativa ao nó de AOS-156 (AC3b): código
	// comprometido in-process não tem material com que mintar. Neste modo [Node.Authority]
	// é nil (o mint corre FORA do processo, na autoridade endurecida) e [Config.Humans]
	// não é exigida (o directório de humanos vive com a autoridade externa). nil ⇒ o nó
	// cai no modo de REFERÊNCIA (autoridade co-localizada). Mutuamente exclusiva com
	// IssuerSigningKey.
	IssuerPubKey ed25519.PublicKey

	// --- Canal de controlo autenticado (AOS-160) -------------------------------
	// Operators mapeia emitterID→PUBKEY dos operadores humanos/serviço autorizados a
	// emitir sinais de controlo (steer/pause/resume). Só PUBKEYS — a privada assina no
	// processo do operador. Vazio ⇒ default-deny (nenhum sinal autentica até haver
	// operador registado).
	Operators map[string]ed25519.PublicKey
	// SteerTTL é a janela de frescura dos sinais de controlo. <=0 ⇒ default 5min.
	SteerTTL time.Duration
	// SteerSkew tolera carimbos ligeiramente no futuro (relógios adiantados). Default 0.
	SteerSkew time.Duration

	// --- Four-eyes / dual-control (AOS-162) — OPCIONAL --------------------------
	// Approvers regista os aprovadores do FourEyesGate. Vazio ⇒ o gate não é composto.
	Approvers []ApproverConfig

	// --- Colaboradores NÃO-identidade (defaults de REFERÊNCIA) -----------------
	// O foco de AOS-163 é a composição de SEGURANÇA/IDENTIDADE real; estes podem vir por
	// config OU cair para um default de referência para o nó arrancar. O Model Gateway
	// real é EPIC-06.
	Model       agentruntime.ModelClient
	Catalog     toolset.Catalog
	Revalidator *revalidation.Revalidator
	Policy      integration.PolicyProvider

	// --- Substrato (durável = AOS-170; referência in-memory) -------------------
	// EventStore é a espinha append-only partilhada (turnos + sinais de controlo +
	// nonce-store anti-replay). nil ⇒ um in-memory de referência (não-durável).
	EventStore *eventstore.Store
	// WORM é o audit.Store tamper-evident único do RM. nil ⇒ um in-memory de referência.
	WORM audit.Store

	// --- Relógios injectáveis (testes determinísticos) -------------------------
	IssuerClock   func() time.Time
	VerifierClock func() time.Time
	SteerClock    func() time.Time
}

// Node é o nó `aos` composto: a superfície de PRODUÇÃO pronta a hospedar runs (o loop
// de serviço é AOS-164). Expõe os colaboradores compostos para o loop/CLI/API os
// conduzirem. A Authority é co-localizada no nó de REFERÊNCIA (single-process); num
// deployment endurecido corre FORA do processo e o nó recebe só o trust anchor.
type Node struct {
	// Runtime é o SecuredRuntime (cadeia REAL + verifier REAL). O único caminho de
	// execução de tools é o seu Reference Monitor (no-bypass).
	Runtime *integration.SecuredRuntime
	// Steer é o canal de controlo out-of-band sobre o Ed25519Authenticator (AOS-160).
	Steer *control.SteerChannel
	// FourEyes é o gate de dual-control (AOS-162); nil se não configurado.
	FourEyes *integration.FourEyesGate
	// Authority é a autoridade de identidade AOS-156 (detém a chave, encapsulada). No
	// nó de referência serve a superfície de emissão (mint) do operador/admin. É NIL no
	// modo endurecido (trust-anchor-only): a autoridade corre FORA do processo e o nó
	// recebe só o trust anchor (nenhuma chave de assinatura in-process).
	Authority *integration.IssuerAuthority
	// Verifier é o verifier REAL derivado do trust anchor da autoridade (só pubkey).
	Verifier *identity.Verifier
	// SteerAuth é o autenticador ed25519 do canal de controlo (só pubkeys).
	SteerAuth *integration.Ed25519Authenticator
	// EventStore e WORM são o substrato partilhado.
	EventStore *eventstore.Store
	WORM       audit.Store
	// IdentityMode é o modo de identidade em vigor (sempre IdentityModeReal aqui).
	IdentityMode string

	ownsEventStore bool
}

// Bootstrap compõe o nó `aos` de PRODUÇÃO a partir de cfg, escrevendo o banner de
// arranque (que DECLARA o modo de identidade em vigor) em logw. Fail-closed: um
// colaborador de identidade obrigatório em falta aborta ANTES de qualquer composição.
//
// A ordem de composição:
//
//  1. valida a config de identidade (fail-closed);
//  2. substrato (Event Store + WORM), com defaults de referência in-memory;
//  3. autoridade de identidade AOS-156 + VERIFIER REAL (NewVerifierFromAuthority);
//  4. Ed25519Authenticator (AOS-160) + SteerChannel — HMAC demo DESLIGADO;
//  5. FourEyesGate (AOS-162) opcional;
//  6. colaboradores não-identidade (defaults de referência);
//  7. SecuredRuntime (cadeia REAL) com o verifier REAL ligado;
//  8. declara o modo de identidade REAL no log.
func Bootstrap(_ context.Context, cfg Config, logw io.Writer) (*Node, error) {
	log := func(format string, args ...any) {
		if logw != nil {
			fmt.Fprintf(logw, "[aos] "+format+"\n", args...)
		}
	}

	// (1) FAIL-CLOSED na config de identidade — antes de compor o que quer que seja. Dois
	// modos: ENDURECIDO (trust-anchor-only, IssuerPubKey definida) vs REFERÊNCIA (autoridade
	// co-localizada). O IssuerID (trust anchor) é obrigatório em ambos.
	if cfg.IssuerID == "" {
		return nil, ErrNoIssuerID
	}
	hardened := len(cfg.IssuerPubKey) > 0
	if hardened {
		// No modo endurecido NENHUMA chave de assinatura pode entrar no processo do nó:
		// uma IssuerSigningKey presente derrotaria a propriedade que o modo garante.
		if cfg.IssuerSigningKey != nil {
			return nil, ErrConflictingIssuerKey
		}
	} else {
		// No modo de referência a autoridade é co-localizada e minta a partir da allowlist
		// de humanos — sem humano algum não haveria raiz de delegação legítima.
		if len(cfg.Humans) == 0 {
			return nil, ErrNoHumans
		}
	}

	// (2) SUBSTRATO. Durabilidade real = AOS-170; aqui, defaults in-memory de referência.
	es := cfg.EventStore
	ownsES := false
	if es == nil {
		created, err := eventstore.New()
		if err != nil {
			return nil, fmt.Errorf("aos: event store de referência: %w", err)
		}
		es = created
		ownsES = true
	}
	worm := cfg.WORM
	if worm == nil {
		worm = audit.NewMemStore()
	}

	// (3) IDENTIDADE REAL. Em AMBOS os modos o que entra na CADEIA DE SEGURANÇA é SÓ o
	// verifier (pubkey) — nunca uma chave de assinatura. A diferença é ONDE vive a
	// autoridade de emissão:
	//   - ENDURECIDO (trust-anchor-only): o nó recebe só (IssuerID+pubkey) e compõe o
	//     verifier directamente; NENHUMA autoridade/chave in-process (authority = nil). O
	//     mint corre FORA do processo. Fecha a não-forjabilidade relativa ao nó (AC3b).
	//   - REFERÊNCIA: a autoridade é CO-LOCALIZADA (detém a chave, encapsulada e nunca
	//     devolvida) e o nó deriva o verifier do seu trust anchor.
	var verifierOpts []identity.VerifierOption
	if cfg.VerifierClock != nil {
		verifierOpts = append(verifierOpts, identity.WithVerifierClock(cfg.VerifierClock))
	}
	var authority *integration.IssuerAuthority
	var verifier *identity.Verifier
	var err error
	if hardened {
		// VERIFIER REAL a partir de APENAS o trust anchor injectado — sem autoridade no
		// processo. Cópia defensiva da pubkey (o chamador não a muta sob os nossos pés).
		anchor := append(ed25519.PublicKey(nil), cfg.IssuerPubKey...)
		base := append([]identity.VerifierOption{identity.WithTrustedIssuer(cfg.IssuerID, anchor)}, verifierOpts...)
		verifier = identity.NewVerifier(base...)
	} else {
		var issuerOpts []identity.IssuerOption
		if cfg.IssuerClock != nil {
			issuerOpts = append(issuerOpts, identity.WithIssuerClock(cfg.IssuerClock))
		}
		authority, err = integration.NewIssuerAuthority(integration.AuthorityConfig{
			IssuerID:      cfg.IssuerID,
			Classes:       cfg.IssuerClasses,
			Directory:     integration.NewAllowlistDirectory(cfg.Humans...),
			SigningKey:    cfg.IssuerSigningKey,
			IssuerOptions: issuerOpts,
		})
		if err != nil {
			return nil, fmt.Errorf("aos: autoridade de identidade (AOS-156): %w", err)
		}
		// VERIFIER REAL: só o trust anchor (issuerID + pubkey) da autoridade. NUNCA o
		// identity.NewVerifier() sem anchors nem o IdentityStub do demo.
		verifier = integration.NewVerifierFromAuthority(authority, verifierOpts...)
	}

	// (4) CANAL DE CONTROLO AUTENTICADO (AOS-160). Ed25519Authenticator sobre o
	// nonce-store durável (EventStoreNonceStore de AOS-159) + frescura. HMAC demo NÃO
	// é usado. As pubkeys dos operadores vêm de config (só material público).
	ttl := cfg.SteerTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	var edOpts []integration.Ed25519Option
	if cfg.SteerClock != nil {
		edOpts = append(edOpts, integration.WithEd25519Clock(cfg.SteerClock))
	}
	if cfg.SteerSkew > 0 {
		edOpts = append(edOpts, integration.WithEd25519Skew(cfg.SteerSkew))
	}
	steerAuth, err := integration.NewEd25519Authenticator(hitl.NewEventStoreNonceStore(es), ttl, edOpts...)
	if err != nil {
		return nil, fmt.Errorf("aos: authenticator ed25519 do canal de controlo (AOS-160): %w", err)
	}
	for id, pub := range cfg.Operators {
		steerAuth.Register(id, pub)
	}
	steer, err := control.NewChannel(es, steerAuth)
	if err != nil {
		return nil, fmt.Errorf("aos: steer channel: %w", err)
	}

	// (5) FOUR-EYES GATE (AOS-162) — opcional. Composto quando há aprovadores em config.
	var foureyes *integration.FourEyesGate
	if len(cfg.Approvers) > 0 {
		registry := hitl.NewMemApproverRegistry()
		for _, a := range cfg.Approvers {
			registry.Register(a.Principal, a.PubKey, a.Authority...)
		}
		foureyes, err = integration.NewFourEyesGate(registry, hitl.NewEventStoreNonceStore(es))
		if err != nil {
			return nil, fmt.Errorf("aos: four-eyes gate (AOS-162): %w", err)
		}
	}

	// (6) COLABORADORES NÃO-IDENTIDADE — defaults de REFERÊNCIA (o foco é a identidade).
	model := cfg.Model
	if model == nil {
		model = referenceModel{}
	}
	catalog := cfg.Catalog
	if catalog == nil {
		catalog = emptyCatalog{}
	}
	policy := cfg.Policy
	if policy == nil {
		policy = integration.StaticPolicy{MaxEgress: domain.EgressExternal}
	}
	revalidator := cfg.Revalidator
	if revalidator == nil {
		revalidator, err = referenceRevalidator()
		if err != nil {
			return nil, fmt.Errorf("aos: revalidador de referência: %w", err)
		}
	}

	// (7) SECURED RUNTIME — a CADEIA REAL (via NewProductionSecure), com o VERIFIER REAL
	// ligado. Fail-closed: um colaborador obrigatório em falta é recusado aqui.
	sec, err := integration.NewSecuredRuntime(integration.SecuredConfig{
		Model:       model,
		Recorder:    agentruntime.NewTurnRecorder(es),
		Catalog:     catalog,
		Revalidator: revalidator,
		Policy:      policy,
		WORM:        worm,
		Verifier:    verifier, // <-- REAL (AOS-156): nunca o IdentityStub nem o default sem anchors
	})
	if err != nil {
		if ownsES {
			_ = es.Close()
		}
		return nil, fmt.Errorf("aos: secured runtime (cadeia real de produção): %w", err)
	}

	// (8) DECLARAÇÃO do modo de identidade EM VIGOR (AC3). O modo é declarado SEM ambiguidade
	// e, no modo de referência, com AVISO explícito de que a autoridade é co-localizada — a
	// honestidade do banner não pode deixar um operador confundir referência com produção
	// endurecida (AC3b/AC3c).
	identityMode := IdentityModeReal
	if hardened {
		identityMode = IdentityModeRealHardened
	}
	if hardened {
		log("no `aos` a arrancar — modo de IDENTIDADE: REAL ENDURECIDO (trust-anchor-only, issuer=%q)", cfg.IssuerID)
		log("autoridade de identidade FORA do processo: NENHUMA chave de assinatura entra no runtime do no")
		log("=> nao-forjabilidade RELATIVA AO NO garantida (AOS-156): codigo comprometido in-process nao tem material com que mintar")
	} else {
		issuerID, _ := authority.TrustAnchor()
		log("no `aos` a arrancar — modo de IDENTIDADE: REAL (trust-anchor da autoridade AOS-156, issuer=%q)", issuerID)
		log("AVISO: MODO DE REFERENCIA single-process — a autoridade de identidade e CO-LOCALIZADA no processo do no")
		log("=> a nao-forjabilidade RELATIVA AO NO so vale com autoridade OUT-OF-PROCESS: use Config.IssuerPubKey (trust-anchor-only) num deployment endurecido")
		log("humanos autorizados na autoridade: %d (allowlist de referencia — OIDC/WebAuthn e a porta a preencher)", len(cfg.Humans))
	}
	log("verifier: so a pubkey entra na cadeia de seguranca; a chave de assinatura NUNCA entra na cadeia do no")
	log("canal de controlo: Ed25519Authenticator (AOS-160) — %d operador(es) registado(s); HMACAuthenticator demo DESLIGADO", len(cfg.Operators))
	if foureyes != nil {
		log("four-eyes gate (AOS-162) composto: %d aprovador(es) pinado(s)", len(cfg.Approvers))
	}
	log("substrato: %s (durabilidade real = AOS-170)", substrateMode(cfg))

	return &Node{
		Runtime:        sec,
		Steer:          steer,
		FourEyes:       foureyes,
		Authority:      authority, // nil no modo endurecido (a autoridade corre fora do processo)
		Verifier:       verifier,
		SteerAuth:      steerAuth,
		EventStore:     es,
		WORM:           worm,
		IdentityMode:   identityMode,
		ownsEventStore: ownsES,
	}, nil
}

// Close liberta os recursos que o nó CRIOU (o Event Store de referência quando não foi
// fornecido por config). Um store fornecido pelo chamador é da sua responsabilidade.
func (n *Node) Close() error {
	if n.ownsEventStore && n.EventStore != nil {
		return n.EventStore.Close()
	}
	return nil
}

// substrateMode descreve a proveniência do substrato para o banner de arranque.
func substrateMode(cfg Config) string {
	if cfg.EventStore == nil && cfg.WORM == nil {
		return "in-memory de referencia (nao-duravel)"
	}
	if cfg.EventStore != nil && cfg.WORM != nil {
		return "fornecido por config"
	}
	return "misto (parte fornecida por config, parte in-memory de referencia)"
}

// referenceModel é o [agentruntime.ModelClient] de REFERÊNCIA do nó: conclui o run no
// primeiro turno sem tool calls. Substitui o Model Gateway real (EPIC-06). O foco de
// AOS-163 é a composição de segurança/identidade, não o modelo.
type referenceModel struct{}

// Call implementa [agentruntime.ModelClient].
func (referenceModel) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{
		Text:  "no `aos`: modelo de referencia (Model Gateway real = EPIC-06)",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// emptyCatalog é um [toolset.Catalog] de REFERÊNCIA vazio: o nó arranca sem tools
// registadas (o REG real é preenchido por config). Default-deny por omissão.
type emptyCatalog struct{}

// ActiveEntries implementa [toolset.Catalog].
func (emptyCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) { return nil, nil }

// referenceRevalidator constrói o revalidador de REFERÊNCIA (AOS-051) com um trust
// store vazio sobre um audit in-memory. É fail-closed por construção: sem publicadores
// confiados, qualquer artefacto que precisasse de revalidação de assinatura seria
// bloqueado — coerente com o default-deny do nó de referência.
func referenceRevalidator() (*revalidation.Revalidator, error) {
	auditStore := audit.NewMemStore()
	trust, err := signing.NewTrustStore(auditStore)
	if err != nil {
		return nil, err
	}
	return revalidation.New(trust, auditStore)
}
