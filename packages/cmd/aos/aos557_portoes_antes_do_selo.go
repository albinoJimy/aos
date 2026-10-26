package main

// aos557_portoes_antes_do_selo.go — OS PORTÕES DO RUN CORREM ANTES DO SELO DAS ENTRADAS (AOS-557).
//
// # O defeito, na forma em que ele existia
//
// A hospedagem de um run ([NodeService.hostRun]) fixa o layout, sela as entradas do plano em
// handles ([selarEntradasDoPlano]) e só depois chama o [integration.SecuredRuntime.Run]. E o
// `Run` tem portões fail-closed que recusam o run ANTES do primeiro turno e sem um único efeito,
// dois deles accionáveis por quem submete: o contrato de conclusão e a origem declarada da saída.
//
// Um `POST /runs` num layout por handle, com entradas e um `completion_requires` que o nó não
// pode cumprir, selava TODAS as entradas e fechava o run sem turno. Os bytes cifrados ficavam no
// espaço de handles e nada os apaga — nem o guarda da resolução (que exige o par (titular, run,
// digest) de um run que existe) nem uma passagem de limpeza (não há). Repetível por submissão,
// até o tecto do VOLUME fazer a selagem recusar em TODOS os runs do nó.
//
// Não era defeito vivo — a 1.8.0 continua inalcançável até o interruptor fechar. Passava a ser.
//
// # Porque é que não se desfaz, e sim se antecipa
//
// Desfazer no erro do `Run` acontece depois de [NodeService.persistCrashResumeRecord], pelo que
// deixaria um run RETOMÁVEL cujas entradas não têm bytes — perda silenciosa de capacidade em vez
// de bytes a mais, que é a troca errada. A correcção é correr a conferência ANTES do selo.
//
// # O que esta conferência é, e o que NÃO é
//
// NÃO É UM SEGUNDO PORTÃO. A garantia continua a ser a do kernel, no `Run`, contra o tool set
// CONGELADO — e é a mesma função que decide nos dois sítios ([agentruntime.RefuseAtStartup] e o
// portão do `Run` partilham a implementação, AOS-557). Esta é só a que evita o selo: decide mais
// cedo, com a oferta que o nó conhece, para um run condenado não deixar handles atrás de si.
//
// # Onde corre, e porquê exactamente ali
//
// Entre a travessia da aresta do plano (AOS-553) e o selo (AOS-551), em [NodeService.hostRun]:
//
//   - DEPOIS da fixação do layout e do modo do veredicto, porque é deles que a conferência
//     depende (um nó em `off` não lê o contrato, e o layout decide se há selo);
//   - DEPOIS da travessia, que só LÊ do armazém do run produtor e não deixa bytes atrás de si —
//     pô-la antes mudava a causa que um run com a aresta E o contrato maus reporta, sem mudar o
//     desfecho;
//   - ANTES do selo, que é a única coisa neste caminho que cunha handles e escreve bytes.
//
// NÃO É A PRIMEIRA ESCRITA DURÁVEL DA HOSPEDAGEM, e isso fica dito em vez de sugerido. A primeira
// é o claim `ready→running` da máquina de estados, muito acima, que corre em TODA a hospedagem;
// e num nó com candidato a servir a fixação do modelo ([NodeService.escolherModeloDoRun]) grava
// ainda `run.model_pinned` e consome uma atribuição do livro do canary — essa só nessa composição.
// A conferência depende apenas do modo do veredicto e do layout, pelo que em princípio podia subir
// acima das duas; não sobe porque trocaria a ordem de recusas já estabelecidas por um ganho que não
// é o deste ticket — o resíduo que ele mede são os BYTES no espaço de handles, e esses são do selo.
//
// # A OFERTA, e porque é que ela é fiel (a medição, não a suposição)
//
// O portão do kernel decide a pertença contra o [agentruntime.Goal.Tools] — o tool set CONGELADO
// do run, JÁ CORTADO pela lista-branca ([integration.ApplyFrozenToGoal], que chama o
// `specsOferecidas`). Na hospedagem esse campo está VAZIO: o nó não o preenche em sítio nenhum, e
// quem o escreve é o `ApplyFrozenToGoal` dentro do próprio `Run`. (O registo de retoma TEM o campo
// e repõe-no — mas o que ele guarda é o Goal de ANTES do `Run`, onde está vazio: a ida e a volta
// transportam o vazio.) Erguer o portão com o Goal tal como está recusaria TODOS os contratos.
//
// Logo a oferta vem do catálogo, que é a MESMA fonte do congelamento
// ([integration.SecuredRuntime.OfferedToolNames], que explica a equivalência de conjuntos), E
// CORTADA PELA LISTA-BRANCA DO RUN AQUI ([ofertaDoNo]) — porque é cortada que o `Run` a vê. Sem o
// corte o veredicto era o mesmo e a CAUSA mudava: uma tool no catálogo e fora da lista-branca saía
// daqui como «fora da lista-branca» e do `Run` como «não está no tool set», e é a causa que o
// `GET /runs/{id}` serve.
//
// # Donde o ÂMBITO: a PRIMEIRA hospedagem, e só onde o selo vai cunhar
//
// Duas condições, e a segunda é a que a revisão adversarial deste ticket apanhou:
//
//  1. [haEntradasPorSelar] — o selo vai cunhar pelo menos um handle (layout por handle, com alguma
//     entrada ainda sem referência). Fora disto não há resíduo a evitar.
//  2. `!rs.existente` — esta é a PRIMEIRA hospedagem deste run. É a bandeira que o
//     [NodeService.submitObservando] escreve nos três caminhos de re-hospedagem, e é ela que
//     distingue o arranque — não a forma do Goal.
//
// PORQUE É QUE A SEGUNDA NÃO SE DERIVA DA PRIMEIRA, que foi o erro da primeira versão deste
// ficheiro. Num run re-hospedado o tool set vem do snapshot DURÁVEL, que ganha de propósito ao
// catálogo vivo (AOS-050/AOS-155): conferi-lo contra o catálogo recusaria um run que o `Run`
// admite. Duas das três re-hospedagens chegam com as entradas já seladas — a retoma pela API e a
// varredura de crash-resume repõem os `Inputs` do registo, com as referências —, pelo que
// [haEntradasPorSelar] já as excluía. A TERCEIRA NÃO: um `POST /runs` com um `run_id` que JÁ EXISTE
// e está em curso é admitido como retoma por outro nome (caso «(2-ter-ter)» do
// [NodeService.submitObservando], AOS-520) e o Goal dele é o DO CORPO, com `Inputs` sem referência
// ([Node.goalDoRunExistente] só toca no modelo e no perfil). Sem a bandeira, um operador que
// tirasse uma tool do manifesto e reiniciasse o nó fazia essa re-submissão fechar `failed` num run
// que o `Run` continuaria a servir do snapshot.
//
// O QUE ISSO DEIXA DE FORA, declarado: um run re-hospedado por essa via com um contrato impossível
// continua a selar antes de o `Run` o recusar. Fechá-lo exigia conferir contra o snapshot durável,
// que só se lê repondo-o no registo de tool sets por-run — estado partilhado que o `Run` repõe e
// larga a seguir. Não se faz aqui; fica nomeado.
//
// # FAIL-CLOSED, e é o mesmo desfecho
//
// Um catálogo que não se lê recusa o run. No âmbito desta conferência isso não acrescenta
// severidade: a primeira hospedagem de um run não tem snapshot durável, pelo que o `Run` chega ao
// MESMO catálogo pelo `FreezeToolSet` e devolve o mesmo erro — o run fechava `failed` de qualquer
// maneira, agora sem ter selado nada.
//
// # A JANELA DO CRASH CONTINUA ABERTA, e este ticket NÃO a fecha
//
// Um processo que morra entre o selo e o primeiro evento do run deixa handles cunhados e bytes em
// disco sem run que os use: não há `defer` que sobreviva a um `SIGKILL`. É outro problema, com
// outra forma (precisa de uma passagem que limpe o espaço de um run sem história) e tem ticket
// próprio — o reaper por run, AOS-554. Fica declarado aqui e no bloco do selo, e não escondido.

import (
	"context"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// haEntradasPorSelar diz se [selarEntradasDoPlano] vai CUNHAR pelo menos um handle para este
// Goal: o layout põe as entradas do plano por handle, há entradas, e alguma ainda não traz
// referência.
//
// É DELIBERADAMENTE MAIS ESTREITA DO QUE A SAÍDA ANTECIPADA DO SELO, e não um duplicado dela. O
// selo recusa a hospedagem quando o layout pede handles e o espaço não está composto, mesmo que
// todas as entradas já venham seladas — e essa recusa tem de continuar a ser dele. Aqui a
// pergunta é outra e mais apertada: «vai ser escrito algum byte novo?». Um teste amarra as duas
// (haEntradasPorSelar ⇒ o selo cunha), sem afirmar o recíproco.
//
// E NÃO DIZ SE A HOSPEDAGEM É A PRIMEIRA. Dizê-lo foi o erro da primeira versão deste ficheiro:
// há uma re-hospedagem que chega com as entradas por selar (ver o cabeçalho). Quem o diz é a
// bandeira `rs.existente`.
func haEntradasPorSelar(goal agentruntime.Goal) bool {
	if len(goal.Inputs) == 0 || !agentruntime.HasPlanInputsByHandle(goal.AssemblyVersion) {
		return false
	}
	for i := range goal.Inputs {
		if goal.Inputs[i].Handle == "" {
			return true
		}
	}
	return false
}

// ofertaDoNo devolve a oferta contra a qual a conferência decide: os nomes que o catálogo tem
// activos, CORTADOS pela lista-branca do run — pela MESMA regra com que o
// [integration.ApplyFrozenToGoal] corta o tool set congelado ([referencemonitor.RunAllowsTool],
// comparação exacta). Uma lista-branca nil não corta nada, como lá.
//
// O corte não muda o veredicto (o portão do kernel confere a lista-branca à parte, pela mesma
// função): muda a CAUSA, que é o que quem submete lê.
func ofertaDoNo(nomes, listaBranca []string) []string {
	if listaBranca == nil {
		return nomes
	}
	out := make([]string, 0, len(nomes))
	for _, nome := range nomes {
		if referencemonitor.RunAllowsTool(listaBranca, nome) {
			out = append(out, nome)
		}
	}
	return out
}

// recusaAntesDoSelo corre, ANTES de qualquer selagem, a recusa de arranque que o kernel faria
// depois dela — contra a OFERTA do nó, cortada pela lista-branca do run. Devolve nil quando o run
// não é recusado, quando não há nada por selar, e quando esta NÃO é a primeira hospedagem do run
// (ver o âmbito, no cabeçalho: ali a oferta fiel é o snapshot durável e não o catálogo).
//
// A RECUSA DO PORTÃO SAI NUA, e quem chama grava-a SEM A EMBRULHAR. A razão é que o `error` de um
// run recusado no arranque é servido em TEXTO no `GET /runs/{id}` (`resp.Error`), e até aqui esse
// texto era o erro nu que o `Run` devolvia: embrulhá-lo mudava a resposta a quem submete por uma
// recusa que é a mesma. É a excepção deliberada ao idioma dos vizinhos deste bloco, que embrulham
// porque a recusa deles não tem um «antes» com que ter de coincidir.
//
// A FALHA DE LEITURA DA OFERTA é o único erro que esta função embrulha, e aí é o idioma certo:
// não é uma recusa do portão, é o nó a não conseguir perguntar. Não tem «antes» nenhum em texto —
// o que o `Run` servia nesse caso era o erro do congelamento, que também não é o do portão.
//
// O nó e o runtime nunca são nil nesta posição — o `hostRun` já desreferenciou o runtime no
// `RebuildLedger`, muito acima. O guarda é defesa em profundidade para um [NodeService] montado à
// mão em teste, e não um caminho em que o portão do kernel ainda decidiria.
func (s *NodeService) recusaAntesDoSelo(ctx context.Context, rs *runState, goal agentruntime.Goal) error {
	if rs == nil || rs.existente || !haEntradasPorSelar(goal) {
		return nil
	}
	if s.node == nil || s.node.Runtime == nil {
		return nil
	}
	nomes, err := s.node.Runtime.OfferedToolNames(ctx)
	if err != nil {
		return fmt.Errorf("aos: oferta de tools do no para a conferencia de arranque do run %q (AOS-557): %w", goal.RunID, err)
	}
	return agentruntime.RefuseAtStartup(goal, ofertaDoNo(nomes, goal.AllowedTools))
}
