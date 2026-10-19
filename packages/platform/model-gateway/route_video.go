package modelgateway

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A CAPACIDADE `video` (AOS-535, sub-fase A6.1).
//
// A parte de vídeo segue a regra da de imagem ([CapabilityImage], AOS-530): só sai para uma rota
// cujo perfil declare [CapabilityVideo], e a rota que conta é a RESOLVIDA — depois de qualquer
// recuo de tier ou de região —, nunca a pedida. Uma rota sem perfil não declara nada, e recusa. O
// streaming fica de fora, qualquer que seja a rota.

// ErrRouteWithoutVideo — o pedido leva uma parte de vídeo por uma via que não a aceita: a rota a
// que vai não declara a capacidade `video`, ou o pedido é em streaming. O pedido não sai: nenhuma
// credencial é buscada, nenhum selo escrito, nenhum adaptador chamado e nenhum custo contado.
var ErrRouteWithoutVideo = errors.New("model-gateway: o pedido leva uma parte de video que esta via nao aceita (rota sem a capacidade video, ou streaming) — recusado antes de sair")

// Causas de [RouteWithoutVideoError]. Vocabulário FECHADO.
const (
	// VideoCauseProfileWithout — a rota tem perfil e ele não declara `video`.
	VideoCauseProfileWithout = "perfil_sem_video"
	// VideoCauseNoProfile — a rota não tem perfil.
	VideoCauseNoProfile = "rota_sem_perfil"
	// VideoCauseStreaming — o pedido com vídeo é em streaming.
	VideoCauseStreaming = "video_em_streaming"
)

// RouteWithoutVideoError é o erro de um pedido com vídeo recusado pela capacidade da rota. Casa
// com [ErrRouteWithoutVideo] por [errors.Is]. Só leva vocabulário fechado.
type RouteWithoutVideoError struct {
	// Cause é [VideoCauseProfileWithout], [VideoCauseNoProfile] ou [VideoCauseStreaming].
	Cause string
}

// Error implementa error.
func (e *RouteWithoutVideoError) Error() string {
	return fmt.Sprintf("%v: causa=%s", ErrRouteWithoutVideo, e.Cause)
}

// Unwrap liga o erro a [ErrRouteWithoutVideo].
func (e *RouteWithoutVideoError) Unwrap() error { return ErrRouteWithoutVideo }

// recusarVideoSemCapacidade devolve [*RouteWithoutVideoError] quando o pedido leva uma parte de
// vídeo e a rota RESOLVIDA (`ex.ResolvedModel`; a pedida só quando o roteamento não a escreveu)
// não declara [CapabilityVideo]. Um pedido sem vídeo passa sempre, sem olhar para o perfil.
func (g *Gateway) recusarVideoSemCapacidade(ex *pipeline.Exchange, req port.ChatRequest) error {
	if !req.HasVideos() {
		return nil
	}
	rota := ex.ResolvedModel
	if rota == "" {
		rota = ex.RequestedModel
	}
	perfil, ok := g.perfis.For(rota)
	switch {
	case !ok:
		return &RouteWithoutVideoError{Cause: VideoCauseNoProfile}
	case !slices.Contains(perfil.Capabilities, CapabilityVideo):
		return &RouteWithoutVideoError{Cause: VideoCauseProfileWithout}
	}
	return nil
}
