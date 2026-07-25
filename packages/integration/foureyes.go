package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
)

// Este ficheiro FIXA A INVARIANTE ESTRUTURAL DO 4-EYES REAL (AOS-162, ADR-016 §4). Uma
// acção IRREVERSÍVEL (DualControlRequired) exige DUAS aprovações autenticadas que sejam de
// (principal, sessão, credencial) TODOS DISTINTOS. O gate RECUSA fail-closed se a 2.ª
// assinatura for do MESMO PRINCIPAL (auto-aprovação), da MESMA SESSÃO, ou da MESMA
// CREDENCIAL — cada colisão tem o SEU erro dedicado. Reforça, na fronteira de composição, o
// que o [approvalcard.DualControlCollector] já garante ao nível do quórum (approver_1 !=
// approver_2): aqui a distinção é sobre os TRÊS eixos estruturais, não só o principal.
//
// CADA PERNA TRAZ O SEU CHALLENGE. A assinatura ed25519 do aprovador cobre o tuplo canónico
//
//	(request_id ‖ preview_ou_digest ‖ classe_risco ‖ dual_required ‖ approver ‖ session ‖ credential ‖ challenge)
//
// com a codificação INJECTIVA length-prefix de AOS-160 (ver [appendLenPrefixed]) — nenhum
// deslize de fronteira entre campos variáveis produz duas tuplas distintas com os mesmos
// bytes. A CLASSE DE RISCO e o bit DUAL_REQUIRED entram no tuplo (não são parâmetros
// livres do chamador): uma perna assinada para uma acção IRREVERSÍVEL (dual_required=1)
// NÃO valida quando um relay a re-submete como single-approval (dual_required=0) — o
// mismatch de assinatura fecha o DOWNGRADE dual→single tal como o preview fecha o WYSIWYS
// (ADR-016 §4: o BFF non-signing não pode rebaixar a exigência que o aprovador assinou). Um
// challenge reutilizado (perna-replay, ou dois legs a partilhar o mesmo challenge)
// é recusado pelo consumo durável do [NonceConsumer] (o mesmo de AOS-159). A verificação é
// contra a PUBKEY PINADA do aprovador ([hitl.ApproverRegistry]); o gate NUNCA detém a chave
// privada (non-signing, ADR-016 §1) — a assinatura vem da credencial do aprovador, produzida
// FORA deste processo (ver [SignFourEyesLeg], helper de teste/emissor).
//
// WYSIWYS (ADR-016 §2): o gate reconstrói o tuplo com o req.Preview AUTORITATIVO (o efeito
// que vai executar) e a pubkey do aprovador; se uma perna assinou um preview DIFERENTE do
// exibido, a assinatura não valida. Ambas as pernas assinam O MESMO preview — o que o humano
// viu.
//
// AUTORIDADE POR-PERNA (approve:<classe>). O gate NÃO se contenta com "principal autêntico":
// cada perna tem de deter a CAPABILITY de aprovação da classe da acção. A autoridade
// autoritativa devolvida por [hitl.ApproverRegistry.Lookup] tem de conter
// [hitl.RequiredAuthority](req.RiskClass) — a expressão concreta do "utilizador ∩ classe".
// Sem ela, dois principais quaisquer (inclusive NHIs registadas para outro fim, sem qualquer
// capability de aprovação) satisfariam o 4-eyes de um irreversível. Falha ⇒ [ErrInsufficientAuthority]
// fail-closed. Assim o gate é uma decisão de autorização COMPLETA por si (não depende de o
// [hitl.Channel] a montante ter feito o enforcement de autoridade), embora componha bem com ele.
//
// EMISSÃO SERVER-SIDE DO CHALLENGE — PORTA OPCIONAL [ChallengeIssuance] (remediação AOS-177).
// O consumo durável (acima) dá DEDUP, não FRESCURA: um challenge escolhido pelo cliente é
// aceite desde que nunca visto NAQUELE scope, e o scope é por-request_id. Com
// [WithChallengeIssuance] ligada, o challenge de CADA perna tem de constar do REGISTO DE
// EMISSÃO do servidor para (pedido, aprovador) e estar dentro do TTL — só então é consumido.
// É o que impede reapresentar indefinidamente uma prova (incluindo uma attestation WebAuthn
// capturada) em pedidos novos. Ver [ChallengeIssuance] para o modelo de ataque concreto.
//
// ATTESTATION DE DISPOSITIVO — REAL, POR PORTA OPCIONAL (AOS-177, D4 Opção A). O ADR-016 §4
// exige "duas credenciais WebAuthn ATESTADAS E DISTINTAS": a distinção por igualdade de
// string (acima) é NECESSÁRIA MAS INSUFICIENTE. O gate aceita a porta
// [DeviceAttestationVerifier] ([WithDeviceAttestation]) e, para a ATRIBUIÇÃO ao aprovador, a
// porta [DeviceEnrollment] ([WithDeviceEnrollment]):
//
//   - PORTA LIGADA ⇒ cada perna TEM de trazer attestationObject + clientDataJSON WebAuthn
//     ([ApprovalLeg.DeviceAttestation], [ApprovalLeg.DeviceClientData]); a attestation é
//     VERIFICADA (formato/AAGUID na allowlist/cadeia x509/assinatura/flags/origin/rpId) e o
//     challenge do clientDataJSON tem de ser O DA PERNA. As duas pernas têm de apresentar
//     CREDENCIAIS WebAuthn ATESTADAS DISTINTAS ([ErrSameDevice]), ALÉM dos três eixos
//     estruturais. Attestation ausente ou inválida ⇒ a perna é RECUSADA (fail-closed), nunca
//     degradada para o modo estrutural. NOTA DE ALCANCE: [ErrSameDevice] NÃO prova dois
//     autenticadores FÍSICOS distintos (o AAGUID é de MODELO e a attestation é de LOTE, por
//     desenho de privacidade do WebAuthn) — ver [ErrSameDevice].
//   - COM [WithDeviceEnrollment] ⇒ o dispositivo atestado tem ainda de estar REGISTADO para o
//     APROVADOR nomeado na perna ([ErrDeviceNotEnrolled]); é isso que dá ATRIBUIÇÃO. Sem
//     enrollment, qualquer autenticador de um modelo permitido serve qualquer perna.
//   - PORTA AUSENTE ⇒ comportamento ESTRUTURAL de sempre, byte-a-byte retro-compatível: o
//     campo é transportado mas não verificado (é o STUB histórico). A implementação real
//     precisa de um descodificador CBOR, que a Carta (emenda 1.3) confina ao componente de
//     autoridade EXTERNO para o nó ficar zero-dep; o nó liga-se a esse componente pelo
//     adaptador HTTP stdlib [NewRemoteDeviceAttestationVerifier] (ou corre no modo
//     estrutural, se não houver componente configurado). Ver packages/platform/attestation.
//
// A attestation NÃO entra no tuplo assinado ed25519 (o binding faz-se pelo challenge, já lá
// dentro), pelo que ligar a porta não invalida assinaturas nem muda o formato da perna.

// Erros do gate 4-eyes (todos fail-closed). Cada colisão estrutural tem o seu erro dedicado
// para ser atribuível no audit e não-vacuosa nos testes.
var (
	// ErrNoApproverRegistry — construção sem [hitl.ApproverRegistry]. Sem a porta de
	// identidade não há pubkey pinada contra a qual verificar; recusado fail-closed.
	ErrNoApproverRegistry = errors.New("integration: four-eyes gate exige um ApproverRegistry (pubkeys pinadas)")
	// ErrNoChallengeStore — construção sem [NonceConsumer] durável. Sem ele não há
	// anti-replay de challenges por-perna; recusado fail-closed.
	ErrNoChallengeStore = errors.New("integration: four-eyes gate exige um nonce-store durável para os challenges")
	// ErrNilFourEyesOption — [NewFourEyesGate] recebeu uma [FourEyesOption] nil. Aborta a
	// construção em vez de a ignorar: uma opção perdida é verificação perdida.
	ErrNilFourEyesOption = errors.New("integration: four-eyes: opção de construção nil (fail-closed)")
	// ErrMissingRequestID — o pedido não traz request_id (âncora do challenge e do tuplo).
	ErrMissingRequestID = errors.New("integration: four-eyes: request_id em falta")
	// ErrMissingPreview — o pedido não traz preview/digest do efeito exibido (WYSIWYS).
	ErrMissingPreview = errors.New("integration: four-eyes: preview/digest em falta (WYSIWYS)")
	// ErrWrongLegCount — número de pernas errado: uma acção irreversível exige EXACTAMENTE
	// 2 aprovações; uma reversível exige EXACTAMENTE 1.
	ErrWrongLegCount = errors.New("integration: four-eyes: número de aprovações errado (irreversível=2, reversível=1)")
	// ErrInvalidLeg — perna com campo obrigatório em falta (approver/session/credential/
	// challenge/signature).
	ErrInvalidLeg = errors.New("integration: four-eyes: aprovação inválida (campo obrigatório em falta)")
	// ErrChallengeTooShort — o challenge da perna tem menos de [MinNonceLen] bytes.
	ErrChallengeTooShort = errors.New("integration: four-eyes: challenge demasiado curto (mínimo MinNonceLen bytes)")
	// ErrUnknownApprover — o aprovador não tem pubkey pinada no registo (default-deny).
	ErrUnknownApprover = errors.New("integration: four-eyes: aprovador desconhecido (sem pubkey pinada)")
	// ErrRegistryBackend — erro do backend do ApproverRegistry; tratado fail-closed.
	ErrRegistryBackend = errors.New("integration: four-eyes: falha do ApproverRegistry (fail-closed)")
	// ErrBadLegSignature — a assinatura ed25519 da perna não valida sobre o tuplo canónico.
	ErrBadLegSignature = errors.New("integration: four-eyes: assinatura ed25519 da aprovação inválida")
	// ErrInsufficientAuthority — o aprovador é autêntico mas a sua autoridade autoritativa
	// NÃO contém a capability approve:<classe> exigida pela classe de risco da acção. A
	// autenticidade não basta: a autoridade tem de cobrir a classe que está a aprovar.
	ErrInsufficientAuthority = errors.New("integration: four-eyes: aprovador sem autoridade approve:<classe> para a classe da acção")
	// ErrSamePrincipal — AUTO-APROVAÇÃO: a 2.ª aprovação é do MESMO principal que a 1.ª.
	ErrSamePrincipal = errors.New("integration: four-eyes: auto-aprovação recusada (mesmo principal nas duas pernas)")
	// ErrSameSession — a 2.ª aprovação vem da MESMA sessão viva que a 1.ª.
	ErrSameSession = errors.New("integration: four-eyes: mesma sessão nas duas pernas (recusado)")
	// ErrSameCredential — a 2.ª aprovação usa a MESMA credencial que a 1.ª (mesmo humano/
	// dispositivo a assinar duas vezes).
	ErrSameCredential = errors.New("integration: four-eyes: mesma credencial nas duas pernas (recusado)")
	// ErrReplayedChallenge — o challenge da perna já foi consumido: perna-replay, ou as duas
	// pernas partilham o mesmo challenge.
	ErrReplayedChallenge = errors.New("integration: four-eyes: challenge replayado (já consumido)")
	// ErrChallengeStoreBackend — erro do backend do nonce-store; tratado fail-closed.
	ErrChallengeStoreBackend = errors.New("integration: four-eyes: falha do nonce-store (fail-closed)")
)

// fourEyesDomain é o separador de domínio versionado prefixado ao tuplo assinado de uma
// perna, para que uma assinatura 4-eyes nunca colida com a de outro subsistema (sinal de
// steer, token NHI, decisão HITL) nem entre versões do formato.
const fourEyesDomain = "aos.integration.foureyes.v1"

// FourEyesRequest descreve a acção irreversível (ou reversível) a autorizar. O Preview é o
// digest canónico do efeito EXIBIDO ao(s) humano(s) — a base do WYSIWYS: a assinatura de
// cada perna cobre-o, e o gate reconstrói o tuplo com ESTE valor (o que vai executar), pelo
// que uma perna que tenha assinado outro preview não valida.
type FourEyesRequest struct {
	// RequestID liga a decisão ao pedido concreto e é o scope do anti-replay dos challenges.
	RequestID string
	// Preview é o digest/preview determinista do efeito exibido (WYSIWYS). Coberto pela
	// assinatura de cada perna.
	Preview []byte
	// RiskClass é a CLASSE DE RISCO autoritativa da acção ([risk.Class]). Determina a
	// capability approve:<classe> exigida a CADA perna (via [hitl.RequiredAuthority]) e
	// ENTRA no tuplo assinado, pelo que não é rebaixável por um relay. Valor-zero =
	// [risk.ClassDanger] (fail-closed: uma classe não computada trata-se como o pior caso,
	// exigindo approve:danger).
	RiskClass risk.Class
	// DualControlRequired marca a acção como IRREVERSÍVEL (danger/não-desfazível) ⇒ exige
	// DUAS aprovações estruturalmente distintas. false ⇒ reversível ⇒ 1 aprovação basta.
	// Amarrado ao RISCO INTRÍNSECO, não ao mode do tiering (ADR-016 §4). ENTRA no tuplo
	// assinado por cada perna: uma perna assinada com dual_required=true não valida se um
	// relay a re-submeter com dual_required=false (fecha o downgrade dual→single).
	DualControlRequired bool
}

// ApprovalLeg é UMA perna (aprovação) do 4-eyes: a decisão assinada de um aprovador sobre o
// pedido, com o SEU challenge por-perna. A chave privada NUNCA entra aqui — só a assinatura,
// produzida pela credencial do aprovador fora deste processo (ADR-016 §1, non-signing).
type ApprovalLeg struct {
	// Approver é o PRINCIPAL que decide (a NHI cuja pubkey pinada verifica a assinatura).
	// Eixo 1 da distinção estrutural.
	Approver string
	// Session é o identificador da SESSÃO viva do aprovador. Eixo 2 da distinção: duas
	// pernas da mesma sessão não constituem 4-eyes.
	Session string
	// Credential é o identificador da CREDENCIAL (credential-id WebAuthn, etc.) que assinou.
	// Eixo 3 da distinção: a mesma credencial não pode assinar as duas pernas.
	Credential string
	// Challenge é o nonce/challenge POR-PERNA, ligado ao pedido pelo scope (request_id) e
	// coberto pela assinatura. Duas garantias, com portas distintas:
	//
	//   - SEM [WithChallengeIssuance]: só ANTI-REPLAY de uso-único durável (dedup) — o MESMO
	//     challenge não se re-verifica no mesmo pedido. Como o valor é escolhido pelo CLIENTE
	//     e o scope é por-request_id, isto NÃO prova liveness nem impede pré-computação
	//     offline por quem detenha a credencial: um request_id novo reabre o espaço de
	//     challenges. O que limita o alcance é o (request_id, preview, classe) no tuplo.
	//   - COM [WithChallengeIssuance]: issue-then-consume REAL — o challenge tem de constar do
	//     registo de EMISSÃO do servidor para (pedido, aprovador) e estar dentro do TTL, e só
	//     depois é consumido. É este modo que dá frescura por-cerimónia.
	//
	// >= [MinNonceLen] bytes.
	Challenge []byte
	// DeviceAttestation é o attestationObject WebAuthn (CBOR: fmt/attStmt/authData) do
	// dispositivo que assinou esta perna.
	//
	// Só é VERIFICADO se o gate tiver a porta [DeviceAttestationVerifier] ligada
	// ([WithDeviceAttestation]) — nesse caso é obrigatório e a sua recusa recusa a perna
	// (AOS-177). SEM a porta mantém-se o comportamento histórico: transportado, não
	// verificado (STUB) — o modo em que o binário zero-dep do nó corre. NÃO entra no tuplo
	// assinado: o binding à perna faz-se pelo challenge, que já está no tuplo.
	DeviceAttestation []byte
	// DeviceClientData são os bytes EXACTOS do clientDataJSON WebAuthn correspondentes à
	// [ApprovalLeg.DeviceAttestation] (o seu hash é coberto pela assinatura de attestation).
	// Tem de conter o [ApprovalLeg.Challenge] desta perna — é isso que liga a attestation à
	// perna. Obrigatório quando a porta de attestation está ligada; ignorado sem ela.
	DeviceClientData []byte
	// Signature é a assinatura ed25519 do aprovador sobre o tuplo canónico da perna.
	Signature []byte
}

// FourEyesDecision é o veredicto do gate. O gate NÃO assina/sela (non-signing) — só compõe
// a decisão a partir das pernas verificadas. Approvers lista os principais DISTINTOS que
// aprovaram (1 para reversível, 2 para dual-control; vazio se negado).
type FourEyesDecision struct {
	// Authorized é a decisão final fail-closed.
	Authorized bool
	// Approvers são os principais distintos que aprovaram (sem segredos).
	Approvers []string
	// Reason descreve o desfecho (sem segredos).
	Reason string
}

// FourEyesGate impõe a invariante estrutural do 4-eyes. Detém APENAS a porta de pubkeys
// pinadas ([hitl.ApproverRegistry]) e o nonce-store durável dos challenges — NUNCA chaves
// privadas de aprovadores. Seguro para uso concorrente na medida em que os colaboradores o
// forem. Construir com [NewFourEyesGate].
type FourEyesGate struct {
	registry   hitl.ApproverRegistry
	challenges NonceConsumer
	// devices é a porta OPCIONAL de attestation de dispositivo (AOS-177). nil ⇒ modo
	// estrutural histórico (retro-compatível); não-nil ⇒ attestation exigida e verificada
	// em CADA perna, e credenciais atestadas DISTINTAS no dual-control.
	devices DeviceAttestationVerifier
	// enrollment é a porta OPCIONAL de ATRIBUIÇÃO dispositivo↔aprovador. Só é utilizável com
	// `devices` ligada (validado na construção). nil ⇒ a attestation prova modelo e posse,
	// não prova de QUEM é o dispositivo.
	enrollment DeviceEnrollment
	// issuance é a porta OPCIONAL do registo de challenges EMITIDOS pelo servidor. nil ⇒ o
	// challenge só tem de ser INÉDITO (dedup, sem frescura); não-nil ⇒ tem de ter sido
	// EMITIDO para (pedido, aprovador) e estar dentro do TTL.
	issuance ChallengeIssuance
}

// FourEyesOption configura capacidades OPCIONAIS do gate na construção. Uma opção que não
// possa ser honrada devolve erro e ABORTA a construção — nunca se degrada em silêncio para
// menos verificação do que o chamador pediu.
type FourEyesOption func(*FourEyesGate) error

// WithDeviceAttestation LIGA a verificação real de attestation de dispositivo (ADR-016 §4).
// Com ela, cada perna passa a ter de trazer uma attestation WebAuthn válida e as duas pernas
// de um dual-control têm de vir de dispositivos atestados DISTINTOS. Verificador nil ⇒
// [ErrNilDeviceAttestationVerifier] (fail-closed: pedir attestation e não a exigir seria
// pior que não a pedir).
func WithDeviceAttestation(v DeviceAttestationVerifier) FourEyesOption {
	return func(g *FourEyesGate) error {
		if v == nil {
			return ErrNilDeviceAttestationVerifier
		}
		g.devices = v
		return nil
	}
}

// WithDeviceEnrollment LIGA a ATRIBUIÇÃO dispositivo↔aprovador: o dispositivo atestado de cada
// perna tem de estar REGISTADO para o aprovador que a assinou ([ErrDeviceNotEnrolled],
// default-deny). Exige [WithDeviceAttestation] (sem deviceID não há o que confrontar) — a
// construção é recusada com [ErrEnrollmentWithoutAttestation] se faltar. Registo nil ⇒
// [ErrNilDeviceEnrollment].
func WithDeviceEnrollment(e DeviceEnrollment) FourEyesOption {
	return func(g *FourEyesGate) error {
		if e == nil {
			return ErrNilDeviceEnrollment
		}
		g.enrollment = e
		return nil
	}
}

// WithChallengeIssuance EXIGE que o challenge de cada perna tenha sido EMITIDO PELO SERVIDOR
// para (pedido, aprovador) e esteja dentro da validade — não bastando ser inédito. É o que
// transforma o anti-replay de DEDUP em FRESCURA por-cerimónia (ver [ChallengeIssuance]).
// Registo nil ⇒ [ErrNilChallengeIssuance] (fail-closed).
func WithChallengeIssuance(iss ChallengeIssuance) FourEyesOption {
	return func(g *FourEyesGate) error {
		if iss == nil {
			return ErrNilChallengeIssuance
		}
		g.issuance = iss
		return nil
	}
}

// NewFourEyesGate constrói o gate sobre a porta de identidade (pubkeys pinadas) e o
// nonce-store durável dos challenges. Sem registo ([ErrNoApproverRegistry]) ou sem
// nonce-store ([ErrNoChallengeStore]) é recusado fail-closed. NUNCA há segredos: só pubkeys
// (via registry) e o store de challenges. As opções variádicas (ex.:
// [WithDeviceAttestation]) são aditivas — a chamada histórica de dois argumentos mantém-se
// válida e com o comportamento de sempre.
func NewFourEyesGate(registry hitl.ApproverRegistry, challenges NonceConsumer, opts ...FourEyesOption) (*FourEyesGate, error) {
	if registry == nil {
		return nil, ErrNoApproverRegistry
	}
	if challenges == nil {
		return nil, ErrNoChallengeStore
	}
	g := &FourEyesGate{registry: registry, challenges: challenges}
	for _, opt := range opts {
		if opt == nil {
			return nil, ErrNilFourEyesOption
		}
		if err := opt(g); err != nil {
			return nil, err
		}
	}
	// COERÊNCIA DAS OPÇÕES: um enrollment sem attestation nunca seria consultado (não há
	// deviceID). Recusa-se a construção em vez de guardar uma porta inerte que daria a
	// impressão de haver atribuição.
	if g.enrollment != nil && g.devices == nil {
		return nil, ErrEnrollmentWithoutAttestation
	}
	return g, nil
}

// Authorize recolhe as aprovações e SÓ autoriza se a invariante estrutural for satisfeita.
//
//   - REVERSÍVEL (DualControlRequired=false): EXACTAMENTE 1 perna autenticada (assinatura
//     válida contra a pubkey pinada + challenge fresco). Não há distinção a fazer.
//   - IRREVERSÍVEL (DualControlRequired=true): EXACTAMENTE 2 pernas; ambas as assinaturas
//     válidas; e (principal_1 != principal_2) E (session_1 != session_2) E (credential_1 !=
//     credential_2). Qualquer colisão ⇒ recusa fail-closed com o erro dedicado. Cada
//     challenge é consumido de uso-único (perna-replay ⇒ [ErrReplayedChallenge]).
//
// Ordem fail-closed (molde de AOS-160): valida o pedido; verifica AS ASSINATURAS de todas as
// pernas ANTES de qualquer consumo de challenge (uma assinatura inválida NÃO gasta um
// challenge); aplica a distinção estrutural; só então CONSOME os challenges (o consumo, que
// é o efeito durável, fica para o fim, na senda que autorizaria). Devolve (decisão, erro):
// erro não-nil em qualquer negação.
func (g *FourEyesGate) Authorize(ctx context.Context, req FourEyesRequest, legs ...ApprovalLeg) (FourEyesDecision, error) {
	if req.RequestID == "" {
		return denied("pedido sem request_id"), ErrMissingRequestID
	}
	if len(req.Preview) == 0 {
		return denied("pedido sem preview/digest"), ErrMissingPreview
	}

	if !req.DualControlRequired {
		return g.authorizeSingle(ctx, req, legs)
	}
	return g.authorizeDual(ctx, req, legs)
}

// authorizeSingle trata a acção reversível: 1 aprovação autenticada basta.
func (g *FourEyesGate) authorizeSingle(ctx context.Context, req FourEyesRequest, legs []ApprovalLeg) (FourEyesDecision, error) {
	if len(legs) != 1 {
		return denied("reversível exige exactamente 1 aprovação"), fmt.Errorf("%w: reversível quer 1, veio %d", ErrWrongLegCount, len(legs))
	}
	if _, err := g.verifyLeg(ctx, req, legs[0]); err != nil {
		return denied("aprovação não verificada (fail-closed)"), err
	}
	if err := g.consumeChallenge(ctx, req, legs[0]); err != nil {
		return denied("challenge não consumível (fail-closed)"), err
	}
	return FourEyesDecision{Authorized: true, Approvers: []string{legs[0].Approver}, Reason: "aprovação única (acção reversível)"}, nil
}

// authorizeDual trata a acção irreversível: 2 aprovações estruturalmente distintas.
func (g *FourEyesGate) authorizeDual(ctx context.Context, req FourEyesRequest, legs []ApprovalLeg) (FourEyesDecision, error) {
	if len(legs) != 2 {
		return denied("irreversível exige exactamente 2 aprovações"), fmt.Errorf("%w: dual-control quer 2, veio %d", ErrWrongLegCount, len(legs))
	}
	a, b := legs[0], legs[1]

	// (1) Assinaturas de AMBAS as pernas (e, com a porta ligada, as ATTESTATIONS) ANTES de
	// qualquer consumo de challenge — uma perna inválida não gasta challenges.
	devA, err := g.verifyLeg(ctx, req, a)
	if err != nil {
		return denied("1.ª aprovação não verificada (fail-closed)"), err
	}
	devB, err := g.verifyLeg(ctx, req, b)
	if err != nil {
		return denied("2.ª aprovação não verificada (fail-closed)"), err
	}

	// (2) Invariante ESTRUTURAL: os três eixos têm de ser DISTINTOS. Cada colisão o seu erro.
	if a.Approver == b.Approver {
		return denied("auto-aprovação: mesmo principal"), fmt.Errorf("%w: %q", ErrSamePrincipal, a.Approver)
	}
	if a.Session == b.Session {
		return denied("mesma sessão nas duas pernas"), fmt.Errorf("%w: %q", ErrSameSession, a.Session)
	}
	if a.Credential == b.Credential {
		return denied("mesma credencial nas duas pernas"), fmt.Errorf("%w: %q", ErrSameCredential, a.Credential)
	}

	// (2b) Invariante de CREDENCIAL ATESTADA (só com a porta de attestation ligada — ADR-016
	// §4): as duas pernas não podem apresentar a MESMA credencial WebAuthn atestada. Fecha o
	// buraco que os três eixos de string não fecham: dois rótulos de credencial diferentes
	// sobre a MESMA credencial atestada não constituem 4-eyes.
	//
	// A condição é sobre a CAPACIDADE (g.devices != nil), NÃO sobre os dados. Condicioná-la a
	// `len(devA)>0 && len(devB)>0` seria fail-OPEN por forma: bastaria um refactor que
	// devolvesse um deviceID vazio sem erro para a regra se desligar em SILÊNCIO, com a
	// decisão a continuar a anunciar dispositivos distintos. Com a porta ligada, um deviceID
	// vazio NEGA.
	if g.devices != nil {
		if len(devA) == 0 || len(devB) == 0 {
			return denied("dispositivo atestado sem identificador (fail-closed)"), fmt.Errorf("%w: identificador de dispositivo vazio", ErrDeviceAttestationRejected)
		}
		if bytes.Equal(devA, devB) {
			return denied("mesma credencial atestada nas duas pernas"), ErrSameDevice
		}
	}

	// (3) Anti-replay dos challenges por-perna. Consumo durável de uso-único no scope do
	// pedido: se as duas pernas partilharem o challenge, a 2.ª consome como replay.
	if err := g.consumeChallenge(ctx, req, a); err != nil {
		return denied("challenge da 1.ª perna não consumível (fail-closed)"), err
	}
	if err := g.consumeChallenge(ctx, req, b); err != nil {
		return denied("challenge da 2.ª perna não consumível (fail-closed)"), err
	}

	reason := "dual-control: dois aprovadores de principal/sessão/credencial distintos"
	if g.devices != nil {
		// Descrição HONESTA do que ficou provado: credenciais WebAuthn atestadas distintas
		// (não autenticadores físicos distintos — ver [ErrSameDevice]).
		reason += " e credenciais WebAuthn atestadas distintas"
		if g.enrollment != nil {
			reason += ", registadas para cada aprovador"
		}
	}
	return FourEyesDecision{
		Authorized: true,
		Approvers:  []string{a.Approver, b.Approver},
		Reason:     reason,
	}, nil
}

// verifyLeg valida os campos obrigatórios da perna, VERIFICA a sua assinatura ed25519
// contra a pubkey PINADA do aprovador (sobre o tuplo que inclui a classe e o bit
// dual_required, fechando o downgrade), exige que a AUTORIDADE do aprovador cubra a classe
// da acção e — com a porta ligada — VERIFICA a attestation de dispositivo. Não consome
// challenge (isso é [consumeChallenge]). Devolve o identificador OPACO do dispositivo
// atestado (vazio se a porta não estiver ligada) para a regra de dispositivos distintos.
// Fail-closed: campo em falta ⇒ [ErrInvalidLeg]; challenge curto ⇒ [ErrChallengeTooShort];
// aprovador desconhecido ⇒ [ErrUnknownApprover]; assinatura inválida ⇒ [ErrBadLegSignature];
// sem a capability approve:<classe> ⇒ [ErrInsufficientAuthority]; attestation ausente ⇒
// [ErrMissingDeviceAttestation]; attestation recusada ⇒ [ErrDeviceAttestationRejected].
func (g *FourEyesGate) verifyLeg(ctx context.Context, req FourEyesRequest, leg ApprovalLeg) ([]byte, error) {
	if leg.Approver == "" || leg.Session == "" || leg.Credential == "" || len(leg.Challenge) == 0 || len(leg.Signature) == 0 {
		return nil, ErrInvalidLeg
	}
	if len(leg.Challenge) < MinNonceLen {
		return nil, fmt.Errorf("%w: %d < %d", ErrChallengeTooShort, len(leg.Challenge), MinNonceLen)
	}

	pub, authority, ok, err := g.registry.Lookup(ctx, leg.Approver)
	if err != nil {
		return nil, fmt.Errorf("%w: aprovador %q: %v", ErrRegistryBackend, leg.Approver, err)
	}
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: %q", ErrUnknownApprover, leg.Approver)
	}

	msg := fourEyesMessage(req.RequestID, req.Preview, req.RiskClass, req.DualControlRequired, leg.Approver, leg.Session, leg.Credential, leg.Challenge)
	if !ed25519.Verify(pub, msg, leg.Signature) {
		return nil, fmt.Errorf("%w: aprovador %q", ErrBadLegSignature, leg.Approver)
	}

	// AUTORIDADE approve:<classe>: a autenticidade não basta — a autoridade autoritativa do
	// aprovador tem de conter a capability da classe de risco da acção (utilizador ∩ classe).
	required := hitl.RequiredAuthority(req.RiskClass)
	if !hasAuthority(authority, required) {
		return nil, fmt.Errorf("%w: aprovador %q carece de %q", ErrInsufficientAuthority, leg.Approver, required)
	}

	// FRESCURA: com a porta de emissão ligada, o challenge tem de ter sido EMITIDO pelo
	// servidor para (pedido, aprovador) e estar dentro do TTL. É consulta pura — não consome
	// nada (o consumo de uso-único continua a ser [consumeChallenge], no fim).
	if err := g.checkIssued(ctx, req, leg); err != nil {
		return nil, err
	}

	return g.verifyDevice(ctx, leg)
}

// checkIssued impõe a EMISSÃO SERVER-SIDE do challenge quando a porta está ligada. Sem a porta
// devolve nil (comportamento histórico: dedup sem frescura). Fail-closed nos dois modos de
// falha: não-emitido/expirado ⇒ [ErrChallengeNotIssued]; backend em erro ⇒
// [ErrChallengeIssuanceBackend].
func (g *FourEyesGate) checkIssued(ctx context.Context, req FourEyesRequest, leg ApprovalLeg) error {
	if g.issuance == nil {
		return nil
	}
	issued, err := g.issuance.IsChallengeIssued(ctx, challengeScope(req.RequestID), leg.Approver, leg.Challenge)
	if err != nil {
		return fmt.Errorf("%w: aprovador %q: %v", ErrChallengeIssuanceBackend, leg.Approver, err)
	}
	if !issued {
		return fmt.Errorf("%w: aprovador %q", ErrChallengeNotIssued, leg.Approver)
	}
	return nil
}

// verifyDevice verifica a attestation de dispositivo da perna QUANDO a porta está ligada
// (AOS-177). Sem porta devolve (nil, nil) — modo estrutural retro-compatível. Com porta é
// OBRIGATÓRIA: attestationObject e clientDataJSON têm de estar presentes e o verificador tem
// de devolver um deviceID não-vazio; caso contrário a perna é recusada. O challenge esperado
// é o DA PERNA (já coberto pela assinatura ed25519), o que liga a attestation a esta perna
// deste pedido. Os erros propagados não transportam segredos nem PII.
func (g *FourEyesGate) verifyDevice(ctx context.Context, leg ApprovalLeg) ([]byte, error) {
	if g.devices == nil {
		return nil, nil
	}
	if len(leg.DeviceAttestation) == 0 || len(leg.DeviceClientData) == 0 {
		return nil, fmt.Errorf("%w: aprovador %q", ErrMissingDeviceAttestation, leg.Approver)
	}
	deviceID, err := g.devices.VerifyDeviceAttestation(ctx, leg.DeviceAttestation, leg.DeviceClientData, leg.Challenge)
	if err != nil {
		return nil, fmt.Errorf("%w: aprovador %q: %v", ErrDeviceAttestationRejected, leg.Approver, err)
	}
	// Um verificador que devolva (vazio, nil) não provou dispositivo nenhum — trata-se como
	// recusa em vez de se aceitar um identificador que não distingue nada.
	if len(deviceID) == 0 {
		return nil, fmt.Errorf("%w: aprovador %q: identificador de dispositivo vazio", ErrDeviceAttestationRejected, leg.Approver)
	}
	// ATRIBUIÇÃO: o dispositivo tem de estar REGISTADO para ESTE aprovador. Sem este passo a
	// attestation prova modelo+posse, não prova de quem é o autenticador — e um insider com um
	// único autenticador permitido satisfaria as duas pernas. Default-deny, fail-closed, e
	// ANTES de qualquer consumo de challenge (o consumo é o último passo do Authorize).
	if g.enrollment != nil {
		enrolled, err := g.enrollment.IsEnrolled(ctx, leg.Approver, deviceID)
		if err != nil {
			return nil, fmt.Errorf("%w: aprovador %q: %v", ErrDeviceEnrollmentBackend, leg.Approver, err)
		}
		if !enrolled {
			return nil, fmt.Errorf("%w: aprovador %q", ErrDeviceNotEnrolled, leg.Approver)
		}
	}
	return deviceID, nil
}

// hasAuthority indica se o conjunto de capabilities autoritativas contém a exigida. Igualdade
// exacta de string sobre a autoridade PINADA (não auto-declarada) — default-deny se ausente.
func hasAuthority(authority []string, required string) bool {
	for _, a := range authority {
		if a == required {
			return true
		}
	}
	return false
}

// consumeChallenge consome o challenge da perna de uso-único no scope do pedido (anti-replay
// durável). (false,nil)=replay ⇒ [ErrReplayedChallenge]; (false,err)=backend ⇒
// [ErrChallengeStoreBackend]. Ambos fail-closed.
func (g *FourEyesGate) consumeChallenge(ctx context.Context, req FourEyesRequest, leg ApprovalLeg) error {
	fresh, err := g.challenges.ConsumeNonce(ctx, challengeScope(req.RequestID), leg.Challenge)
	if err != nil {
		return fmt.Errorf("%w: aprovador %q: %v", ErrChallengeStoreBackend, leg.Approver, err)
	}
	if !fresh {
		return fmt.Errorf("%w: aprovador %q", ErrReplayedChallenge, leg.Approver)
	}
	return nil
}

// challengeScope namespaceia os challenges por request_id: um challenge é de uso-único
// dentro do pedido. Separador de domínio para não colidir com o scope do steer (AOS-160).
func challengeScope(requestID string) string {
	return "4eyes:" + requestID
}

// fourEyesMessage constrói o tuplo canónico assinado por uma perna, com a codificação
// INJECTIVA length-prefix de AOS-160 ([appendLenPrefixed]): cada campo variável é prefixado
// com o seu comprimento em 8 bytes big-endian. Como TODOS os campos são length-prefixed,
// duas tuplas logicamente distintas nunca colidem na mesma sequência de bytes (não há
// deslize de fronteira possível). O domínio versionado abre o tuplo para isolamento
// cross-protocolo.
// A CLASSE DE RISCO e o bit DUAL_REQUIRED entram no tuplo como um campo "policy" de 2 bytes
// (largura fixa) também length-prefixed: uma perna assinada para uma classe/exigência NÃO
// valida quando reconstruída com outra (fecha o downgrade dual→single e a troca de classe).
func fourEyesMessage(requestID string, preview []byte, riskClass risk.Class, dualRequired bool, approver, session, credential string, challenge []byte) []byte {
	var dualByte byte
	if dualRequired {
		dualByte = 1
	}
	policy := []byte{byte(riskClass), dualByte}
	msg := make([]byte, 0, 8+len(fourEyesDomain)+8+len(requestID)+8+len(preview)+8+len(policy)+8+len(approver)+8+len(session)+8+len(credential)+8+len(challenge))
	msg = appendLenPrefixed(msg, []byte(fourEyesDomain))
	msg = appendLenPrefixed(msg, []byte(requestID))
	msg = appendLenPrefixed(msg, preview)
	msg = appendLenPrefixed(msg, policy)
	msg = appendLenPrefixed(msg, []byte(approver))
	msg = appendLenPrefixed(msg, []byte(session))
	msg = appendLenPrefixed(msg, []byte(credential))
	msg = appendLenPrefixed(msg, challenge)
	return msg
}

// denied é um construtor de conveniência para uma decisão negada (sem segredos).
func denied(reason string) FourEyesDecision {
	return FourEyesDecision{Authorized: false, Reason: reason}
}

// SignFourEyesLeg é um HELPER de teste/emissor: com a chave PRIVADA do aprovador (que vive
// FORA do gate, tipicamente no secure element/credencial do humano — ADR-016 §1), produz a
// [ApprovalLeg] assinada pronta a submeter. Um adversário sem a chave privada não a consegue
// produzir. NÃO faz parte da fronteira de verificação — é a contraparte de assinatura que o
// aprovador legítimo corre no seu próprio dispositivo. A chave privada é um PARÂMETRO; o gate
// nunca a detém.
func SignFourEyesLeg(priv ed25519.PrivateKey, req FourEyesRequest, approver, session, credential string, challenge, deviceAttestation []byte) ApprovalLeg {
	return SignFourEyesLegAttested(priv, req, approver, session, credential, challenge, deviceAttestation, nil)
}

// SignFourEyesLegAttested é [SignFourEyesLeg] com o par WebAuthn COMPLETO
// (attestationObject + clientDataJSON) que a porta [DeviceAttestationVerifier] exige. A
// attestation é produzida pelo AUTENTICADOR do humano (fora deste processo, tal como a
// assinatura) — este helper apenas a transporta para a perna. Continua a NÃO entrar no
// tuplo assinado: o binding é o challenge, que já lá está.
func SignFourEyesLegAttested(priv ed25519.PrivateKey, req FourEyesRequest, approver, session, credential string, challenge, attestationObject, clientDataJSON []byte) ApprovalLeg {
	sig := ed25519.Sign(priv, fourEyesMessage(req.RequestID, req.Preview, req.RiskClass, req.DualControlRequired, approver, session, credential, challenge))
	return ApprovalLeg{
		Approver:          approver,
		Session:           session,
		Credential:        credential,
		Challenge:         challenge,
		DeviceAttestation: attestationObject,
		DeviceClientData:  clientDataJSON,
		Signature:         sig,
	}
}
