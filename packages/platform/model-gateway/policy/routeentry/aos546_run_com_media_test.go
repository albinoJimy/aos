package routeentry_test

import (
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// AOS-546 (ADR-043 §2.2): um registo de uso run cujo perfil declare `imagem` ou `video` é recusado,
// mesmo com um relatório de qualificação válido — esse relatório só mede o protocolo de tools, e a
// capacidade deixaria o gateway passar a media do tail ao modelo do run sem medida (AOS-547).
func TestAOS546_RegistoRunComMediaRecusado(t *testing.T) {
	for _, caps := range []string{`["tools","imagem"]`, `["tools","video"]`, `["imagem"]`} {
		t.Run(caps, func(t *testing.T) {
			a := novoAmbiente(t)
			perfil := strings.Replace(rt.ProfileJSON, `["tools"]`, caps, 1)
			report, digest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, perfil)))
			entry := rt.Marshal(t, rt.EntryDoc(t, perfil, rt.Board, rt.Region, digest))
			rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), report)
			exigirCausa(t, a.cfg, routeentry.CauseRunProfileMedia, "r")
		})
	}
}
