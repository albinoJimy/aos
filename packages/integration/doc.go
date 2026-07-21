// Package integration é o COMPOSITION ROOT do AOS de referência: o único lugar
// que LIGA os primitivos de plataforma, já completos e testados em isolamento, ao
// caminho quente do Agent Runtime (packages/kernel/agent-runtime, AOS-013) e do
// Reference Monitor (packages/kernel/reference-monitor, AOS-003). Nenhum pilar
// depende deste pacote; ele depende de todos — é o ápice do grafo de módulos, e é
// aqui, e só aqui, que o wiring cross-module vive (ADR-002).
//
// # Porquê um módulo separado
//
// O grafo de dependências é acíclico por camadas: eventstore ← reference-monitor
// ← {agent-runtime, audit} ← memory ← registry. O Agent Runtime NÃO pode importar
// registry/memory (criaria um ciclo de pacote — esses pilares importam a porta
// Tracer zero-dep do runtime). Logo o loop consome os primitivos por PORTA
// (inversão de dependência) e o concreto é adaptado AQUI. O Reference Monitor é
// folha e expõe a costura [referencemonitor.Hook]; a revalidação por chamada
// entra por essa costura sem que o pacote referencemonitor tenha de a conhecer.
//
// # O que este pacote liga (par de segurança — AOS-050 + AOS-051)
//
//   - AOS-050 (congelamento do tool set por run): no ARRANQUE de cada run,
//     [SecuredRuntime.Run] chama [toolset.FreezeToolSet] sobre o REG, materializa o
//     snapshot no prefixo imutável do prompt e no manifesto de dependências
//     ([ApplyFrozenToGoal]) e regista-o em [RunToolSets] para a revalidação o
//     consultar. Ver freeze.go.
//   - AOS-051 (revalidação criptográfica por chamada): [RevalidationHook] adapta o
//     [revalidation.Revalidator] à cadeia de mediação do RM. A CADA tool call, o
//     RM corre LOOKUP→digest→assinatura→scope/egress→EXEC→AUDIT ANTES de mintar o
//     seu permit; um veredicto de bloqueio devolve [referencemonitor.HookDeny] e a
//     tool NUNCA é despachada — a "última linha anti rug-pull" fica de facto em
//     todo o caminho quente (mediação total). Ver revalhook.go. O Quarantiner é
//     ligado à quarentena de proveniência de AOS-042 ([ProvenanceQuarantiner],
//     quarantine.go) e o Alerter a um sink observável ([RecordingAlerter],
//     alerter.go) — a costura para o pilar de alertas de EPIC-08, que ainda não
//     existe.
//
// # Fronteira honesta
//
// As restantes ligações diferidas (AOS-021 dispatcher, AOS-037 janela/exaustão,
// AOS-043 checkpoint de compressão) NÃO são cobertas por esta passagem; entram
// numa integração seguinte, pelo mesmo padrão de composição. Ver o CHANGELOG.
package integration
