package modelgateway

import (
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// O PERFIL DA ROTA FIXADO POR RUN (AOS-520, PR 0; emenda ao ADR-036 §2.8).
//
// O QUE ISTO FECHA. Até aqui, por run só ficava fixada a VERSÃO DA PROJECÇÃO (AOS-513). O perfil
// não: um run começado com o perfil A e retomado num nó que arrancou com o perfil B para o mesmo
// nome pedido continuava, com o modelo esperado e os parâmetros de B, e nada falhava — era o
// «limite conhecido» do AOS-519. Agora quem hospeda o run fixa-lhe o DIGEST do perfil em vigor
// ([agentruntime.Goal.RouteProfileDigest]), esse digest fica no registo de retoma, e é conferido
// aqui, com o perfil que o nó tem em vigor, em dois sítios: antes de um run ser re-hospedado, e
// antes de cada pedido ao provider.
//
// QUEM DECIDE É O VALOR SELADO NO RUN. A conferência recebe o digest que o run traz e compara-o
// com o perfil ACTUAL do nome pedido; nunca calcula «o digest que o run devia ter» a partir da
// configuração do nó. Um run fixado é conferido em qualquer nó — com ou sem perfis assinados: um
// nó a que tiraram a pasta dos perfis recusa o run que começou com um deles.
//
// O QUE NÃO COBRE. Um run sem perfil fixado — de antes deste ticket, ou de um nó sem perfis
// assinados — não é fixado a meio e continua como sempre, com uma excepção (ver
// [PinnedCauseSignedOverState]). Fica por isso aberta a troca da tabela em código entre duas
// imagens do nó, sem pasta de perfis: esses runs nunca tiveram digest fixado.

// Causas de recusa de um run pelo seu perfil fixado. Vocabulário FECHADO
// ([PinnedRouteProfileCauses]).
const (
	// PinnedCauseProfileDifferent — o run está fixado num perfil, e o perfil que o nó tem em
	// vigor para o nome pedido do run é OUTRO (outro digest).
	PinnedCauseProfileDifferent = "perfil_diferente_do_fixado"
	// PinnedCauseModelNotServed — o run está fixado num perfil, e o nó não tem perfil NENHUM para
	// o nome pedido do run: o modelo em que o run ficou fixado deixou de ser servido.
	PinnedCauseModelNotServed = "modelo_fixado_nao_servido"
	// PinnedCauseSignedOverState — o run NÃO tem perfil fixado (começou antes da fixação), e o nó
	// tem agora, para o nome pedido do run, um perfil que SUBSTITUIU uma entrada da tabela em
	// código que devolvia o estado opaco ao provider. O run começou numa rota que exige o estado
	// de volta e seguiria por um perfil que não o devolve: o estado perdia-se em silêncio.
	PinnedCauseSignedOverState = "perfil_assinado_sobre_estado_sem_fixacao"
)

// PinnedRouteProfileCauses devolve o vocabulário FECHADO das causas, pela ordem em que estão
// escritas acima.
func PinnedRouteProfileCauses() []string {
	return []string{PinnedCauseProfileDifferent, PinnedCauseModelNotServed, PinnedCauseSignedOverState}
}

// ErrPinnedRouteProfile — o run não pode seguir com o perfil de rota que este nó tem em vigor.
// Fail-closed: o pedido não sai, e o run não é re-hospedado. A causa vem a seguir
// ([PinnedRouteProfileCauseOf]).
var ErrPinnedRouteProfile = errors.New("model-gateway: o perfil de rota em vigor neste no nao e o do run")

// PinnedRouteProfileError é a recusa de um run pelo seu perfil fixado, com a causa. A mensagem
// leva só a causa: nem o nome pedido nem os digests — quem recusa di-los no seu log.
type PinnedRouteProfileError struct {
	// Cause é uma das causas de [PinnedRouteProfileCauses].
	Cause string
}

func (e *PinnedRouteProfileError) Error() string {
	return fmt.Sprintf("%v: causa=%s", ErrPinnedRouteProfile, e.Cause)
}

// Unwrap liga a recusa ao sentinela [ErrPinnedRouteProfile].
func (e *PinnedRouteProfileError) Unwrap() error { return ErrPinnedRouteProfile }

// PinnedRouteProfileCauseOf devolve a causa de uma recusa pelo perfil fixado, ou vazio se err não
// for uma.
func PinnedRouteProfileCauseOf(err error) string {
	var e *PinnedRouteProfileError
	if errors.As(err, &e) {
		return e.Cause
	}
	return ""
}

// CheckPinnedProfile confere o perfil em que um run está fixado com o que ESTE conjunto tem em
// vigor para o nome pedido do run. nil ⇒ o run pode seguir.
//
//   - pinned vazio ⇒ o run não está fixado e nunca foi re-hospedado: nada a conferir.
//   - pinned == [agentruntime.RouteProfileDigestUnpinned] ⇒ o run foi re-hospedado e começou sem
//     perfil fixado. Segue como sempre, salvo se o perfil em vigor para o seu nome pedido
//     substituiu uma entrada da tabela em código que devolvia estado
//     ([PinnedCauseSignedOverState]).
//   - qualquer outro valor é o digest fixado: o conjunto tem de ter um perfil para o nome pedido
//     ([PinnedCauseModelNotServed]) e o digest desse perfil tem de ser, byte a byte, o fixado
//     ([PinnedCauseProfileDifferent]). Um valor sem a forma de um digest não casa com nenhum, e
//     recusa.
//
// `requested` é o nome que o run pede ao proxy — o do adaptador —, e não o da rota a que o
// gateway acabe por mandar o pedido: o run fica fixado no perfil da SUA rota.
func (s *RouteProfileSet) CheckPinnedProfile(requested, pinned string) error {
	switch pinned {
	case "":
		return nil
	case agentruntime.RouteProfileDigestUnpinned:
		antes, substituida := s.substituida(requested)
		if !substituida || antes.StateReturn == StateReturnNever {
			return nil
		}
		// A entrada substituída devolvia estado. Só não se perde nada se o perfil de agora for o
		// mesmo perfil (o mesmo digest) — um candidato igual à entrada que substitui.
		if agora, tem := s.For(requested); tem && agora.Digest() == antes.Digest() {
			return nil
		}
		return &PinnedRouteProfileError{Cause: PinnedCauseSignedOverState}
	}
	agora, tem := s.For(requested)
	if !tem {
		return &PinnedRouteProfileError{Cause: PinnedCauseModelNotServed}
	}
	if agora.Digest() != pinned {
		return &PinnedRouteProfileError{Cause: PinnedCauseProfileDifferent}
	}
	return nil
}

// substituida devolve a entrada da tabela em código que um candidato deste conjunto substituiu
// para o nome pedido dado. Com o conjunto nil — a tabela em código — nada foi substituído.
func (s *RouteProfileSet) substituida(requested string) (RouteProfile, bool) {
	if s == nil {
		return RouteProfile{}, false
	}
	p, ok := s.substituidas[requested]
	return p, ok
}

// perfilFixadoNoRun devolve o digest em que o run da vista está fixado, ou vazio quando não está
// fixado em nenhum (sem valor, ou com a marca de um run re-hospedado sem fixação).
func perfilFixadoNoRun(view agentruntime.PromptView) string {
	if view.RouteProfileDigest == agentruntime.RouteProfileDigestUnpinned {
		return ""
	}
	return view.RouteProfileDigest
}
