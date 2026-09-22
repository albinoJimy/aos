package eventstore

// migracao.go — COPIAR UM STREAM PARA OUTRO NOME, SEM PERDER O QUE A ORDEM E A CHAVE GARANTEM.
//
// # PORQUE É QUE ISTO VIVE AQUI, E NÃO EM CADA CHAMADOR
//
// Renomear um stream é, no AOS, uma operação recorrente e não um acidente: o AOS-424 mediu NOVE
// `stream_id` da árvore que não eram representáveis num subject NATS, e o `subjectDe` recusa-os.
// Cada rename de um stream COM histórico obriga a copiar os factos.
//
// A primeira dessas migrações — a da fila de aprovações — foi escrita à mão e tinha um defeito
// CRÍTICO que uma revisão adversarial encontrou: tolerava `ErrStreamNotFound` e não `ErrConfig`.
// Sobre JetStream o `Read` de um nome com ponto devolve `ErrConfig` (o `subjectDe` recusa
// LEXICALMENTE, antes de tocar na rede), a migração propagava, e **o nó deixava de arrancar** —
// exactamente no substrato que o rename existia para destrancar.
//
// Essa subtileza é o que esta função concentra. Duas cópias dela é como ela volta.
//
// # A REGRA QUE A TORNA CORRECTA
//
// Copia-se preservando `(RunID, StepID)` — a idempotency-key do store é `run_id + ":" + step_id`
// e a deduplicação é POR STREAM. Copiada verbatim, a chave continua a valer no destino:
//
//   - um marcador de CONSUMO (`used-<id>`) continua a bloquear uma segunda reclamação;
//   - um TOMBSTONE de memória continua a apagar o registo que apagava;
//   - e re-correr a cópia devolve `StatusDuplicate` em cada facto, pelo que é IDEMPOTENTE —
//     o que a torna segura no arranque e retomável se falhar a meio.
//
// A ORDEM RELATIVA preserva-se: lê-se por `seq` ascendente e apende-se nessa ordem. Os `seq` do
// destino são OUTROS, e isso é deliberado — o que os consumidores usam é a ordem (varreduras do
// fim para o início, last-write-wins, contagem de gerações), não o valor.
//
// # O QUE ISTO NÃO FAZ
//
// Não apaga a origem — um log append-only não apaga. Não arbitra entre processos: assume **um
// único escritor** durante a cópia, o que o substrato de ficheiro impõe pela tranca do WAL. Numa
// topologia replicada, um escritor antigo ainda a escrever na origem depois da cópia deixa
// factos por copiar; quem migrar aí tem de garantir que todos os escritores já cortaram.

import (
	"context"
	"errors"
	"fmt"
)

// CopiadorDeStream é o subconjunto do store de que a cópia depende.
type CopiadorDeStream interface {
	Append(ctx context.Context, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]Event, error)
}

// CopiarStream copia os factos de `origem` para `destino`, preservando a idempotency-key, a
// ordem relativa e o envelope.
//
// Devolve quantos factos foram COPIADOS NESTA PASSAGEM — os que já lá estavam vêm
// `StatusDuplicate` e não entram na conta. É esse número que um chamador declara no arranque:
// uma migração silenciosa não deixa ninguém saber que aconteceu.
//
// FAIL-CLOSED em tudo o que não seja «a origem não tem nada»: o chamador não pode seguir sobre
// uma cópia parcial.
func CopiarStream(ctx context.Context, store CopiadorDeStream, origem, destino string) (int, error) {
	if store == nil {
		return 0, errors.New("eventstore: copiar stream exige o store (nil)")
	}
	if origem == destino {
		// Defesa contra uma edição futura que iguale os dois nomes: copiar um stream para si
		// mesmo seria um no-op SILENCIOSO, e o silêncio esconderia a perda da migração.
		return 0, fmt.Errorf("eventstore: origem e destino sao o mesmo stream (%q) — a copia nao "+
			"tem o que fazer, e isso quase de certeza nao e o que se queria", origem)
	}

	factos, err := store.Read(ctx, origem, 0)
	if err != nil {
		if errors.Is(err, ErrStreamNotFound) {
			// A origem nunca existiu, ou a migração já correu num nó que nunca a teve. É o caso
			// da esmagadora maioria dos arranques depois da primeira passagem.
			return 0, nil
		}
		if errors.Is(err, ErrConfig) {
			// O BACKEND NÃO CONSEGUE SEQUER NOMEAR A ORIGEM — e isso significa que ela NÃO PODE
			// TER NADA.
			//
			// É o defeito CRÍTICO que a primeira migração escrita à mão teve. Sobre JetStream o
			// `subjectDe` recusa um `stream_id` com `.`/`*`/`>`/espaço LEXICALMENTE, antes de
			// tocar na rede, e o `Read` propaga `ErrConfig`. Tolerar só o `ErrStreamNotFound`
			// fazia a migração abortar o arranque de qualquer nó replicado, tivesse ou não
			// factos a migrar — até um nó fresco.
			//
			// **NÃO É TOLERÂNCIA A ERRO, É A VERDADE DAQUELE BACKEND.** O `Append`, o
			// `StreamHead` e o `IngestStream` do restauro passam pelo MESMO `subjectDe`: um
			// stream cujo nome ele recusa nunca pôde receber uma escrita ali, nem por restauro
			// de backup. «Não consigo ler a origem» e «a origem não tem factos» são, nesse
			// backend, a mesma afirmação.
			//
			// O âmbito é estreito: só a LEITURA DA ORIGEM. Um `ErrConfig` na escrita do destino
			// propaga — esse seria um nome EM USO que o backend recusa, que é um defeito.
			return 0, nil
		}
		return 0, fmt.Errorf("eventstore: ler a origem %q: %w", origem, err)
	}

	copiados := 0
	for _, ev := range factos {
		// O ENVELOPE INTEIRO. Um facto copiado tem de continuar a dizer quem o emitiu, sob que
		// schema, e de que passo descende — uma cópia que reescrevesse isso seria uma
		// falsificação com melhor intenção, e este material costuma ser de governação.
		res, aerr := store.Append(ctx, destino, EventInput{
			Type:          ev.Type,
			Payload:       ev.Payload,
			SchemaVersion: ev.SchemaVersion,
			RunID:         ev.RunID,
			StepID:        ev.StepID,
			ParentStepID:  ev.ParentStepID,
			Producer:      ev.Producer,
		})
		if aerr != nil {
			return copiados, fmt.Errorf("eventstore: copiar o facto %q (step %q) de %q para %q: %w",
				ev.Type, ev.StepID, origem, destino, aerr)
		}
		if res.Status != StatusDuplicate {
			copiados++
		}
	}
	return copiados, nil
}
