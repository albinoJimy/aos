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

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	hitl "github.com/aos-ref/control-plane/governance/hitl"
	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
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
	// ErrDurableExecutionNeedsDurableSubstrate — config que pede execução durável
	// (DurableExecution) mas em que o nó só teria um Event Store IN-MEMORY para a
	// suportar (nem EventStore injectado, nem EventStorePath). Fail-closed (AOS-191):
	// checkpoints, capturas de não-determinismo e step-ledger compostos sobre um store
	// volátil EVAPORAM no reinício — o nó anunciaria "execução durável" e perderia
	// exactamente o estado cuja sobrevivência a durabilidade promete. É a classe de
	// defeito que a auditoria v4 nomeia (capacidade anunciada que o artefacto não
	// cumpre), pelo que a ambiguidade NEGA o arranque em vez de degradar em silêncio —
	// coerente com ErrBadBoardRegions (config auto-contraditória aborta sempre, não só
	// em produção). Um EventStore INJECTADO por config não dispara esta guarda: a sua
	// durabilidade é do chamador (o nó não a pode atestar) e o banner declara-o.
	ErrDurableExecutionNeedsDurableSubstrate = errors.New("aos: DurableExecution exige um Event Store DURAVEL (Config.EventStorePath / AOS_EVENTSTORE_PATH) — checkpointer, capturer e step-ledger sobre um store in-memory evaporam no reinicio (durabilidade anunciada e nao cumprida)")
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

	// --- Soberania/conformidade de leitura (AOS-172, E7) -----------------------
	// BoardRegions é o registo DEMO-GRADE board→região autorizada (AOS-094/ADR-011) que o
	// READ-PATH SOBERANO (D7) consulta para autorizar o leitor por-chamador e resolver a região
	// selada em cada leitura sensível (D6). É a MESMA fonte de verdade que o PDP usa
	// ([govsov.Registry]) — a regra fail-closed NÃO é duplicada. Vazio ⇒ read-path LEGADO (sem
	// authz por-chamador nem selo): a REGRA é FIXA (Carta §4), mas a TOPOLOGIA real (IdP de
	// soberania da organização) fica DEFERIDA para EPIC-09/10 (análogo ao tratamento de D4 para
	// identidade). Entradas com board OU região vazios são descartadas por [govsov.NewRegistry].
	BoardRegions map[string]string

	// --- Colaboradores NÃO-identidade (defaults de REFERÊNCIA) -----------------
	// O foco de AOS-163 é a composição de SEGURANÇA/IDENTIDADE real; estes podem vir por
	// config OU cair para um default de referência para o nó arrancar. O Model Gateway
	// real é EPIC-06.
	Model       agentruntime.ModelClient
	Catalog     toolset.Catalog
	Revalidator *revalidation.Revalidator
	Policy      integration.PolicyProvider
	// Authority é a fonte de autoridade user∩classe para o ScopeGate (AOS-071).
	// nil ⇒ fonte vazia fail-closed (scope negado para toda a tool call); em testes
	// e wiring de referência pode usar [authz.NewStaticAuthoritySource].
	Authority authz.AuthoritySource
	// PDP é um Policy Decision Point injectado para testes/wiring avançado. nil ⇒
	// [pdp.NewUnloaded] (deny fail-closed até bundle ser carregado). Quando
	// fornecido, tem PRECEDÊNCIA sobre o PDP default não-carregado.
	PDP *pdp.PDP

	// --- Substrato DURÁVEL (AOS-170) -------------------------------------------
	// DurableExecution activa o checkpointer, capturer e step-ledger duráveis
	// (AOS-180) sobre o Event Store. Quando false, o runtime usa os defaults no-op
	// (AOS-013) — o nó arranca sem execução durável. Fail-closed: se true mas o Event
	// Store não puder ser aberto, o bootstrap já aborta em (2).
	//
	// SUPERFÍCIE DE CONFIGURAÇÃO (AOS-191): é escrita a partir de AOS_DURABLE_EXECUTION
	// em [nodeConfigFromEnv] — sem isso o campo era INALCANÇÁVEL pelo binário entregue
	// (Config vive em `package main`, logo nem um embedder externo o podia preencher).
	//
	// EXIGE SUBSTRATO DURÁVEL: true sem EventStore injectado E sem EventStorePath ⇒
	// [ErrDurableExecutionNeedsDurableSubstrate] (a durabilidade sobre um store
	// in-memory seria uma promessa falsa — ver (1b)).
	DurableExecution bool
	// EventStore é a espinha append-only partilhada (turnos + sinais de controlo +
	// nonce-store anti-replay). Precedência: se != nil, usa-o tal-qual (o chamador é
	// dono do ciclo de vida); senão, se EventStorePath != "", ABRE um Event Store
	// DURÁVEL respaldado em disco (eventstore.Open — WAL append-only + fsync + replay
	// crash-safe no arranque, o reinício não perde nem duplica); senão, um in-memory
	// de referência (não-durável).
	EventStore *eventstore.Store
	// EventStorePath é o caminho do WAL do Event Store durável (AOS-170). Só é
	// consultado quando EventStore == nil. Um ficheiro inexistente é criado (store
	// novo); um existente é reconstruído byte-a-byte no arranque.
	EventStorePath string
	// WORM é o audit.Store tamper-evident único do RM. Precedência análoga: se != nil,
	// usa-o; senão, se WORMPath != "", ABRE um WORM DURÁVEL (audit.OpenFileStore —
	// mesma mecânica; a hash-chain sobrevive ao restart); senão, um in-memory de
	// referência.
	WORM audit.Store
	// WORMPath é o caminho do WAL do WORM durável (AOS-170). Só consultado quando
	// WORM == nil.
	WORMPath string
	// IssuerKeyPath, quando definido no modo de REFERÊNCIA (não endurecido) e sem
	// IssuerSigningKey explícita, faz o nó carregar a chave de assinatura do issuer de
	// um ficheiro de seed PERSISTENTE (LoadOrCreateIssuerKey) em vez de a gerar por
	// CSPRNG a CADA arranque — os tokens emitidos antes do restart continuam válidos
	// (durabilidade da identidade, AOS-170). PROIBIDO no modo endurecido (nenhuma
	// chave de assinatura entra no processo): ErrConflictingIssuerKey.
	IssuerKeyPath string

	// --- Observabilidade OTLP (AOS-173, EPIC-15 §13) ---------------------------
	// OTLPEndpoint é o endpoint do colector OTLP/HTTP (ex.: "http://collector:4318").
	// GATING: vazio ⇒ NoopTracer (zero overhead, comportamento inalterado — a
	// observabilidade não está no caminho crítico); presente ⇒ o nó compõe um
	// [otelgenai.SpanTracer] sobre um [OTLPHTTPExporter] fail-open e liga-o ao RT/RM/
	// toolset e ao WORM. Fail-closed de CONFIG: um endpoint malformado aborta o arranque
	// ([ErrBadOTLPEndpoint]) — o nó não sobe a fingir que exporta para um destino inválido.
	OTLPEndpoint string
	// OTLPExporter, quando != nil, TEM PRECEDÊNCIA sobre OTLPEndpoint e é usado tal-qual
	// (o chamador é dono do seu ciclo de vida). Serve testes/deployments que injectam um
	// exporter próprio (ex.: um RecordingExporter ou um exporter para um httptest.Server).
	// nil ⇒ segue OTLPEndpoint.
	OTLPExporter otelgenai.Exporter
	// TracerOptions são opções do [otelgenai.NewTracer] (ex.: WithIDGenerator/WithClock)
	// — usadas pelos testes para determinismo. Só aplicadas quando a observabilidade está
	// ligada (OTLPExporter != nil OU OTLPEndpoint != "").
	TracerOptions []otelgenai.TracerOption

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

	// --- Execução durável (AOS-180, activável por AOS_DURABLE_EXECUTION — AOS-191) ---
	// Checkpointer, Capturer e Ledger são os colaboradores duráveis EM VIGOR, expostos
	// para inspecção (o banner declara-os e os testes asseram-nos). São compostos EM
	// CONJUNTO sobre o Event Store quando [Config.DurableExecution] está activa; quando
	// não está, ficam os três nil e o runtime usa os defaults no-op de AOS-013.
	Checkpointer agentruntime.Checkpointer
	Capturer     agentruntime.Capturer
	Ledger       *durable.StepLedger
	// IdentityMode é o modo de identidade em vigor (sempre IdentityModeReal aqui).
	IdentityMode string

	// Tracer é a porta de observabilidade EM VIGOR (AOS-173): um [otelgenai.SpanTracer]
	// sobre OTLP quando ligada, senão o [otelgenai.NoopTracer]. Exposto para inspecção.
	Tracer otelgenai.Tracer

	// --- Soberania/conformidade de leitura (AOS-172, E7) -----------------------
	// SovereignReadRegions é o registo board→região (AOS-094) que o read-path soberano (D7)
	// consulta. nil ⇒ read-path legado (soberania de leitura não composta). A API auto-deriva
	// dele o gate de leitura em [NewAPIHandler] (fail-closed por construção no caminho de
	// produção quando composto).
	SovereignReadRegions *govsov.Registry
	// DSAR é o fluxo de apagamento/crypto-shredding (AOS-093/Art. 17) composto no nó. O endpoint
	// POST /dsar/erase encaminha para ele. nil ⇒ endpoint desligado (501).
	DSAR *dsar.Flow
	// DSARHolds é o legal hold que SUSPENDE o apagamento (um titular retido NÃO é shredded). É
	// exposto para o operador/testes colocarem/levantarem holds. O fluxo re-consulta-o ANTES de
	// cada shred (fail-closed do apagamento).
	DSARHolds *audit.LegalHold
	// DSARVault é o vault DEMO-GRADE de chaves de PII por-titular que o crypto-shredding destrói
	// (a KEK é apagada ⇒ a PII fica irrecuperável sem mutar a hash-chain). Exposto para
	// selar/provar a PII em testes de conformidade; produção liga um KMS/HSM real pela mesma porta.
	DSARVault *audit.InMemoryKeyVault
	// DSARIndex mapeia titular→partições (torna executável o legal hold POR-PARTIÇÃO no shred).
	DSARIndex *audit.InMemorySubjectPartitionIndex

	ownsEventStore bool
	ownsWORM       bool
	// otlp é o exporter OTLP/HTTP que o nó ABRIU (nil se a observabilidade está desligada
	// ou se foi injectado um OTLPExporter por config, cujo ciclo de vida é do chamador).
	// [Node.Close] drena-o.
	otlp *OTLPHTTPExporter
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
		// uma IssuerSigningKey presente — OU um IssuerKeyPath que a carregaria de disco
		// para o processo — derrotaria a propriedade que o modo garante.
		if cfg.IssuerSigningKey != nil || cfg.IssuerKeyPath != "" {
			return nil, ErrConflictingIssuerKey
		}
	} else {
		// No modo de referência a autoridade é co-localizada e minta a partir da allowlist
		// de humanos — sem humano algum não haveria raiz de delegação legítima.
		if len(cfg.Humans) == 0 {
			return nil, ErrNoHumans
		}
	}

	// (1b) FAIL-CLOSED da EXECUÇÃO DURÁVEL (AOS-191). Pedir execução durável sobre um
	// substrato que o nó SABE ser volátil é uma promessa falsa: recusa-se ANTES de abrir
	// seja o que for. Só se aplica quando o substrato seria criado PELO NÓ e sem caminho
	// (⇒ in-memory); um EventStore fornecido por config é do chamador (a sua durabilidade
	// não é atestável aqui) e o banner declara essa fronteira.
	if cfg.DurableExecution && cfg.EventStore == nil && cfg.EventStorePath == "" {
		return nil, ErrDurableExecutionNeedsDurableSubstrate
	}

	// (2) SUBSTRATO DURÁVEL (AOS-170). Precedência: fornecido por config > durável em
	// disco (path) > in-memory de referência. Um store durável aberto AQUI é propriedade
	// do nó (fecha-o em Close); um fornecido por config é do chamador.
	es := cfg.EventStore
	ownsES := false
	if es == nil {
		if cfg.EventStorePath != "" {
			created, err := eventstore.Open(cfg.EventStorePath)
			if err != nil {
				return nil, fmt.Errorf("aos: event store durável (AOS-170) %q: %w", cfg.EventStorePath, err)
			}
			es = created
		} else {
			created, err := eventstore.New()
			if err != nil {
				return nil, fmt.Errorf("aos: event store de referência: %w", err)
			}
			es = created
		}
		ownsES = true
	}
	worm := cfg.WORM
	ownsWORM := false
	if worm == nil {
		if cfg.WORMPath != "" {
			fs, err := audit.OpenFileStore(cfg.WORMPath)
			if err != nil {
				if ownsES {
					_ = es.Close()
				}
				return nil, fmt.Errorf("aos: WORM durável (AOS-170) %q: %w", cfg.WORMPath, err)
			}
			worm = fs
			ownsWORM = true
		} else {
			worm = audit.NewMemStore()
		}
	}

	// Guarda de limpeza fail-closed: se o bootstrap abortar após ter ABERTO stores
	// duráveis próprios, fecha-os (não deixa descritores/ficheiros pendurados). Só os
	// stores que o nó abriu (ownsES/ownsWORM) — nunca os fornecidos por config. O
	// exporter OTLP (se aberto pelo nó) é drenado na mesma guarda para não deixar a
	// goroutine de flush pendurada num arranque abortado.
	success := false
	var otlpExp *OTLPHTTPExporter
	defer func() {
		if success {
			return
		}
		if otlpExp != nil {
			_ = otlpExp.Close()
		}
		if ownsES {
			_ = es.Close()
		}
		if ownsWORM {
			_ = closeIfCloser(worm)
		}
	}()

	// (2c) EXECUÇÃO DURÁVEL (AOS-180). Quando configurada, compõe o checkpointer,
	// capturer de não-determinismo e step-ledger sobre o MESMO Event Store. São
	// opcionais em conjunto: quando cfg.DurableExecution é false, permanecem nil e
	// o runtime usa os defaults no-op (AOS-013).
	var (
		checkpointer agentruntime.Checkpointer
		capturer     agentruntime.Capturer
		ledger       *durable.StepLedger
	)
	if cfg.DurableExecution {
		var err error
		checkpointer, err = durable.NewCheckpointer(es)
		if err != nil {
			return nil, fmt.Errorf("aos: checkpointer durável (AOS-180): %w", err)
		}
		capturer, err = replay.NewCapturer(es)
		if err != nil {
			return nil, fmt.Errorf("aos: capturer de replay (AOS-180): %w", err)
		}
		ledger, err = durable.NewStepLedger(es)
		if err != nil {
			return nil, fmt.Errorf("aos: step-ledger durável (AOS-180): %w", err)
		}
	}

	// (2b) OBSERVABILIDADE OTLP (AOS-173). GATING: sem exporter injectado E sem endpoint
	// ⇒ NoopTracer (zero overhead; o WORM e o RT/RM/toolset ficam byte-idênticos ao modo
	// sem observabilidade). Com endpoint ⇒ abre um OTLPHTTPExporter fail-open (fail-closed
	// SÓ na config: endpoint malformado aborta). Um exporter injectado tem precedência e
	// não é propriedade do nó (não é fechado por Close).
	var exporter otelgenai.Exporter
	switch {
	case cfg.OTLPExporter != nil:
		exporter = cfg.OTLPExporter
	case cfg.OTLPEndpoint != "":
		e, oerr := NewOTLPHTTPExporter(cfg.OTLPEndpoint, WithOTLPLogger(log))
		if oerr != nil {
			return nil, fmt.Errorf("aos: observabilidade OTLP (AOS-173): %w", oerr)
		}
		exporter = e
		otlpExp = e
	}
	tracer := otelgenai.Tracer(otelgenai.NoopTracer{})
	tracingEnabled := exporter != nil
	if tracingEnabled {
		tracer = otelgenai.NewTracer(exporter, cfg.TracerOptions...)
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
		// KEYSOURCE PERSISTENTE (AOS-170): sem IssuerSigningKey explícita mas com
		// IssuerKeyPath, carrega/persiste a chave de um ficheiro de seed — estável entre
		// reinícios (os tokens não invalidam no restart). Sem nenhum dos dois, a
		// autoridade gera por CSPRNG a cada boot (referência não-durável, AOS-163).
		signingKey := cfg.IssuerSigningKey
		if signingKey == nil && cfg.IssuerKeyPath != "" {
			loaded, kerr := LoadOrCreateIssuerKey(cfg.IssuerKeyPath)
			if kerr != nil {
				return nil, kerr
			}
			signingKey = loaded
		}
		authority, err = integration.NewIssuerAuthority(integration.AuthorityConfig{
			IssuerID:      cfg.IssuerID,
			Classes:       cfg.IssuerClasses,
			Directory:     integration.NewAllowlistDirectory(cfg.Humans...),
			SigningKey:    signingKey,
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

	// (6b) OBSERVABILIDADE ligada à CADEIA (AOS-173). Só quando a observabilidade está
	// activa: o tracer flui para o RT (spans invoke_agent/chat — o chat JÁ carrega o
	// custo do turno) e, partilhado por New(), para o RM (execute_tool); o toolset
	// congelado emite o span de freeze; e o WORM é decorado para emitir um span de
	// LIGAÇÃO por selo (trajectória↔hash-chain). Sem observabilidade, nada disto é
	// composto ⇒ zero overhead e comportamento byte-idêntico.
	wormForChain := worm
	var freezeOpts []toolset.Option
	var runtimeOpts []agentruntime.Option
	if tracingEnabled {
		wormForChain = newAuditTracingStore(worm, tracer)
		freezeOpts = append(freezeOpts, toolset.WithTracer(tracer))
		runtimeOpts = append(runtimeOpts, agentruntime.WithTracer(tracer))
	}

	// (7) SECURED RUNTIME — a CADEIA REAL (via NewProductionSecure), com o VERIFIER REAL
	// ligado. Fail-closed: um colaborador obrigatório em falta é recusado aqui.
	var toolSetStore integration.ToolSetStore
	if cfg.DurableExecution {
		toolSetStore = es // AOS-155: snapshot do tool set congelado persiste no mesmo ES
	}
	sec, err := integration.NewSecuredRuntime(integration.SecuredConfig{
		Model:          model,
		Recorder:       agentruntime.NewTurnRecorder(es),
		Catalog:        catalog,
		Revalidator:    revalidator,
		Policy:         policy,
		WORM:           wormForChain, // decorado com observabilidade quando ligada (AOS-173)
		Verifier:       verifier,     // <-- REAL (AOS-156): nunca o IdentityStub nem o default sem anchors
		Authority:      cfg.Authority,
		PDP:            cfg.PDP,
		ToolSetStore:   toolSetStore,
		Checkpointer:   checkpointer,
		Capturer:       capturer,
		Ledger:         ledger,
		FreezeOptions:  freezeOpts,  // toolset.WithTracer quando a observabilidade está ligada
		RuntimeOptions: runtimeOpts, // agentruntime.WithTracer (RT+RM partilham o tracer)
	})
	if err != nil {
		return nil, fmt.Errorf("aos: secured runtime (cadeia real de produção): %w", err)
	}

	// (7b) SOBERANIA DE LEITURA (AOS-172, D7). A REGRA fail-closed board→região é FIXA (Carta
	// §4); a TOPOLOGIA é DEMO-GRADE self-hosted por config (cfg.BoardRegions) — o provisioning
	// real de regiões/boards (IdP de soberania) fica DEFERIDO para EPIC-09/10 (análogo a D4).
	// Vazio ⇒ read-path legado (sem authz por-chamador nem selo). Reutiliza a MESMA autoridade
	// board→região que o PDP (AOS-094): a regra NÃO é duplicada.
	var readRegions *govsov.Registry
	if len(cfg.BoardRegions) > 0 {
		readRegions = govsov.NewRegistry(cfg.BoardRegions)
	}

	// (7c) DSAR / CRYPTO-SHREDDING (AOS-172, Art. 17). COMPÕE o fluxo DSAR já existente
	// (AOS-093) sobre: um vault de chaves de PII por-titular DEMO-GRADE (produção liga um KMS
	// real pela mesma porta), o legal hold (preservação P0) + índice titular→partição, e o WORM
	// do nó como EventSealer (received/key_destroyed/blocked selados SEM PII). O mesmo
	// [*audit.Shredder] satisfaz HoldOracle (Held) E ShreddableKeyStore (via dsar.AuditStore) —
	// a governança não é reimplementada, só cabelada no nó.
	//
	// FRONTEIRA de COBERTURA (declarada — NÃO re-litigar): a erasure DSAR do nó destrói a KEK
	// por-titular do VAULT DEMO-GRADE. O conteúdo dos runs que o [eventstore.Store] persiste
	// (objective/system/prompt dos turnos) é HOJE guardado em texto-claro e NÃO cifrado por
	// titular, pelo que o crypto-shredding NÃO o torna ilegível. A erasure "unificada" real do
	// substrato (cifra por-titular do EventStore + registo das partições/streams do titular no
	// DSARIndex) fica DEFERIDA para a cifra do substrato (EPIC-06/09/10), análogo ao tratamento
	// de D4 para identidade. O banner declara esta fronteira explicitamente.
	dsarVault := audit.NewInMemoryKeyVault(nil)
	dsarIndex := audit.NewInMemorySubjectPartitionIndex()
	dsarHolds := audit.NewLegalHold()
	dsarShredder := audit.NewShredder(dsarVault, dsarHolds, audit.NewRetentionPolicy(nil),
		audit.WithShredderSubjectIndex(dsarIndex))
	dsarFlow := dsar.NewFlow(
		wormEventSealer{store: worm},
		dsarShredder,
		[]dsar.ShreddableKeyStore{dsar.AuditStore("audit", dsarShredder)},
		dsar.WithPartition("governance.dsar"),
	)

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
	log("substrato: %s", substrateMode(cfg))
	// EXECUÇÃO DURÁVEL (AOS-180/AOS-191). O banner declara o estado REALMENTE COMPOSTO
	// (não a intenção da config) e sobre que substrato — para que um operador nunca tenha
	// de adivinhar se checkpointer/capturer/step-ledger estão ligados.
	if checkpointer != nil && capturer != nil && ledger != nil {
		log("execucao duravel (AOS-180): LIGADA — checkpointer + capturer + step-ledger COMPOSTOS sobre o event store (%s); o tool set congelado (AOS-155) persiste no mesmo store", describeSubstrate(cfg.EventStore != nil, cfg.EventStorePath))
		if cfg.EventStore != nil {
			log("NOTA execucao duravel: o event store foi FORNECIDO POR CONFIG — a durabilidade dos checkpoints/capturas/ledger e a DESSE store; o no nao a pode atestar (com AOS_EVENTSTORE_PATH e duravel em disco por construcao)")
		}
	} else {
		log("execucao duravel (AOS-180): DESLIGADA — checkpointer/capturer/step-ledger NAO compostos (defaults no-op AOS-013); defina AOS_DURABLE_EXECUTION=1 (exige AOS_EVENTSTORE_PATH) para ligar")
	}
	if readRegions != nil {
		log("soberania de leitura (AOS-172, D7): READ-PATH SOBERANO FAIL-CLOSED ligado — %d board(s) no registo GOV DEMO-GRADE (board→regiao); leitura sensivel SELADA no WORM (D6). Provisioning real de regioes/boards DEFERIDO (EPIC-09/10)", readRegions.Len())
		log("NOTA D6: o selo grava a regiao do BOARD DO LEITOR (nao a residencia por-run do dado); a verificacao de coincidencia leitor.regiao==run.regiao fica DEFERIDA ate haver board->regiao por-run (EPIC-09/10)")
	} else {
		log("soberania de leitura (AOS-172, D7): read-path LEGADO (sem authz por-chamador nem selo) — defina Config.BoardRegions para ligar a regra fail-closed DEMO-GRADE")
	}
	log("DSAR/crypto-shredding (AOS-172, Art. 17): fluxo composto (POST /dsar/erase) — legal hold re-consultado antes do shred; received/key_destroyed/blocked selados no WORM sem PII")
	log("AVISO DSAR: a erasure destroi a KEK por-titular do VAULT DEMO-GRADE; o conteudo dos runs no Event Store (texto-claro, nao cifrado por-titular) fica FORA do alcance do shredding — cifra por-titular do substrato DEFERIDA (EPIC-06/09/10)")
	if tracingEnabled {
		if otlpExp != nil {
			log("observabilidade OTLP (AOS-173): tracer REAL -> exporter OTLP/HTTP fail-open (spans invoke_agent/chat[+custo]/execute_tool/freeze + selos WORM)")
		} else {
			log("observabilidade OTLP (AOS-173): tracer REAL -> exporter injectado por config (spans invoke_agent/chat[+custo]/execute_tool/freeze + selos WORM)")
		}
	} else {
		log("observabilidade OTLP (AOS-173): DESLIGADA (NoopTracer, zero overhead) — defina AOS_OTLP_ENDPOINT para exportar traces")
	}

	success = true // o bootstrap concluiu: a guarda de limpeza não fecha os stores.
	return &Node{
		Runtime:      sec,
		Steer:        steer,
		FourEyes:     foureyes,
		Authority:    authority, // nil no modo endurecido (a autoridade corre fora do processo)
		Verifier:     verifier,
		SteerAuth:    steerAuth,
		EventStore:   es,
		WORM:         worm, // o store REAL (não decorado): o ciclo de vida/leitura é sobre este
		IdentityMode: identityMode,
		Tracer:       tracer,

		Checkpointer: checkpointer, // nil quando a execução durável está desligada
		Capturer:     capturer,     // (os três são compostos/omitidos EM CONJUNTO)
		Ledger:       ledger,

		SovereignReadRegions: readRegions,
		DSAR:                 dsarFlow,
		DSARHolds:            dsarHolds,
		DSARVault:            dsarVault,
		DSARIndex:            dsarIndex,

		ownsEventStore: ownsES,
		ownsWORM:       ownsWORM,
		otlp:           otlpExp,
	}, nil
}

// Close liberta os recursos que o nó CRIOU (os stores duráveis/de referência abertos
// pelo nó quando não foram fornecidos por config). Um store fornecido pelo chamador é
// da sua responsabilidade. Fecha AMBOS mesmo que o primeiro devolva erro (não deixa o
// WORM aberto por causa de um erro no Event Store).
func (n *Node) Close() error {
	var firstErr error
	// Drena o exporter OTLP PRIMEIRO (AOS-173): dá à goroutine de flush a oportunidade
	// de esvaziar a fila de spans dos runs já terminados antes de o processo sair, sem
	// deixar a goroutine pendurada. É fail-open (bounded) — nunca bloqueia o shutdown do
	// nó por causa da telemetria. Só o exporter que o nó ABRIU (um injectado é do chamador).
	if n.otlp != nil {
		_ = n.otlp.Close()
	}
	if n.ownsEventStore && n.EventStore != nil {
		if err := n.EventStore.Close(); err != nil {
			firstErr = err
		}
	}
	if n.ownsWORM && n.WORM != nil {
		if err := closeIfCloser(n.WORM); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// closeIfCloser fecha o valor se implementar io.Closer (o WORM durável FileStore
// implementa; o MemStore de referência não). No-op caso contrário.
func closeIfCloser(v any) error {
	if c, ok := v.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// substrateMode descreve a proveniência do substrato para o banner de arranque,
// distinguindo o durável (AOS-170) do in-memory de referência.
func substrateMode(cfg Config) string {
	es := describeSubstrate(cfg.EventStore != nil, cfg.EventStorePath)
	worm := describeSubstrate(cfg.WORM != nil, cfg.WORMPath)
	if es == worm {
		return es
	}
	return "event-store=" + es + ", worm=" + worm
}

// describeSubstrate classifica a proveniência de um store: fornecido por config,
// durável em disco (AOS-170) ou in-memory de referência (não-durável).
func describeSubstrate(provided bool, path string) string {
	switch {
	case provided:
		return "fornecido por config"
	case path != "":
		return "duravel em disco (AOS-170)"
	default:
		return "in-memory de referencia (nao-duravel)"
	}
}

// referenceModel é o [agentruntime.ModelClient] de REFERÊNCIA do nó: conclui o run no
// primeiro turno sem tool calls. Substitui o Model Gateway real (EPIC-06). O foco de
// AOS-163 é a composição de segurança/identidade, não o modelo.
type referenceModel struct{}

// Call implementa [agentruntime.ModelClient]. Devolve um uso e um CUSTO não-nulos: o
// span chat que o RT abre em volta desta chamada carrega AttrCostMicroUSD/AttrCostUSD
// (ver kernel/agent-runtime/loop.go callModel), pelo que a observabilidade OTLP (AOS-173)
// exporta um custo real por turno mesmo com o modelo de referência (o Model Gateway real,
// com custo por provedor, é EPIC-06). O custo é micro-USD INTEIRO (fonte de verdade).
func (referenceModel) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{
		Text:         "no `aos`: modelo de referencia (Model Gateway real = EPIC-06)",
		Final:        true,
		Usage:        agentruntime.Usage{InputTokens: 12, OutputTokens: 8},
		CostMicroUSD: 1500, // 0.0015 USD — custo de referência não-nulo (observável no span chat)
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
