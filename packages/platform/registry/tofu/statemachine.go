package tofu

import "github.com/aos-ref/platform/registry/domain"

// TrustState reutiliza o estado de confiança TOFU reservado no domínio (AOS-045,
// domain.TrustState) — first_seen → pinned → (changed). AOS-049 concretiza a
// MÁQUINA que produz e transita entre estes estados; o vocabulário é o mesmo que a
// proveniência de uma entrada grava, para que o estado TOFU de um servidor e o
// estado gravado numa entrada falem a mesma língua.
type TrustState = domain.TrustState

// Estados de confiança (à imagem da máquina de estados durável): first_seen é o
// estado inicial de qualquer identidade recém-observada; pinned é o estado de
// confiança TOFU (só depois de ratificação explícita do operador); changed é o
// estado de INCIDENTE (divergência de digest após pinned), que bloqueia.
const (
	StateFirstSeen = domain.TrustFirstSeen
	StatePinned    = domain.TrustPinned
	StateChanged   = domain.TrustChanged
)

// reference é o par (versão, digest) a que uma identidade está ANCORADA. Em
// first_seen é o par observado (ainda pendente de ratificação); em pinned é o par
// de confiança contra o qual toda a divergência posterior é medida; em changed
// mantém o par pinado anterior (a referência que foi violada) para que a
// re-aprovação saiba que versão tem de ser superada.
type reference struct {
	Version domain.Version
	Digest  string
}

// equal indica se r coincide EXACTAMENTE (versão E digest) com o. A detecção de
// drift é sobre o par completo: um digest diferente na mesma versão é o rug-pull
// clássico; uma versão diferente sem re-aprovação explícita é igualmente uma
// divergência não-autorizada da referência pinada.
func (r reference) equal(o reference) bool {
	return r.Version.Equal(o.Version) && r.Digest == o.Digest
}

// record é o estado TOFU interno de UMA identidade de servidor. Guarda o estado de
// confiança corrente e a referência ancorada. É um value type: o Monitor guarda
// cópias no seu mapa e nunca partilha ponteiros mutáveis.
type record struct {
	State TrustState
	Ref   reference
}

// transition descreve a decisão pura de uma máquina TOFU sobre um evento: o estado
// e a referência resultantes, se houve de facto uma mudança de estado a auditar, e
// o erro sentinela quando o evento é recusado (fail-closed). É PURA — não toca no
// audit, no relógio nem no mapa; o Monitor traduz esta decisão em efeitos.
type transition struct {
	// Next é o record resultante (estado + referência) a gravar se o evento proceder.
	Next record
	// Changed indica se o estado de confiança MUDOU (none→first_seen, first_seen→
	// pinned, pinned→changed, changed→pinned) — só uma mudança real é auditada como
	// transição. Uma re-observação idêntica (pinned que se mantém pinned) não é
	// transição.
	Changed bool
	// Cap é a capability de audit da transição (vocabulário estável) quando Changed.
	Cap string
	// Decision é o veredicto de audit da transição (allow para confiar/avançar, deny
	// para o incidente de drift e para tentativas recusadas).
	Decision transitionDecision
	// Err é o erro sentinela quando o evento é RECUSADO. Numa transição válida é nil;
	// no drift é ErrSchemaDrift (o estado muda para changed E o erro sinaliza o
	// incidente, fail-closed); numa tentativa inválida é o erro específico (sem mudança).
	Err error
}

// transitionDecision é o veredicto de audit de uma transição (allow/deny),
// desacoplado do pacote audit para manter a máquina pura.
type transitionDecision int

const (
	decisionAllow transitionDecision = iota
	decisionDeny
)

// Capabilities de audit das transições TOFU (vocabulário estável, selado na cadeia
// WORM para ser tamper-evident).
const (
	capFirstSeen  = "registry.tofu.first_seen"
	capPinned     = "registry.tofu.pinned"
	capChanged    = "registry.tofu.changed"
	capReapproved = "registry.tofu.reapproved"
)

// onObserve é a decisão PURA de uma observação (re-)descoberta de manifesto. É o
// coração da detecção de drift:
//
//   - Identidade NUNCA vista (prev == nil): regista first_seen com (versão, digest).
//     É a primeira ligação — a identidade e o digest do manifesto ficam registados,
//     mas NADA é confiado ainda (aguarda ratificação).
//   - Em first_seen: uma observação IDÊNTICA mantém-se pendente (sem transição); uma
//     observação DIFERENTE re-regista o first_seen com o novo par (nada estava
//     confiado, logo não é drift — é apenas o par pendente a actualizar-se, auditado).
//   - Em pinned: observação IDÊNTICA à referência → PASSA (mantém pinned, sem
//     transição). Observação DIVERGENTE (digest e/ou versão) → CHANGED: incidente de
//     drift, bloqueia (ErrSchemaDrift).
//   - Em changed: mantém-se changed e bloqueado (idempotente, sem nova transição) —
//     a recuperação é só por Reapprove, nunca por uma observação.
func onObserve(prev *record, obs reference) transition {
	if prev == nil {
		return transition{
			Next:     record{State: StateFirstSeen, Ref: obs},
			Changed:  true,
			Cap:      capFirstSeen,
			Decision: decisionAllow,
		}
	}
	switch prev.State {
	case StateFirstSeen:
		if prev.Ref.equal(obs) {
			// Re-observação idêntica de um first_seen pendente: sem transição.
			return transition{Next: *prev}
		}
		// Ainda nada confiado: o par pendente actualiza-se para o observado. Auditado
		// como first_seen (a identidade continua não-confiada; não é um incidente).
		return transition{
			Next:     record{State: StateFirstSeen, Ref: obs},
			Changed:  true,
			Cap:      capFirstSeen,
			Decision: decisionAllow,
		}
	case StatePinned:
		if prev.Ref.equal(obs) {
			// Digest coincide com o pinado: PASSA, mantém pinned (sem transição).
			return transition{Next: *prev}
		}
		// DRIFT: divergência do digest pinado = incidente. Transita para changed
		// (preservando a referência violada) e bloqueia com ErrSchemaDrift.
		return transition{
			Next:     record{State: StateChanged, Ref: prev.Ref},
			Changed:  true,
			Cap:      capChanged,
			Decision: decisionDeny,
			Err:      ErrSchemaDrift,
		}
	default: // StateChanged
		// Já em incidente: mantém-se bloqueado. Sem nova transição (idempotente); o
		// erro sinaliza que continua a ser um incidente (fail-closed).
		return transition{Next: *prev, Err: ErrSchemaDrift}
	}
}

// onRatify é a decisão PURA da ratificação do operador (first_seen → pinned). Só o
// first_seen é ratificável, e SÓ sobre a (versão, digest) exactamente observada — o
// operador ratifica o que viu (elimina o TOCTOU). Qualquer outra condição recusa.
func onRatify(prev *record, want reference) transition {
	if prev == nil || prev.State != StateFirstSeen {
		return transition{Err: ErrNotFirstSeen}
	}
	if !prev.Ref.equal(want) {
		return transition{Err: ErrRatifyMismatch}
	}
	return transition{
		Next:     record{State: StatePinned, Ref: want},
		Changed:  true,
		Cap:      capPinned,
		Decision: decisionAllow,
	}
}

// onReapprove é a decisão PURA da re-aprovação após um incidente (changed → pinned).
// É a recuperação do drift e impõe a regra central de ADR-012: a mudança de schema
// EXIGE uma NOVA versão SemVer. Só procede se:
//
//   - a identidade está em changed (senão ErrNotChanged); e
//   - a versão re-aprovada é ESTRITAMENTE SUPERIOR à pinada anterior:
//   - versão igual → ErrInBandReapproval (o rug-pull in-band; nunca a mesma versão);
//   - versão inferior → ErrVersionRegression (sem downgrades).
//
// Em sucesso, a nova (versão, digest) passa a ser a referência de confiança e o
// estado volta a pinned.
func onReapprove(prev *record, next reference) transition {
	if prev == nil || prev.State != StateChanged {
		return transition{Err: ErrNotChanged}
	}
	switch next.Version.Compare(prev.Ref.Version) {
	case 0:
		return transition{Err: ErrInBandReapproval}
	case -1:
		return transition{Err: ErrVersionRegression}
	default: // +1: versão estritamente superior
		return transition{
			Next:     record{State: StatePinned, Ref: next},
			Changed:  true,
			Cap:      capReapproved,
			Decision: decisionAllow,
		}
	}
}

// admits traduz um estado de confiança no veredicto de admissibilidade TOFU e a
// razão legível. SÓ pinned admite a utilização do artefacto (ADR-002, default-deny):
//
//   - first_seen → NÃO admite (aguarda ratificação do operador);
//   - changed    → NÃO admite (incidente de drift; exige re-aprovação com nova versão);
//   - pinned     → admite.
//
// Uma identidade desconhecida (record ausente) é tratada pelo Monitor como
// default-deny antes de chegar aqui.
func admits(state TrustState) (bool, string) {
	switch state {
	case StatePinned:
		return true, ""
	case StateFirstSeen:
		return false, "first_seen: aguarda ratificacao do operador (nao-confiado)"
	case StateChanged:
		return false, "changed: incidente de schema drift; bloqueado ate re-aprovacao com nova versao SemVer"
	default:
		return false, "estado de confianca invalido (fail-closed)"
	}
}
