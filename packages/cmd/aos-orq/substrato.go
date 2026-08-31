package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// substrato.go — de onde vem o Event Store deste processo (AOS-100).
//
// # As duas topologias, e porque a diferença é do binário e não de uma opção escondida
//
// O `aos-orq` demonstra a posse de um run por lease durável entre PROCESSOS REAIS cujo
// único canal de coordenação é o Event Store. Isso significa que o substrato NÃO é um
// detalhe de configuração: é a variável independente da demonstração.
//
//   - `--wal F`: Event Store de REFERÊNCIA sobre um ficheiro. NÃO arbitra entre
//     processos (DEF-282, medido), pelo que o escritor toma a POSSE EXCLUSIVA do
//     ficheiro (AOS-285/286) e dois `serve` em simultâneo são RECUSADOS. A topologia
//     suportada é a posse SEQUENCIAL.
//   - `--nats ADDR`: Event Store REPLICADO (JetStream). ARBITRA entre processos — o
//     `expected_seq` é imposto pelo servidor —, pelo que dois `serve` em simultâneo são
//     a configuração PRETENDIDA e o vencedor é decidido pelo LEASE, não por um ficheiro.
//
// A escolha é EXCLUSIVA de propósito. Aceitar os dois e escolher um por precedência
// daria um processo que anuncia coordenação distribuída enquanto trancava um ficheiro
// local — o modo de falha mais caro possível, porque parece que funciona.
type substrato struct {
	wal      string
	nats     string
	stream   string
	regiao   string
	replicas int
}

// registarFlags declara as flags de substrato num FlagSet.
func (s *substrato) registarFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.wal, "wal", "", "caminho do WAL do Event Store de referência (topologia de posse SEQUENCIAL)")
	fs.StringVar(&s.nats, "nats", "", "endereço host:porta de um cluster NATS JetStream (Event Store REPLICADO — arbitra entre processos, AOS-100)")
	fs.StringVar(&s.stream, "nats-stream", "", "nome do stream JetStream (só com --nats; vazio usa o padrão)")
	fs.IntVar(&s.replicas, "nats-replicas", 0, "factor de replicação do stream (só com --nats; vazio usa 3)")
	fs.StringVar(&s.regiao, "nats-region", "", "região da fronteira de soberania do board (só com --nats; vazio deixa a fronteira dormente — ADR-011)")
}

var errSubstratoAmbiguo = errors.New("--wal e --nats são EXCLUSIVOS: um é o store de referência sobre ficheiro (posse sequencial), o outro é o replicado que arbitra entre processos. Aceitar ambos daria um processo a anunciar coordenação distribuída enquanto trancava um ficheiro local")

func (s substrato) validar() error {
	switch {
	case s.wal == "" && s.nats == "":
		return errors.New("indique --wal FICHEIRO ou --nats HOST:PORTA (o Event Store é o único canal de coordenação)")
	case s.wal != "" && s.nats != "":
		return errSubstratoAmbiguo
	case s.nats == "" && (s.stream != "" || s.replicas != 0 || s.regiao != ""):
		return errors.New("--nats-stream/--nats-replicas/--nats-region só fazem sentido com --nats")
	case s.replicas < 0:
		return errors.New("--nats-replicas tem de ser um inteiro positivo (3 ou 5; 1 é só dev)")
	}
	return nil
}

// replicado indica se o substrato ARBITRA entre processos.
func (s substrato) replicado() bool { return s.nats != "" }

// descrever devolve a linha que o processo imprime para que o operador saiba, sem
// adivinhar, se pode correr uma segunda instância.
func (s substrato) descrever() string {
	if s.replicado() {
		if s.regiao != "" {
			return fmt.Sprintf("substrato: REPLICADO em %s, confinado a %q — arbitra entre processos (AOS-100); N instâncias em paralelo são suportadas", s.nats, s.regiao)
		}
		return fmt.Sprintf("substrato: REPLICADO em %s, SEM fronteira regional — arbitra entre processos (AOS-100); N instâncias em paralelo são suportadas", s.nats)
	}
	return fmt.Sprintf("substrato: ficheiro %s — NÃO arbitra entre processos (DEF-282); posse SEQUENCIAL, uma instância de cada vez", s.wal)
}

// abrirParaLeitura abre o Event Store sem pedir posse. Ler nunca a pede, e nunca é
// bloqueado por quem a detém.
func (s substrato) abrirParaLeitura() (eventstore.EventStore, func() error, error) {
	if err := s.validar(); err != nil {
		return nil, nil, err
	}
	if s.replicado() {
		return s.abrirReplicado()
	}
	store, err := eventstore.Open(s.wal)
	if err != nil {
		return nil, nil, err
	}
	return store, store.Close, nil
}

// abrirParaEscrita abre o Event Store para escrever.
//
// # A revisão de AOS-285 que o AOS-100 obriga, feita aqui também
//
// Sobre FICHEIRO, a posse exclusiva vem ANTES do Open — abrir primeiro e trancar depois
// deixaria uma janela com dois escritores sobre o mesmo WAL, que é o estado a impedir.
//
// Sobre o substrato REPLICADO não há ficheiro a trancar, e trancar seria PIOR do que
// inútil: recusaria exactamente a configuração que o AOS-100 entrega. O vencedor de uma
// disputa passa a ser decidido pelo LEASE (código de saída 3, «posse negada»), não pelo
// guard do ficheiro (código 5) — e essa diferença de código é o que diz ao operador
// onde ir procurar.
func (s substrato) abrirParaEscrita() (eventstore.EventStore, func() error, error) {
	if err := s.validar(); err != nil {
		return nil, nil, err
	}
	if s.replicado() {
		return s.abrirReplicado()
	}

	largar, err := eventstore.LockWAL(s.wal)
	if err != nil {
		if errors.Is(err, eventstore.ErrWALHeld) {
			return nil, nil, fmt.Errorf("%w. Outro ESCRITOR (o nó `aos`, ou outro `aos-orq serve`) detém este Event Store. "+
				"Pare-o, aponte este a um WAL próprio, ou use --nats para o substrato REPLICADO, onde N escritores são suportados. "+
				"Razão: o Event Store de referência não arbitra entre processos — ver DEF-282", err)
		}
		return nil, nil, err
	}
	store, err := eventstore.Open(s.wal)
	if err != nil {
		// Largar a posse se o Open falhar: uma posse retida por um arranque abortado
		// recusaria a tentativa seguinte em nome de um detentor que já não existe.
		_ = largar()
		return nil, nil, err
	}
	// Ordem INVERSA da aquisição: fecha-se o store e só depois se larga a posse, para
	// não haver instante em que outro escritor a tome com o WAL ainda aberto por nós.
	return store, func() error {
		errFechar := store.Close()
		if errLargar := largar(); errFechar == nil {
			return errLargar
		}
		return errFechar
	}, nil
}

func (s substrato) abrirReplicado() (eventstore.EventStore, func() error, error) {
	opts := []jetstream.Option{}
	if s.stream != "" {
		opts = append(opts, jetstream.ComNomeDeStream(s.stream))
	}
	if s.regiao != "" {
		opts = append(opts, jetstream.ComRegiao(s.regiao))
	}
	if s.replicas > 0 {
		opts = append(opts, jetstream.ComReplicas(s.replicas))
	}
	store, err := jetstream.Abrir(s.nats, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("event store replicado (AOS-100) em %q: %w", s.nats, err)
	}
	return store, store.Close, nil
}
