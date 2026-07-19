package backup

import (
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

func TestImmutable_WriteOnce(t *testing.T) {
	s := NewInMemoryImmutableStore("eu-west")
	retain := t0.Add(time.Hour)
	if err := s.Put("ref-1", []byte("primeiro"), retain); err != nil {
		t.Fatalf("Put#1: %v", err)
	}
	// Segunda escrita à MESMA ref é recusada (WORM write-once).
	if err := s.Put("ref-1", []byte("sobrescrita"), retain); !errors.Is(err, ErrImmutable) {
		t.Fatalf("Put#2 devia falhar com ErrImmutable, deu: %v", err)
	}
	// O conteúdo original permanece intacto.
	got, err := s.Get("ref-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "primeiro" {
		t.Fatalf("conteúdo mutado: %q", got)
	}
}

func TestImmutable_GetNotFound(t *testing.T) {
	s := NewInMemoryImmutableStore("eu-west")
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(inexistente)=%v, quero ErrNotFound", err)
	}
	if err := s.Delete("nope", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(inexistente)=%v, quero ErrNotFound", err)
	}
}

func TestImmutable_ObjectLock(t *testing.T) {
	s := NewInMemoryImmutableStore("eu-west")
	retain := t0.Add(time.Hour)
	if err := s.Put("ref-1", []byte("x"), retain); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("ref-1", t0.Add(30*time.Minute)); !errors.Is(err, ErrObjectLocked) {
		t.Fatalf("Delete dentro do lock devia falhar: %v", err)
	}
	if err := s.Delete("ref-1", t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("Delete após o lock: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("objecto devia ter sido removido após o lock")
	}
}

func TestImmutable_LegalHoldBlocksDelete(t *testing.T) {
	hold := audit.NewLegalHold()
	s := NewInMemoryImmutableStore("eu-west", WithLegalHold(hold, "backup:eu-west"))
	// object-lock já expirado, mas legal hold activo.
	if err := s.Put("ref-1", []byte("x"), t0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	hold.HoldPartition("backup:eu-west")
	if err := s.Delete("ref-1", t0.Add(time.Hour)); !errors.Is(err, ErrObjectLocked) {
		t.Fatalf("legal hold devia impedir a remoção: %v", err)
	}
	// Levantado o hold → remoção permitida (lock já expirara).
	hold.ReleasePartition("backup:eu-west")
	if err := s.Delete("ref-1", t0.Add(time.Hour)); err != nil {
		t.Fatalf("após levantar o hold: %v", err)
	}
}
