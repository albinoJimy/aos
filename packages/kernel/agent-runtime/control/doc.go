// Package control implementa o CANAL DE STEER/INTERRUPT OUT-OF-BAND e o estado paused
// do Agent Runtime do AOS (AOS-023) — o controlo BIDIRECCIONAL que o ADR-013 exige:
// qualquer superfície pausa o run no fim do turno, injecta uma correcção e retoma,
// preservando a durabilidade e o replay. Assenta sobre a máquina de estados durável de
// AOS-017 (o estado [state.Paused] e as arestas running↔paused) e sobre a fronteira de
// fim-de-turno do loop base de AOS-013.
//
// # Canal de controlo SEPARADO do canal de dados
//
// Os sinais pause/steer/resume entram por um [SteerChannel] indexado por run_id —
// NUNCA pelo prompt. É a distinção arquitectural central: o prompt é o canal de DADOS
// (e o seu conteúdo, vindo de tools/web, é untrusted por construção, ADR-005); o
// [SteerChannel] é o canal de CONTROLO (e os seus sinais, autenticados, dirigem o
// agente). Confundi-los seria a escalada de privilégio que este pacote impede.
//
// # Graceful pause no FIM do turno
//
// [SteerChannel.Pause] regista o sinal e marca uma pausa PENDENTE, mas NÃO interrompe o
// trabalho a meio. O loop chama [SteerChannel.GracefulPause] na fronteira de FIM DE
// TURNO — só aí, com todas as activities do turno confirmadas (sem efeitos parciais), a
// transição running→paused se materializa via o [StateGate] (AOS-017). Um pause emitido
// a meio de um turno só toma efeito no fim desse turno.
//
// # Steer autenticado e NÃO-untrusted (a fronteira de segurança)
//
// [SteerChannel.Steer] injecta uma correcção que é AUTENTICADA (assinatura do emissor
// verificada pelo [Authenticator]) e gravada como evento append-only `control.steer`
// COM a identidade do emissor — o registo de NÃO-REPÚDIO de ADR-013 (o log prova quem
// corrigiu o run). A correcção é uma INSTRUÇÃO do canal de controlo, DISTINTA de dados
// untrusted (ADR-005): conteúdo untrusted (resultado de tool / web) não carrega uma
// credencial de emissor válida, logo [Authenticator.Authenticate] rejeita-o e ele NUNCA
// se torna um steer nem autoriza acções. Um steer sem autenticação válida é rejeitado
// com [ErrUnauthenticated] — a escalada de privilégio é impedida por construção.
//
// # Retoma que APLICA a correcção como instrução confiável
//
// [SteerChannel.Resume] materializa paused→running via o [StateGate] e devolve a
// [Correction] pendente com taint [agentruntime.TaintTrusted] — injectável no loop como
// instrução CONFIÁVEL, nunca como dado untrusted. É o oposto exacto de um resultado de
// tool: entrou pelo canal de controlo e foi autenticada.
//
// # Durável e reproduzível por replay
//
// Cada sinal aceite é UM evento append-only no Event Store (AOS-002). A projecção
// corrente (pausa/correcção pendentes) é uma DOBRA desses eventos, reconstruível por
// [SteerChannel.Rebuild]. É isto que faz o ciclo pause→steer→resume SOBREVIVER A CRASH
// (um crash em paused → um worker novo relê o log e recupera a correcção INTACTA) e
// reproduzir-se fielmente por replay (AOS-016) — os eventos de controlo são
// determinísticos e ordenados por seq.
//
// # Contrato de identidade mínimo (AOS-005 opcional)
//
// O [Emitter] é um contrato de identidade LOCAL e mínimo (ID + assinatura). NÃO importa
// o módulo platform/identity de AOS-005 DE PROPÓSITO: mantém o agent-runtime com ZERO
// dependências externas e evita um ciclo de módulos. O [HMACAuthenticator] é a
// realização de referência (HMAC-SHA256, só stdlib). A identidade ed25519 real com
// cadeia de delegação de AOS-005 liga-se por um adaptador de [Authenticator] quando o
// wiring de superfície (EPIC-12) o exigir — aqui expõe-se apenas a API do canal.
//
// # Fora de âmbito (delegado)
//
// A paridade de superfícies (Slack/Telegram/… como cards) é UX de EPIC-12; a ligação
// física do loop de AOS-013 ao [SteerChannel] (chamar GracefulPause na fronteira real e
// injectar a [Correction] no tail) é wiring de integração — aqui fica o CONTRATO e a
// prova por testes determinísticos. A monotonicidade durável do fencing token é
// AOS-018; a retoma paused→running reentra sob o lease já detido e não re-exige token.
package control
