package main

// FORA DE `on`, A LINHA 1.3.0 DO PLANO NÃO CORRE NESTE BINÁRIO (AOS-500, ADR-038 §2.1).
//
// DESDE O AOS-501 ESTA GUARDA DEPENDE DO INTERRUPTOR. Com `AOS_ORQ_SAIDA_POR_REFERENCIA` em `off`
// (a omissão) ou `observe`, tudo o que este ficheiro descreve abaixo vale tal como foi escrito:
// as mesmas recusas, os mesmos códigos, as mesmas mensagens. Com `on` a guarda cai — quem
// decide então se um plano com origem corre é a postura do `serve` contra o nó
// ([posturaDaEntrega], entrega_por_referencia.go): com um nó que anuncia o vínculo vinculativo,
// corre e entrega por referência; sem ele, é recusado com outra causa
// (`no_sem_saida_por_referencia`), e nunca cai para o texto do modelo. O texto que segue é o do
// AOS-500, e descreve os modos `off` e `observe`.
//
// O schema do plano (linha 1.3.0) deixa uma saída declarar `from_tool`: a tool do mesmo nó de que
// ela é o resultado. A declaração promete ao nó seguinte — e ao humano que aprovou o plano — o
// que a tool devolveu. Quem cumpre a promessa é a entrega por referência, que é do AOS-501 e
// ainda não existe: este binário publica como saída de um nó o TEXTO FINAL do run. Correr um
// plano destes era publicar o texto do modelo sob um contrato que promete outra coisa, com o
// `contract_digest` a dizer que sim.
//
// # ESTA É A ÚNICA GUARDA, E O AOS-501 RETIRA-A
//
// O `plan.Decode`, o validador (`planvalidate`), o cartão de aprovação e
// `plan.CurrentPlanVersion` já conhecem a 1.3.0: o código do campo está pronto. O que mantém o
// AOS-500 INVISÍVEL em produção é este ficheiro, e só ele — tudo o que este binário faz com um
// documento da linha 1.3.0 passa por [semEntregaPorReferencia]. O AOS-501, ao ligar a entrega,
// apaga o ficheiro e as suas chamadas (o `aos-orq` não precisa de mais nada para aceitar a
// linha).
//
// # O QUE É «DA LINHA 1.3.0»: O CAMPO **E** O CARIMBO
//
// Um documento com `outputs[].from_tool`, e também um documento CARIMBADO 1.3.x sem o campo.
// O binário anterior recusava os dois (o primeiro no `Decode`, o segundo no validador, por
// `plan_version_ahead_of_reader`), e nenhum planeador é hoje instruído a produzir nenhum deles —
// o prompt 1.4.0 manda carimbar até 1.2.0. Aceitar o carimbo sozinho parecia inofensivo e não
// era: o documento ficava guardado carimbado 1.3.0, e depois de um rollback o binário anterior
// recusava-o a meio do plano.
//
// # O QUE ACONTECE, CAMINHO A CAMINHO — O MESMO QUE NO BINÁRIO ANTERIOR
//
//   - `serve --goal` (o planeador decompõe): um documento da linha 1.3.0 é uma TENTATIVA
//     RECUSADA do planeador ([recusaDoLacoSemEntrega], chamada pelo validador do laço). Volta ao
//     laço como qualquer recusa do validador, com o código que o binário anterior dava ao
//     carimbo, e o planeador tenta de novo. Só é terminal se o laço esgotar as tentativas.
//   - `serve --plan-doc` e todo o documento RELIDO DO DISCO — a segunda leitura em
//     `materializar`, a re-verificação de um pendente pelo `consume`
//     (`documentoExigeHumano`) e o `decide` (`lerDocumentoPorHash`): [recusarSemEntrega], saída
//     10 e causa `origem_sem_entrega`, antes de qualquer efeito. Um documento do disco não tem
//     laço a que voltar.
//
// DEPENDE DO INTERRUPTOR `AOS_ORQ_SAIDA_POR_REFERENCIA` desde o AOS-501: ver o topo do ficheiro.

import (
	"errors"
	"fmt"
	"os"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planner"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// linhaSemEntrega é o MINOR do schema do plano que este binário ainda não corre: o que
// introduziu `from_tool`.
var linhaSemEntrega = plan.PlanVersion{Major: 1, Minor: 3, Patch: 0}

// errOrigemSemEntrega — o documento do plano é da linha 1.3.0 (declara a origem de uma saída, ou
// carimba essa linha) e este binário não entrega por referência. É DETERMINISTA para o
// documento: partilha a saída 10 com o documento recusado ([exitDocumentoRecusado]) e larga a
// posse, mas tem causa própria no vocabulário fechado de [tipoDoErro] (`origem_sem_entrega`) —
// quem lê o resumo da drenagem tem de distinguir «o documento não presta» de «o documento pede
// uma entrega que ainda não existe».
var errOrigemSemEntrega = errors.New("plano da linha 1.3.0 (origem de saida declarada em outputs[].from_tool, ou carimbo 1.3.x) NAO CORRE: este binario ainda nao entrega por referencia (AOS-501)")

// semEntregaPorReferencia é o PREDICADO da guarda, e a sua única leitura: o documento é da linha
// 1.3.0 — algum nó declara a origem de uma saída, ou o carimbo é dessa linha. Devolve as duas
// metades para a mensagem as poder distinguir.
//
// Um carimbo ACIMA da 1.3 não é desta guarda: é do validador, que o recusa por
// `plan_version_ahead_of_reader` como sempre recusou.
func semEntregaPorReferencia(doc plan.PlanDocument) (nosComOrigem int, carimboDaLinha bool) {
	for _, n := range doc.Nodes {
		if n.DeclaresOutputSource() {
			nosComOrigem++
		}
	}
	carimboDaLinha = doc.PlanVersion.Major == linhaSemEntrega.Major && doc.PlanVersion.Minor == linhaSemEntrega.Minor
	return nosComOrigem, carimboDaLinha
}

// entregaLigadaNoAmbiente diz se o interruptor está em `on`. É a leitura dos sítios que julgam um
// documento SEM falar com o nó (antes da posse, a re-verificação de um pendente, o `decide`): aí
// a guarda cai com o interruptor, e quem recusa um plano com origem contra um nó que não o
// suporta é o `serve`. Um valor inválido não é `on`: a guarda fica, e quem valida o interruptor
// recusa o arranque a seguir.
func entregaLigadaNoAmbiente() bool {
	modo, err := modoDaSaidaPorReferenciaDoAmbiente()
	return err == nil && modo == saidaPorReferenciaOn
}

// recusarSemEntrega é a guarda dos leitores que NÃO têm o nó na mão: com o interruptor em `on`
// não recusa nada; fora dele é a guarda do AOS-500 ([recusarLinhaDaOrigem]).
func recusarSemEntrega(doc plan.PlanDocument) error {
	if entregaLigadaNoAmbiente() {
		return nil
	}
	return recusarLinhaDaOrigem(doc)
}

// recusar é a guarda de quem TEM a postura do `serve` (a materialização e a última linha depois
// do laço do planeador):
//
//   - [entregaDesligada] — a guarda do AOS-500, sem tirar nem pôr;
//   - [entregaSemNo] — o interruptor diz `on` e não há nó a quem declarar a origem: um plano que
//     a DECLARE não corre ([errNoSemEntregaPorReferencia]). Só o carimbo 1.3.0, sem o campo, não
//     pede entrega nenhuma e segue para o validador;
//   - [entregaActiva] — não recusa.
func (p posturaDaEntrega) recusar(doc plan.PlanDocument) error {
	switch p {
	case entregaActiva:
		return nil
	case entregaSemNo:
		if nos, _ := semEntregaPorReferencia(doc); nos > 0 {
			return fmt.Errorf("%w — %d no(s) do plano declaram a origem; nunca se entrega o texto do modelo no lugar do resultado da tool", errNoSemEntregaPorReferencia, nos)
		}
		return nil
	default:
		return recusarLinhaDaOrigem(doc)
	}
}

// recusaDoLaco é a forma NÃO-TERMINAL de [posturaDaEntrega.recusar], para o laço de tentativas
// do planeador. Sem entrega activa o planeador recebe o prompt 1.4.0, que não pede o campo: um
// documento com origem (ou, com a entrega desligada, só com o carimbo) é uma tentativa
// recusada com o código de sempre, e ele tenta de novo.
func (p posturaDaEntrega) recusaDoLaco(doc plan.PlanDocument) *planner.Rejection {
	switch p {
	case entregaActiva:
		return nil
	case entregaSemNo:
		if nos, _ := semEntregaPorReferencia(doc); nos == 0 {
			return nil
		}
		return &planner.Rejection{Rule: string(plannerevents.RuleSchema), Reason: string(planvalidate.ReasonVersionAheadOfReader)}
	default:
		return recusaDoLacoSemEntrega(doc)
	}
}

// recusarLinhaDaOrigem devolve [errOrigemSemEntrega] para um documento da linha 1.3.0. É a forma
// TERMINAL da guarda: para o documento que vem do disco, e como última linha depois do laço do
// planeador. A mensagem leva só a contagem e o carimbo: os `node_id` e os nomes das tools são
// texto do documento, e este erro pode ser levantado antes de o validador os ter conferido.
func recusarLinhaDaOrigem(doc plan.PlanDocument) error {
	nos, carimbo := semEntregaPorReferencia(doc)
	switch {
	case nos > 0:
		return fmt.Errorf("%w — %d no(s) do plano declaram a origem; correr era publicar o texto do modelo sob um contrato que promete o resultado da tool", errOrigemSemEntrega, nos)
	case carimbo:
		return fmt.Errorf("%w — o documento carimba %s sem declarar origem nenhuma; ate ao AOS-501 este binario le-o como o anterior o lia, e o anterior recusava-o", errOrigemSemEntrega, doc.PlanVersion)
	}
	return nil
}

// recusaDoLacoSemEntrega é a forma NÃO-TERMINAL da guarda, para o laço de tentativas do planeador
// (`serve --goal`): um documento carimbado na linha 1.3.0 é uma tentativa recusada, e o planeador
// tenta de novo. nil ⇒ não é desta guarda, e o validador decide.
//
// O CÓDIGO É O QUE O BINÁRIO ANTERIOR DAVA a este carimbo — `schema` /
// `plan_version_ahead_of_reader` —, para o planeador ler a mesma razão e para o log ficar igual.
// E é verdade: para este binário, a 1.3.0 está à frente do que ele corre.
//
// Só o CARIMBO decide aqui. Um documento com `from_tool` carimbado abaixo de 1.3.0 é recusado
// pelo validador (`plan_version_below_features`), e acima é recusado pelo validador também; em
// nenhum dos casos chega a ser aceite, e [recusarSemEntrega] fica a jusante do laço como última
// linha.
func recusaDoLacoSemEntrega(doc plan.PlanDocument) *planner.Rejection {
	if _, carimbo := semEntregaPorReferencia(doc); !carimbo {
		return nil
	}
	return &planner.Rejection{Rule: string(plannerevents.RuleSchema), Reason: string(planvalidate.ReasonVersionAheadOfReader)}
}

// recusarDocumentoSemNo aplica [posturaDaEntrega.recusar] ao ficheiro do `--plan-doc`, ANTES da
// posse do run, quando o interruptor está em `on` e o nó não anuncia o vínculo vinculativo. Como
// [recusarDocumentoComOrigem], só recusa o que é seu: um ficheiro que não se lê ou não
// descodifica tem o seu tratamento no caminho de sempre.
func recusarDocumentoSemNo(docPath string, p posturaDaEntrega) error {
	if p != entregaSemNo {
		return nil
	}
	raw, err := os.ReadFile(docPath)
	if err != nil {
		return nil
	}
	doc, err := plan.Decode(raw)
	if err != nil {
		return nil
	}
	return p.recusar(doc)
}

// recusarDocumentoComOrigem aplica [recusarSemEntrega] ao ficheiro do `--plan-doc`, ANTES da
// posse do run. Não abre o Event Store, não fala com o nó e não escreve nada.
//
// SÓ RECUSA O QUE É SEU. Um ficheiro que não se lê ou que não descodifica devolve nil: esses
// casos têm o seu tratamento, com as suas mensagens e o seu código, no caminho de sempre
// (`materializar`), e não é este ticket que os muda de sítio.
func recusarDocumentoComOrigem(docPath string) error {
	raw, err := os.ReadFile(docPath)
	if err != nil {
		return nil
	}
	doc, err := plan.Decode(raw)
	if err != nil {
		return nil
	}
	return recusarSemEntrega(doc)
}
