package integration

// aos431_four_eyes_sobre_jetstream_test.go — A CERIMÓNIA FOUR-EYES SOBRE O SUBSTRATO REAL.
//
// # O CRITÉRIO QUE ISTO FECHA, E PORQUE ESTEVE DOIS TICKETS POR FECHAR
//
// O AOS-424 deixou por marcar: «pelo menos um teste da cerimónia four-eyes sobre JetStream».
// Não foi esquecimento — não havia NATS no CI, e é o AOS-431 que o põe lá.
//
// A razão de este teste ter de existir está medida, não suposta. O AOS-424 encontrou nove
// nomes de stream que o JetStream não consegue representar, e um deles era o desta cerimónia:
// `gov.approvals`. O ponto no nome do stream torna-se um separador de tokens no subject, e a
// consequência é a pior que há — sob `AOS_MODE=production` o four-eyes EXIGE substrato
// durável, pelo que a falha seria fail-closed **e invisível**: o operador nunca veria o que
// tem para aprovar.
//
// Esse defeito passou dez gates, uma revisão adversarial e o smoke. Passou porque TUDO corria
// sobre ficheiro, e o ficheiro aceita qualquer nome. Um teste sobre a nossa regra prova a
// nossa regra; só o servidor prova o servidor.
//
// # O QUE ESTE FICHEIRO ACRESCENTA AO `approval_store_durable_test.go`
//
// Aquele monta a store sobre `eventstore.New()` — o store de referência EM MEMÓRIA. O seu
// «sobrevive a restart» é uma instância nova sobre o MESMO objecto em memória: prova que a
// store relê o log, não que o log sobrevive a alguma coisa.
//
// Aqui o substrato é um cluster JetStream R3, e o restart é uma ligação NOVA, com o processo
// anterior fechado. É a diferença entre reler uma variável e reler um log replicado.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// envClusterFourEyes é o mesmo `AOS_NATS_URL` do resto da árvore. O gate `scripts/ci/nats.sh`
// define-o a partir de `scripts/ci/nats-cluster.sh`, que levanta quatro nós.
const envClusterFourEyes = "AOS_NATS_URL"

// clusterFourEyes devolve o PRIMEIRO endereço do cluster, ou salta.
//
// A variável nomeia o cluster (lista separada por vírgulas) porque os testes de reconexão
// precisam de alternativas; `jetstream.Abrir` sabe reparti-la, por isso aqui passa-se inteira.
func clusterFourEyes(t *testing.T) string {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv(envClusterFourEyes))
	if addr == "" {
		// SALTA, não finge. Um substrato falso mediria o substrato falso — que é exactamente
		// o defeito que este ficheiro existe para não repetir.
		t.Skipf("sem cluster: define %s (bash scripts/ci/nats-cluster.sh up)", envClusterFourEyes)
	}
	return addr
}

func sufixoDeStream(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("sufixo: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// TestAOS431_FourEyesSobreJetStream_UsoUnicoSobrevive é o teste que o AOS-424 pediu.
//
// Emite um grant, consome-o UMA vez, e prova que o segundo consumo é recusado — tudo sobre um
// cluster JetStream real, e com o segundo consumo a vir de uma LIGAÇÃO NOVA, para que o
// uso-único não possa vir de estado em memória do processo.
func TestAOS431_FourEyesSobreJetStream_UsoUnicoSobrevive(t *testing.T) {
	addr := clusterFourEyes(t)
	ctx := context.Background()

	// Um nome de stream por execução, para que duas corridas não disputem o mesmo log. O
	// prefixo de subject deriva do nome (ver `prefixoDe`), logo isto também isola os subjects.
	nome := "AOS431FOUREYES_" + sufixoDeStream(t)

	abrir := func() *jetstream.Store {
		t.Helper()
		st, err := jetstream.Abrir(addr, jetstream.ComNomeDeStream(nome))
		if err != nil {
			t.Fatalf("abrir JetStream %q: %v", nome, err)
		}
		return st
	}

	primeira := abrir()
	t.Cleanup(func() { _ = primeira.ApagarStream() })

	store, err := NewEventStoreApprovalStore(primeira)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore sobre JetStream: %v", err)
	}

	// (1) EMITIR. Se o nome do stream da cerimónia não fosse representável, é AQUI que
	// rebentava — e foi exactamente esta a falha que o AOS-424 corrigiu às cegas, sem poder
	// prová-la. Um `Put` que passa é a prova que faltava.
	const id = "g-aos431"
	if err := store.Put(ctx, sampleGrant(id)); err != nil {
		t.Fatalf("Put sobre JetStream: %v\n"+
			"se o erro falar de subject ou de stream_id, o nome da cerimónia voltou a não ser representável", err)
	}

	// (2) CONSUMIR UMA VEZ.
	if _, ok, err := store.Consume(ctx, id); err != nil || !ok {
		t.Fatalf("1.º consumo tinha de encontrar o grant; ok=%t err=%v", ok, err)
	}

	// (3) O SEGUNDO CONSUMO É RECUSADO — E DE UMA LIGAÇÃO NOVA.
	//
	// Fechar a primeira e abrir outra é o que torna esta asserção sobre o SUBSTRATO e não
	// sobre a store: se o uso-único vivesse num mutex ou num mapa do processo, um processo
	// novo voltaria a conceder. É a distinção que o teste in-memory não consegue fazer.
	_ = primeira.Close()

	segunda := abrir()
	t.Cleanup(func() { _ = segunda.Close() })
	storeDepois, err := NewEventStoreApprovalStore(segunda)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore sobre a 2.ª ligação: %v", err)
	}
	if _, ok, err := storeDepois.Consume(ctx, id); ok {
		t.Fatalf("2.º consumo, de uma LIGAÇÃO NOVA, encontrou o grant — o uso-único não sobreviveu ao substrato; err=%v", err)
	}
}

// TestAOS431_SubjectDeRecusaNomeLegadoContraNATSReal fecha o quarto critério do AOS-431.
//
// # PORQUE É QUE ISTO NÃO É REDUNDANTE COM O TESTE DE LÓGICA
//
// `TestSubjectDe_RecusaOQueNaoERepresentavel` já prova que a NOSSA regra recusa um ponto num
// `stream_id`. O que ele não pode provar é que a regra esteja CERTA — que o servidor também
// recusaria, ou pior, que o aceitaria e partisse o nome em tokens em silêncio.
//
// Este exercita o nome LEGADO real (`gov.approvals`, o que a cerimónia usava antes do
// AOS-424) contra um NATS a sério, e prova que a recusa acontece do NOSSO lado, antes da rede.
// Se um dia a regra for relaxada por engano, este teste mostra o que o servidor faz com o
// nome — que é a informação que faltava quando o defeito foi encontrado.
func TestAOS431_SubjectDeRecusaNomeLegadoContraNATSReal(t *testing.T) {
	addr := clusterFourEyes(t)
	ctx := context.Background()

	nome := "AOS431LEGADO_" + sufixoDeStream(t)
	st, err := jetstream.Abrir(addr, jetstream.ComNomeDeStream(nome))
	if err != nil {
		t.Fatalf("abrir JetStream %q: %v", nome, err)
	}
	t.Cleanup(func() { _ = st.ApagarStream() })

	// `gov.approvals` é o nome que a cerimónia four-eyes teve até ao AOS-424. Está aqui como
	// literal, e não importado da constante, de propósito: a constante mudou, e o que este
	// teste fixa é que a FORMA antiga continua recusada mesmo que alguém a volte a escrever.
	const legado = "gov.approvals"

	_, err = st.Append(ctx, legado, eventstore.EventInput{
		Type:    "aos431.sonda",
		Payload: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatalf("`%s` foi ACEITE pelo substrato real — o ponto é separador de tokens no subject,\n"+
			"e um nome assim volta a tornar a cerimónia four-eyes inoperante sob produção", legado)
	}
	// A recusa tem de ser a NOSSA, pela regra, e não um erro de rede: é o que prova que o
	// nome nunca chega ao servidor.
	if !strings.Contains(err.Error(), "stream_id") && !strings.Contains(err.Error(), "representável") {
		t.Errorf("a recusa de `%s` não nomeia a regra do stream_id: %v\n"+
			"se vier da rede, o nome CHEGOU ao servidor e a defesa está no sítio errado", legado, err)
	}
}
