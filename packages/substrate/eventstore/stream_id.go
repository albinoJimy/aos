package eventstore

// stream_id.go — A REGRA DO QUE UM `stream_id` PODE SER, NUM SÓ SÍTIO.
//
// # PORQUÊ
//
// Esta regra existia em TRÊS cópias: o `ContainsAny` do `jetstream.Store.subjectDe`, a constante
// `caracteresNaoRepresentaveis` do nó, e a extracção por regex do gate `scripts/ci/stream-names`.
// As três com a mesma lista de caracteres, escrita à mão, e nada estrutural a ligá-las.
//
// O AOS-424 mediu o que isso custa. Duas cópias da subtileza do `ErrConfig` já tinham deixado um
// defeito CRÍTICO voltar a meio do mesmo ticket; e a regra em si esteve a um `ContainsAny`
// acrescentado acima do `subjectDe` de fazer o gate medir só o ponto, em verde.
//
// Agora há uma função, e os outros dois lêem-na ou chamam-na.
//
// # O QUE ESTA REGRA NÃO FAZ (ainda), E PORQUE NÃO
//
// **Não é imposta na escrita do backend de ficheiro.** Esse é o «aperto do contrato» que o
// AOS-424 propõe como correcção da CAUSA-RAIZ — a assimetria entre backends —, e está
// BLOQUEADO por uma cadeia que termina fora do código:
//
//	apertar o Append  →  exige um `node_id` stream-safe
//	                  →  exige apertar o `plan.ValidNodeID`
//	                  →  exige uma versão nova do prompt de decomposição
//	                  →  exige revalidar a decomposição com o modelo vivo
//
// O prompt em vigor (1.2.0) diz ao modelo, por escrito, que o `node_id` aceita
// `[A-Za-z0-9_.:-]`. O `childRunID` compõe `<run>~<node_id>` e submete-o ao nó, pelo que um nó
// de plano chamado `analise.dados` produz um `run_id` com ponto — que é o `stream_id` do run.
// Apertar o `Append` hoje **mataria esse run a meio**, e sobre o substrato de ficheiro, que é o
// que corre em produção.
//
// Esta função é o pré-requisito comum a qualquer das saídas: quando a decisão for tomada, o
// `Append` chama-a e a assimetria fecha-se num sítio.

import (
	"fmt"
	"strings"
)

// CaracteresNaoRepresentaveis são os que um subject NATS não representa: o ponto separa tokens,
// `*` e `>` são curingas, e o espaço em branco não é transportável.
//
// É a FONTE desta regra. Quem precisar dela chama [ValidarStreamID] ou lê esta constante — não
// escreve a lista outra vez.
const CaracteresNaoRepresentaveis = ". *>\t\r\n"

// ValidarStreamID devolve erro se o `stream_id` não for representável em todos os backends.
//
// O erro embrulha [ErrConfig], que é o que o backend replicado já devolve para o mesmo caso: um
// chamador que trate `ErrConfig` continua a tratá-lo, venha de onde vier.
//
// PORQUE É QUE A RECUSA É A ESCOLHA CERTA, e não escapar: escapar `a.b` para `a-b` faria dois
// `stream_id` distintos colidirem no mesmo subject, e um stream leria os eventos do outro. Um
// nome que não se representa recusa-se; não se aproxima.
func ValidarStreamID(streamID string) error {
	if streamID == "" {
		return fmt.Errorf("%w: stream_id vazio", ErrConfig)
	}
	if i := strings.IndexAny(streamID, CaracteresNaoRepresentaveis); i >= 0 {
		return fmt.Errorf("%w: stream_id %q contém %q, que não é representável num subject NATS "+
			"(os proibidos são `. * >`, espaço, tab, CR e LF). Use `-` onde a tentação for um `.`, "+
			"e `/` para separar níveis — a barra é representável e mantém o stream fora do alcance "+
			"de `GET /runs/{id}/...`",
			ErrConfig, streamID, streamID[i])
	}
	return nil
}
