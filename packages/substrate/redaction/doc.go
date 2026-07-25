// Package redaction implementa o MOTOR de redação/tokenização de PII partilhado do
// AOS (ticket AOS-091, ADR-011, tecnica/09 §5), a primeira camada da conformidade
// GDPR por desenho: a MINIMIZAÇÃO na ingestão. PII é detectada e tratada ANTES de
// qualquer persistência — destinado a ser aplicado no Event Store, na memória, nos
// spans e no audit — de modo que nenhuma PII em claro alcance esses destinos.
//
// # Escopo real de importação (AOS-188)
//
// O motor é um módulo FOLHA (substrate) com zero dependências internas; qualquer
// camada superior pode importá-lo. Hoje é consumido pelos módulos de governação do
// plano de controlo (trajectory-surface, approval-card, dsar, confidence-calibration)
// e cablado nos composition-roots (cmd/aos, cmd/aos-demo, integration). A ligação
// directa ao Event Store, platform/memory, substrate/otel-genai e platform/audit
// ainda não está completa na v1; quando esses módulos importarem o motor, usarão o
// mesmo [Ingestor] e a mesma política, garantindo a consistência aqui provada.
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
// um novo eixo kernel→substrate. A composição está provada em class/redact tests
// (ver aos091_integration_test.go, cenário "obrigacao redact + deteccao").
//
// # Integridade
//
// O hash/integridade a jusante (audit hash-chain, AOS-083) é sempre calculado sobre
// o payload JÁ TRATADO por este motor — o audit sela o redigido, nunca o cru. O
// motor não calcula hashes: aplica-se ANTES, garantindo que o que chega ao selo já
// não tem PII em claro.
package redaction
