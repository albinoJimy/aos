package conformance_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/conformance"
)

// referenciaWAL é o substrato de HOJE: n chamadas a [eventstore.Open] sobre o MESMO
// ficheiro WAL. É exactamente o que n processos do nó têm — cada um faz replay do WAL
// no arranque e, a partir daí, tem a SUA cabeça em memória.
// Cada chamada abre um WAL PRÓPRIO, como [conformance.Substrato] exige: sem isso, a
// sonda do CAS deixa dois registos no mesmo seq e o Open seguinte recusa o ficheiro
// inteiro com E_RESTORE_ORDER — ver [TestDefeito_DoisEscritoresTornamOWALInabrivel].
func referenciaWAL(t *testing.T) conformance.Substrato {
	t.Helper()
	dir := t.TempDir()
	var n0 int
	return func(n int) ([]eventstore.EventStore, func(), error) {
		n0++
		wal := filepath.Join(dir, fmt.Sprintf("referencia-%d.wal", n0))
		hs := make([]eventstore.EventStore, 0, n)
		largar := func() {
			for _, h := range hs {
				_ = h.Close()
			}
		}
		for i := 0; i < n; i++ {
			s, err := eventstore.Open(wal)
			if err != nil {
				largar()
				return nil, func() {}, err
			}
			hs = append(hs, s)
		}
		return hs, largar, nil
	}
}

// TestSensor_ReferenciaNaoArbitraEntreEscritores é um SENSOR, não um teste de sucesso:
// asserta que o Event Store de referência NÃO tem a propriedade, que é o estado
// registado no DEF-282 e declarado no ADR-023 §4.
//
// # Porque asserta a ausência
//
// A alternativa — correr [conformance.RunArbitragem] e vê-lo vermelho — deixaria a
// suite permanentemente vermelha, e uma suite que se espera vermelha deixa de ser lida.
// Assim o repo fica verde enquanto a ausência for verdade, e ACUSA no dia em que
// deixar de ser: qualquer sonda que passe a reportar «presente» faz este teste falhar.
//
// # O que fazer quando este teste falhar
//
// Falhar é a BOA notícia e é o objectivo do AOS-100. Nessa altura:
//
//  1. trocar este sensor por uma chamada a [conformance.RunArbitragem] contra o
//     substrato novo — a mesma medição, agora como gate;
//  2. converter TestLimite_EventStoreDeReferenciaNaoArbitraEntreProcessos
//     (packages/cmd/aos-orq) na asserção forte, como o próprio teste instrui;
//  3. retirar a declaração de ADR-023 §4 e fechar o DEF-282;
//  4. rever guardDePosseAplicavel/guardDoWORMAplicavel (packages/cmd/aos/wal_posse.go):
//     com substrato partilhado, recusar N réplicas é recusar o objectivo — mas o guard
//     continua certo para um deployment de ficheiro local, por isso REVER, não apagar;
//  5. actualizar tecnica/10 §3-bis, que declara o limite operacional.
func TestSensor_ReferenciaNaoArbitraEntreEscritores(t *testing.T) {
	relatorio := conformance.Sondar(referenciaWAL(t))
	if len(relatorio) == 0 {
		t.Fatal("Sondar não devolveu sondas — o instrumento de medição está vazio")
	}

	for _, r := range relatorio {
		if r.Erro != nil {
			t.Errorf("[%s] INCONCLUSIVA: %v — %s\n"+
				"Um sensor inconclusivo não mede nada: nem confirma a ausência de hoje nem acusa a sua "+
				"chegada. Arranjar a sonda antes de tirar conclusões do substrato.", r.Sonda, r.Erro, r.Detalhe)
			continue
		}
		if r.Tem {
			t.Errorf("[%s] O SUBSTRATO GANHOU A PROPRIEDADE — %s\n"+
				"Isto é uma BOA notícia e este teste é o sensor dela. Ver o que fazer no doc deste teste: "+
				"converter em gate (RunArbitragem), converter o sensor de aos-orq, fechar o DEF-282, rever "+
				"os guardas de posse e actualizar tecnica/10 §3-bis.", r.Sonda, r.Detalhe)
			continue
		}
		t.Logf("[%s] ausente (esperado hoje) — %s", r.Sonda, r.Detalhe)
	}
}

// TestDefeito_DoisEscritoresTornamOWALInabrivel regista o que o instrumento MEDIU a
// 2026-08-31, na sua primeira execução, e que não estava registado em lado nenhum:
//
// a consequência da propriedade em falta não é apenas «ambos ganham» — é que o log
// fica INABRÍVEL. Dois handles independentes commitam ambos seq=1 no mesmo stream, o
// WAL passa a ter dois registos na mesma posição, e o `Open` SEGUINTE recusa o ficheiro
// inteiro com E_RESTORE_ORDER. O nó não arranca.
//
// É o mesmo desfecho que guardDoWORMAplicavel (packages/cmd/aos/wal_posse.go) mediu
// para a hash-chain do WORM — «o arranque seguinte RECUSA a cadeia» —, agora medido
// para o WAL do Event Store. Reforça, sem o substituir, o argumento do guard de
// arranque de AOS-285/286: o custo de dois escritores não é degradação, é indisponibilidade.
//
// Este teste NÃO é o sensor da propriedade (esse é
// [TestSensor_ReferenciaNaoArbitraEntreEscritores]). É o registo executável do custo,
// e deixa de fazer sentido no dia em que o substrato arbitrar — nessa altura o segundo
// escritor vê E_SEQ_CONFLICT e nunca chega a escrever o segundo seq=1.
func TestDefeito_DoisEscritoresTornamOWALInabrivel(t *testing.T) {
	ctx := context.Background()
	wal := filepath.Join(t.TempDir(), "fork.wal")
	const stream = "run-fork"

	facto := eventstore.EventInput{Type: "conformance.fork", Payload: json.RawMessage(`{}`)}

	a, err := eventstore.Open(wal)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	b, err := eventstore.Open(wal)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}

	ra, err := a.Append(ctx, stream, facto, eventstore.WithExpectedSeq(0))
	if err != nil {
		t.Fatalf("A.Append: %v", err)
	}
	rb, err := b.Append(ctx, stream, facto, eventstore.WithExpectedSeq(0))
	if err != nil {
		// Se um dia isto passar a ser E_SEQ_CONFLICT, o substrato ganhou a propriedade
		// e este teste deixou de descrever a realidade — o sensor acima dirá o que fazer.
		t.Fatalf("B.Append: %v — o substrato recusou o segundo escritor, logo já arbitra: "+
			"ver TestSensor_ReferenciaNaoArbitraEntreEscritores para o que fazer a seguir", err)
	}
	if ra.Seq != rb.Seq {
		t.Fatalf("A e B ficaram em seqs diferentes (%d e %d) — o fork não se deu como medido", ra.Seq, rb.Seq)
	}
	_ = a.Close()
	_ = b.Close()

	// O CUSTO: o log com o fork já não abre.
	if _, err := eventstore.Open(wal); !errors.Is(err, eventstore.ErrRestoreOrder) {
		t.Fatalf("reabrir o WAL depois do fork: quero E_RESTORE_ORDER, veio %v — "+
			"se o WAL passou a abrir, o custo medido mudou e este registo tem de ser reescrito", err)
	}
	t.Logf("dois escritores commitaram ambos seq=%d; o WAL deixou de abrir (E_RESTORE_ORDER)", ra.Seq)
}

// TestNaoVacuidade_UmSubstratoQueArbitraEDetectado prova que o sensor acima NÃO é
// decorativo. Um sensor que asserta ausência passa trivialmente se as sondas nunca
// souberem reconhecer a presença — seria verde hoje, verde depois do AOS-100, e não
// diria nada em nenhum dos dois dias.
//
// A mutação é a mais forte disponível sem o backend novo: um substrato que arbitra
// PERFEITAMENTE, porque os n handles são o MESMO store in-memory. Não simula um
// backend replicado — não é para isso que serve. Serve para responder à única pergunta
// que valida o instrumento: as sondas conseguem dizer «presente»?
//
// Se este teste falhar, [TestSensor_ReferenciaNaoArbitraEntreEscritores] deixou de ser
// prova de coisa nenhuma, independentemente de estar verde.
func TestNaoVacuidade_UmSubstratoQueArbitraEDetectado(t *testing.T) {
	arbitro := func(n int) ([]eventstore.EventStore, func(), error) {
		s, err := eventstore.New()
		if err != nil {
			return nil, func() {}, err
		}
		hs := make([]eventstore.EventStore, n)
		for i := range hs {
			hs[i] = s // o MESMO store: a arbitragem é total por construção
		}
		return hs, func() { _ = s.Close() }, nil
	}

	for _, r := range conformance.Sondar(arbitro) {
		switch {
		case r.Erro != nil:
			t.Errorf("[%s] INCONCLUSIVA contra um substrato que arbitra: %v — %s", r.Sonda, r.Erro, r.Detalhe)
		case !r.Tem:
			t.Errorf("[%s] reportou AUSENTE contra um substrato que arbitra por construção — %s\n"+
				"A sonda não sabe reconhecer a propriedade, logo o sensor de ausência é vácuo.", r.Sonda, r.Detalhe)
		default:
			t.Logf("[%s] presente, como tem de ser — %s", r.Sonda, r.Detalhe)
		}
	}
}
