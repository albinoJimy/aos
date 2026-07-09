// Package delegation implementa a cadeia de delegação on-behalf-of do AOS
// (AOS-006): uma sequência ORDENADA de elos hash-encadeados que liga o agente
// actual, subindo por cada delegação, até um humano responsável na raiz.
//
// A cadeia materializa o requisito de governação "The Audit Log Lied": quando o
// regulador pergunta quem autorizou uma acção, a resposta nunca é "o pool" — é
// sempre o human_principal único na raiz (ADR-003, tecnica/09 §3).
//
// # Modelo
//
// Cada elo ([Link]) declara quem delega (Sub), para quem (ActAs), a autoridade
// concedida nesse elo (Authority), a profundidade (Depth) e o hash do elo
// anterior (PrevHash). A [Chain] é ordenada da RAIZ (Sub = "human:<user_id>")
// até à FOLHA (o agente actual). Encadear cada elo pelo hash do anterior torna a
// ORDEM da cadeia tamper-evident: reordenar, inserir ou remover um elo quebra o
// encadeamento.
//
// # Invariantes verificadas ([Chain.Verify])
//
//   - raiz humana: o primeiro elo tem Sub com prefixo "human:"; caso contrário a
//     cadeia é ÓRFÃ ([ErrOrphanChain]) e é rejeitada fail-closed;
//   - profundidade monotónica: Depth começa em 0 e incrementa +1 por elo;
//   - continuidade: PrevHash de cada elo é exactamente o hash do elo anterior
//     (o primeiro elo tem PrevHash vazio — âncora de génese);
//   - não-escalada: a autoridade de um elo é sempre um SUBCONJUNTO da do elo
//     anterior (Authority(i) ⊆ Authority(i-1)) — a autoridade só pode estreitar
//     ao descer a cadeia, nunca alargar.
//
// # Modelo de confiança
//
// A cadeia NÃO é auto-suficiente criptograficamente: os hashes só provam a
// INTEGRIDADE DA ORDEM interna. A AUTENTICIDADE (que estes elos foram de facto
// emitidos por quem afirma) vem de a cadeia inteira ir embebida nos claims do
// token NHI e ser SELADA pela assinatura ed25519 do emissor (AOS-005). Assim,
// "cada elo assina o seguinte" está ancorado na assinatura única do emissor
// sobre a cadeia completa. Ver README.md para o endurecimento futuro (chave
// própria por principal).
//
// Este pacote é puro (stdlib apenas: crypto/sha256, encoding/json) e não importa
// o pacote identity — é este que o importa para embeber/validar a cadeia.
package delegation
