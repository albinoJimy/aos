package replay

import (
	"context"
	"errors"
	"testing"
)

// Accessores de teste que MODELAM o IAM próprio do payload store: o escritor do
// Event Store detém só o escopo de ESCRITA; o leitor de replay detém só o de
// LEITURA. A separação é o cerne do content-capture mode 3 (AOS-079).
var (
	writerAccessor = Accessor{Principal: "es-writer", Scopes: []string{DefaultPayloadWriteScope}}
	readerAccessor = Accessor{Principal: "replay-reader", Scopes: []string{DefaultPayloadReadScope}}
)

func TestPayloadStorePutGetRoundTrip(t *testing.T) {
	s := NewInMemoryPayloadStore()
	ctx := context.Background()
	content := []byte("payload-nao-deterministico")

	ref, err := s.Put(ctx, PayloadPutRequest{RunID: "run1", StepID: "step-000001", Turn: 1, Payload: content, Writer: writerAccessor})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.IsZero() || ref.Digest != digestOf(content) {
		t.Fatalf("ref não content-addressable: %+v", ref)
	}
	got, err := s.Get(ctx, ref, readerAccessor)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("round-trip corrompido: %q != %q", got, content)
	}
}

// Content-addressable: o MESMO conteúdo produz a MESMA referência (dedup natural).
func TestPayloadStoreContentAddressable(t *testing.T) {
	s := NewInMemoryPayloadStore()
	ctx := context.Background()
	r1, _ := s.Put(ctx, PayloadPutRequest{Payload: []byte("igual"), Writer: writerAccessor})
	r2, _ := s.Put(ctx, PayloadPutRequest{Payload: []byte("igual"), Writer: writerAccessor})
	if r1.Digest != r2.Digest {
		t.Fatalf("conteúdo igual devia dar refs iguais: %q != %q", r1.Digest, r2.Digest)
	}
	if s.count() != 1 {
		t.Fatalf("dedup content-addressable falhou: %d blobs", s.count())
	}
}

// IAM: um accessor SEM o escopo de leitura é NEGADO fail-closed, mesmo que o payload
// exista. É a prova de que o payload está atrás do seu próprio IAM.
func TestPayloadStoreGetDeniesUnauthorized(t *testing.T) {
	s := NewInMemoryPayloadStore()
	ctx := context.Background()
	ref, _ := s.Put(ctx, PayloadPutRequest{Payload: []byte("segredo"), Writer: writerAccessor})

	// O ESCRITOR do Event Store (só escopo de escrita) NÃO pode ler — separação de IAM.
	if _, err := s.Get(ctx, ref, writerAccessor); !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("escritor do ES não devia poder ler: %v", err)
	}
	// Um accessor sem escopos nenhuns é negado.
	if _, err := s.Get(ctx, ref, Accessor{Principal: "anon"}); !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("accessor anónimo devia ser negado: %v", err)
	}
	// O leitor autorizado passa.
	if _, err := s.Get(ctx, ref, readerAccessor); err != nil {
		t.Fatalf("leitor autorizado devia passar: %v", err)
	}
}

// IAM na escrita: um Writer sem o escopo de escrita é negado (não escreve nada).
func TestPayloadStorePutDeniesUnauthorized(t *testing.T) {
	s := NewInMemoryPayloadStore()
	ctx := context.Background()
	_, err := s.Put(ctx, PayloadPutRequest{Payload: []byte("x"), Writer: readerAccessor})
	if !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("Writer sem escopo de escrita devia ser negado: %v", err)
	}
	if s.count() != 0 {
		t.Fatalf("nada devia ter sido escrito num Put negado: %d", s.count())
	}
}

func TestPayloadStoreGetNotFound(t *testing.T) {
	s := NewInMemoryPayloadStore()
	_, err := s.Get(context.Background(), PayloadRef{Digest: "sha256:inexistente"}, readerAccessor)
	if !errors.Is(err, ErrPayloadNotFound) {
		t.Fatalf("esperava ErrPayloadNotFound, obtive %v", err)
	}
}

// Integridade: um blob adulterado (o conteúdo já não corresponde ao seu digest) é
// rejeitado — a referência content-addressable PROVA a integridade.
func TestPayloadStoreGetIntegrityViolation(t *testing.T) {
	s := NewInMemoryPayloadStore()
	ctx := context.Background()
	ref, _ := s.Put(ctx, PayloadPutRequest{Payload: []byte("original"), Writer: writerAccessor})
	// Adultera o blob armazenado directamente (simula corrupção no storage) mantendo a
	// chave (o digest da referência) — o conteúdo deixa de bater com o hash.
	s.mu.Lock()
	s.blobs[ref.Digest] = []byte("adulterado")
	s.mu.Unlock()
	if _, err := s.Get(ctx, ref, readerAccessor); !errors.Is(err, ErrPayloadIntegrity) {
		t.Fatalf("esperava ErrPayloadIntegrity, obtive %v", err)
	}
}

// Escopos configuráveis: o IAM não está preso aos defaults.
func TestPayloadStoreCustomScopes(t *testing.T) {
	s := NewInMemoryPayloadStore(WithReadScope("obs:payload:get"), WithWriteScope("rt:payload:put"))
	ctx := context.Background()
	w := Accessor{Scopes: []string{"rt:payload:put"}}
	r := Accessor{Scopes: []string{"obs:payload:get"}}
	ref, err := s.Put(ctx, PayloadPutRequest{Payload: []byte("z"), Writer: w})
	if err != nil {
		t.Fatalf("Put com escopo custom: %v", err)
	}
	if _, err := s.Get(ctx, ref, r); err != nil {
		t.Fatalf("Get com escopo custom: %v", err)
	}
	// O default já não serve — escopos custom em vigor.
	if _, err := s.Get(ctx, ref, readerAccessor); !errors.Is(err, ErrPayloadAccessDenied) {
		t.Fatalf("escopo default não devia servir com escopos custom: %v", err)
	}
}
