package modelgateway

import (
	"context"
	"fmt"
	"time"

	"github.com/aos-ref/platform/audit"
)

// O PERFIL QUE VEIO DE UM REGISTO DE ENTRADA ASSINADO (AOS-519, emenda ao ADR-036 §2.8).
//
// O gateway recebe os perfis pela porta de sempre ([ProductionConfig.RouteProfiles]) e não sabe
// ler registos nem conferir assinaturas: isso é de quem lhos entrega (o nó, pelo pacote
// policy/routeentry). O que o gateway faz é DEIXAR PROVA: cada perfil que veio de um registo fica
// selado no audit de governação, com os três digests, para que do log se reconstrua que perfil
// serviu que nome, e desde quando.

// RouteProfileEntry identifica o registo de entrada assinado de onde veio um perfil. Só leva
// identificadores e digests.
type RouteProfileEntry struct {
	// Requested é o nome pedido do perfil.
	Requested string
	// ProfileDigest é o digest do perfil ([RouteProfile.Digest]).
	ProfileDigest string
	// EntryDigest é o digest do registo de entrada — o que a assinatura do dono cobre.
	EntryDigest string
	// ReportDigest é o digest do relatório de qualificação que o registo cita.
	ReportDigest string
}

// routeProfileSealer é o que o selo do carregamento precisa do recorder de governação.
type routeProfileSealer interface {
	SealRouteProfileLoaded(ctx context.Context, at time.Time, requested, expectedModel, profileDigest, entryDigest, reportDigest string) (audit.AuditRecord, error)
}

// sealRouteProfileEntries confere que cada registo corresponde a um perfil do conjunto com o mesmo
// nome pedido e o mesmo digest, e sela-o. Sem registos é no-op: um gateway sem perfis assinados
// não escreve nada a mais no audit.
func sealRouteProfileEntries(ctx context.Context, sealer routeProfileSealer, at time.Time, perfis *RouteProfileSet, entries []RouteProfileEntry) error {
	vistos := map[string]bool{}
	for _, e := range entries {
		perfil, tem := perfis.For(e.Requested)
		if !tem || perfil.Digest() != e.ProfileDigest || e.EntryDigest == "" || e.ReportDigest == "" {
			return fmt.Errorf("%w: um registo de entrada nao corresponde a nenhum perfil candidato com o mesmo nome pedido e o mesmo digest", ErrBadRouteProfile)
		}
		if vistos[e.Requested] {
			return fmt.Errorf("%w: dois registos de entrada para o mesmo nome pedido", ErrBadRouteProfile)
		}
		vistos[e.Requested] = true
	}
	// Só depois de TODOS conferidos é que se escreve: uma lista má não deixa selos a meio.
	for _, e := range entries {
		perfil, _ := perfis.For(e.Requested)
		if _, err := sealer.SealRouteProfileLoaded(ctx, at, e.Requested, perfil.ExpectedModel, e.ProfileDigest, e.EntryDigest, e.ReportDigest); err != nil {
			return fmt.Errorf("modelgateway: o carregamento de um perfil de rota assinado nao foi selado (fail-closed): %w", err)
		}
	}
	return nil
}
