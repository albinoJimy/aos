package eventstore

// aos424_aperto_do_append_test.go — A REGRA DE NOMES BINDA A ESCRITA, E SÓ A ESCRITA.
//
// O aperto do [Store.Append] fecha a assimetria entre backends. A ASSIMETRIA QUE FICA — escrita
// valida, leitura não — é deliberada, e estes testes existem sobretudo para a defender: sem
// eles, alguém «arruma» a inconsistência e leva à frente a migração de dados legados e o
// restauro de backups anteriores à regra.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const legado = "gov.approvals" // um dos nomes que o AOS-424 migrou; a regra nova recusa-o

func factoDeTeste(step string) EventInput {
	return EventInput{
		Type:     "teste.facto",
		Payload:  []byte(`{"a":1}`),
		RunID:    "run-424",
		StepID:   step,
		Producer: Producer{NHIID: "nhi:teste"},
	}
}

// O Append RECUSA um nome irrepresentável, e recusa-o lexicalmente.
func TestAOS424AppendRecusaNomeIrrepresentavel(t *testing.T) {
	s := mustNew(t)
	for _, nome := range []string{
		"gov.approvals", "sched.queue", "a b", "a*b", "a>b", "a\tb", "a\nb", "",
	} {
		_, err := s.Append(context.Background(), nome, factoDeTeste("s1"))
		if !errors.Is(err, ErrConfig) {
			t.Errorf("Append(%q) devia recusar com ErrConfig, veio %v:\n"+
				"sobre JetStream este nome nao e representavel, e o backend de ficheiro estava a\n"+
				"aceita-lo — era essa diferenca que deixava o defeito passar todos os gates", nome, err)
		}
	}
}

// O CARÁCTER DE CONTROLO, que a lista nomeada não continha.
//
// Este é o caso que o aperto revelou e que a versão anterior da regra deixava passar: o
// `nonceScope` do autenticador juntava domínio e emissor com um `\x00`, e esse byte ia inteiro
// para o nome do stream. Uma lista de proibidos escrita à mão só cobre o que quem a escreveu se
// lembrou; esta classe decide-se por propriedade.
func TestAOS424AppendRecusaCaractereDeControlo(t *testing.T) {
	s := mustNew(t)
	for _, nome := range []string{
		"steer:dominio\x00emissor", // o caso real
		"a\x01b", "a\x1fb", "a\x7fb",
	} {
		_, err := s.Append(context.Background(), nome, factoDeTeste("s1"))
		if !errors.Is(err, ErrConfig) {
			t.Errorf("Append(%q) devia recusar o caractere de controlo, veio %v", nome, err)
		}
	}
	// E a mensagem tem de dizer ONDE, senão um nome com um byte invisível é indiagnosticável.
	_, err := s.Append(context.Background(), "a\x00b", factoDeTeste("s1"))
	if err == nil || !strings.Contains(err.Error(), "posição 1") {
		t.Errorf("a recusa devia nomear a posicao do byte invisivel, veio: %v", err)
	}
}

// A RECUSA NÃO DEIXA RASTO. É lexical e acontece antes de stripe, líder e quórum, pelo que o
// stream não fica a existir com zero eventos nem consome um seq.
func TestAOS424RecusaNaoMaterializaNada(t *testing.T) {
	s := mustNew(t)
	if _, err := s.Append(context.Background(), legado, factoDeTeste("s1")); err == nil {
		t.Fatal("o Append devia ter recusado")
	}
	h, err := s.StreamHead(context.Background(), legado)
	if err != nil && !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("StreamHead apos recusa: %v", err)
	}
	if h != 0 {
		t.Errorf("a recusa deixou o stream %q com head=%d; devia nao ter tocado em nada", legado, h)
	}
}

// A ASSIMETRIA, FIXADA: o que já existe continua a poder ser LIDO e RESTAURADO.
//
// Se este teste ficar vermelho porque alguém acrescentou a validação às leituras, a resposta NÃO
// é actualizar o teste: é reverter. Um nome legado tem de poder ser lido para ser migrado para
// fora com [CopiarStream], e um backup anterior a esta regra tem de poder ser restaurado. A
// regra proíbe CRIAR nomes irrepresentáveis, não recuperar os que existem.
func TestAOS424LeituraERestauroAceitamNomeLegado(t *testing.T) {
	ctx := context.Background()
	s := mustNew(t)
	if _, err := SemearStreamLegado(ctx, s, legado, factoDeTeste("s1")); err != nil {
		t.Fatalf("semear o mundo antes: %v", err)
	}

	if _, err := s.Read(ctx, legado, 0); err != nil {
		t.Errorf("Read(%q) = %v; um nome legado tem de continuar legivel para poder ser migrado", legado, err)
	}
	if _, err := s.StreamHead(ctx, legado); err != nil {
		t.Errorf("StreamHead(%q) = %v", legado, err)
	}
	snap, err := s.SnapshotStream(ctx, legado, 1)
	if err != nil {
		t.Fatalf("SnapshotStream(%q) = %v", legado, err)
	}

	// E o restauro para um store novo — o caminho de recuperação de desastre.
	destino := mustNew(t)
	if err := destino.IngestStream(ctx, legado, snap); err != nil {
		t.Errorf("IngestStream(%q) = %v; um backup anterior a esta regra tem de poder ser restaurado", legado, err)
	}
}

// A COSTURA SEMEIA UM MUNDO FIEL — mesmo envelope, mesma idempotência.
//
// É o que torna os testes de migração honestos: se a semente divergisse do que o binário antigo
// escrevia, a migração seria provada contra um «antes» que nunca existiu.
func TestAOS424SementeProduzOMesmoEnvelopeQueOAppend(t *testing.T) {
	ctx := context.Background()
	s := mustNew(t)

	semeado, err := SemearStreamLegado(ctx, s, legado, factoDeTeste("s1"))
	if err != nil {
		t.Fatalf("SemearStreamLegado: %v", err)
	}
	normal, err := s.Append(ctx, "aos-internal/gov/approvals", factoDeTeste("s1"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if semeado.Event.IdempotencyKey != normal.Event.IdempotencyKey {
		t.Errorf("a semente produziu idempotency-key %q e o Append %q: o mundo «antes» nao seria fiel",
			semeado.Event.IdempotencyKey, normal.Event.IdempotencyKey)
	}
	if semeado.Seq != normal.Seq || semeado.Status != normal.Status {
		t.Errorf("semente (seq=%d status=%v) diverge do Append (seq=%d status=%v)",
			semeado.Seq, semeado.Status, normal.Seq, normal.Status)
	}

	// E a idempotência DO STREAM LEGADO continua a funcionar através da costura — é ela que a
	// migração transporta, e é o uso-único de um grant já consumido que depende dela.
	repetido, err := SemearStreamLegado(ctx, s, legado, factoDeTeste("s1"))
	if err != nil {
		t.Fatalf("segunda semente: %v", err)
	}
	if repetido.Status != StatusDuplicate {
		t.Errorf("semear o mesmo facto duas vezes deu %v, esperava StatusDuplicate: "+
			"sem deduplicacao, o mundo «antes» nao preserva o uso-unico", repetido.Status)
	}
}
