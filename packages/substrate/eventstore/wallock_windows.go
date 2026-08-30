//go:build windows

package eventstore

import (
	"errors"
	"fmt"
	"syscall"
)

// wallock_windows.go — a posse no Windows: abrir SEM PARTILHA.
//
// `CreateFile` com `dwShareMode = 0` concede o ficheiro a UM só handle em todo o
// sistema: qualquer segunda abertura — deste processo ou de outro — falha com
// ERROR_SHARING_VIOLATION. É o mecanismo mais simples que dá a propriedade que
// interessa, e o SO fecha o handle (logo, larga a posse) quando o processo morre.
//
// Zero dependências: `syscall` é stdlib. `LockFileEx` seria a alternativa canónica mas
// NÃO está exportada no `syscall` do Go — usá-la exigiria `golang.org/x/sys/windows`, e
// o binário do nó é zero-dep por decisão registada (ADR-017).

// errSharingViolation é o ERROR_SHARING_VIOLATION do Win32 (32). Não existe como
// constante nomeada no `syscall` do Go; o valor é o do próprio SO e é estável desde
// sempre. Declarado aqui com nome para que a comparação abaixo se leia.
const errSharingViolation = syscall.Errno(32)

// errLockViolation é o ERROR_LOCK_VIOLATION (33). Aceite pela mesma razão: o SO pode
// reportar a contenção por qualquer um dos dois, e tratar só um deixaria a recusa a
// parecer uma avaria de I/O.
const errLockViolation = syscall.Errno(33)

func lockWALFile(lockPath string) (WALUnlocker, error) {
	p, err := syscall.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, fmt.Errorf("eventstore: caminho de posse %q: %w", lockPath, err)
	}
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_WRITE,
		0, // SEM PARTILHA — é isto que arbitra
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, errSharingViolation) || errors.Is(err, errLockViolation) {
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
		return syscall.CloseHandle(h)
	}, nil
}
