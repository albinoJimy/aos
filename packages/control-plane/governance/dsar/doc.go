// Package dsar implementa o FLUXO DSAR de apagamento (GDPR Art. 17, AOS-093) — o
// componente GOV que satisfaz o "direito ao apagamento" por CRYPTO-SHREDDING, sem
// reescrever o log encadeado nem quebrar a cadeia de hashes do audit (ADR-011/010).
//
// # O que este pacote NÃO faz
//
// Não reimplementa a cifra por-titular, o KeyVault nem o Shredder do audit
// (AOS-083), nem o KeySource de tokenização da redação (AOS-091). Toda essa
// mecânica de crypto-shredding já existe. Este pacote ORQUESTRA-A: compõe, por
// porta, os stores de PII existentes numa erasure UNIFICADA por-titular.
//
// # O fluxo
//
// [Flow.Receive] recebe um [Request] (subjectID pseudónimo, NUNCA o valor pessoal) e:
//
//  1. sela dsar.received na hash-chain (metadados de conformidade, sem PII);
//  2. consulta o legal hold ([HoldOracle], subject OU partição) — se retido, sela
//     dsar.blocked e PARA fail-closed (a chave é preservada, nada é destruído);
//  3. senão, destrói a chave do titular em CADA [ShreddableKeyStore] ligado (o
//     KeyVault do audit E o KeySource da redação) — para não deixar PII recuperável
//     num store esquecido;
//  4. sela dsar.key_destroyed (timestamp + rótulos dos stores, sem PII nem chave).
//
// É idempotente: re-submeter um DSAR de um titular já apagado não falha nem
// re-destrói indevidamente (o shred de uma chave ausente é no-op).
//
// # Isolamento do segredo (ADR-006)
//
// A chave por-titular NUNCA é devolvida, logada nem colocada num span/evento: o
// shred opera server-side (os stores já não expõem a chave) e os adaptadores
// descartam qualquer material devolvido. Os eventos e spans carregam só o
// subjectID/rótulos/timestamps — nunca a chave nem o valor pessoal.
//
// # Integridade (ADR-010)
//
// O fluxo NÃO toca no log encadeado dos registos do titular: a cadeia sela o hash
// do CIPHERTEXT, pelo que destruir a chave torna a PII irrecuperável mas mantém
// audit.Verify válido ANTES e DEPOIS do shred. Os metadados não-pessoais (quem fez
// o quê, quando) são preservados como facto de conformidade.
package dsar
