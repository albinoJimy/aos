package integration

// approval_stream_migracao.go — A FILA DE APROVAÇÕES MUDA DE NOME SEM PERDER O USO-ÚNICO
// (AOS-424, decisão 3).
//
// # PORQUÊ
//
// O stream chamava-se `gov.approvals`. Um `stream_id` do AOS é livre, mas um subject NATS não
// é: o ponto separa tokens, e o `jetstream.Store.subjectDe` RECUSA qualquer `stream_id` que o
// contenha. Sobre JetStream a cerimónia four-eyes estava inteiramente inoperante — e o pior
// dos modos era o `PendingApprovals.Put` a falhar: o operador nunca via o que tinha para
// aprovar, fail-closed E invisível.
//
// O JetStream é o único substrato que arbitra entre processos (DEF-282), logo o único em que o
// caminho do plano pode ter consumidor. Enquanto este nome tivesse um ponto, essa migração de
// topologia destruía a governação de aprovações.
//
// # PORQUE É QUE ISTO NÃO É TROCAR UMA CONSTANTE
//
// Este stream tem HISTÓRICO em produção, e o que ele guarda não é telemetria: são grants de
// aprovação four-eyes, pendentes por decidir, expirações, decisões humanas e os registos de
// retoma (que carregam o `Goal`). Renomear sem migrar deixa runs suspensos sem a aprovação que
// os destrava, e o operador sem a lista do que lhe falta decidir.
//
// E o veículo óbvio está fechado: o `platform/backup` não serve, porque o `IngestStream`
// (`jetstream/backup.go`) passa pelo MESMO `subjectDe` — um backup tirado de um nó WAL aborta
// ao restaurar para JetStream, e `gov.approvals` é alfabeticamente anterior a `memory.*` e a
// `run-*`, pelo que nem os streams de run chegam a ser tentados.
//
// # PORQUE NÃO SE FAZ LEITURA DUPLA
//
// O uso-único atómico do `Consume` assenta na DEDUPLICAÇÃO DO EVENT STORE, que é **por
// stream**: reclamar um grant é um append com `StepID: "used-"+id`, e o `StatusDuplicate` é o
// primitivo atómico. Com dois streams vivos, um grant consumido no ANTIGO não deduplica no
// NOVO — e a garantia «uma aprovação destrava no máximo UMA execução» quebra-se durante toda a
// janela. Numa cerimónia de quatro olhos, isso não é um incómodo operacional: é a propriedade
// que ela existe para dar.
//
// # O DESENHO: COPIAR E CORTAR, NUMA SÓ VEZ
//
// Copiam-se os factos do nome antigo para o novo PRESERVANDO `(RunID, StepID)` — e é daí que
// vem a correcção inteira. A idempotency-key é `run_id + ":" + step_id`; copiada verbatim, um
// `used-<id>` que existia no antigo passa a bloquear, no NOVO, qualquer tentativa de reclamar
// o mesmo grant. O uso-único atravessa a migração intacto.
//
// A cópia é, pela mesma razão, IDEMPOTENTE: corrê-la de novo devolve `StatusDuplicate` em cada
// facto e não duplica nada. Isso é o que a torna segura de pôr no arranque e retomável se
// falhar a meio.
//
// A ORDEM RELATIVA preserva-se: lê-se por `seq` ascendente e apende-se nessa ordem. Os `seq` do
// stream novo são outros — o que os consumidores usam é a ordem, não o valor (`lookup` varre do
// fim para o início, `geracaoDe` conta ocorrências).
//
// # O QUE ESTA MIGRAÇÃO **NÃO** GARANTE, E TEM DE SER DITO
//
// Assume **UM ÚNICO ESCRITOR** durante a cópia. O substrato de ficheiro impõe-o (`LockWAL`), e
// é o que corre em produção. Sobre `--nats` com várias réplicas, um binário ANTIGO ainda a
// escrever no nome antigo depois da cópia deixa um facto por copiar — e se esse facto for um
// `used-`, abre-se a janela de duplo-consumo que todo este ficheiro existe para fechar. Numa
// topologia replicada, **todas as réplicas têm de estar no binário novo** antes de a garantia
// valer. Não se resolve isto aqui: declara-se.
//
// O stream ANTIGO não é apagado — um log append-only não apaga. Fica lá, inerte.
//
// # O ÍNDICE TITULAR→PARTIÇÃO, E UMA AFIRMAÇÃO QUE EU FIZ E ERA FALSA
//
// A primeira versão deste ficheiro dizia que o `restoreSubjectIndex` religa os titulares aos
// DOIS nomes no arranque, e que isso era «sobre-cobertura de legal hold, a direcção segura».
// **Não é verdade, e a direcção é a contrária.** O `restoreSubjectIndex` filtra por
// `subjectOf`, que reconhece apenas `replay.captured` e `step.ledger.applied` — nenhum facto
// deste stream é de uma dessas famílias. O índice não religa ao nome novo NEM ao antigo.
//
// A única ligação titular→partição para este stream é feita AO VIVO pelo `contentSealer`, em
// memória, no momento de cada escrita — e depois do rename passa a apontar para o nome novo.
// **Consequência, declarada:** um legal hold DURÁVEL posto sobre a partição `gov.approvals`
// (restaurado pelo nome literal) deixa de intersectar as partições de qualquer titular, e
// portanto deixa de se estender às OUTRAS partições desse titular. É SUB-cobertura, que é o
// fail-open que o AOS-352 documenta como o pior dos quatro.
//
// O gatilho é condicional (exige um hold posto sobre essa partição exacta) e esta migração
// **não o re-chaveia**. Quem tiver holds sobre `gov.approvals` tem de os repor sobre o nome
// novo. Fica escrito aqui em vez de descoberto por quem contar com o hold.
//
// O que CONTINUA verdadeiro, e foi verificado: a DECIFRAÇÃO atravessa o rename. O
// `audit.SealContent` não recebe sequer o `streamID` — cifra por titular — e o `OpenContent`
// resolve pelo mesmo titular. Um registo de retoma selado antes continua decifrável depois.
//
// # ROLLBACK — O QUE ACONTECE SE SE VOLTAR AO BINÁRIO ANTERIOR
//
// **Reabre o duplo-consumo.** Um grant migrado e consumido pelo binário NOVO tem o seu
// `used-<id>` apenas no stream novo; o binário antigo lê `gov.approvals`, onde esse marcador
// não existe, e o grant volta a ser consumível. É a propriedade que o four-eyes existe para
// dar, perdida na direcção que a primeira versão deste ficheiro não cobria.
//
// **E pode perder factos em silêncio.** Um facto escrito pelo binário antigo durante a janela
// de rollback, cuja `(RunID, StepID)` já exista no stream novo, vem `StatusDuplicate` no
// roll-forward: não atravessa, `copiados` não o conta, e o log de arranque não diz nada. A
// ordem relativa também deixa de valer para os factos legados tardios.
//
// **Postura:** o rollback DEPOIS desta migração não é seguro para a cerimónia four-eyes, e
// não há aqui código que o torne seguro — um log append-only não desfaz. Quem precisar de
// reverter tem de o fazer sabendo isto, e verificar os grants pendentes à mão antes de voltar
// a aceitar aprovações.

import (
	"context"
	"errors"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// approvalStreamLegado é o nome ANTIGO, e existe só para a migração o conseguir ler.
//
// Não é usado em escrita em lado nenhum: o `approvalStream` é o nome em vigor. Fica na
// baseline do gate `stream-names` enquanto esta constante existir — é dívida declarada, e a
// entrada da baseline nomeia-a.
const approvalStreamLegado = "gov.approvals"

// MigrarAprovacoes copia os factos de aprovação do stream LEGADO para o actual.
//
// Devolve quantos factos foram COPIADOS NESTA PASSAGEM (os que já lá estavam contam como
// duplicados e não entram), para que o chamador possa declarar no arranque o que aconteceu —
// uma migração silenciosa de material de governação seria pior do que nenhuma.
//
// FAIL-CLOSED: qualquer erro aborta e propaga. O chamador **não pode** compor a cerimónia
// four-eyes sobre uma migração parcial: um `used-` por copiar é um grant que se pode consumir
// duas vezes.
func MigrarAprovacoes(ctx context.Context, store approvalAppendReader) (int, error) {
	if store == nil {
		return 0, errors.New("integration: migracao de aprovacoes exige o Event Store (nil)")
	}
	if approvalStreamLegado == approvalStream {
		// Defesa contra uma edição futura que iguale os dois nomes: copiar um stream para si
		// mesmo seria um no-op silencioso, e o silêncio aqui esconderia a perda da migração.
		return 0, fmt.Errorf("integration: o stream legado e o actual sao o mesmo (%q) — "+
			"a migracao nao tem o que fazer e isso quase de certeza nao e o que se queria",
			approvalStream)
	}

	antigos, err := store.Read(ctx, approvalStreamLegado, 0)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			// Nó fresco, ou migração já feita num nó cujo stream antigo nunca existiu. Não é
			// erro: é a maioria dos casos depois da primeira passagem.
			return 0, nil
		}
		if errors.Is(err, eventstore.ErrConfig) {
			// O BACKEND NÃO CONSEGUE SEQUER NOMEAR O STREAM LEGADO — e isso significa que ele
			// NÃO PODE TER NADA.
			//
			// Sobre JetStream o `subjectDe` recusa o ponto LEXICALMENTE, antes de tocar na rede,
			// e o `Read` propaga `ErrConfig`. A primeira versão desta função tolerava apenas o
			// `ErrStreamNotFound` e propagava isto — o `Bootstrap` abortava, e **um nó JetStream
			// com four-eyes deixava de arrancar, sempre**, tivesse ou não factos legados. Era o
			// inverso exacto do propósito deste ficheiro: a migração que existe para destrancar o
			// four-eyes sobre JetStream era a única coisa que o impedia de correr lá. Uma revisão
			// adversarial provou-o; o smoke não o via porque corre sobre WAL de ficheiro.
			//
			// **ISTO NÃO É TOLERÂNCIA A ERRO, É A VERDADE DAQUELE BACKEND.** O `Append`
			// (`jetstream/store.go`) e o `IngestStream` do restauro passam pelo MESMO `subjectDe`:
			// um stream com ponto nunca pôde receber uma escrita nesse substrato, nem por
			// restauro de backup. Logo «não consigo ler o nome legado» e «o nome legado não tem
			// factos» são, ali, a MESMA afirmação.
			//
			// O âmbito é estreito de propósito: só o `Read` do nome LEGADO, e só este sentinela.
			// Um `ErrConfig` na ESCRITA do nome novo continua a abortar — esse seria um nome em
			// uso que o backend recusa, que é um defeito e não um facto.
			return 0, nil
		}
		return 0, fmt.Errorf("integration: ler o stream legado de aprovacoes: %w", err)
	}

	copiados := 0
	for _, ev := range antigos {
		// PRESERVA `(RunID, StepID)`: é a idempotency-key, e é ela que faz o uso-único
		// atravessar a migração. Preserva também o `SchemaVersion` e o `Producer` — o facto
		// copiado tem de continuar a dizer quem o emitiu e sob que schema, senão a cópia é
		// uma falsificação com melhor intenção.
		res, aerr := store.Append(ctx, approvalStream, eventstore.EventInput{
			Type:          ev.Type,
			Payload:       ev.Payload,
			SchemaVersion: ev.SchemaVersion,
			RunID:         ev.RunID,
			StepID:        ev.StepID,
			ParentStepID:  ev.ParentStepID,
			Producer:      ev.Producer,
		})
		if aerr != nil {
			return copiados, fmt.Errorf("integration: copiar o facto %q (step %q) para %q: %w",
				ev.Type, ev.StepID, approvalStream, aerr)
		}
		if res.Status != eventstore.StatusDuplicate {
			copiados++
		}
	}
	return copiados, nil
}
