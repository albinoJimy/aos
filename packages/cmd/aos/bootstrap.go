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
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	hitl "github.com/aos-ref/control-plane/governance/hitl"
	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	oidc "github.com/aos-ref/integration/oidc"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/saga"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
	broker "github.com/aos-ref/platform/broker"
	identity "github.com/aos-ref/platform/identity"
	memory "github.com/aos-ref/platform/memory"
	memadapters "github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/substrate/redaction"
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
	// ErrBadIssuerAnchor — no modo endurecido (trust-anchor-only) a IssuerPubKey do ápice
	// (Config, IN-PROCESS) tem de ser uma chave pública ed25519 ESTRUTURALMENTE válida
	// (exactamente ed25519.PublicKeySize bytes). Defesa-em-profundidade (AOS-225): NÃO é um
	// furo — o boundary já está fechado (AOS-193 rejeita a pubkey partilhada dos operadores;
	// o próprio verifier nega fail-closed qualquer token; e a fronteira de AMBIENTE já
	// valida len==32 em [parseEd25519PubHex] antes de povoar Config). O que faltava era a
	// asserção no ÁPICE PROGRAMÁTICO: qualquer chamador que constrói Config.IssuerPubKey
	// DIRECTAMENTE (embedders, composition-roots, testes) — sem passar pelo parser de env —
	// podia injectar uma pubkey de comprimento errado. Bootstrap NÃO a apanhava: fluía para
	// [identity.WithTrustedIssuer] em §(3), que a DESCARTA em silêncio (só regista se
	// len==32), e o nó SUBIA a anunciar "REAL ENDURECIDO (issuer=X)" com o trust map VAZIO —
	// a degradação só aflorava ao primeiro token, tardiamente e com ErrUnknownIssuer
	// enganador (o issuer ESTAVA em config, foi descartado por tamanho). Esta asserção
	// converte esse silêncio numa RECUSA fail-closed cedo e clara, espelhando no ápice a
	// guarda que a fronteira de env já tinha (é o gémeo in-process de ErrBadIssuerPubKey e o
	// análogo, para o issuer, de ErrBadOperatorEntry). (Uma IssuerPubKey vazia NÃO chega
	// aqui: o gate `len > 0` não activa o modo endurecido — cai no modo de referência,
	// inalterado.)
	ErrBadIssuerAnchor = errors.New("aos: modo endurecido exige IssuerPubKey ed25519 de exactamente 32 bytes (chave de comprimento errado seria descartada em silencio pelo trust anchor — o no anunciaria identidade endurecida com verifier vazio)")
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
	// ErrEventStoreReplicadoSemRegiao — o no declara boards com regiao autorizada
	// (AOS_BOARD_REGIONS, read-path soberano AOS-094) mas aponta o Event Store REPLICADO
	// a um cluster SEM fronteira. Seria uma contradicao servida em silencio: as leituras
	// respeitariam a regiao e os DADOS poderiam estar em qualquer par do cluster. E a
	// mesma classe de ErrDurableExecutionNeedsDurableSubstrate — capacidade anunciada que
	// o artefacto nao cumpre — e resolve-se do mesmo modo: negando o arranque.
	ErrEventStoreReplicadoSemRegiao = errors.New("aos: AOS_BOARD_REGIONS declarado com AOS_EVENTSTORE_NATS exige AOS_EVENTSTORE_NATS_REGION — um read-path soberano sobre um Event Store replicado SEM fronteira regional poe os dados onde a politica diz que eles nao podem estar (ADR-011, AC5 de AOS-100)")

	// ErrEventStoreRegiaoForaDosBoards — a regiao do Event Store nao e a regiao de
	// nenhum board declarado. Config auto-contraditoria: aborta sempre.
	ErrEventStoreRegiaoForaDosBoards = errors.New("aos: a regiao do Event Store replicado nao corresponde a nenhum board declarado em AOS_BOARD_REGIONS")

	ErrDurableExecutionNeedsDurableSubstrate = errors.New("aos: DurableExecution exige um Event Store DURAVEL (Config.EventStoreNATS / AOS_EVENTSTORE_NATS, ou Config.EventStorePath / AOS_EVENTSTORE_PATH) — checkpointer, capturer e step-ledger sobre um store in-memory evaporam no reinicio (durabilidade anunciada e nao cumprida)")

	// ErrBadOperatorEntry — uma entrada de [Config.Operators] que o
	// [integration.Ed25519Authenticator.Register] DESCARTARIA em silêncio (emitterID vazio ou
	// pubkey de tamanho != ed25519.PublicKeySize). Fail-closed (AOS-193): registar-a-descartar
	// produziria um nó que arrancou a declarar "N operador(es) registado(s)" no banner e
	// recusaria com ErrUnknownEmitter todos os sinais desse operador. Também é o que torna
	// SÃ a leitura de cardinalidade do bind-guardrail: depois desta guarda,
	// SteerAuth.EmitterCount() == len(cfg.Operators) por construção.
	ErrBadOperatorEntry = errors.New("aos: entrada de Config.Operators invalida (emitterID vazio, pubkey ed25519 de tamanho != 32 bytes, ou pubkey PARTILHADA por dois emitterIDs) — um registo descartado em silencio daria um operador que nunca autentica; uma pubkey partilhada destruiria a atribuicao do steer")
	// ErrBadApproverEntry — uma entrada de [Config.Approvers] estruturalmente inútil
	// (principal vazio, pubkey de tamanho errado, ou autoridade vazia). Fail-closed (AOS-193),
	// contraparte de [ErrBadOperatorEntry] para o FourEyesGate: um aprovador sem capability
	// `approve:<classe>` NUNCA satisfaz hitl.RequiredAuthority, pelo que compor o gate com ele
	// seria anunciar dual-control com um roster que não aprova nada.
	ErrBadApproverEntry = errors.New("aos: entrada de Config.Approvers invalida (principal vazio ou DUPLICADO, pubkey ed25519 de tamanho != 32 bytes, autoridade vazia ou fora do vocabulario approve:{safe,gray,danger}, ou pubkey PARTILHADA por dois principals) — um aprovador sem capability approve:<classe> nunca autoriza nada; dois principals com a MESMA pubkey deixariam UMA chave privada satisfazer o dual-control")
	// ErrEnrollmentWithoutAttestationURL — [Config.DeviceEnrollment] traz dispositivos mas
	// [Config.AttestationVerifierURL] está vazia (AOS-266, fail-loud molde AOS-193). Sem a porta
	// de attestation não há deviceID a confrontar com o enrollment: [integration.NewFourEyesGate]
	// recusaria com ErrEnrollmentWithoutAttestation, mas aqui aborta-se ANTES, nomeando as duas
	// envs, para o operador não ficar a adivinhar. Descartar os dispositivos em silêncio daria um
	// nó que arrancou a parecer atribuir e nunca atribui.
	ErrEnrollmentWithoutAttestationURL = errors.New("aos: AOS_DEVICE_ENROLLMENT_FILE traz dispositivos registados mas AOS_ATTESTATION_VERIFIER_URL nao esta definida — a atribuicao dispositivo<->aprovador so vale com a porta de attestation ligada (sem ela nao ha deviceID a confrontar); defina a URL do verificador ou remova os dispositivos do ficheiro de enrollment")
)

// approveCapabilities devolve o VOCABULÁRIO FECHADO das capabilities de aprovação aceites num
// roster de four-eyes. É DERIVADO de [hitl.RequiredAuthority] sobre as três classes de risco —
// não uma lista literal — para que não possa divergir do produtor: se uma classe nova aparecer
// em risk.Class, esta lista acompanha-a.
//
// A validação existe porque [integration.hasAuthority] compara strings EXACTAS (sem wildcards):
// "approve:dangerous", "approve:*" ou "approver:danger" são fail-closed (nunca autorizam) mas
// SILENCIOSOS — dariam um aprovador contado no banner que nunca aprova nada.
func approveCapabilities() []string {
	return []string{
		hitl.RequiredAuthority(risk.ClassSafe),
		hitl.RequiredAuthority(risk.ClassGray),
		hitl.RequiredAuthority(risk.ClassDanger),
	}
}

// effectiveChallengeTTL devolve o TTL de challenge REALMENTE em vigor (AOS-266): o configurado
// se > 0, senão o default do emissor. Só para o banner declarar o valor honesto — a mesma regra
// que [hitl.NewEventStoreChallengeIssuer] aplica na construção.
func effectiveChallengeTTL(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return hitl.DefaultChallengeTTL
}

// isApproveCapability indica se a capability pertence ao vocabulário de [approveCapabilities].
func isApproveCapability(capability string) bool {
	for _, known := range approveCapabilities() {
		if capability == known {
			return true
		}
	}
	return false
}

// ApproverConfig regista UM aprovador do FourEyesGate (AOS-162): o principal, a sua
// PUBKEY pinada (a privada vive no dispositivo do humano — nunca aqui) e a autoridade
// autoritativa (capabilities approve:<classe> que detém).
type ApproverConfig struct {
	Principal string
	PubKey    ed25519.PublicKey
	Authority []string
}

// WormAnchor é o material OUT-OF-BAND da verificação ancorada do WORM (AOS-268/AOS-072). É
// DADOS parseados na fronteira de ambiente ([parseWormAnchorFromEnv]) — os três provêm,
// juntos, de uma fonte de confiança FORA do store (deploy/VCS read-only), NUNCA do próprio WORM
// mutável que se está a verificar. Os três TÊM de vir juntos: só o conjunto fecha o vector.
//
//   - Public é o TRUST-ANCHOR (pubkey ed25519 do selador dos checkpoints) — a raiz de confiança
//     contra a qual a assinatura do checkpoint é validada. A chave PRIVADA correspondente vive
//     e assina FORA do nó (molde AOS-156).
//   - Checkpoint é a ÂNCORA ASSINADA já selada out-of-process (JSON de [audit.Checkpoint]): o
//     par (Partition, AuditSeq, EntryHash) que a assinatura cobre.
//   - ExpectedHead é o PISO DE FRESCURA persistido INDEPENDENTEMENTE do store — o audit_seq que
//     a cadeia TEM de ter atingido. É o que torna o rollback de checkpoint fail-closed: um
//     checkpoint LEGÍTIMO mas ANTERIOR (reapresentado para mascarar a truncatura dos registos
//     posteriores) tem AuditSeq < ExpectedHead e é rejeitado ([audit.ErrCheckpointStale]). Por
//     isso NÃO se deriva do próprio Checkpoint: derivá-lo dele tornaria o piso um no-op
//     (AuditSeq == ExpectedHead sempre ⇒ nunca stale) e reabriria exactamente o rollback que
//     [audit.VerifyFromCheckpointAtHead] existe para fechar.
type WormAnchor struct {
	Public ed25519.PublicKey
	// Checkpoints são as âncoras assinadas, UMA POR PARTIÇÃO. Plural desde 2026-08-20: o campo
	// era singular e o nó ancorava, quando muito, UMA partição — e o WORM de produção tinha 108.
	//
	// A cobertura NUNCA pode ser completa, e isso é do desenho e não uma falta: as partições
	// nascem POR RUN (`run-<id>`, `ingestion:<id>`, `gov.residency/<id>`), pelo que o run seguinte
	// cria uma partição que nenhuma selagem anterior cobre. A propriedade honesta é «ancorado até
	// ao último selo; depois disso, só re-encadeamento» — e é isso que o banner declara, com o
	// número. Selar mais vezes encolhe a janela; não a fecha.
	Checkpoints []audit.Checkpoint
	// ExpectedHeads é o PISO DE FRESCURA por partição, persistido INDEPENDENTEMENTE do store e
	// TAMBÉM do ficheiro de checkpoints: se viajassem juntos, quem trocasse o ficheiro trocava os
	// dois e o piso deixaria de morder no rollback de checkpoint que existe para fechar.
	//
	// Toda a partição com checkpoint TEM de ter piso — sem ele a âncora dessa partição seria
	// aceite sem frescura, que é precisamente o no-op que [audit.VerifyFromCheckpointAtHead] tenta
	// impedir.
	ExpectedHeads map[string]uint64
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

	// AttestationVerifierURL liga a attestation de dispositivo WebAuthn (AOS-177) ao FourEyesGate
	// via [integration.RemoteDeviceAttestationVerifier] (cliente HTTP STDLIB para o componente de
	// autoridade externo que corre o verificador CBOR — o binário do nó fica ZERO-DEP, ADR-017).
	// Presente ⇒ cada perna de aprovação TEM de trazer attestationObject+clientDataJSON válidos; o
	// enforcement dormente de AOS-177 passa a ACTIVO. Vazio ⇒ inalterado (sem attestation).
	AttestationVerifierURL string
	// AttestationVerifierToken é o bearer opcional apresentado ao componente de autoridade
	// (material NÃO-secreto entra por env; o token vem de ficheiro montado, como o do Vault).
	AttestationVerifierToken string

	// ChallengeIssuance liga a FRESCURA POR-CERIMÓNIA do 4-eyes (AOS-266, achado F10): o modo
	// issue-then-consume da porta [integration.ChallengeIssuance]. Com ela, o nó EMITE o
	// challenge de cada perna por (pedido, aprovador) com TTL ([ChallengeTTL]) e a verificação
	// EXIGE que o challenge conste do registo de emissão e esteja fresco — fecha o replay de
	// uma prova capturada por quem detenha a chave de um aprovador (o anti-replay por uso-único
	// só dá DEDUP, não frescura). INDIVISÍVEL da emissão: quando ligada, o MESMO
	// [hitl.EventStoreChallengeIssuer] alimenta a porta do gate E o endpoint POST
	// /runs/{id}/challenge ([Node.ChallengeIssuer]) — não há como compor a porta sem o caminho
	// de emissão (compô-la sem emissão negaria TODA a perna com ErrChallengeNotIssued). Vazio ⇒
	// modo histórico (dedup sem frescura). Exige aprovadores (senão não há gate a compor).
	ChallengeIssuance bool
	// ChallengeTTL é a validade de um challenge EMITIDO — o tempo de uma CERIMÓNIA de aprovação,
	// não de uma sessão (não se guarda através de suspend/resume). <=0 ⇒ [hitl.DefaultChallengeTTL].
	ChallengeTTL time.Duration

	// DeviceEnrollment é o registo de ATRIBUIÇÃO dispositivo↔aprovador (AOS-266, achado F10):
	// aprovador → identificadores OPACOS de dispositivo registados. Não-vazio ⇒ o gate é composto
	// com [integration.WithDeviceEnrollment] e uma perna cujo dispositivo atestado NÃO esteja
	// registado para o aprovador é RECUSADA ([integration.ErrDeviceNotEnrolled], default-deny) —
	// é isto que faz a attestation provar de QUEM é o autenticador, não só o modelo. EXIGE
	// [AttestationVerifierURL] (sem deviceID não há o que confrontar): dispositivos configurados
	// sem attestation ABORTAM o boot ([ErrEnrollmentWithoutAttestationURL], fail-loud).
	DeviceEnrollment map[string][][]byte

	// --- Promotion controller / ratificação de produção (AOS-159/AOS-206) ------
	// Ratifiers regista os ratificadores de PRODUÇÃO pinados do promotion controller
	// (principal + pubkey; autoridade fixa "ratify:production"). Ao contrário de Approvers,
	// NÃO gateia a composição: o promotion controller é SEMPRE composto (ver [PromotionController]).
	// Vazio ⇒ o controller é composto na mesma mas toda a promoção é NEGADA fail-closed
	// (ratifier_unknown) e o banner declara-o. Um roster inútil é fail-closed ([ErrBadRatifierEntry]).
	Ratifiers []RatifierConfig
	// PromotionEval é o eval-gate (pré-condição eval-gate+canary) do promotion controller.
	// nil ⇒ referência fail-closed [otelgenai.FailClosedGate] (score >= [defaultPromotionMinScore]).
	// O eval-gate CONCRETO da política de promoção (AOS-114/115) fica DEFERIDO.
	PromotionEval otelgenai.EvalGate
	// RatifyTTL é a janela de frescura da ratificação. <=0 ⇒ [defaultRatifyTTL]. A via
	// sancionada EXIGE ttl > 0 ([hitl.ErrNoFreshness]); o nó satisfá-la sempre.
	RatifyTTL time.Duration
	// RatifySkew tolera ratificações com IssuedAt ligeiramente no futuro (relógios adiantados).
	RatifySkew time.Duration
	// RatifyClock injecta o relógio do gate de ratificação (frescura/selo). nil ⇒ [time.Now].
	// Uso interno/testes deterministas.
	RatifyClock func() time.Time

	// --- Soberania/conformidade de leitura (AOS-172, E7) -----------------------
	// BoardRegions é o registo DEMO-GRADE board→região autorizada (AOS-094/ADR-011) que o
	// READ-PATH SOBERANO (D7) consulta para autorizar o leitor por-chamador e resolver a região
	// selada em cada leitura sensível (D6). É a MESMA fonte de verdade que o PDP usa
	// ([govsov.Registry]) — a regra fail-closed NÃO é duplicada. Vazio ⇒ read-path LEGADO (sem
	// authz por-chamador nem selo): a REGRA é FIXA (Carta §4), mas a TOPOLOGIA real (IdP de
	// soberania da organização) fica DEFERIDA (análogo ao tratamento de D4 para identidade).
	// EIXO (AOS-196): o provisionamento do IdP de soberania NÃO tem ticket no backlog — está
	// registado como POR ATRIBUIR em docs/governance/REGISTO-Deferimentos.md (DEF-201). NÃO é
	// EPIC-09/10: nenhum desses epics tem ticket para ele.
	// Entradas com board OU região vazios são descartadas por [govsov.NewRegistry].
	BoardRegions map[string]string

	// --- Soberania de leitura: FONTE DE AUTORIDADE + CREDENCIAL FORTE (AOS-205) ---
	// SovereignReadOIDC configura o verificador OIDC (AOS-174) da CREDENCIAL FORTE do leitor de
	// governação / operador DSAR: quando presente, o read-path soberano EXIGE um ID-token OIDC
	// verificado e DERIVA o board/reader das CLAIMS VERIFICADAS (o header X-Aos-Board deixa de
	// autorizar). nil ⇒ via LEGADA por headers demo-grade (aceite só FORA de produção — a
	// produção exige-o, ver [ErrProductionNeedsSovereignAuthority] em main.go). O TENANT concreto
	// (issuer/audience reais) é config: o provisionamento do IdP de soberania da organização fica
	// DEFERIDO, o nó fica com o CONTRATO (verifica a credencial e deriva claims).
	SovereignReadOIDC *oidc.Config
	// SovereignClock injecta o relógio da AUDITORIA de alterações da [SovereignRegionAuthority]
	// (selos de provisionamento/rotação). nil ⇒ time.Now. Uso interno/testes deterministas.
	SovereignClock func() time.Time

	// --- Colaboradores NÃO-identidade (defaults de REFERÊNCIA) -----------------
	// O foco de AOS-163 é a composição de SEGURANÇA/IDENTIDADE real; estes podem vir por
	// config OU cair para um default de referência para o nó arrancar. O Model Gateway
	// real é EPIC-06.
	Model agentruntime.ModelClient
	// ModelIdentityBinder liga, no Bootstrap, o VERIFIER REAL do nó ao estágio authn do Model
	// Gateway REAL (AOS-278, CUTOVER DURO). O gateway é construído na fronteira de ambiente
	// (parseModelFromEnv), ANTES de a identidade estar composta; o seu estágio authn arranca
	// com um verifier NÃO-LIGADO que NEGA toda a chamada (fail-closed). O Bootstrap chama este
	// binder com o MESMO verifier que verifica as tool calls, fechando o cutover: sem este
	// bind, nenhum turno de modelo passa. nil ⇒ sem gateway por ambiente (Config.Model
	// injectado, ou referenceModel — nenhum exige este seam). Ver modelgatewaywiring.go.
	ModelIdentityBinder func(verifier *identity.Verifier)
	Catalog             toolset.Catalog
	Revalidator         *revalidation.Revalidator
	Policy              integration.PolicyProvider
	// Authority é a fonte de autoridade user∩classe para o ScopeGate (AOS-071).
	// nil ⇒ fonte vazia fail-closed (scope negado para toda a tool call); em testes
	// e wiring de referência pode usar [authz.NewStaticAuthoritySource].
	Authority authz.AuthoritySource
	// PDP é um Policy Decision Point injectado para testes/wiring avançado. nil ⇒
	// [pdp.NewUnloaded] (deny fail-closed até bundle ser carregado). Quando
	// fornecido, tem PRECEDÊNCIA sobre o PDP default não-carregado.
	PDP *pdp.PDP

	// Autonomy é a cablagem do ORÁCULO DE NÍVEIS DE AUTONOMIA (AOS-087/AOS-248) que a fronteira
	// de ambiente já ligou ao [Config.PDP] mas que ainda NÃO está provisionada: o Bootstrap tem
	// de chamar [autonomyWiring.provision] com o WORM composto, e só aí os níveis declarados em
	// AOS_AUTONOMY_LEVELS entram em vigor — selados na hash-chain, com motivo e actor. nil ⇒
	// oráculo não ligado (nenhum `escalate` é emitido; ver autonomy_levels.go).
	//
	// PORQUÊ UM CAMPO E NÃO UMA LEITURA DE AMBIENTE AQUI: o oráculo é opção de [pdp.Open], que
	// corre na config; separar as duas fases num campo mantém UMA só leitura do ambiente e deixa
	// o composition-root dono da ordem (WORM primeiro, provisionamento depois).
	Autonomy *autonomyWiring

	// HumanDirectory é o directório de autenticação humana injectado na autoridade de
	// identidade de REFERÊNCIA (co-localizada). nil ⇒ [integration.NewAllowlistDirectory]
	// (allowlist de nomes de `Humans`, não autenticação real). Quando fornecido — ex.:
	// [integration.NewOIDCDirectory] a partir de AOS_HUMAN_OIDC_* — a autoridade autentica o
	// humano contra um IdP REAL (ID-token OIDC verificado) antes do mint, e a via allowlist
	// sem prova passa a ser recusada (frente 1 do D4, fecha DEF-110). Tem PRECEDÊNCIA sobre a
	// allowlist. Só tem efeito no modo de REFERÊNCIA; no modo endurecido (IssuerPubKey) o nó é
	// trust-anchor-only e o directório humano vive com o issuer EXTERNO (`cmd/aos-issuer`,
	// AOS-226/227), não no nó.
	HumanDirectory integration.HumanDirectory

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
	// dono do ciclo de vida); senão, se EventStoreNATS != "", liga-se ao Event Store
	// REPLICADO (AOS-100); senão, se EventStorePath != "", ABRE um Event Store DURÁVEL
	// respaldado em disco (eventstore.Open — WAL append-only + fsync + replay
	// crash-safe no arranque, o reinício não perde nem duplica); senão, um in-memory
	// de referência (não-durável).
	EventStore EventStorePort
	// EventStorePath é o caminho do WAL do Event Store durável (AOS-170). Só é
	// consultado quando EventStore == nil e EventStoreNATS == "". Um ficheiro
	// inexistente é criado (store novo); um existente é reconstruído byte-a-byte no
	// arranque.
	EventStorePath string
	// EventStoreNATS é o endereço ("host:porta") de um cluster NATS JetStream que
	// serve de Event Store REPLICADO (AOS-100, ADR-007). Só é consultado quando
	// EventStore == nil, e tem PRECEDÊNCIA sobre EventStorePath.
	//
	// # O que muda quando isto está preenchido, e não é pouco
	//
	// O substrato deixa de ser local e passa a ARBITRAR ENTRE PROCESSOS: o
	// expected_seq é imposto pelo servidor, medido (ver
	// docs/reports/medicao-jetstream-arbitragem-2026-08-31.md). É a propriedade de que
	// toda a disciplina de posse do AOS depende — LeaseManager (AOS-018),
	// FencedAppender, a composição ORQ/SCH↔posse (ADR-023) — e sem ela essas peças
	// estavam correctas mas assentes numa suposição (DEF-282).
	//
	// CONSEQUÊNCIA DIRECTA: correr N réplicas do nó sobre o MESMO Event Store passa a
	// ser o OBJECTIVO, e o guard de arranque de AOS-285 deixa de se aplicar ao Event
	// Store — ver [guardDePosseAplicavel], onde essa condição está nomeada. O guard do
	// WORM MANTÉM-SE: o WORM continua a ser um ficheiro local, e dois escritores
	// FORKAM a hash-chain (AOS-284, medido).
	//
	// LIMITE POR FECHAR (AC5 do AOS-100): este backend ainda NÃO exprime a fronteira
	// regional de soberania (falta `placement` na configuração do stream). Não deve
	// servir um board com fronteira declarada — ver o doc de eventstore/jetstream.
	EventStoreNATS string
	// EventStoreNATSStream é o nome do stream JetStream. Vazio usa o padrão
	// (jetstream.NomeStreamPorOmissao). Só é consultado com EventStoreNATS != "".
	EventStoreNATSStream string
	// EventStoreNATSRegion é a REGIÃO da fronteira de soberania do board (ADR-011).
	// Vazia ⇒ fronteira DORMENTE (retro-compatível). Preenchida ⇒ o stream é criado com
	// `placement` restrita a servidores que anunciem `region:<valor>`, e a colocação é
	// VERIFICADA contra a configuração armazenada — ligar-se a um stream sem colocação
	// ABORTA. Ver packages/substrate/eventstore/jetstream/soberania.go.
	EventStoreNATSRegion string
	// EventStoreNATSReplicas é o factor de replicação do stream (3 ou 5; 1 é só dev).
	// Zero usa o padrão. Só é consultado com EventStoreNATS != "".
	EventStoreNATSReplicas int
	// WORM é o audit.Store tamper-evident único do RM. Precedência análoga: se != nil,
	// usa-o; senão, se WORMPath != "", ABRE um WORM DURÁVEL (audit.OpenFileStore —
	// mesma mecânica; a hash-chain sobrevive ao restart E é RE-ENCADEADA e verificada no
	// load, não só validado o CRC de framing — AOS-221); senão, um in-memory de
	// referência.
	WORM audit.Store
	// WORMPath é o caminho do WAL do WORM durável (AOS-170). Só consultado quando
	// WORM == nil.
	WORMPath string
	// WORMAnchor é a VERIFICAÇÃO ANCORADA do WORM no arranque (AOS-268/AOS-072) — o material
	// out-of-band que fecha os DOIS vectores que a re-verificação de hash-chain de AOS-221
	// (audit.VerifyStore) NÃO apanha porque não tem raiz de confiança fora do próprio store: a
	// TRUNCATURA DO TAIL (remover os registos mais recentes; a cadeia truncada re-encadeia como
	// íntegra) e a REESCRITA DESDE A GÉNESE através de um restart (uma cadeia inteiramente
	// forjada re-encadeia consigo mesma). nil ⇒ NÃO-ANCORADO: comportamento actual, declarado no
	// banner ([wormAnchorPostureBanner]). != nil ⇒ [audit.VerifyFromCheckpointAtHead] corre
	// DEPOIS de o store estar composto e re-encadeado (AOS-221) e ANTES de o nó servir; qualquer
	// falha (assinatura forjada, rollback/stale, âncora que não corresponde, cadeia adulterada)
	// ABORTA o arranque fail-closed. O material é parseado na fronteira de ambiente
	// ([parseWormAnchorFromEnv]); a VERIFICAÇÃO contra o store só é possível aqui, com o store
	// composto — daí a separação. A SELAGEM periódica de novos checkpoints exige a chave PRIVADA
	// (fora do runtime, molde AOS-156) e fica DEFERIDA (DEF-268): este nó CONSOME uma âncora
	// selada out-of-process, não a produz.
	WORMAnchor *WormAnchor
	// DSARVault é a CUSTÓDIA das chaves de PII por-titular (KEK) que o crypto-shredding destrói
	// (AOS-215/DEF-302). É a PORTA [audit.KeyVault] (EnsureKey/Key/Delete), injectável: um
	// deployment liga aqui um key-service/software-KMS de CUSTÓDIA EXTERNA (as KEK vivem FORA do
	// processo, sobrevivem ao restart) sem tocar o binário. Precedência no MOLDE do Event Store/
	// WORM: se != nil, usa-o TAL-QUAL (o chamador é dono do ciclo de vida e da durabilidade — o nó
	// não a atesta); senão, um [audit.InMemoryKeyVault] de referência (DEMO-GRADE: KEK em memória,
	// NÃO-durável, perdem-se no restart — o banner declara-o). A MESMA instância serve o cifrador
	// de conteúdo (AOS-093), o shredder DSAR e o sink de expiração (AOS-213): o /dsar/erase destrói
	// a KEK ONDE ela vive. FAIL-CLOSED: um vault injectado que falha propaga o erro (a cifra/shred
	// aborta) — NUNCA se cai em silêncio para o in-memory. O KMS/HSM real (AWS KMS, Vault, PKCS#11)
	// é INFRA-ORG por trás desta porta; um HSM key-never-leaves exige a porta de envelope (residual
	// nomeado com eixo em DEF-302). nil ⇒ referência in-memory demo-grade.
	DSARVault audit.KeyVault
	// BrokerVault é o cliente Vault REAL (KV v2) da custódia de CREDENCIAIS DOWNSTREAM
	// do Credential Broker (AOS-070/AOS-264) — SEPARADO do DSARVault (D7: cliente/token
	// próprios AOS_BROKER_VAULT_*, distintos do KEK Transit que RECUSA devolver
	// material). PREPARADO por AOS-264 a partir do ambiente, mas a TROCA MEDIADA ainda
	// NÃO está ligada ao gateway nesta entrega: é CONSUMIDO em AOS-265 (a porta de
	// aquisição in-process). nil ⇒ não configurado. O banner declara o modo e que a
	// troca está pendente — nunca "broker ligado" (seria a promessa a mais que AOS-248
	// proíbe). Ver broker_vault_env.go.
	BrokerVault broker.VaultClient
	// BrokerVaultAddr / BrokerVaultKVMount são material PÚBLICO (uma URL, um nome de
	// mount) que o banner usa para declarar o modo do broker Vault. Vazios ⇒ dormente.
	BrokerVaultAddr    string
	BrokerVaultKVMount string
	// IssuerKeyPath, quando definido no modo de REFERÊNCIA (não endurecido) e sem
	// IssuerSigningKey explícita, faz o nó carregar a chave de assinatura do issuer de
	// um ficheiro de seed PERSISTENTE (LoadOrCreateIssuerKey) em vez de a gerar por
	// CSPRNG a CADA arranque — os tokens emitidos antes do restart continuam válidos
	// (durabilidade da identidade, AOS-170). PROIBIDO no modo endurecido (nenhuma
	// chave de assinatura entra no processo): ErrConflictingIssuerKey.
	IssuerKeyPath string

	// --- Retenção / expiração por TTL (AOS-092/AOS-213) ------------------------
	// Retention é a política de retenção TTL-por-classe (policy-as-code) que conduz o
	// [audit.ExpirationJob] composto no nó. VAZIA (zero value) ⇒ o job é composto na mesma mas
	// NADA expira (nenhuma classe tem período ⇒ [audit.RetentionPolicy] fail-closed): o
	// POST /dsar/expire varre e devolve tudo em not_expired. Com períodos definidos, um registo
	// classificado que cruza o TTL da sua classe E não está sob legal hold é expirado por
	// crypto-shred da KEK por-titular (AOS-093 — apagamento REAL). O banner declara o estado.
	Retention audit.RetentionConfig
	// RetentionClock injecta o relógio do [audit.ExpirationJob] (a idade de cada registo é
	// agora−CreatedAt). nil ⇒ time.Now. Uso interno/testes deterministas.
	RetentionClock func() time.Time

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
	// --- Autenticação forte da perna OTLP (DEF-012, EIXO 2) — OPT-IN, por FICHEIRO ---------
	// OTLPClientCertPath/OTLPClientKeyPath compõem o mTLS de CLIENTE perante o colector (o par
	// certificado+chave montado por ficheiro); OTLPBearerTokenPath compõe a autenticação por
	// bearer (o token, um SEGREDO, lido de ficheiro). Só se aplicam quando o nó ABRE o exporter
	// (OTLPEndpoint != "" e sem OTLPExporter injectado); um exporter injectado é do chamador e
	// traz a sua própria autenticação. Vazios ⇒ sem autenticação de cliente (comportamento
	// actual). Material privado NUNCA por variável de ambiente — sempre por ficheiro montado.
	OTLPClientCertPath  string
	OTLPClientKeyPath   string
	OTLPBearerTokenPath string

	// --- Relógios injectáveis (testes determinísticos) -------------------------
	IssuerClock   func() time.Time
	VerifierClock func() time.Time
	SteerClock    func() time.Time
	// FreezeClock injecta o relógio do timestamp de congelamento do tool set
	// (frozen_at do evento run.toolset.frozen). nil ⇒ time.Now. É OBSERVACIONAL —
	// não entra em nenhuma decisão nem no hash do conjunto (que deriva dos specs); a
	// reconstrução pós-failover restaura o valor persistido (byte-idêntica). Sem
	// injecção o toolset.FreezeToolSet cai no relógio zero-value determinista (o
	// frozen_at aparecia como "0001-01-01T00:00:00Z"). Uso interno/testes.
	FreezeClock func() time.Time
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
	// ApprovalBroker liga a cerimónia four-eyes ao bridge negação→aprovação→reexecução
	// (AOS-021): uma aprovação concluída produz um GRANT persistido, amarrado à preview
	// da acção, em vez de evaporar. nil quando o four-eyes não está composto.
	ApprovalBroker *integration.ApprovalBroker
	// PendingApprovals é o registo DURÁVEL das tool calls escaladas que aguardam aval
	// humano — o que a superfície de administração expõe ao operador (polling). nil
	// quando o four-eyes não está composto.
	PendingApprovals *integration.PendingApprovals
	// ResumeRecords guarda o que reconstitui um run SUSPENSO (o Goal SEM a credencial,
	// cifrado por-titular quando há cifrador). É o que torna a retoma possível depois de
	// o processo largar lease e goroutine. nil quando o four-eyes não está composto.
	ResumeRecords *integration.ResumeRecords
	// ChallengeIssuer é o emissor DURÁVEL de challenges do 4-eyes (AOS-266). É o LADO DE
	// EMISSÃO da frescura por-cerimónia — o MESMO valor que alimenta a porta
	// [integration.WithChallengeIssuance] do gate. Não-nil ⇒ o endpoint POST /runs/{id}/challenge
	// emite; nil ⇒ frescura DORMENTE (o endpoint responde 501). Amarrar os dois lados ao MESMO
	// valor é o que torna emissão↔composição INDIVISÍVEIS: nunca se compõe a porta (que sem
	// emissão negaria toda a perna) sem o endpoint que a alimenta.
	ChallengeIssuer *hitl.EventStoreChallengeIssuer
	// Promotion é o promotion controller (AOS-159/AOS-206): a via SANCIONADA
	// [hitl.NewProductionRatificationGate] (freshness + nonce-store durável FORÇADOS) que
	// interpõe a ratificação humana assinada entre o canary e a produção de um artefacto de
	// auto-modificação. NUNCA é nil — é composto INCONDICIONALMENTE (ver [PromotionController]);
	// sem ratificadores registados, toda a promoção é negada fail-closed (ratifier_unknown).
	Promotion *PromotionController
	// Authority é a autoridade de identidade AOS-156 (detém a chave, encapsulada). No
	// nó de referência serve a superfície de emissão (mint) do operador/admin. É NIL no
	// modo endurecido (trust-anchor-only): a autoridade corre FORA do processo e o nó
	// recebe só o trust anchor (nenhuma chave de assinatura in-process).
	Authority *integration.IssuerAuthority
	// Verifier é o verifier REAL derivado do trust anchor da autoridade (só pubkey).
	Verifier *identity.Verifier
	// SteerAuth é o autenticador ed25519 do canal de controlo (só pubkeys).
	SteerAuth *integration.Ed25519Authenticator
	// Autonomy é a cablagem do oráculo de níveis (AOS-087/AOS-248). Exposta no nó — e não só
	// na Config — porque o plano de CONTROLO precisa dela: sem isto, mudar um nível continuaria
	// a exigir editar o `.env` e recriar o processo. nil ⇒ oráculo não composto.
	Autonomy *autonomyWiring
	// EventStore e WORM são o substrato partilhado.
	EventStore EventStorePort
	WORM       audit.Store

	// --- Execução durável (AOS-180, activável por AOS_DURABLE_EXECUTION — AOS-191) ---
	// Checkpointer, Capturer e Ledger são os colaboradores duráveis EM VIGOR, expostos
	// para inspecção (o banner declara-os e os testes asseram-nos). São compostos EM
	// CONJUNTO sobre o Event Store quando [Config.DurableExecution] está activa; quando
	// não está, ficam os três nil e o runtime usa os defaults no-op de AOS-013.
	Checkpointer agentruntime.Checkpointer
	Capturer     agentruntime.Capturer
	Ledger       *durable.StepLedger
	// Compensations é o REGISTO DE COMPENSAÇÕES (AOS-020) do nó — a costura que AOS-254
	// COMPÔS: o dispatcher durável regista aqui a acção inversa de cada activity reversível
	// (via [activity.WithCompensationRegistry], ligado em [integration.SecuredConfig]), e o
	// caminho de FALHA DURÁVEL percorre-o com um [saga.SagaCoordinator] para conduzir
	// failed→compensating. NIL quando a execução durável está desligada (compõe-se EM CONJUNTO
	// com Ledger: sem ledger não há idempotência de compensação nem dispatcher a que ligar o
	// registrar). ALCANCE HONESTO: o loop base NUNCA povoa [activity.Activity.Compensation] (a
	// call do modelo não traz acção inversa), pelo que em produção este registo está VAZIO e o
	// caminho de falha declara "sem compensação" atribuivelmente (AOS-254/AC2); um produtor de
	// activities reversíveis (AOS-022) alimentá-lo-ia pela mesma costura, sem re-compor nada.
	Compensations *saga.CompensationRegistry
	// IdentityMode é o modo de identidade em vigor (sempre IdentityModeReal aqui).
	IdentityMode string

	// Tracer é a porta de observabilidade EM VIGOR (AOS-173): um [otelgenai.SpanTracer]
	// sobre OTLP quando ligada, senão o [otelgenai.NoopTracer]. Exposto para inspecção.
	Tracer otelgenai.Tracer

	// sloTap é a TORNEIRA de spans que alimenta o avaliador de SLOs/alertas (AOS-274) com os
	// MESMOS spans que saem para o colector OTLP — não uma segunda instrumentação. É nil quando
	// a observabilidade está desligada (nesse caso o avaliador corre à mesma, sobre as fontes que
	// não dependem de spans, e o banner declara quais ficaram sem produtor). Não-exportado: é um
	// detalhe da composição do nó, não uma porta.
	sloTap *sloSpanTap

	// Ingestion é o motor de redacção/tokenização de PII (AOS-091) LIGADO de facto ao
	// fecho transitivo do nó (AOS-208): a fronteira de minimização onde o objectivo de
	// um run é redigido ANTES de alcançar o Event Store, a memória (platform/memory), os
	// spans (substrate/otel-genai) e o audit (platform/audit) — todos pela MESMA porta
	// [redaction.Ingestor] e a mesma política. NUNCA nil: o Bootstrap compõe-o SEMPRE
	// (o default não deixa o motor inalcançável — é o defeito que AOS-208 fecha), e o
	// [NodeService.Submit] invoca-o em cada run. O banner declara o estado.
	Ingestion *integration.IngestionGateway

	// --- Soberania/conformidade de leitura (AOS-172, E7) -----------------------
	// SovereignReadRegions é o registo board→região (AOS-094) EM VIGOR — snapshot da autoridade
	// (AOS-205) quando composta, ou nil ⇒ read-path legado (soberania não composta). Mantido para
	// o predicado do banner/kill-switch (AOS-203); o gate de leitura auto-derivado em
	// [NewAPIHandler] prefere [Node.SovereignAuthority].
	SovereignReadRegions *govsov.Registry
	// SovereignAuthority é a FONTE DE AUTORIDADE board→região (AOS-205) com ROTAÇÃO e AUDITORIA
	// de alterações — a fonte que substitui o mapa estático de env tratado como verdade. nil ⇒
	// sem soberania composta. [NewAPIHandler] deriva dela o gate de leitura.
	SovereignAuthority *SovereignRegionAuthority
	// SovereignReadCredential é a CREDENCIAL FORTE do read-path (AOS-205): quando composta
	// (OIDC/mTLS verificado contra o IdP de soberania), o board/reader vêm das CLAIMS VERIFICADAS,
	// não dos headers. nil ⇒ via legada por headers demo-grade (só fora de produção).
	SovereignReadCredential readCredentialVerifier
	// DSAR é o fluxo de apagamento/crypto-shredding (AOS-093/Art. 17) composto no nó. O endpoint
	// POST /dsar/erase encaminha para ele. nil ⇒ endpoint desligado (501).
	DSAR *dsar.Flow
	// DSARHolds é o legal hold que SUSPENDE o apagamento (um titular retido NÃO é shredded). É
	// exposto para o operador/testes colocarem/levantarem holds. O fluxo re-consulta-o ANTES de
	// cada shred (fail-closed do apagamento).
	DSARHolds *audit.LegalHold
	// holdsRestored é a PROVA de que [DSARHolds] (e o índice titular→partição que o faz valer
	// por-partição) foram RE-HIDRATADOS do substrato durável no arranque — não apenas
	// construídos. É o antecedente do varredor AUTOMÁTICO de retenção (AOS-267): a
	// não-nulidade do objecto prova que a barreira EXISTE, nunca que o seu CONTEÚDO sobreviveu
	// ao restart, e um conjunto de holds vazio derrota a barreira em silêncio. Ver
	// governance_restore.go e [retentionSchedulerArmed].
	//
	// Não-exportado de propósito: é estado interno de composição, não superfície do nó. Um teste
	// que componha um [Node] à mão arma-o explicitamente, e essa explicitação é o ponto.
	holdsRestored bool
	// DSARVault é o vault de chaves de PII por-titular que o crypto-shredding destrói (a KEK é
	// apagada ⇒ a PII fica irrecuperável sem mutar a hash-chain). É a PORTA [audit.KeyVault] EM
	// VIGOR (AOS-215/DEF-302): o vault INJECTADO por [Config.DSARVault] (custódia externa) quando
	// composto, ou o [audit.InMemoryKeyVault] de referência (demo-grade, KEK em memória) senão.
	// Exposto para selar/provar a PII em testes de conformidade; produção liga um KMS/HSM real
	// pela mesma porta (o real é infra-org).
	//
	// IMUTÁVEL DEPOIS DO BOOTSTRAP, e isto é um contrato de CORRIDA, não de estilo: o campo não
	// tem sincronização e é lido por goroutines de FUNDO de qualquer [NodeService] composto sobre
	// o nó — a manutenção do token da custódia ([NodeService.renewVaultToken], AOS-249) e o
	// avaliador de SLOs ([NodeService.controlPlaneAvailable], AOS-274). Um teste que queira OUTRA
	// custódia compõe OUTRO nó; reatribuir aqui depois de o serviço existir é um data race.
	DSARVault audit.KeyVault
	// DSARIndex mapeia titular→partições (torna executável o legal hold POR-PARTIÇÃO no shred).
	DSARIndex *audit.InMemorySubjectPartitionIndex
	// ExpirationJob é o job de expiração por TTL (AOS-092) COMPOSTO no nó (AOS-213): varre os
	// registos classificados do Event Store ([eventStoreRecordSource]) e expira os que cruzaram o
	// TTL e não estão sob legal hold por crypto-shred da KEK por-titular ([cryptoShredSink],
	// reutilizando o vault de AOS-093). Conduzido por POST /dsar/expire. NUNCA nil: o Bootstrap
	// compõe-o SEMPRE (com política vazia ⇒ nada expira, fail-closed). Respeita [DSARHolds].
	ExpirationJob *audit.ExpirationJob
	// Retention é a política de retenção TTL-por-classe EM VIGOR (a MESMA que alimentou o
	// [ExpirationJob]). Exposta porque o loop de serviço precisa de saber se há TTL a aplicar
	// antes de arrancar o scheduler interno (AOS-267) e porque a versão é selada em cada
	// varrimento. Zero-value ⇒ nada expira (o default do nó).
	Retention audit.RetentionConfig
	// IssuerID é o trust anchor de identidade DESTE deployment (a config que o compôs). Exposto
	// para que um selo emitido pelo nó EM NOME PRÓPRIO — o do scheduler de retenção, AOS-267 —
	// possa nomear qual nó agiu, e não só qual papel. Material PÚBLICO (um identificador).
	IssuerID string
	// contentOpener é o cifrador por-titular do nó (o MESMO [contentSealer]/[agentruntime.ContentCipher]
	// que o capturer/step-ledger usam para SELAR) exposto como [agentruntime.ContentOpener] para o
	// REPLAY SOBERANO do lado do leitor (AOS-214): a reconstrução de um run selado por um TERCEIRO
	// autorizado por soberania decifra o conteúdo por AQUI, atrás do gate (ver sovereign_replay.go).
	// Reutiliza [audit.OpenContent] — depois do /dsar/erase (KEK destruída) o open falha
	// (audit.ErrDecrypt), pelo que o replay não ressuscita o que a erasure apagou. NUNCA nil: o
	// Bootstrap compõe-o SEMPRE (mesma instância que sela), independentemente de DurableExecution.
	contentOpener agentruntime.ContentOpener

	// stateGates é a costura por-run (AOS-218) que resolve o [control.StateGate] durável
	// (AOS-017) que o canal de steer usa para materializar running↔paused. O loop de
	// serviço ABRE/LIBERTA um gate por run hospedado (ver service.go hostRun); o loop base
	// resolve-o pelo gates func já cablado no [control.LoopSteer]. NIL só se o nó não tiver
	// canal de steer (não acontece na via de produção — o Bootstrap compõe-o sempre).
	stateGates *runStateGates

	// breakers é o registo de disjuntores por-run (AOS-080/081). O loop de serviço
	// LIBERTA o estado de cada run quando ele sai do registo de em-curso ([runBreakers.forget],
	// ver service.go hostRun). NIL quando não há limiares configurados (disjuntor não composto).
	breakers *runBreakers

	// progress é o observador de burn-down por-run (AOS-261/AOS-262). O loop de serviço
	// LIBERTA o estado de cada run (superfície, latch do aviso e cursor da fonte) no mesmo
	// ponto em que liberta o disjuntor ([runProgress.forget], ver service.go hostRun). NIL
	// quando o burn-down não está composto (sem AOS_BUDGET_MAX_TOKENS não há tecto ⇒ não há
	// denominador ⇒ nenhum aviso poderia disparar).
	progress *runProgress

	ownsEventStore bool
	ownsWORM       bool
	// posseWAL e a posse EXCLUSIVA de escrita do Event Store duravel (AOS-285), ou nil
	// se o guard nao se aplica a esta configuracao. [Node.Close] larga-a — e a morte
	// abrupta do processo larga-a na mesma, pelo SO, que e a razao de o arbitro ser ele.
	posseWAL *posseDoWAL
	// otlp é o exporter OTLP/HTTP que o nó ABRIU (nil se a observabilidade está desligada
	// ou se foi injectado um OTLPExporter por config, cujo ciclo de vida é do chamador).
	// [Node.Close] drena-o.
	otlp *OTLPHTTPExporter
	// orcamento e o tecto por-run. Guardado aqui pela MESMA razao que o `otlp`: para a cablagem
	// ser observavel por um teste. Sem isto, «o no liga a fonte duravel ao orcamento» era uma
	// afirmacao sem prova — e uma mutacao que removesse a ligacao passava despercebida.
	orcamento *integration.RunBudget
	// ancora e a verificacao ancorada do WORM que PASSOU no arranque (nil se desligada ou se o
	// arranque a recusou — nesse caso o no nem chega aqui, porque e fail-closed).
	//
	// Guardada pela MESMA razao que o `otlp` e o `orcamento`: para o /metrics a poder ler. Sem
	// isto, a cobertura da ancora existia so no banner de arranque — legivel por quem estivesse
	// a ver o boot, e invisivel para um alerta. Ver [apiHandler.handleMetrics].
	ancora *WormAnchor
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
func Bootstrap(ctx context.Context, cfg Config, logw io.Writer) (*Node, error) {
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
		// (AOS-225) Defesa-em-profundidade do trust anchor: a IssuerPubKey tem de ser
		// estruturalmente uma pubkey ed25519 (len == ed25519.PublicKeySize). Sem esta
		// asserção, uma chave de comprimento errado é DESCARTADA em silêncio por
		// identity.WithTrustedIssuer em §(3) (que só regista o anchor se len==32) e o nó
		// sobe com o trust map VAZIO — identidade endurecida ANUNCIADA no banner e não
		// cumprida, com a falha a aflorar só ao primeiro token (ErrUnknownIssuer tardio e
		// enganador). Recusa-se cedo, fail-closed, com erro claro. Note-se que o gate
		// `len > 0` acima garante que uma chave vazia não activa sequer o modo endurecido;
		// esta guarda cobre as chaves NÃO-vazias de comprimento errado (curtas/longas).
		if len(cfg.IssuerPubKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: len=%d", ErrBadIssuerAnchor, len(cfg.IssuerPubKey))
		}
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

	// (1a) FAIL-CLOSED do PLANO DE CONTROLO (AOS-193). Valida-se ANTES de compor: tanto
	// [integration.Ed25519Authenticator.Register] como [hitl.MemApproverRegistry.Register]
	// aceitam entradas inúteis sem se queixarem (o primeiro DESCARTA silenciosamente uma
	// pubkey de tamanho errado). Uma entrada descartada aqui viraria um operador/aprovador
	// que o banner conta mas que nunca autentica — precisamente a classe de "capacidade
	// anunciada e não cumprida" que este eixo vem fechar. Depois desta guarda vale o
	// invariante que o bind-guardrail lê: EmitterCount() == len(cfg.Operators).
	//
	// A guarda cobre também as duas COLISÕES DE MATERIAL DE CHAVE, que nenhum registo
	// subjacente vê: dois emitterIDs com a mesma pubkey (atribuição do steer destruída) e dois
	// principals com a mesma pubkey (UMA chave privada satisfaria as duas pernas do
	// dual-control — a distinção de authorizeDual é sobre três strings escolhidas pelo CLIENTE,
	// e a pubkey pinada é a ÚNICA âncora criptográfica de "duas pessoas"). Espelha o que
	// [parseOperators]/[parseApproversFile] impõem à fronteira de ambiente: Config é alcançável
	// IN-PROCESS, e é aqui — não no parser — que o invariante tem de valer para todo o caminho.
	opIDs := make([]string, 0, len(cfg.Operators))
	for id := range cfg.Operators {
		opIDs = append(opIDs, id)
	}
	sort.Strings(opIDs) // ordem determinista ⇒ a mensagem de erro é reproduzível.
	seenOpKey := make(map[string]string, len(cfg.Operators))
	for _, id := range opIDs {
		pub := cfg.Operators[id]
		if id == "" || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: emitterID %q", ErrBadOperatorEntry, id)
		}
		fp := hex.EncodeToString(pub)
		if other, dup := seenOpKey[fp]; dup {
			return nil, fmt.Errorf("%w: emitterIDs %q e %q partilham a MESMA pubkey", ErrBadOperatorEntry, other, id)
		}
		seenOpKey[fp] = id
	}
	seenPrincipal := make(map[string]struct{}, len(cfg.Approvers))
	seenApKey := make(map[string]string, len(cfg.Approvers))
	for i, a := range cfg.Approvers {
		if a.Principal == "" || len(a.PubKey) != ed25519.PublicKeySize || len(a.Authority) == 0 {
			return nil, fmt.Errorf("%w: aprovador #%d (%q)", ErrBadApproverEntry, i, a.Principal)
		}
		for _, capability := range a.Authority {
			if !isApproveCapability(capability) {
				return nil, fmt.Errorf("%w: aprovador %q com capability %q fora do vocabulario (%s)",
					ErrBadApproverEntry, a.Principal, capability, strings.Join(approveCapabilities(), ", "))
			}
		}
		if _, dup := seenPrincipal[a.Principal]; dup {
			// hitl.MemApproverRegistry.Register SOBREPÕE em silêncio — "o último ganha" seria
			// uma escolha de autoridade feita por acidente de ordenação.
			return nil, fmt.Errorf("%w: principal %q duplicado", ErrBadApproverEntry, a.Principal)
		}
		seenPrincipal[a.Principal] = struct{}{}
		fp := hex.EncodeToString(a.PubKey)
		if other, dup := seenApKey[fp]; dup {
			return nil, fmt.Errorf("%w: principals %q e %q partilham a MESMA pubkey (o dual-control seria satisfeito por UMA chave privada)", ErrBadApproverEntry, other, a.Principal)
		}
		seenApKey[fp] = a.Principal
	}
	// RATIFICADORES do promotion controller (AOS-206), a par das duas guardas acima: um roster
	// inútil (pubkey de tamanho errado, principal duplicado, pubkey partilhada) é fail-closed
	// ANTES de compor o gate — senão o banner contaria ratificadores que nunca ratificam.
	if err := validateRatifiers(cfg.Ratifiers); err != nil {
		return nil, err
	}

	// (1b) FAIL-CLOSED da EXECUÇÃO DURÁVEL (AOS-191). Pedir execução durável sobre um
	// substrato que o nó SABE ser volátil é uma promessa falsa: recusa-se ANTES de abrir
	// seja o que for. Só se aplica quando o substrato seria criado PELO NÓ e sem caminho
	// (⇒ in-memory); um EventStore fornecido por config é do chamador (a sua durabilidade
	// não é atestável aqui) e o banner declara essa fronteira.
	if cfg.DurableExecution && cfg.EventStore == nil && cfg.EventStorePath == "" && cfg.EventStoreNATS == "" {
		return nil, ErrDurableExecutionNeedsDurableSubstrate
	}

	// (1c) COERÊNCIA DA SOBERANIA (AC5 do AOS-100, ADR-011). Um nó que declara boards com
	// região autorizada (read-path soberano, AOS-094) sobre um Event Store REPLICADO sem
	// fronteira seria uma contradição servida em silêncio: as leituras respeitariam a
	// região e os DADOS poderiam estar em qualquer par do cluster. Fail-closed, e antes de
	// abrir seja o que for.
	//
	// Só se aplica ao substrato replicado: o WAL local está onde o processo está, e não
	// há colocação a declarar.
	if cfg.EventStoreNATS != "" && len(cfg.BoardRegions) > 0 {
		regiao := strings.ToLower(strings.TrimSpace(cfg.EventStoreNATSRegion))
		if regiao == "" {
			return nil, ErrEventStoreReplicadoSemRegiao
		}
		conhecida := false
		for _, r := range cfg.BoardRegions {
			if strings.ToLower(strings.TrimSpace(r)) == regiao {
				conhecida = true
				break
			}
		}
		if !conhecida {
			return nil, fmt.Errorf("%w: AOS_EVENTSTORE_NATS_REGION=%q não é a região de nenhum board declarado em AOS_BOARD_REGIONS (%v)",
				ErrEventStoreRegiaoForaDosBoards, cfg.EventStoreNATSRegion, cfg.BoardRegions)
		}
	}

	// (2) SUBSTRATO DURÁVEL (AOS-170). Precedência: fornecido por config > durável em
	// disco (path) > in-memory de referência. Um store durável aberto AQUI é propriedade
	// do nó (fecha-o em Close); um fornecido por config é do chamador.
	// (2-pre) GUARD DE ARRANQUE (AOS-285). A posse EXCLUSIVA de escrita do Event Store é
	// tomada ANTES de o abrir: abrir primeiro e trancar depois deixaria uma janela em que
	// duas réplicas teriam ambas o WAL aberto, que é precisamente o estado a impedir.
	// Fail-closed — uma posse já tomada aborta o arranque. Ver wal_posse.go.
	// A libertação num arranque ABORTADO é feita na guarda de limpeza fail-closed que já
	// existe mais abaixo (a do `success`), junto com os stores que o nó abriu — é a mesma
	// preocupação e não merece um segundo mecanismo.
	posse, errPosse := tomarPosseDoWAL(cfg)
	if errPosse != nil {
		return nil, errPosse
	}
	// A libertação tem de ser armada AQUI, e não na guarda de limpeza mais abaixo.
	// Entre este ponto e essa guarda há `return`s — a abertura do WORM é um deles, e é
	// o que um WORM adulterado faz disparar. Com a libertação só lá em baixo, um
	// arranque abortado nesse intervalo deixava os ficheiros DETIDOS por um processo
	// que nunca existiu, e o reinício seguinte era recusado por um fantasma.
	// (Medido: `TestNode_RestartRejectsTamperedWORM` falhava na limpeza do TempDir.)
	arranqueOK := false
	defer func() {
		if !arranqueOK {
			_ = posse.Largar()
		}
	}()

	es := cfg.EventStore
	ownsES := false
	if es == nil {
		switch {
		case cfg.EventStoreNATS != "":
			// EVENT STORE REPLICADO (AOS-100). Precede o WAL local: quando o operador
			// aponta o nó a um cluster, é isso que ele quer — e um fallback silencioso
			// para um ficheiro local daria um nó que ARRANCA a anunciar substrato
			// partilhado sobre um substrato que não arbitra. Falha fail-closed.
			opts := []jetstream.Option{jetstream.ComPrazo(30 * time.Second)}
			if cfg.EventStoreNATSStream != "" {
				opts = append(opts, jetstream.ComNomeDeStream(cfg.EventStoreNATSStream))
			}
			if r := strings.TrimSpace(cfg.EventStoreNATSRegion); r != "" {
				opts = append(opts, jetstream.ComRegiao(r))
			}
			if cfg.EventStoreNATSReplicas > 0 {
				opts = append(opts, jetstream.ComReplicas(cfg.EventStoreNATSReplicas))
			}
			created, err := jetstream.Abrir(cfg.EventStoreNATS, opts...)
			if err != nil {
				return nil, fmt.Errorf("aos: event store replicado (AOS-100) em %q: %w", cfg.EventStoreNATS, err)
			}
			es = created
		case cfg.EventStorePath != "":
			created, err := eventstore.Open(cfg.EventStorePath)
			if err != nil {
				return nil, fmt.Errorf("aos: event store durável (AOS-170) %q: %w", cfg.EventStorePath, err)
			}
			es = created
		default:
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
		// AOS-285: um arranque abortado NÃO pode deixar o Event Store detido — a
		// tentativa seguinte (o supervisor a reiniciar o serviço) seria recusada por um
		// detentor que já não existe. Largar DEPOIS de fechar o store, pela mesma ordem
		// em que foram adquiridos ao contrário.
		// A posse é largada pelo defer ARMADO na aquisição (ver `arranqueOK`), que cobre
		// também os returns anteriores a esta guarda. Aqui só se marca o sucesso.
	}()

	// (2a) AOS-221 — IMPOR a tamper-evidence do WORM no RESTART. Re-encadeia e VERIFICA a
	// hash-chain de TODAS as partições do WORM logo após o reabrir, ANTES de compor seja o
	// que for sobre ela. É a via SEM CHAVE PRIVADA (um encadeamento de HASHES, não uma
	// assinatura): detecta MUTAÇÃO, REMOÇÃO INTERNA, INSERÇÃO e ENCADEAMENTO QUEBRADO no
	// estado RESTAURADO do disco. Uma cadeia adulterada ABORTA o arranque fail-closed — o
	// nó nunca sobe a servir um WORM cuja cadeia está partida; um WORM in-memory
	// recém-criado (vazio) ou um durável íntegro passa em silêncio (retro-compat).
	//
	// O WORM durável ([audit.FileStore]) já RECUSA o próprio Open sobre uma cadeia partida
	// (defesa no substrato, AOS-221); esta chamada IMPÕE a MESMA verificação para TODAS as
	// proveniências (durável, in-memory, INJECTADA por config) e é o ponto onde o nó chama
	// audit.Verify no restart. Um WORM injectado por config que NÃO saiba enumerar as suas
	// partições ([audit.PartitionLister]) devolve [audit.ErrPartitionsUnavailable]: aí a
	// verificação integral não é possível PELO NÓ e o banner DECLARA-O (não finge ter
	// verificado), mas NÃO aborta — o ciclo de vida/durabilidade desse store é do chamador
	// (a mesma fronteira do substrato injectado, que o nó não atesta).
	//
	// SIGNER/CHECKPOINT ASSINADO — NÃO COMPOSTO, honestamente. A truncatura do TAIL (remover
	// os registos MAIS RECENTES) é o único vector que a re-verificação de hash-chain não
	// apanha (ver o comentário de [audit.Verify]); fechá-la exige uma ÂNCORA DE FRESCURA
	// assinada ([audit.Signer]/checkpoint) cujo AuditSeq == head esperado. SELAR um checkpoint
	// exige a chave PRIVADA do operador — que, pela regra do nó, NÃO vive no runtime — pelo
	// que o Signer/checkpoint não é composto aqui. Eixo nomeado: AOS-072 (checkpoint como
	// âncora de frescura), com a chave de assinatura sob custódia OUT-OF-PROCESS no mesmo
	// molde do modo endurecido de AOS-156. O nó IMPÕE o que consegue sem chave privada
	// (re-encadeamento) e não alega o que não compõe.
	wormVerifiedParts, wormVerifyErr := audit.VerifyStore(ctx, worm)
	wormPartitionsOpaque := errors.Is(wormVerifyErr, audit.ErrPartitionsUnavailable)
	if wormVerifyErr != nil && !wormPartitionsOpaque {
		return nil, fmt.Errorf("aos: tamper-evidence do WORM (AOS-221) — hash-chain adulterada no arranque, o no recusa servir: %w", wormVerifyErr)
	}

	// (2a-bis) VERIFICAÇÃO ANCORADA DO WORM (AOS-268/AOS-072) — fecha os DOIS vectores que a
	// re-verificação de hash-chain de (2a) não apanha por não ter raiz de confiança FORA do
	// store: a TRUNCATURA DO TAIL (remover os registos mais recentes — a cadeia truncada
	// re-encadeia como íntegra, ver o comentário de [audit.Verify]) e a REESCRITA DESDE A GÉNESE
	// através de um restart (uma cadeia inteiramente forjada re-encadeia consigo mesma). A âncora
	// assinada + o piso de frescura persistido INDEPENDENTEMENTE do store dão a raiz de confiança
	// que faltava: [audit.VerifyFromCheckpointAtHead] valida a assinatura do checkpoint contra o
	// trust-anchor out-of-band, rejeita ([audit.ErrCheckpointStale]) um checkpoint com AuditSeq
	// abaixo do piso (rollback), confirma que a âncora corresponde ao registo real desse audit_seq
	// (a reescrita da génese diverge do EntryHash assinado ⇒ [audit.ErrCheckpointAnchor]) e recusa
	// um store cujo head caiu abaixo da âncora ([audit.ErrRangeBeyondHead], que é a truncatura
	// abaixo do checkpoint). `to` == Checkpoint.AuditSeq: o intervalo além da âncora não se
	// re-percorre aqui porque (2a) já re-encadeou a partição inteira; o que ESTA verificação ADICIONA
	// é a raiz de confiança assinada no head e o piso de frescura — não a repetição do
	// re-encadeamento. AUSENTE (WORMAnchor == nil) ⇒ comportamento actual, sem custo, declarado no
	// banner. PRESENTE ⇒ FAIL-CLOSED: qualquer falha ABORTA o arranque — o nó nunca sobe a servir
	// um WORM que não conseguiu ancorar. A guarda de limpeza acima fecha os stores que o nó abriu.
	//
	// LIMITE HONESTO declarado no banner: o piso só prova até ao audit_seq SELADO; registos
	// legítimos APENDIDOS depois do último checkpoint e ainda não selados podem ser truncados sem
	// esta verificação os apanhar. Encolher essa janela exige SELAGEM PERIÓDICA (chave privada,
	wormAncoradas, wormTotal := 0, 0
	if cfg.WORMAnchor != nil {
		a := cfg.WORMAnchor
		for _, cp := range a.Checkpoints {
			// FAIL-CLOSED POR PARTIÇÃO. Uma âncora que não verifique aborta o arranque inteiro —
			// incluindo o caso em que a partição ancorada DESAPARECEU: o checkpoint nomeia-a, o
			// store não a tem, e a leitura falha. É a metade de consumo do mesmo argumento que a
			// monotonia faz do lado da produção.
			if err := audit.VerifyFromCheckpointAtHead(ctx, worm, a.Public, cp, a.ExpectedHeads[cp.Partition], cp.AuditSeq); err != nil {
				return nil, fmt.Errorf("aos: verificacao ancorada do WORM (AOS-268/AOS-072) — o no recusa servir um WORM nao-ancorado no arranque (particao %q): %w", cp.Partition, err)
			}
			wormAncoradas++
		}
		// COBERTURA. O denominador é o que o store tem AGORA; o numerador é o que a selagem cobriu.
		// Declarar só «ANCORADA» seria repetir o erro que custou doze horas: um banner verdadeiro
		// sobre o mecanismo e enganador sobre o efeito.
		if lister, ok := worm.(interface{ Partitions() []string }); ok {
			wormTotal = len(lister.Partitions())
		}

	}

	// (2b) PROVISIONAMENTO DOS NÍVEIS DE AUTONOMIA (AOS-087/AOS-248) — a FASE 2 da cablagem que
	// a fronteira de ambiente deixou por fazer. AQUI, e não antes, porque só aqui existe o WORM:
	// ligar o sink ao store composto é o que faz cada alteração de nível nascer SELADA (motivo +
	// actor) na MESMA hash-chain que o `audit.VerifyStore` acima acabou de re-encadear.
	//
	// O defeito que fecha (F11): [buildAutonomyOracle] chamava NewLevelRegistry() SEM WithSink e
	// registava os níveis ali mesmo — a autonomia com que o nó ia correr mudava sem ficar
	// registada em lado nenhum. Fail-closed: uma selagem recusada ABORTA o arranque
	// ([ErrAutonomyProvisioning]); a guarda de limpeza acima fecha os stores que o nó abriu.
	if err := cfg.Autonomy.provision(ctx, worm); err != nil {
		return nil, err
	}

	// (2c-pre) CIFRA POR-TITULAR DO CONTEÚDO DOS RUNS (AOS-093). O vault de chaves de
	// PII por-titular e o índice titular→partição são criados AQUI — antes da execução
	// durável — porque são PARTILHADOS por duas frentes que TÊM de usar a mesma chave:
	//   (i)  a cifra do conteúdo dos runs (capturer/step-ledger) que persiste no ES;
	//   (ii) o crypto-shredding DSAR (POST /dsar/erase) que destrói a KEK por-titular.
	// É a MESMA KEK: apagar a chave no erase torna irrecuperável tanto a PII de audit
	// como o conteúdo do run. Zero-dep — reutiliza o envelope DEK/KEK de platform/audit
	// ([audit.SealContent]/[audit.OpenContent]), sem crypto novo nem libs.
	//
	// CUSTÓDIA DA KEK (AOS-215/DEF-302). Precedência no MOLDE do Event Store/WORM: um vault
	// INJECTADO por [Config.DSARVault] (custódia externa — key-service/software-KMS que sobrevive
	// ao restart) é usado TAL-QUAL; senão, o [audit.InMemoryKeyVault] de referência (DEMO-GRADE:
	// KEK em memória do processo, NÃO-durável). Uma deployment deixa de precisar de tocar o
	// binário para ligar custódia externa. A MESMA instância é partilhada pelo cifrador de
	// conteúdo, pelo shredder DSAR e pelo sink de expiração — o erase/expira destrói a KEK ONDE
	// ela realmente vive. FAIL-CLOSED: um vault injectado que falha propaga o erro pela cadeia de
	// cifra/shred; NUNCA há fallback silencioso para o in-memory.
	dsarVaultInjected := cfg.DSARVault != nil
	var dsarVault audit.KeyVault
	if dsarVaultInjected {
		dsarVault = cfg.DSARVault
	} else {
		dsarVault = audit.NewInMemoryKeyVault(nil)
	}
	dsarIndex := audit.NewInMemorySubjectPartitionIndex()
	contentCipher := newContentSealer(dsarVault, dsarIndex)

	// (2c) EXECUÇÃO DURÁVEL (AOS-180). Quando configurada, compõe o checkpointer,
	// capturer de não-determinismo e step-ledger sobre o MESMO Event Store. São
	// opcionais em conjunto: quando cfg.DurableExecution é false, permanecem nil e
	// o runtime usa os defaults no-op (AOS-013).
	var (
		checkpointer agentruntime.Checkpointer
		capturer     agentruntime.Capturer
		ledger       *durable.StepLedger
		// compensations é o registo de compensações de AOS-020 (AOS-254). Composto EM CONJUNTO
		// com o ledger: a saga de rollback reusa a idempotência do ledger (AOS-014) para "0
		// reversões duplicadas", pelo que sem ledger durável não haveria substrato para o
		// coordinator. nil ⇒ compensação desligada (o caminho de falha declara-o).
		compensations *saga.CompensationRegistry
	)
	if cfg.DurableExecution {
		var err error
		checkpointer, err = durable.NewCheckpointer(es)
		if err != nil {
			return nil, fmt.Errorf("aos: checkpointer durável (AOS-180): %w", err)
		}
		// AOS-093: o capturer cifra o conteúdo não-determinístico (resposta do modelo +
		// resultados de tools) por chave POR-TITULAR antes de o persistir — o texto-claro
		// nunca toca o WAL; o /dsar/erase torna-o irrecuperável.
		capturer, err = replay.NewCapturer(es, replay.WithContentSealer(contentCipher))
		if err != nil {
			return nil, fmt.Errorf("aos: capturer de replay (AOS-180): %w", err)
		}
		// AOS-093/AOS-245: o step-ledger cifra o Result.Payload — o OUTPUT de cada tool
		// call — sob a MESMA KEK por-titular que o capturer usa. O TITULAR é POR-RUN e
		// este ledger é composto UMA vez, partilhado por todos os runs: por isso NÃO vem
		// de um produtor fixo aqui, mas do contexto do despacho
		// ([durable.ContextWithTitular], anexado pelo activity.Dispatcher a partir do
		// Principal do run — o mesmo valor que loop.go passa ao capturer).
		//
		// [durable.WithRequireTitular] fecha a degradação silenciosa que ISTO corrige: até
		// AOS-245 o cifrador estava composto mas o titular nunca chegava, pelo que o ledger
		// caía no caminho retro-compatível e escrevia o output da tool EM CLARO no WAL — os
		// mesmos bytes cifrados em replay.captured e em claro em step.ledger.applied, fora
		// do alcance do crypto-shredding por-titular. Com a guarda, um passo sem titular é
		// RECUSADO antes de qualquer efeito em vez de vazar. Não há degradação a temer no
		// nó: o loop já recusa um run sem principal ([agentruntime.ErrNoPrincipal]), logo o
		// titular é sempre resolvível no caminho de execução.
		ledger, err = durable.NewStepLedger(es,
			durable.WithContentSealer(contentCipher), durable.WithRequireTitular())
		if err != nil {
			return nil, fmt.Errorf("aos: step-ledger durável (AOS-180): %w", err)
		}
		// AOS-254: o REGISTO DE COMPENSAÇÕES (AOS-020). Composto aqui, no MESMO ramo do ledger,
		// para que o dispatcher durável ([integration.SecuredConfig].CompensationRegistry) e o
		// coordinator do caminho de falha partilhem a MESMA instância. Vazio por construção — só
		// uma activity com Compensation (AOS-022) o povoaria; o loop base nunca o faz.
		compensations = saga.NewCompensationRegistry()
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
		// AUTENTICAÇÃO FORTE perante o colector (DEF-012, EIXO 2) — OPT-IN, só quando o NÓ abre o
		// exporter. Os caminhos vêm da Config (env em main.go); vazios ⇒ sem autenticação de
		// cliente (comportamento actual). Fail-closed de CONFIG dentro de NewOTLPHTTPExporter.
		otlpOpts := []OTLPOption{WithOTLPLogger(log)}
		if cfg.OTLPClientCertPath != "" || cfg.OTLPClientKeyPath != "" {
			otlpOpts = append(otlpOpts, WithOTLPClientCertFiles(cfg.OTLPClientCertPath, cfg.OTLPClientKeyPath))
		}
		if cfg.OTLPBearerTokenPath != "" {
			otlpOpts = append(otlpOpts, WithOTLPBearerTokenFile(cfg.OTLPBearerTokenPath))
		}
		e, oerr := NewOTLPHTTPExporter(cfg.OTLPEndpoint, otlpOpts...)
		if oerr != nil {
			return nil, fmt.Errorf("aos: observabilidade OTLP (AOS-173): %w", oerr)
		}
		exporter = e
		otlpExp = e
	}
	// AOS-274 — TORNEIRA DE SPANS para o avaliador de SLOs. Quando há observabilidade ligada,
	// interpõe-se um T ([sloTeeExporter]) que entrega ao exportador REAL e guarda uma cópia num
	// anel limitado. Os SLIs derivados de spans passam a agregar EXACTAMENTE os mesmos dados que
	// saem para o colector — nenhuma instrumentação nova, nenhuma contabilidade paralela. Sem
	// observabilidade a torneira NÃO é composta (nil): o nó fica byte-idêntico ao modo sem
	// tracing, e o avaliador continua a correr sobre as fontes que não dependem de spans
	// (prontidão do plano de controlo, integridade do WORM), declarando no banner o que ficou
	// sem produtor. FAIL-OPEN: o T nunca devolve um erro seu — ver slo_span_tap.go.
	var sloTap *sloSpanTap
	tracer := otelgenai.Tracer(otelgenai.NoopTracer{})
	tracingEnabled := exporter != nil
	if tracingEnabled {
		sloTap = newSLOSpanTap(defaultSLOTapCapacity)
		tracer = otelgenai.NewTracer(sloTeeExporter{primary: exporter, tap: sloTap}, cfg.TracerOptions...)
	}
	// EPIC-08 sobre AOS-100 — o Event Store REPLICADO passa a emitir spans.
	//
	// A ligacao e TARDIA porque o store e construido antes do tracer, e antecipar a
	// construcao do tracer arrastaria a guarda de limpeza do exportador — o sitio onde
	// este ficheiro ja teve um defeito registado. `LigarRastreador` RECUSA depois do
	// primeiro uso, pelo que a janela nao pode ser perdida em silencio.
	//
	// O store de REFERENCIA nao entra aqui: o rastreio dele e o `Observer` que ele proprio
	// declara, e misturar os dois daria duas fontes para o mesmo facto.
	if js, ok := es.(*jetstream.Store); ok && tracingEnabled {
		if errR := js.LigarRastreador(integration.NovoRastreioDoEventStore(tracer)); errR != nil {
			return nil, fmt.Errorf("aos: ligar o rastreio ao Event Store replicado: %w", errR)
		}
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
		// DIRECTÓRIO HUMANO: OIDC real (injectado por AOS_HUMAN_OIDC_*, fecha DEF-110) com
		// PRECEDÊNCIA; senão a allowlist de referência (nomes de `Humans`) (retro-compat).
		humanDir := integration.HumanDirectory(integration.NewAllowlistDirectory(cfg.Humans...))
		if cfg.HumanDirectory != nil {
			humanDir = cfg.HumanDirectory
		}
		authority, err = integration.NewIssuerAuthority(integration.AuthorityConfig{
			IssuerID:      cfg.IssuerID,
			Classes:       cfg.IssuerClasses,
			Directory:     humanDir,
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

	// (3-bis) CUTOVER DURO DE IDENTIDADE DO MODELO (AOS-278). O Model Gateway REAL foi
	// construído na fronteira de ambiente (parseModelFromEnv) ANTES de a identidade existir; o
	// seu estágio authn arrancou com um verifier NÃO-LIGADO que NEGA toda a chamada. Liga-se
	// aqui o MESMO verifier REAL que as tool calls verificam — fechando o cutover: sem este
	// bind nenhum turno de modelo passa. É SEMPRE alcançável (o verifier acima é composto em
	// AMBOS os modos, hardened e referência); a guarda defensiva declara o invariante. nil ⇒
	// sem gateway por ambiente (Config.Model injectado, ou referenceModel).
	if cfg.ModelIdentityBinder != nil {
		if verifier == nil {
			return nil, fmt.Errorf("aos: model gateway composto sem verifier de identidade (AOS-278): %w", ErrModelIdentityNotComposed)
		}
		cfg.ModelIdentityBinder(verifier)
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
	// As três portas de endurecimento (AOS-177/AOS-266) são declaradas ao banner pelo estado
	// REALMENTE composto (não pela intenção da config): um "LIGADA" sobre uma porta não composta
	// seria pior que o silêncio.
	var foureyes *integration.FourEyesGate
	var challengeIssuer *hitl.EventStoreChallengeIssuer
	var attestationComposed, enrollmentComposed, freshnessComposed bool
	if len(cfg.Approvers) > 0 {
		registry := hitl.NewMemApproverRegistry()
		for _, a := range cfg.Approvers {
			registry.Register(a.Principal, a.PubKey, a.Authority...)
		}
		var feOpts []integration.FourEyesOption

		// ATTESTATION DE DISPOSITIVO (AOS-177) — opcional. Com AttestationVerifierURL, liga o
		// verificador REMOTO (cliente HTTP stdlib; o CBOR corre no componente externo) e cada perna
		// de aprovação passa a EXIGIR attestationObject+clientDataJSON. Fail-closed: URL malformada
		// ou não-https-fora-de-loopback ABORTA o boot (o enforcement não degrada para o modo dormente).
		if strings.TrimSpace(cfg.AttestationVerifierURL) != "" {
			av, aerr := integration.NewRemoteDeviceAttestationVerifier(integration.RemoteAttestationConfig{
				URL:       cfg.AttestationVerifierURL,
				AuthToken: cfg.AttestationVerifierToken,
			})
			if aerr != nil {
				return nil, fmt.Errorf("aos: verificador de attestation remoto (AOS-177): %w", aerr)
			}
			feOpts = append(feOpts, integration.WithDeviceAttestation(av))
			attestationComposed = true
		}

		// ATRIBUIÇÃO DISPOSITIVO↔APROVADOR (AOS-266) — opcional. Com dispositivos em config, o
		// dispositivo atestado de cada perna tem de estar REGISTADO para o aprovador que a assinou
		// (default-deny). EXIGE a porta de attestation (sem deviceID não há o que confrontar):
		// aborta-se ANTES de NewFourEyesGate, nomeando as duas envs, em vez de descartar em silêncio.
		if len(cfg.DeviceEnrollment) > 0 {
			if !attestationComposed {
				return nil, ErrEnrollmentWithoutAttestationURL
			}
			feOpts = append(feOpts, integration.WithDeviceEnrollment(integration.NewStaticDeviceEnrollment(cfg.DeviceEnrollment)))
			enrollmentComposed = true
		}

		// FRESCURA POR-CERIMÓNIA (AOS-266, achado F10) — opcional, INDIVISÍVEL da emissão. O MESMO
		// [hitl.EventStoreChallengeIssuer] é (a) passado ao gate por WithChallengeIssuance — o lado
		// de CONSULTA (o challenge tem de constar do registo de emissão para (pedido, aprovador) e
		// estar fresco) — e (b) guardado em Node.ChallengeIssuer, que o endpoint POST
		// /runs/{id}/challenge usa para EMITIR. Um único sítio de construção amarra os dois lados:
		// não existe caminho que componha a porta (que, sozinha, negaria TODA a perna com
		// ErrChallengeNotIssued — a armadilha de indivisibilidade do desafio A6) sem o endpoint que
		// a alimenta. O TTL é o tempo de uma cerimónia, não de uma sessão.
		if cfg.ChallengeIssuance {
			challengeIssuer, err = hitl.NewEventStoreChallengeIssuer(es, cfg.ChallengeTTL)
			if err != nil {
				return nil, fmt.Errorf("aos: emissor de challenges do four-eyes (AOS-266): %w", err)
			}
			feOpts = append(feOpts, integration.WithChallengeIssuance(challengeIssuer))
			freshnessComposed = true
		}

		foureyes, err = integration.NewFourEyesGate(registry, hitl.NewEventStoreNonceStore(es), feOpts...)
		if err != nil {
			return nil, fmt.Errorf("aos: four-eyes gate (AOS-162): %w", err)
		}
	}

	// (5a-bis) BROKER DE APROVAÇÃO (AOS-021) — liga a cerimónia four-eyes ao bridge
	// negação→aprovação→reexecução: uma aprovação concluída deixa de EVAPORAR (o /approve
	// só verificava e respondia "authorized") e passa a produzir um GRANT persistido,
	// amarrado à preview da acção, que a retoma apresenta ao Reference Monitor.
	//
	// A exigência de EXECUÇÃO DURÁVEL para o four-eyes em produção é imposta a montante,
	// com os restantes guards de produção (ver ErrProductionNeedsDurableApproval em
	// main.go): sem ela não há como reproduzir com fidelidade o turno escalado nem
	// impedir a dupla execução das activities já aplicadas do mesmo turno.
	// A store de grants é DURÁVEL sobre o MESMO Event Store dos turnos/sinais: os grants
	// vivem no caminho de autorização e não podem evaporar num restart/failover — entre o
	// POST /approve e a retoma isso obrigaria a repetir a cerimónia four-eyes inteira. O
	// uso-único ATÓMICO vem do dedup do Event Store (o consumo é uma reclamação
	// idempotente), o mesmo mecanismo em que o step-ledger assenta.
	var approvalBroker *integration.ApprovalBroker
	var pendingApprovals *integration.PendingApprovals
	var resumeRecords *integration.ResumeRecords
	if foureyes != nil {
		grantStore, gerr := integration.NewEventStoreApprovalStore(es)
		if gerr != nil {
			return nil, fmt.Errorf("aos: store de grants de aprovacao (AOS-021): %w", gerr)
		}
		approvalBroker, err = integration.NewApprovalBroker(foureyes, grantStore)
		if err != nil {
			return nil, fmt.Errorf("aos: broker de aprovacao (AOS-021): %w", err)
		}
		// Registo DURÁVEL de pendentes (o que o operador vê para decidir). Mesma razão dos
		// grants: se evaporasse num restart, o operador deixaria de saber o que aprovar.
		pendingApprovals, err = integration.NewPendingApprovals(es)
		if err != nil {
			return nil, fmt.Errorf("aos: registo de aprovacoes pendentes (AOS-021): %w", err)
		}
		// O Goal transporta o objectivo do utilizador e o contexto de memória — conteúdo
		// que o turn.recorded deliberadamente NÃO persiste em claro. Aqui passa pelo MESMO
		// cifrador por-titular do capturer (AOS-093), pelo que o crypto-shredding o alcança.
		resumeRecords, err = integration.NewResumeRecords(es, contentCipher)
		if err != nil {
			return nil, fmt.Errorf("aos: registo de retoma (AOS-021): %w", err)
		}
	}

	// (5b) PROMOTION CONTROLLER (AOS-159/AOS-206, achado DEF-03). SEMPRE composto (ver
	// [PromotionController]), pela via SANCIONADA [hitl.NewProductionRatificationGate] — que
	// FORÇA freshness + nonce-store durável e RECUSA a construção sem eles. O nonce-store é
	// DURÁVEL sobre o MESMO Event Store dos turnos/sinais (molde de AOS-159/AOS-160/AOS-162);
	// o eval-gate é a referência fail-closed (ou o injectado); o WORM do nó sela a decisão. O
	// crú [hitl.NewRatificationGate] (anti-replay opcional) NÃO é chamado. Sem ratificadores
	// registados, o gate é composto na mesma mas toda a promoção é NEGADA (ratifier_unknown) —
	// o default seguro, declarado no banner.
	promotionEval := cfg.PromotionEval
	if promotionEval == nil {
		promotionEval = otelgenai.FailClosedGate{MinScore: defaultPromotionMinScore}
	}
	promotion, err := newPromotionController(
		promotionEval,
		buildRatifierRegistry(cfg.Ratifiers),
		worm, es,
		cfg.RatifyTTL, cfg.RatifySkew,
		len(cfg.Ratifiers),
		cfg.RatifyClock,
	)
	if err != nil {
		return nil, fmt.Errorf("aos: promotion controller (AOS-159/AOS-206): %w", err)
	}

	// (6) COLABORADORES NÃO-IDENTIDADE — defaults de REFERÊNCIA (o foco é a identidade).
	model := cfg.Model
	if model == nil {
		model = referenceModel{}
	}
	// RETOMA (AOS-021) — decorador OUTERMOST, aplicado AQUI e não no construtor de modelo
	// do arranque por ambiente. Num turno coberto pelo plano de replay (que viaja no ctx),
	// devolve a resposta REGISTADA sem tocar no modelo; nos restantes delega. Um run normal
	// nunca tem plano ⇒ totalmente transparente.
	//
	// PORQUÊ AQUI: a garantia de replay-then-continue é do NÓ, não de quem construiu o
	// modelo. Enquanto o decorador viveu só no caminho por-ambiente, um cfg.Model injectado
	// (embedder, teste, harness) perdia-o EM SILÊNCIO — a retoma voltava a interrogar o
	// modelo nos turnos já vividos, e a acção aprovada podia deixar de ser a acção
	// reproduzida. Aplicá-lo na composição do nó torna a garantia independente da origem.
	model = newResumeAwareModelClient(model)
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
	//
	// AOS-210: chainTracer é a QUARTA via — o MESMO tracer entregue explicitamente ao
	// composition root, para ele instrumentar o dispatcher durável que constrói
	// INTERNAMENTE (activity.Dispatcher, AOS-021). Sem ela o span aos.activity — a
	// camada intermédia da árvore, onde vivem dedup/replay e o custo do efeito real —
	// ficava com NoopTracer e nunca era exportado com AOS_DURABLE_EXECUTION ligado.
	//
	// Fica NIL quando a observabilidade está desligada ⇒ o dispatcher mantém o seu
	// default [agentruntime.NoopTracer] e a composição é byte-idêntica à de antes de
	// AOS-210. É deliberado que a atribuição viva DENTRO do `if` com as outras três vias
	// (WORM decorado, freeze, runtime) e não numa inicialização incondicional
	// `= tracer`: `tracer` é `otelgenai.NoopTracer{}` — NÃO-nil — quando a
	// observabilidade está desligada, pelo que inicializá-lo aqui faria o nó passar
	// SEMPRE um tracer ao composition root. O efeito observável seria o mesmo (um Noop
	// não emite), mas a retro-compatibilidade passaria a depender de o Noop do nó ser
	// equivalente ao default do dispatcher, em vez de ser ESTRUTURAL — e o CA «apenas
	// quando a observabilidade está ligada» deixaria de ser verdade.
	wormForChain := worm
	// Relógio REAL do timestamp de congelamento (frozen_at). Sem isto o
	// toolset.FreezeToolSet cai no zero-value determinista e o frozen_at do evento
	// run.toolset.frozen serializa como "0001-01-01T00:00:00Z". É observacional (não
	// entra em decisão nem no hash do conjunto), pelo que injectar time.Now é seguro;
	// o rebuild pós-failover restaura o valor persistido (byte-idêntico).
	freezeClock := cfg.FreezeClock
	if freezeClock == nil {
		freezeClock = time.Now
	}
	freezeOpts := []toolset.Option{toolset.WithClock(freezeClock)}
	var runtimeOpts []agentruntime.Option
	var chainTracer agentruntime.Tracer
	if tracingEnabled {
		wormForChain = newAuditTracingStore(worm, tracer)
		freezeOpts = append(freezeOpts, toolset.WithTracer(tracer))
		runtimeOpts = append(runtimeOpts, agentruntime.WithTracer(tracer))
		chainTracer = tracer // a MESMA variável das três vias acima (invariante de SecuredConfig.Tracer)
	}

	// (6c) STEER LIGADO AO LOOP (AOS-218, ACHADO-2). Compõe o adaptador que faltava:
	// [control.NewLoopSteer] casa o [control.SteerChannel] do nó (node.Steer) com o
	// resolvedor de [control.StateGate] por-run ([runStateGates.Resolve]) e liga-se ao
	// runtime de PRODUÇÃO via [agentruntime.WithSteerSource] (abaixo, SecuredConfig.SteerSource).
	// A partir daqui a pausa graciosa e a injecção da correcção TRUSTED tornam-se EFECTIVAS
	// na fronteira de fim-de-turno — antes de AOS-218, NewLoopSteer/WithSteerSource não
	// tinham chamador de produção e a correcção nunca chegava ao loop. O StateGate durável
	// vem da FONTE REAL do nó: a máquina de estados de AOS-017 sobre o mesmo Event Store,
	// aberta por-run pelo loop de serviço com o fencing token do lease (ver service.go).
	//
	// AOS-252: o tecto de wall-clock em `running` resolve-se ANTES dos gates — as máquinas
	// abertas por eles precisam dele para o varrimento de deadlines (CheckDeadlines). É o
	// MESMO valor do sinal wall-clock do breaker (UM conceito operador, dois pontos de
	// enforcement: fronteira de fim-de-turno e backstop a meio do turno).
	breakerProvider, berr := breakerThresholdsFromEnv()
	if berr != nil {
		return nil, berr
	}
	var runWallClock time.Duration
	if breakerProvider != nil {
		runWallClock = breakerProvider.Thresholds(breakerClass).MaxWallClock
	}
	stateGates := newRunStateGates(es, chainTracer, runWallClock)
	loopSteer := control.NewLoopSteer(steer, stateGates.Resolve)

	// (6d) BRIDGE DE APROVAÇÃO LIGADO AO LOOP (AOS-021). É esta composição que faz o ciclo
	// escalada→aprovação→retoma CORRER: sem ela as portas do kernel ficam inertes e um
	// veredicto `escalate` do RM comporta-se como uma negação (o run prossegue sem que
	// ninguém peça aval). Só existe quando o four-eyes está composto — sem aprovadores não
	// há a quem escalar, e degradar para negação é a postura correcta.
	//   - escalationSink: suspende o run (running→waiting_on_human) na MESMA state.Machine
	//     por-run que o steer usa, e regista o pendente DURÁVEL para o operador ver.
	//   - approvalBroker: como ApprovalEvidenceSource, resolve na retoma se existe aprovação
	//     por usar para a preview da call e devolve a evidência a anexar.
	// (6e) DISJUNTOR DO AGENTE VIVO (AOS-080/081) — POR-RUN, sobre a MESMA state.Machine
	// que o steer e a escalada usam. Só é composto quando há limiares configurados: um
	// disjuntor sem limiares é cego e daria a ilusão de protecção (ver breaker_thresholds.go).
	// O provider foi resolvido em (6c) — as máquinas de estado precisavam do wall-clock (AOS-252).
	// AOS-246: a cablagem incompatível (limiar de velocidade ligado sem VelocitySource)
	// aborta o ARRANQUE aqui — antes valia um disjuntor ausente em silêncio.
	breakers, berr := newRunBreakers(stateGates, breakerProvider)
	if berr != nil {
		return nil, berr
	}
	livenessBreaker := breakers.livenessAdapter()
	// AOS-251: o detector de no-progress só vê acções se o loop as reportar — liga o
	// observador ao runtime (method value nil-safe: sem disjuntor composto é inerte).
	runtimeOpts = append(runtimeOpts, agentruntime.WithActionObserver(breakers.observeAction))

	var escalationSink agentruntime.EscalationSink
	var approvalEvidence agentruntime.ApprovalEvidenceSource
	var approvalVerifier referencemonitor.ApprovalVerifier
	if approvalBroker != nil && pendingApprovals != nil {
		sink, serr := newNodeEscalationSink(stateGates, pendingApprovals)
		if serr != nil {
			return nil, fmt.Errorf("aos: adaptador de escalada (AOS-021): %w", serr)
		}
		escalationSink = sink
		approvalEvidence = approvalBroker
		// O TERCEIRO lado do bridge, dentro da cadeia: sem o gate, a evidência viaja e
		// ninguém a lê — o escalate repetir-se-ia para sempre e a cerimónia four-eyes não
		// destravaria nada. É o MESMO broker: quem emite o grant é quem o verifica e o
		// consome (uso-único atómico).
		approvalVerifier = approvalBroker
	}

	// (7) SECURED RUNTIME — a CADEIA REAL (via NewProductionSecure), com o VERIFIER REAL
	// ligado. Fail-closed: um colaborador obrigatório em falta é recusado aqui.
	var toolSetStore integration.ToolSetStore
	if cfg.DurableExecution {
		toolSetStore = es // AOS-155: snapshot do tool set congelado persiste no mesmo ES
	}
	// Bindings de execução em sandbox (AOS-005/AOS-064): as tools de AOS_MODEL_TOOLS que
	// declaram um bloco `sandbox`. Vazio ⇒ EffectRewriter nil (comportamento inalterado). O
	// rewriter traduz args→ExecRequest no dispatcher (onde há RunID/StepID reais); o registo
	// dos MediatedLaunchers no RM faz-se A SEGUIR (precisa de sec.Monitor()). Ver sandboxwiring.go.
	sandboxBindings, sandboxSemExecutor, err := sandboxBindingsFromEnv()
	if err != nil {
		return nil, err
	}
	// (7-ante) ORÇAMENTO POR-RUN (AOS-008, ligado em AOS-256/AOS-257). nil ⇒ AOS_BUDGET_MAX_TOKENS
	// não está definida ⇒ o ponto de injecção "budget" da cadeia fica com o stub neutro. É ESTE
	// valor — o que foi REALMENTE composto, não a intenção da config — que alimenta a linha do
	// banner mais abaixo. Fail-closed: um tecto ilegível/≤0 aborta o arranque (ErrBadBudget).
	runBudget, err := budgetFromEnv()
	if err != nil {
		return nil, err
	}
	// CRUZAMENTO DAS DUAS POSTURAS (AOS-259 x AOS-260): um tecto em DÓLARES só é uma protecção
	// se houver de onde derivar dólares. Sem fonte de preço — ou com o modelo de referência, cujo
	// custo é fabricado — o tecto seria uma capacidade-fantasma escrita na config e anunciada no
	// banner. Fail-closed no arranque, no molde de ErrProgressBudgetUnwired logo abaixo.
	if err := requireCostSourceForBudgetCap(runBudget, cfg.Model); err != nil {
		return nil, err
	}
	// (7-ante-bis) BURN-DOWN + AVISO DE EXAUSTÃO (AOS-261/AOS-262). A fonte é o LEDGER DE
	// TURNOS (o mesmo `es` onde o TurnRecorder logo abaixo grava — a coincidência não é
	// acidental, é o contrato: se fossem stores diferentes a fonte denunciaria com
	// ErrBurndownNoLedger em vez de somar zeros). O tecto vem do orçamento por-run.
	//
	// GATE FAIL-CLOSED: pedir o aviso (AOS_PROGRESS_THRESHOLD) sem tecto (AOS_BUDGET_MAX_TOKENS)
	// é uma configuração impossível — sem denominador a fracção é 0 para sempre e o aviso
	// nunca dispararia. Aborta aqui, como AOS-246 faz com o disjuntor sem VelocitySource.
	progressThreshold, progressRequested, err := progressThresholdFromEnv()
	if err != nil {
		return nil, err
	}
	if progressRequested && runBudget == nil {
		return nil, ErrProgressBudgetUnwired
	}
	burndownSource := newTurnLedgerBurndown(es)
	// (7-ante-quinquies) O TECTO POR-RUN DEIXA DE RECOMEÇAR A CADA HOSPEDAGEM (AOS-256).
	//
	// A árvore de orçamento vive em memória e o nó do run nascia, a cada hospedagem, com o tecto
	// INTEIRO — pelo que um run em ciclo de escalada/retoma podia gastar N × tecto. Escalar e
	// retomar é o fluxo NORMAL de tudo o que precisa de aprovação humana, não um caso exótico.
	//
	// AQUI, e não na fronteira de ambiente, porque só aqui existe o Event Store: é o MESMO ledger
	// de turnos que o burn-down lê, chaveado por `run_id` e sobrevivente à retoma. O aviso já via
	// o total; o enforcement passa a vê-lo também.
	//
	// ALCANCE COMPLETO desde AOS-287: o ledger conta turnos de MODELO, e o registo de consumo
	// de tool calls (ligado logo a seguir) cobre a outra metade. Antes disso a fuga ENCOLHIA
	// (do tecto inteiro por incarnação para o consumo de tool calls por incarnação) sem fechar.
	if runBudget != nil && burndownSource != nil {
		runBudget.LigarConsumoDuravel(consumoDuravelParaOrcamento(burndownSource), log)
		// AOS-287 — A ESCRITA, simétrica da leitura acima e ligada no MESMO ponto e pela
		// mesma razão (só aqui existe Event Store). Sem ela, a leitura continua a ver só
		// turnos de modelo e as tool calls continuam a ser esquecidas a cada incarnação.
		runBudget.LigarRegistoDeConsumo(registoDeConsumoDeToolCall(es, producerDoConsumoDeToolCall()), log)
	}
	// (7-ante-ter) PROMPT DE EXAUSTÃO (AOS-263) — a CONSEQUÊNCIA do aviso. Reutiliza o que já
	// está composto: o registo durável de pendentes (2.º tipo) e o registo de retoma do
	// four-eyes em (5), os gates de estado (suspensão em waiting_on_human), e a ROTA DE DECISÃO
	// — o autenticador ed25519 de (4) COM operadores pinados e o WORM. Falta uma ⇒ o prompt
	// fica DESARMADO (o nó avisa como em AOS-262 e o banner di-lo): suspender um run para uma
	// pergunta que ninguém pode responder, ou cuja resposta não se pode selar, seria matá-lo
	// com outro nome.
	exhaustion, err := newExhaustionPrompt(stateGates, pendingApprovals, resumeRecords, steerAuth, worm, log)
	if err != nil {
		return nil, err
	}
	// O `log` do nó entra aqui porque é a superfície onde o aviso é VISÍVEL sempre: sem
	// AOS_OTLP_ENDPOINT o `tracer` acima é o NoopTracer e o span do aviso não vai a lado
	// nenhum (ver runProgress.avisar).
	progress := newRunProgress(stateGates, runBudget, burndownSource, tracer, progressThreshold, log, exhaustion)
	// nil-safe: sem burn-down composto o observador é nil e o loop nunca o consulta
	// (comportamento de AOS-013 byte-idêntico).
	runtimeOpts = append(runtimeOpts, agentruntime.WithProgressObserver(progress.observer()))
	// (7-ante-quater) ADMISSÃO DO TURNO DE MODELO (AOS-260, D1 opção B). É o MESMO orçamento
	// por-run do hook de tool calls — nenhum tecto novo —, agora também a admitir a INFERÊNCIA,
	// que era a linha de custo dominante sem admission control. A degradação por esgotamento
	// reutiliza o MESMO `exhaustion` de AOS-263: armado ⇒ o run suspende-se à espera de decisão
	// humana; desarmado ⇒ pára com razão própria (`timed_out`/`budget_exhausted`).
	// nil (sem AOS_BUDGET_MAX_TOKENS) ⇒ porta não ligada e o loop corre como em AOS-013.
	modelAdmission, err := newNodeModelAdmission(runBudget, exhaustion, log)
	if err != nil {
		return nil, err
	}
	runtimeOpts = append(runtimeOpts, agentruntime.WithModelAdmission(modelAdmission.port()))

	sec, err := integration.NewSecuredRuntime(integration.SecuredConfig{
		EffectRewriter: newSandboxEffectRewriter(sandboxBindings), // AOS-005: args→ExecRequest; nil ⇒ sem reescrita
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
		// AOS-254: liga o registo de compensações ao dispatcher durável (WithCompensationRegistry
		// na composição de produção). nil quando a execução durável está desligada.
		CompensationRegistry: compensations,
		FreezeOptions:        freezeOpts,  // toolset.WithTracer quando a observabilidade está ligada
		RuntimeOptions:       runtimeOpts, // agentruntime.WithTracer (RT+RM partilham o tracer)
		Tracer:               chainTracer, // AOS-210: o MESMO tracer para o dispatcher durável (aos.activity)
		SteerSource:          loopSteer,   // AOS-218: liga o canal de steer ao loop de produção (ACHADO-2)
		// AOS-080/081: disjuntor do agente vivo. nil quando não há limiares configurados.
		LivenessBreaker: livenessBreaker,
		// AOS-021: bridge de aprovação. nil quando não há four-eyes ⇒ escalate degrada para
		// negação (o loop prossegue) — retro-compatível.
		EscalationSink:   escalationSink,
		ApprovalEvidence: approvalEvidence,
		ApprovalVerifier: approvalVerifier,
		// AOS-256/AOS-257: nó de orçamento por-run + BudgetCheck no lugar do stub + saldo da
		// reserva no decorator do dispatcher. nil ⇒ stub neutro (nenhuma decisão consulta custo).
		Budget: runBudget,
	})
	if err != nil {
		return nil, fmt.Errorf("aos: secured runtime (cadeia real de produção): %w", err)
	}

	// (7-bis) EXECUÇÃO EM SANDBOX (AOS-005/AOS-064). Regista um MediatedLauncher por tool com
	// binding NO RM do nó (sec.Monitor()) — no-bypass estrutural. Só agora, porque precisa do RM
	// já construído. Casado com o EffectRewriter acima, fecha o caminho args→ExecRequest→sandbox
	// para o loop live. Vazio ⇒ no-op. Fail-closed: uma falha de registo aborta o arranque.
	if err := registerSandboxLaunchers(sec, es, sandboxBindings, sandboxSemExecutor, log); err != nil {
		return nil, err
	}

	// (7b) SOBERANIA DE LEITURA (AOS-172, D7). A REGRA fail-closed board→região é FIXA (Carta
	// §4); a TOPOLOGIA é DEMO-GRADE self-hosted por config (cfg.BoardRegions) — o provisioning
	// real de regiões/boards (IdP de soberania) fica DEFERIDO (análogo a D4). EIXO (AOS-196):
	// POR ATRIBUIR, registado em docs/governance/REGISTO-Deferimentos.md (DEF-201). A
	// verificação leitor.região == run.região POR-RUN está ENTREGUE (AOS-182/DEF-202): a
	// residência do run é selada na criação e authorizeReadComCausa recusa cross-region — ver
	// sovereignty.go/api.go. NÃO é EPIC-09/10.
	// Vazio ⇒ read-path legado (sem authz por-chamador nem selo). Reutiliza a MESMA autoridade
	// board→região que o PDP (AOS-094): a regra NÃO é duplicada.
	// AOS-205: a fonte board→região é agora uma PORTA DE AUTORIDADE ([SovereignRegionAuthority])
	// com ROTAÇÃO e AUDITORIA de alterações — o mapa de env é apenas a SEMENTE do provisionamento
	// inicial, não a verdade congelada. A REGRA fail-closed (govsov.RegionFor) NÃO se duplica.
	// readRegions é o snapshot em vigor, mantido para o predicado do banner/kill-switch.
	var readAuthority *SovereignRegionAuthority
	var readRegions *govsov.Registry
	var readCred readCredentialVerifier
	if len(cfg.BoardRegions) > 0 {
		readAuthority, err = NewSovereignRegionAuthority(ctx, cfg.BoardRegions, worm, cfg.SovereignClock)
		if err != nil {
			return nil, fmt.Errorf("aos: fonte de autoridade de soberania (AOS-205): %w", err)
		}
		readRegions = readAuthority.Registry()
	}
	// CREDENCIAL FORTE do leitor (AOS-205): quando o verificador OIDC de soberania está
	// configurado, o read-path exige um ID-token verificado e deriva o board das CLAIMS. Sem ele,
	// mantém-se a via legada por headers (aceite só fora de produção). Fail-closed: uma config
	// OIDC inválida (issuer/audience vazios, transporte inseguro) aborta o arranque.
	if cfg.SovereignReadOIDC != nil {
		v, verr := oidc.NewVerifier(*cfg.SovereignReadOIDC)
		if verr != nil {
			return nil, fmt.Errorf("aos: verificador OIDC de soberania (AOS-205/AOS-174): %w", verr)
		}
		readCred = newOIDCReadCredential(v)
	}

	// (7c) DSAR / CRYPTO-SHREDDING (AOS-172, Art. 17). COMPÕE o fluxo DSAR já existente
	// (AOS-093) sobre: um vault de chaves de PII por-titular DEMO-GRADE (produção liga um KMS
	// real pela mesma porta), o legal hold (preservação P0) + índice titular→partição, e o WORM
	// do nó como EventSealer (received/key_destroyed/blocked selados SEM PII). O mesmo
	// [*audit.Shredder] satisfaz HoldOracle (Held) E ShreddableKeyStore (via dsar.AuditStore) —
	// a governança não é reimplementada, só cabelada no nó.
	//
	// COBERTURA DO SUBSTRATO (AOS-093 — NÚCLEO ENTREGUE): a erasure DSAR do nó destrói a
	// KEK por-titular do vault. Essa MESMA KEK cifra o CONTEÚDO DOS RUNS que o Event Store
	// persiste — a resposta do modelo e os resultados de tools do capturer de replay são
	// cifrados por-titular (envelope DEK/KEK) ANTES de tocar o WAL (ver (2c-pre) e
	// [replay.WithContentSealer]), e o step-ledger cifra o Result.Payload sob o titular do
	// run que o despacho lhe leva no contexto (AOS-245). O capturer regista subject→stream no DSARIndex (via
	// [contentSealer.SealContent]), pelo que o crypto-shredding e o legal hold POR-PARTIÇÃO
	// alcançam o SUBSTRATO. Apagar a KEK no /dsar/erase torna o conteúdo do run
	// IRRECUPERÁVEL (a decifragem falha) sem mutar o log — a hash-chain continua a validar.
	// RESIDUAL nomeado (eixo AOS-093): (a) turn.recorded persiste apenas hashes (prompt_hash/
	// system_hash), nunca o objective/prompt cru — nada a cifrar aí; (b) o REPLAY de um run
	// cujo conteúdo foi selado exige o acesso ao vault do titular pelo leitor (fora do âmbito
	// deste núcleo); (c) FECHADO por AOS-245 — o step-ledger selava só com Producer.NHIID, que o
	// nó nunca passava (o titular é por-RUN e o ledger é composto uma vez), pelo que o OUTPUT de
	// cada tool call ia EM CLARO para o WAL com o cifrador composto e inerte; agora o titular
	// chega pelo contexto do despacho e a ausência dele é RECUSADA
	// ([durable.WithRequireTitular]), não degradada. Ver DEF-301/A-DEF-301 em
	// docs/governance/REGISTO-Deferimentos.md.
	//
	// DEPENDÊNCIA que a selagem do ledger cria (AOS-215): o registo canónico do ledger passa a
	// ser decifrável só enquanto a KEK do titular viver. Num restart com a custódia de REFERÊNCIA
	// (vault in-memory) o Rebuild deixa de re-hidratar os passos selados e um step re-despachado
	// falha ao reler o canónico — é exactamente a combinação (substrato durável + KEK efémera)
	// que [ErrProductionNeedsDurableKEK] já PROÍBE em produção. Fora de produção mantém-se a
	// postura demo-grade declarada no banner.
	//
	// CON-02 (legal hold + job de expiração — superfície de ADMINISTRAÇÃO): foi DEFERIDA por
	// decisão do dono (Opção C, docs/governance/DOSSIE-CON-02-legal-hold.md, 2026-07-29) e está
	// agora ENTREGUE por AOS-213 — o gatilho da Opção C (o apagamento ser real) cumpriu-se com
	// AOS-093. O audit.LegalHold (dsarHolds, exposto em Node.DSARHolds) ganha rotas de
	// administração (POST /dsar/hold / /dsar/release — ver legalhold.go) e o audit.ExpirationJob
	// (AOS-092) É composto no nó (ver (7c-bis) abaixo, exposto em Node.ExpirationJob, conduzido
	// por POST /dsar/expire) — deixa de ter ZERO chamadores de produção. A barreira de hold cobre
	// o substrato (um titular retido não é shredded, logo o conteúdo cifrado mantém-se decifrável
	// enquanto o hold vigorar, provado em teste) E é respeitada pela expiração. DEF-903 fechado
	// (FECHADO-RESIDUAL) no REGISTO-Deferimentos.md; o residual de granularidade (por-titular vs
	// por-registo) fica nomeado com eixo em retention.go e no registo.
	dsarHolds := audit.NewLegalHold()

	// (7c-ter) RE-HIDRATAÇÃO do estado de governação a partir do substrato DURÁVEL (remediação
	// da W6). O legal hold e o índice titular→partição nascem VAZIOS, mas o facto que os origina
	// — cada `POST /dsar/hold` selado em [legalHoldPartition], cada conteúdo cifrado no Event
	// Store — sobrevive ao processo. Sem esta reposição, um restart deixava a cadeia a AFIRMAR
	// uma preservação que o nó já não sabia fazer valer; com o varredor AUTOMÁTICO de AOS-267 no
	// loop de serviço, isso é uma destruição de material sob ordem de preservação, sem ninguém no
	// caminho e irreversível. Ver governance_restore.go.
	//
	// FAIL-CLOSED sem abortar o arranque: um erro de leitura deixa `holdsRestored` a false, e
	// [retentionSchedulerArmed] recusa armar o varredor automático (a expiração continua
	// conduzível por `POST /dsar/expire`, que tem um humano autenticado a assumi-la).
	holdsRestored := true
	nHolds, err := restoreLegalHolds(ctx, worm, dsarHolds)
	if err != nil {
		holdsRestored = false
		log("legal hold: RE-HIDRATACAO FALHADA a partir da cadeia %q — o no nao consegue provar que as preservacoes seladas estao em vigor; o varredor AUTOMATICO de retencao (AOS-267) NAO arma (a expiracao fica so por POST /dsar/expire): %v", legalHoldPartition, err)
	}
	nLinks, err := restoreSubjectIndex(ctx, es, dsarIndex)
	if err != nil {
		holdsRestored = false
		log("indice titular->particao: RE-HIDRATACAO FALHADA — o legal hold POR-PARTICAO nao cobriria as demais particoes dos titulares antigos; o varredor AUTOMATICO de retencao (AOS-267) NAO arma: %v", err)
	}
	if holdsRestored && (nHolds > 0 || nLinks > 0) {
		log("estado de governacao RE-HIDRATADO do substrato duravel: %d alvo(s) sob legal hold repostos da cadeia %q, %d ligacao(oes) titular->particao repostas do Event Store", nHolds, legalHoldPartition, nLinks)
	}

	// (7c-ter) O DESMENTIDO DA CUSTÓDIA SOBREVIVE AO RESTART. Sem isto, o conjunto de destruições
	// por confirmar nascia vazio a cada arranque e o /readyz ficava VERDE sobre uma cadeia que
	// afirma um apagamento por provar. Mesmo eixo do `holdsRestored`; ver [restoreShredPending].
	//
	// FAIL-CLOSED SEM ABORTAR O ARRANQUE, na disciplina dos restauros acima: um erro de leitura
	// é anunciado e o nó arranca — recusar arrancar deixaria o serviço em baixo por causa de uma
	// cadeia ilegível, que é um remédio pior do que o mal.
	if nPend, perr := restoreShredPending(ctx, worm, "governance.dsar", dsarVault); perr != nil {
		log("DSAR/crypto-shred (achado 1.6): NAO foi possivel reconstruir as destruicoes por confirmar a partir da cadeia — o /readyz pode ficar VERDE sobre um apagamento por provar: %v", perr)
	} else if nPend > 0 {
		log("DSAR/crypto-shred (achado 1.6): %d destruicao(oes) de KEK POR CONFIRMAR repostas da cadeia governance.dsar — o /readyz fica VERMELHO ate uma destruicao confirmada das mesmas chaves; o desmentido deixou de morrer no restart", nPend)
	}

	dsarShredder := audit.NewShredder(dsarVault, dsarHolds, audit.NewRetentionPolicy(nil),
		audit.WithShredderSubjectIndex(dsarIndex))
	dsarFlow := dsar.NewFlow(
		wormEventSealer{store: worm},
		dsarShredder,
		[]dsar.ShreddableKeyStore{dsar.AuditStore("audit", dsarShredder)},
		dsar.WithPartition("governance.dsar"),
		dsar.WithShredConfirmer(confirmadorDeShredDe(dsarVault)),
	)

	// (7c-bis) AOS-213 (CON-02/DEF-903) — JOB DE EXPIRAÇÃO por TTL COMPOSTO no nó. Fecha a
	// superfície que a Opção C do dono sequenciou para DEPOIS de o apagamento ser real (AOS-093
	// entregou-o). O [audit.ExpirationJob] (AOS-092) — que era composto por ZERO chamadores de
	// produção — passa a agir sobre o substrato REAL: [eventStoreRecordSource] expõe os registos
	// classificados do Event Store (eventos "replay.captured" cifrados por-titular) e
	// [cryptoShredSink] materializa a expiração por crypto-shred da MESMA KEK por-titular que o
	// /dsar/erase destrói (apagamento real, não no-op — a prova de AOS-093 pela via da expiração).
	// RESPEITA o legal hold (o job salta os held) e SELA cada retention.expired no WORM
	// ([retentionPartition]) sem PII. É SEMPRE composto: com [Config.Retention] vazia nada expira
	// (fail-closed), com períodos definidos o TTL entra em vigor. Conduzido por POST /dsar/expire
	// (sob demanda / agendamento externo); a granularidade (por-titular) está resolvida e o eixo
	// residual nomeado em retention.go.
	// (7c-quater) A RECONCILIAÇÃO SOBREVIVE AO RESTART (achado 1.7). Sem isto, um deploy bastava
	// para o nó esquecer que uma destruição tinha ficado por confirmar, e a cadeia ficava sozinha
	// a afirmar uma irrecuperabilidade que não aconteceu. Ver [restoreReconciliation].
	//
	// FAIL-CLOSED SEM ABORTAR O ARRANQUE, na disciplina dos restauros acima: um erro de leitura é
	// anunciado e o nó arranca com o conjunto VAZIO — nesse caso perde-se a re-tentativa, não se
	// ganha uma destruição indevida.
	reconciliacao, nRec, rerr := restoreReconciliation(ctx, worm, retentionPartition)
	if rerr != nil {
		log("expiracao por TTL (achado 1.7): NAO foi possivel reconstruir os registos POR RECONCILIAR a partir da cadeia — uma destruicao que falhou antes do restart nao sera re-tentada: %v", rerr)
	} else if nRec > 0 {
		log("expiracao por TTL (achado 1.7): %d registo(s) com a destruicao POR CONFIRMAR repostos da cadeia %q — a proxima passagem re-tenta o sink SEM re-selar o retention.expired", nRec, retentionPartition)
	}

	expirationOpts := []audit.ExpirationOption{
		audit.WithExpirationAudit(worm, retentionPartition),
		audit.WithExpirationSubjectIndex(dsarIndex),
		audit.WithExpirationReconciliation(reconciliacao),
	}
	// IDEMPOTÊNCIA RE-HIDRATADA (remediação da W6). O seen-set default é in-memory: os eventos
	// cifrados permanecem no Event Store durável (o crypto-shred destrói a KEK, não os registos),
	// pelo que a fonte volta a listá-los com o TTL vencido e o PRIMEIRO varrimento após cada
	// restart re-selava um SEGUNDO `retention.expired` para o mesmo facto, re-contado no
	// `retention.sweep.completed`. Repõe-se o seen-set a partir dos eventos já selados — a mesma
	// disciplina do legal hold: o que a cadeia durável já afirma não se re-afirma.
	if idem, nIdem, ierr := restoreExpirationIdempotency(ctx, worm, retentionPartition); ierr != nil {
		log("idempotencia da expiracao: RE-HIDRATACAO FALHADA da cadeia %q — o proximo varrimento pode RE-SELAR expiracoes ja seladas (a cadeia fica poluida, nada e destruido a mais): %v", retentionPartition, ierr)
	} else {
		expirationOpts = append(expirationOpts, audit.WithExpirationIdempotency(idem))
		if nIdem > 0 {
			log("idempotencia da expiracao RE-HIDRATADA: %d expiracao(oes) ja seladas na cadeia %q nao serao re-seladas", nIdem, retentionPartition)
		}
	}
	if tracingEnabled {
		expirationOpts = append(expirationOpts, audit.WithExpirationTracer(tracer))
	}
	if cfg.RetentionClock != nil {
		expirationOpts = append(expirationOpts, audit.WithExpirationClock(cfg.RetentionClock))
	}
	expirationJob := audit.NewExpirationJob(
		cfg.Retention,
		dsarHolds,
		eventStoreRecordSource{es: es},
		cryptoShredSink{vault: dsarVault},
		expirationOpts...,
	)

	// (7d) MOTOR DE REDACÇÃO (AOS-091) LIGADO AO FECHO TRANSITIVO (AOS-208). A cablagem
	// que faltava: o objectivo de um run é INPUT DO UTILIZADOR e pode carregar PII em
	// claro. O [integration.IngestionGateway] redige-o na FRONTEIRA de ingestão e faz o
	// fan-out do valor REDIGIDO para as quatro portas — Event Store, platform/memory,
	// substrate/otel-genai e o WORM do nó — com o MESMO [redaction.Ingestor] e a mesma
	// política (a consistência que o doc.go do motor promete). É composto SEMPRE (o
	// default NÃO deixa o motor inalcançável — o defeito que AOS-208 fecha); o
	// [NodeService.Submit] invoca-o em cada run e substitui o objectivo pelo redigido.
	//
	// Política: [redaction.RemoveAllPolicy] (MINIMIZAÇÃO máxima — cada PII detectada é
	// substituída por [REDACTED:<classe>], sem KeySource nem tokens retidos), o mesmo
	// default seguro do preview do approval-card no demo (a MESMA disciplina, não uma
	// variante). O tracer é o do nó (NoopTracer quando a observabilidade está desligada
	// ⇒ o span de ingestão é descartado, zero overhead); o WORM é o REAL do nó (não o
	// decorado). O backend de memória é o [memadapters.EventStoreAdapter] sobre o MESMO
	// Event Store — pelo que a porta memory e a porta Event Store são a MESMA escrita.
	redactionEngine := redaction.NewEngine(nil) // RemoveAllPolicy não exige KeySource
	redactionPolicy := redaction.RemoveAllPolicy("aos-node-redaction-v1")
	ingestor := redaction.NewIngestor(redactionEngine, redactionPolicy)
	memPort := memadapters.NewEventStoreAdapter(es, memadapters.WithEventStoreTracer(tracer))
	memService := memory.NewService(memPort)
	ingestion, err := integration.NewIngestionGateway(ingestor, memService, tracer, worm)
	if err != nil {
		return nil, fmt.Errorf("aos: gateway de ingestao/redaccao (AOS-208/AOS-091): %w", err)
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
	// CANAL DE CONTROLO (AOS-160/AOS-193). O banner declara a CARDINALIDADE REAL de emissores
	// registados (lida do próprio authenticator, não da intenção da config) e a consequência
	// operacional de ela ser zero — um operador nunca tem de adivinhar porque é que o seu
	// `aos steer` leva 403 nem porque é que o bind a 0.0.0.0 foi recusado.
	if n := steerAuth.EmitterCount(); n > 0 {
		log("canal de controlo: Ed25519Authenticator (AOS-160) — %d operador(es) registado(s) via AOS_OPERATORS; HMACAuthenticator demo DESLIGADO", n)
	} else {
		log("canal de controlo: Ed25519Authenticator (AOS-160) composto mas SEM OPERADORES — steer/pause serao TODOS recusados (ErrUnknownEmitter) e o bind NAO-loopback e RECUSADO; defina AOS_OPERATORS=\"emitterID=hexpubkey\" para o tornar operavel")
	}
	if foureyes != nil {
		log("four-eyes gate (AOS-162) composto: %d aprovador(es) pinado(s) via AOS_APPROVERS_FILE", len(cfg.Approvers))
		// ATTESTATION DE DISPOSITIVO (AOS-177/AOS-266). O banner declara o estado REALMENTE
		// composto (LIGADA/DORMENTE), nunca a intenção — SEGUE o wiring acima.
		if attestationComposed {
			log("  attestation de dispositivo (AOS-177): LIGADA — cada perna EXIGE attestationObject+clientDataJSON WebAuthn, verificados pelo componente externo (AOS_ATTESTATION_VERIFIER_URL); attestation ausente/invalida => perna RECUSADA")
		} else {
			log("  attestation de dispositivo (AOS-177): DORMENTE — sem AOS_ATTESTATION_VERIFIER_URL o 4-eyes e SO estrutural (nao prova modelo nem posse do dispositivo); defina a URL do verificador externo para a ligar")
		}
		// ATRIBUIÇÃO DISPOSITIVO↔APROVADOR (AOS-266).
		if enrollmentComposed {
			log("  atribuicao dispositivo<->aprovador (AOS-266): LIGADA — %d aprovador(es) com dispositivo(s) registado(s) via AOS_DEVICE_ENROLLMENT_FILE; dispositivo atestado nao registado para o aprovador => perna RECUSADA (ErrDeviceNotEnrolled, default-deny)", len(cfg.DeviceEnrollment))
		} else {
			log("  atribuicao dispositivo<->aprovador (AOS-266): DORMENTE — sem dispositivos registados a attestation prova modelo+posse, NAO de quem e o autenticador; defina AOS_DEVICE_ENROLLMENT_FILE (default AOS_APPROVERS_FILE) para exigir atribuicao")
		}
		// FRESCURA POR-CERIMÓNIA (AOS-266, achado F10).
		if freshnessComposed {
			log("  frescura por-cerimonia (AOS-266): LIGADA — challenges EMITIDOS pelo no por (pedido, aprovador) com TTL=%s via POST /runs/{id}/challenge; a verificacao EXIGE challenge emitido e fresco (issue-then-consume); challenge nao emitido/expirado => perna RECUSADA (ErrChallengeNotIssued)", effectiveChallengeTTL(cfg.ChallengeTTL))
		} else {
			log("  frescura por-cerimonia (AOS-266): DORMENTE — anti-replay so por uso-unico duravel (dedup, sem frescura); quem detenha a chave de um aprovador pode reapresentar uma prova capturada num pedido novo; defina AOS_CHALLENGE_ISSUANCE=1 para exigir emissao server-side")
		}
	} else {
		log("four-eyes gate (AOS-162): DESLIGADO (sem aprovadores) — POST /runs/{id}/approve responde 501; defina AOS_APPROVERS_FILE=<ficheiro JSON montado> para o compor")
		// AVISO banner-mudo (AOS-266, achado F10): capacidades de endurecimento do 4-eyes
		// definidas SEM aprovadores são IGNORADAS em silêncio (o gate nem sequer é composto). O
		// banner deixa de ser mudo: nomeia cada env orfã e a razão de não ter efeito.
		if strings.TrimSpace(cfg.AttestationVerifierURL) != "" {
			log("  AVISO (AOS-266): AOS_ATTESTATION_VERIFIER_URL esta definida mas NAO ha aprovadores (AOS_APPROVERS_FILE) — o gate 4-eyes nao e composto e a URL de attestation e IGNORADA; a attestation so se liga AO 4-eyes")
		}
		if cfg.ChallengeIssuance {
			log("  AVISO (AOS-266): AOS_CHALLENGE_ISSUANCE=1 mas NAO ha aprovadores — sem 4-eyes composto nao ha emissao de challenges nem endpoint /runs/{id}/challenge; defina AOS_APPROVERS_FILE")
		}
		if len(cfg.DeviceEnrollment) > 0 {
			log("  AVISO (AOS-266): AOS_DEVICE_ENROLLMENT_FILE traz dispositivos mas NAO ha aprovadores — enrollment IGNORADO sem 4-eyes composto; defina AOS_APPROVERS_FILE")
		}
	}
	// PROMOTION CONTROLLER (AOS-159/AOS-206). O banner declara o estado REALMENTE COMPOSTO (a
	// via sancionada com anti-replay SEMPRE ligado) e a cardinalidade REAL de ratificadores
	// (lida do controller, não da intenção da config) — um roster vazio é declarado como tal,
	// sem capacidade-fantasma.
	if n := promotion.RatifierCount(); n > 0 {
		log("promotion controller (AOS-159/AOS-206) composto via NewProductionRatificationGate — freshness+nonce-store DURAVEL FORCADOS; %d ratificador(es) pinado(s) via AOS_RATIFIERS; ratificacao re-submetida apos consumo => ratification_replayed", n)
	} else {
		log("promotion controller (AOS-159/AOS-206) composto via NewProductionRatificationGate (freshness+nonce-store DURAVEL FORCADOS) mas SEM RATIFICADORES — toda a promocao sera NEGADA (ratifier_unknown); defina AOS_RATIFIERS=\"principal=hexpubkey\" para pinar ratificadores")
	}
	// AOS-275 (achado F7): a submissao de ratificacoes DEIXOU de ser deferida — ha rota externa.
	// O banner nomeia-a e declara o que ela NAO afrouxa (a decisao continua a ser do MESMO gate),
	// para que nenhum operador leia "endpoint" como "caminho alternativo mais fraco".
	log("promotion controller (AOS-275): ROTA EXTERNA POST /promote — MESMA admissao do /approve (token-bucket do plano de controlo + mTLS de controlo quando composto) e MESMA disciplina non-signing (a assinatura ed25519 do ratificador vem no corpo; o no nunca detem a chave privada); a decisao e do MESMO gate de producao (freshness + nonce-store DURAVEL), pelo que uma ratificacao re-submetida apos consumo e ratification_replayed TAMBEM pela rota; toda a decisao terminal e selada no WORM com o PRINCIPAL do ratificador")
	log("promotion controller (AOS-275): o corpo do POST /promote produz-se FORA do no com `aos-issuer ratify-sign --artifact-id ... --version ... --content-hash ... --ratifier ... --key-file <seed>` — a chave privada do ratificador NUNCA entra neste processo (non-signing, ADR-016 §1)")
	log("NOTA promotion controller: o PIPELINE de promocao (staging->eval-gate->canary->producao, AOS-096) continua a montante e fora do no — a rota ratifica um artefacto candidato APRESENTADO pelo operador; o que o no garante e que NENHUM chega a producao sem ratificacao humana assinada, fresca e de uso-unico")
	log("substrato: %s", substrateMode(cfg))
	// TAMPER-EVIDENCE DO WORM IMPOSTA (AOS-221). O banner declara o estado REALMENTE
	// verificado (não a intenção): a hash-chain foi RE-ENCADEADA e verificada no arranque —
	// uma cadeia adulterada teria ABORTADO aqui, fail-closed — ou, para um WORM injectado
	// opaco, que a verificação integral não foi possível PELO NÓ (custódia do chamador).
	if wormPartitionsOpaque {
		log("tamper-evidence do WORM (AOS-221): WORM INJECTADO nao expoe as particoes (audit.PartitionLister) — verificacao integral da hash-chain NAO feita pelo no (custodia/durabilidade do chamador); o WORM duravel/in-memory do no re-encadeia sempre")
	} else {
		log("tamper-evidence do WORM (AOS-221): hash-chain RE-ENCADEADA e verificada no arranque (%d particao(oes)) — nao so o CRC de framing; uma cadeia adulterada ABORTARIA o arranque fail-closed (audit.Verify chamado no restart e pos-shred)", wormVerifiedParts)
	}
	// VERIFICAÇÃO ANCORADA DO WORM (AOS-268/AOS-072). Linha PRÓPRIA e logo a seguir à de AOS-221
	// porque é a metade complementar: AOS-221 re-encadeia (sem chave), AOS-268 ancora (com
	// trust-anchor out-of-band). Duas dobras — ancorado (o piso de frescura + a âncora assinada
	// fecham a truncatura do tail e a reescrita da génese) vs não-ancorado (declara o vector que
	// FICA aberto e como o fechar). O argumento deriva do ESTADO real da verificação
	// (`wormAnchored`), nunca da intenção da config: se WORMAnchor foi dado e a verificação
	// PASSOU, `wormAnchored` é true; ausente ⇒ false.
	for _, line := range wormAnchorPostureBanner(wormAncoradas, wormTotal) {
		log("%s", line)
	}
	// EXECUÇÃO DURÁVEL (AOS-180/AOS-191). O banner declara o estado REALMENTE COMPOSTO
	// (não a intenção da config) e sobre que substrato — para que um operador nunca tenha
	// de adivinhar se checkpointer/capturer/step-ledger estão ligados.
	if checkpointer != nil && capturer != nil && ledger != nil {
		log("execucao duravel (AOS-180): LIGADA — checkpointer + capturer + step-ledger COMPOSTOS sobre o event store (%s); o tool set congelado (AOS-155) persiste no mesmo store", describeSubstrateEx(cfg.EventStore != nil, cfg.EventStorePath, cfg.EventStoreNATS))
		if cfg.EventStore != nil {
			log("NOTA execucao duravel: o event store foi FORNECIDO POR CONFIG — a durabilidade dos checkpoints/capturas/ledger e a DESSE store; o no nao a pode atestar (com AOS_EVENTSTORE_PATH e duravel em disco por construcao)")
		}
	} else {
		log("execucao duravel (AOS-180): DESLIGADA — checkpointer/capturer/step-ledger NAO compostos (defaults no-op AOS-013); defina AOS_DURABLE_EXECUTION=1 (exige AOS_EVENTSTORE_PATH) para ligar")
	}
	// MEDIAÇÃO DE POLÍTICA (AOS-220). O banner declara o estado REALMENTE composto do PDP — para
	// que o default-deny nunca seja um acidente silencioso: ou um BUNDLE ASSINADO está carregado
	// (o nó medeia tool calls pela allowlist assinada + regras Cedar), ou o PDP está NÃO-CARREGADO
	// e TODA a tool call mediada é negada fail-closed, DECLARADAMENTE.
	if cfg.PDP != nil {
		log("mediacao de politica (AOS-220): PDP com BUNDLE CARREGADO (AOS_POLICY_BUNDLE_DIR) — trust anchor FORCADO out-of-band (AOS_POLICY_TRUST_ANCHOR), NAO lido do bundle; politica em vigor versao %q; tool calls decididas pela allowlist assinada + regras Cedar", cfg.PDP.ActiveVersion())
	} else {
		log("mediacao de politica (AOS-220): PDP NAO-CARREGADO (NewUnloaded) — DEFAULT-DENY EXPLICITO de TODA a tool call mediada; defina AOS_POLICY_BUNDLE_DIR + AOS_POLICY_TRUST_ANCHOR (pubkey ed25519 out-of-band) para carregar um bundle assinado")
	}
	// AS QUATRO SUPERFÍCIES QUE O BANNER CALAVA (AOS-248, achado F14). Ficam JUNTAS e logo a
	// seguir à mediação de política porque é com ela que o operador as compõe mentalmente: o PDP
	// diz o que é permitido, a autonomia diz o que ainda assim EXIGE um humano, o modelo diz quem
	// propõe as acções, e o orçamento/broker dizem o que NÃO as limita nem lhes guarda os
	// segredos. Cada linha segue o estado REALMENTE composto — ver posture_banner.go, onde a
	// justificação de cada afirmação está amarrada ao código que a suporta.
	for _, line := range autonomyPostureBanner(cfg.Autonomy) {
		log("%s", line)
	}
	for _, line := range modelPostureBanner(cfg.Model) {
		log("%s", line)
	}
	// AOS-256/AOS-257: o argumento DERIVA do que foi REALMENTE composto — `runBudget` é o
	// mesmo valor entregue a [integration.SecuredConfig.Budget], não a intenção da config. Um
	// tecto configurado ⇒ o hook real está na cadeia e o nó por-run é registado em cada run;
	// nil ⇒ o ponto de injecção ficou com o stub neutro. O guard-test de AOS-255
	// (aos255_budget_scope_test.go) sela que este argumento nunca volta a ser um literal.
	for _, line := range budgetPostureBanner(runBudget != nil) {
		log("%s", line)
	}
	// AOS-261/AOS-262: mesma disciplina — o argumento é o observador REALMENTE composto
	// (`progress`, o mesmo valor entregue a agentruntime.WithProgressObserver), nunca a
	// intenção da config. Vem LOGO A SEGUIR ao orçamento porque é a leitura desse tecto.
	for _, line := range burndownPostureBanner(progress != nil, progressThreshold) {
		log("%s", line)
	}
	// O ESTADO, não a intenção: os campos vêm do que este composition-root REALMENTE carregou.
	// `hardened` é o mesmo predicado que a guarda de [ErrConflictingIssuerKey] usa, para que a
	// linha do banner e a recusa do arranque não possam divergir.
	for _, line := range credentialBrokerPostureBanner(materialPrivadoDoNo{
		Endurecido:    hardened,
		IssuerKey:     cfg.IssuerKeyPath != "" || cfg.IssuerSigningKey != nil,
		OTLPClientKey: cfg.OTLPClientKeyPath != "",
		OTLPBearer:    cfg.OTLPBearerTokenPath != "",
	}) {
		log("%s", line)
	}
	// AOS-264: o Vault de credenciais downstream do broker, PREPARADO por
	// AOS_BROKER_VAULT_* (separado do KEK, D7). Declara "configurado, troca pendente"
	// ou "dormente" — nunca "broker ligado" (a troca só medeia algo em AOS-265). O
	// argumento deriva do ESTADO composto: `cfg.BrokerVault != nil` ⇒ preparado.
	var brokerVaultSet *brokerVaultSettings
	if cfg.BrokerVault != nil {
		brokerVaultSet = &brokerVaultSettings{Addr: cfg.BrokerVaultAddr, KVMount: cfg.BrokerVaultKVMount}
	}
	for _, line := range brokerVaultPostureBanner(brokerVaultSet) {
		log("%s", line)
	}
	// AUTORIDADE DE ESCOPO (AOS-071). O banner distingue a defesa-em-profundidade ACTIVA
	// da DORMENTE: sem directório externo, o ScopeGate acaba a re-verificar o que o hook
	// de identidade já impôs e NÃO há revogação — um token válido vale até expirar. Com
	// directório, há segunda opinião independente e revogação, e o fingerprint diz QUAL
	// directório está em vigor (uma rotação é visível sem despejar a lista de sujeitos).
	if dir, ok := cfg.Authority.(*authorityDirectory); ok {
		log("autoridade de escopo (AOS-071): DIRECTORIO EXTERNO ligado (AOS_AUTHORITY_FILE) — %d sujeito(s), %d revogado(s), revisao %d, fingerprint %s; o escopo efectivo e token INTERSECTADO com o directorio: restringe e REVOGA, nunca amplia (a ampliacao esta estruturalmente fora do alcance do gate)", dir.Subjects, dir.Revoked, dir.Revision, dir.Fingerprint)
		log("=> NOTA AOS-071: um sujeito AUSENTE do directorio NAO e restringido (cai na autoridade do token) — e o que torna seguro ligar um directorio PARCIAL. REVOGAR nao e remover: liste o sujeito com \"capabilities\": [] para lhe negar tudo")
	} else {
		log("autoridade de escopo (AOS-071): sem directorio externo — a defesa-em-profundidade fica DORMENTE (o gate verifica capability ∈ token.Scope, que a identidade ja impos) e NAO ha revogacao: um token valido vale ate expirar. Defina AOS_AUTHORITY_FILE para a segunda opiniao independente e revogacao/RBAC organizacional")
	}
	if readAuthority != nil {
		if readCred != nil {
			// Declara HONESTAMENTE a postura anti-replay REALMENTE composta (fecha o achado da
			// auditoria v4): o tecto de idade (MaxAge) limita a janela de um token capturado
			// INDEPENDENTEMENTE de jti; RequireJTI (opt-in) impõe single-use estrito quando o IdP
			// emite jti. Sem capacidade-fantasma — o banner nunca anuncia mais do que o wiring aplica.
			jtiPosture := "por-jti quando o IdP emite jti"
			if cfg.SovereignReadOIDC != nil && cfg.SovereignReadOIDC.RequireJTI {
				jtiPosture = "jti OBRIGATORIO (single-use estrito)"
			}
			var maxAge time.Duration
			if cfg.SovereignReadOIDC != nil {
				maxAge = cfg.SovereignReadOIDC.MaxAge
			}
			log("soberania de leitura (AOS-172/AOS-205, D7): READ-PATH SOBERANO FAIL-CLOSED ligado sobre FONTE DE AUTORIDADE board->regiao (rotacao+auditoria) — %d board(s), revisao %d, fingerprint %s; CREDENCIAL FORTE do leitor VERIFICADA (OIDC AOS-174) — board/reader das CLAIMS, o header X-Aos-Board NAO autoriza; anti-replay: idade maxima do token %s + reutilizacao recusada %s; leitura sensivel SELADA no WORM (D6)", readAuthority.Len(), readAuthority.Revision(), fingerShort(readAuthority.Fingerprint()), maxAge, jtiPosture)
		} else {
			log("soberania de leitura (AOS-172/AOS-205, D7): READ-PATH SOBERANO FAIL-CLOSED ligado sobre FONTE DE AUTORIDADE board->regiao (rotacao+auditoria) — %d board(s), revisao %d; credencial do leitor DEMO-GRADE por headers (X-Aos-Reader/X-Aos-Board), aceite so FORA de producao — defina AOS_SOVEREIGN_OIDC_ISSUER+AOS_SOVEREIGN_OIDC_AUDIENCE para exigir credencial forte OIDC; leitura sensivel SELADA no WORM (D6)", readAuthority.Len(), readAuthority.Revision())
		}
		log("NOTA AOS-205: o TENANT concreto (IdP de soberania da organizacao que empurra o board->regiao autoritativo) fica DEFERIDO — a semente/rotacoes entram por config/operador; o no fica com o CONTRATO (fonte rotacionavel+auditada e verificacao de credencial). A coincidencia leitor.regiao==run.regiao POR-RUN esta ENTREGUE (AOS-182/DEF-202): residencia selada na criacao, authorizeReadComCausa recusa cross-region")
	} else {
		log("soberania de leitura (AOS-172, D7): read-path LEGADO (sem authz por-chamador nem selo) — defina Config.BoardRegions para ligar a regra fail-closed")
	}
	log("DSAR/crypto-shredding (AOS-172, Art. 17): fluxo composto (POST /dsar/erase) — legal hold re-consultado antes do shred; received/key_destroyed/blocked selados no WORM sem PII")
	// CUSTÓDIA DA KEK (AOS-215/DEF-302). O banner DECLARA a postura REALMENTE composta: custódia
	// externa injectada vs referência in-memory demo-grade — sem esconder que a referência perde
	// as KEK no restart. É a mesma classe de honestidade do substrato/identidade.
	if dsarVaultInjected {
		log("custodia da KEK (AOS-215/DEF-302): vault de PII por-titular INJECTADO por config (custodia EXTERNA pela porta audit.KeyVault) — as KEK vivem FORA do processo; a durabilidade/rotacao e do custodiante (o no nao a atesta); /dsar/erase e a expiracao destroem a KEK NESSE vault")
	} else {
		log("custodia da KEK (AOS-215/DEF-302): vault de PII por-titular de REFERENCIA in-memory — DEMO-GRADE: as KEK vivem em MEMORIA do processo, NAO-duraveis (perdem-se no restart); injecte Config.DSARVault (key-service/software-KMS de custodia externa) para producao; o KMS/HSM real e infra-org por tras da mesma porta")
	}
	// LEGAL HOLD + EXPIRAÇÃO (AOS-213, CON-02/DEF-903). O banner declara a superfície de
	// administração REALMENTE composta e o MODO de expiração (sob demanda por rota) — sem
	// capacidade-fantasma. A granularidade (por-titular) e o eixo residual são declarados.
	log("legal hold (AOS-213): POST /dsar/hold / POST /dsar/release (por titular e/ou particao) — MESMA credencial forte do /dsar/erase (readGov); cada accao selada no WORM sem PII (particao %q)", legalHoldPartition)
	if _, hasPII := cfg.Retention.Period(audit.ClassPIIOperational); hasPII || cfg.Retention.Version() != "" {
		log("expiracao por TTL (AOS-092/AOS-213): ExpirationJob COMPOSTO (politica %q) — conduzido por POST /dsar/expire (sob demanda) e, QUANDO O NO SERVE, pelo scheduler INTERNO do loop de servico (AOS-267, cadencia AOS_RETENTION_SWEEP_INTERVAL; um `aos` que so faz bootstrap sem AOS_API_ADDR nao tem loop de servico, logo nao tem scheduler — o banner do servico declara a postura real); expiracao = crypto-shred da KEK POR-TITULAR (AOS-093, apagamento real), RESPEITA o legal hold; retention.expired selado no WORM (particao %q)", cfg.Retention.Version(), retentionPartition)
		log("NOTA granularidade (AOS-213): o TTL e por-registo/classe mas o crypto-shred e por-CHAVE-DE-TITULAR (uma KEK embrulha todos os registos do titular) => a expiracao e POR-TITULAR; a retencao diferencial por-classe DENTRO de um titular colapsa para a classe que expira primeiro (residual nomeado, eixo AOS-093/envelope; ver DEF-903)")
	} else {
		log("expiracao por TTL (AOS-092/AOS-213): ExpirationJob COMPOSTO mas SEM politica de retencao (Config.Retention vazia) — POST /dsar/expire varre mas NADA expira (fail-closed); defina a politica de retencao TTL-por-classe para ligar o TTL")
	}
	// MOTOR DE REDACÇÃO (AOS-091/AOS-208). O banner declara que o motor está LIGADO ao
	// fecho transitivo do nó e a política em vigor — sem capacidade-fantasma. Ligar o
	// motor MUDA o comportamento por omissão: o objectivo de cada run passa a ser
	// redigido na ingestão (um objectivo sem PII é byte-idêntico; um com PII segue
	// minimizado para [REDACTED:<classe>]). É essa a decisão que o banner torna visível.
	log("redaccao de PII (AOS-091/AOS-208): motor LIGADO ao fecho transitivo — objectivo de cada run redigido na ingestao (RemoveAllPolicy) ANTES do Event Store/memory/spans/audit; MESMO Ingestor e mesma politica em todas as portas")
	if capturer != nil {
		log("DSAR/substrato (AOS-093): o conteudo dos runs (resposta do modelo + resultados de tools do capturer) e CIFRADO POR-TITULAR (envelope DEK/KEK) antes de tocar o Event Store; a erasure destroi a MESMA KEK => o conteudo fica IRRECUPERAVEL e a hash-chain continua a validar; subject->stream registado no DSARIndex (shred/hold alcancam o substrato)")
	} else {
		log("DSAR/substrato (AOS-093): execucao duravel DESLIGADA => sem capturer, o conteudo dos runs nao e persistido no Event Store; a cifra por-titular activa-se com AOS_DURABLE_EXECUTION=1")
	}
	if tracingEnabled {
		if otlpExp != nil {
			log("observabilidade OTLP (AOS-173): tracer REAL -> exporter OTLP/HTTP fail-open (spans invoke_agent/chat[+custo]/execute_tool/freeze + selos WORM)")
			// AUTENTICAÇÃO FORTE perante o colector (DEF-012, EIXO 2). O banner declara o estado
			// REALMENTE composto, sem revelar o segredo (o bearer nunca é impresso — só se ESTÁ
			// ou NÃO ligado). Fail-open preservado: uma recusa do colector nunca quebra um run.
			mtlsOTLP := cfg.OTLPClientCertPath != "" && cfg.OTLPClientKeyPath != ""
			bearerOTLP := cfg.OTLPBearerTokenPath != ""
			switch {
			case mtlsOTLP && bearerOTLP:
				log("autenticacao OTLP (DEF-012): mTLS de cliente + bearer LIGADOS (material por ficheiro; fail-open intacto — recusa do colector nao quebra o run)")
			case mtlsOTLP:
				log("autenticacao OTLP (DEF-012): mTLS de cliente LIGADO (par cert+chave por ficheiro; fail-open intacto)")
			case bearerOTLP:
				log("autenticacao OTLP (DEF-012): bearer LIGADO (token por ficheiro, nunca logado; fail-open intacto)")
			default:
				log("autenticacao OTLP (DEF-012): DESLIGADA — so TLS de servidor (se https); defina AOS_OTLP_CLIENT_CERT_PATH+KEY ou AOS_OTLP_BEARER_TOKEN_PATH para autenticar o cliente")
			}
		} else {
			log("observabilidade OTLP (AOS-173): tracer REAL -> exporter injectado por config (spans invoke_agent/chat[+custo]/execute_tool/freeze + selos WORM)")
		}
	} else {
		log("observabilidade OTLP (AOS-173): DESLIGADA (NoopTracer, zero overhead) — defina AOS_OTLP_ENDPOINT para exportar traces")
	}

	success = true    // o bootstrap concluiu: a guarda de limpeza não fecha os stores.
	arranqueOK = true // ... nem larga a posse dos ficheiros (AOS-285/284): passa a ser do Node.Close.
	return &Node{
		Runtime: sec,
		// A ancora que PASSOU no arranque, para o /metrics a poder declarar. Fail-closed acima:
		// se nao tivesse passado, nao se chegava aqui.
		ancora:           cfg.WORMAnchor,
		Steer:            steer,
		FourEyes:         foureyes,
		ApprovalBroker:   approvalBroker,
		PendingApprovals: pendingApprovals,
		ResumeRecords:    resumeRecords,
		ChallengeIssuer:  challengeIssuer, // nil quando a frescura por-cerimónia (AOS-266) está DORMENTE
		Promotion:        promotion,       // SEMPRE composto (AOS-206) — via sancionada, anti-replay forçado
		Authority:        authority,       // nil no modo endurecido (a autoridade corre fora do processo)
		Verifier:         verifier,
		SteerAuth:        steerAuth,
		Autonomy:         cfg.Autonomy,
		EventStore:       es,
		WORM:             worm, // o store REAL (não decorado): o ciclo de vida/leitura é sobre este
		IdentityMode:     identityMode,
		Tracer:           tracer,
		sloTap:           sloTap, // AOS-274: nil quando a observabilidade OTLP está desligada
		Ingestion:        ingestion,

		Checkpointer:  checkpointer, // nil quando a execução durável está desligada
		Capturer:      capturer,     // (os três são compostos/omitidos EM CONJUNTO)
		Ledger:        ledger,
		Compensations: compensations, // AOS-254: registo de compensações (nil sem execução durável)

		SovereignReadRegions:    readRegions,
		SovereignAuthority:      readAuthority,
		SovereignReadCredential: readCred,
		DSAR:                    dsarFlow,
		DSARHolds:               dsarHolds,
		holdsRestored:           holdsRestored, // prova de re-hidratação (antecedente do varredor automático)
		DSARVault:               dsarVault,
		DSARIndex:               dsarIndex,
		ExpirationJob:           expirationJob,
		Retention:               cfg.Retention, // AOS-267: o loop de serviço decide o scheduler por ela
		IssuerID:                cfg.IssuerID,  // AOS-267: nomeia o nó no selo em nome próprio
		contentOpener:           contentCipher, // AOS-214: o MESMO cifrador que sela decifra o replay soberano
		stateGates:              stateGates,    // AOS-218: fonte do StateGate durável por-run para o steer
		breakers:                breakers,      // AOS-080/081/251: disjuntores por-run (libertados no fim do run)
		progress:                progress,      // AOS-261/262: burn-down + aviso por-run (libertado no fim do run)

		ownsEventStore: ownsES,
		posseWAL:       posse,
		ownsWORM:       ownsWORM,
		otlp:           otlpExp,
		orcamento:      runBudget,
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
	// AOS-285 — largar a posse do Event Store DEPOIS de o fechar. A ordem importa: entre
	// largar e fechar haveria um instante em que outra réplica podia tomar a posse com o
	// WAL ainda aberto por nós, que é exactamente o estado que o guard existe para não
	// haver. Nil-safe num nó sem guard.
	if err := n.posseWAL.Largar(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// VerifyWORM re-encadeia e verifica a hash-chain de TODAS as partições do WORM do nó
// (AOS-221). É a MESMA verificação SEM CHAVE PRIVADA do arranque, exposta para os pontos
// PÓS-SHRED: após um crypto-shred (POST /dsar/erase — AOS-093 — ou a expiração por TTL de
// AOS-213) o nó chama-a para PROVAR que o shred destruiu a KEK SEM mutar a cadeia (o shred
// apaga a CHAVE, não os registos selados — a hash-chain TEM de continuar a validar). Um WORM
// injectado opaco (sem [audit.PartitionLister]) devolve [audit.ErrPartitionsUnavailable]: a
// verificação integral é do chamador, não uma falha do shred. Um [*audit.VerifyError]
// (desembrulha para [audit.ErrTampered]) sinaliza cadeia partida — o chamador impõe fail-closed.
func (n *Node) VerifyWORM(ctx context.Context) error {
	_, err := audit.VerifyStore(ctx, n.WORM)
	return err
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
	es := describeSubstrateEx(cfg.EventStore != nil, cfg.EventStorePath, cfg.EventStoreNATS)
	worm := describeSubstrate(cfg.WORM != nil, cfg.WORMPath)
	if es == worm {
		return es
	}
	return "event-store=" + es + ", worm=" + worm
}

// describeSubstrate classifica a proveniência de um store: fornecido por config,
// durável em disco (AOS-170) ou in-memory de referência (não-durável).
func describeSubstrate(provided bool, path string) string {
	return describeSubstrateEx(provided, path, "")
}

// describeSubstrateEx acrescenta o caso do Event Store REPLICADO (AOS-100). O banner
// tem de o distinguir do WAL local: sao substratos com garantias DIFERENTES, e um
// operador que leia «duravel em disco» quando esta sobre um cluster fica sem saber se
// pode correr uma segunda replica.
func describeSubstrateEx(provided bool, path, nats string) string {
	switch {
	case provided:
		return "fornecido por config"
	case nats != "":
		return "replicado em " + nats + " (AOS-100 — arbitra entre processos)"
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

// Os números FABRICADOS do modelo de referência. São constantes NOMEADAS e não literais porque
// deixaram de ser só decoração de observabilidade: desde AOS-260 o custo de um turno é debitado na
// árvore de orçamento, pelo que [requireCostSourceForBudgetCap] tem de os poder citar ao operador
// quando RECUSA um tecto em dólares sobre este modelo. Ver o comentário de [ErrBudgetCostNoPriceSource].
const (
	referenceModelInputTokens  = 12
	referenceModelOutputTokens = 8
	// referenceModelCostMicroUSD é 0,0015 USD. NÃO é uma medição — é um valor de referência para
	// que o span `chat` do modelo de referência não saia com custo nulo. Com um tecto em dólares
	// configurado seria autoridade de enforcement derivada de um número inventado, e é por isso
	// que o arranque recusa a combinação em vez de a servir.
	referenceModelCostMicroUSD = 1500
)

// Call implementa [agentruntime.ModelClient]. Devolve um uso e um CUSTO não-nulos: o
// span chat que o RT abre em volta desta chamada carrega AttrCostMicroUSD/AttrCostUSD
// (ver kernel/agent-runtime/loop.go callModel), pelo que a observabilidade OTLP (AOS-173)
// exporta um custo por turno mesmo com o modelo de referência (o Model Gateway real,
// com custo por provedor, é EPIC-06). O custo é micro-USD INTEIRO (fonte de verdade) e
// FABRICADO — o banner declara-o, e o arranque recusa pendurar-lhe um tecto em dólares.
func (referenceModel) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{
		Text:         "no `aos`: modelo de referencia (Model Gateway real = EPIC-06)",
		Final:        true,
		Usage:        agentruntime.Usage{InputTokens: referenceModelInputTokens, OutputTokens: referenceModelOutputTokens},
		CostMicroUSD: referenceModelCostMicroUSD,
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
