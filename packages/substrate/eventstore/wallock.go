package eventstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// wallock.go — POSSE EXCLUSIVA DE ESCRITA DE UM WAL, ARBITRADA PELO SISTEMA OPERATIVO
// (AOS-285).
//
// # O problema, e porque o árbitro NÃO pode ser este pacote
//
// Dois processos a escrever o mesmo WAL não é seguro: as réplicas de AOS-100 são cópias
// IN-PROCESS do log e o índice de dedup vive em memória, pelo que cada [Open] fica com a
// sua própria cabeça. Medido (DEF-282): dois `Open` sobre o mesmo ficheiro e dois
// `Claim` do mesmo run passam AMBOS, com o mesmo token.
//
// A defesa óbvia — um lease singleton sobre o próprio Event Store — é VACUOSA, e pela
// razão exacta que ela existiria para cobrir: dois processos reclamam, ambos ganham,
// ambos concluem que são o único. O árbitro tem de estar FORA do log.
//
// Aqui é o SISTEMA OPERATIVO. E essa escolha traz de graça a parte difícil de qualquer
// solução por lease: **o SO liberta a posse quando o processo morre**, pelo que não há
// TTL a afinar, nem posse órfã após um crash, nem relógio em que confiar.
//
// # Porque um ficheiro AO LADO, e não o próprio WAL
//
// Trancar o WAL bloquearia quem só o LÊ — e as vias de leitura (`aos wal-inspect`,
// `aos wal-summary`) são precisamente o que um operador usa a meio de um incidente, com
// o nó a correr. A posse é marcada num ficheiro irmão (`<wal>.lock`), pelo que:
//
//   - quem ESCREVE pede a posse explicitamente e é recusado se ela estiver tomada;
//   - quem LÊ abre o WAL como sempre, sem pedir nada e sem ser bloqueado.
//
// O ficheiro `.lock` FICA em disco depois de libertado, e isso é irrelevante por
// desenho: o que arbitra é a posse do descritor, não a existência do ficheiro. É a
// diferença entre isto e um lock-file ingénuo — que deixaria um ficheiro órfão a
// bloquear o arranque seguinte depois de um crash, exactamente o modo de falha que este
// mecanismo evita.
//
// # Isto NÃO é o [Store] a arbitrar
//
// [LockWAL] é DELIBERADAMENTE separado de [Open], e não é arrumação: o Event Store
// continua sem arbitrar coisa nenhuma entre processos, e o `DEF-282` continua ABERTO.
// Dobrar a tranca dentro do [Open] faria parecer que o substrato ganhou uma garantia que
// não tem — e a garantia que falta (o `expected_seq` atómico entre escritores) é outra,
// e é a que interessa. Quem compõe pede a posse; o substrato não muda de contrato.

// ErrWALHeld — o WAL já é detido por outro processo. É o sinal de que a configuração
// pedida (duas réplicas sobre o mesmo Event Store) não é suportada, não o de uma avaria.
var ErrWALHeld = errors.New("eventstore: WAL já detido por outro processo")

// WALUnlocker larga a posse. Idempotente. Chamar em shutdown; a morte abrupta do
// processo larga-a na mesma, pelo SO.
type WALUnlocker func() error

// LockWAL adquire a posse EXCLUSIVA de ESCRITA do WAL em `path`, arbitrada pelo sistema
// operativo sobre o ficheiro irmão `<path>.lock`.
//
// Devolve [ErrWALHeld] se outro processo já a detiver — fail-closed e distinguível por
// errors.Is de qualquer erro de I/O, porque a acção do operador é diferente nos dois
// casos (parar a outra réplica, versus arranjar o disco).
//
// O directório do WAL é criado se não existir, pela mesma razão que [Open] o faz: pedir
// a posse de um caminho ainda por povoar é o caso normal do primeiro arranque.
func LockWAL(path string) (WALUnlocker, error) {
	if path == "" {
		return nil, errors.New("eventstore: caminho do WAL vazio")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("eventstore: criar directório do WAL %q: %w", dir, err)
		}
	}
	return lockWALFile(path + ".lock")
}
