package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aos-ref/platform/backup"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// backup_replicado_test.go — o backup imutável e o PITR a correr sobre o Event Store
// REPLICADO (AOS-101 sobre AOS-100), de ponta a ponta.
//
// # Porque este teste vive no ápice
//
// `platform/backup` não conhece o adaptador JetStream, e não deve: fala com as portas
// [eventstore.BackupSource]/[eventstore.RestoreSink] e é isso que o mantém indiferente ao
// substrato. `substrate/eventstore/jetstream` não pode importar `platform/` — inverteria a
// camada, e o layer-lint recusa. O único sítio que pode ver os dois é o ápice de composição.
//
// # O que este teste prova que uma asserção de tipo NÃO prova
//
// As garantias `var _ eventstore.BackupSource = (*jetstream.Store)(nil)` provam que o
// adaptador COMPILA contra as portas. Não provam que o exportador e o restaurador
// funcionam por cima dele — que o manifesto hash-chain fecha, que o checkpoint assinado
// verifica, que o restauro reconstrói. É a diferença entre «encaixa» e «serve», e antes
// deste ticket nem a primeira existia: com o Event Store replicado, o módulo de backup não
// se conseguia sequer construir.

const envServidorAp = "AOS_NATS_URL"

func clusterOuSalta(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(envServidorAp)
	if addr == "" {
		t.Skipf("sem cluster: define %s (túnel SSH para um nó do cluster)", envServidorAp)
	}
	return addr
}

func marcaAleatoria(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("marca: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// A região do cluster de medição, declarada nas `server_tags` dos nós. É a MESMA que os
// testes de soberania do AOS-100 usam; um valor diferente faria a colocação falhar na
// abertura, que é precisamente o comportamento pretendido.
const regiaoDoCluster = "eu-west"

func abrirReplicado(t *testing.T, addr, marca string) *jetstream.Store {
	t.Helper()
	s := marcaAleatoria(t)
	st, err := jetstream.Abrir(addr,
		jetstream.ComNomeDeStream("AOSAPX_"+marca+"_"+s),
		jetstream.ComPrefixoDeSubject("aosapx."+marca+"."+s),
		jetstream.ComPrazo(20*time.Second),
		jetstream.ComReplicas(3),
		jetstream.ComBoardDeSoberania("board-ue", regiaoDoCluster),
	)
	if err != nil {
		t.Fatalf("abrir replicado (%s): %v", marca, err)
	}
	t.Cleanup(func() {
		_ = st.ApagarStream()
		_ = st.Close()
	})
	return st
}

func assinador(t *testing.T) *backup.Ed25519Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave: %v", err)
	}
	s, err := backup.NewEd25519Signer(priv)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	return s
}

// TestAOS101_ExportarERestaurarPorCimaDoSubstratoREPLICADO.
//
// O caminho inteiro: exporta de um Event Store replicado para segmentos imutáveis cifrados,
// verifica o manifesto hash-chain e o checkpoint assinado, e restaura para OUTRO stream
// replicado — comparando o envelope no fim, que é o que distingue um restauro de uma
// segunda escrita.
func TestAOS101_ExportarERestaurarPorCimaDoSubstratoREPLICADO(t *testing.T) {
	addr := clusterOuSalta(t)
	ctx := context.Background()
	origem := abrirReplicado(t, addr, "exp")
	destino := abrirReplicado(t, addr, "res")

	const stream = "run-apice"
	const n = 9
	for i := 1; i <= n; i++ {
		if _, err := origem.Append(ctx, stream, eventstore.EventInput{
			Type:    "aos.teste.apice.v1",
			Payload: []byte(`{"i":` + string(rune('0'+i)) + `}`),
			RunID:   stream,
			StepID:  "passo-" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	originais, err := origem.Read(ctx, stream, 1)
	if err != nil {
		t.Fatalf("ler a origem: %v", err)
	}

	// (1) EXPORTAR. O destino imutável declara a MESMA região do board — um destino
	// noutra região é recusado, e isso é afirmado no teste a seguir.
	imutavel := backup.NewInMemoryImmutableStore(regiaoDoCluster)
	exp, err := backup.NewExporter(origem, imutavel, assinador(t))
	if err != nil {
		t.Fatalf("NewExporter sobre o substrato replicado: %v", err)
	}
	res, err := exp.Export(ctx)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Events != uint64(n) {
		t.Fatalf("exportou %d eventos, quer %d", res.Events, n)
	}

	// (2) VERIFICAR a cadeia antes de restaurar. O `res.Cycle` é o ciclo esperado: passá-lo
	// é o que faz o verificador recusar um checkpoint ANTERIOR (rollback), e não apenas um
	// adulterado. — um restauro que não verifica é uma
	// suposição com passos extra.
	restaurador, err := backup.NewRestorer(imutavel, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	manifesto, checkpoint := exp.Manifest(), exp.Checkpoint()
	if err := restaurador.VerifyManifest(manifesto, checkpoint, res.Cycle); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}

	// (3) RESTAURAR para o outro stream replicado.
	evidencia, err := restaurador.RestoreTo(ctx, manifesto, checkpoint, res.Cycle, nil, destino)
	if err != nil {
		t.Fatalf("RestoreTo sobre o substrato replicado: %v", err)
	}
	if evidencia.EventsRestored != uint64(n) {
		t.Fatalf("restaurou %d eventos, quer %d", evidencia.EventsRestored, n)
	}

	// (4) O ENVELOPE. Se isto passasse com EventIDs novos, o «restauro» seria uma
	// reescrita — e a contagem do passo (3) teria batido certo na mesma.
	relidos, err := destino.Read(ctx, stream, 1)
	if err != nil {
		t.Fatalf("ler o destino: %v", err)
	}
	if len(relidos) != len(originais) {
		t.Fatalf("destino com %d eventos, origem com %d", len(relidos), len(originais))
	}
	for i := range originais {
		if relidos[i].EventID != originais[i].EventID || relidos[i].Ts != originais[i].Ts || relidos[i].Seq != originais[i].Seq {
			t.Fatalf("seq %d: o envelope não sobreviveu à travessia (%q/%q vs %q/%q)",
				originais[i].Seq, relidos[i].EventID, relidos[i].Ts, originais[i].EventID, originais[i].Ts)
		}
	}
}

// TestAOS101_UmDestinoDeBackupNOUTRARegiaoERecusadoSobreOReplicado.
//
// A soberania (ADR-011) tem de valer também quando a origem é o cluster: o board declara a
// fronteira e o destino do backup não a pode cruzar. A propriedade só é verificável porque
// o adaptador passou a expor `Region()` — a porta que o exportador interroga.
func TestAOS101_UmDestinoDeBackupNOUTRARegiaoERecusadoSobreOReplicado(t *testing.T) {
	addr := clusterOuSalta(t)
	origem := abrirReplicado(t, addr, "sob")

	if origem.Region() != regiaoDoCluster {
		t.Fatalf("Region() = %q, quer %q — sem isto o teste a seguir seria vacuoso", origem.Region(), regiaoDoCluster)
	}

	// NOTA para quem vier a seguir: a sentinela é a do `backup`, e NÃO a
	// `eventstore.ErrSovereigntyViolation` — existem duas, com o mesmo nome, para a mesma
	// ideia (uma para réplicas fora da fronteira, outra para destinos de backup fora dela).
	// Escrever a errada aqui compila e o teste falha a dizer que a mensagem certa é a
	// errada, que foi exactamente o que aconteceu ao escrevê-lo.
	fora := backup.NewInMemoryImmutableStore("us-east")
	_, err := backup.NewExporter(origem, fora, assinador(t))
	if !errors.Is(err, backup.ErrSovereigntyViolation) {
		t.Fatalf("exportador para outra região = %v, quer ErrSovereigntyViolation", err)
	}

	// E um destino SEM região declarada também é recusado: não provar que respeita a
	// fronteira é diferente de a respeitar, e a diferença resolve-se negando.
	semRegiao := backup.NewInMemoryImmutableStore("")
	if _, err := backup.NewExporter(origem, semRegiao, assinador(t)); !errors.Is(err, backup.ErrSovereigntyViolation) {
		t.Fatalf("exportador para destino sem região = %v, quer ErrSovereigntyViolation", err)
	}
}
