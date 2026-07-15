// Caso MAU: código de consumidor externo que CONTORNA o Model Gateway, falando
// com um provedor de LLM directamente. O analisador DEVE sinalizar as duas
// violações abaixo: um import de SDK de provedor e um endpoint hard-coded.
package bad

import (
	"net/http"

	openai "github.com/sashabaranov/go-openai" // want: import de SDK de provider
)

// providerEndpoint é o endpoint do provedor referenciado directamente — VIOLAÇÃO
// (provider-endpoint-literal): a chamada sai fora do GW.
const providerEndpoint = "https://api.openai.com/v1/chat/completions" // want: endpoint de provider

// chamarProviderDirecto usa o SDK do provedor directamente — VIOLAÇÃO
// (provider-sdk-import): salta o gate do Model Gateway.
func chamarProviderDirecto(key string) *openai.Client {
	return openai.NewClient(key)
}

// postDirecto faz POST directo ao endpoint do provedor.
func postDirecto() (*http.Response, error) {
	return http.Post(providerEndpoint, "application/json", nil)
}
