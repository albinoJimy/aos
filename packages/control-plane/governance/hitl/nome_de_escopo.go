package hitl

// nome_de_escopo.go — UM ESCOPO DE PROVENIÊNCIA ARBITRÁRIA NÃO ENTRA INTEIRO NUM NOME DE STREAM.
//
// # O DEFEITO QUE ISTO FECHA
//
// Dois streams deste pacote namespaceiam-se por um `scope`: o do nonce de uso-único
// (`ratify-nonce:<scope>:<hex>`) e o do challenge four-eyes (`4eyes-challenge:<scope>:<hex>`).
// O `scope` não é escolhido aqui — chega de fora, e as suas fontes são três:
//
//   - CONSTANTES de domínio do autenticador: `foureyes.challenge`, `governance.dsar`,
//     `nhi.revoke`. **Têm ponto.**
//   - O `request_id` do CORPO DE UM PEDIDO, via `integration.ChallengeScope`. Um cliente
//     escolhia parte de um nome de stream.
//   - O tuplo `(domínio, emissor)` do `nonceScope`, junto com um `\x00` como separador. **Um
//     byte de CONTROLO ia inteiro para o nome do stream.**
//
// Sobre o substrato de ficheiro nada disto falha, e é por isso que sobreviveu a dez gates, a uma
// revisão adversarial e ao smoke. Sobre JetStream o `subjectDe` recusa, `ConsumeNonce` devolve
// erro de backend, o gate trata-o como bloqueio — e **toda a emissão de challenges e toda a
// ratificação passam a ser negadas**, com um 403 que diz «aprovador não autorizado» e não diz
// que o nome do stream é que era irrepresentável.
//
// Foi o aperto do [eventstore.Store.Append] (AOS-424) que o revelou: quinze testes do nó
// passaram a falhar, e o 403 era a ponta visível.
//
// # PORQUÊ RESUMIR, E NÃO ESCAPAR NEM RECUSAR
//
// **Recusar** obrigaria a fazer os três domínios stream-safe (uma alteração de constantes que
// muda a separação de domínio das ASSINATURAS) e a recusar um `request_id` que hoje é aceite —
// uma quebra de contrato do lado do cliente, a meio de uma cerimónia humana.
//
// **Escapar** manteria a legibilidade, mas o alfabeto de entrada aqui é arbitrário (um
// `RatificationID` é um token opaco de fonte externa), e uma marca de escape que pertença ao
// alfabeto de entrada colide — foi exactamente o erro que o escape do `node_id` cometeu e que o
// seu teste de injectividade apanhou.
//
// **Resumir** dá a propriedade que interessa, que é a única que estes nomes precisam de ter:
// escopos distintos ⇒ streams distintos, escopo igual ⇒ stream igual. E dá-a para QUALQUER
// entrada, sem lista de proibidos e sem alfabeto.
//
// O que se perde é legibilidade, e o custo é baixo: estes streams existem para um
// check-and-set de uso-único, ninguém os lê por nome, e o prefixo (`ratify-nonce:` /
// `4eyes-challenge:`) continua a dizer de que classe são.
//
// # O QUE ISTO NÃO É
//
// Não é segurança — o escopo não é segredo e o resumo não o esconde de ninguém que o saiba
// adivinhar. É representabilidade. O que impede o adivinhar é o nonce/challenge aleatório que
// vai no resto do nome, e esse não mudou.

import (
	"crypto/sha256"
	"encoding/hex"
)

// nomeDeEscopo devolve um segmento de `stream_id` sempre representável para um `escopo` de
// proveniência arbitrária, preservando a injectividade que o namespacing exige.
//
// A colisão é resistida pelo SHA-256: dois escopos distintos com o mesmo nome exigiriam uma
// colisão da função. Isso importa porque uma colisão AQUI não seria um bypass — dois escopos no
// mesmo stream fazem o segundo consumo ver um replay e NEGAR —, mas seria uma negação silenciosa
// de uma cerimónia legítima, e essas são as que ninguém diagnostica.
func nomeDeEscopo(escopo string) string {
	soma := sha256.Sum256([]byte(escopo))
	return hex.EncodeToString(soma[:])
}
