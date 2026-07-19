package otelgenai

// catalog_embed.go — o artefacto de dashboard VERSIONADO embebido no binário
// (dashboard-as-code, AC6). O ficheiro operational_dashboard.json é gerado a partir
// de [DefaultDashboardCatalog] e vive no repo: prova que o dashboard é reproduzível a
// partir do código (o teste de round-trip assegura que o ficheiro e o código não
// divergem). Sem segredos — só nomes de métrica, SLOs e janelas.

import _ "embed"

//go:embed operational_dashboard.json
var embeddedDashboardJSON []byte

// EmbeddedDashboardJSON devolve o JSON versionado do dashboard operacional (uma cópia
// defensiva do embed). É o artefacto dashboard-as-code tal como versionado no repo.
func EmbeddedDashboardJSON() []byte {
	out := make([]byte, len(embeddedDashboardJSON))
	copy(out, embeddedDashboardJSON)
	return out
}

// EmbeddedDashboardCatalog carrega e VALIDA o catálogo embebido (fail-closed). É o
// ponto de entrada de produção: o dashboard operacional reproduzível a partir do
// artefacto versionado, não configurado à mão.
func EmbeddedDashboardCatalog() (DashboardCatalog, error) {
	return LoadDashboardCatalog(embeddedDashboardJSON)
}
