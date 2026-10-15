package modelgateway

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A CAPACIDADE `imagem` (AOS-530, ADR-042 §2.4, barreira 2).
//
// Uma parte de imagem no pedido só sai para uma rota cujo perfil declare [CapabilityImage]. A
// rota é a RESOLVIDA — a que o roteamento escolheu, depois de qualquer recuo de tier ou de região
// —, e não a pedida: um pedido a um alias com `imagem` que o roteamento mande para uma rota sem
// ela é recusado, e nunca cai em silêncio para essa rota. Uma rota sem perfil não declara nada, e
// também recusa.

// ErrRouteWithoutImage — o pedido leva uma parte de imagem e a rota a que vai não declara a
// capacidade `imagem`. O pedido não sai: nenhum adaptador é chamado e nenhum custo é contado.
var ErrRouteWithoutImage = errors.New("model-gateway: o pedido leva uma parte de imagem e a rota nao declara a capacidade imagem — recusado antes de sair")

// Causas de [RouteWithoutImageError]. Vocabulário FECHADO.
const (
	// ImageCauseProfileWithout — a rota tem perfil e ele não declara `imagem`.
	ImageCauseProfileWithout = "perfil_sem_imagem"
	// ImageCauseNoProfile — a rota não tem perfil.
	ImageCauseNoProfile = "rota_sem_perfil"
)

// RouteWithoutImageError é o erro de um pedido com imagem recusado pela capacidade da rota. Casa
// com [ErrRouteWithoutImage] por [errors.Is]. Só leva vocabulário fechado.
type RouteWithoutImageError struct {
	// Cause é [ImageCauseProfileWithout] ou [ImageCauseNoProfile].
	Cause string
}

// Error implementa error.
func (e *RouteWithoutImageError) Error() string {
	return fmt.Sprintf("%v: causa=%s", ErrRouteWithoutImage, e.Cause)
}

// Unwrap liga o erro a [ErrRouteWithoutImage].
func (e *RouteWithoutImageError) Unwrap() error { return ErrRouteWithoutImage }

// recusarImagemSemCapacidade devolve [*RouteWithoutImageError] quando o pedido leva uma parte de
// imagem e a rota RESOLVIDA (`ex.ResolvedModel`; a pedida só quando o roteamento não a escreveu)
// não declara [CapabilityImage]. Um pedido sem imagens passa sempre, sem olhar para o perfil.
func (g *Gateway) recusarImagemSemCapacidade(ex *pipeline.Exchange, req port.ChatRequest) error {
	if !req.HasImages() {
		return nil
	}
	rota := ex.ResolvedModel
	if rota == "" {
		rota = ex.RequestedModel
	}
	perfil, ok := g.perfis.For(rota)
	switch {
	case !ok:
		return &RouteWithoutImageError{Cause: ImageCauseNoProfile}
	case !slices.Contains(perfil.Capabilities, CapabilityImage):
		return &RouteWithoutImageError{Cause: ImageCauseProfileWithout}
	}
	return nil
}
