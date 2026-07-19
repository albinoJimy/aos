// Package backup implementa o backup IMUTÁVEL e contínuo do Event Store e o
// Point-In-Time Recovery (PITR) validado — AOS-101 (EPIC-10, ADR-007/010/011/006).
//
// # Modelo
//
// A replicação por quórum (AOS-100) protege contra a falha de um nó, mas não
// contra corrupção lógica, apagamento acidental ou desastre regional. Este módulo
// acrescenta um segundo eixo de durabilidade:
//
//   - EXPORTAÇÃO contínua/incremental: a cada ciclo, o [Exporter] lê um snapshot
//     consistente do Event Store (porta eventstore.BackupSource, envelope intacto),
//     cifra os eventos novos EM REPOUSO (envelope AES-256-GCM sob uma KEK do
//     audit.KeyVault) e escreve-os como um SEGMENTO IMUTÁVEL no [ImmutableStore]
//     (object-lock/WORM). Cada segmento é encadeado num MANIFESTO hash-chain
//     (molde de audit.ComputeEntryHash) cujo head é selado num [Checkpoint]
//     assinado (ed25519). O Event Store não tem cadeia nativa — ela é construída
//     aqui sobre os segmentos.
//
//   - PITR: o [Restorer] reconstrói um Event Store até um seq-alvo por stream,
//     VERIFICANDO o manifesto hash-chain no processo (uma adulteração de um
//     segmento é detectada) e reinserindo os eventos com o ENVELOPE preservado
//     (porta eventstore.RestoreSink). Devolve EVIDÊNCIA do restauro (AC6).
//
// # Soberania (ADR-011), fail-closed
//
// O destino do backup tem uma região. Se cruza a fronteira do board de soberania
// do Store (região diferente, ausente ou desconhecida) o backup é RECUSADO com
// [ErrSovereigntyViolation] — backups e cópias NUNCA cruzam a fronteira regional.
//
// # Cifra em repouso (ADR-006)
//
// Nenhum plaintext de payload é escrito no [ImmutableStore]: cada segmento é um
// envelope AES-256-GCM (DEK fresca por segmento embrulhada pela KEK do titular do
// backup). A KEK vive no audit.KeyVault (porta de KMS/Vault); a chave privada de
// assinatura de checkpoints vive fora do repositório. Não há segredos no código.
//
// # Modelo de referência
//
// object-lock/WORM real e KMS real são BACKENDS DE PRODUÇÃO, modelados aqui por
// PORTAS injectáveis com implementações de referência in-memory/zero-dep. Sem
// dependências externas.
package backup
