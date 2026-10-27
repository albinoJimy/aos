package modelgateway

import (
	"context"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A MEDIA NO TURNO DO MODELO DO RUN (AOS-546, ADR-043 §2.2).
//
// Um tail da 1.7.0 pode ter segmentos [agentruntime.TailMedia]: a referência (documento, digest,
// tipo, tamanho) a uma imagem que uma tool local leu. Este ficheiro é o ÚNICO sítio do adaptador
// dos turnos que põe partes de media num pedido: resolve os bytes pela referência, confere-os e
// anexa-os à mensagem `user` do segmento. Quem pode receber a imagem continua a ser decidido pelo
// gateway, que recusa um pedido com imagem para uma rota cujo perfil não declare `imagem`.
//
// FAIL-CLOSED. Sem resolvedor, com bytes que já não existem, ou com bytes cujo digest, tipo ou
// tamanho não batem com a referência, o turno falha com [ErrMediaUnavailable] — nunca sai sem a
// imagem que o modelo foi avisado de ter junto, nem com outra.

// MediaResolver devolve os bytes do media de uma referência. É composto por quem conhece o sítio
// de onde a tool os leu (no nó, a pasta semeada e o espaço de handles do run).
//
// O `runID` É O DO TURNO, e é parte do contrato desde o AOS-563: no nó, uma referência pode nomear
// uma ENTRADA DE MEDIA do próprio run — bytes que quem submeteu mandou no `POST /runs` e que o nó
// selou no espaço de handles, cifrados com o run nos dados associados. Sem o run, o resolvedor não
// tem como os abrir, e a mesma referência resolveria na pasta semeada do nó ou em nada.
//
// Vem como ARGUMENTO e não dentro da [agentruntime.MediaRef]: a referência é o RESULTADO de uma
// tool call e viaja no tail e no log do run, pelo que um campo novo nela seria uma mudança de
// contrato persistido — e o run não pertence à referência, pertence ao turno que a está a resolver.
type MediaResolver func(ctx context.Context, runID string, ref agentruntime.MediaRef) (port.MediaPart, error)

// WithMediaResolver liga ao adaptador o resolvedor da media do tail (AOS-546). Sem ele, um turno
// cujo tail tenha media falha com [ErrMediaUnavailable].
func WithMediaResolver(r MediaResolver) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.media = r }
}

// ErrMediaUnavailable — a media de um segmento do tail não pôde ser anexada ao pedido. O pedido
// não sai. [*MediaUnavailableError] casa com este erro por [errors.Is].
var ErrMediaUnavailable = errors.New("modelgateway: media do tail indisponivel")

// Causas de [MediaUnavailableError]. Vocabulário FECHADO.
const (
	// MediaCauseNoResolver — o tail tem media e o adaptador não tem resolvedor.
	MediaCauseNoResolver = "sem_resolvedor"
	// MediaCauseUnreadable — o resolvedor não devolveu os bytes (desapareceram, ou não se leem).
	MediaCauseUnreadable = "media_ilegivel"
	// MediaCauseMismatch — os bytes não batem com a referência (digest, tipo ou tamanho).
	MediaCauseMismatch = "media_nao_bate_com_a_referencia"
	// MediaCauseRejected — os bytes não passam a validação da parte da sua classe.
	MediaCauseRejected = "media_recusada"
)

// MediaUnavailableError é o erro de uma media do tail que não se anexou. Casa com
// [ErrMediaUnavailable].
type MediaUnavailableError struct {
	Cause string
}

// Error implementa error.
func (e *MediaUnavailableError) Error() string {
	return fmt.Sprintf("%v: causa=%s", ErrMediaUnavailable, e.Cause)
}

// Unwrap liga o erro a [ErrMediaUnavailable].
func (e *MediaUnavailableError) Unwrap() error { return ErrMediaUnavailable }

// ProjectNativeVersionWithMedia é [ProjectNativeVersion] com a media do tail anexada às mensagens
// `user` dos seus segmentos, resolvida por resolve. Um tail sem media dá exactamente as mensagens
// de [ProjectNativeVersion].
//
// `ctx` e `runID` são os do turno, e passam ao resolvedor — ver [MediaResolver].
func ProjectNativeVersionWithMedia(ctx context.Context, version string, view agentruntime.PromptView, runID string, resolve MediaResolver) ([]port.Message, error) {
	msgs, media, err := projetarNativo(version, view)
	if err != nil {
		return nil, err
	}
	for i, refs := range media {
		if err := anexarMedia(ctx, &msgs[i], runID, refs, resolve); err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

// mediaDoTail devolve as referências a media do tail, pela ordem — as que o pedido em texto único
// leva na sua única mensagem `user`.
func mediaDoTail(view agentruntime.PromptView) []agentruntime.MediaRef {
	var refs []agentruntime.MediaRef
	for _, seg := range view.Tail {
		if ref, ok := agentruntime.MediaOfSegment(seg); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

// anexarMedia resolve cada referência, confere os bytes contra ela e anexa-os à mensagem.
func anexarMedia(ctx context.Context, m *port.Message, runID string, refs []agentruntime.MediaRef, resolve MediaResolver) error {
	if len(refs) == 0 {
		return nil
	}
	if resolve == nil {
		return &MediaUnavailableError{Cause: MediaCauseNoResolver}
	}
	for _, ref := range refs {
		parte, err := resolve(ctx, runID, ref)
		if err != nil {
			return &MediaUnavailableError{Cause: MediaCauseUnreadable}
		}
		if parte.Digest() != ref.SHA256 || parte.MediaType != ref.MediaType || int64(len(parte.Data)) != ref.Bytes {
			return &MediaUnavailableError{Cause: MediaCauseMismatch}
		}
		if err := m.AttachMedia(parte); err != nil {
			return &MediaUnavailableError{Cause: MediaCauseRejected}
		}
	}
	return nil
}
