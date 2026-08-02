// Package planadversarial é a SUITE DE SEGURANÇA ADVERSARIAL DO PLANO (AOS-244,
// EPIC-18 / tecnica/18 §9). Não introduz mecanismo novo: PROVA, exercitando os
// pacotes REAIS já entregues (plan, planvalidate, intake, plandispatch), que os
// vectores adversariais do «plano enquanto vector» (§8) estão FECHADOS.
//
// Cada vector é um TESTE NEGATIVO que ataca as fronteiras reais e mapeia a UMA
// linha da tabela de riscos de tecnica/18 §9. Um falso-negativo — um vector
// aberto — FALHA o gate. O estilo é o guard-test das negações do
// composition-root: o ataque é montado, executado contra o código de produção, e
// o teste afirma que o efeito indevido NÃO acontece (com a «falha-antes»
// documentada em cada teste).
//
// Mapa vector → §9:
//
//   - PlanoAdversarial     → §9 «Plano adversarial (objectivo/untrusted induz
//     organigrama hostil)»: plano é DADOS (ADR-005), validação pura barra-o antes
//     de qualquer efeito, e o spawn é mediado a jusante do gate.
//   - DowngradeDeRisco     → §9 «gate humano com risco RESOLVIDO» (regra 6,
//     AOS-232): o rótulo `safe` num nó cujas tools derivam `danger` é IGNORADO —
//     o piso derivado vence (elevateOnly).
//   - ExaustaoFanout       → §9 «Fan-out de exaustão (plano gigante drena
//     orçamento)»: tectos estruturais (AOS-231) + teto duro por-nó (breaker à
//     admissão) + circuit breaker de concorrência no despacho (AOS-028/029).
//   - GamingDoIntake       → §9 «Manipulação do classificador de intake»:
//     classificação determinística sobre campos declarativos (nunca o texto do
//     `objective`); `simple` forçado REENTRA no gate por-spawn (invariante de
//     não-bypass, AOS-233).
//   - InjeccaoViaRetry     → §9 «Proposta inválida em ciclo»: feedback
//     ESTRUTURADO/allowlisted; o node_id/diagnóstico NÃO ecoa conteúdo untrusted
//     cru; retry bounded (N=3) que esgota fail-closed.
//
// É um pacote de PROVA: só transporta a declaração de pacote (para `go build
// ./...` o reconhecer) — toda a carga vive nos ficheiros `_test.go`. Não declara
// tipos de evento novos nem toca nos pacotes irmãos: apenas os IMPORTA e ataca.
package planadversarial
