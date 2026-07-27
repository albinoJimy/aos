package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-206 (DEF-03) — PROMOTION CONTROLLER do nó `aos`.
//
// O AOS-159 entregou o MECANISMO de ratificação endurecido
// ([hitl.NewProductionRatificationGate], que FORÇA freshness + nonce-store durável),
// mas o CA do *wiring* foi marcado feito SEM chamador de produção existir (desmarcado
// por AOS-196, achado DEF-03): `grep promotion|Promote packages/cmd/aos/*.go` (não-teste)
// devolvia ZERO — o nó não compunha promotion controller nenhum, pelo que a via sancionada
// era INALCANÇÁVEL no artefacto entregue. É a classe de defeito que a auditoria v4 nomeia:
// capacidade implementada ao nível do componente e inatingível no nó.
//
// Este ficheiro fecha-o: compõe no nó um PROMOTION CONTROLLER real que interpõe
// [hitl.NewProductionRatificationGate] entre o canary e a produção — NENHUM artefacto
// auto-escrito (skill/memória procedural, AOS-096) chega a produção sem RATIFICAÇÃO HUMANA
// ASSINADA, e a MESMA ratificação re-submetida após consumo é [hitl.ReasonRatificationReplayed]
// (uso-único durável anti-re-promoção pós-rollback).
//
// # Via SANCIONADA, nunca a crua
//
// A composição usa EXCLUSIVAMENTE [hitl.NewProductionRatificationGate] — o construtor que
// RECUSA a construção sem freshness ([hitl.ErrNoFreshness]) e sem nonce-store
// ([hitl.ErrNoNonceStore]) e aplica ambos POR ÚLTIMO (um `opts` do chamador não os pode
// desactivar). O [hitl.NewRatificationGate] cru — que deixaria o anti-replay OPCIONAL e por
// omissão DESLIGADO — NÃO é chamado em lado nenhum do nó (é exactamente o anti-padrão que
// este ticket fecha).
//
// # SEMPRE composto (decisão; ver o banner)
//
// O controller é composto INCONDICIONALMENTE, no molde do canal de controlo (AOS-160): o
// [hitl.Ed25519Authenticator] é sempre composto mesmo com ZERO operadores (roster vazio ⇒
// tudo recusado, declarado no banner), e NÃO no molde OPCIONAL do FourEyesGate (composto só
// quando há aprovadores). A razão é o próprio DEF-03: tornar o controller condicional à
// config manteria a via sancionada inalcançável por omissão — precisamente o defeito. Sendo
// incondicional, `grep promotion` é não-zero POR CONSTRUÇÃO e a capacidade é atingível
// independentemente da config. Retro-compatível: não existia caminho de promoção antes, pelo
// que um gate sempre-composto e fail-closed não regride comportamento — sem ratificador
// registado, toda a promoção é NEGADA ([hitl.ReasonRatifierUnknown]), o default seguro. O
// roster de ratificadores É configurável (AOS_RATIFIERS — senão [Config.Ratifiers] seria o
// MESMO defeito de campo inalcançável); vazio ⇒ banner declara "COMPOSTO mas SEM
// RATIFICADORES".
type PromotionController struct {
	// gate é o gate de ratificação de PRODUÇÃO (via sancionada AOS-159): freshness + nonce
	// durável FORÇADOS por construção.
	gate *hitl.RatificationGate
	// ratifiers é a cardinalidade REAL de ratificadores pinados (lida do roster, não da
	// intenção da config) — o banner e os testes leem-na.
	ratifiers int
}

// Erros de composição do promotion controller (todos fail-closed).
var (
	// ErrBadRatifierEntry — uma entrada de [Config.Ratifiers] estruturalmente inútil
	// (principal vazio, pubkey ed25519 de tamanho != 32 bytes, principal DUPLICADO, ou pubkey
	// PARTILHADA por dois principals). Fail-closed (AOS-206), contraparte de
	// [ErrBadOperatorEntry]/[ErrBadApproverEntry]: um ratificador que o
	// [hitl.MemApproverRegistry.Register] guardaria-a-descartar (pubkey de tamanho errado nunca
	// verifica assinatura; principal duplicado sobrepõe-se em silêncio) daria um nó que arrancou
	// a contar "N ratificador(es)" no banner e recusaria com ratifier_unknown/ratification_forged
	// toda a ratificação desse principal. Uma pubkey partilhada destruiria a atribuição do
	// não-repúdio (UMA chave privada assinaria por dois nomes).
	ErrBadRatifierEntry = errors.New("aos: entrada de Config.Ratifiers invalida (principal vazio ou DUPLICADO, pubkey ed25519 de tamanho != 32 bytes, ou pubkey PARTILHADA por dois principals) — um registo descartado em silencio daria um ratificador que nunca ratifica; uma pubkey partilhada destruiria a atribuicao do nao-repudio da ratificacao")
)

const (
	// defaultRatifyTTL é a janela de frescura por omissão da ratificação quando
	// [Config.RatifyTTL] não é positiva. A via sancionada EXIGE ttl > 0 ([hitl.ErrNoFreshness]);
	// este default garante que o nó a satisfaz sempre sem obrigar o operador a escolher um valor
	// — limita a janela temporal em que uma ratificação assinada pode promover.
	defaultRatifyTTL = 15 * time.Minute
	// defaultPromotionMinScore é o limiar do eval-gate de referência do controller quando
	// [Config.PromotionEval] não é injectado. Fail-closed por construção ([otelgenai.FailClosedGate]):
	// só um veredicto pass explícito com score >= este valor é pré-condição de ratificação. O
	// eval-gate CONCRETO da política de promoção (AOS-114/115) fica DEFERIDO; aqui vai a
	// referência fail-closed, no molde dos outros colaboradores não-identidade do nó
	// (referenceModel/emptyCatalog).
	defaultPromotionMinScore = 0.8
)

// RatifierConfig regista UM ratificador de produção do promotion controller (AOS-206): o
// principal e a sua PUBKEY pinada (a privada vive no dispositivo do humano — nunca aqui, no
// molde de [ApproverConfig]). A autoridade é FIXA — [hitl.DefaultRatifyAuthority]
// ("ratify:production") —, ao contrário do FourEyes (autoridade por-classe): a ratificação de
// auto-modificação tem uma só capability, pelo que não há nada a variar por entrada.
type RatifierConfig struct {
	Principal string
	PubKey    ed25519.PublicKey
}

// validateRatifiers impõe o fail-closed de [Config.Ratifiers] (chamado no passo (1a) do
// [Bootstrap], a par das guardas de operadores/aprovadores). Devolve a ordem determinista dos
// principals para uma mensagem de erro reproduzível. Depois desta guarda vale o invariante que
// o banner lê: o registo de ratificadores tem exactamente len(cfg.Ratifiers) chaves, todas
// verificáveis, todas distintas.
func validateRatifiers(ratifiers []RatifierConfig) error {
	seenPrincipal := make(map[string]struct{}, len(ratifiers))
	seenKey := make(map[string]string, len(ratifiers))
	for i, r := range ratifiers {
		if r.Principal == "" || len(r.PubKey) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: ratificador #%d (%q)", ErrBadRatifierEntry, i, r.Principal)
		}
		if _, dup := seenPrincipal[r.Principal]; dup {
			return fmt.Errorf("%w: principal %q duplicado", ErrBadRatifierEntry, r.Principal)
		}
		seenPrincipal[r.Principal] = struct{}{}
		fp := string(r.PubKey)
		if other, dup := seenKey[fp]; dup {
			return fmt.Errorf("%w: principals %q e %q partilham a MESMA pubkey (o nao-repudio da ratificacao seria satisfeito por UMA chave privada)", ErrBadRatifierEntry, other, r.Principal)
		}
		seenKey[fp] = r.Principal
	}
	return nil
}

// newPromotionController compõe o promotion controller do nó pela via SANCIONADA
// [hitl.NewProductionRatificationGate]. Fail-closed: o construtor de produção RECUSA sem
// freshness + nonce-store — este helper fornece SEMPRE ambos (nonce-store DURÁVEL sobre o
// Event Store, no molde de AOS-159/AOS-160/AOS-162; janela de frescura com default seguro),
// pelo que nenhum caminho do nó constrói o gate cru.
//
// eval é o eval-gate de referência (default fail-closed) OU o injectado por config; sealer é o
// WORM do nó (a decisão de ratificação é selada na cadeia tamper-evident); es é o Event Store
// partilhado sobre o qual o nonce-store durável impõe o uso-único.
func newPromotionController(eval otelgenai.EvalGate, registry hitl.ApproverRegistry, sealer audit.Store, es *eventstore.Store, ttl, skew time.Duration, ratifiers int, clock func() time.Time) (*PromotionController, error) {
	if ttl <= 0 {
		ttl = defaultRatifyTTL
	}
	var opts []hitl.RatificationOption
	if clock != nil {
		opts = append(opts, hitl.WithRatifyClock(clock))
	}
	// VIA SANCIONADA: NewProductionRatificationGate FORÇA WithRatifyFreshness +
	// WithRatifyNonceStore (recusa a construção sem eles) e aplica-os POR ÚLTIMO — o crú
	// NewRatificationGate, que deixaria o anti-replay opcional/desligado, NUNCA é chamado.
	gate, err := hitl.NewProductionRatificationGate(
		eval, registry, sealer,
		hitl.NewEventStoreNonceStore(es), // uso-único DURÁVEL sobre o Event Store (AOS-159)
		ttl, skew,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	return &PromotionController{gate: gate, ratifiers: ratifiers}, nil
}

// Promote decide se um artefacto de auto-modificação (skill/memória procedural, AOS-096) pode
// ser PROMOVIDO a produção, dada a ratificação assinada. É o CAMINHO DO NÓ para a via
// sancionada: delega em [hitl.RatificationGate.Ratify] do gate de PRODUÇÃO, pelo que a
// pré-condição eval-gate+canary, a verificação da assinatura contra a chave pinada do
// ratificador autorizado, o anti-transplante, a frescura E o uso-único durável (nonce
// consumido no Event Store) valem TODOS neste caminho. Uma ratificação re-submetida após
// consumo devolve admit=false com [hitl.ReasonRatificationReplayed] selado no WORM.
func (c *PromotionController) Promote(ctx context.Context, artifact hitl.SelfModArtifact, ratification hitl.SignedApproval) (bool, error) {
	return c.gate.Ratify(ctx, artifact, ratification)
}

// RatifierCount devolve a cardinalidade REAL de ratificadores pinados (lida do roster
// composto, não da intenção da config). Zero ⇒ o gate está composto mas toda a promoção é
// NEGADA fail-closed (ratifier_unknown) — o banner declara-o.
func (c *PromotionController) RatifierCount() int { return c.ratifiers }

// buildRatifierRegistry constrói o [hitl.ApproverRegistry] dos ratificadores de produção a
// partir do roster já validado, pinando cada pubkey com a autoridade FIXA
// [hitl.DefaultRatifyAuthority] ("ratify:production"). Só material PÚBLICO entra. A ordem de
// registo é determinista (por principal) para não depender da ordenação da config.
func buildRatifierRegistry(ratifiers []RatifierConfig) *hitl.MemApproverRegistry {
	registry := hitl.NewMemApproverRegistry()
	sorted := append([]RatifierConfig(nil), ratifiers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Principal < sorted[j].Principal })
	for _, r := range sorted {
		registry.Register(r.Principal, r.PubKey, hitl.DefaultRatifyAuthority)
	}
	return registry
}
