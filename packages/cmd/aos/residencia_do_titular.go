package main

import (
	"context"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// PODE-SE DESTRUIR O QUE NÃO SE PODE LER?
//
// Achado da varredura adversarial de 2026-08-21, demonstrado com controlo:
//
//	GET /runs/{id}  por chamador de outra região  → 404 (não pode LER)
//	POST /dsar/erase sobre o titular dessa região → 200 "erased"  (pode DESTRUIR)
//	CONTROLO: board desconhecido → 403, e a KEK sobrevive
//
// `authorize` resolve a região de QUEM CHAMA; `authorizeRead` acrescenta a do RECURSO. O erase
// usava a primeira, e mais nada — nem capability, nem authority.json, nem PDP no caminho. Uma
// fronteira de soberania que trava a leitura e deixa passar a destruição irreversível não é uma
// fronteira; é uma preferência.
//
// ---------------------------------------------------------------------------------------------
// O CUSTO ERA MENOR DO QUE EU TINHA ESTIMADO, E FICA DITO PORQUE ERREI NA DIRECÇÃO SEGURA.
//
// O relatório dizia: «não existe conceito de residência POR TITULAR — só por run. A confrontação
// teria de ser CONSTRUÍDA, não activada.» A primeira metade é literalmente verdade. A segunda não
// se segue: a residência por-titular é DERIVÁVEL do que já existe.
//
//	SealContent(ctx, subject, runID, …)   liga titular → run no [audit.SubjectPartitionIndex]
//	gov.residency/<runID> seq 1           carrega a região selada na criação do run
//
// A residência de um titular é, então, o conjunto das residências dos runs onde tem dados. Nada
// de novo a selar, nada de novo a migrar.
//
// UMA EXCEPÇÃO QUE MUDA O DESENHO, e só apareceu ao ler os chamadores: nem tudo o que entra no
// índice é um run. O `resume_records.go` liga o titular a `gov.approvals`, um stream de
// governação partilhado que não tem — nem devia ter — residência. Por isso a regra quantifica
// sobre o que TEM residência selada, em vez de presumir que tudo tem. Presumir teria produzido
// uma negação permanente de todo o erase de qualquer titular que alguma vez tivesse tido uma
// aprovação.
// ---------------------------------------------------------------------------------------------

// podeApagarTitular decide se `id` tem autoridade para destruir os dados de `subject`, aplicando a
// MESMA regra de residência que o read-path aplica por run — mas quantificada sobre TODOS os runs
// do titular.
//
// FAIL-CLOSED em cada perna:
//
//  1. ÍNDICE NÃO COMPOSTO ⇒ NEGA. Sem o índice não é possível provar que o titular não tem dados
//     noutra região, e não se destrói na dúvida. É a mesma postura que [audit.Shredder.held] já
//     toma para o legal hold por-partição («evita o fail-open silencioso por wiring em falta»);
//  2. WORM não responde sobre uma partição ⇒ NEGA (nunca se destrói sem resolver a residência);
//  3. QUALQUER run do titular com residência selada que NÃO coincida com a do chamador ⇒ NEGA.
//
// O que NÃO constrange, e é deliberado:
//
//   - partições SEM residência selada — runs legados e streams de governação como
//     `gov.approvals`. É a mesma retro-compatibilidade que [readGovernance.podeLerRun] concede, e
//     recusá-las tornaria INAPAGÁVEL todo o titular anterior à residência selada. Um nó que não
//     consegue honrar um pedido de apagamento tem um problema de conformidade tão real como um
//     que apaga de mais;
//   - um titular SEM dados nenhuns — nada há a proteger, e negar seria transformar um no-op numa
//     recusa.
//
// NÃO RE-VERIFICA A CREDENCIAL, e a razão está em [readGovernance.podeLerRun]: o verificador OIDC
// tem anti-replay por `jti`, pelo que uma segunda verificação do MESMO pedido devolveria
// `ErrTokenReplayed`. Recebe a identidade JÁ VERIFICADA, exactamente como o read-path por-run.
func (g *readGovernance) podeApagarTitular(ctx context.Context, id readerIdentity, subject string, idx audit.SubjectPartitionIndex) bool {
	if g == nil {
		return true // gate soberano não composto ⇒ comportamento legado (via por headers)
	}
	if idx == nil {
		return false
	}
	for _, particao := range idx.Partitions(subject) {
		region, selada, err := g.runResidency(ctx, particao)
		if err != nil {
			return false
		}
		if !selada {
			continue
		}
		if !regionsCoincide(id.region, region) {
			return false
		}
	}
	return true
}
