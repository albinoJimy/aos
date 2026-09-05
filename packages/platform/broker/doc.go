// Package broker é o Credential Broker + Vault (BRK) do AOS (AOS-070, ADR-006).
//
// # Problema
//
// Se o agente detém o segredo downstream (a chave de um serviço externo), qualquer
// prompt injection ou exfiltração o compromete. O blueprint elimina essa classe de
// falha: os segredos vivem num Vault e o broker troca o token SCOPED do agente por
// credenciais downstream SERVER-SIDE, injectando-as directamente no ponto de
// execução (a sandbox microVM). O agente apresenta identidade, NUNCA segredo.
//
// # Invariantes (ADR-006)
//
//   - O AGENTE NUNCA VÊ O SEGREDO. A troca devolve um HANDLE opaco e não-secreto
//     (compõe o credentials_handle de AOS-064); o valor só existe server-side,
//     entre o [internal/vault] e o ponto de injecção. Estruturalmente: nenhuma API
//     exportada devolve o valor — o portador do segredo ([vault.Secret]) tem o
//     campo NÃO-EXPORTADO e a sua única saída é [vault.Secret.DeliverTo], que o
//     entrega a um sink server-side e NÃO o devolve.
//   - ESCOPO utilizador ∩ classe (AOS-057). O broker só troca por credenciais
//     consistentes com a autoridade efectiva = autoridade do utilizador ∩ escopo
//     da classe do agente. Um pedido fora do escopo é NEGADO fail-closed (ver
//     [ScopeGate]).
//   - EIXO PROVIDER (AOS-324). A chave do material é {Provider, Region, Capability}
//     e vem do PEDIDO. O eixo capability é imposto pelo [ScopeGate] (acima); o eixo
//     region pelo Reference Monitor (`ObligationRegion`, sobre `Resource.Region`);
//     o eixo PROVIDER é imposto pelo mesmo [ScopeGate] e pela defesa server-side de
//     dispatch, comparando o provedor pedido com a autoridade efectiva de provedor
//     (tecto da classe ∩ grants do token). A política declara-se em
//     [WithClassProviders]; a sua ausência é a postura DECLARADA
//     [ProviderPostureUnset], selada em cada troca no campo `provider_policy` do
//     evento. Ver provider.go para a política, o estado por omissão e os limites.
//   - TTL CURTO + REVOGÁVEL. A credencial downstream tem TTL curto (relógio
//     injectável) e é revogável por id de lease; expira automaticamente. Uma lease
//     expirada/revogada não é injectável (a injecção falha, sem entregar o valor).
//   - SEM SEGREDO EM CÓDIGO/LOGS/SPANS/EVENT STORE. Redação ([fmt.Stringer] +
//     [json.Marshaler]) em TODOS os portadores; o registo da troca sela apenas
//     handle/lease-id/metadados não-secretos, nunca o valor.
//   - TROCA MEDIADA PELO RM. A troca de token é mediada pelo Reference Monitor
//     (AOS-003): só um principal autorizado/consistente com o escopo a pode pedir,
//     e a mediação regista quem/para quê/quando no Event Store (audit-before-effect),
//     sem o valor.
//
// # Composição (sem ciclos)
//
// O broker compõe por porta o Reference Monitor (medeia e regista a troca), o
// Event Store (sela o registo da troca), o Vault ([internal/vault], onde o segredo
// vive) e o SBX (implementa [github.com/aos-ref/substrate/sandbox.CredentialInjector],
// resolvendo o handle e injectando server-side). Nenhum desses módulos importa o
// broker.
package broker
