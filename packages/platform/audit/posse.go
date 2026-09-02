package audit

import (
	"context"
	"errors"
	"sync"
)

// posse.go — posse EXCLUSIVA de escrita por partição (AC1 e AC3 do AOS-284).
//
// # O defeito que isto fecha
//
// A hash-chain é sequencial por construção e o seu único guarda era um mutex EM PROCESSO
// (`FileStore.mu`). O próprio filestore.go declarava o limite — «a ordem total por-partição
// é a ordem de entrada no lock» —, e um lock não atravessa processos. Dois escritores na
// mesma partição bifurcam a cadeia; medido em aos284_dois_escritores_test.go, com a
// consequência de o WORM ficar INABRÍVEL e o nó não voltar a arrancar.
//
// # Porque é uma PORTA e não uma dependência
//
// A arbitragem que serve é o lease DURÁVEL do ADR-023, que vive em
// `kernel/agent-runtime/durable`. O `platform/audit` não depende dele nem deve: importá-lo
// inverteria a camada, e o `go.mod` deste módulo declara ZERO dependências para lá do
// Reference Monitor. A porta é de UM método, e a implementação vive em quem já conhece as
// duas coisas — o ápice de composição, ou o nó.
//
// É o mesmo padrão que o `eventstore.Rastreador` usa para o OTel, e pela mesma razão:
// trocar uma propriedade de arquitectura por conveniência de chamada é um mau negócio que
// só se descobre tarde.
//
// # O que esta porta NÃO resolve, e tem de ficar dito
//
// Entre o `Detem` responder «sim» e a escrita ficar durável há uma janela. Um lease pode
// expirar aí — e o detentor pode não o saber ainda. É a janela INERENTE a qualquer lease, e
// fechá-la exige um token de fencing aceite pelo destino da escrita (ADR-001/023); um
// ficheiro local não tem onde o validar. Portanto: isto dá EXCLUSÃO ENTRE RÉPLICAS BEM
// COMPORTADAS, não exclusão contra um processo que perdeu a posse e continua a escrever.
// A rede de segurança do segundo caso é a detecção — a cadeia bifurcada é RECUSADA e
// CLASSIFICADA (ver [TamperFork]), em vez de aceite em silêncio.

// PosseDeParticao é a porta de posse exclusiva de escrita por partição.
//
// Uma implementação responde se ESTE processo detém a partição NESTE instante. A
// implementação natural é o lease durável do ADR-023, chaveado pelo nome da partição.
type PosseDeParticao interface {
	// Detem diz se este processo detém a partição agora.
	//
	// Um erro NÃO é «não detém»: é «não se sabe», e o [FileStore] trata os dois da mesma
	// maneira — recusa. Não conseguir estabelecer a posse não autoriza a escrever.
	Detem(ctx context.Context, particao string) (bool, error)
}

// ErrParticaoAlheia — a escrita foi RECUSADA porque este processo não detém a partição, ou
// porque não foi possível saber se a detém (fail-closed).
//
// A recusa é ANTES de qualquer efeito: nada é selado, nada é persistido, e o audit_seq não
// é consumido. É a diferença entre uma escrita recusada e uma escrita a meio.
var ErrParticaoAlheia = errors.New("audit: escrita recusada — este processo nao detem a particao (E_PARTITION_NOT_OWNED)")

// FileStoreOption configura um [FileStore] na abertura.
type FileStoreOption func(*FileStore)

// ComPosseDeParticao arma a disciplina de posse.
//
// SEM esta opção o [FileStore] comporta-se exactamente como antes — um único escritor
// in-process, sem arbitragem. É deliberado e não é um esquecimento: em v1 o nó é o único
// escritor e o guard de arranque (AOS-285/286) recusa um segundo. A disciplina existe para
// o deployment em que dois escritores são LEGÍTIMOS, e ligá-la é uma decisão de quem
// compõe, não um default que mudaria o comportamento de todos os chamadores actuais.
func ComPosseDeParticao(p PosseDeParticao) FileStoreOption {
	return func(s *FileStore) { s.posse = p }
}

// recusasDePosse conta as recusas por partição (AC3: o facto de recusa é OBSERVÁVEL).
//
// Contar por partição e não só no total é o que torna o contador accionável: «houve 400
// recusas» diz que algo está mal; «houve 400 recusas em gov.read/run-7» diz ONDE, e é a
// diferença entre um alarme e um diagnóstico.
type recusasDePosse struct {
	mu    sync.Mutex
	porID map[string]uint64
	total uint64
}

func (r *recusasDePosse) registar(particao string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.porID == nil {
		r.porID = make(map[string]uint64)
	}
	r.porID[particao]++
	r.total++
}

func (r *recusasDePosse) instantaneo() (uint64, map[string]uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copia := make(map[string]uint64, len(r.porID))
	for k, v := range r.porID {
		copia[k] = v
	}
	return r.total, copia
}

// RecusasDePosse devolve o total de escritas recusadas por falta de posse e a repartição
// por partição (uma CÓPIA — o chamador não pode mutar o estado do store).
//
// É o que torna o AC3 verificável de fora: sem isto, uma recusa seria um erro devolvido a
// um chamador que talvez o engula, e o operador nunca saberia que a disciplina disparou.
func (s *FileStore) RecusasDePosse() (uint64, map[string]uint64) {
	return s.recusas.instantaneo()
}

// autorizadoAEscrever consulta a porta de posse. Sem porta armada, autoriza (comportamento
// anterior). Com porta, um «não» e um erro dão o MESMO desfecho — recusar.
func (s *FileStore) autorizadoAEscrever(ctx context.Context, particao string) error {
	if s.posse == nil {
		return nil
	}
	detem, err := s.posse.Detem(ctx, particao)
	if err != nil {
		s.recusas.registar(particao)
		return errors.Join(ErrParticaoAlheia, err)
	}
	if !detem {
		s.recusas.registar(particao)
		return ErrParticaoAlheia
	}
	return nil
}
