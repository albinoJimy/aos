package main

// AOS-352 — `Streams()` NÃO PODIA DEVOLVER ERRO, E A PERNA DO LEGAL HOLD DEGRADAVA
// FAIL-OPEN.
//
// `jetstream/store.go` respondia a `Streams()` perguntando ao servidor e, em erro,
// `return nil` — sem log, sem sinal. Uma falha transitória de rede ficava indistinguível de
// «não há streams». E o defeito não era local: a PORTA declarava `Streams() []string`, sem
// canal de erro, e as duas implementações seguiam-na.
//
// Quatro varredores de arranque consomem-na, e degradavam em direcções diferentes:
//
//	governance_restore.go   FAIL-OPEN     o índice titular→partição do legal hold volta
//	                                      VAZIO; um hold que devia cobrir partições
//	                                      noutros streams deixa de cobrir, e o
//	                                      ExpirationJob pode crypto-shred material sob hold
//	crash_resume.go         degradação    zero runs órfãos retomados
//	retention.go            fail-closed   nada expira — direcção segura, por acidente
//	wal_summary.go          diagnóstico   sumário incompleto com cara de completo
//
// Porque sobreviveu: `eventstore_port.go` discute explicitamente a semântica ENTRE
// PROCESSOS de `Streams()` — «um índice em memória responde à pergunta errada» — e nunca
// menciona sinalização de erro. A porta foi pensada, e este eixo escapou.

import (
	"context"
	"errors"
	"testing"

	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// errEnumeracao é a falha injectada: o substrato não conseguiu responder à pergunta.
var errEnumeracao = errors.New("sonda: o servidor nao respondeu a enumeracao (rede)")

// storeQueNaoEnumera responde a tudo como o store real, MENOS a `Streams()`. É a forma
// exacta do defeito: nada mais avariou — só a pergunta não chegou a ser respondida.
type storeQueNaoEnumera struct {
	EventStorePort
}

func (s *storeQueNaoEnumera) Streams() ([]string, error) { return nil, errEnumeracao }

// TestAOS352_RestauroDeGovernacaoNaoConcluiComIndiceVazio é o teste que nasceu VERMELHO:
// antes, `restoreSubjectIndex` devolvia `(0, nil)` — sucesso, com o índice vazio.
func TestAOS352_RestauroDeGovernacaoNaoConcluiComIndiceVazio(t *testing.T) {
	base, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer base.Close()

	index := audit.NewInMemorySubjectPartitionIndex()
	n, err := restoreSubjectIndex(context.Background(), &storeQueNaoEnumera{EventStorePort: base}, index)

	if err == nil {
		t.Fatalf("restoreSubjectIndex CONCLUIU com %d ligações sobre uma enumeração FALHADA — "+
			"o índice do legal hold fica VAZIO e o ExpirationJob pode crypto-shred material sob hold", n)
	}
	if !errors.Is(err, errEnumeracao) {
		t.Fatalf("o erro não nomeia a causa: %v", err)
	}
	if n != 0 {
		t.Fatalf("ligações = %d sobre uma enumeração falhada, quero 0", n)
	}
}

// TestAOS352_RestauroDeGovernacaoDistingueVazioDeFalhado é a metade que dá sentido à
// outra. Um substrato que responde «não há streams» é uma resposta LEGÍTIMA, e o arranque
// tem de a aceitar — senão a correcção trocaria um fail-open por um nó que não sobe num
// primeiro arranque.
func TestAOS352_RestauroDeGovernacaoDistingueVazioDeFalhado(t *testing.T) {
	base, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer base.Close()

	index := audit.NewInMemorySubjectPartitionIndex()
	n, err := restoreSubjectIndex(context.Background(), base, index)
	if err != nil {
		t.Fatalf("um Event Store VAZIO é uma resposta legítima e não pode abortar o arranque: %v", err)
	}
	if n != 0 {
		t.Fatalf("ligações = %d num store vazio, quero 0", n)
	}
}

// TestAOS352_RetencaoNaoExpiraSobreVarreduraIncompleta fixa o segundo consumidor. A
// direcção já era segura POR ACIDENTE — com `nil`, a lista vinha vazia e nada expirava. O
// acidente passa a decisão explícita, e a diferença mede-se: agora devolve ERRO, e o
// `ExpirationJob` não corre sobre o que não viu em vez de correr sobre uma lista vazia.
func TestAOS352_RetencaoNaoExpiraSobreVarreduraIncompleta(t *testing.T) {
	base, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer base.Close()

	src := eventStoreRecordSource{es: &storeQueNaoEnumera{EventStorePort: base}}
	recs, err := src.List(context.Background())
	if err == nil {
		t.Fatalf("List devolveu %d registo(s) e err=nil sobre uma enumeração FALHADA — "+
			"uma lista vazia é indistinguível de «não há nada a expirar»", len(recs))
	}
	if !errors.Is(err, errEnumeracao) {
		t.Fatalf("o erro não nomeia a causa: %v", err)
	}
	if recs != nil {
		t.Fatalf("registos = %v sobre uma enumeração falhada, quero nil", recs)
	}
}

// TestAOS352_StoreDeReferenciaDistingueFechadoDeVazio fecha o ciclo no substrato. Um store
// FECHADO devolvia uma lista vazia, que se lê como «não há streams» — a mesma ambiguidade,
// do outro lado da porta.
func TestAOS352_StoreDeReferenciaDistingueFechadoDeVazio(t *testing.T) {
	s, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	vazio, err := s.Streams()
	if err != nil {
		t.Fatalf("um store vazio e aberto tem de responder sem erro: %v", err)
	}
	if len(vazio) != 0 {
		t.Fatalf("store vazio devolveu %v", vazio)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := s.Streams()
	if !errors.Is(err, eventstore.ErrClosed) {
		t.Fatalf("Streams() de um store FECHADO = (%v, %v), quero ErrClosed", got, err)
	}
}
