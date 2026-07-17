// Package risk é o núcleo do GATE DE RISCO SA-ROC do Reference Monitor (AOS-074,
// ADR-013): classifica cada acção proposta num eixo composto — SENSIBILIDADE dos
// dados + EGRESS + REVERSIBILIDADE — e aplica FRICÇÃO PROPORCIONAL, cortando o
// approval-fatigue que anula a governação quando o gate é uniforme.
//
// # Porquê mudar o eixo do gate (tecnica/07 §3)
//
// Um gate binário "destrutivo/não-destrutivo" falha o modelo de ameaça real: o
// risco de maior severidade não é o `rm -rf` mas a EXFILTRAÇÃO via tools
// "benignas" (padrão CamoLeak, CVSS 9.6) — uma leitura de dados sensíveis seguida
// de um POST para um domínio externo é DANGER mesmo sem nenhuma operação
// "destrutiva". Por isso a classe de risco é função de três eixos, não de um.
//
// # Classificação (policy-as-code versionada)
//
// [Classify] mapeia uma [Action] (sensibilidade, egress, reversibilidade, mais o
// sinal de taint que ELEVA a sensibilidade — conteúdo untrusted, AOS-069) numa
// [Class] via uma [Policy] DETERMINISTA e VERSIONADA (digest sha256 canónico, à
// imagem das allowlists de AOS-067/058). O mapa de decisão é:
//
//   - SAFE — local, reversível, sem egress e de baixa sensibilidade: corre sem gate.
//   - GRAY — algum risco mas agrupável: agrega-se em lote com um resumo.
//   - DANGER — egress externo de dados sensíveis OU acção irreversível: escala para
//     confirmação individual com preview do efeito concreto resolvido.
//
// FAIL-CLOSED PELO TIPO: os valores-zero de cada eixo são os MAIS seguros para o
// classificador ([SensitivityUnknown] conta como o topo do reticulado, egress
// desconhecido como externo, reversibilidade desconhecida como irreversível) — um
// eixo ausente ou adulterado ELEVA o risco, nunca o baixa.
//
// # SA-ROC (o gate, [Gate.Evaluate])
//
// A invariante central (ADR-013): SAFE corre sem fricção; GRAY AGRUPA numa
// confirmação de LOTE (uma confirmação para o grupo, não uma por acção); DANGER e
// IRREVERSÍVEL escalam para CONFIRMAÇÃO INDIVIDUAL com PREVIEW do efeito CONCRETO
// resolvido, via a porta [ConfirmationChannel] (o HITL do EPIC-09 liga-se por
// trás). A ausência de confirmação numa acção IRREVERSÍVEL dentro do TIMEOUT
// resolve DENY (fail-closed) — a ausência de aprovação NEGA, nunca permite.
//
// # Anti-fatigue sem bypass
//
// A [AutoApprovePolicy] é configurável por CLASSE e MATURIDADE (um utilizador
// maduro auto-aprova safe/gray), MAS danger/irreversível NUNCA é auto-aprovável
// sem confirmação — prova ESTRUTURAL em [AutoApprovePolicy.Allows] (o caso
// [ClassDanger] devolve sempre false, ignorando a maturidade). O OVERRIDE-RATE
// (fracção de acções gray/danger que o utilizador APROVA) é medido e exposto por
// [Metrics] (molde de SLI por porta, AOS-061) — um override-rate alto sinaliza
// rubber-stamping.
//
// # Zero dependências / determinismo
//
// Só stdlib (crypto/sha256, encoding/json, sort, sync, context) e o subpacote
// irmão taint (o rótulo untrusted é ENTRADA da classificação). NÃO importa
// platform/* nem o sandbox de rede (o "egress-class" é modelado como propriedade
// da acção, derivável sem importar o sandbox — ver o RiskGate no pacote ápice) —
// evita ciclos. A classificação é PURA; só o gate consulta o relógio (timeout,
// injectável) e a porta de confirmação.
package risk
