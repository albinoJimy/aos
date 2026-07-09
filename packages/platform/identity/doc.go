// Package identity implementa a identidade não-humana por agente (NHI) do AOS
// (AOS-005, ADR-003): a segunda fundação não-negociável — identidade ANTES de
// autoridade.
//
// Cada agente e sub-agente é uma NHI única, portadora de um token scoped e
// time-bound que codifica o par (utilizador, agente), a classe/política sob a
// qual actua e o escopo (capabilities/recursos) que pode exercer. O token é
// assinado com EdDSA (ed25519) num envelope JWS compacto (base64url), construído
// exclusivamente sobre a stdlib — sem dependências JWT externas.
//
// Três componentes:
//
//   - [Issuer] emite tokens. O TTL e o escopo-máximo são configuráveis POR CLASSE
//     de agente; a autoridade efectiva embutida no token é a INTERSECÇÃO
//     utilizador ∩ classe (nunca alarga). Em delegação on-behalf-of o escopo do
//     filho é ⊆ escopo do pai. A emissão grava um evento identity.nhi.issued no
//     Event Store (só metadados — nunca o token bearer nem a assinatura).
//
//   - [Verifier] valida o token: assinatura (contra a chave pública do emissor —
//     o trust anchor), janela temporal (nbf/exp, com relógio injectável) e
//     revogação. Rejeita fail-closed assinatura inválida, alg/none confusion,
//     token expirado, ainda-não-válido, emissor desconhecido e revogado. Em
//     sucesso resolve um [Principal].
//
//   - [Revocations] mantém o conjunto de jti revogados e grava identity.nhi.revoked
//     no Event Store. O TTL curto minimiza a janela entre revogação e expiração.
//
// A integração com o Reference Monitor (AOS-003) é o hook [IdentityCheck], que
// ocupa o ponto de injecção "identity": lê o token do Call, verifica-o, resolve
// o Principal (mutação do *Call partilhado) e devolve permit; SEM NHI resolvida
// (token ausente, inválido, expirado, fora de escopo ou revogado) devolve DENY.
// É a proibição de identidade anónima/round-robin: nenhuma chamada mediada
// prossegue sem NHI (ADR-003).
package identity
