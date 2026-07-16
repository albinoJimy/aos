// Package messaging implementa a assinatura e a verificação de mensagens
// inter-agente (AOS-073, EPIC-07, ADR-003). Cada mensagem trocada entre agentes é
// ASSINADA pela identidade não-humana (NHI) do emissor e VERIFICADA pelo receptor
// ANTES de agir: um agente só age sobre mensagens cuja ORIGEM, AUTORIDADE e
// REFERÊNCIA estejam criptograficamente comprovadas.
//
// # Elevação do hallucination gate
//
// No plano-base, o hallucination gate apenas verificava que um ID EXISTIA — um
// pai podia agir sobre um resumo fabricado por um sub-agente, ou sobre uma
// mensagem cuja origem fora falsificada, desde que o ID referido existisse. Este
// módulo ELEVA esse gate a autenticação de ORIGEM + AUTORIDADE + REFERÊNCIA via
// assinatura. A distinção é explícita e provada por teste (ver
// TestGateElevation_*): um emissor cujo ID EXISTE no directório mas cuja mensagem
// vem sem assinatura, ou assinada por OUTRA chave (forja), é REJEITADO — o gate
// antigo tê-lo-ia aceite. "O ID existe" deixou de bastar; exige-se a assinatura
// válida do emissor REAL, a autoridade que cobre a acção e a referência autêntica.
//
// Ressalva de rigor (tecnica/07 §7.2): a assinatura garante ORIGEM e
// NÃO-REPÚDIO (a mensagem vem mesmo daquele emissor e não foi adulterada), NÃO a
// veracidade do conteúdo — uma mensagem validamente assinada pode conter uma
// alucinação. Impedir o pai de agir sobre uma mentira exige adicionalmente
// grounding/evals (tecnica/08), fora do escopo deste ticket.
//
// # Composição e layering (sem ciclos)
//
// O módulo COMPÕE três fundações por porta, sem reimplementar nenhuma:
//
//   - IDENTIDADE (AOS-005/006): a porta [NHIRegistry] resolve a NHI CLAMADA como
//     origem para a sua chave pública PINADA (única âncora de origem) e a sua
//     autoridade AUTORITATIVA (o escopo registado da NHI, não o auto-declarado na
//     mensagem). Uma NHI desconhecida é fail-closed.
//   - BROKER/Vault (AOS-070, ADR-006): a porta [Signer] assina server-side com a
//     chave PRIVADA ed25519 da NHI do emissor. A chave privada é gerida pelo
//     broker/Vault e NUNCA entra neste módulo nem no runtime do agente — [Signer]
//     recebe o digest canónico e devolve só a assinatura, espelhando o padrão do
//     Vault (o segredo nunca regressa ao chamador). Nenhuma chave privada em
//     código: só chaves públicas pinadas para verificação, e seeds efémeras nos
//     testes.
//   - AUDIT tamper-evident (AOS-072, ADR-010): cada REJEIÇÃO (mensagem forjada,
//     assinatura inválida, referência inexistente/inautêntica, autoridade
//     insuficiente, replay/frescura) sela um evento na cadeia WORM. A ATRIBUIÇÃO
//     depende de a origem já estar autenticada no momento da rejeição: uma rejeição
//     PÓS-assinatura é atribuída ao emissor REAL (na sua partição); uma rejeição
//     PRÉ-assinatura (forma inválida/origem desconhecida/forjada) vai para uma
//     partição de QUARENTENA sem principal autenticado, com a origem clamada apenas
//     como CLAIM — assim um flood de forjas com Origin=vítima não polui a cadeia
//     atribuível da vítima. Importado concretamente (zero-dep).
//
// # Anti-replay (frescura + dedup de nonce)
//
// A assinatura autentica a ORIGEM mas não a FRESCURA: sem mais, uma mensagem
// legítima capturada verificaria indefinidamente. Por isso cada mensagem carrega,
// COBERTOS pela assinatura, um Nonce único e um IssuedAt. Depois de a origem estar
// autenticada, o Verifier impõe uma janela de frescura sobre o IssuedAt (rejeita
// mensagens demasiado antigas ou com timestamp futuro além do skew) e deduplica o
// par (Origin, Nonce) num seen-set — reenviar a mesma mensagem é rejeitado
// ([ErrReplayedNonce]). O seen-set é podado pelos limites da janela e protegido por
// mutex (verificação concorrente-segura). A dedup deste módulo fecha o vetor de
// captura-e-reenvio ao nível da AUTENTICAÇÃO inter-agente; a idempotência de
// EFEITOS a jusante (AOS-014) é uma camada complementar, não um substituto.
//
// # Observabilidade
//
// A decisão de verificação é coberta por um span [OpMessageVerify] (porta [Tracer]
// zero-dep, default [NoopTracer]; o SDK OTel é EPIC-08), com atributos NÃO-secretos
// (origem clamada, acção, referência, decisão, motivo) — nunca o payload/chaves.
//
// # Composição/wiring (deferido a um composition root)
//
// Este módulo entrega a BIBLIOTECA de assinatura/verificação e as suas portas
// ([Signer], [NHIRegistry], [ReferenceResolver], [Tracer]); os adaptadores
// concretos que as ligam à identidade NHI real (AOS-005/006), ao broker/Vault
// (AOS-070) e à fonte real de sub-resultados, e a integração no ponto real de troca
// de mensagens (RT/ORQ), pertencem ao composition root (packages/integration) e
// ficam DEFERIDOS — este branch não os inclui para não puxar o grafo de
// identity/broker/orquestrador para dentro do módulo (evita ciclos; ver go.mod).
// Enquanto o wiring não existir, a propriedade "toda a mensagem inter-agente é
// assinada/verificada" está garantida ao nível da biblioteca, não observável num
// canal real — o critério 1 de AOS-073 NÃO deve ser lido como já integrado.
//
// Nada importa messaging, logo não há ciclo. A serialização canónica é
// determinista (ordem de campos fixa, length-prefixing, autoridade ordenada). A
// frescura usa um relógio INJECTÁVEL (determinístico nos testes) e o seen-set é
// mutex-protegido, pelo que os testes são -race limpos.
package messaging
