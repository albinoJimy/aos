//go:build !windows

package eventstore

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// wallock_unix.go — a posse em Unix: `flock` EXCLUSIVO e NÃO-BLOQUEANTE.
//
// `LOCK_EX|LOCK_NB` concede a posse a um só descritor aberto no sistema e devolve
// EWOULDBLOCK de imediato se ela já estiver tomada — não espera, que é o comportamento
// certo no arranque: um nó que fica pendurado à espera de uma posse é indistinguível de
// um nó avariado.
//
// O SO liberta o `flock` quando o descritor fecha, incluindo na morte abrupta do
// processo. É isso que dispensa TTL e torna impossível a posse órfã.
//
// Zero dependências: `syscall` é stdlib.

func lockWALFile(lockPath string) (WALUnlocker, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eventstore: posse do WAL %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Fechar ANTES de devolver: um descritor deixado aberto num arranque recusado
		// seria um leak por cada tentativa, e as tentativas repetem-se (supervisor a
		// reiniciar o serviço).
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %q", ErrWALHeld, lockPath)
		}
		return nil, fmt.Errorf("eventstore: posse do WAL %q: %w", lockPath, err)
	}
	var largado bool
	return func() error {
		if largado {
			return nil
		}
		largado = true
		// O `Close` larga o flock; libertá-lo explicitamente antes torna a intenção
		// legível e não depende de o leitor saber essa propriedade do descritor.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return f.Close()
	}, nil
}
