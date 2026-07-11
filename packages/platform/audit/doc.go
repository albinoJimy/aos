// Package audit implementa a base de audit tamper-evident do AOS (AOS-011,
// ADR-010): um registo de responsabilização (accountability) encadeado por hash
// criptográfico, fisicamente separado do Event Store (AOS-002) e dos
// diagnósticos efémeros.
//
// # O que o audit responde
//
// Onde o Event Store responde "o que aconteceu no run", o audit responde "quem
// autorizou e sob que política". Cada decisão do PDP mediada pelo Reference
// Monitor (AOS-003) produz um [AuditRecord] append-only, encadeado ao anterior
// pelo hash — tornando qualquer adulteração (mutação, remoção ou inserção)
// detectável por re-verificação da cadeia.
//
// # Hash-chain (SHA-256)
//
//	EntryHash = SHA-256( PrevHash || serialização_canónica_determinística(conteúdo) )
//
// onde conteúdo são todos os campos do registo EXCEPTO o próprio EntryHash. O
// PrevHash do primeiro registo de cada partição é uma génese fixa e determinística
// (ver [GenesisHash]). O encadeamento é contíguo (gapless) por partição: audit_seq
// começa em 1 e incrementa de 1. A serialização é canónica, com ordem de campos
// fixa e length-prefixing, portanto estável cross-SO (ver [record.go]).
//
// # Verificação
//
//   - [Verify] percorre a cadeia de uma partição de audit_seq=from a to e detecta
//     com 100% de fiabilidade qualquer adulteração INTERNA ao intervalo: mutação (o
//     EntryHash recalculado diverge do armazenado), remoção interna e inserção (o
//     encadeamento por PrevHash e/ou a contiguidade de audit_seq quebram). A
//     truncatura do TAIL (remover os registos mais recentes) NÃO é detectável por
//     Verify(..., Head()), porque `to` é limitado ao head reportado pelo store; só
//     um [Checkpoint] assinado no head esperado (ou um `to` conhecido de forma
//     independente) a expõe. Devolve um [*VerifyError] sentinela que identifica o
//     registo e o tipo de adulteração.
//   - [VerifyFromCheckpoint] valida primeiro a assinatura de um [Checkpoint]
//     (âncora de confiança ed25519) e depois verifica a cadeia SÓ de cp+1 a to,
//     permitindo verificação eficiente de grandes intervalos sem reprocessar
//     desde a génese.
//
// # WORM e crypto-shredding (interfaces estáveis, EPIC-08/09)
//
// O [Store] é append-only por contrato: não expõe update nem delete. A
// implementação de referência [MemStore] é in-memory; produção usa storage WORM
// real por trás da MESMA interface (esta é a fronteira estável). O EntryHash é
// calculado sobre o ContentHash do payload (ver [PayloadRef]), NUNCA sobre o
// plaintext: destruir a chave por titular (KeyRef) torna o payload ilegível SEM
// quebrar a cadeia — o hash sela ciphertext/metadados, não os dados pessoais.
// KMS e storage WORM real NÃO são implementados aqui (EPIC-08/09); só as
// interfaces estáveis que os acomodam.
//
// # Integração com o Reference Monitor
//
// [MediationSink] implementa a porta [referencemonitor.EventSink]: cada decisão
// final da mediação (allow/deny/escalate) entra na hash-chain com principal,
// capability, policy_version, a correlação run_id/step_id/parent_step_id/
// request_id, o alvo tool_id/resource, o contexto de decisão e as obligations —
// todos selados no EntryHash, portanto tamper-evident (não apenas registados no
// Event Store). É a alteração ZERO ao RM — o sink já recebe a decisão final
// pós-cadeia.
//
// Para ter SIMULTANEAMENTE o Event Store durável (AOS-002/009) e a cadeia
// tamper-evident sem escolher um, componha-os com [TeeSink] (fan-out fail-closed):
// se qualquer destino falhar num permit, o RM degrada a decisão para Deny.
package audit
