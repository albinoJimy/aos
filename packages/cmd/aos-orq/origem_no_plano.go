package main

// UM PLANO QUE DECLARA A ORIGEM DE UMA SAÍDA NÃO CORRE NESTE BINÁRIO (AOS-500, ADR-038 §2.1).
//
// O schema do plano (linha 1.3.0) deixa uma saída declarar `from_tool`: a tool do mesmo nó de que
// ela é o resultado. A declaração promete ao nó seguinte — e ao humano que aprovou o cartão — o
// que a tool devolveu. Quem cumpre a promessa é a entrega por referência, que é do AOS-501 e
// ainda não existe: este binário publica como saída de um nó o TEXTO FINAL do run.
//
// Correr um plano destes era publicar o texto do modelo sob um contrato que promete outra coisa,
// com o `contract_digest` a dizer que sim. Por isso recusa-se, com causa própria, e cedo:
//
//   - com `--plan-doc`, ANTES da posse do run — o documento conhece-se à partida
//     ([recusarDocumentoComOrigem], em `cmdServeCom`), e outra vez sobre os bytes que de facto
//     se vão materializar (o ficheiro é relido em `materializar`);
//   - com `--goal`, a seguir à decomposição e ANTES da validação, do gate e da materialização:
//     nenhum nó é admitido nem submetido. Aqui a posse já existe (o planeador corre sob ela) e
//     é largada, como em qualquer recusa determinista do documento.
//
// NÃO DEPENDE DO INTERRUPTOR `AOS_ORQ_SAIDA_POR_REFERENCIA` (AOS-499): esse governa a medição, e
// nenhum dos seus valores entrega por referência. O AOS-501 retira esta recusa ao ligar a entrega.
//
// O planeador não é instruído a emitir o campo (o prompt que o pede é o 1.5.0, do AOS-501), pelo
// que hoje um plano só o traz se alguém o escrever à mão ou se o modelo o inventar.

import (
	"errors"
	"fmt"
	"os"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// errOrigemSemEntrega — o documento do plano declara a origem de uma saída (`from_tool`) e este
// binário não entrega por referência. É DETERMINISTA para o documento: partilha a saída 10 com o
// documento recusado ([exitDocumentoRecusado]) e larga a posse, mas tem causa própria no
// vocabulário fechado de [tipoDoErro] (`origem_sem_entrega`) — quem lê o resumo da drenagem tem
// de distinguir «o documento não presta» de «o documento pede uma entrega que ainda não existe».
var errOrigemSemEntrega = errors.New("plano com origem de saida declarada (outputs[].from_tool) NAO CORRE: este binario ainda nao entrega por referencia (AOS-501)")

// recusarOrigemDeclarada devolve [errOrigemSemEntrega] se algum nó do documento declarar a
// origem de uma saída. A mensagem leva só a contagem: os `node_id` e os nomes das tools são
// texto do documento, e este erro pode ser levantado antes de o validador os ter conferido.
func recusarOrigemDeclarada(doc plan.PlanDocument) error {
	nos := 0
	for _, n := range doc.Nodes {
		if n.DeclaresOutputSource() {
			nos++
		}
	}
	if nos == 0 {
		return nil
	}
	return fmt.Errorf("%w — %d no(s) do plano declaram-na; correr era publicar o texto do modelo sob um contrato que promete o resultado da tool", errOrigemSemEntrega, nos)
}

// recusarDocumentoComOrigem aplica [recusarOrigemDeclarada] ao ficheiro do `--plan-doc`, ANTES da
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
	return recusarOrigemDeclarada(doc)
}
