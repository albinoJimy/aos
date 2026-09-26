package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-101 — a retoma FALHA FECHADA sobre um estado que não prova ser deste exportador e deste log.
//
// O retoma_test.go mede os modos de falha do desenho (ciclo interrompido, dois donos, cursor
// adulterado, sufixo). Este ficheiro mede o critério do ticket tal como foi pedido: um registo de
// ciclo CORROMPIDO, de OUTRA região, com OUTRA KEK, com um BURACO, ou sobre um destino que não é
// write-once condicional não é adoptado — o exportador não chega a existir —; uma cadeia de OUTRO log
// ou sobre um log REBOBINADO não é continuada — o ciclo não escreve —; e um destino que não sabe
// responder não é lido como virgem.

// retomaDeUmaCadeia sela `ciclos` ciclos num destino novo e devolve-o, com o signer, a custódia da
// KEK e a fonte.
func retomaDeUmaCadeia(t *testing.T, ciclos int) (*eventstore.Store, *InMemoryImmutableStore, *Ed25519Signer, audit.KeyVault) {
	t.Helper()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	vault := audit.NewInMemoryKeyVault(nil)
	exp, err := NewExporter(src, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	for i := 0; i < ciclos; i++ {
		seed(t, src, "run-a", 2, "m")
		if _, err := exp.Export(context.Background()); err != nil {
			t.Fatalf("ciclo %d: %v", i+1, err)
		}
	}
	return src, dst, signer, vault
}

// restaurarCadeia reconstrói a cadeia do DESTINO (LoadManifest), exige `ciclos` elos, verifica-a e
// RESTAURA-A para um Event Store novo. Não basta verificar: uma cadeia cuja KEK se perdeu verifica e
// não restaura.
func restaurarCadeia(t *testing.T, dst ImmutableStore, vault audit.KeyVault, signer Signer, ciclos int) RestoreEvidence {
	t.Helper()
	rst, err := NewRestorer(dst, vault, signer.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	m, cp, err := rst.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Segments) != ciclos || cp.Cycle != uint64(ciclos) {
		t.Fatalf("a cadeia devia ter %d ciclos; got %d segmentos, cp.Cycle=%d", ciclos, len(m.Segments), cp.Cycle)
	}
	if err := rst.VerifyManifest(m, cp, 0); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	ev, err := rst.RestoreTo(context.Background(), m, cp, 0, nil, freshDest(t, "board-eu", "eu-west"))
	if err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	if !ev.Verified {
		t.Fatalf("restauro sem veredicto verificado: %+v", ev)
	}
	return ev
}

// UM REGISTO DE CICLO ILEGÍVEL NÃO É LIDO COMO DESTINO VIRGEM.
//
// A tentação é tratar «não consegui ler o estado» como «não há estado» e recomeçar do génesis —
// que é exactamente o defeito que a retoma fechou, reaberto por um byte. Um registo corrompido é
// [ErrResumeUnverifiable] e o exportador não é construído.
func TestAOS101_RegistoDeCicloCORROMPIDOeRecusadoNoArranque(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	if err := dst.Put(cycleRef("eu-west", 1), []byte(`{"entry":{"index":1,`), t0.Add(time.Hour)); err != nil {
		t.Fatalf("Put do registo corrompido: %v", err)
	}

	exp, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if !errors.Is(err, ErrResumeUnverifiable) {
		t.Fatalf("um registo de ciclo ilegivel devia recusar o arranque com ErrResumeUnverifiable; got exp=%v err=%v", exp, err)
	}
	if exp != nil {
		t.Fatal("o exportador NAO pode existir depois de uma retoma recusada")
	}
}

// UM REGISTO AUTÊNTICO DE OUTRA REGIÃO NÃO É ADOPTADO (ADR-011).
//
// A mesma chave assina as cadeias de todas as regiões de um operador, pelo que a assinatura valida.
// É a região selada no checkpoint que o recusa: adoptar o cursor de outra fronteira misturaria duas
// cadeias de soberania numa só.
func TestAOS101_RegistoDeOUTRARegiaoeRecusadoNoArranque(t *testing.T) {
	ctx := context.Background()
	signer := newSigner(t)

	srcCentral := newSourceStore(t, "board-eu", "eu-central")
	seed(t, srcCentral, "run-a", 2, "m")
	central := NewInMemoryImmutableStore("eu-central")
	expC, err := NewExporter(srcCentral, central, signer, WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter eu-central: %v", err)
	}
	if _, err := expC.Export(ctx); err != nil {
		t.Fatalf("ciclo 1 eu-central: %v", err)
	}
	blob, err := central.Get(cycleRef("eu-central", 1))
	if err != nil {
		t.Fatalf("Get do registo eu-central: %v", err)
	}

	// O registo AUTÊNTICO de eu-central, posto onde o exportador de eu-west o vai procurar.
	west := NewInMemoryImmutableStore("eu-west")
	if err := west.Put(cycleRef("eu-west", 1), blob, t0.Add(time.Hour)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	srcWest := newSourceStore(t, "board-eu", "eu-west")
	seed(t, srcWest, "run-a", 2, "m")

	_, err = NewExporter(srcWest, west, signer, WithRandSource(detRand()))
	if !errors.Is(err, ErrResumeUnverifiable) {
		t.Fatalf("um registo de outra regiao devia recusar o arranque com ErrResumeUnverifiable; got %v", err)
	}
	if !strings.Contains(err.Error(), "regiao") {
		t.Errorf("o erro tem de nomear a REGIAO como causa; got %v", err)
	}
}

// UMA CADEIA AUTÊNTICA DE OUTRO LOG NÃO É CONTINUADA — E O ARRANQUE NÃO É RECUSADO POR ISSO.
//
// Mesma chave, mesma região, destino partilhado por engano: a assinatura, o elo e a região
// verificam todos, e nenhum deles diz se a cadeia é DESTE Event Store. Continuá-la coseria no mesmo
// backup os eventos de outro log. É a fonte que o denuncia — o cursor (run-a) está à frente dela —,
// e denuncia-o no CICLO, sem escrever: no arranque a mesma recusa impediria um nó de subir depois de
// um PITR ou de um DR, que é quando o log fica, legitimamente, atrás do cursor.
func TestAOS101_CadeiaDeOUTROLogNaoEContinuada(t *testing.T) {
	_, dst, signer, vault := retomaDeUmaCadeia(t, 1)

	outroLog := newSourceStore(t, "board-eu", "eu-west")
	seed(t, outroLog, "run-x", 5, "m")

	exp, err := NewExporter(outroLog, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("o arranque NAO pode ser recusado por o log estar atras do cursor: %v", err)
	}
	antes := dst.Len()
	_, err = exp.Export(context.Background())
	if !errors.Is(err, ErrSourceBehindBackup) {
		t.Fatalf("o ciclo sobre uma cadeia de outro log devia falhar com ErrSourceBehindBackup; got %v", err)
	}
	if !strings.Contains(err.Error(), `"run-a"`) {
		t.Errorf("o erro tem de nomear o stream que a fonte nao cobre; got %v", err)
	}
	if dst.Len() != antes {
		t.Fatalf("o ciclo recusado NAO pode escrever no destino; objectos %d -> %d", antes, dst.Len())
	}
}

// UM LOG REBOBINADO (PITR para trás) NÃO É CONTINUADO SOBRE A CADEIA ANTIGA.
//
// O mesmo log, restaurado para um ponto ANTERIOR ao cursor. Continuar saltaria o stream até o head
// voltar a passar o cursor — e aí exportaria uma história DIFERENTE por cima da que o backup tem,
// numa cadeia que verificaria. O ciclo recusa sem escrever; o arranque sobe. Um destino novo para a
// cadeia nova é decisão de operação.
func TestAOS101_LogREBOBINADONaoEContinuado(t *testing.T) {
	_, dst, signer, vault := retomaDeUmaCadeia(t, 2) // cursor run-a = 4

	rebobinado := newSourceStore(t, "board-eu", "eu-west")
	seed(t, rebobinado, "run-a", 3, "m") // head 3 < cursor 4

	expR, err := NewExporter(rebobinado, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("o arranque sobre um log rebobinado tem de SUBIR (a recusa e do ciclo): %v", err)
	}
	if _, err := expR.Export(context.Background()); !errors.Is(err, ErrSourceBehindBackup) {
		t.Fatalf("o ciclo sobre um log rebobinado para tras do cursor devia falhar com ErrSourceBehindBackup; got %v", err)
	}

	// CONTROLO: a mesma cadeia com a fonte NO cursor (ou à frente) retoma. Sem isto, o teste
	// passaria com uma verificação que recusasse tudo.
	emDia := newSourceStore(t, "board-eu", "eu-west")
	seed(t, emDia, "run-a", 4, "m")
	exp, err := NewExporter(emDia, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("a fonte com head == cursor devia retomar: %v", err)
	}
	if got := exp.ResumedFrom(); got != 2 {
		t.Fatalf("devia retomar do ciclo 2; got %d", got)
	}
	if _, err := exp.Export(context.Background()); err != nil {
		t.Fatalf("com head == cursor o ciclo devia correr: %v", err)
	}
}

// fonteComHeadRecuado é uma [eventstore.BackupSource] que, depois de armada, declara para um stream
// um head abaixo do real — o log a ser rebobinado DEBAIXO de um exportador vivo.
type fonteComHeadRecuado struct {
	*eventstore.Store
	stream string
	head   uint64
	armada bool
}

func (f *fonteComHeadRecuado) StreamHead(ctx context.Context, st string) (uint64, error) {
	if f.armada && st == f.stream {
		return f.head, nil
	}
	return f.Store.StreamHead(ctx, st)
}

// A MESMA RECUSA A MEIO DA VIDA DO EXPORTADOR: o ciclo não produz segmento.
//
// Antes, `head <= from` era um `continue` — o stream era saltado em silêncio e o ciclo devolvia
// sucesso. Agora o ciclo devolve [ErrSourceBehindBackup] e o destino fica exactamente como estava.
func TestAOS101_LogREBOBINADOaMeioNaoProduzSegmento(t *testing.T) {
	ctx := context.Background()
	store := newSourceStore(t, "board-eu", "eu-west")
	seed(t, store, "run-a", 4, "m")
	src := &fonteComHeadRecuado{Store: store, stream: "run-a", head: 2}
	dst := NewInMemoryImmutableStore("eu-west")

	exp, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}
	antes := dst.Len()

	src.armada = true
	seed(t, store, "run-b", 1, "m") // há novidade NOUTRO stream: o ciclo não pode selá-la sozinha
	if _, err := exp.Export(ctx); !errors.Is(err, ErrSourceBehindBackup) {
		t.Fatalf("um head abaixo do cursor a meio devia falhar o ciclo com ErrSourceBehindBackup; got %v", err)
	}
	if dst.Len() != antes {
		t.Fatalf("o ciclo recusado NAO pode escrever no destino; objectos %d -> %d", antes, dst.Len())
	}
}

// fonteQueEsqueceUmStream deixa de enumerar um stream que o cursor já conhece.
type fonteQueEsqueceUmStream struct {
	*eventstore.Store
	esquecido string
	armada    bool
}

func (f *fonteQueEsqueceUmStream) Streams() ([]string, error) {
	todos, err := f.Store.Streams()
	if err != nil || !f.armada {
		return todos, err
	}
	out := todos[:0:0]
	for _, st := range todos {
		if st != f.esquecido {
			out = append(out, st)
		}
	}
	return out, nil
}

// UM STREAM QUE A FONTE DEIXA DE ENUMERAR TEM, PARA ELA, HEAD 0 — ABAIXO DO CURSOR.
//
// Os streams lógicos do AOS não desaparecem (o log é append-only, AOS-100). Um que o cursor conhece
// e a enumeração deixou de devolver é uma fonte que já não é a mesma, ou uma enumeração parcial que
// se declara completa — e o ciclo que a aceitasse selaria um elo a dizer «em dia» sobre um stream
// que não viu.
func TestAOS101_StreamQueAFonteDeixaDeEnumerarFalhaOCiclo(t *testing.T) {
	ctx := context.Background()
	store := newSourceStore(t, "board-eu", "eu-west")
	seed(t, store, "run-a", 2, "m")
	src := &fonteQueEsqueceUmStream{Store: store, esquecido: "run-a"}
	dst := NewInMemoryImmutableStore("eu-west")

	exp, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}
	antes := dst.Len()

	src.armada = true
	seed(t, store, "run-b", 1, "m")
	if _, err := exp.Export(ctx); !errors.Is(err, ErrSourceBehindBackup) {
		t.Fatalf("um stream do cursor fora da enumeracao devia falhar o ciclo com ErrSourceBehindBackup; got %v", err)
	}
	if dst.Len() != antes {
		t.Fatalf("o ciclo recusado NAO pode escrever no destino; objectos %d -> %d", antes, dst.Len())
	}
}

// A CADEIA DE DOIS ARRANQUES RESTAURA — e não só verifica — a partir do que está no DESTINO.
//
// O reinicio_test.go prova que a cadeia de dois processos VERIFICA. A metade que faltava ao limite
// antigo era outra: um restauro sem manifesto guardado à parte. Aqui o restauro corre sobre
// [Restorer.LoadManifest] e os eventos saem com o envelope dos DOIS arranques, seq a seq. A KEK é
// a mesma nos dois (o [audit.KeyVault] partilhado é uma FIXTURE de uma custódia que sobrevive ao
// processo — que no deployment ainda não existe para o backup, AOS-453); sem isso os
// segmentos do primeiro arranque seriam indecifráveis — que é um passo de operação, não do módulo.
func TestAOS101_ACadeiaDeDoisArranquesRestauraPeloLoadManifest(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	vault := audit.NewInMemoryKeyVault(nil)

	seed(t, src, "run-a", 3, "m")
	exp1, err := NewExporter(src, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter (1.º): %v", err)
	}
	if _, err := exp1.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}

	seed(t, src, "run-a", 2, "m")
	seed(t, src, "run-b", 1, "m")
	exp2, err := NewExporter(src, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter (2.º): %v", err)
	}
	if r, err := exp2.Export(ctx); err != nil || r.Cycle != 2 || r.Events != 3 {
		t.Fatalf("o 2.º arranque devia selar o ciclo 2 com os 3 eventos novos; got r=%+v err=%v", r, err)
	}

	rst, err := NewRestorer(dst, vault, signer.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	m, cp, err := rst.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	out := freshDest(t, "board-eu", "eu-west")
	ev, err := rst.RestoreTo(ctx, m, cp, 0, nil, out)
	if err != nil {
		t.Fatalf("RestoreTo sobre a cadeia de dois arranques: %v", err)
	}
	if !ev.Verified || ev.EventsRestored != 6 {
		t.Fatalf("deviam restaurar-se os 6 eventos dos DOIS arranques, verificados; got %+v", ev)
	}
	for _, st := range []string{"run-a", "run-b"} {
		orig, err := src.SnapshotStream(ctx, st, 0)
		if err != nil {
			t.Fatalf("SnapshotStream %s: %v", st, err)
		}
		got, err := out.Read(ctx, st, 1)
		if err != nil {
			t.Fatalf("Read %s: %v", st, err)
		}
		if len(got) != len(orig) {
			t.Fatalf("%s restaurado com %d eventos, quero %d", st, len(got), len(orig))
		}
		for i := range got {
			if got[i].EventID != orig[i].EventID || got[i].Seq != orig[i].Seq || got[i].Ts != orig[i].Ts {
				t.Fatalf("%s#%d: envelope divergente", st, i+1)
			}
		}
	}
}

// lojaQueNaoResponde falha qualquer Get com um erro que NÃO é [ErrNotFound].
type lojaQueNaoResponde struct {
	*InMemoryImmutableStore
	err error
}

func (s *lojaQueNaoResponde) Get(string) ([]byte, error) { return nil, s.err }

// UM DESTINO QUE NÃO RESPONDE NÃO É UM DESTINO VIRGEM.
//
// Se a sondagem lesse um erro de rede como «não há ciclo 1», o exportador recomeçaria do génesis
// sobre uma cadeia que existe — e colidiria no primeiro registo de ciclo, ou pior, num destino que
// só estava lento. A construção ABORTA com o erro do destino.
func TestAOS101_DestinoQueNaoRespondeNaoEVirgem(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	falha := errors.New("destino indisponivel")
	dst := &lojaQueNaoResponde{InMemoryImmutableStore: NewInMemoryImmutableStore("eu-west"), err: falha}

	if _, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand())); !errors.Is(err, falha) {
		t.Fatalf("um destino que nao responde devia abortar a construcao com o erro dele; got %v", err)
	}
}

// lojaQueJaTemOSegmento finge que a referência endereçada por conteúdo já está ocupada: o Put
// devolve [ErrImmutable] e o Get devolve `existente` (o próprio blob, ou outro).
type lojaQueJaTemOSegmento struct {
	*InMemoryImmutableStore
	outro []byte // nil ⇒ o objecto lá guardado é o MESMO que se tentou escrever
}

func (s *lojaQueJaTemOSegmento) Put(ref string, blob []byte, retainUntil time.Time) error {
	if !strings.Contains(ref, "/seg-") {
		return s.InMemoryImmutableStore.Put(ref, blob, retainUntil)
	}
	guardado := blob
	if s.outro != nil {
		guardado = s.outro
	}
	if err := s.InMemoryImmutableStore.Put(ref, guardado, retainUntil); err != nil {
		return err
	}
	return ErrImmutable
}

// A COLISÃO NA REF DO SEGMENTO: o mesmo conteúdo é aceite, conteúdo diferente é recusado.
//
// O retoma_test.go deixa de propósito por fixar qual dos dois caminhos a re-tentativa percorre.
// Aqui fixam-se os dois, deterministicamente: com o MESMO blob o ciclo sela (é a re-tentativa
// idempotente), com OUTRO blob sob a mesma ref o ciclo é recusado com [ErrSegmentRefCollision] e
// nenhum registo de ciclo é selado — senão o manifesto selaria um hash que o destino não guarda.
func TestAOS101_ColisaoNaRefDoSegmentoSoAceitaOMesmoConteudo(t *testing.T) {
	ctx := context.Background()

	mesmo := &lojaQueJaTemOSegmento{InMemoryImmutableStore: NewInMemoryImmutableStore("eu-west")}
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	exp, err := NewExporter(src, mesmo, newSigner(t), WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if r, err := exp.Export(ctx); err != nil || !r.Created || r.Cycle != 1 {
		t.Fatalf("o MESMO conteudo ja no destino devia ser aceite como re-tentativa; got r=%+v err=%v", r, err)
	}

	outro := &lojaQueJaTemOSegmento{InMemoryImmutableStore: NewInMemoryImmutableStore("eu-west"), outro: []byte("outro objecto")}
	exp2, err := NewExporter(src, outro, newSigner(t), WithRandSource(detRand()))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp2.Export(ctx); !errors.Is(err, ErrSegmentRefCollision) {
		t.Fatalf("conteudo DIFERENTE sob a ref devia ser ErrSegmentRefCollision; got %v", err)
	}
	if ok, err := cycleExists(outro, "eu-west", 1); err != nil || ok {
		t.Fatalf("nenhum registo de ciclo pode ser selado sobre uma colisao; existe=%v err=%v", ok, err)
	}
}

// UM PREFIXO EXPIRADO NÃO É UM DESTINO VIRGEM — NEM UMA CADEIA QUE SE POSSA CONTINUAR.
//
// A retenção expira os registos mais ANTIGOS primeiro — o caso normal, não uma avaria. Uma sondagem
// que exigisse o ciclo 1 leria a cadeia inteira como virgem e selaria um ciclo 1 NOVO por cima dela;
// continuá-la a partir do último ciclo anunciaria «RETOMADA» sobre um backup que já não se restaura
// (incremental, sem génese). Recusa-se, a nomear a causa.
func TestAOS101_PrefixoExpiradoRecusaARetoma(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	vault := audit.NewInMemoryKeyVault(nil)
	agora := t0
	relogio := func() time.Time { return agora }
	pol := audit.NewRetentionPolicy(map[audit.DataClass]time.Duration{audit.ClassAudit: time.Hour})

	exp1, err := NewExporter(src, dst, signer, WithKeyVault(vault), WithClock(relogio), WithRetention(pol, audit.ClassAudit))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	var seg1 string
	for i := 0; i < 4; i++ {
		seed(t, src, "run-a", 1, "m")
		r, err := exp1.Export(ctx)
		if err != nil {
			t.Fatalf("ciclo %d: %v", i+1, err)
		}
		if i == 0 {
			seg1 = r.Ref
		}
		agora = agora.Add(20 * time.Minute)
	}
	// O ciclo de vida do destino apaga o ciclo 1 e o seu segmento, cuja retenção expirou.
	for _, ref := range []string{cycleRef("eu-west", 1), seg1} {
		if err := dst.Delete(ref, t0.Add(61*time.Minute)); err != nil {
			t.Fatalf("Delete de %q expirado: %v", ref, err)
		}
	}

	// O que se RECUSA continuar já não era restaurável: o LoadManifest começa no ciclo 1.
	rst, err := NewRestorer(dst, vault, signer.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	if _, _, err := rst.LoadManifest(); err == nil {
		t.Fatal("uma cadeia sem o ciclo 1 nao devia reconstruir-se para restauro")
	}

	_, err = NewExporter(src, dst, signer, WithKeyVault(vault), WithClock(relogio), WithRetention(pol, audit.ClassAudit))
	if !errors.Is(err, ErrResumeUnverifiable) || !strings.Contains(err.Error(), "INICIO da cadeia expirou") {
		t.Fatalf("um prefixo expirado devia RECUSAR a retoma a nomear a genese; got %v", err)
	}
}

// UM BURACO NO MEIO DA CADEIA RECUSA A RETOMA.
//
// Com o ciclo 8 em falta numa cadeia de 10, a bissecção pára no 7; retomar de lá escreveria no
// buraco um ciclo 8 NOVO, divergente do que lá esteve, e a cadeia deixava de verificar a partir
// dele. A guarda procura ciclos para lá do fim encontrado.
func TestAOS101_BuracoNaCadeiaRecusaARetoma(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	vault := audit.NewInMemoryKeyVault(nil)
	pol := audit.NewRetentionPolicy(map[audit.DataClass]time.Duration{audit.ClassAudit: time.Hour})

	exp1, err := NewExporter(src, dst, signer, WithKeyVault(vault), WithClock(fixedClock(t0)), WithRetention(pol, audit.ClassAudit))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	for i := 0; i < 10; i++ {
		seed(t, src, "run-a", 1, "m")
		if _, err := exp1.Export(ctx); err != nil {
			t.Fatalf("ciclo %d: %v", i+1, err)
		}
	}
	if err := dst.Delete(cycleRef("eu-west", 8), t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("Delete do ciclo 8: %v", err)
	}

	_, err = NewExporter(src, dst, signer, WithKeyVault(vault), WithClock(fixedClock(t0)))
	if !errors.Is(err, ErrResumeUnverifiable) || !strings.Contains(err.Error(), "buraco") {
		t.Fatalf("um buraco na cadeia devia recusar a retoma com ErrResumeUnverifiable (buraco); got %v", err)
	}
}

// O SEGMENTO DO ÚLTIMO ELO TEM DE ESTAR NO DESTINO.
func TestAOS101_SegmentoDoUltimoEloEmFaltaRecusaARetoma(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	seed(t, src, "run-a", 2, "m")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	vault := audit.NewInMemoryKeyVault(nil)
	pol := audit.NewRetentionPolicy(map[audit.DataClass]time.Duration{audit.ClassAudit: time.Hour})

	exp1, err := NewExporter(src, dst, signer, WithKeyVault(vault), WithClock(fixedClock(t0)), WithRetention(pol, audit.ClassAudit))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	r, err := exp1.Export(ctx)
	if err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}
	if err := dst.Delete(r.Ref, t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("Delete do segmento: %v", err)
	}
	_, err = NewExporter(src, dst, signer, WithKeyVault(vault))
	if !errors.Is(err, ErrResumeUnverifiable) {
		t.Fatalf("um elo cujo segmento falta devia recusar a retoma; got %v", err)
	}
}

// lojaComCommitAmbiguo faz commit do PRIMEIRO registo de ciclo e devolve um erro, como um timeout
// depois do commit. A re-tentativa encontra o registo lá.
type lojaComCommitAmbiguo struct {
	*InMemoryImmutableStore
	jaFalhou bool
}

func (l *lojaComCommitAmbiguo) Put(ref string, blob []byte, retainUntil time.Time) error {
	err := l.InMemoryImmutableStore.Put(ref, blob, retainUntil)
	if err == nil && strings.Contains(ref, "/cycle-") && !l.jaFalhou {
		l.jaFalhou = true
		return errors.New("timeout depois do commit")
	}
	return err
}

// A NOSSA ESCRITA AMBÍGUA É ADOPTADA, E NÃO LIDA COMO OUTRO DONO.
//
// O Put do registo fez commit e devolveu erro; o ciclo seguinte colide com ele. O registo verifica
// com a nossa chave e continua o nosso head: é o nosso. Parar o laço aqui por «dois escritores»
// mandaria o operador procurar uma réplica que não existe.
func TestAOS101_EscritaAmbiguaDoRegistoEAdoptada(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := &lojaComCommitAmbiguo{InMemoryImmutableStore: NewInMemoryImmutableStore("eu-west")}
	signer := newSigner(t)
	vault := audit.NewInMemoryKeyVault(nil)
	exp, err := NewExporter(src, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	seed(t, src, "run-a", 2, "m")
	if _, err := exp.Export(ctx); err == nil {
		t.Fatal("o 1.o ciclo devia devolver o erro do commit ambiguo")
	}
	r, err := exp.Export(ctx)
	if err != nil {
		t.Fatalf("a re-tentativa devia ADOPTAR o registo que ja e nosso; got %v", err)
	}
	if r.Cycle != 1 {
		t.Fatalf("o elo adoptado e o ciclo 1; got %d", r.Cycle)
	}
	seed(t, src, "run-a", 1, "m")
	if r, err := exp.Export(ctx); err != nil || r.Cycle != 2 {
		t.Fatalf("depois de adoptar, o ciclo seguinte e o 2; got r=%+v err=%v", r, err)
	}
	if ev := restaurarCadeia(t, dst, vault, signer, 2); ev.EventsRestored != 3 {
		t.Fatalf("a cadeia com a escrita adoptada devia restaurar os 3 eventos; got %d", ev.EventsRestored)
	}
}

// lojaQueFalhaOPrimeiroRegisto recusa (SEM commit) a primeira escrita de um registo de ciclo.
type lojaQueFalhaOPrimeiroRegisto struct {
	*InMemoryImmutableStore
	jaFalhou bool
}

func (l *lojaQueFalhaOPrimeiroRegisto) Put(ref string, blob []byte, retainUntil time.Time) error {
	if strings.Contains(ref, "/cycle-") && !l.jaFalhou {
		l.jaFalhou = true
		return errors.New("destino indisponivel (sem commit)")
	}
	return l.InMemoryImmutableStore.Put(ref, blob, retainUntil)
}

// UMA ESCRITA AMBÍGUA NOSSA NÃO FAZ ADOPTAR O REGISTO DE OUTRO.
//
// B tentou o ciclo 1 e o Put falhou sem commit; entretanto A selou o ciclo 1. A re-tentativa de B
// colide com o registo de A — autêntico, sobre o mesmo head — e ter uma escrita pendente não o torna
// nosso: só um registo com o EntryHash de uma das nossas tentativas o é.
func TestAOS101_EscritaPendenteNaoFazAdoptarORegistoDeOutro(t *testing.T) {
	ctx := context.Background()
	srcA := newSourceStore(t, "board-eu", "eu-west")
	srcB := newSourceStore(t, "board-eu", "eu-west")
	dst := &lojaQueFalhaOPrimeiroRegisto{InMemoryImmutableStore: NewInMemoryImmutableStore("eu-west")}
	signer := newSigner(t)
	vault := audit.NewInMemoryKeyVault(nil)
	expB, err := NewExporter(srcB, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter B: %v", err)
	}
	expA, err := NewExporter(srcA, dst, signer, WithKeyVault(vault))
	if err != nil {
		t.Fatalf("NewExporter A: %v", err)
	}
	seed(t, srcB, "run-a", 3, "B")
	seed(t, srcA, "run-a", 2, "A")
	if _, err := expB.Export(ctx); err == nil {
		t.Fatal("o 1.o registo de B devia falhar (sem commit)")
	}
	if _, err := expA.Export(ctx); err != nil {
		t.Fatalf("A, ciclo 1: %v", err)
	}
	if _, err := expB.Export(ctx); !errors.Is(err, ErrChainOwned) {
		t.Fatalf("a re-tentativa de B colide com o registo de A e devia dar ErrChainOwned, nao adoptar; got %v", err)
	}
}

// UM REGISTO QUE NÃO VERIFICA NA REFERÊNCIA DO CICLO É ADULTERAÇÃO, NÃO BIFURCAÇÃO.
func TestAOS101_RegistoQueNaoVerificaEAdulteracao(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	exp, err := NewExporter(src, dst, newSigner(t))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	// Depois da construção, alguém ocupa a referência do ciclo 1 com lixo.
	if err := dst.Put(cycleRef("eu-west", 1), []byte(`{"lixo":true}`), t0.Add(time.Hour)); err != nil {
		t.Fatalf("Put do lixo: %v", err)
	}
	seed(t, src, "run-a", 1, "m")
	_, err = exp.Export(ctx)
	if !errors.Is(err, ErrCycleRecordInvalid) {
		t.Fatalf("um registo que nao verifica devia dar ErrCycleRecordInvalid; got %v", err)
	}
	if errors.Is(err, ErrChainOwned) {
		t.Fatalf("lixo NAO e bifurcacao; got %v", err)
	}
}

// UM REGISTO AUTÊNTICO QUE PARTE DE OUTRO HEAD É BIFURCAÇÃO.
//
// Assinado com a nossa chave, no índice e na região certos — mas encadeado sobre um elo que não é o
// nosso. É a única colisão que é, de facto, dois donos da mesma cadeia.
func TestAOS101_RegistoAutenticoDeOutroHeadEBifurcacao(t *testing.T) {
	ctx := context.Background()
	signer := newSigner(t)

	// Uma cadeia PARALELA, com a mesma chave, noutro destino da mesma região: dá um ciclo 2
	// autêntico cujo PrevHash é o ciclo 1 DELA.
	srcX := newSourceStore(t, "board-eu", "eu-west")
	dstX := NewInMemoryImmutableStore("eu-west")
	expX, err := NewExporter(srcX, dstX, signer)
	if err != nil {
		t.Fatalf("NewExporter X: %v", err)
	}
	for i := 0; i < 2; i++ {
		seed(t, srcX, "run-x", 1, "X")
		if _, err := expX.Export(ctx); err != nil {
			t.Fatalf("X ciclo %d: %v", i+1, err)
		}
	}
	ciclo2DeX, err := dstX.Get(cycleRef("eu-west", 2))
	if err != nil {
		t.Fatalf("Get ciclo 2 de X: %v", err)
	}

	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	exp, err := NewExporter(src, dst, signer)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	seed(t, src, "run-a", 1, "m")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}
	if err := dst.Put(cycleRef("eu-west", 2), ciclo2DeX, t0.Add(time.Hour)); err != nil {
		t.Fatalf("Put do ciclo 2 de X: %v", err)
	}
	seed(t, src, "run-a", 1, "m")
	if _, err := exp.Export(ctx); !errors.Is(err, ErrChainOwned) {
		t.Fatalf("um registo autentico sobre OUTRO head devia dar ErrChainOwned; got %v", err)
	}
}

// lojaNaoCondicional aceita uma segunda escrita na mesma referência — o S3 com Object Lock mas sem
// If-None-Match: cria uma versão nova e devolve sucesso.
type lojaNaoCondicional struct {
	*InMemoryImmutableStore
}

func (l *lojaNaoCondicional) Put(ref string, blob []byte, retainUntil time.Time) error {
	if err := l.InMemoryImmutableStore.Put(ref, blob, retainUntil); err != nil && !errors.Is(err, ErrImmutable) {
		return err
	}
	return nil
}

// UM DESTINO QUE NÃO É write-once condicional É RECUSADO NA CONSTRUÇÃO.
func TestAOS101_DestinoNaoCondicionalERecusado(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := &lojaNaoCondicional{InMemoryImmutableStore: NewInMemoryImmutableStore("eu-west")}
	_, err := NewExporter(src, dst, newSigner(t))
	if !errors.Is(err, ErrDestinationNotConditional) {
		t.Fatalf("um destino que aceita uma segunda escrita devia ser recusado com ErrDestinationNotConditional; got %v", err)
	}
	if !strings.Contains(err.Error(), "If-None-Match") {
		t.Errorf("o erro tem de nomear o contrato (If-None-Match); got %v", err)
	}
}
