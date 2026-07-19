package otelgenai

// operational_alerts_embed.go — o artefacto de ALERTAS operacionais VERSIONADO
// embebido no binário (alerting-as-code, AOS-105 AC5). O ficheiro
// operational_alerts.json é gerado a partir de [DefaultOperationalAlertConfig] e vive
// no repo: prova que o catálogo de alertas é reproduzível a partir do código (o teste
// de round-trip assegura que o ficheiro e o código não divergem). Sem segredos — só
// nomes de SLI/alerta, severidades, runbooks/owners e janelas.

import _ "embed"

//go:embed operational_alerts.json
var embeddedOperationalAlertsJSON []byte

// EmbeddedOperationalAlertsJSON devolve o JSON versionado do catálogo de alertas
// operacionais (cópia defensiva do embed). É o artefacto alerting-as-code tal como
// versionado no repo.
func EmbeddedOperationalAlertsJSON() []byte {
	out := make([]byte, len(embeddedOperationalAlertsJSON))
	copy(out, embeddedOperationalAlertsJSON)
	return out
}

// EmbeddedOperationalAlertConfig carrega e VALIDA o catálogo de alertas embebido
// (fail-closed). É o ponto de entrada de produção: os alertas operacionais
// reproduzíveis a partir do artefacto versionado, não configurados à mão.
func EmbeddedOperationalAlertConfig() (OperationalAlertConfig, error) {
	return LoadOperationalAlertConfig(embeddedOperationalAlertsJSON)
}
