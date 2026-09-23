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
// # ONDE É IMPOSTA
//
// **Na ESCRITA, nos dois backends** — [Store.Append] no de ficheiro e o `subjectDe` no de
// JetStream. É o «aperto do contrato» do AOS-424, e fecha a assimetria que deixava um nome
// funcionar sobre WAL e falhar sobre NATS.
//
// **Na LEITURA, não.** [Store.Read], [Store.StreamHead], [Store.SnapshotStream] e
// [Store.IngestStream] do backend de ficheiro continuam a aceitar nomes legados, e isso é
// deliberado: um nome que já existe tem de poder ser LIDO para ser migrado para fora, e um
// backup anterior a esta regra tem de poder ser RESTAURADO. A regra proíbe CRIAR nomes
// irrepresentáveis; não proíbe recuperar os que existem. Está fixado por teste — sem ele,
// alguém «arruma» a assimetria e leva o restauro de desastre à frente.
//
// # O QUE A REGRA NÃO ALCANÇA
//
// Nomes COMPOSTOS em runtime a partir de valores que entram por política, por token externo ou
// por corpo de pedido. A regra recusa-os no ponto de USO, que para alguns caminhos é tarde
// demais — é o AOS-425. Dois desses caminhos foram corrigidos na origem quando este aperto os
// revelou (ver `hitl/nome_de_escopo.go`); o resto da tabela do AOS-425 continua aberto.

import (
	"fmt"
	"strings"
)

// CaracteresNaoRepresentaveis são os que um subject NATS não representa e que se NOMEIAM: o
// ponto separa tokens, `*` e `>` são curingas, e o espaço em branco não é transportável.
//
// É a FONTE desta regra para quem a lê de fora — o gate `scripts/ci/stream-names` extrai esta
// constante para varrer os literais da árvore. Quem precisar dela em Go chama [ValidarStreamID],
// que impõe ESTA lista MAIS os restantes caracteres de controlo (ver abaixo porquê).
const CaracteresNaoRepresentaveis = ". *>\t\r\n"

// CaractereNaoRepresentavel diz se UM carácter não pode aparecer num `stream_id`.
//
// # PORQUE É QUE ISTO EXISTE SEPARADO DE [ValidarStreamID]
//
// Há um consumidor que não valida um nome inteiro: o escape do `node_id` no `childRunID`
// (AOS-424), que decide carácter a carácter o que escapar. Ele derivava a decisão da constante
// [CaracteresNaoRepresentaveis] — e essa é um SUBCONJUNTO PRÓPRIO do que a regra recusa, desde
// que a regra passou a apanhar a classe dos caracteres de controlo.
//
// Estava tapado por acaso (a gramática do `node_id` tem charset fechado) e não por construção.
// «Tapado por acaso» é como a classe inteira do AOS-424 sobreviveu; agora as duas pontas
// decidem pela MESMA função.
func CaractereNaoRepresentavel(r rune) bool {
	return strings.ContainsRune(CaracteresNaoRepresentaveis, r) || r < 0x20 || r == 0x7f
}

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
	// OS RESTANTES CARACTERES DE CONTROLO, e porque não estão na constante acima.
	//
	// A constante nomeia os que alguém escreve à mão num literal, e é isso que o gate de
	// repositório varre. Um NUL não se escreve à mão — CHEGA AQUI POR COMPOSIÇÃO, e foi assim
	// que apareceu: o `nonceScope` do autenticador juntava domínio e emissor com um `\x00`, e
	// esse byte ia inteiro para o nome do stream. A versão anterior desta função deixava-o
	// passar, porque a lista nomeada não o continha.
	//
	// Uma lista de proibidos escrita à mão só cobre o que quem a escreveu se lembrou. Para uma
	// CLASSE inteira — caracteres de controlo — a pertença decide-se por propriedade.
	if i := strings.IndexFunc(streamID, CaractereNaoRepresentavel); i >= 0 {
		return fmt.Errorf("%w: stream_id %q contém o carácter de controlo %#x na posição %d, "+
			"que não é transportável num subject NATS. Nomes de stream compostos a partir de "+
			"vários campos não devem usar um byte de controlo como separador — use um resumo "+
			"do tuplo (ver `hitl.nomeDeEscopo`)",
			ErrConfig, streamID, streamID[i], i)
	}
	return nil
}
