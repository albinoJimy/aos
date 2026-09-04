package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// MinNonceLen é o comprimento mínimo (bytes) exigido a um nonce. Um nonce curto
// eleva o risco de colisão ACIDENTAL entre dois sinais legítimos (o 2º seria
// rejeitado como replay, fail-closed mas quebrando funcionalidade) e reduz a margem
// anti-replay. 16 bytes (128 bits) é o piso; [NewNonce] gera 32.
const MinNonceLen = 16

// Este ficheiro é o AUTENTICADOR DE PRODUÇÃO do canal de controlo OUT-OF-BAND (AOS-160,
// ADR-013 + ADR-012). Substitui o control.HMACAuthenticator demo-grade (replayável,
// sem nonce/frescura) por um Ed25519Authenticator que:
//
//  1. verifica a assinatura ed25519 do sinal contra a PUBKEY do EMISSOR (operador
//     humano/serviço), num registo emitterID→pubkey. A chave PRIVADA do emissor NUNCA é
//     detida pelo authenticator/nó — SÓ pubkeys (o mesmo padrão de AOS-156, em que o nó
//     recebe só o trust anchor e por isso verifica mas não forja);
//  2. anti-replay DURÁVEL: cada sinal carrega um nonce de uso-único consumido num
//     nonce-store durável (o EventStoreNonceStore de AOS-159) — o MESMO sinal capturado
//     não se re-verifica, e a detecção SOBREVIVE a restart;
//  3. frescura/expiração: o carimbo issued_at tem de cair numa janela [now-ttl, now+skew]
//     (relógio INJECTÁVEL) — um sinal velho é recusado.
//
// A assinatura cobre (run_id ‖ kind ‖ payload ‖ nonce ‖ issued_at): sem isso o nonce e o
// carimbo seriam adulteráveis (um atacante trocava-os mantendo a assinatura). Fail-closed
// em TUDO: emissor desconhecido, assinatura inválida, replay, expirado ⇒ erro não-nil.
//
// LAYERING. Vive em packages/integration — a única camada que pode compor a INTERFACE do
// kernel (control.Authenticator) com o nonce-store do control-plane (hitl). O kernel/
// control não pode importar control-plane; a integração fecha o buraco sem inverter a
// dependência. Liga-se ao SteerChannel via a interface control.Authenticator já aceite
// por control.NewChannel — o canal NÃO muda.

// Erros do autenticador de produção (todos fail-closed).
var (
	// ErrNoNonceStore — construção sem nonce-store durável. Sem ele não há anti-replay
	// durável, logo a construção é recusada (fail-closed).
	ErrNoNonceStore = errors.New("integration: ed25519 authenticator exige um nonce-store durável")
	// ErrNoFreshnessWindow — construção com ttl <= 0. Uma janela de frescura nula
	// aceitaria carimbos arbitrariamente velhos — recusada fail-closed.
	ErrNoFreshnessWindow = errors.New("integration: ed25519 authenticator exige uma janela de frescura (ttl > 0)")
	// ErrUnknownEmitter — o emissor não tem pubkey registada (default-deny).
	ErrUnknownEmitter = errors.New("integration: emissor desconhecido (sem pubkey registada)")
	// ErrBadSignature — a assinatura ed25519 não valida sobre o tuplo assinado.
	ErrBadSignature = errors.New("integration: assinatura ed25519 inválida")
	// ErrMissingNonce — o sinal não traz nonce (obrigatório para o anti-replay).
	ErrMissingNonce = errors.New("integration: nonce em falta no sinal")
	// ErrNonceTooShort — o nonce tem menos de [MinNonceLen] bytes. Rejeitado
	// fail-closed: nonces curtos reduzem a margem anti-replay e elevam o risco de
	// colisão acidental.
	ErrNonceTooShort = errors.New("integration: nonce demasiado curto (mínimo MinNonceLen bytes)")
	// ErrMissingIssuedAt — o sinal não traz issued_at (obrigatório para a frescura).
	ErrMissingIssuedAt = errors.New("integration: issued_at em falta no sinal")
	// ErrStaleSignal — o issued_at está fora da janela de frescura (velho ou futuro além
	// do skew) ⇒ expirado.
	ErrStaleSignal = errors.New("integration: sinal expirado (issued_at fora da janela de frescura)")
	// ErrReplayedSignal — o nonce já foi consumido: replay do MESMO sinal.
	ErrReplayedSignal = errors.New("integration: sinal replayado (nonce já consumido)")
	// ErrNonceStoreBackend — erro do backend do nonce-store; tratado como bloqueio
	// (fail-closed) — na dúvida, rejeita.
	ErrNonceStoreBackend = errors.New("integration: falha do nonce-store (fail-closed)")
)

// NonceConsumer é a porta durável de uso-único de que o Ed25519Authenticator depende. É
// EXACTAMENTE a assinatura de hitl.EventStoreNonceStore.ConsumeNonce (AOS-159), mas
// declarada aqui como interface mínima para desacoplar do store concreto e o tornar
// testável. Semântica: (true,nil)=nonce FRESCO, (false,nil)=REPLAY, (false,err)=erro de
// backend. O check-and-set TEM de ser atómico e durável (não um set in-memory).
type NonceConsumer interface {
	ConsumeNonce(ctx context.Context, scope string, nonce []byte) (bool, error)
}

// Ed25519Authenticator implementa control.Authenticator com verificação ed25519 +
// anti-replay durável + frescura. Detém APENAS pubkeys (nunca chaves privadas de
// emissores). Seguro para uso concorrente.
type Ed25519Authenticator struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey

	nonces NonceConsumer
	now    func() time.Time
	ttl    time.Duration
	skew   time.Duration
}

// Ed25519Option configura o autenticador na construção.
type Ed25519Option func(*Ed25519Authenticator)

// WithEd25519Clock injecta o relógio (default time.Now). Usar nos testes determinísticos
// de frescura/expiração.
func WithEd25519Clock(now func() time.Time) Ed25519Option {
	return func(a *Ed25519Authenticator) {
		if now != nil {
			a.now = now
		}
	}
}

// WithEd25519Skew define a tolerância para carimbos ligeiramente no futuro (relógios
// adiantados). Default 0. Valores negativos são ignorados.
func WithEd25519Skew(skew time.Duration) Ed25519Option {
	return func(a *Ed25519Authenticator) {
		if skew >= 0 {
			a.skew = skew
		}
	}
}

// NewEd25519Authenticator constrói o autenticador de produção sobre um nonce-store
// durável e uma janela de frescura (ttl). Sem nonce-store ([ErrNoNonceStore]) ou com
// ttl <= 0 ([ErrNoFreshnessWindow]) é recusado fail-closed. Começa SEM emissores
// (default-deny: qualquer sinal é rejeitado até um emissor ser registado com [Register]).
// NUNCA há segredos hardcoded — só pubkeys, registadas pelo chamador.
func NewEd25519Authenticator(nonces NonceConsumer, ttl time.Duration, opts ...Ed25519Option) (*Ed25519Authenticator, error) {
	if nonces == nil {
		return nil, ErrNoNonceStore
	}
	if ttl <= 0 {
		return nil, ErrNoFreshnessWindow
	}
	a := &Ed25519Authenticator{
		keys:   make(map[string]ed25519.PublicKey),
		nonces: nonces,
		now:    time.Now,
		ttl:    ttl,
	}
	for _, o := range opts {
		o(a)
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// Register regista (ou substitui) a PUBKEY de um emissor. Só se detém material PÚBLICO —
// a chave privada do emissor vive fora deste processo (no operador/serviço que assina).
// Um emissor sem pubkey registada NUNCA autentica (default-deny). Uma pubkey de tamanho
// inválido é ignorada (não regista) — fail-closed.
func (a *Ed25519Authenticator) Register(emitterID string, pub ed25519.PublicKey) {
	if emitterID == "" || len(pub) != ed25519.PublicKeySize {
		return
	}
	key := make(ed25519.PublicKey, len(pub))
	copy(key, pub)
	a.mu.Lock()
	a.keys[emitterID] = key
	a.mu.Unlock()
}

// EmitterCount devolve quantos emissores têm pubkey registada. É a leitura HONESTA do
// estado default-deny do canal de controlo: 0 ⇒ NENHUM sinal pode autenticar (toda a
// chamada a [Ed25519Authenticator.Authenticate] devolve [ErrUnknownEmitter]), pelo que um
// canal com 0 emissores está tecnicamente autenticado mas é INOPERÁVEL.
//
// Existe para que um chamador (o bind-guardrail do nó, AOS-193) possa DISTINGUIR "canal de
// controlo autenticado E operável" de "canal composto mas vazio" — a distinção que a mera
// presença do authenticator NÃO faz. Só expõe uma CARDINALIDADE: nem os IDs dos emissores
// nem qualquer material de chave saem daqui.
func (a *Ed25519Authenticator) EmitterCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.keys)
}

// Authenticate implementa control.Authenticator. Ordem fail-closed: (a) emissor conhecido?
// (b) nonce e issued_at presentes? (c) ed25519.Verify sobre o tuplo COMPLETO
// (run_id ‖ kind ‖ payload ‖ nonce ‖ issued_at); (d) frescura — issued_at na janela
// [now-ttl, now+skew]; (e) anti-replay — ConsumeNonce durável. Qualquer falha ⇒ erro
// não-nil (o SteerChannel envolve-o em control.ErrUnauthenticated e o sinal nunca toca no
// log).
//
// A verificação da assinatura vem ANTES do consumo do nonce: um sinal com assinatura
// inválida NÃO gasta um nonce (evita que um atacante queime nonces legítimos com lixo).
func (a *Ed25519Authenticator) Authenticate(ctx context.Context, runID string, kind control.SignalKind, payload []byte, emitter control.Emitter) error {
	a.mu.RLock()
	pub, ok := a.keys[emitter.ID]
	a.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEmitter, emitter.ID)
	}
	if len(emitter.Nonce) == 0 {
		return ErrMissingNonce
	}
	if len(emitter.Nonce) < MinNonceLen {
		return fmt.Errorf("%w: %d < %d", ErrNonceTooShort, len(emitter.Nonce), MinNonceLen)
	}
	if emitter.IssuedAt.IsZero() {
		return ErrMissingIssuedAt
	}

	// (c) Assinatura ed25519 sobre o tuplo COMPLETO — inclui nonce e issued_at, senão
	// seriam adulteráveis.
	msg := signedMessage(runID, kind, payload, emitter.Nonce, emitter.IssuedAt)
	if !ed25519.Verify(pub, msg, emitter.Signature) {
		return fmt.Errorf("%w: emissor %q, kind %q", ErrBadSignature, emitter.ID, kind)
	}

	// (d) Frescura: age = now - issued_at; rejeita se demasiado velho (age > ttl) ou
	// demasiado no futuro (age < -skew). Mesma convenção de hitl.WithRatifyFreshness.
	age := a.now().Sub(emitter.IssuedAt)
	if age > a.ttl || age < -a.skew {
		return fmt.Errorf("%w: emissor %q, idade %s (ttl %s, skew %s)", ErrStaleSignal, emitter.ID, age, a.ttl, a.skew)
	}

	// (e) Anti-replay durável. O scope liga o nonce a (run_id, emissor): um nonce é de
	// uso-único dentro desse par. (false,nil)=replay, (false,err)=erro de backend —
	// ambos rejeitam (fail-closed).
	fresh, err := a.nonces.ConsumeNonce(ctx, nonceScope(runID, emitter.ID), emitter.Nonce)
	if err != nil {
		return fmt.Errorf("%w: emissor %q: %v", ErrNonceStoreBackend, emitter.ID, err)
	}
	if !fresh {
		return fmt.Errorf("%w: emissor %q, kind %q", ErrReplayedSignal, emitter.ID, kind)
	}
	return nil
}

// VerifyEmitterSignature verifica SÓ A ASSINATURA de um [control.Emitter] sobre
// (runID ‖ kind ‖ payload ‖ nonce ‖ issued_at) — o MESMO tuplo canónico de
// [Ed25519Authenticator.Authenticate], produzido pela mesma [signedMessage], para que as
// duas fronteiras nunca possam divergir.
//
// O QUE NÃO FAZ, e é o ponto: não consome nonce e não avalia frescura.
//
// PORQUÊ VERIFY-ONLY. Existe para REVERIFICAR, à posteriori, uma assinatura já selada num
// registo de audit — o caso concreto é a rehidratação dos níveis de autonomia no arranque
// (AOS-307), em que o nó relê alterações que ele próprio, ou outro nó, já executou. Nesse
// contexto:
//
//   - CONSUMIR o nonce estaria errado ao ponto de ser um auto-DoS: o nonce foi consumido
//     quando o pedido foi servido, pelo que reconsumi-lo devolveria «replay» e o nó
//     recusaria arrancar com os seus PRÓPRIOS registos legítimos. Pior, com um nonce-store
//     durável, o primeiro arranque envenenaria a cadeia para todos os seguintes.
//   - A FRESCURA não faz sentido a reler o passado: um `issued_at` de há três meses é
//     exactamente o que se espera de uma decisão tomada há três meses. Exigir uma janela
//     seria exigir que a história fosse recente.
//
// O que a assinatura continua a provar é o que se precisa aqui: que aquele emissor, com
// aquela chave privada, pediu AQUELA alteração exacta (o payload canónico amarra o par, o
// nível e o motivo). A protecção anti-replay do CANAL — que impede reapresentar um pedido
// como se fosse novo — é da fronteira de admissão, não desta.
//
// Fail-closed: pubkey de tamanho errado, nonce/carimbo em falta ou assinatura que não
// verifica ⇒ erro não-nil. Não toca em estado nenhum.
func VerifyEmitterSignature(runID string, kind control.SignalKind, payload []byte, pub ed25519.PublicKey, em control.Emitter) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: %q sem pubkey ed25519 valida", ErrUnknownEmitter, em.ID)
	}
	// Nonce e issued_at NÃO são validados quanto a frescura nem a uso-único, mas TÊM de
	// estar presentes: fazem parte do tuplo assinado, e sem eles a mensagem reconstruída
	// não é a que foi assinada.
	if len(em.Nonce) == 0 {
		return ErrMissingNonce
	}
	if em.IssuedAt.IsZero() {
		return ErrMissingIssuedAt
	}
	if !ed25519.Verify(pub, signedMessage(runID, kind, payload, em.Nonce, em.IssuedAt), em.Signature) {
		return fmt.Errorf("%w: emissor %q, kind %q", ErrBadSignature, em.ID, kind)
	}
	return nil
}

// nonceScope namespaceia o nonce por (run_id, emissor): o mesmo nonce só é de uso-único
// dentro deste par. Separador de domínio para que fronteiras distintas não colidam.
func nonceScope(runID, emitterID string) string {
	return "steer:" + runID + "\x00" + emitterID
}

// signedMessage constrói o tuplo canónico assinado com uma codificação INJECTIVA:
// cada campo de comprimento variável (run_id ‖ kind ‖ payload ‖ nonce) é prefixado
// com o seu comprimento em 8 bytes big-endian (length-prefix), seguido do issued_at
// como nanos UTC big-endian (largura fixa 8 bytes).
//
// Length-prefix e NÃO separadores 0x00: um separador de byte único NÃO é injectivo
// porque o byte separador pode ocorrer DENTRO de um campo variável (um nonce binário
// contém 0x00 em ~6% dos casos). Com separadores, um atacante SEM a chave privada podia
// capturar um sinal cujo nonce/payload contivesse um 0x00 e DESLIZAR a fronteira
// payload|nonce (reusando esse 0x00), obtendo um tuplo logicamente distinto — nonce'
// diferente (novo scope ⇒ o anti-replay durável NÃO o detecta) e payload' mutado — com
// a MESMA sequência de bytes assinada, logo a MESMA assinatura ed25519 válida. O
// length-prefix elimina essa ambiguidade: o comprimento fixa a fronteira de cada campo,
// pelo que duas tuplas distintas NUNCA colidem na mesma sequência de bytes.
func signedMessage(runID string, kind control.SignalKind, payload, nonce []byte, issuedAt time.Time) []byte {
	var tsb [8]byte
	binary.BigEndian.PutUint64(tsb[:], uint64(issuedAt.UTC().UnixNano()))
	msg := make([]byte, 0, 8+len(runID)+8+len(kind)+8+len(payload)+8+len(nonce)+8)
	msg = appendLenPrefixed(msg, []byte(runID))
	msg = appendLenPrefixed(msg, []byte(kind))
	msg = appendLenPrefixed(msg, payload)
	msg = appendLenPrefixed(msg, nonce)
	msg = append(msg, tsb[:]...)
	return msg
}

// appendLenPrefixed anexa uint64(len(field)) big-endian seguido de field. É o tijolo
// da codificação injectiva de [signedMessage].
func appendLenPrefixed(dst, field []byte) []byte {
	var lb [8]byte
	binary.BigEndian.PutUint64(lb[:], uint64(len(field)))
	dst = append(dst, lb[:]...)
	return append(dst, field...)
}

// NewNonce gera um nonce fresco de 32 bytes (256 bits) via crypto/rand — bem acima de
// [MinNonceLen]. Helper para os emissores não terem de o fazer à mão; a aleatoriedade
// forte torna a colisão acidental negligenciável.
func NewNonce() ([]byte, error) {
	n := make([]byte, 32)
	if _, err := rand.Read(n); err != nil {
		return nil, fmt.Errorf("integration: falha a gerar nonce: %w", err)
	}
	return n, nil
}

// SignSignal é um HELPER de teste/emissor: com a chave PRIVADA do operador (que vive
// FORA do authenticator), produz o control.Emitter assinado pronto a passar ao
// SteerChannel. Um adversário sem a chave privada não o consegue produzir. NÃO faz parte
// da fronteira de verificação — é a contraparte de assinatura que um operador/serviço
// legítimo corre no seu próprio processo.
//
// A chave privada é um PARÂMETRO — o authenticator/nó nunca a detém.
func SignSignal(priv ed25519.PrivateKey, emitterID, runID string, kind control.SignalKind, payload, nonce []byte, issuedAt time.Time) control.Emitter {
	sig := ed25519.Sign(priv, signedMessage(runID, kind, payload, nonce, issuedAt))
	return control.Emitter{
		ID:        emitterID,
		Signature: sig,
		Nonce:     nonce,
		IssuedAt:  issuedAt,
	}
}

// AutonomyScope é o `runID` do tuplo assinado das mudanças de autonomia. Não há run: o âmbito é
// fixo, e o alvo concreto (par e nível) vive no PAYLOAD.
const AutonomyScope = "autonomy"

// CanonicalAutonomyPayload é o payload assinado de uma mudança de nível de autonomia.
//
// Length-prefixed pela MESMA razão que [signedMessage]: com um separador simples, um `agent` e um
// `domain` podiam deslizar a fronteira entre si e produzir o mesmo tuplo de bytes para uma
// mudança logicamente diferente — a mesma assinatura válida para outro alvo.
//
// O NÍVEL e o MOTIVO entram no payload de propósito. É o que impede reapresentar uma assinatura
// legítima de "L1" como se fosse de "L5", e o que amarra a justificação ao acto: o motivo que
// fica selado é o que foi assinado, não um que se acrescente depois.
func CanonicalAutonomyPayload(agent, domain, level, reason string) []byte {
	out := make([]byte, 0, 32+len(agent)+len(domain)+len(level)+len(reason))
	out = appendLenPrefixed(out, []byte("aos.autonomy.set_level/v1"))
	out = appendLenPrefixed(out, []byte(agent))
	out = appendLenPrefixed(out, []byte(domain))
	out = appendLenPrefixed(out, []byte(level))
	out = appendLenPrefixed(out, []byte(reason))
	return out
}

// RevokeScope é o `runID` do tuplo assinado das revogações de NHI (AOS-288). Não há run: o
// âmbito é fixo, e o `jti` alvo vive no PAYLOAD. É um valor DISTINTO de [AutonomyScope] de
// propósito — se os dois partilhassem âmbito, uma assinatura de mudança de autonomia e uma de
// revogação diferiam apenas pelo payload, e o `kind` deixaria de ser a segunda amarra.
const RevokeScope = "nhi.revoke"

// CanonicalRevokePayload é o payload assinado de uma revogação de token NHI.
//
// Length-prefixed pela mesma razão de [CanonicalAutonomyPayload]: sem isso, um `jti` e um
// `reason` podiam deslizar a fronteira entre si e produzir o mesmo tuplo para uma revogação
// logicamente diferente.
//
// O MOTIVO entra no payload e não é decorativo. Revogar um token é uma acção de governação
// irreversível dentro da janela de vida dele; o que fica selado tem de ser o motivo que foi
// ASSINADO, não um que se acrescente depois de a assinatura estar feita.
func CanonicalRevokePayload(jti, reason string) []byte {
	out := make([]byte, 0, 32+len(jti)+len(reason))
	out = appendLenPrefixed(out, []byte("aos.nhi.revoke/v1"))
	out = appendLenPrefixed(out, []byte(jti))
	out = appendLenPrefixed(out, []byte(reason))
	return out
}

// ChallengeRequestScope é o `runID` do tuplo assinado de um PEDIDO DE CHALLENGE do four-eyes
// (AOS-308). Distinto de [AutonomyScope] e [RevokeScope] pela mesma razão: âmbitos partilhados
// deixariam duas assinaturas diferir só pelo payload.
//
// NÃO confundir com [ChallengeScope], que é o namespace do NONCE-STORE onde o challenge emitido
// é depois consumido pela perna de aprovação — este é o âmbito da ASSINATURA de quem o pede.
const ChallengeRequestScope = "foureyes.challenge"

// CanonicalChallengePayload é o payload assinado por um APROVADOR a pedir um challenge para
// (run, request_id, ele próprio).
//
// O aprovador entra no payload e é confrontado com o emissor da assinatura: quem pede um
// challenge para "human:ana" tem de ser "human:ana". Sem isso, qualquer detentor de UMA chave de
// aprovador podia inundar o registo de emissão com challenges atribuídos a OUTROS aprovadores —
// que era, no essencial, o que uma rota sem assinatura permitia a qualquer cliente. O run e o
// request_id entram para que uma assinatura capturada não sirva para outra cerimónia.
func CanonicalChallengePayload(runID, requestID, approver string) []byte {
	out := make([]byte, 0, 32+len(runID)+len(requestID)+len(approver))
	out = appendLenPrefixed(out, []byte("aos.foureyes.challenge/v1"))
	out = appendLenPrefixed(out, []byte(runID))
	out = appendLenPrefixed(out, []byte(requestID))
	out = appendLenPrefixed(out, []byte(approver))
	return out
}

// SignEmitter produz um [control.Emitter] assinado para (runID, kind, payload).
//
// Existe para que quem emite um sinal de controlo NÃO reimplemente o tuplo canónico. Duas
// implementações do mesmo encoding divergem, e a divergência não dá um erro legível — dá "assinatura
// inválida", que se lê como chave errada e manda o operador procurar no sítio errado. A mesma razão
// pela qual o digest da delegação vive no `aos-issuer` e não no script de browser.
func SignEmitter(id string, priv ed25519.PrivateKey, runID string, kind control.SignalKind, payload []byte, issuedAt time.Time) (control.Emitter, error) {
	nonce, err := NewNonce()
	if err != nil {
		return control.Emitter{}, err
	}
	return control.Emitter{
		ID:        id,
		Nonce:     nonce,
		IssuedAt:  issuedAt.UTC(),
		Signature: ed25519.Sign(priv, signedMessage(runID, kind, payload, nonce, issuedAt.UTC())),
	}, nil
}
