package natsjs_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
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

// criarStreamR3 cria um stream de medição com a configuração que o AOS-100 exige, e
// devolve o seu nome e um subject virgem dentro dele.
func criarStreamR3(t *testing.T, cn *natsjs.Conn, prefixo string, janela time.Duration) (stream, subject string) {
	t.Helper()
	s := sufixo(t)
	stream, subject = prefixo+"_"+s, prefixo+"."+s+".run"
	if err := cn.CriarStream(natsjs.ConfigStream{
		Name:        stream,
		Subjects:    []string{prefixo + "." + s + ".>"},
		NumReplicas: 3,
		Storage:     "file",
		DenyDelete:  true,
		DenyPurge:   true,
		Duplicates:  int64(janela),
	}, prazo); err != nil {
		t.Fatalf("criar stream R3 %q: %v", stream, err)
	}
	return stream, strings.ToLower(subject)
}

// TestIntegracao_CASSobContencao fecha o buraco que a auditoria adversarial de
// 2026-08-31 abriu: TODAS as medições anteriores do CAS eram SEQUENCIAIS (A, depois B),
// e o próprio `conformance/doc.go` nomeia o modo de falha que só a contenção revela —
// «o CAS existe mas não é serializável sob contenção».
//
// N ligações INDEPENDENTES afirmam o mesmo expected_seq AO MESMO TEMPO. Exactamente uma
// pode ganhar. Se ganharem duas, o substrato não arbitra e todo o AOS-100 cai.
func TestIntegracao_CASSobContencao(t *testing.T) {
	addr := servidor(t)
	const n = 8

	abridor := ligar(t, addr)
	_, subject := criarStreamR3(t, abridor, "aosconc", 2*time.Minute)

	conns := make([]*natsjs.Conn, n)
	for i := range conns {
		conns[i] = ligar(t, addr)
	}

	// Barreira: todas as goroutines ficam bloqueadas até o canal fechar, para que as
	// escritas se cruzem de facto em vez de se sucederem.
	largada := make(chan struct{})
	var wg sync.WaitGroup
	acks := make([]natsjs.PubAck, n)
	errs := make([]error, n)
	for i := range conns {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-largada
			acks[i], errs[i] = conns[i].PublicarComCAS(subject, 0, nil,
				[]byte(`{"escritor":`+strconv.Itoa(i)+`}`), prazo)
		}(i)
	}
	close(largada)
	wg.Wait()

	vencedores, recusados, outros := 0, 0, 0
	var seqVencedor uint64
	for i := range conns {
		switch {
		case errs[i] == nil:
			vencedores++
			seqVencedor = acks[i].Seq
		case errors.Is(errs[i], natsjs.ErrSeqErrada):
			recusados++
			if acks[i].Seq != 0 {
				t.Errorf("escritor %d recusado mas com seq=%d, quer 0 — uma recusa não pode ocupar posição", i, acks[i].Seq)
			}
		default:
			outros++
			t.Logf("escritor %d com erro que não é recusa de CAS: %v", i, errs[i])
		}
	}

	if outros != 0 {
		t.Fatalf("%d de %d escritores falharam com erro que não é E_SEQ_ERRADA — "+
			"recusar tem de ser distinguível de avariar, senão a medição não distingue arbitragem de indisponibilidade", outros, n)
	}
	if vencedores != 1 || recusados != n-1 {
		t.Fatalf("vencedores=%d recusados=%d de %d escritores em contenção; quer exactamente 1 e %d — "+
			"o expected_seq NÃO é serializável sob contenção e o AOS-100 não está cumprido", vencedores, recusados, n, n-1)
	}
	t.Logf("%d escritores concorrentes sobre expected_seq=0: 1 vencedor (seq=%d), %d recusados com seq=0", n, seqVencedor, recusados)
}

// TestIntegracao_RecusaNaoDeixaRasto observa NO LOG o que a recusa fez — em vez de o
// inferir do `seq:0` da resposta, que foi o que a auditoria apontou como lacuna.
//
// O contrato C2 exige «ERRO ⇒ NADA FICOU DURÁVEL», e o revert em memória do GraphBuilder
// depende disso. Aqui lê-se a mensagem que ficou na posição disputada e confirma-se que
// é a do vencedor, não a do recusado.
func TestIntegracao_RecusaNaoDeixaRasto(t *testing.T) {
	addr := servidor(t)
	a, b := ligar(t, addr), ligar(t, addr)
	stream, subject := criarStreamR3(t, a, "aosrasto", 2*time.Minute)

	vencedor, err := a.PublicarComCAS(subject, 0, nil, []byte(`{"escritor":"vencedor"}`), prazo)
	if err != nil {
		t.Fatalf("A.PublicarComCAS: %v", err)
	}
	if _, err := b.PublicarComCAS(subject, 0, nil, []byte(`{"escritor":"recusado"}`), prazo); !errors.Is(err, natsjs.ErrSeqErrada) {
		t.Fatalf("B: quero recusa, veio %v", err)
	}

	// A posição disputada tem de conter o vencedor.
	_, dados, err := a.MensagemPorSeq(stream, vencedor.Seq, prazo)
	if err != nil {
		t.Fatalf("ler seq=%d: %v", vencedor.Seq, err)
	}
	if string(dados) != `{"escritor":"vencedor"}` {
		t.Fatalf("seq=%d contém %q — a posição disputada não é do vencedor", vencedor.Seq, dados)
	}

	// E o log não cresceu: o recusado não ficou noutra posição qualquer.
	info, err := a.InfoDoStream(stream, prazo)
	if err != nil {
		t.Fatalf("InfoDoStream: %v", err)
	}
	if info.Messages != 1 || info.LastSeq != vencedor.Seq {
		t.Fatalf("stream com messages=%d last_seq=%d; quer 1 e %d — a escrita recusada deixou rasto",
			info.Messages, info.LastSeq, vencedor.Seq)
	}
	t.Logf("posição disputada (seq=%d) contém o vencedor; stream com 1 mensagem — a recusa não deixou rasto", vencedor.Seq)
}

// TestIntegracao_DedupExpiraComAJanela mede o LIMITE, que é o que
// TestIntegracao_DedupDoServidorEUmaJanela nomeia mas não observa: a mesma chave, no
// mesmo stream, deduplica DENTRO da janela e volta a ser aceite DEPOIS dela.
//
// O controlo positivo (a 2.ª publicação, dentro da janela) é o que exclui a hipótese
// rival «a deduplicação nunca esteve activa neste stream» — sem ele, a 3.ª publicação a
// receber seq novo não prova nada sobre janelas.
//
// CONSEQUÊNCIA: a idempotência do AOS por (run_id, step_id) não tem prazo, logo NÃO pode
// assentar aqui. É a razão de o índice continuar derivado do log.
func TestIntegracao_DedupExpiraComAJanela(t *testing.T) {
	if testing.Short() {
		t.Skip("mede a expiração de uma janela: precisa de esperar por ela")
	}
	addr := servidor(t)
	cn := ligar(t, addr)

	const janela = 3 * time.Second
	_, subject := criarStreamR3(t, cn, "aosjanela", janela)
	chave := natsjs.Header{natsjs.HdrMsgID: "run:passo-1"}

	primeiro, err := cn.Publicar(subject, chave, []byte(`{"n":1}`), prazo)
	if err != nil {
		t.Fatalf("1.ª publicação: %v", err)
	}

	// CONTROLO POSITIVO: dentro da janela tem de deduplicar.
	dentro, err := cn.Publicar(subject, chave, []byte(`{"n":2}`), prazo)
	if err != nil {
		t.Fatalf("2.ª publicação (dentro da janela): %v", err)
	}
	if !dentro.Duplicate || dentro.Seq != primeiro.Seq {
		t.Fatalf("dentro da janela: duplicate=%v seq=%d, quer true e %d — "+
			"sem este controlo a experiência não distingue «a janela expirou» de «a dedup nunca esteve activa»",
			dentro.Duplicate, dentro.Seq, primeiro.Seq)
	}

	time.Sleep(janela + 2*time.Second)

	fora, err := cn.Publicar(subject, chave, []byte(`{"n":3}`), prazo)
	if err != nil {
		t.Fatalf("3.ª publicação (fora da janela): %v", err)
	}
	if fora.Duplicate || fora.Seq == primeiro.Seq {
		t.Fatalf("fora da janela: duplicate=%v seq=%d — a dedup do servidor NÃO é uma janela temporal. "+
			"Se isto mudou, a conclusão de que a idempotência do AOS não pode assentar aqui tem de ser reavaliada",
			fora.Duplicate, fora.Seq)
	}
	t.Logf("janela=%v: 1.ª seq=%d; dentro da janela duplicate no mesmo seq; %v depois a MESMA chave ficou committed em seq=%d",
		janela, primeiro.Seq, janela+2*time.Second, fora.Seq)
}
