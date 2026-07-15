// Caso MAU: um consumidor externo que importa o pacote de ADAPTADORES do próprio
// Model Gateway para construir um adaptador e a credencial e falar com o provedor
// directamente — bypass da porta [port.Gateway]. A garantia forte é estrutural
// (os adaptadores vivem sob model-gateway/internal/, logo isto NÃO compila fora
// do módulo), mas o arch-lint deve sinalizá-lo em defesa-em-profundidade.
package badgwadapters

import (
	adapters "github.com/aos-ref/platform/model-gateway/adapters" // want: import de adaptadores do GW
)

// bypass constrói uma credencial via o pacote de adaptadores do GW — VIOLAÇÃO
// (provider-sdk-import): salta o gate do Model Gateway.
func bypass() any {
	return adapters.NewCredential("openai", "eu", "sk-secreto")
}
