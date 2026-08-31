package natsjs_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// A medição precisa de um cluster REAL. Sem ele o teste é saltado em vez de fingir:
// um mock do JetStream mediria o mock, e é precisamente esse o erro que o handoff do
// AOS-100 manda não cometer («não assumas que o backend TEM a propriedade porque a
// documentação o diz»).
//
//	AOS_NATS_URL=127.0.0.1:14222 go test ./natsjs/
const envServidor = "AOS_NATS_URL"

const prazo = 10 * time.Second

func servidor(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(envServidor)
	if addr == "" {
		t.Skipf("sem cluster: define %s (ex.: túnel SSH para o nó 0 do cluster)", envServidor)
	}
	return addr
}

func ligar(t *testing.T, addr string) *natsjs.Conn {
	t.Helper()
	cn, err := natsjs.Ligar(addr, prazo)
	if err != nil {
		t.Fatalf("ligar a %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = cn.Close() })
	return cn
}

func sufixo(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("sufixo: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// TestIntegracao_CASArbitraEntreDuasLigacoes é a medição do AC1 do AOS-100 a partir do
// código do AOS — e não do CLI, como na primeira medição.
//
// DUAS ligações independentes são o que dois PROCESSOS são um para o outro. É a mesma
// forma de [doisOpensReclamamOMesmoToken] (packages/cmd/aos-orq), agora contra o
// substrato novo: sequencial e determinista, porque um sensor intermitente é pior do
// que nenhum.
func TestIntegracao_CASArbitraEntreDuasLigacoes(t *testing.T) {
	addr := servidor(t)
	a, b := ligar(t, addr), ligar(t, addr)

	s := sufixo(t)
	stream, subject := "AOSCAS_"+s, "aoscas."+s+".run"
	if err := a.CriarStream(natsjs.ConfigStream{
		Name:        stream,
		Subjects:    []string{"aoscas." + s + ".>"},
		NumReplicas: 3,
		Storage:     "file",
		DenyDelete:  true,
		DenyPurge:   true,
		Duplicates:  int64(2 * time.Minute),
	}, prazo); err != nil {
		t.Fatalf("criar stream R3: %v", err)
	}

	primeiro, err := a.PublicarComCAS(subject, 0, nil, []byte(`{"escritor":"a"}`), prazo)
	if err != nil {
		t.Fatalf("A.PublicarComCAS(expected=0): %v", err)
	}
	if primeiro.Seq == 0 {
		t.Fatalf("A ficou com seq=0 e sem erro — um committed tem de ter seq")
	}

	segundo, recusa := b.PublicarComCAS(subject, 0, nil, []byte(`{"escritor":"b"}`), prazo)
	if !errors.Is(recusa, natsjs.ErrSeqErrada) {
		t.Fatalf("B afirmou o MESMO expected_seq=0 e o substrato aceitou (seq=%d, err=%v) — "+
			"não há arbitragem entre escritores e o AOS-100 não está cumprido", segundo.Seq, recusa)
	}
	// A recusa tem de deixar o log intacto: é a garantia «ERRO ⇒ NADA FICOU DURÁVEL»
	// de que o revert em memória do GraphBuilder depende.
	if segundo.Seq != 0 {
		t.Errorf("a recusa trouxe seq=%d, quer 0 — uma escrita recusada não pode ocupar posição", segundo.Seq)
	}

	// E o escritor que RELÊ e reavalia passa: a recusa é de conflito, não de avaria.
	terceiro, err := b.PublicarComCAS(subject, primeiro.Seq, nil, []byte(`{"escritor":"b2"}`), prazo)
	if err != nil {
		t.Fatalf("B.PublicarComCAS(expected=%d) depois de reler: %v", primeiro.Seq, err)
	}
	t.Logf("A=%d; B recusado com seq=%d (%v); B após reler=%d", primeiro.Seq, segundo.Seq, recusa, terceiro.Seq)
}

// TestIntegracao_DedupDoServidorEUmaJanela fixa em teste o limite MEDIDO a 2026-08-31,
// para que ninguém volte a assumir que a Nats-Msg-Id resolve a idempotência do AOS.
//
// Dentro da janela o servidor devolve duplicate com o seq ORIGINAL — que é o
// StatusDuplicate do contrato C2. Com a janela vencida, a MESMA chave ficaria committed
// de novo. É por isso que a idempotência por (run_id, step_id) continua derivada do
// log, com este cabeçalho apenas como rede de segurança para retries imediatos.
func TestIntegracao_DedupDoServidorEUmaJanela(t *testing.T) {
	addr := servidor(t)
	a, b := ligar(t, addr), ligar(t, addr)

	s := sufixo(t)
	stream, subject := "AOSDUP_"+s, "aosdup."+s+".run"
	if err := a.CriarStream(natsjs.ConfigStream{
		Name:        stream,
		Subjects:    []string{"aosdup." + s + ".>"},
		NumReplicas: 3,
		Storage:     "file",
		DenyDelete:  true,
		DenyPurge:   true,
		Duplicates:  int64(2 * time.Minute),
	}, prazo); err != nil {
		t.Fatalf("criar stream R3: %v", err)
	}

	chave := natsjs.Header{natsjs.HdrMsgID: "run-" + s + ":passo-1"}
	primeiro, err := a.Publicar(subject, chave, []byte(`{"n":1}`), prazo)
	if err != nil {
		t.Fatalf("A.Publicar: %v", err)
	}
	segundo, err := b.Publicar(subject, chave, []byte(`{"n":2}`), prazo)
	if err != nil {
		t.Fatalf("B.Publicar (mesma chave): %v", err)
	}

	if !segundo.Duplicate || segundo.Seq != primeiro.Seq {
		t.Fatalf("B: duplicate=%v seq=%d; queria duplicate=true e seq=%d (o ORIGINAL) — "+
			"se isto mudou, a leitura do PubAck está errada", segundo.Duplicate, segundo.Seq, primeiro.Seq)
	}
	t.Logf("dentro da janela: A=%d, B=duplicate no mesmo seq=%d — mas a janela expira, "+
		"e é por isso que a idempotência do AOS não pode assentar aqui", primeiro.Seq, segundo.Seq)
}
