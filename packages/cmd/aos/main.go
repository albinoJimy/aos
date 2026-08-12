package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	oidc "github.com/aos-ref/integration/oidc"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// ErrProductionNeedsHardenedIdentity — sob AOS_MODE=production o nó RECUSA arrancar no
// modo de referência (autoridade co-localizada): exige o trust-anchor-only via
// AOS_ISSUER_PUBKEY, para que nenhuma chave de assinatura viva no processo do nó. Um
// operador não pode, por engano, correr o arranque de referência como fronteira de
// produção endurecida (fail-closed).
var ErrProductionNeedsHardenedIdentity = errors.New("aos: AOS_MODE=production exige AOS_ISSUER_PUBKEY (trust-anchor-only) — a autoridade co-localizada de referencia nao e uma fronteira de producao")

// ErrBadIssuerPubKey — AOS_ISSUER_PUBKEY presente mas não é uma pubkey ed25519 válida
// (32 bytes em hex). Fail-closed: um anchor malformado nunca compõe o verifier.
var ErrBadIssuerPubKey = errors.New("aos: AOS_ISSUER_PUBKEY invalida (esperado 64 hex chars = 32 bytes ed25519)")

// ErrBadBoardRegions — AOS_BOARD_REGIONS presente mas com uma entrada MALFORMADA (sem '=',
// ou com board/região vazios). Fail-closed de CONFIG da soberania de leitura (AOS-172, D7):
// distingue "não configurado" (vazio ⇒ read-path legado deliberado) de "configurado mas
// inválido" (aborta). Um operador que TENCIONA ligar a soberania mas comete um typo (ex.:
// "aos-demo" sem '=') NÃO obtém um nó que serve TODAS as leituras sem authz por-chamador
// (D7) nem selo (D6) — a ambiguidade de soberania NEGA o arranque, coerente com o resto do
// fail-closed do boot (ErrProductionNeedsHardenedIdentity, ErrBadIssuerPubKey).
var ErrBadBoardRegions = errors.New("aos: AOS_BOARD_REGIONS invalida (esperado \"board=regiao,board2=regiao2\" — entrada malformada aborta em vez de degradar silenciosamente para o read-path aberto)")

// ErrProductionNeedsSovereignRead — sob AOS_MODE=production a soberania de leitura NÃO pode
// estar desligada por engano: exige-se um registo board→região NÃO-VAZIO (a par de
// ErrProductionNeedsHardenedIdentity para a identidade). Um nó de produção nunca serve o
// read-path LEGADO (sem authz por-chamador D7 nem selo D6) — fail-closed.
var ErrProductionNeedsSovereignRead = errors.New("aos: AOS_MODE=production exige AOS_BOARD_REGIONS nao-vazio (read-path soberano fail-closed) — a producao nao serve leituras sem authz por-chamador nem selo WORM")

// ErrProductionNeedsSovereignAuthority — sob AOS_MODE=production a FONTE DE AUTORIDADE da
// soberania (AOS-205) tem de ter CREDENCIAL FORTE verificada: exige-se a config do verificador
// OIDC do leitor de governação/operador DSAR (AOS_SOVEREIGN_OIDC_ISSUER + AOS_SOVEREIGN_OIDC_AUDIENCE).
// Hoje a produção já recusa SEM soberania (ErrProductionNeedsSovereignRead), mas ACEITAVA o mapa
// self-hosted de AOS_BOARD_REGIONS COMO SE fosse autoridade da organização — com o board
// AUTO-DECLARADO no header X-Aos-Board. Sem uma fonte de credencial forte, um X-Aos-Board forjado
// autorizaria a leitura. Fail-closed: a produção nunca deriva o board de um header cru.
var ErrProductionNeedsSovereignAuthority = errors.New("aos: AOS_MODE=production exige credencial forte verificada para a soberania de leitura — defina AOS_SOVEREIGN_OIDC_ISSUER e AOS_SOVEREIGN_OIDC_AUDIENCE (o leitor/operador DSAR apresenta um ID-token OIDC verificado; o board vem das claims, nao do header X-Aos-Board auto-declarado). O mapa self-hosted AOS_BOARD_REGIONS nao e, por si so, autoridade da organizacao")

// ErrBadSovereignOIDC — a config do verificador OIDC de soberania (AOS-205) está presente mas
// INVÁLIDA: um de AOS_SOVEREIGN_OIDC_ISSUER/AOS_SOVEREIGN_OIDC_AUDIENCE em falta, ou
// AOS_SOVEREIGN_OIDC_MAX_AGE não-parseável/≤0, ou AOS_SOVEREIGN_OIDC_REQUIRE_JTI não-booleano.
// Fail-closed de CONFIG, no padrão de ErrBadBoardRegions: distingue "não configurado" (ambos
// vazios ⇒ via legada, fora de produção) de "configurado mas inválido" (aborta). Um operador que
// define só o issuer NÃO obtém um nó que serve leituras sem verificar a credencial; um MaxAge
// inválido NÃO degrada para "sem tecto de replay".
var ErrBadSovereignOIDC = errors.New("aos: config OIDC de soberania incompleta — AOS_SOVEREIGN_OIDC_ISSUER e AOS_SOVEREIGN_OIDC_AUDIENCE sao AMBOS obrigatorios (definir so um aborta em vez de degradar para o read-path por header auto-declarado)")

// ErrBadHumanOIDC — a config do directório humano OIDC (frente 1 do D4, AOS-174; fecha DEF-110)
// está presente mas INVÁLIDA: um de AOS_HUMAN_OIDC_ISSUER/AOS_HUMAN_OIDC_AUDIENCE em falta, ou o
// OIDCDirectory não constrói (ex.: issuer http não-loopback). Fail-closed de CONFIG (padrão de
// ErrBadSovereignOIDC): distingue "não configurado" (ambos vazios ⇒ allowlist de referência) de
// "configurado mas inválido" (aborta). Um operador que define só o issuer NÃO obtém um nó que
// autentica humanos por allowlist quando tencionava ligar o IdP.
var ErrBadHumanOIDC = errors.New("aos: config do directorio humano OIDC incompleta — AOS_HUMAN_OIDC_ISSUER e AOS_HUMAN_OIDC_AUDIENCE sao AMBOS obrigatorios (definir so um aborta em vez de degradar para a allowlist de referencia)")

// ErrBadDurableExecution — AOS_DURABLE_EXECUTION presente com um valor que NÃO é um
// booleano reconhecido. Fail-closed de CONFIG (AOS-191), no padrão de ErrBadBoardRegions:
// lixo NÃO é tratado como false. Um operador que escreve `AOS_DURABLE_EXECUTION=tru` (ou
// "enabled", ou "sim") TENCIONA ligar a execução durável; silenciosamente arrancar sem
// checkpointer/capturer/step-ledger dar-lhe-ia um nó que perde estado no reinício
// acreditando ele que não perde. A ambiguidade NEGA o arranque.
var ErrBadDurableExecution = errors.New("aos: AOS_DURABLE_EXECUTION invalida (aceites: 1/true/t/yes/y/on para ligar, 0/false/f/no/n/off ou vazio para desligar) — um valor nao reconhecido aborta em vez de degradar silenciosamente para execucao NAO-duravel")

// ErrPolicyBundleNeedsTrustAnchor — AOS_POLICY_BUNDLE_DIR está definido (o operador PEDE
// mediação de política real) mas AOS_POLICY_TRUST_ANCHOR está em falta. Fail-closed de CONFIG
// do bundle PDP (AOS-220): o trust anchor TEM de vir OUT-OF-BAND (do ambiente, provisionado por
// deploy/VCS read-only), NUNCA do próprio directório mutável do bundle. Carregar o bundle e ler
// o anchor do MESMO dir reabriria o vector de cold-start que [pdp.WithTrustAnchor] fecha — um
// adversário com escrita no dir substituiria o anchor E re-assinaria o bundle com a sua chave,
// contornando a verificação. A ambiguidade NEGA o arranque em vez de carregar política cuja
// âncora de confiança é a própria coisa a verificar.
var ErrPolicyBundleNeedsTrustAnchor = errors.New("aos: AOS_POLICY_BUNDLE_DIR definido exige AOS_POLICY_TRUST_ANCHOR (a pubkey ed25519 do trust anchor, 64 hex chars) — o anchor e FORCADO out-of-band pelo ambiente, nunca lido do proprio bundle (fecha o cold-start em que quem escreve no dir do bundle re-assina com a sua chave)")

// ErrBadPolicyTrustAnchor — AOS_POLICY_TRUST_ANCHOR presente mas não é uma pubkey ed25519 válida
// (32 bytes em hex, 64 chars). Fail-closed de CONFIG (AOS-220), gémeo de [ErrBadIssuerPubKey]:
// um anchor malformado NUNCA verifica bundle nenhum. Só material público bem-formado se torna
// âncora de confiança da política.
var ErrBadPolicyTrustAnchor = errors.New("aos: AOS_POLICY_TRUST_ANCHOR invalida (esperado 64 hex chars = 32 bytes ed25519 — a pubkey do trust anchor da politica; a chave PRIVADA assina o bundle FORA do no)")

// ErrPolicyBundleLoad — AOS_POLICY_BUNDLE_DIR e AOS_POLICY_TRUST_ANCHOR estão definidos mas o
// bundle NÃO carregou fail-closed: ausente, não-assinado, adulterado, ou com assinatura que a
// âncora out-of-band NÃO verifica (envolve [pdp.ErrPolicyUnavailable]/[pdp.ErrSignatureInvalid]).
// AOS-220: um bundle cuja assinatura a âncora forçada não valida é RECUSADO, não carregado — o nó
// não arranca a servir política que não conseguiu verificar contra o anchor de confiança.
var ErrPolicyBundleLoad = errors.New("aos: falha ao carregar o bundle PDP de AOS_POLICY_BUNDLE_DIR (ausente/nao-assinado/adulterado ou assinatura nao verificavel pelo trust anchor out-of-band) — RECUSADO fail-closed, nao carregado")

// --- VERIFICAÇÃO ANCORADA DO WORM (AOS-268/AOS-072) -----------------------------------------
// As três variáveis fecham, JUNTAS, os dois vectores que a re-verificação de hash-chain de
// AOS-221 não apanha (truncatura do tail; reescrita da génese via restart). O molde é o do PDP
// de AOS-220: material out-of-band (pubkey/checkpoint/piso) provisionado por deploy/VCS
// read-only, NUNCA lido do próprio WORM mutável que se está a verificar. Fail-closed em qualquer
// malformação e, sobretudo, na config PARCIAL — "algumas mas não todas" é ambiguidade de
// integridade e NEGA o arranque.

// ErrWormAnchorIncomplete — ALGUMAS mas NÃO TODAS as três envs da verificação ancorada do WORM
// (AOS_WORM_TRUST_ANCHOR + AOS_WORM_CHECKPOINT_FILE + AOS_WORM_EXPECTED_HEAD) estão definidas.
// Fail-closed de CONFIG (AOS-268), no padrão de [ErrPolicyBundleNeedsTrustAnchor]: as três só
// fecham o vector EM CONJUNTO (âncora assinada + trust-anchor que a valida + piso de frescura que
// prova que ela não é rollback). Uma config parcial — ex.: só o checkpoint sem o piso — daria um
// operador convencido de que ancorou o WORM quando não ancorou; a ambiguidade NEGA o arranque em
// vez de degradar em silêncio para a re-verificação de AOS-221 (que não fecha a truncatura do tail).
var ErrWormAnchorIncomplete = errors.New("aos: verificacao ancorada do WORM incompleta — AOS_WORM_TRUST_ANCHOR, AOS_WORM_CHECKPOINT_FILE e AOS_WORM_EXPECTED_HEAD sao AS TRES obrigatorias em conjunto (definir algumas-mas-nao-todas aborta em vez de degradar para a re-verificacao de AOS-221, que NAO fecha a truncatura do tail nem a reescrita da genese)")

// ErrBadWormTrustAnchor — AOS_WORM_TRUST_ANCHOR presente mas não é uma pubkey ed25519 válida
// (32 bytes em hex, 64 chars). Fail-closed (AOS-268), gémeo de [ErrBadPolicyTrustAnchor]: um
// trust-anchor malformado nunca valida checkpoint nenhum. Só material público bem-formado se
// torna raiz de confiança da frescura do WORM.
var ErrBadWormTrustAnchor = errors.New("aos: AOS_WORM_TRUST_ANCHOR invalida (esperado 64 hex chars = 32 bytes ed25519 — a pubkey do selador dos checkpoints do WORM; a chave PRIVADA sela FORA do no)")

// ErrBadWormCheckpoint — AOS_WORM_CHECKPOINT_FILE presente mas o ficheiro não existe, não é
// legível, ou não descodifica como JSON de [audit.Checkpoint]. Fail-closed (AOS-268): a âncora
// assinada tem de ser material bem-formado antes sequer de a assinatura ser verificada contra o
// trust-anchor (essa verificação acontece já dentro de [audit.VerifyFromCheckpointAtHead], no
// Bootstrap, contra o store composto).
var ErrBadWormCheckpoint = errors.New("aos: AOS_WORM_CHECKPOINT_FILE invalido (ficheiro inexistente/ilegivel ou JSON de audit.Checkpoint malformado) — a ancora assinada e lida de um ficheiro montado out-of-band, nunca do proprio WORM")

// ErrBadWormExpectedHead — AOS_WORM_EXPECTED_HEAD presente mas não é um uint64 decimal válido
// (>0). Fail-closed (AOS-268): o piso de frescura é o audit_seq que a cadeia TEM de ter atingido;
// um valor não-parseável NÃO degrada para "sem piso" (que reabriria o rollback de checkpoint).
var ErrBadWormExpectedHead = errors.New("aos: AOS_WORM_EXPECTED_HEAD invalida (esperado um audit_seq decimal > 0 — o piso de frescura persistido INDEPENDENTEMENTE do store; nao se deriva do checkpoint, senao o piso seria um no-op e o rollback passaria)")

// ErrBadOperators — AOS_OPERATORS presente mas com uma entrada MALFORMADA (sem '=', com
// emitterID vazio, com pubkey que não é ed25519 hex de 32 bytes, ou com emitterID DUPLICADO).
// Fail-closed de CONFIG do CANAL DE CONTROLO (AOS-193), no padrão exacto de
// [ErrBadBoardRegions]: distingue "não configurado" (vazio ⇒ default-deny deliberado, o canal
// continua inoperável e o bind não-loopback continua recusado) de "configurado mas inválido"
// (aborta). Um operador que TENCIONA registar a sua pubkey mas comete um typo NÃO obtém um nó
// que aceita o arranque e depois recusa TODOS os seus steer/pause com ErrUnknownEmitter — a
// ambiguidade NEGA o arranque. O DUPLICADO aborta em vez de "o último ganha": duas linhas com o
// mesmo emitterID e pubkeys diferentes são um conflito de autoridade, não uma preferência.
var ErrBadOperators = errors.New("aos: AOS_OPERATORS invalida (esperado \"emitterID=hexpubkey,emitterID2=hexpubkey2\" com pubkey ed25519 de 64 hex chars = 32 bytes — entrada malformada, emitterID duplicado ou a MESMA pubkey em dois emitterIDs abortam em vez de degradar silenciosamente para um canal de controlo inoperavel ou sem atribuicao)")

// ErrBadApproversFile — AOS_APPROVERS_FILE presente mas o ficheiro não é legível, não é um
// documento JSON válido no esquema esperado, ou tem uma entrada inválida (principal vazio,
// pubkey não-ed25519-hex, autoridade vazia ou fora do vocabulário, principal duplicado, DOIS
// principals com a MESMA pubkey, lista vazia). Fail-closed de CONFIG do 4-EYES (AOS-193), a
// contraparte de [ErrBadOperators] para o registo RICO dos aprovadores. Um ficheiro configurado
// mas inválido ABORTA: senão o nó arrancaria com [Node.FourEyes] == nil e POST
// /runs/{id}/approve continuaria a devolver 501 — exactamente a degradação silenciosa que o
// achado ORF-02 nomeia — ou, pior, com um gate COMPOSTO cujo dual-control uma única chave
// privada satisfaz (capacidade anunciada e não cumprida, na versão perigosa).
var ErrBadApproversFile = errors.New("aos: AOS_APPROVERS_FILE invalido (esperado JSON {\"approvers\":[{\"principal\":..., \"pubkey\":\"<64 hex>\", \"authority\":[\"approve:safe|gray|danger\"]}]} — ficheiro ilegivel, esquema invalido, principal duplicado, MESMA pubkey em dois principals, capability fora do vocabulario, autoridade vazia ou lista vazia abortam em vez de deixarem o four-eyes silenciosamente DESLIGADO ou ANULADO)")

// ErrBadChallengeIssuance — AOS_CHALLENGE_ISSUANCE presente com um valor que NÃO é um booleano
// reconhecido (AOS-266). Molde de [ErrBadDurableExecution]: lixo NÃO degrada silenciosamente
// para "frescura desligada" — um `AOS_CHALLENGE_ISSUANCE=tru` que deixasse o replay em aberto
// seria a pior degradação possível. Aborta.
var ErrBadChallengeIssuance = errors.New("aos: AOS_CHALLENGE_ISSUANCE invalida (aceites: 1/true/t/yes/y/on para ligar a frescura por-cerimonia, 0/false/f/no/n/off ou vazio para desligar) — um valor nao reconhecido aborta em vez de deixar o replay de attestation em aberto")

// ErrBadChallengeTTL — AOS_CHALLENGE_TTL presente mas não é uma duração Go válida e positiva
// (AOS-266). O TTL é o tempo de uma CERIMÓNIA de aprovação; um valor negativo/zero/lixo aborta em
// vez de silenciosamente cair no default (que poderia mascarar um erro de operador — ex. "5" sem
// unidade, ou "-5m").
var ErrBadChallengeTTL = errors.New("aos: AOS_CHALLENGE_TTL invalida (esperada duracao Go POSITIVA, ex. 5m, 90s — o tempo de UMA cerimonia de aprovacao, nao de uma sessao); vazio usa o default")

// ErrBadDeviceEnrollmentFile — o ficheiro de enrollment de dispositivos (AOS_DEVICE_ENROLLMENT_FILE,
// ou o AOS_APPROVERS_FILE por omissão) não é legível, não segue o esquema, tem um principal vazio,
// um device_id não-base64, ou (quando o ficheiro foi EXPLICITAMENTE apontado) não traz dispositivo
// nenhum. Fail-loud (molde AOS-193): descartar em silêncio daria um nó que arrancou a parecer
// atribuir dispositivos e nunca atribui.
var ErrBadDeviceEnrollmentFile = errors.New("aos: AOS_DEVICE_ENROLLMENT_FILE invalido (esperado JSON {\"approvers\":[{\"principal\":..., \"devices\":[\"<base64 do device_id opaco>\"]}]} — ficheiro ilegivel, esquema invalido, principal vazio, device_id nao-base64, ou ficheiro explicitamente apontado mas SEM dispositivos abortam em vez de deixarem a atribuicao silenciosamente DESLIGADA)")

// ErrBadRatifiers — AOS_RATIFIERS presente mas com uma entrada MALFORMADA (sem '=', com
// principal vazio, com pubkey que não é ed25519 hex de 32 bytes, com principal DUPLICADO, ou com
// a MESMA pubkey em dois principals). Fail-closed de CONFIG do PROMOTION CONTROLLER (AOS-206), no
// padrão exacto de [ErrBadOperators]: distingue "não configurado" (vazio ⇒ o controller é composto
// na mesma mas toda a promoção é negada com ratifier_unknown) de "configurado mas inválido"
// (aborta). Sem esta porta, [Config.Ratifiers] seria INALCANÇÁVEL pelo binário entregue (Config
// vive em `package main`) — o MESMO defeito de campo-fantasma (DEF-03/REG-01) que este ticket
// fecha. O DUPLICADO e a pubkey partilhada abortam em vez de "o último ganha": duas linhas com o
// mesmo principal (autoridade por acidente de ordenação) ou uma chave por dois nomes (não-repúdio
// da ratificação satisfeito por UMA chave privada) são conflitos, não preferências.
var ErrBadRatifiers = errors.New("aos: AOS_RATIFIERS invalida (esperado \"principal=hexpubkey,principal2=hexpubkey2\" com pubkey ed25519 de 64 hex chars = 32 bytes — entrada malformada, principal duplicado ou a MESMA pubkey em dois principals abortam em vez de degradar silenciosamente para um ratificador que nunca ratifica ou sem atribuicao de nao-repudio)")

// ErrProductionNeedsTLS — sob AOS_MODE=production o ingresso NÃO pode ser servido em
// texto-claro por engano (AOS-209). Exige-se OU a terminação TLS no nó
// (AOS_TLS_CERT_PATH+AOS_TLS_KEY_PATH) OU a DECLARAÇÃO explícita de terminação a montante
// (AOS_TLS_EXTERNAL_TERMINATION). A par de ErrProductionNeedsHardenedIdentity (identidade) e
// ErrProductionNeedsSovereignRead (soberania de leitura), é a terceira exigência de postura de
// produção: um nó de produção nunca serve API/SSE/DSAR sem transporte cifrado sem que alguém o
// tenha DECIDIDO — fail-closed.
var ErrProductionNeedsTLS = errors.New("aos: AOS_MODE=production exige terminacao TLS do ingresso — defina AOS_TLS_CERT_PATH+AOS_TLS_KEY_PATH (TLS no no) OU DECLARE terminacao a montante com AOS_TLS_EXTERNAL_TERMINATION=1; a producao nao serve API/SSE/DSAR em texto-claro sem decisao explicita")

// ErrProductionNeedsDurableKEK — sob AOS_MODE=production COM substrato durável (AOS_WORM_PATH
// e/ou AOS_DURABLE_EXECUTION), a custódia da KEK por-titular NÃO pode ser o vault in-memory de
// referência (AOS-215/AOS-216). É a SIMÉTRICA de ErrDurableExecutionNeedsDurableSubstrate: aquela
// exige que o SUBSTRATO seja durável; esta exige que a CHAVE que o decifra seja igualmente
// durável. Sem AOS_DSAR_VAULT_ADDR, o read-path soberano sela conteúdo sensível (D6) e a captura
// de não-determinismo sob uma KEK que evapora no restart — o conteúdo cifrado fica PERMANENTEMENTE
// indecifrável (over-erasure silenciosa) e o legal hold deixa de preservar o que a lei manda reter.
// Ao contrário das outras colunas de produção, a KEK-em-memória só AVISAVA; agora RECUSA. O modo
// de referência (sem AOS_MODE=production) mantém a KEK-em-memória demo-grade.
// ErrProductionNeedsDurableApproval — sob AOS_MODE=production com aprovadores four-eyes
// configurados (AOS_APPROVERS_FILE), a EXECUÇÃO DURÁVEL é obrigatória. O bridge
// negação→aprovação→reexecução (AOS-021) depende dela em dois pontos: reproduzir o turno
// escalado com fidelidade (o log durável NÃO guarda os inputs das tool calls — só a
// captura de replay os tem) e impedir a dupla execução das activities já aplicadas do
// mesmo turno (step-ledger). Decisão do dono: exigir, não degradar.
var ErrProductionNeedsDurableApproval = errors.New("aos: AOS_MODE=production com aprovadores four-eyes (AOS_APPROVERS_FILE) exige EXECUCAO DURAVEL — defina AOS_DURABLE_EXECUTION=1 (+AOS_EVENTSTORE_PATH). Sem ela o bridge de aprovacao nao funciona: o turno escalado nao pode ser reproduzido com fidelidade (o log duravel nao guarda os inputs das tool calls) e nada impede a dupla execucao das activities ja aplicadas do mesmo turno. Um four-eyes que verifica assinaturas e nao destrava nada e pior do que desligado — cria a expectativa de aprovacao humana onde so ha negacoes")

// ErrProductionNeedsModelCredential — sob AOS_MODE=production COM o model gateway LIGADO
// (AOS_MODEL_ENDPOINT), a credencial que o nó apresenta ao upstream NÃO pode ser o bearer de DEV
// embebido no binário (AOS-247, achado F5). Sem AOS_MODEL_API_KEY_PATH, [staticModelCredential]
// serve uma constante do código — a MESMA em todos os nós e legível por quem tenha o artefacto —
// e o nó de produção falava com o gateway upstream com uma credencial que não identifica ninguém
// e não se pode revogar. Pertence à mesma família de ErrProductionNeedsHardenedIdentity/
// ErrProductionNeedsTLS: a produção não degrada silenciosamente para uma postura de referência.
//
// PORQUÊ NO ARRANQUE E NÃO NO Fetch. O `Fetch` corre POR-PEDIDO e não tem por onde abortar o
// processo — devolver erro ali daria um nó que arranca, anuncia gateway e falha cada chamada em
// runtime. A decisão é de CONFIG, logo vive na fronteira que lê o ambiente ([parseModelFromEnv]).
var ErrProductionNeedsModelCredential = errors.New("aos: AOS_MODE=production com o model gateway ligado (AOS_MODEL_ENDPOINT) exige AOS_MODEL_API_KEY_PATH — sem ele o no apresentaria ao upstream um bearer de DEV embebido no binario (identico em todos os nos, legivel por quem tenha o artefacto, nao revogavel) em vez da credencial da organizacao; material privado por FICHEIRO montado, NUNCA por variavel de ambiente")

var ErrProductionNeedsDurableKEK = errors.New("aos: AOS_MODE=production com substrato duravel (AOS_WORM_PATH e/ou AOS_DURABLE_EXECUTION) exige custodia de KEK DURAVEL — defina AOS_DSAR_VAULT_ADDR (+AOS_DSAR_VAULT_TOKEN_PATH). Sem ela a KEK por-titular vive no vault in-memory de referencia e um restart torna o conteudo selado (D6/captura) PERMANENTEMENTE indecifravel (over-erasure silenciosa; o legal hold deixa de preservar). Simetrica a ErrDurableExecutionNeedsDurableSubstrate: a chave tem de ser tao duravel quanto o substrato que cifra")

// ErrBadTLSExternalTermination — AOS_TLS_EXTERNAL_TERMINATION presente com um valor que não é
// um booleano reconhecido. Fail-closed de CONFIG (AOS-209), no padrão de ErrBadDurableExecution:
// lixo NÃO é tratado como false. Um operador que escreve "AOS_TLS_EXTERNAL_TERMINATION=sim"
// (ou "yep", ou "externo") TENCIONA declarar a terminação a montante; silenciosamente tratá-la
// como não-declarada faria o bind não-loopback ser RECUSADO por texto-claro, com o operador a
// acreditar que o tinha permitido. A ambiguidade NEGA o arranque.
var ErrBadTLSExternalTermination = errors.New("aos: AOS_TLS_EXTERNAL_TERMINATION invalida (aceites: 1/true/t/yes/y/on para declarar terminacao a montante, 0/false/f/no/n/off ou vazio para nao declarar) — um valor nao reconhecido aborta em vez de degradar silenciosamente para nao-declarado")

// main arranca o nó `aos` de PRODUÇÃO a partir de config de ambiente e declara o modo
// de identidade em vigor. O loop de serviço long-running (hospedar N runs, shutdown
// gracioso) é AOS-164; aqui prova-se o BOOTSTRAP fail-closed e a declaração do modo
// REAL. Sem segredos em código: o IssuerID e os humanos vêm do ambiente; a chave de
// assinatura de referência é gerada por CSPRNG (a real vem de vault — AOS-170/EPIC-06).
func main() {
	// A CLI (AOS-165) despacha os subcomandos: `serve` (ou sem args) arranca o nó via [run];
	// `run`/`observe`/`steer`/`pause` são clientes HTTP da API (AOS-166).
	if err := dispatch(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "aos: %v\n", err)
		os.Exit(1)
	}
}

// run compõe e valida o nó, escrevendo o banner em w. Extraída de main para ser
// exercitável sem processo. NÃO entra no loop de serviço (AOS-164): o objectivo de
// AOS-163 é o arranque de produção fail-closed com identidade real declarada.
func run(w io.Writer) error {
	ctx := context.Background()

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		return err
	}

	node, err := Bootstrap(ctx, cfg, w)
	if err != nil {
		return err
	}
	defer func() { _ = node.Close() }()

	// KILL-SWITCH VISÍVEL da soberania de leitura (AOS-203, achado ORF-05). Em produção o
	// estado desligado nem chega aqui (ErrProductionNeedsSovereignRead abortou em
	// [nodeConfigFromEnv], e esse fail-closed NÃO é alterado); FORA de produção o
	// definido-vazio é aceite — mas deixa de ser SILENCIOSO. O aviso sai do lado do
	// entrypoint (a fronteira que LÊ a variável), a seguir ao banner do composition-root,
	// e nomeia o que fica desligado e como se religa. Ver [sovereignReadKillSwitchBanner].
	//
	// O predicado é o GATE REAL do read-path soberano, não uma aproximação: [NewAPIServer]
	// só compõe a read-governance quando o registo board→região E o WORM estão ambos
	// compostos (`node.SovereignReadRegions != nil && node.WORM != nil` — ver newReadGovernance
	// em api.go). Espelhá-lo aqui, em vez de olhar só para o registo, garante que o aviso
	// nunca pode ficar CALADO enquanto o nó serve o read-path legado: se o WORM alguma vez
	// se tornar opcional (hoje o Bootstrap cai sempre para audit.NewMemStore), o nó avisa em
	// vez de anunciar uma soberania que não está a aplicar — que é precisamente a promessa
	// falsa que AOS-203 vem eliminar.
	rawBoardRegions, boardRegionsDefined := os.LookupEnv("AOS_BOARD_REGIONS")
	for _, line := range sovereignReadKillSwitchBanner(node.SovereignReadRegions != nil, node.WORM != nil, rawBoardRegions, boardRegionsDefined) {
		fmt.Fprintf(w, "[aos] %s\n", line)
	}

	// FALLBACK DE CREDENCIAL DO MODELO VISÍVEL (AOS-247, achado F5), pela mesma via: em produção
	// este estado nem chega aqui (ErrProductionNeedsModelCredential abortou em
	// [nodeConfigFromEnv]); fora de produção é aceite mas deixa de ser SILENCIOSO. O predicado é o
	// estado composto — cfg.Model != nil significa que [parseModelFromEnv] construiu MESMO o
	// gateway — para o aviso nunca sair quando o nó usa o referenceModel (que não apresenta bearer
	// nenhum). Ver [devModelCredentialBanner].
	for _, line := range devModelCredentialBanner(cfg.Model != nil, os.Getenv("AOS_MODEL_API_KEY_PATH")) {
		fmt.Fprintf(w, "[aos] %s\n", line)
	}

	// AUDIT DE GOVERNAÇÃO DO GATEWAY (AOS-265): declara se a activação da allowlist e as
	// decisões por chamada selam num WORM DURÁVEL (AOS_MODEL_AUDIT_PATH) ou num MemStore
	// volátil. Amarrado ao estado composto (cfg.Model != nil): sem gateway não há linha.
	for _, line := range modelAuditPostureBanner(cfg.Model != nil, strings.TrimSpace(os.Getenv("AOS_MODEL_AUDIT_PATH"))) {
		fmt.Fprintf(w, "[aos] %s\n", line)
	}

	// Superfície de REDE (AOS-166). Se AOS_API_ADDR estiver definido, o nó gradua para um nó
	// OPERÁVEL de fora: levanta o loop de serviço + a API HTTP com o BIND-GUARDRAIL no
	// CAMINHO DE PRODUÇÃO (é aqui, não só nos testes, que um bind não-loopback sem
	// autenticação do canal de controlo é RECUSADO). Sem AOS_API_ADDR mantém-se o arranque
	// de bootstrap/declaração (AOS-163) sem abrir socket.
	apiAddr := strings.TrimSpace(os.Getenv("AOS_API_ADDR"))
	if apiAddr == "" {
		fmt.Fprintf(w, "[aos] bootstrap concluido — no pronto (modo de identidade: %s). API HTTP nao levantada (AOS_API_ADDR vazio).\n", node.IdentityMode)
		return nil
	}

	// SIGINT/SIGTERM ⇒ paragem graciosa (drena runs em curso, liberta leases).
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(w, "[aos] bootstrap concluido — a levantar a API HTTP em %q (modo de identidade: %s)\n", apiAddr, node.IdentityMode)
	return serveAPI(sigCtx, w, node, apiAddr)
}

// nodeConfigFromEnv constrói a [Config] do nó a partir do AMBIENTE — a ÚNICA superfície de
// configuração do binário entregue. É a costura testável entre o processo e o
// composition-root: um campo de Config que NÃO seja escrito aqui é, na prática,
// inalcançável pelo artefacto (Config vive em `package main`, logo nem um embedder
// externo o pode preencher) — foi exactamente esse o defeito de AOS_DURABLE_EXECUTION
// (AOS-191, achado REG-01 da auditoria v4).
//
// Toda a validação FAIL-CLOSED de config vive aqui: um valor presente mas inválido ABORTA
// (ErrBadIssuerPubKey, ErrBadBoardRegions, ErrBadDurableExecution) e as exigências de
// postura de produção são impostas (ErrProductionNeedsHardenedIdentity,
// ErrProductionNeedsSovereignRead). Nada degrada em silêncio.
func nodeConfigFromEnv() (Config, error) {
	issuerID := envOr("AOS_ISSUER_ID", "iss:aos-node")
	humans := splitCSV(envOr("AOS_HUMANS", "operator"))
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("AOS_MODE")), "production")

	// SOBERANIA DE LEITURA (AOS-172, D7) — config fail-closed, sem degradação silenciosa.
	// Distingue TRÊS estados (LookupEnv, não envOr — envOr colapsaria "não definido" com
	// "definido vazio"):
	//   - NÃO DEFINIDO ⇒ default DEMO ("board:aos-demo=eu"): a soberania de leitura está LIGADA
	//     no caminho comum (o `aos serve` de referência é fail-closed por omissão);
	//   - DEFINIDO VAZIO ⇒ nil: opt-out DELIBERADO do read-path legado (aceite só fora de produção);
	//   - DEFINIDO MALFORMADO (entrada não-vazia inválida) ⇒ ABORTA (ErrBadBoardRegions): um typo
	//     (ex.: "aos-demo" sem '=') NÃO degrada em silêncio para o read-path aberto.
	//
	// O segundo estado é um KILL-SWITCH de um controlo de conformidade. Em produção é RECUSADO
	// (ErrProductionNeedsSovereignRead, logo abaixo); fora de produção é aceite mas deixou de
	// ser silencioso — [run] escreve o aviso de [sovereignReadKillSwitchBanner] (AOS-203).
	rawBoardRegions, ok := os.LookupEnv("AOS_BOARD_REGIONS")
	if !ok {
		rawBoardRegions = "board:aos-demo=eu"
	}
	boardRegions, err := parseBoardRegions(rawBoardRegions)
	if err != nil {
		return Config{}, err
	}
	// FAIL-CLOSED de produção: a soberania de leitura NÃO pode estar desligada num nó de
	// produção (a par da identidade endurecida, ErrProductionNeedsHardenedIdentity). Sem
	// registo board→região não-vazio, a produção serviria o read-path LEGADO sem authz
	// por-chamador (D7) nem selo (D6) — recusa o arranque.
	if production && len(boardRegions) == 0 {
		return Config{}, ErrProductionNeedsSovereignRead
	}

	// CREDENCIAL FORTE da soberania de leitura (AOS-205) — o verificador OIDC do leitor de
	// governação / operador DSAR. Config fail-closed: ambos vazios ⇒ nil (via legada por headers,
	// só fora de produção); um só ⇒ ErrBadSovereignOIDC (aborta). Quando presente, o read-path
	// EXIGE um ID-token verificado e deriva o board das CLAIMS, não do header X-Aos-Board.
	sovereignOIDC, err := parseSovereignReadOIDC()
	if err != nil {
		return Config{}, err
	}
	// A exigência de produção (credencial forte) é imposta ABAIXO, a par de
	// ErrProductionNeedsHardenedIdentity — depois de o trust anchor ser parseado, para que a
	// falta da identidade endurecida (a fronteira mais fundamental) seja diagnosticada primeiro.

	// EXECUÇÃO DURÁVEL (AOS-180) por ambiente — AOS_DURABLE_EXECUTION (AOS-191). OPT-IN:
	// ausente/vazio ⇒ false, isto é, o comportamento ACTUAL byte-a-byte (checkpointer,
	// capturer e step-ledger ficam nil e o runtime usa os defaults no-op de AOS-013).
	// Presente e verdadeiro ⇒ o nó compõe os três sobre o MESMO Event Store dos turnos e
	// sinais de controlo, e o snapshot do tool set congelado passa a persistir nele.
	//
	// Ao contrário de AOS_BOARD_REGIONS, NÃO é preciso distinguir "não definido" de
	// "definido vazio": ali o vazio era um KILL-SWITCH (desligava uma protecção LIGADA por
	// omissão); aqui o default JÁ é "desligado", pelo que vazio colapsa no default sem
	// perder informação e sem baixar a postura de segurança.
	//
	// Um valor NÃO RECONHECIDO aborta (ErrBadDurableExecution) em vez de ser tratado como
	// false — ver a justificação no erro.
	//
	// POSTURA DE PRODUÇÃO — DECIDIDA EM AOS-203: MANTÉM-SE OPT-IN, TAMBÉM EM
	// AOS_MODE=production. Ao contrário da identidade endurecida
	// (ErrProductionNeedsHardenedIdentity) e da soberania de leitura
	// (ErrProductionNeedsSovereignRead), AOS_MODE=production NÃO exige execução durável e NÃO
	// passará a exigir: um nó de produção arranca sem ela. Isto não é uma pendência — é a
	// decisão, e o eixo que AOS-191 abriu fica FECHADO aqui.
	//
	// CRITÉRIO que separa este caso dos outros dois: a PROMESSA FALSA. Ali o nó SERVIRIA
	// identidade/leituras com uma postura mais fraca do que a que um nó de produção
	// implicitamente anuncia; aqui não anuncia durabilidade nenhuma — o banner declara
	// "execucao duravel (AOS-180): DESLIGADA" em CADA arranque e nenhum endpoint promete
	// sobrevivência de checkpoints. Capacidade declaradamente ausente, não capacidade
	// anunciada e não cumprida. Argumentos subordinados: (i) exigi-la quebraria a
	// retro-compatibilidade que AOS-191 impõe e converteria um ticket de SUPERFÍCIE DE
	// CONFIGURAÇÃO numa mudança de postura de produção não anunciada aos operadores
	// existentes; (ii) o eixo REALMENTE perigoso — durabilidade LIGADA sobre substrato
	// volátil — já é fail-closed em QUALQUER modo, logo abaixo.
	//
	// A tabela comparativa das três posturas e a consequência para o operador (quem quiser
	// retoma de runs interrompidos TEM de a ligar explicitamente, mesmo em produção) estão em
	// deploy/node/README.md, secção "Postura de produção de AOS_DURABLE_EXECUTION — decisão
	// (AOS-203)". Reabrir isto exige emenda registada, não um PR silencioso.
	durableExecution, err := parseDurableExecution(os.Getenv("AOS_DURABLE_EXECUTION"))
	if err != nil {
		return Config{}, err
	}
	// INTERACÇÃO COM AOS_EVENTSTORE_PATH — fail-closed SEMPRE (não só em produção). A
	// execução durável compõe-se SOBRE o Event Store: sem AOS_EVENTSTORE_PATH o nó abriria
	// um store IN-MEMORY e checkpoints/capturas/step-ledger evaporariam no reinício — o nó
	// anunciaria durabilidade e perderia tudo. A guarda canónica vive no composition-root
	// ([ErrDurableExecutionNeedsDurableSubstrate], Bootstrap (1b)) e cobre também um
	// EventStore injectado por config; aqui, na fronteira de ambiente, a condição é
	// simplesmente "AOS_DURABLE_EXECUTION=1 exige AOS_EVENTSTORE_PATH".
	eventStorePath := strings.TrimSpace(os.Getenv("AOS_EVENTSTORE_PATH"))
	if durableExecution && eventStorePath == "" {
		return Config{}, fmt.Errorf("%w (defina AOS_EVENTSTORE_PATH, ex.: /var/lib/aos/events.wal)", ErrDurableExecutionNeedsDurableSubstrate)
	}

	// CANAL DE CONTROLO AUTENTICADO (AOS-160) — PUBKEYS DOS OPERADORES por ambiente
	// (AOS_OPERATORS, AOS-193). É o caminho que faltava: sem ele [Config.Operators] era
	// INALCANÇÁVEL pelo binário entregue (Config vive em `package main`), o
	// Ed25519Authenticator ficava com ZERO emissores e devolvia ErrUnknownEmitter a TODO o
	// steer/pause — dois subcomandos da CLI e dois endpoints de controlo inatingíveis sem
	// forkar e recompilar (achado ORF-02).
	//
	// SÓ MATERIAL PÚBLICO. O valor é "emitterID=hexpubkey": 64 hex chars = 32 bytes de
	// pubkey ed25519, a MESMA codificação de AOS_ISSUER_PUBKEY. A chave PRIVADA do operador
	// vive na máquina do operador (é lá que `aos steer` assina, cli.go) e NUNCA entra aqui.
	//
	// LIMITE HONESTO desta validação: uma SEED ed25519 tem também 32 bytes, pelo que o nó
	// NÃO a consegue distinguir estruturalmente de uma pubkey. Um operador que colar a sua
	// seed obtém um registo que nunca verifica assinatura nenhuma (fail-closed, sem
	// elevação de privilégio) — mas terá exposto a sua chave privada ao ambiente do nó por
	// erro seu. Está documentado em deploy/node/README.md em vez de fingido em código.
	operators, err := parseOperators(os.Getenv("AOS_OPERATORS"))
	if err != nil {
		return Config{}, err
	}

	// FOUR-EYES / DUAL-CONTROL (AOS-162) — APROVADORES por FICHEIRO MONTADO
	// (AOS_APPROVERS_FILE, AOS-193). Ver [parseApproversFile] para a JUSTIFICAÇÃO de ser um
	// ficheiro e não uma env plana (o registo é rico: principal + pubkey + autoridade []).
	approvers, err := parseApproversFile(strings.TrimSpace(os.Getenv("AOS_APPROVERS_FILE")))
	if err != nil {
		return Config{}, err
	}

	// PROMOTION CONTROLLER / RATIFICAÇÃO DE PRODUÇÃO (AOS-159/AOS-206) — RATIFICADORES por
	// ambiente (AOS_RATIFIERS). É o caminho que faltava (achado DEF-03): o promotion controller
	// é SEMPRE composto pela via sancionada, mas sem esta porta [Config.Ratifiers] era
	// INALCANÇÁVEL pelo binário e o gate nunca teria um ratificador autorizado — toda a promoção
	// negada com ratifier_unknown, sem forma de a religar sem forkar e recompilar. A gramática é
	// a de [parseOperators] (principal=hexpubkey): a autoridade é FIXA ("ratify:production"),
	// pelo que — ao contrário dos aprovadores — não há []Authority por entrada e uma env plana
	// chega. SÓ MATERIAL PÚBLICO: a chave PRIVADA do ratificador assina FORA do nó.
	ratifiers, err := parseRatifiers(os.Getenv("AOS_RATIFIERS"))
	if err != nil {
		return Config{}, err
	}

	// DIRECTÓRIO DE AUTORIDADE EXTERNO (AOS-071) — a SEGUNDA opinião independente do
	// ScopeGate e a única via de REVOGAÇÃO. Sem ele, o gate acaba a re-verificar o que o
	// hook de identidade já impôs e um token válido continua válido até expirar, aconteça
	// o que acontecer à organização. [Config.Authority] existia e era inalcançável pelo
	// binário — o mesmo defeito de campo-fantasma de AOS_RATIFIERS.
	authorityDir, err := parseAuthorityFile(os.Getenv("AOS_AUTHORITY_FILE"))
	if err != nil {
		return Config{}, err
	}

	// Trust-anchor-only ENDURECIDO: se AOS_ISSUER_PUBKEY estiver presente, o nó recebe só
	// a pubkey do issuer (a autoridade/chave vivem FORA do processo). É o modo de fronteira
	// de produção. Fail-closed: uma pubkey malformada aborta.
	var issuerPub ed25519.PublicKey
	if raw := strings.TrimSpace(os.Getenv("AOS_ISSUER_PUBKEY")); raw != "" {
		pub, err := parseEd25519PubHex(raw)
		if err != nil {
			return Config{}, err
		}
		issuerPub = pub
	}

	// FAIL-CLOSED de produção: AOS_MODE=production recusa o modo de referência (autoridade
	// co-localizada). Um operador não pode confundir o arranque de referência com uma
	// fronteira de produção endurecida — exige-se o trust-anchor-only.
	if production && issuerPub == nil {
		return Config{}, ErrProductionNeedsHardenedIdentity
	}

	// FAIL-CLOSED de produção (AOS-205): a FONTE DE AUTORIDADE da soberania de leitura tem de ter
	// CREDENCIAL FORTE verificada — a produção nunca deriva o board de um header X-Aos-Board
	// auto-declarado. O mapa self-hosted de AOS_BOARD_REGIONS é semente, não autoridade da
	// organização. Sem o verificador OIDC configurado (AOS_SOVEREIGN_OIDC_ISSUER+AUDIENCE), recusa.
	if production && sovereignOIDC == nil {
		return Config{}, ErrProductionNeedsSovereignAuthority
	}

	// Config MÍNIMA. Numa implantação real, IssuerSigningKey e as pubkeys dos operadores/
	// aprovadores vêm de config/vault (nunca do código). No modo de referência (sem
	// AOS_ISSUER_PUBKEY) a chave é gerada por CSPRNG e a autoridade co-localizada
	// encapsula-a — nunca é impressa nem entra na cadeia de segurança; o banner AVISA que
	// é referência. No modo endurecido (AOS_ISSUER_PUBKEY presente) NENHUMA chave de
	// assinatura entra no processo.
	// DIRECTÓRIO HUMANO OIDC (frente 1 do D4, AOS-174; fecha DEF-110): AOS_HUMAN_OIDC_* compõe o
	// OIDCDirectory que autentica o humano contra um IdP REAL antes do mint (modo de referência);
	// nil ⇒ allowlist de nomes de `Humans` (retro-compat). Fail-closed em config incompleta.
	humanDir, err := parseHumanOIDCDirectory()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		IssuerID:       issuerID,
		Humans:         humans,
		HumanDirectory: humanDir,
		IssuerPubKey:   issuerPub, // nil ⇒ referência; presente ⇒ trust-anchor-only endurecido
		IssuerClasses: map[string]identity.ClassPolicy{
			"researcher": {TTL: 15 * time.Minute, Scope: []string{"cap:doc.read"}},
		},
		// SOBERANIA DE LEITURA (AOS-172, D7). Registo board→região DEMO-GRADE self-hosted por
		// ambiente (AOS_BOARD_REGIONS = "board=regiao,board2=regiao2"), com um board demo por
		// omissão. Já validado fail-closed acima (vazio ⇒ legado; malformado ⇒ abortou). A REGRA
		// fail-closed é FIXA; o provisioning real de regiões/boards fica DEFERIDO (AOS-205).
		// Ligar isto torna o read-path soberano fail-closed E o selo WORM de leitura sensível (D6)
		// — os clientes de leitura têm de declarar X-Aos-Reader/X-Aos-Board.
		BoardRegions: boardRegions,
		// CREDENCIAL FORTE da soberania de leitura (AOS-205): o verificador OIDC do leitor de
		// governação / operador DSAR, configurado por AOS_SOVEREIGN_OIDC_ISSUER/AUDIENCE (já
		// validado fail-closed acima). Presente ⇒ o read-path exige um ID-token verificado e
		// deriva o board das CLAIMS (o header X-Aos-Board deixa de autorizar). O tenant concreto
		// (issuer real) é config; o provisionamento do IdP de soberania fica DEFERIDO.
		SovereignReadOIDC: sovereignOIDC,
		// SUBSTRATO DURÁVEL (AOS-170) por ambiente. OPT-IN: vazio ⇒ in-memory de referência
		// (inalterado; a imagem roda limpa sob `--read-only`). Presente ⇒ o nó ABRE o Event Store
		// (WAL append-only + fsync + replay crash-safe) e o WORM (hash-chain tamper-evident) nos
		// caminhos dados — tipicamente um mount GRAVÁVEL fora do root-fs read-only (deploy/node:
		// -v aos-data:/var/lib/aos). É esta ligação que torna real o estado durável que o
		// Dockerfile documenta e que a durabilidade de kill+reinício-sem-duplicação exige.
		EventStorePath: eventStorePath,
		WORMPath:       strings.TrimSpace(os.Getenv("AOS_WORM_PATH")),
		// EXECUÇÃO DURÁVEL (AOS-180) por ambiente (AOS_DURABLE_EXECUTION — AOS-191): liga o
		// checkpointer, o capturer de não-determinismo e o step-ledger sobre o Event Store
		// durável já validado acima. Sem a variável fica false ⇒ os três permanecem nil
		// (comportamento actual, inalterado).
		DurableExecution: durableExecution,
		// Observabilidade OTLP (AOS-173): vazio ⇒ NoopTracer (default, zero overhead);
		// presente ⇒ o nó exporta traces (invoke_agent/chat[+custo]/execute_tool/freeze +
		// selos WORM) via OTLP/HTTP. Um endpoint malformado aborta o arranque (fail-closed).
		OTLPEndpoint: strings.TrimSpace(os.Getenv("AOS_OTLP_ENDPOINT")),
		// AUTENTICAÇÃO FORTE da perna OTLP (DEF-012, EIXO 2) — OPT-IN, por FICHEIRO montado. mTLS
		// de cliente (par cert+chave) e/ou bearer perante o colector. Vazios ⇒ sem autenticação de
		// cliente (comportamento actual). Só se aplicam quando o nó ABRE o exporter (endpoint
		// definido); fail-closed de CONFIG (par incompleto/inválido, bearer ilegível) dentro de
		// NewOTLPHTTPExporter. Material privado (chave, bearer) NUNCA por variável — só por ficheiro.
		OTLPClientCertPath:  strings.TrimSpace(os.Getenv("AOS_OTLP_CLIENT_CERT_PATH")),
		OTLPClientKeyPath:   strings.TrimSpace(os.Getenv("AOS_OTLP_CLIENT_KEY_PATH")),
		OTLPBearerTokenPath: strings.TrimSpace(os.Getenv("AOS_OTLP_BEARER_TOKEN_PATH")),
		// CANAL DE CONTROLO (AOS-160/AOS-193): pubkeys dos operadores lidas de AOS_OPERATORS
		// (já validadas fail-closed acima). Vazio ⇒ default-deny do canal de controlo (o steer
		// anónimo é recusado — a inércia do D4 não protege pause/steer) E, desde AOS-193, o
		// bind-guardrail RECUSA um bind não-loopback (um canal inoperável não é um canal
		// autenticado).
		Operators: operators,
		// FOUR-EYES (AOS-162/AOS-193): aprovadores lidos do ficheiro montado AOS_APPROVERS_FILE
		// (já validados fail-closed acima). Vazio ⇒ o gate NÃO é composto e POST
		// /runs/{id}/approve devolve 501 (endpoint declaradamente desligado, não uma falha).
		Approvers: approvers,
		// PROMOTION CONTROLLER (AOS-159/AOS-206): ratificadores lidos de AOS_RATIFIERS (já
		// validados fail-closed acima). O controller é composto SEMPRE (via sancionada,
		// freshness+nonce durável forçados); vazio ⇒ composto mas toda a promoção negada
		// (ratifier_unknown), declarado no banner. A janela de frescura usa o default do nó.
		Ratifiers: ratifiers,
	}

	// DIRECTÓRIO DE AUTORIDADE (AOS-071): nil ⇒ campo não preenchido e o ScopeGate opera
	// só sobre a autoridade do token (comportamento anterior, byte-idêntico). Presente ⇒
	// segunda opinião independente e revogação, declaradas no banner.
	if authorityDir != nil {
		cfg.Authority = authorityDir
	}

	// DURABILIDADE DA IDENTIDADE (AOS-170) por ambiente, SÓ no modo de REFERÊNCIA. Um
	// AOS_ISSUER_KEY_PATH faz a autoridade co-localizada carregar/persistir a chave de assinatura
	// de um ficheiro de seed em vez de a gerar por CSPRNG a cada arranque — os tokens emitidos
	// antes do reinício continuam válidos. PROIBIDO no modo endurecido (nenhuma chave de assinatura
	// entra no processo): só se liga quando issuerPub == nil, senão o Bootstrap aborta fail-closed
	// com ErrConflictingIssuerKey.
	if issuerPub == nil {
		cfg.IssuerKeyPath = strings.TrimSpace(os.Getenv("AOS_ISSUER_KEY_PATH"))
	}

	// SUPERFÍCIE DE CARREGAMENTO DO BUNDLE PDP (AOS-220, achado #5 / DEF-604). Preenche
	// [Config.PDP] a partir do ambiente — sem isto o campo era INALCANÇÁVEL pelo binário e o nó
	// caía sempre em [pdp.NewUnloaded] (default-deny de TODA a tool call mediada). AOS_POLICY_BUNDLE_DIR
	// → pdp.Open(dir, WithTrustAnchor(anchor de AOS_POLICY_TRUST_ANCHOR)); ausente ⇒ nil (default-deny
	// EXPLÍCITO). Fail-closed: dir sem anchor / anchor malformado / bundle não-verificável ABORTAM.
	policyDP, autonomyCabling, err := loadPolicyBundleFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg.PDP = policyDP // nil ⇒ NewUnloaded (default-deny EXPLÍCITO) no composition-root; != nil ⇒ precedência
	// ORÁCULO DE AUTONOMIA (AOS-087/AOS-248), fase 1 de 2. O registo já vai com o sink de audit
	// ligado, mas VAZIO: os níveis só são aplicados — e SELADOS — em [Bootstrap], que é quem tem
	// o WORM. Ver [autonomyWiring]. nil ⇒ oráculo não ligado e nenhum `escalate` é emitido.
	cfg.Autonomy = autonomyCabling

	// SUPERFÍCIE DE CARREGAMENTO DA VERIFICAÇÃO ANCORADA DO WORM (AOS-268/AOS-072). Preenche
	// [Config.WORMAnchor] a partir do ambiente — sem isto o campo era INALCANÇÁVEL pelo binário e o
	// nó ficava só com a re-verificação de hash-chain de AOS-221 (que não fecha a truncatura do
	// tail nem a reescrita da génese). AS TRÊS envs presentes ⇒ o material é parseado e o Bootstrap
	// verifica-o contra o store composto; ausentes ⇒ nil (não-ancorado, declarado no banner);
	// parciais/malformadas ⇒ ABORTAM fail-closed.
	wormAnchor, err := parseWormAnchorFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg.WORMAnchor = wormAnchor

	// POLÍTICA DE RETENÇÃO TTL (AOS-092/AOS-213) por ambiente. Preenche [Config.Retention] a partir
	// de AOS_RETENTION_VERSION + AOS_RETENTION_PERIODS — sem isto o campo era INALCANÇÁVEL pelo
	// binário e o /dsar/expire varria mas NADA expirava. Ambas vazias ⇒ zero-value (nada expira,
	// inalterado); config inválida ⇒ ABORTA (ErrBadRetention), fail-closed.
	retention, err := parseRetentionFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg.Retention = retention

	// CUSTÓDIA EXTERNA DA KEK em HashiCorp Vault (AOS-215/AOS-216) por ambiente. Vazio ⇒
	// [Config.DSARVault] fica nil e o Bootstrap usa o vault in-memory demo-grade (inalterado);
	// AOS_DSAR_VAULT_ADDR presente ⇒ liga a custódia key-never-leaves via Transit. Fail-closed:
	// config incompleta ABORTA (ErrBadVaultDSAR) em vez de degradar para o in-memory.
	dsarVault, err := parseVaultDSARFromEnv()
	if err != nil {
		return Config{}, err
	}
	if dsarVault != nil {
		cfg.DSARVault = dsarVault
	}

	// CUSTÓDIA DAS CREDENCIAIS DOWNSTREAM do credential broker (AOS-070/AOS-264) por
	// ambiente — SEPARADA da custódia da KEK (D7: cliente/token AOS_BROKER_VAULT_*
	// próprios). Vazio ⇒ dormente (inalterado); presente ⇒ PREPARA o cliente Vault
	// REAL (KV v2) e valida fail-closed (ErrBadBrokerVault). Fica em [Config.BrokerVault]
	// para AOS-265 CONSUMIR (a troca mediada in-process); AOS-264 só o prepara e
	// declara o modo no banner — a troca NÃO está ligada nesta entrega.
	brokerVault, brokerVaultSet, err := parseBrokerVaultFromEnv()
	if err != nil {
		return Config{}, err
	}
	if brokerVault != nil {
		cfg.BrokerVault = brokerVault
		cfg.BrokerVaultAddr = brokerVaultSet.Addr
		cfg.BrokerVaultKVMount = brokerVaultSet.KVMount
	}

	// FAIL-CLOSED de produção (AOS-215/AOS-216) — a KEK tem de ser tão durável quanto o substrato
	// que cifra. Se há substrato durável (WORM durável e/ou execução durável) mas a KEK ficaria no
	// vault in-memory de referência, um restart perde-a e o conteúdo selado (D6/captura) fica
	// PERMANENTEMENTE indecifrável. Simétrica a ErrDurableExecutionNeedsDurableSubstrate. Fora de
	// produção a KEK-em-memória demo-grade é aceite (inalterado).
	if production && cfg.DSARVault == nil && (durableExecution || cfg.WORMPath != "") {
		return Config{}, ErrProductionNeedsDurableKEK
	}

	// FAIL-CLOSED de produção (AOS-021) — o four-eyes só é uma via de aprovação REAL com
	// execução durável ligada. Sem ela, o bridge negação→aprovação→reexecução não funciona:
	// o log durável não guarda os inputs das tool calls, pelo que o turno escalado não pode
	// ser reproduzido com fidelidade (a acção aprovada nunca voltaria a ser apresentada de
	// forma idêntica), e sem o step-ledger nada impede que as activities JÁ EXECUTADAS do
	// mesmo turno voltem a correr na retoma. Ficaria um four-eyes que verifica assinaturas e
	// não destrava nada — pior do que desligado, porque cria a expectativa de que há
	// aprovação humana quando na prática só há negações.
	if production && len(cfg.Approvers) > 0 && !durableExecution {
		return Config{}, ErrProductionNeedsDurableApproval
	}

	// ATTESTATION DE DISPOSITIVO WebAuthn (AOS-177) por ambiente: AOS_ATTESTATION_VERIFIER_URL liga
	// o verificador REMOTO ao FourEyesGate (o CBOR corre no componente de autoridade externo; o
	// binário do nó fica zero-dep). Com ele, cada perna de aprovação exige attestationObject válido.
	// O token opcional vem de FICHEIRO montado (material privado nunca por variável de ambiente).
	cfg.AttestationVerifierURL = strings.TrimSpace(os.Getenv("AOS_ATTESTATION_VERIFIER_URL"))
	if p := strings.TrimSpace(os.Getenv("AOS_ATTESTATION_VERIFIER_TOKEN_PATH")); p != "" {
		tb, rerr := os.ReadFile(p)
		if rerr != nil {
			return Config{}, fmt.Errorf("aos: AOS_ATTESTATION_VERIFIER_TOKEN_PATH: %w", rerr)
		}
		cfg.AttestationVerifierToken = strings.TrimSpace(string(tb))
	}

	// FRESCURA POR-CERIMÓNIA DO 4-EYES (AOS-266, achado F10): AOS_CHALLENGE_ISSUANCE=1 liga o modo
	// issue-then-consume — o nó EMITE o challenge de cada perna por (pedido, aprovador) com TTL e a
	// verificação exige-o fresco. Sem ela, o anti-replay só dá dedup (sem frescura). O TTL opcional
	// (AOS_CHALLENGE_TTL) é o tempo de uma cerimónia; fail-closed em valor não-positivo/lixo.
	cfg.ChallengeIssuance, err = parseChallengeIssuance(os.Getenv("AOS_CHALLENGE_ISSUANCE"))
	if err != nil {
		return Config{}, err
	}
	if d := strings.TrimSpace(os.Getenv("AOS_CHALLENGE_TTL")); d != "" {
		ttl, terr := time.ParseDuration(d)
		if terr != nil || ttl <= 0 {
			return Config{}, fmt.Errorf("%w: valor %q", ErrBadChallengeTTL, d)
		}
		cfg.ChallengeTTL = ttl
	}

	// ATRIBUIÇÃO DISPOSITIVO↔APROVADOR (AOS-266, achado F10): AOS_DEVICE_ENROLLMENT_FILE alimenta
	// NewStaticDeviceEnrollment; POR OMISSÃO é o próprio AOS_APPROVERS_FILE (os device_ids vivem no
	// campo `devices` de cada aprovador). Distingue-se o ficheiro EXPLICITAMENTE apontado (vazio ⇒
	// erro) do herdado por omissão (vazio ⇒ atribuição dormente — o caso comum de um roster sem
	// dispositivos ainda enrolados). A atribuição EXIGE a porta de attestation (sem deviceID não há
	// o que confrontar): dispositivos sem AOS_ATTESTATION_VERIFIER_URL ABORTAM (fail-loud).
	enrollPath := strings.TrimSpace(os.Getenv("AOS_DEVICE_ENROLLMENT_FILE"))
	enrollExplicit := enrollPath != ""
	if enrollPath == "" {
		enrollPath = strings.TrimSpace(os.Getenv("AOS_APPROVERS_FILE"))
	}
	cfg.DeviceEnrollment, err = parseDeviceEnrollmentFile(enrollPath, enrollExplicit)
	if err != nil {
		return Config{}, err
	}
	if len(cfg.DeviceEnrollment) > 0 && cfg.AttestationVerifierURL == "" {
		return Config{}, ErrEnrollmentWithoutAttestationURL
	}

	// MODEL GATEWAY (OpenAI-compatível) por ambiente. Vazio ⇒ [Config.Model] fica nil e o Bootstrap
	// usa o referenceModel (inalterado); AOS_MODEL_ENDPOINT presente ⇒ liga o adaptador ao gateway
	// (OmniRoute/OpenRouter/…). Fail-closed: config incompleta ABORTA (ErrBadModelConfig) e, em
	// produção, um gateway sem ficheiro de credencial ABORTA (ErrProductionNeedsModelCredential,
	// AOS-247) em vez de apresentar o bearer de DEV embebido.
	modelClient, modelBinder, err := parseModelFromEnv(production)
	if err != nil {
		return Config{}, err
	}
	if modelClient != nil {
		cfg.Model = modelClient
		// CUTOVER DURO (AOS-278): o binder liga, no Bootstrap, o verifier REAL do nó ao
		// estágio authn do gateway construído aqui. Sem ele nenhum turno de modelo passa.
		cfg.ModelIdentityBinder = modelBinder
	}

	// REGISTRY ASSINADO DE TOOLS (AOS_MODEL_TOOLS_REGISTER): regista as tools de AOS_MODEL_TOOLS
	// como catálogo ASSINADO+congelável para a REVALIDAÇÃO do RM as admitir — a decisão passa então
	// ao PDP/Cedar (o gate seguinte), que nega uma capability privilegiada originada pelo modelo
	// (taint=untrusted). Desligado ⇒ (nil,…): o nó mantém o catálogo/revalidador de referência
	// (default-deny na revalidação). Ver modelcatalog.go. Fail-closed: config incoerente ABORTA.
	cat, reval, pol, rerr := buildSignedToolRegistryFromEnv()
	if rerr != nil {
		return Config{}, rerr
	}
	if cat != nil {
		cfg.Catalog = cat
		cfg.Revalidator = reval
		cfg.Policy = pol
	}

	return cfg, nil
}

// serveAPI gradua o nó para a superfície de REDE (AOS-166): compõe o loop de serviço
// ([NodeService]) e o [APIServer] com o BIND-GUARDRAIL, e serve em addr. É AQUI que o
// guardrail entra no CAMINHO DE PRODUÇÃO — [APIServer.Serve] RECUSA (erro, não um log) um
// bind não-loopback enquanto o canal de controlo não estiver autenticado, pelo que a única
// barreira contra expor o canal de controlo sem authn corre no arranque REAL, não só em
// httptest. Bloqueia até ctx ser cancelado (o sinal de paragem) e então encerra
// graciosamente. Um erro SÍNCRONO de Serve (guardrail recusou / Listen falhou) retorna de
// imediato, encerrando o loop de serviço.
func serveAPI(ctx context.Context, w io.Writer, node *Node, addr string) error {
	// TERMINAÇÃO TLS DO INGRESSO (AOS-209). Lê a config de TLS do ambiente e impõe a postura
	// de produção fail-closed (sem TLS nem opt-out em AOS_MODE=production ⇒ ErrProductionNeedsTLS)
	// ANTES de compor o serviço. As opções resultantes ligam a terminação no nó ou declaram a
	// terminação a montante; o banner (se houver) é emitido depois de o servidor compor.
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("AOS_MODE")), "production")
	tlsOpts, tlsBanner, err := apiTLSOptionsFromEnv(production)
	if err != nil {
		return err
	}
	// AOS-021 — período do varrimento de aprovações expiradas (loop de serviço). Vazio ⇒
	// default; "0" ⇒ DESLIGADO (os pendentes nunca expiram sozinhos). Um valor malformado
	// é ignorado (mantém o default): o varrimento é higiene operacional, não um gate de
	// segurança — degradar para o default é preferível a recusar o arranque do nó.
	svcOpts := []NodeServiceOption{WithServiceLog(w)}
	if v := strings.TrimSpace(os.Getenv("AOS_APPROVAL_SWEEP_INTERVAL")); v != "" {
		if d, derr := time.ParseDuration(v); derr == nil {
			svcOpts = append(svcOpts, WithApprovalSweepInterval(d))
		}
	}
	// AOS-267 — CADÊNCIA DO SCHEDULER DE RETENÇÃO. Ao contrário da linha acima, esta é
	// FAIL-CLOSED: um valor ilegível ou <= 0 ABORTA o arranque em vez de degradar para o default.
	// A diferença não é estilo. O varrimento de aprovações é higiene operacional; este conduz a
	// EXPIRAÇÃO POR TTL, e um operador que se engana na cadência e fica com outra qualquer não
	// tem forma de o notar — o sintoma seria dados pessoais retidos para lá do prazo, meses
	// depois. Ver [ErrBadRetentionSweepInterval]: nenhum valor desliga o scheduler.
	retentionSweep, err := retentionSweepIntervalFromEnv()
	if err != nil {
		return err
	}
	svcOpts = append(svcOpts, WithRetentionSweepInterval(retentionSweep))
	// AOS-274 — CADÊNCIA E JANELA DO AVALIADOR DE SLOs/ALERTAS. A avaliação em si é FAIL-OPEN (a
	// observabilidade nunca derruba o nó), mas a CONFIGURAÇÃO é fail-closed como todas as outras:
	// um valor ilegível ABORTA aqui, no arranque, em vez de degradar para o default. A fronteira
	// é deliberada — um operador que pede uma cadência e fica com outra não tem como o notar, e
	// isso não é a observabilidade a falhar, é a config a mentir. `0` na cadência DESLIGA
	// explicitamente (e o banner di-lo).
	sloInterval, err := sloEvalIntervalFromEnv()
	if err != nil {
		return err
	}
	sloWindow, err := sloWindowFromEnv()
	if err != nil {
		return err
	}
	svcOpts = append(svcOpts, WithSLOEvalInterval(sloInterval), WithSLOWindow(sloWindow))
	// AOS-277 — KNOBS DE INGRESSO. As três variáveis são lidas UMA vez, AQUI (o arranque), e
	// fail-closed: um valor inválido devolve [ErrBadIngressLimits] e o nó NÃO chega a servir.
	// O que se liga é a admission que já existia desde AOS-166 (token-bucket + tecto de runs
	// em curso, 429 em handleSubmit); o que estas opções mudam são os NÚMEROS, até aqui
	// constantes do binário. Ver ingress_env.go.
	ingressLim, ingressOpts, err := ingressLimitsFromEnv()
	if err != nil {
		return err
	}
	// TECTO DE TURNOS por run (AOS-203, F2/AOS-250): AOS_MAX_TURNS clampa `max_turns` na
	// fronteira de ingresso. Resolvido ANTES de compor o serviço — um valor malformado aborta
	// o arranque, como os limiares do disjuntor, em vez de degradar em silêncio para o default.
	maxTurnsOpt, err := apiMaxTurnsOptionFromEnv()
	if err != nil {
		return err
	}
	svc, err := NewNodeService(node, svcOpts...)
	if err != nil {
		return err
	}
	apiOpts := append([]APIOption{WithAPILog(w)}, tlsOpts...)
	apiOpts = append(apiOpts, ingressOpts...)
	if maxTurnsOpt != nil {
		apiOpts = append(apiOpts, maxTurnsOpt)
	}
	srv, err := NewAPIServer(svc, node, apiOpts...)
	if err != nil {
		return err
	}
	// Os limites EM VIGOR são declarados a partir do MESMO valor que alimentou as opções
	// acima — postura anunciada = postura ligada (AOS-248). Sai aqui, e não no banner de
	// bootstrap, porque a admission só existe quando o nó SERVE: um `aos` que faz bootstrap
	// sem AOS_API_ADDR não tem ingresso nenhum para anunciar.
	for _, line := range ingressPostureBanner(ingressLim) {
		fmt.Fprintf(w, "[aos] %s\n", line)
	}
	// AVISO PROEMINENTE do opt-out (modelo do kill-switch de soberania, AOS-203): quem termina
	// TLS a montante fica a saber, em CADA arranque, que o nó serve em claro POR DECISÃO sua.
	for _, line := range tlsBanner {
		fmt.Fprintf(w, "[aos] %s\n", line)
	}

	serveErr := make(chan error, 1)
	go func() {
		e := srv.Serve(addr) // aplica o bind-guardrail ANTES do Listen
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		serveErr <- e
	}()

	select {
	case e := <-serveErr:
		// Serve retornou cedo: o bind-guardrail RECUSOU o addr, ou o Listen falhou. O nó não
		// chega a servir — encerra o loop de serviço e propaga o erro.
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(shutCtx)
		return e
	case <-ctx.Done():
		fmt.Fprintf(w, "[aos] sinal de paragem recebido — a encerrar graciosamente (drena runs em curso, liberta leases)\n")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = svc.Shutdown(shutCtx)
		return <-serveErr
	}
}

// apiTLSOptionsFromEnv resolve a config de TERMINAÇÃO TLS do ingresso (AOS-209) a partir do
// ambiente e devolve (opções de API, linhas de banner do opt-out, erro). É a costura entre o
// processo e o [APIServer]: TODO o material privado (a chave TLS) entra por FICHEIRO montado
// (AOS_TLS_KEY_PATH) — NUNCA por variável de ambiente — no mesmo padrão de AOS_ISSUER_KEY_PATH.
//
// Três estados de transporte, e a postura de produção que os discrimina:
//
//   - TLS NO NÓ: AOS_TLS_CERT_PATH e AOS_TLS_KEY_PATH ambos definidos ⇒ [WithTLSFiles]. O
//     [NewAPIServer] carrega o par e serve TLS endurecido (fail-closed: par inválido ou
//     incompleto abortam lá). A declaração de terminação externa é irrelevante e ignorada.
//   - TERMINAÇÃO A MONTANTE (opt-out): AOS_TLS_EXTERNAL_TERMINATION declara-a ⇒
//     [WithExternalTLSTermination] + um banner RUIDOSO. O nó serve em claro POR DECISÃO de quem
//     o configurou (assume a responsabilidade de a malha/ingress cifrar o transporte).
//   - TEXTO-CLARO sem opt-out: nenhuma das duas. Fora de produção é permitido (o bind-guardrail
//     recusa na mesma o não-loopback em claro — ErrRefuseCleartextBind — mas o loopback passa).
//     Em produção ⇒ [ErrProductionNeedsTLS]: um nó de produção nunca serve o ingresso em claro
//     sem decisão explícita.
//
// Um par TLS INCOMPLETO (só cert OU só key) é deixado passar para [NewAPIServer], que o recusa
// com [ErrIncompleteTLSConfig] — a validação do par vive junto do carregamento, não duplicada aqui.
func apiTLSOptionsFromEnv(production bool) ([]APIOption, []string, error) {
	certPath := strings.TrimSpace(os.Getenv("AOS_TLS_CERT_PATH"))
	keyPath := strings.TrimSpace(os.Getenv("AOS_TLS_KEY_PATH"))
	external, err := parseTLSExternalTermination(os.Getenv("AOS_TLS_EXTERNAL_TERMINATION"))
	if err != nil {
		return nil, nil, err
	}
	// mTLS DO PLANO DE CONTROLO (DEF-012, EIXO 1) — OPT-IN, por FICHEIRO. A CA de CLIENTE (material
	// PÚBLICO) é lida do ficheiro montado AOS_CONTROL_MTLS_CA_PATH. Vazio ⇒ desligado. Definido ⇒
	// WithControlMTLS; [NewAPIServer] arbitra fail-closed (exige TLS no nó ⇒ ErrControlMTLSNeedsNodeTLS;
	// CA inválida ⇒ ErrBadControlMTLSCA).
	controlMTLSCA := strings.TrimSpace(os.Getenv("AOS_CONTROL_MTLS_CA_PATH"))

	tlsAtNode := certPath != "" && keyPath != ""
	tlsConfigured := certPath != "" || keyPath != "" // completo ou incompleto (NewAPIServer arbitra)

	// FAIL-CLOSED de produção: sem transporte cifrado NEM opt-out declarado, recusa.
	if production && !tlsConfigured && !external {
		return nil, nil, ErrProductionNeedsTLS
	}

	var opts []APIOption
	if tlsConfigured {
		opts = append(opts, WithTLSFiles(certPath, keyPath))
	}
	if external {
		opts = append(opts, WithExternalTLSTermination(true))
	}
	if controlMTLSCA != "" {
		opts = append(opts, WithControlMTLS(controlMTLSCA))
	}

	// O banner do opt-out só sai quando a terminação externa é a via EFECTIVA — isto é, quando
	// o nó NÃO termina TLS. Com TLS no nó, a declaração externa é ignorada (precedência em
	// NewAPIServer) e um aviso a dizer "sirvo em claro" seria falso.
	var banner []string
	if external && !tlsAtNode {
		banner = tlsExternalTerminationBanner()
	}
	// BANNER HONESTO do mTLS do plano de controlo (DEF-012, EIXO 1): declara o estado REALMENTE
	// composto e que é ADITIVO à assinatura ed25519 (nunca um bypass).
	if controlMTLSCA != "" {
		banner = append(banner, controlMTLSBanner()...)
	}
	return opts, banner, nil
}

// ErrBadMaxTurns — AOS_MAX_TURNS está definido mas não é um inteiro POSITIVO. Fail-closed de
// CONFIG (AOS-203, F2): um tecto malformado que fosse ignorado deixaria o operador convencido
// de que limitou o orçamento de turnos por run quando não limitou — o mesmo raciocínio do
// fail-closed dos limiares do disjuntor. Um tecto <= 0 não faz sentido (clampar tudo a 0 fá-lo-ia
// recair no default do loop, reabrindo o buraco), por isso é recusado em vez de degradado.
var ErrBadMaxTurns = errors.New("aos: AOS_MAX_TURNS invalido — esperado inteiro POSITIVO (o tecto node-local de turnos por run); vazio usa o default")

// apiMaxTurnsOptionFromEnv resolve o TECTO node-local de turnos por run (AOS-203, achado F2)
// a partir de AOS_MAX_TURNS. Vazio ⇒ (nil, nil): [NewAPIHandler] aplica o default
// [agentruntime.DefaultMaxTurns] e o clamp de ingresso continua activo com esse tecto. Definido
// e válido (> 0) ⇒ [WithMaxTurnsCeiling]. Definido e inválido ⇒ [ErrBadMaxTurns] (o nó recusa
// arrancar, no molde dos limiares do disjuntor). É node-local — não importa nada do escalonador.
func apiMaxTurnsOptionFromEnv() (APIOption, error) {
	v := strings.TrimSpace(os.Getenv("AOS_MAX_TURNS"))
	if v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("%w: AOS_MAX_TURNS=%q", ErrBadMaxTurns, v)
	}
	return WithMaxTurnsCeiling(n), nil
}

// controlMTLSBanner declara que o mTLS do plano de controlo (DEF-012, EIXO 1) está LIGADO e que é
// uma SEGUNDA barreira (transporte) ADITIVA à assinatura ed25519 do corpo (AOS-160), nunca um
// substituto dela.
func controlMTLSBanner() []string {
	return []string{
		"mTLS do plano de controlo (DEF-012): LIGADO (AOS_CONTROL_MTLS_CA_PATH) — /steer,/pause,/approve exigem certificado de cliente verificado contra a CA montada, ALEM da assinatura ed25519 do corpo (AOS-160)",
		"=> ADITIVO, NAO BYPASS: um certificado de cliente valido com assinatura ed25519 ausente/ma continua RECUSADO; a assinatura permanece a barreira primaria, o mTLS e a segunda",
		"=> ESCOPADO ao plano de controlo: /healthz,/readyz,GET|POST /runs e /trajectory NAO exigem certificado de cliente (VerifyClientCertIfGiven no listener; a recusa vive nos handlers de controlo)",
	}
}

// parseTLSExternalTermination interpreta AOS_TLS_EXTERNAL_TERMINATION (AOS-209) como um
// booleano EXPLÍCITO, no molde de [parseDurableExecution]: vazio/desligado ⇒ false; um dos
// valores verdadeiros ⇒ true; QUALQUER OUTRO valor ⇒ [ErrBadTLSExternalTermination] (lixo NÃO
// é false — ver a justificação no erro).
func parseTLSExternalTermination(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return false, nil
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%w: valor %q", ErrBadTLSExternalTermination, strings.TrimSpace(s))
	}
}

// tlsExternalTerminationBanner produz o AVISO PROEMINENTE do opt-out de TLS (AOS-209), no
// modelo do kill-switch de soberania (AOS-203): declara, sem eufemismo, que o nó serve o
// ingresso em texto-claro POR DECISÃO de quem o configurou, que assume a cifra do transporte a
// montante, e como se religa a terminação no nó.
func tlsExternalTerminationBanner() []string {
	return []string{
		"AVISO TLS (AOS-209): TERMINACAO A MONTANTE DECLARADA (AOS_TLS_EXTERNAL_TERMINATION) — o no serve API/SSE/DSAR em TEXTO-CLARO por DECISAO de quem o configurou",
		"=> RESPONSABILIDADE ASSUMIDA: a cifra do transporte passa a depender do ingress/malha de servico a montante; se essa camada nao cifrar, o transporte fica legivel por qualquer intermediario na rota",
		"=> O bind NAO-loopback em claro deixa de ser recusado (a quarta conjuncao do bind-guardrail da-se por satisfeita); a assinatura ed25519 do canal de controlo continua integra, mas o CONTEUDO transportado e observavel se a montante nao cifrar",
		"=> PARA TERMINAR TLS NO PROPRIO NO: remova AOS_TLS_EXTERNAL_TERMINATION e defina AOS_TLS_CERT_PATH + AOS_TLS_KEY_PATH (chave privada por ficheiro montado, NUNCA por variavel de ambiente)",
	}
}

// envOr devolve a variável de ambiente name ou def se vazia.
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// parseEd25519PubHex descodifica uma pubkey ed25519 de 32 bytes em hex (64 chars).
// Fail-closed: qualquer erro de descodificação ou tamanho errado devolve
// [ErrBadIssuerPubKey] — só material público bem-formado se torna trust anchor.
func parseEd25519PubHex(raw string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrBadIssuerPubKey
	}
	return ed25519.PublicKey(b), nil
}

// loadPolicyBundleFromEnv é a SUPERFÍCIE DE CARREGAMENTO do bundle PDP (AOS-220) — o caminho que
// FALTAVA entre o ambiente e o composition-root. Sem ele, [Config.PDP] era INALCANÇÁVEL pelo
// binário entregue (Config vive em `package main`) e o nó caía SEMPRE em [pdp.NewUnloaded] ⇒
// default-deny de TODA a tool call mediada, sem forma de carregar política (achado #5 / DEF-604).
//
// Contrato fail-closed, com o trust anchor FORÇADO out-of-band:
//   - AOS_POLICY_BUNDLE_DIR ausente/vazio ⇒ (nil, nil): default-deny MANTÉM-SE, mas agora
//     EXPLÍCITO e declarado (o composition-root compõe [pdp.NewUnloaded] e o banner anuncia-o).
//     É a retro-compatibilidade: o binário sem a variável arranca exactamente como antes.
//   - AOS_POLICY_BUNDLE_DIR definido SEM AOS_POLICY_TRUST_ANCHOR ⇒ [ErrPolicyBundleNeedsTrustAnchor]:
//     o anchor NUNCA é lido do próprio dir do bundle (isso reabriria o cold-start que
//     [pdp.WithTrustAnchor] fecha) — exige-se a pubkey out-of-band do ambiente.
//   - anchor malformado ⇒ [ErrBadPolicyTrustAnchor]; bundle que não verifica contra o anchor
//     (ausente/adulterado/assinado por outra chave) ⇒ [ErrPolicyBundleLoad] via [pdp.Open].
//
// Devolve o [*pdp.PDP] verificado e compilado, pronto a ser injectado em [Config.PDP] (tem
// PRECEDÊNCIA sobre o default não-carregado no Bootstrap), e a cablagem do oráculo de autonomia
// ([autonomyWiring]) que o composition-root tem de PROVISIONAR depois de abrir o WORM — o
// oráculo é ligado ao PDP aqui (é opção de [pdp.Open]) mas os níveis só se registam lá, para que
// nasçam selados na hash-chain (AOS-248). nil ⇒ oráculo não ligado.
func loadPolicyBundleFromEnv() (*pdp.PDP, *autonomyWiring, error) {
	dir := strings.TrimSpace(os.Getenv("AOS_POLICY_BUNDLE_DIR"))
	if dir == "" {
		return nil, nil, nil // sem superfície ⇒ default-deny EXPLÍCITO (NewUnloaded no composition-root)
	}
	raw := strings.TrimSpace(os.Getenv("AOS_POLICY_TRUST_ANCHOR"))
	if raw == "" {
		return nil, nil, ErrPolicyBundleNeedsTrustAnchor
	}
	anchor, err := parsePolicyTrustAnchor(raw)
	if err != nil {
		return nil, nil, err
	}
	// O anchor é FORÇADO via WithTrustAnchor: [pdp.Open] verifica a assinatura do bundle contra
	// ELE, ignorando qualquer trust_anchor.pub que exista no dir mutável do bundle. Um bundle
	// assinado por outra chave é recusado (ErrSignatureInvalid), não carregado.
	opts := []pdp.Option{pdp.WithTrustAnchor(anchor)}
	// ORÁCULO DE AUTONOMIA (AOS-087) — o que torna o `escalate` ALCANÇÁVEL e, com ele,
	// todo o bridge de aprovação humana (AOS-021). Cedar só exprime permit/deny; é a
	// composição nível-de-autonomia × classe-de-risco que rebaixa um permit para
	// escalate. Sem esta variável o oráculo NÃO é ligado e nada escala — ver
	// autonomy_levels.go para a razão de ser opt-in.
	specs, aerr := parseAutonomyLevels()
	if aerr != nil {
		return nil, nil, aerr
	}
	// FASE 1 da cablagem (AOS-248): o registo nasce com o [autonomy.Sink] ligado mas VAZIO. Os
	// níveis são aplicados na FASE 2 ([autonomyWiring.provision], em Bootstrap), depois de o WORM
	// existir — só assim cada SetLevel de provisionamento fica SELADO com motivo e actor. Registar
	// aqui, como se fazia, deixava a mudança de nível sem rasto em lado nenhum.
	cabling := buildAutonomyOracle(specs)
	if cabling != nil {
		opts = append(opts, pdp.WithAutonomyOracle(cabling.oracle()))
	}
	p, err := pdp.Open(dir, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: dir %q: %v", ErrPolicyBundleLoad, dir, err)
	}
	return p, cabling, nil
}

// parsePolicyTrustAnchor descodifica a pubkey ed25519 (64 hex chars) do trust anchor da política
// (AOS_POLICY_TRUST_ANCHOR). Gémeo de [parseEd25519PubHex] mas com o erro DEDICADO
// [ErrBadPolicyTrustAnchor] — fail-closed: qualquer erro de descodificação ou tamanho errado
// aborta, só material público bem-formado se torna âncora de confiança.
func parsePolicyTrustAnchor(raw string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrBadPolicyTrustAnchor
	}
	return ed25519.PublicKey(b), nil
}

// parseWormAnchorFromEnv é a SUPERFÍCIE DE CARREGAMENTO da verificação ancorada do WORM
// (AOS-268/AOS-072) — o caminho entre o ambiente e o composition-root, no MESMO molde de
// [loadPolicyBundleFromEnv]. Devolve o material out-of-band que [Bootstrap] usa para chamar
// [audit.VerifyFromCheckpointAtHead] DEPOIS de o store estar composto e ANTES de servir.
//
// Contrato fail-closed, com o material FORÇADO out-of-band (nunca do próprio WORM):
//   - AS TRÊS ausentes ⇒ (nil, nil): NÃO-ANCORADO, comportamento actual (só a re-verificação de
//     AOS-221), declarado no banner ([wormAnchorPostureBanner]). É a retro-compatibilidade.
//   - ALGUMAS mas não todas ⇒ [ErrWormAnchorIncomplete]: as três só fecham o vector em conjunto.
//   - AOS_WORM_TRUST_ANCHOR malformada ⇒ [ErrBadWormTrustAnchor]; AOS_WORM_CHECKPOINT_FILE
//     inexistente/ilegível/JSON malformado ⇒ [ErrBadWormCheckpoint]; AOS_WORM_EXPECTED_HEAD
//     não-uint64-positivo ⇒ [ErrBadWormExpectedHead].
//
// A VERIFICAÇÃO da assinatura e da frescura NÃO acontece aqui (exige o store composto) — aqui só
// se PARSEIA e se valida a FORMA. O piso de frescura (ExpectedHead) vem de uma env PRÓPRIA e NÃO
// se deriva de Checkpoint.AuditSeq de propósito: derivá-lo tornaria a comparação AuditSeq >=
// piso trivialmente verdadeira (no-op) e reabriria o rollback de checkpoint que
// [audit.VerifyFromCheckpointAtHead] existe para fechar.
func parseWormAnchorFromEnv() (*WormAnchor, error) {
	rawAnchor := strings.TrimSpace(os.Getenv("AOS_WORM_TRUST_ANCHOR"))
	rawFile := strings.TrimSpace(os.Getenv("AOS_WORM_CHECKPOINT_FILE"))
	rawHead := strings.TrimSpace(os.Getenv("AOS_WORM_EXPECTED_HEAD"))

	// AS TRÊS ausentes ⇒ não-ancorado (retro-compat). Config PARCIAL ⇒ aborta.
	present := 0
	if rawAnchor != "" {
		present++
	}
	if rawFile != "" {
		present++
	}
	if rawHead != "" {
		present++
	}
	if present == 0 {
		return nil, nil
	}
	if present != 3 {
		return nil, ErrWormAnchorIncomplete
	}

	b, err := hex.DecodeString(rawAnchor)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrBadWormTrustAnchor
	}
	public := ed25519.PublicKey(b)

	// A âncora assinada é lida de um FICHEIRO montado out-of-band (não do WORM). O JSON é o de
	// [audit.Checkpoint] selado out-of-process pelo [audit.Signer] (campos exportados, EntryHash/
	// Signature em base64 pelo encoding/json da stdlib — zero-dep).
	raw, err := os.ReadFile(rawFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWormCheckpoint, err)
	}
	var cp audit.Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadWormCheckpoint, err)
	}

	head, err := strconv.ParseUint(rawHead, 10, 64)
	if err != nil || head == 0 {
		return nil, ErrBadWormExpectedHead
	}

	return &WormAnchor{Public: public, Checkpoint: cp, ExpectedHead: head}, nil
}

// parseBoardRegions interpreta a config DEMO-GRADE do registo board→região (AOS-172, D7) na
// forma "board=regiao,board2=regiao2". FAIL-CLOSED e sem degradação silenciosa:
//
//   - VAZIO (ou só espaços) ⇒ (nil, nil): "não configurado", read-path legado DELIBERADO.
//   - segmentos VAZIOS entre vírgulas (ex.: vírgula final) são tolerados (ruído de formatação).
//   - qualquer entrada NÃO-VAZIA MALFORMADA (sem '=', ou board/região vazios) ⇒ (nil,
//     [ErrBadBoardRegions]): "configurado mas inválido", ABORTA. Um typo (ex.: "aos-demo"
//     sem '=') NÃO é descartado em silêncio para depois abrir TODAS as leituras sem authz
//     (D7) nem selo (D6) — a ambiguidade de soberania NEGA, coerente com o resto do boot.
//
// Coerente com o fail-closed de [govsov.NewRegistry] (que também descarta board/região
// vazios) mas mais estrito na FRONTEIRA de config: aqui um valor inválido aborta o arranque
// em vez de silenciosamente resultar num registo vazio ⇒ read-path aberto.
func parseBoardRegions(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil // não configurado ⇒ legado deliberado (não é um erro).
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		if strings.TrimSpace(pair) == "" {
			continue // segmento vazio (ex.: vírgula final) ⇒ ruído tolerável, não um typo.
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: entrada %q sem '=' (esperado board=regiao)", ErrBadBoardRegions, strings.TrimSpace(pair))
		}
		board, region := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if board == "" || region == "" {
			return nil, fmt.Errorf("%w: entrada %q com board ou regiao vazios", ErrBadBoardRegions, strings.TrimSpace(pair))
		}
		out[board] = region
	}
	if len(out) == 0 {
		// Só houve segmentos vazios (ex.: ",," ou ", ") — configurado mas sem entrada válida.
		return nil, fmt.Errorf("%w: nenhuma entrada valida em %q", ErrBadBoardRegions, s)
	}
	return out, nil
}

// sovereignReadDefaultMaxAge é o TECTO por-omissão da idade (iat) de um ID-token de leitura
// soberana quando AOS_SOVEREIGN_OIDC_MAX_AGE não é fornecido. FECHA o achado de anti-replay
// (auditoria v4): sem MaxAge, o [oidc.Verifier] só detecta reutilização quando o token TRAZ jti
// (ver [oidc.Verifier.Validate]); um ID-token de soberania LEGITIMAMENTE emitido e capturado
// (ex.: fuga a montante da terminação TLS externa) seria reapresentável durante TODA a janela
// exp. Um MaxAge não-nulo limita essa janela INDEPENDENTEMENTE de jti — um token com iat mais
// antigo que now-MaxAge (mais leeway) é recusado ([oidc.ErrTokenTooOld]). 5 min alinha com a
// recomendação de [oidc.Config.MaxAge] e é folgado o bastante para o relógio real; alarga-se por
// AOS_SOVEREIGN_OIDC_MAX_AGE quando o IdP emite tokens mais longevos.
const sovereignReadDefaultMaxAge = 5 * time.Minute

// parseSovereignReadOIDC constrói a config do verificador OIDC da CREDENCIAL FORTE de leitura
// (AOS-205) a partir do ambiente: AOS_SOVEREIGN_OIDC_ISSUER (obrigatório), AOS_SOVEREIGN_OIDC_AUDIENCE
// (obrigatório), AOS_SOVEREIGN_OIDC_JWKS_URI (opcional — vazio ⇒ discovery via issuer),
// AOS_SOVEREIGN_OIDC_MAX_AGE (opcional — default [sovereignReadDefaultMaxAge]) e
// AOS_SOVEREIGN_OIDC_REQUIRE_JTI (opcional — default false).
//
// FAIL-CLOSED e sem degradação silenciosa (padrão de [parseBoardRegions]):
//
//   - AMBOS vazios (e sem jwks_uri) ⇒ (nil, nil): "não configurado" ⇒ via legada por headers
//     demo-grade. Aceite só FORA de produção (a produção exige-a — ver
//     [ErrProductionNeedsSovereignAuthority]).
//   - UM só de issuer/audience presente ⇒ [ErrBadSovereignOIDC]: "configurado mas incompleto",
//     ABORTA. Não se degrada para o read-path por header quando alguém TENCIONA ligar a credencial.
//   - AOS_SOVEREIGN_OIDC_MAX_AGE presente mas não-parseável, ou ≤ 0 ⇒ [ErrBadSovereignOIDC]:
//     ABORTA (não se degrada para "sem tecto", que é exactamente o buraco de replay que fechamos).
//   - AOS_SOVEREIGN_OIDC_REQUIRE_JTI com valor não-booleano ⇒ [ErrBadSovereignOIDC]: ABORTA.
//
// ANTI-REPLAY (fecha o achado da auditoria v4). Quando a credencial forte está ligada, o
// verificador é SEMPRE construído com um MaxAge não-nulo (default 5 min), pelo que um ID-token de
// soberania capturado deixa de ser reapresentável durante toda a janela exp — a janela fica
// limitada a MaxAge+leeway, INDEPENDENTEMENTE de o IdP emitir jti. Quando o IdP EMITE jti, a
// reutilização do MESMO (iss,jti) é adicionalmente recusada por-token; AOS_SOVEREIGN_OIDC_REQUIRE_JTI=1
// TORNA o jti OBRIGATÓRIO (single-use estrito — recusa qualquer token sem jti), para políticas que
// o exijam. Sem RequireJTI, um IdP que não emita jti continua coberto pelo MaxAge.
//
// A validação do CONTEÚDO (issuer/audience não vazios, transporte https salvo loopback) é de
// [oidc.NewVerifier], invocado no Bootstrap — um issuer http não-loopback aborta lá (fail-closed).
// SÓ material PÚBLICO entra por aqui: o issuer e o client id do IdP de soberania, nunca segredos.
func parseSovereignReadOIDC() (*oidc.Config, error) {
	issuer := strings.TrimSpace(os.Getenv("AOS_SOVEREIGN_OIDC_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("AOS_SOVEREIGN_OIDC_AUDIENCE"))
	jwksURI := strings.TrimSpace(os.Getenv("AOS_SOVEREIGN_OIDC_JWKS_URI"))
	if issuer == "" && audience == "" && jwksURI == "" {
		return nil, nil // não configurado ⇒ via legada por headers (fora de produção).
	}
	if issuer == "" || audience == "" {
		return nil, ErrBadSovereignOIDC
	}
	maxAge, err := parseSovereignReadMaxAge(os.Getenv("AOS_SOVEREIGN_OIDC_MAX_AGE"))
	if err != nil {
		return nil, err
	}
	requireJTI, err := parseSovereignReadRequireJTI(os.Getenv("AOS_SOVEREIGN_OIDC_REQUIRE_JTI"))
	if err != nil {
		return nil, err
	}
	return &oidc.Config{
		Issuer:     issuer,
		Audience:   audience,
		JWKSURI:    jwksURI,
		MaxAge:     maxAge,
		RequireJTI: requireJTI,
	}, nil
}

// ErrBadRetention — a política de retenção TTL (AOS-092/AOS-213) está presente no ambiente mas
// INVÁLIDA: um de AOS_RETENTION_VERSION/AOS_RETENTION_PERIODS em falta, uma classe de dados
// desconhecida, uma duração não-parseável/≤0, ou a versão não-SemVer. Fail-closed de CONFIG, no
// padrão de ErrBadBoardRegions: distingue "não configurado" (ambos vazios ⇒ nada expira, o
// ExpirationJob varre mas não apaga — comportamento actual) de "configurado mas inválido" (aborta,
// em vez de degradar em silêncio para uma política parcial ou vazia). Um operador que se engana no
// nome de uma classe ou na duração NÃO obtém um nó que expira menos do que pensa.
var ErrBadRetention = errors.New("aos: política de retenção inválida — AOS_RETENTION_VERSION (SemVer) e AOS_RETENTION_PERIODS (\"classe=duracao,...\" com classe em {diagnostic,trajectory,audit,pii_operational} e duracao>0) sao AMBOS obrigatorios quando um deles e definido")

// retentionClasses é o vocabulário FECHADO de classes de dados que a política de retenção pode
// nomear — o espelho das constantes [audit.DataClass]. Uma classe fora deste conjunto ABORTA
// (ErrBadRetention) em vez de ser silenciosamente ignorada: um typo não deve deixar uma classe por
// expirar sem aviso.
var retentionClasses = map[string]audit.DataClass{
	string(audit.ClassDiagnostic):     audit.ClassDiagnostic,
	string(audit.ClassTrajectory):     audit.ClassTrajectory,
	string(audit.ClassAudit):          audit.ClassAudit,
	string(audit.ClassPIIOperational): audit.ClassPIIOperational,
}

// parseRetentionFromEnv constrói a política de retenção TTL-por-classe (AOS-092/AOS-213) a partir
// do ambiente — a superfície que faltava para o [audit.ExpirationJob] (SEMPRE composto) ter uma
// política a aplicar. Sem isto, [Config.Retention] era INALCANÇÁVEL pelo binário e o /dsar/expire
// varria mas NADA expirava (fail-closed). Duas variáveis, ambas-ou-nenhuma:
//
//   - AOS_RETENTION_VERSION — SemVer MAJOR.MINOR.PATCH que rotula a política (auditável no selo WORM);
//   - AOS_RETENTION_PERIODS — "classe=duracao,..." (ex.: "pii_operational=720h,audit=8760h"); a
//     duração é um [time.Duration] (>0); uma classe omitida = NUNCA expira (semântica de AOS-092).
//
// Ambas vazias ⇒ devolve o zero-value (versão vazia ⇒ nada expira, inalterado). Uma sem a outra,
// classe desconhecida, duração ≤0/ilegível, ou versão não-SemVer ⇒ [ErrBadRetention] (aborta). A
// GRANULARIDADE de materialização por-titular e o residual da KEK única continuam como em
// retention.go — esta função só liga a POLÍTICA, não muda o mecanismo de crypto-shred.
func parseRetentionFromEnv() (audit.RetentionConfig, error) {
	version := strings.TrimSpace(os.Getenv("AOS_RETENTION_VERSION"))
	rawPeriods := strings.TrimSpace(os.Getenv("AOS_RETENTION_PERIODS"))
	if version == "" && rawPeriods == "" {
		return audit.RetentionConfig{}, nil // não configurado ⇒ nada expira (comportamento actual).
	}
	if version == "" || rawPeriods == "" {
		return audit.RetentionConfig{}, ErrBadRetention
	}
	periods := make(map[audit.DataClass]time.Duration)
	for _, pair := range strings.Split(rawPeriods, ",") {
		if strings.TrimSpace(pair) == "" {
			continue // segmento vazio (vírgula final) ⇒ ruído tolerável, não um typo.
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return audit.RetentionConfig{}, fmt.Errorf("%w: entrada %q sem '='", ErrBadRetention, strings.TrimSpace(pair))
		}
		className, rawDur := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		class, ok := retentionClasses[className]
		if !ok {
			return audit.RetentionConfig{}, fmt.Errorf("%w: classe desconhecida %q", ErrBadRetention, className)
		}
		if _, dup := periods[class]; dup {
			return audit.RetentionConfig{}, fmt.Errorf("%w: classe %q repetida", ErrBadRetention, className)
		}
		dur, err := time.ParseDuration(rawDur)
		if err != nil || dur <= 0 {
			return audit.RetentionConfig{}, fmt.Errorf("%w: duracao invalida %q para a classe %q", ErrBadRetention, rawDur, className)
		}
		periods[class] = dur
	}
	if len(periods) == 0 {
		return audit.RetentionConfig{}, fmt.Errorf("%w: nenhuma entrada valida em AOS_RETENTION_PERIODS", ErrBadRetention)
	}
	rc, err := audit.NewRetentionConfig(version, periods)
	if err != nil {
		return audit.RetentionConfig{}, fmt.Errorf("%w: %v", ErrBadRetention, err)
	}
	return rc, nil
}

// ErrBadVaultDSAR — a custódia externa da KEK em HashiCorp Vault (AOS-215/AOS-216) está pedida
// (AOS_DSAR_VAULT_ADDR presente) mas mal configurada: sem AOS_DSAR_VAULT_TOKEN_PATH, ou o ficheiro
// do token ilegível/vazio. Fail-closed: um endereço de Vault sem credencial NÃO degrada para o
// vault in-memory demo-grade — quem pede custódia externa obtém-na ou o nó recusa arrancar.
var ErrBadVaultDSAR = errors.New("aos: custódia DSAR no Vault mal configurada — AOS_DSAR_VAULT_ADDR exige AOS_DSAR_VAULT_TOKEN_PATH (ficheiro montado com o token do Vault; material privado NUNCA por variável de ambiente)")

// ErrInsecureVaultDSARAddr — AOS_DSAR_VAULT_ADDR com transporte inseguro (AOS-249, achado F6).
// Fail-closed no ARRANQUE, com o MESMO critério do verificador de attestation remoto que vive no
// MESMO binário ([integration.CheckSecureTransportURL]): https, ou http só em loopback.
//
// Não é zelo de esquema. Cada pedido a esta URL leva o token do Vault no cabeçalho X-Vault-Token
// e os ciphertexts da KEK no corpo; em claro pela rede, um intermediário fica com a credencial
// que DESTRÓI chaves de titulares (crypto-shred) — over-erasure irreversível — e com o material
// de cifra. Ter o gémeo de attestation a recusar isto e a custódia da KEK a aceitá-lo era a
// divergência que o desafio A3 apanhou.
var ErrInsecureVaultDSARAddr = errors.New("aos: AOS_DSAR_VAULT_ADDR com transporte INSEGURO — o token do Vault (que destroi chaves de titulares) e o material de custodia atravessariam a rede em claro; exige https (http SO em loopback), o mesmo criterio do verificador de attestation remoto no mesmo binario")

// ErrBadVaultTokenMinTTL — AOS_DSAR_VAULT_TOKEN_MIN_TTL presente mas não é uma duração Go > 0.
// Fail-closed de config: uma margem malformada não degrada silenciosamente para o default — seria
// o operador a julgar ter uma janela de aviso que não tem.
var ErrBadVaultTokenMinTTL = errors.New("aos: AOS_DSAR_VAULT_TOKEN_MIN_TTL invalida (esperada uma duracao Go > 0, ex.: \"5m\") — a margem com que o /readyz fica VERMELHO ANTES de o token do Vault expirar")

// parseVaultDSARFromEnv constrói a custódia da KEK por-titular em HashiCorp Vault (Transit) a
// partir do ambiente — a via que preenche [Config.DSARVault] com custódia EXTERNA em vez do
// [audit.InMemoryKeyVault] demo-grade. Vazio ⇒ nil (in-memory, inalterado). Presente ⇒ exige o
// token por FICHEIRO montado (material privado nunca por env). O adaptador é key-never-leaves
// ([vaultKeyVault] implementa [audit.KeyWrapper]): a KEK vive no Vault e o crypto-shred destrói-a lá.
// AOS-249 acrescenta-lhe duas coisas: a VALIDAÇÃO DE ESQUEMA da URL (reutilizando o critério do
// gémeo de attestation, não escrevendo um segundo) e a passagem do CAMINHO do token + margem de
// prontidão ao adaptador, para que o token possa ser re-lido/renovado em vez de congelar no
// arranque.
func parseVaultDSARFromEnv() (audit.KeyVault, error) {
	addr := strings.TrimSpace(os.Getenv("AOS_DSAR_VAULT_ADDR"))
	if addr == "" {
		return nil, nil // não configurado ⇒ vault in-memory de referência (comportamento actual).
	}
	// AOS-249 (F6) — transporte. O critério é PARTILHADO com o verificador de attestation remoto
	// (o mesmo binário recusava lá o que aceitava aqui); ver [ErrInsecureVaultDSARAddr].
	if err := integration.CheckSecureTransportURL(addr); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInsecureVaultDSARAddr, err)
	}
	tokenPath := strings.TrimSpace(os.Getenv("AOS_DSAR_VAULT_TOKEN_PATH"))
	if tokenPath == "" {
		return nil, ErrBadVaultDSAR
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("%w: ler token: %v", ErrBadVaultDSAR, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("%w: token vazio em %q", ErrBadVaultDSAR, tokenPath)
	}
	mount := strings.TrimSpace(os.Getenv("AOS_DSAR_VAULT_TRANSIT_MOUNT"))
	if mount == "" {
		mount = "transit"
	}
	// AOS-249 (F6) — margem de prontidão do token. É a antecedência com que o /readyz fica
	// VERMELHO face à expiração; vazia ⇒ [DefaultVaultTokenMinTTL].
	minTTL := DefaultVaultTokenMinTTL
	if v := strings.TrimSpace(os.Getenv("AOS_DSAR_VAULT_TOKEN_MIN_TTL")); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil || d <= 0 {
			return nil, fmt.Errorf("%w: AOS_DSAR_VAULT_TOKEN_MIN_TTL=%q", ErrBadVaultTokenMinTTL, v)
		}
		minTTL = d
	}
	// O CAMINHO (nunca o valor) segue para o adaptador: é o que permite adoptar um token rodado
	// por um agente externo sem reiniciar o nó.
	return newVaultKeyVault(addr, mount, token,
		withVaultTokenFile(tokenPath),
		withVaultTokenMinTTL(minTTL)), nil
}

// ErrBadModelConfig — o gateway de modelos está pedido (AOS_MODEL_ENDPOINT presente) mas mal
// configurado: sem AOS_MODEL_NAME, ou o ficheiro de AOS_MODEL_API_KEY_PATH ilegível/vazio. Fail-
// closed: um endpoint sem o modelo a pedir NÃO degrada para o referenceModel — quem liga um gateway
// obtém-no ou o nó recusa arrancar.
var ErrBadModelConfig = errors.New("aos: config do model gateway invalida — AOS_MODEL_ENDPOINT exige AOS_MODEL_NAME (id do modelo a pedir ao gateway); AOS_MODEL_API_KEY_PATH, se definido, tem de ser um ficheiro legivel (material privado NUNCA por variavel de ambiente)")

// parseModelFromEnv liga [Config.Model] a um gateway OpenAI-compatível (OmniRoute/OpenRouter/…) a
// partir do ambiente — a via que preenche a porta [agentruntime.ModelClient] em vez do
// referenceModel. Vazio ⇒ nil (referenceModel, inalterado). Presente ⇒ exige AOS_MODEL_NAME; a API
// key (opcional FORA de produção) vem por FICHEIRO montado. O adaptador é zero-dep
// ([openAIModelClient]).
//
// production é a postura do nó (AOS_MODE=production), recebida por PARÂMETRO — como
// [apiTLSOptionsFromEnv] — para que haja UMA única leitura de AOS_MODE por arranque e o gate de
// AOS-247 não possa divergir das restantes exigências de produção impostas em [nodeConfigFromEnv].
//
// CUTOVER DURO DE IDENTIDADE (AOS-278): o gateway é construído aqui, ANTES de a identidade
// estar composta, com um verifier LIGADO TARDIAMENTE ([lateBoundModelVerifier]) que nega
// fail-closed até o [Bootstrap] o ligar ao verifier REAL do nó. Por isso devolve TAMBÉM o
// binder — a closure que o Bootstrap chama com esse verifier (Config.ModelIdentityBinder).
// Sem gateway (endpoint vazio) ⇒ (nil, nil, nil): sem modelo e sem binder.
func parseModelFromEnv(production bool) (agentruntime.ModelClient, func(*identity.Verifier), error) {
	endpoint := strings.TrimSpace(os.Getenv("AOS_MODEL_ENDPOINT"))
	if endpoint == "" {
		return nil, nil, nil // não configurado ⇒ modelo de referência (comportamento actual).
	}
	model := strings.TrimSpace(os.Getenv("AOS_MODEL_NAME"))
	if model == "" {
		return nil, nil, ErrBadModelConfig
	}
	// Compõe o Model Gateway REAL (EPIC-06) apontado ao endpoint; a API key (opcional) é lida do
	// ficheiro pelo builder. Ver modelgatewaywiring.go.
	apiKeyPath := strings.TrimSpace(os.Getenv("AOS_MODEL_API_KEY_PATH"))
	// FAIL-CLOSED de produção (AOS-247, achado F5) — o gateway está LIGADO (chegámos aqui com
	// AOS_MODEL_ENDPOINT não-vazio) e não há ficheiro de credencial: em produção RECUSA. Sem esta
	// guarda o nó arrancava a apresentar ao upstream o bearer de DEV embebido no binário
	// ([staticModelCredential.Fetch]) — uma credencial que não identifica o nó, é a mesma em todos
	// e não se revoga — e NADA o declarava. É a guarda de ARRANQUE porque o `Fetch` é por-pedido:
	// falhar lá daria um nó vivo que anuncia gateway e erra cada chamada. Fora de produção o
	// fallback continua legítimo (a stack de dev fala com um OmniRoute/LiteLLM que pode nem exigir
	// bearer), mas deixa de ser silencioso — ver [devModelCredentialBanner].
	if production && apiKeyPath == "" {
		return nil, nil, ErrProductionNeedsModelCredential
	}
	// Fronteira de soberania (board/região) que o nó declara ao gateway. Defaults casam com a
	// allowlist EMBEBIDA; com um bundle externo o operador escolhe-os livremente.
	region := defaultModelGatewayRegion
	if v := strings.TrimSpace(os.Getenv("AOS_MODEL_REGION")); v != "" {
		region = v
	}
	board := defaultModelGatewayBoard
	if v := strings.TrimSpace(os.Getenv("AOS_MODEL_BOARD")); v != "" {
		board = v
	}
	// Allowlist EXTERNA (bundle assinado montado) se pedida; nil ⇒ embebida. Ver modelgatewaywiring.go.
	pol, err := loadModelAllowlistFromEnv()
	if err != nil {
		return nil, nil, err
	}
	// Tool set OFERECIDO ao modelo (registry opt-in AOS_MODEL_TOOLS): sem ele o modelo não pede
	// tools; com ele, cada tool call é MEDIADA pelo Reference Monitor (o binding capability/recurso
	// vem do registry trusted, o AuthorizationTaint fica untrusted). Ver modeltools.go.
	tools, bindings, err := loadModelToolsFromEnv()
	if err != nil {
		return nil, nil, err
	}
	// AUDIT DE GOVERNAÇÃO DURÁVEL (AOS-265): AOS_MODEL_AUDIT_PATH ⇒ WORM tamper-evident
	// onde a activação da allowlist e as decisões por chamada sobrevivem ao restart;
	// vazio ⇒ MemStore de referência (volátil, inalterado). Fail-closed: um caminho
	// definido que não abre ABORTA (ErrBadModelAudit), em vez de degradar em silêncio.
	gwAudit, _, err := parseModelAuditFromEnv()
	if err != nil {
		return nil, nil, err
	}
	// CUTOVER DURO (AOS-278): o gateway compõe-se com um verifier LIGADO TARDIAMENTE. Nasce a
	// negar fail-closed; o Bootstrap liga-o (via o binder devolvido) ao verifier REAL do nó —
	// o MESMO que verifica as tool calls. NÃO há stub allow-all no caminho.
	modelVerifier := &lateBoundModelVerifier{}
	client, err := newGatewayModelClient(modelVerifier, endpoint, model, apiKeyPath, region, board, pol, tools, gwAudit)
	if err != nil {
		return nil, nil, err
	}
	// Decora com o enriquecedor de governança só quando há bindings (o modelo escolhe a tool pelo
	// nome; o RM recebe a capability do registry). Sem tools ⇒ cliente nu (comportamento inalterado).
	if len(bindings) > 0 {
		client = &toolEnrichingClient{inner: client, bindings: bindings}
	}
	// NOTA (AOS-021): o decorador de RETOMA já NÃO se aplica aqui. Envolver o modelo é
	// responsabilidade da composição do nó ([Bootstrap]), para que a garantia de
	// replay-then-continue valha para QUALQUER modelo — incluindo um cfg.Model injectado,
	// que por este caminho a perdia em silêncio. O decorador continua a ser o outermost:
	// Bootstrap envolve o que quer que saia daqui, incluindo o enriquecedor de tools.
	return client, modelVerifier.bind, nil
}

// parseHumanOIDCDirectory constrói o directório de autenticação humana OIDC (frente 1 do D4,
// AOS-174; fecha DEF-110) a partir do ambiente: AOS_HUMAN_OIDC_ISSUER (obrigatório),
// AOS_HUMAN_OIDC_AUDIENCE (obrigatório), AOS_HUMAN_OIDC_JWKS_URI (opcional ⇒ discovery via issuer).
// Só tem efeito no modo de REFERÊNCIA (autoridade co-localizada); no modo endurecido o directório
// humano vive com o issuer EXTERNO (cmd/aos-issuer). Fail-closed (padrão de parseSovereignReadOIDC):
// ambos vazios ⇒ (nil,nil) ⇒ allowlist de nomes de referência (retro-compat); um só ⇒
// ErrBadHumanOIDC (aborta, sem degradar para a allowlist quando alguém tenciona ligar o IdP). A
// validação de conteúdo/transporte (issuer/audience não vazios; https salvo loopback) é de
// oidc.NewVerifier, invocado por NewOIDCDirectory aqui — um issuer http não-loopback aborta o
// arranque. Só material PÚBLICO entra (issuer + client id do IdP), nunca segredos. A frescura do
// ID-token é limitada por um MaxAge por-omissão (anti-replay do mint humano).
func parseHumanOIDCDirectory() (integration.HumanDirectory, error) {
	issuer := strings.TrimSpace(os.Getenv("AOS_HUMAN_OIDC_ISSUER"))
	audience := strings.TrimSpace(os.Getenv("AOS_HUMAN_OIDC_AUDIENCE"))
	jwksURI := strings.TrimSpace(os.Getenv("AOS_HUMAN_OIDC_JWKS_URI"))
	if issuer == "" && audience == "" && jwksURI == "" {
		return nil, nil // não configurado ⇒ allowlist de referência (retro-compat).
	}
	if issuer == "" || audience == "" {
		return nil, ErrBadHumanOIDC
	}
	dir, err := integration.NewOIDCDirectory(oidc.Config{
		Issuer:   issuer,
		Audience: audience,
		JWKSURI:  jwksURI,
		MaxAge:   5 * time.Minute, // anti-replay do mint humano (curto, como o read-path soberano)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadHumanOIDC, err)
	}
	return dir, nil
}

// parseSovereignReadMaxAge interpreta AOS_SOVEREIGN_OIDC_MAX_AGE como uma duração Go (ex.: "5m",
// "300s"). VAZIO ⇒ [sovereignReadDefaultMaxAge] (NUNCA 0 — o tecto é sempre aplicado, senão
// reabre-se o buraco de replay). Não-parseável ou ≤ 0 ⇒ [ErrBadSovereignOIDC] (fail-closed de
// config: não se degrada para "sem tecto").
func parseSovereignReadMaxAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return sovereignReadDefaultMaxAge, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: AOS_SOVEREIGN_OIDC_MAX_AGE=%q invalido (esperado duracao Go > 0, ex.: \"5m\")", ErrBadSovereignOIDC, s)
	}
	return d, nil
}

// parseSovereignReadRequireJTI interpreta AOS_SOVEREIGN_OIDC_REQUIRE_JTI como um booleano
// EXPLÍCITO (mesma gramática estrita de [parseDurableExecution]). VAZIO ⇒ false (o default: o
// MaxAge cobre o replay mesmo sem jti). Valor não-booleano ⇒ [ErrBadSovereignOIDC] (fail-closed).
func parseSovereignReadRequireJTI(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return false, nil
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%w: AOS_SOVEREIGN_OIDC_REQUIRE_JTI=%q nao e booleano", ErrBadSovereignOIDC, strings.TrimSpace(s))
	}
}

// sovereignReadKillSwitchBanner produz o AVISO PROEMINENTE do KILL-SWITCH da soberania de
// leitura (AOS-203, achado ORF-05 da auditoria v4). Devolve nil — nenhuma linha — quando o
// read-path soberano está LIGADO (o caso comum: sem AOS_BOARD_REGIONS o nó aplica o default
// de referência "board:aos-demo=eu").
//
// O DEFEITO QUE FECHA. `AOS_BOARD_REGIONS=` (DEFINIDA-VAZIA) é um kill-switch: desliga um
// controlo de CONFORMIDADE que está LIGADO por omissão — a authz por-chamador das leituras de
// governação (D7) e o selo WORM da leitura sensível (D6). Em `AOS_MODE=production` esse estado
// já é RECUSADO ([ErrProductionNeedsSovereignRead]) e este ticket NÃO altera esse fail-closed;
// fora de produção ele é legítimo (os harnesses de durabilidade e de plano de controlo usam-no
// deliberadamente), mas era SILENCIOSO: a única linha do banner era descritiva ("read-path
// LEGADO") e apontava para `Config.BoardRegions`, um campo de `package main` que o operador do
// binário nem consegue escrever. Um operador que herdasse um `-e AOS_BOARD_REGIONS=` de uma
// receita não tinha como saber o que tinha perdido.
//
// PORQUÊ AVISO E NÃO RECUSA (fora de produção). Recusar quebraria a retro-compatibilidade que
// a superfície de configuração impõe e cortaria um estado que o próprio projecto usa em
// harnesses. O critério é o mesmo de AOS-191: o que não se tolera é a PROMESSA FALSA — daí o
// aviso declarar, sem eufemismo, o que fica desligado.
//
// PORQUÊ AQUI E NÃO NO COMPOSITION-ROOT. O banner de [Bootstrap] declara o estado COMPOSTO
// (readRegions nil ⇒ "read-path LEGADO"); esta função declara a CAUSA DE AMBIENTE e o remédio,
// que só a fronteira que lê a variável conhece — um embedder que compõe [Config] à mão não
// tem AOS_BOARD_REGIONS nenhuma para religar.
//
// RESIDUAL CONHECIDO (não é omissão). A linha do composition-root ainda manda "definir
// Config.BoardRegions" — um campo de `package main` INALCANÇÁVEL por quem corre a imagem, isto
// é, metade do próprio defeito ORF-05 (sintoma com remédio inalcançável). Reescrevê-la exige
// tocar em bootstrap.go, fora da propriedade de ficheiros deste ticket; até lá, o aviso
// NEUTRALIZA-A explicitamente (a linha "IGNORE …" abaixo) em vez de a deixar a competir com o
// remédio verdadeiro — as duas linhas do banner ficam coerentes e o operador não segue a morta.
//
// Os parâmetros são o estado REALMENTE COMPOSTO, não a intenção da config: `regionsComposed`
// é Node.SovereignReadRegions != nil e `wormComposed` é Node.WORM != nil — a CONJUNÇÃO que
// api.go exige para compor a read-governance. O aviso nunca contradiz o que o nó está a fazer,
// e nunca fica calado por olhar para metade do gate.
func sovereignReadKillSwitchBanner(regionsComposed, wormComposed bool, rawBoardRegions string, defined bool) []string {
	if regionsComposed && wormComposed {
		return nil
	}
	// A causa é nomeada com precisão — cada estado produz uma mensagem diferente (o operador
	// tem de reconhecer o SEU caso na linha).
	var cause string
	switch {
	case regionsComposed && !wormComposed:
		// Inalcançável pelo caminho do binário (o Bootstrap cai sempre para audit.NewMemStore
		// quando não há WORM durável), mas é o estado em que a soberania seria anunciada e não
		// aplicada: o registo está configurado e o read-path é LEGADO na mesma. Se o WORM
		// alguma vez se tornar opcional, o operador vê a causa exacta em vez de silêncio.
		cause = "o registo board->regiao esta configurado MAS o WORM nao esta composto — o read-path soberano exige AMBOS (o selo D6 nao teria onde ser gravado), pelo que a soberania fica DESLIGADA apesar de AOS_BOARD_REGIONS ter valor"
	case defined && strings.TrimSpace(rawBoardRegions) == "":
		cause = "AOS_BOARD_REGIONS esta DEFINIDA-VAZIA (kill-switch explicito: a variavel existe no ambiente com valor vazio)"
	case !defined:
		// Inalcançável pelo caminho do binário (sem a variável aplica-se o default de
		// referência): só um Config composto in-process chega aqui. A linha diz a verdade
		// para esse caso em vez de mentir sobre uma variável que ninguem definiu.
		cause = "o registo board->regiao esta VAZIO e AOS_BOARD_REGIONS nao esta definida (Config.BoardRegions composta in-process sem entradas)"
	default:
		cause = "o registo board->regiao ficou VAZIO a partir de AOS_BOARD_REGIONS=" + strings.TrimSpace(rawBoardRegions)
	}
	return []string{
		"AVISO KILL-SWITCH (AOS-203): SOBERANIA DE LEITURA (AOS-172, D7) DESLIGADA — " + cause,
		"=> FICA DESLIGADO: (1) AUTHZ POR-CHAMADOR das leituras de governacao (D7) — o no serve TODAS as leituras sem exigir X-Aos-Reader/X-Aos-Board nem resolver a regiao autorizada do board; (2) SELO WORM da leitura sensivel (D6) — nao fica trilho tamper-evident de QUEM leu o que",
		"=> PARA RELIGAR: defina AOS_BOARD_REGIONS=\"board=regiao\" (ex.: AOS_BOARD_REGIONS=\"board:prod=eu\") ou REMOVA a variavel do ambiente para voltar ao default de referencia \"board:aos-demo=eu\"",
		"=> IGNORE a linha \"defina Config.BoardRegions\" do banner acima: Config.BoardRegions e um campo de codigo (package main) que quem corre o binario/imagem NAO consegue escrever — o unico remedio alcancavel e AOS_BOARD_REGIONS, na linha anterior",
		"=> AOS_MODE=production RECUSA arrancar neste estado (ErrProductionNeedsSovereignRead) — este aviso so existe porque o no NAO esta em modo de producao",
	}
}

// devModelCredentialBanner DECLARA o fallback de credencial do model gateway (AOS-247, achado F5),
// na disciplina de banner de AOS-203 (o molde de [tlsExternalTerminationBanner]): uma linha, só
// quando o estado REALMENTE composto o justifica, com a causa e o remédio ALCANÇÁVEL pelo operador.
// Devolve nil — nenhuma linha — nos dois casos honestos: sem gateway composto (o nó usa o
// referenceModel e não apresenta bearer nenhum a ninguém) ou com AOS_MODEL_API_KEY_PATH definido
// (a credencial veio do ficheiro montado, que [newGatewayModelClient] já validou não-vazio).
//
// O DEFEITO QUE FECHA. Sem AOS_MODEL_API_KEY_PATH, [staticModelCredential.Fetch] devolve um bearer
// de DEV embebido no binário. Contra um upstream de dev que não exige bearer isso é inócuo; o que
// não era aceitável é ser INDISTINGUÍVEL, do lado de fora, de um nó que apresenta a credencial da
// organização — nenhum banner, nenhuma env, nenhuma linha de log o dizia.
//
// O VALOR NUNCA ENTRA NA LINHA — nem este nem o do ficheiro montado. O banner declara a POSTURA
// (qual das duas credenciais está em uso e o que isso implica), nunca o segredo.
//
// PORQUÊ AVISO E NÃO RECUSA (fora de produção). O critério de AOS-191/AOS-203/AOS-209: em produção
// RECUSA-SE ([ErrProductionNeedsModelCredential], imposta em [parseModelFromEnv], que é por onde
// este estado nunca chega aqui em modo de produção); fora dela o estado é legítimo e o que não se
// tolera é a PROMESSA FALSA.
//
// gatewayComposed é o estado REALMENTE composto (Config.Model != nil), não a intenção da config —
// a mesma disciplina de [sovereignReadKillSwitchBanner]: o aviso nunca contradiz o que o nó faz.
func devModelCredentialBanner(gatewayComposed bool, apiKeyPath string) []string {
	if !gatewayComposed || strings.TrimSpace(apiKeyPath) != "" {
		return nil
	}
	return []string{
		"AVISO CREDENCIAL DE MODELO (AOS-247): BEARER DE DEV EM USO — o model gateway esta LIGADO (AOS_MODEL_ENDPOINT) sem AOS_MODEL_API_KEY_PATH, pelo que o no apresenta ao upstream um bearer de DEV embebido no binario (o mesmo em todos os nos, legivel por quem tenha o artefacto, nao revogavel) e NAO a credencial da organizacao; para apresentar a credencial real defina AOS_MODEL_API_KEY_PATH (ficheiro montado — material privado NUNCA por variavel de ambiente). AOS_MODE=production RECUSA arrancar neste estado (ErrProductionNeedsModelCredential)",
	}
}

// parseDurableExecution interpreta AOS_DURABLE_EXECUTION (AOS-191) como um booleano
// EXPLÍCITO e previsível. FAIL-CLOSED de CONFIG, no padrão de [parseBoardRegions]:
//
//   - VAZIO (ausente, ou definido só com espaços) ⇒ (false, nil): execução durável
//     DESLIGADA — o default e o comportamento actual byte-a-byte. Não há aqui o
//     terceiro estado que AOS_BOARD_REGIONS precisa: ali o vazio DESLIGAVA uma
//     protecção ligada por omissão (kill-switch); aqui o vazio COINCIDE com o default.
//   - "1"/"true"/"t"/"yes"/"y"/"on" (qualquer caixa) ⇒ (true, nil).
//   - "0"/"false"/"f"/"no"/"n"/"off" (qualquer caixa) ⇒ (false, nil).
//   - QUALQUER OUTRO valor ⇒ (false, [ErrBadDurableExecution]): ABORTA. Tratar lixo como
//     false daria ao operador que escreveu "enabled"/"tru"/"sim" um nó que perde
//     checkpoints, capturas e step-ledger no reinício — silenciosamente, e a acreditar
//     que não perde. É deliberadamente mais estrito que strconv.ParseBool no lado dos
//     valores ACEITES (que documenta) e igualmente estrito no lado dos rejeitados.
func parseDurableExecution(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return false, nil // não configurado ⇒ desligado (o default) — não é um erro.
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%w: valor %q", ErrBadDurableExecution, strings.TrimSpace(s))
	}
}

// parseOperators interpreta AOS_OPERATORS (AOS-193) — o registo emitterID→PUBKEY dos
// operadores autorizados a emitir sinais de controlo (steer/pause/resume) — na forma
// "emitterID=hexpubkey,emitterID2=hexpubkey2".
//
// FORMATO: é DELIBERADAMENTE a gramática de [parseBoardRegions] (mesmo separador de entradas,
// mesmo separador chave/valor, mesma tolerância a segmentos vazios) com o valor codificado
// como em AOS_ISSUER_PUBKEY (32 bytes ed25519 em hex). [Config.Operators] é um mapa
// id→pubkey PLANO, pelo que a env plana o representa sem perda: não se inventa um formato
// novo para uma forma que o projecto já sabe exprimir.
//
// FAIL-CLOSED e sem degradação silenciosa:
//
//   - VAZIO (ou só espaços) ⇒ (nil, nil): "não configurado". O canal fica em DEFAULT-DENY
//     (nenhum sinal autentica) e — desde AOS-193 — o bind não-loopback é RECUSADO. É um
//     estado válido e declarado, não um erro.
//   - segmentos VAZIOS entre vírgulas (ex.: vírgula final) são tolerados (ruído de formatação).
//   - entrada sem '=', emitterID vazio, pubkey vazia/não-hex/de tamanho != 32 bytes, ou
//     emitterID DUPLICADO ⇒ [ErrBadOperators]: ABORTA. Nenhum destes casos pode degradar para
//     "regista os que der": um operador em falta significa que TODOS os seus sinais serão
//     recusados com ErrUnknownEmitter num nó que arrancou a anunciar um canal de controlo.
//   - dois emitterIDs DIFERENTES com a MESMA pubkey ⇒ [ErrBadOperators]: ABORTA. O steer não
//     tem invariante de distinção (ao contrário do 4-eyes), pelo que a partilha não eleva
//     privilégio — DESTRÓI A ATRIBUIÇÃO: um `aos steer --emitter ops:bob` assinado pela chave
//     de `ops:alice` seria aceite e SELADO NO WORM como sendo de `ops:bob`, e o nome do emissor
//     deixaria de ser evidência. É o mesmo conflito de autoridade do emitterID duplicado,
//     invertido (uma chave, dois nomes), e a mesma resposta: abortar, não escolher por nós.
//
// SÓ PUBKEYS. Nunca se aceita nem se lê material privado por esta porta; a chave privada do
// operador assina no processo DO OPERADOR (`aos steer`, cli.go).
func parseOperators(s string) (map[string]ed25519.PublicKey, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil // não configurado ⇒ default-deny deliberado (não é um erro).
	}
	out := make(map[string]ed25519.PublicKey)
	// seenKey indexa o material PÚBLICO já visto (hex da pubkey → emitterID que o trouxe) para
	// apanhar a colisão de chave entre emitterIDs distintos. É um índice de material público
	// mantido em memória durante o parse — nada dele é logado (ver o erro abaixo).
	seenKey := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		if strings.TrimSpace(pair) == "" {
			continue // segmento vazio (ex.: vírgula final) ⇒ ruído tolerável, não um typo.
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: entrada %q sem '=' (esperado emitterID=hexpubkey)", ErrBadOperators, strings.TrimSpace(pair))
		}
		id, rawKey := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if id == "" {
			return nil, fmt.Errorf("%w: entrada %q com emitterID vazio", ErrBadOperators, strings.TrimSpace(pair))
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("%w: emitterID %q duplicado (conflito de autoridade — o ultimo NAO ganha)", ErrBadOperators, id)
		}
		pub, err := parseEd25519PubHex(rawKey)
		if err != nil {
			// A pubkey NUNCA é ecoada no erro (é público, mas ecoá-la enche os logs de
			// material de chave sem necessidade): identifica-se a entrada pelo emitterID.
			return nil, fmt.Errorf("%w: emitterID %q com pubkey invalida (esperado 64 hex chars = 32 bytes ed25519)", ErrBadOperators, id)
		}
		fp := hex.EncodeToString(pub)
		if other, dup := seenKey[fp]; dup {
			// A pubkey NÃO é ecoada: a entrada é identificada pelos DOIS emitterIDs em conflito.
			return nil, fmt.Errorf("%w: emitterIDs %q e %q partilham a MESMA pubkey (a atribuicao do steer deixaria de distinguir quem emitiu o sinal)", ErrBadOperators, other, id)
		}
		seenKey[fp] = id
		out[id] = pub
	}
	if len(out) == 0 {
		// Só houve segmentos vazios (ex.: ",," ou ", ") — configurado mas sem entrada válida.
		return nil, fmt.Errorf("%w: nenhuma entrada valida em %q", ErrBadOperators, s)
	}
	return out, nil
}

// parseRatifiers interpreta AOS_RATIFIERS (AOS-206) — o registo principal→PUBKEY dos
// ratificadores de PRODUÇÃO autorizados a ratificar a promoção de um artefacto de
// auto-modificação (skill/memória procedural, AOS-096) — na forma
// "principal=hexpubkey,principal2=hexpubkey2".
//
// FORMATO: é DELIBERADAMENTE a gramática de [parseOperators] (mesmo separador de entradas, mesmo
// separador chave/valor, mesma tolerância a segmentos vazios) com o valor codificado como em
// AOS_ISSUER_PUBKEY (32 bytes ed25519 em hex). Ao contrário dos aprovadores do FourEyes — que
// trazem uma autoridade []string por entrada e por isso vão num ficheiro JSON montado — o
// ratificador tem uma autoridade FIXA ("ratify:production", [hitl.DefaultRatifyAuthority]), pelo
// que não há nada a variar por entrada e uma env plana representa o roster sem perda.
//
// FAIL-CLOSED e sem degradação silenciosa (idêntico a [parseOperators]):
//
//   - VAZIO (ou só espaços) ⇒ (nil, nil): "não configurado". O promotion controller é composto na
//     mesma (via sancionada, anti-replay forçado) mas SEM ratificadores ⇒ toda a promoção é
//     NEGADA (ratifier_unknown). É um estado válido e declarado no banner, não um erro.
//   - segmentos VAZIOS entre vírgulas (ex.: vírgula final) são tolerados (ruído de formatação).
//   - entrada sem '=', principal vazio, pubkey vazia/não-hex/de tamanho != 32 bytes, ou principal
//     DUPLICADO ⇒ [ErrBadRatifiers]: ABORTA (o último NÃO ganha — é conflito de autoridade).
//   - dois principals DIFERENTES com a MESMA pubkey ⇒ [ErrBadRatifiers]: ABORTA. UMA chave privada
//     satisfaria o não-repúdio de dois nomes, destruindo a atribuição da ratificação assinada.
//
// SÓ PUBKEYS. A chave PRIVADA do ratificador assina FORA do nó (no dispositivo do humano) e NUNCA
// entra aqui.
func parseRatifiers(s string) ([]RatifierConfig, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil // não configurado ⇒ controller composto mas sem ratificadores (não é um erro).
	}
	var out []RatifierConfig
	seenPrincipal := make(map[string]struct{})
	seenKey := make(map[string]string) // hex da pubkey → principal que a trouxe (colisão de chave).
	for _, pair := range strings.Split(s, ",") {
		if strings.TrimSpace(pair) == "" {
			continue // segmento vazio (ex.: vírgula final) ⇒ ruído tolerável, não um typo.
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: entrada %q sem '=' (esperado principal=hexpubkey)", ErrBadRatifiers, strings.TrimSpace(pair))
		}
		principal, rawKey := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if principal == "" {
			return nil, fmt.Errorf("%w: entrada %q com principal vazio", ErrBadRatifiers, strings.TrimSpace(pair))
		}
		if _, dup := seenPrincipal[principal]; dup {
			return nil, fmt.Errorf("%w: principal %q duplicado (conflito de autoridade — o ultimo NAO ganha)", ErrBadRatifiers, principal)
		}
		pub, err := parseEd25519PubHex(rawKey)
		if err != nil {
			// A pubkey NÃO é ecoada no erro: identifica-se a entrada pelo principal.
			return nil, fmt.Errorf("%w: principal %q com pubkey invalida (esperado 64 hex chars = 32 bytes ed25519)", ErrBadRatifiers, principal)
		}
		fp := hex.EncodeToString(pub)
		if other, dup := seenKey[fp]; dup {
			return nil, fmt.Errorf("%w: principals %q e %q partilham a MESMA pubkey (o nao-repudio da ratificacao deixaria de distinguir quem ratificou)", ErrBadRatifiers, other, principal)
		}
		seenKey[fp] = principal
		seenPrincipal[principal] = struct{}{}
		out = append(out, RatifierConfig{Principal: principal, PubKey: pub})
	}
	if len(out) == 0 {
		// Só houve segmentos vazios (ex.: ",," ou ", ") — configurado mas sem entrada válida.
		return nil, fmt.Errorf("%w: nenhuma entrada valida em %q", ErrBadRatifiers, s)
	}
	return out, nil
}

// approversDoc é o esquema do ficheiro de aprovadores (AOS_APPROVERS_FILE, AOS-193). Só
// material PÚBLICO: principal, pubkey ed25519 em hex e a autoridade autoritativa
// (capabilities "approve:<classe>" que o principal detém). A chave PRIVADA do aprovador vive
// no dispositivo do humano e nunca aqui (AOS-162).
type approversDoc struct {
	Approvers []approverDoc `json:"approvers"`
}

// approverDoc é UMA entrada do ficheiro de aprovadores. O campo `devices` (AOS-266) é OPCIONAL e
// só é consumido pelo caminho de ENROLLMENT ([parseDeviceEnrollmentFile]) — [parseApproversFile]
// ignora-o (o roster de pubkeys e a atribuição de dispositivos são preocupações separadas, lidas
// do MESMO ficheiro por omissão). Cada device_id é o digest OPACO base64 que o componente de
// attestation devolve em /verify; NÃO é segredo.
type approverDoc struct {
	Principal string   `json:"principal"`
	PubKey    string   `json:"pubkey"`            // 64 hex chars = 32 bytes ed25519 (== AOS_ISSUER_PUBKEY)
	Authority []string `json:"authority"`         // ex.: ["approve:danger", "approve:gray"]
	Devices   []string `json:"devices,omitempty"` // AOS-266: device_ids opacos em base64 (atribuição)
}

// parseApproversFile lê o ficheiro MONTADO de aprovadores do FourEyesGate (AOS-162) apontado
// por AOS_APPROVERS_FILE (AOS-193).
//
// PORQUÊ UM FICHEIRO E NÃO UMA ENV (decisão, não omissão). [Config.Operators] é um mapa
// id→escalar e cabe sem perda numa env plana (ver [parseOperators]); [ApproverConfig] NÃO é
// escalar — traz `Authority []string` por entrada. Espremê-lo numa env exigiria um TERCEIRO
// nível de delimitador ("principal=hex;cap1;cap2,principal2=..."), uma gramática que o projecto
// não usa em lado nenhum, ilegível a partir de 2 capabilities e sem forma de ser revista em
// code-review. Um ficheiro JSON montado é a via que o ADR-017 ponto 2 já prevê ("Config por
// env/ficheiro montado"), é versionável e é exactamente o artefacto que uma roster de
// aprovadores de dual-control deve ser. A COERÊNCIA com [parseOperators] mantém-se onde importa:
// a MESMA codificação hex do material público (a de AOS_ISSUER_PUBKEY), a MESMA disciplina
// fail-closed (malformado/duplicado/vazio ABORTA) e o MESMO invariante (só pubkeys entram).
// Cada colaborador tem UM caminho de configuração — não há precedência env-vs-ficheiro a
// divergir em silêncio.
//
// FAIL-CLOSED:
//
//   - path VAZIO ⇒ (nil, nil): four-eyes NÃO configurado; o gate não é composto e
//     POST /runs/{id}/approve devolve 501 (desligado por declaração).
//   - ficheiro ilegível, JSON malformado ou com campos DESCONHECIDOS ⇒ [ErrBadApproversFile]
//     (o `DisallowUnknownFields` apanha um esquema em drift — ex.: "pub_key" — em vez de o
//     ignorar e compor um gate incompleto; é a mesma disciplina do decodeJSON da API).
//   - lista VAZIA, principal vazio, principal DUPLICADO, pubkey inválida, ou autoridade vazia
//     ⇒ ABORTA. A autoridade vazia é rejeitada porque um aprovador sem NENHUMA capability
//     `approve:<classe>` nunca satisfaz [hitl.RequiredAuthority]: seria uma entrada que parece
//     configurada e nunca aprova nada — a degradação silenciosa que este ticket vem fechar.
//   - capability FORA do vocabulário fechado ("approve:safe"|"approve:gray"|"approve:danger",
//     ver [approveCapabilities]) ⇒ ABORTA. hitl.RequiredAuthority só produz estes três valores e
//     a comparação em integration.hasAuthority é de string EXACTA (sem wildcards): um typo
//     ("approve:dangerous", "approve:*") daria exactamente o estado anterior — uma entrada que
//     parece configurada, contada no banner, e que nunca aprova nada.
//   - duas entradas com a MESMA PUBKEY (mesmo com principals diferentes) ⇒ ABORTA. Esta é a
//     guarda de SEGURANÇA do ficheiro, não de higiene: a distinção estrutural do dual-control
//     (integration.FourEyesGate.authorizeDual) compara approver/session/credential, TRÊS
//     strings ESCOLHIDAS PELO CLIENTE na perna; a única âncora criptográfica de "duas pessoas"
//     é a pubkey PINADA. Com a mesma pubkey em duas linhas, UMA só chave privada assina as duas
//     pernas e o 4-eyes é anulado EM SILÊNCIO, com o banner a declarar "2 aprovador(es)
//     pinados". Antes de AOS-193 esse roster exigia forkar e recompilar; agora é um ficheiro
//     montado, e um copy-paste é suficiente para o produzir.
func parseApproversFile(path string) ([]ApproverConfig, error) {
	if path == "" {
		return nil, nil // não configurado ⇒ four-eyes desligado por declaração (não é um erro).
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ler %q: %v", ErrBadApproversFile, path, err)
	}
	var doc approversDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrBadApproversFile, path, err)
	}
	if len(doc.Approvers) == 0 {
		return nil, fmt.Errorf("%w: %q sem aprovadores (um ficheiro configurado e vazio deixaria o four-eyes DESLIGADO em silencio)", ErrBadApproversFile, path)
	}
	out := make([]ApproverConfig, 0, len(doc.Approvers))
	seen := make(map[string]struct{}, len(doc.Approvers))
	// seenKey: hex do material PÚBLICO → principal que o trouxe (ver a guarda de colisão abaixo).
	seenKey := make(map[string]string, len(doc.Approvers))
	for i, a := range doc.Approvers {
		principal := strings.TrimSpace(a.Principal)
		if principal == "" {
			return nil, fmt.Errorf("%w: %q entrada #%d com principal vazio", ErrBadApproversFile, path, i)
		}
		if _, dup := seen[principal]; dup {
			return nil, fmt.Errorf("%w: %q principal %q duplicado (conflito de autoridade — o ultimo NAO ganha)", ErrBadApproversFile, path, principal)
		}
		seen[principal] = struct{}{}
		pub, perr := parseEd25519PubHex(strings.TrimSpace(a.PubKey))
		if perr != nil {
			return nil, fmt.Errorf("%w: %q principal %q com pubkey invalida (esperado 64 hex chars = 32 bytes ed25519)", ErrBadApproversFile, path, principal)
		}
		if other, dup := seenKey[hex.EncodeToString(pub)]; dup {
			// A pubkey NÃO é ecoada: identificam-se os DOIS principals que a partilham.
			return nil, fmt.Errorf("%w: %q principals %q e %q partilham a MESMA pubkey — UMA chave privada assinaria as DUAS pernas e o dual-control (AOS-162) ficaria anulado", ErrBadApproversFile, path, other, principal)
		}
		seenKey[hex.EncodeToString(pub)] = principal
		authority := splitTrimmed(a.Authority)
		if len(authority) == 0 {
			return nil, fmt.Errorf("%w: %q principal %q sem autoridade (um aprovador sem capability approve:<classe> nunca autoriza nada)", ErrBadApproversFile, path, principal)
		}
		for _, capability := range authority {
			if !isApproveCapability(capability) {
				return nil, fmt.Errorf("%w: %q principal %q com capability %q desconhecida (vocabulario fechado: %s — a comparacao e de string EXACTA, sem wildcards)",
					ErrBadApproversFile, path, principal, capability, strings.Join(approveCapabilities(), ", "))
			}
		}
		out = append(out, ApproverConfig{Principal: principal, PubKey: pub, Authority: authority})
	}
	return out, nil
}

// parseChallengeIssuance lê AOS_CHALLENGE_ISSUANCE (AOS-266) com a MESMA gramática booleana
// fail-closed de [parseDurableExecution]: vazio ⇒ desligado; um valor não-reconhecido ABORTA
// (nunca degrada em silêncio para "frescura desligada", que reabriria o replay).
func parseChallengeIssuance(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return false, nil
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%w: valor %q", ErrBadChallengeIssuance, strings.TrimSpace(s))
	}
}

// parseDeviceEnrollmentFile lê o registo de ATRIBUIÇÃO dispositivo↔aprovador (AOS-266) de um
// ficheiro montado, no MESMO esquema do ficheiro de aprovadores ({"approvers":[...]}), mas
// consumindo APENAS `principal` + `devices`. Devolve o mapa aprovador → device_ids (bytes) que
// alimenta [integration.NewStaticDeviceEnrollment].
//
// `explicit` distingue o ficheiro EXPLICITAMENTE apontado (AOS_DEVICE_ENROLLMENT_FILE) do herdado
// por omissão (AOS_APPROVERS_FILE):
//
//   - path VAZIO ⇒ (nil, nil): sem enrollment (atribuição dormente).
//   - ficheiro ilegível / JSON malformado / campo desconhecido / principal vazio / device_id
//     não-base64 ⇒ [ErrBadDeviceEnrollmentFile] (fail-loud, nunca descartar em silêncio).
//   - SEM nenhum dispositivo: se `explicit`, ABORTA (apontou-se um ficheiro de enrollment que não
//     enrola nada — a degradação silenciosa que AOS-193 fecha); se herdado por omissão, devolve
//     (nil, nil) — o caso comum de um roster de aprovadores ainda sem dispositivos enrolados.
//
// Entradas de aprovador SEM `devices` são simplesmente saltadas (quando o ficheiro é o de
// aprovadores, a maioria não tem dispositivos). device_ids duplicados no mesmo aprovador são
// idempotentes ([integration.NewStaticDeviceEnrollment] usa um conjunto).
func parseDeviceEnrollmentFile(path string, explicit bool) (map[string][][]byte, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ler %q: %v", ErrBadDeviceEnrollmentFile, path, err)
	}
	var doc approversDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrBadDeviceEnrollmentFile, path, err)
	}
	out := make(map[string][][]byte)
	for i, a := range doc.Approvers {
		if len(a.Devices) == 0 {
			continue
		}
		principal := strings.TrimSpace(a.Principal)
		if principal == "" {
			return nil, fmt.Errorf("%w: %q entrada #%d com principal vazio mas com dispositivos", ErrBadDeviceEnrollmentFile, path, i)
		}
		for _, d := range a.Devices {
			ds := strings.TrimSpace(d)
			if ds == "" {
				continue
			}
			// O device_id NÃO é ecoado no erro (é opaco mas evita-se expor identificadores).
			id, derr := base64.StdEncoding.DecodeString(ds)
			if derr != nil || len(id) == 0 {
				return nil, fmt.Errorf("%w: %q principal %q com device_id nao-base64 utilizavel", ErrBadDeviceEnrollmentFile, path, principal)
			}
			out[principal] = append(out[principal], id)
		}
	}
	if len(out) == 0 {
		if explicit {
			return nil, fmt.Errorf("%w: %q apontado explicitamente mas sem dispositivos (um ficheiro de enrollment que nao enrola nada deixaria a atribuicao DESLIGADA em silencio)", ErrBadDeviceEnrollmentFile, path)
		}
		return nil, nil // herdado do AOS_APPROVERS_FILE sem dispositivos ⇒ atribuição dormente.
	}
	return out, nil
}

// splitTrimmed normaliza uma lista de strings: remove espaços e descarta as vazias. Devolve
// nil quando não sobra nenhuma (⇒ o chamador trata "configurado mas vazio" fail-closed).
func splitTrimmed(in []string) []string {
	var out []string
	for _, v := range in {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// splitCSV divide uma lista separada por vírgulas, descartando vazios e espaços.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
