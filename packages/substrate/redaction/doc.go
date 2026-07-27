// Package redaction implementa o MOTOR de redação/tokenização de PII partilhado do
// AOS (ticket AOS-091, ADR-011, tecnica/09 §5), a primeira camada da conformidade
// GDPR por desenho: a MINIMIZAÇÃO na ingestão. PII é detectada e tratada ANTES de
// qualquer persistência — destinado a ser aplicado no Event Store, na memória, nos
// spans e no audit — de modo que nenhuma PII em claro alcance esses destinos.
//
// # Escopo real de importação (AOS-188, corrigido em AOS-195, LIGADO em AOS-208)
//
// O motor é um módulo FOLHA (substrate) com zero dependências internas; qualquer
// camada superior pode importá-lo. Em código de PRODUÇÃO é importado por CINCO
// módulos: três de governação do plano de controlo — trajectory-surface (surface.go,
// drilldown.go), approval-card (card.go) e confidence-calibration (history.go) — e,
// desde AOS-208, os DOIS composition-roots do nó: integration (ingestion.go) e cmd/aos
// (bootstrap.go). No módulo dsar a importação existe APENAS em teste (flow_test.go),
// não em produção.
//
// Nos composition-roots a cablagem está COMPLETA, e é este o facto verificável:
//
//   - cmd/aos-demo — o motor ESTÁ no fecho transitivo, por via de approval-card
//     (main.go → control-plane/governance/approval-card → substrate/redaction).
//   - integration — o motor ESTÁ no fecho transitivo desde AOS-208, via o
//     [github.com/aos-ref/integration.IngestionGateway] (ingestion.go), o ponto de
//     composição que alimenta as quatro portas a partir de um único [Ingestor].
//   - cmd/aos — o motor ESTÁ no fecho transitivo desde AOS-208: o Bootstrap compõe o
//     IngestionGateway (bootstrap.go) e o NodeService.Submit redige o objectivo de
//     cada run na fronteira de ingestão.
//
// A ligação ao Event Store, platform/memory, substrate/otel-genai e platform/audit
// está feita no IngestionGateway de AOS-208: o MESMO [Ingestor] e a mesma política
// alimentam as quatro portas a partir de UMA passagem de redacção — a consistência
// aqui provada é, no nó, estrutural (há um só motor), não uma coincidência de
// configuração. O objectivo de um run (input do utilizador) é redigido ANTES de
// alcançar qualquer destino; nenhuma PII em claro chega ao Event Store, à memória,
// aos spans ou ao audit (prova falsificável em
// packages/integration/ingestion_test.go e packages/cmd/aos/ingestion_redaction_test.go).
//
// # Como verificar as afirmações acima
//
// TODAS as afirmações desta secção de escopo — os importadores em produção, a lista
// por composition-root e a nota abaixo — são auto-verificáveis pelos comandos
// seguintes, e DEVEM ser reverificadas sempre que este texto for alterado: foi a
// ausência desta nota que permitiu, em AOS-188, afirmar-se uma cablagem inexistente
// (achado VAC-02 ≡ DEF-02 ≡ CON-03 da auditoria v4). AOS-208 fez a cablagem que
// faltava, pelo que os comandos agora CONFIRMAM PRESENÇA (antes confirmavam ausência).
// Correr a partir da RAIZ DO REPOSITÓRIO, em shell POSIX (bash/CI). Cada linha é
// auto-contida — os `cd` estão dentro de subshells e não acumulam — pelo que o bloco
// pode ser colado inteiro ou linha a linha:
//
//	(cd packages/cmd/aos      && go list -deps ./... | grep substrate/redaction)  # 1 linha (AOS-208)
//	(cd packages/integration  && go list -deps ./... | grep substrate/redaction)  # 1 linha (AOS-208)
//	(cd packages/cmd/aos-demo && go list -deps ./... | grep substrate/redaction)  # 1 linha
//	grep -rln 'aos-ref/substrate/redaction' --include='*.go' --exclude-dir=redaction packages/ | grep -v _test.go  # 6 ficheiros, 5 módulos
//	grep -rn 'aos-ref/substrate/redaction' --include='*.go' packages/cmd/aos packages/integration | grep -v _test.go  # 2 linhas (bootstrap.go, ingestion.go)
//	grep -n 'substrate/redaction' packages/cmd/aos/go.mod packages/integration/go.mod  # 4 linhas: `require` + `replace` em CADA go.mod
//
// As três primeiras linhas provam a lista por composition-root (o motor no fecho
// transitivo dos DOIS roots do nó E do demo); a quarta prova os cinco módulos
// importadores em produção (e, por exclusão, que dsar não está entre eles — o
// `--exclude-dir=redaction` apenas omite este próprio pacote, que contém a cadeia em
// comentário e não a pode importar); a quinta localiza a cablagem do nó (o import
// directo em bootstrap.go e ingestion.go); a sexta prova que agora há `require`
// (não só o `replace` vestigial) em ambos os go.mod. Em PowerShell o `grep` não
// existe: `grep: command not found` NÃO é evidência de nada — usar bash.
//
// Uma armadilha que estes comandos desfazem: `go list -deps` NÃO inclui dependências
// só-de-teste — é por isso que dsar, presente no grafo de cmd/aos, nunca arrastou o
// motor consigo (a importação em dsar é só-de-teste); o motor entra agora pelo caminho
// de PRODUÇÃO (bootstrap.go → integration.IngestionGateway), não por dsar. `go mod
// why -m` não serve como prova de ausência porque inclui os caminhos de teste.
//
// # Porquê um módulo FOLHA (substrate) zero-dep
//
// O motor tem de ser invocável por TODAS as vias de entrada e por camadas de
// abstracção distintas — o kernel (Reference Monitor / PEP, AOS-087), a platform
// (memory, audit) e o substrate (otel-genai) — sem introduzir um ciclo de módulos.
// Vive por isso num módulo folha com ZERO dependências internas e apenas a stdlib
// (regexp, strings, crypto). Qualquer camada o pode importar; o motor não importa
// nenhuma delas.
//
// # As duas operações
//
//   - DETECÇÃO (class.go): classificadores determinísticos por CLASSE (email,
//     telefone, cartão de crédito com Luhn, IBAN com mod-97, IPv4). Regex compiladas
//     uma vez, heurística pura — SEM ML, SEM dependências externas. Extensível por
//     [Detector]. Distingue-se da redação por NOME-DE-CAMPO do RM (AOS-087,
//     reference-monitor/obligations.go enforceRedactPII), que redige campos NOMEADOS
//     pela obrigação: aqui a redação é dirigida pela DETECÇÃO do conteúdo.
//
//   - REDAÇÃO/TOKENIZAÇÃO (redact.go, token.go): [Engine.Redact] é uma função pura
//     sobre um payload (string ou JSON recursivo) que devolve o payload tratado mais
//     os [TokenRef] produzidos. Por política e por classe, cada ocorrência é:
//     REMOVIDA (minimização — substituída pelo marcador [REDACTED:<classe>]) quando
//     não é necessária; ou TOKENIZADA (tok:<titular>:<classe>:<opaco>) quando é. O
//     token é reversível SÓ com a chave POR-TITULAR obtida da porta [KeySource] — a
//     mesma noção de chave-por-titular do audit (AOS-083, KeyVault). Apagar essa
//     chave (crypto-shredding, AOS-093) torna o token IRRESOLÚVEL: prepara o Art. 17.
//
// # Chave por titular como PORTA (não gerimos o vault)
//
// O motor NÃO gere o cofre de chaves: recebe-o pela porta [KeySource]. Produz o
// TOKEN e a KeyRef; a impl concreta (KMS/HSM em produção, envelope AES-256-GCM do
// audit em AOS-083) é injectada. Isto mantém o motor folha e reconcilia-o com o
// crypto-shredding: destruir a chave no vault injectado torna o token irresolúvel
// sem tocar no motor.
//
// # Aplicação consistente nas três vias
//
// O MESMO motor aplica-se a input de utilizador, tool result e ingestão de memória.
// [Ingestor] expõe [Ingestor.UserInput], [Ingestor.ToolResult] e [Ingestor.Memory]
// — todas delegam em [Engine.Redact], provando por construção (e por teste) que as
// três vias redigem com o mesmo classificador e a mesma política.
//
// # Integração com a obrigação redact do PDP/PEP (AOS-087)
//
// A obrigação redact_pii do RM redige HOJE campos nomeados (redação por-campo). A
// DETECÇÃO deste motor compõe-se com ela: [Engine.Redact] aplicado sobre o Input já
// tratado por-campo acrescenta a redação por-padrão (o campo não-nomeado que ainda
// contenha PII em claro é apanhado pela detecção). Como o RM é um módulo folha
// (kernel) e este motor é outro (substrate), a composição é feita no ponto de
// integração (composition root) e não por importação directa do kernel — evitando
// um novo eixo kernel→substrate. A composição está provada pelo teste
// TestComposesWithFieldNameRedaction (aos091_integration_test.go).
//
// # Integridade
//
// O hash/integridade a jusante (audit hash-chain, AOS-083) é sempre calculado sobre
// o payload JÁ TRATADO por este motor — o audit sela o redigido, nunca o cru. O
// motor não calcula hashes: aplica-se ANTES, garantindo que o que chega ao selo já
// não tem PII em claro.
package redaction
