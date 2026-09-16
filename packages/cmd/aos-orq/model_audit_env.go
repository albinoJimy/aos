package main

// AUDIT DE GOVERNAÇÃO DO MODEL GATEWAY DO PLANEADOR (AOS-395) — STORE DURÁVEL.
//
// O gateway sela no seu [audit.Store] a governação da soberania: o changelog de ACTIVAÇÃO da
// allowlist regional no arranque e cada decisão allow/deny por chamada. No `aos-orq` esse store
// era SEMPRE um [audit.NewMemStore]: os selos das chamadas de decomposição do planeador
// PERDIAM-SE no fim do processo — e o `aos-orq` é um processo curto, ao contrário do nó, pelo
// que a perda era a regra e não a excepção.
//
// AOS-395 troca-o pela cadeia DURÁVEL quando AOS_MODEL_AUDIT_PATH está definida, no MESMO molde
// do nó (packages/cmd/aos/model_audit_env.go): um [audit.FileStore] WORM tamper-evident, com
// replay crash-safe na abertura. Vazio ⇒ mantém o MemStore, e a postura volátil é DECLARADA.
//
// UM ESCRITOR POR CAMINHO. O WORM do audit não arbitra entre processos: dois processos a selar
// a MESMA partição (`modelgw-gov:<board>`) do MESMO ficheiro BIFURCAM a hash-chain, e uma cadeia
// bifurcada deixa o WORM INABRÍVEL na reabertura. Por isso a abertura pede primeiro a posse
// EXCLUSIVA de escrita ao sistema operativo ([eventstore.LockWAL], o árbitro do AOS-285, sobre o
// irmão `<path>.lock`): uma segunda réplica `aos-orq` com o mesmo caminho é recusada ANTES de
// abrir (o replay de abertura trunca uma cauda incompleta, e a cauda seria a escrita em curso do
// outro processo) e sai com o código 5, como o `--wal` detido. A posse não cobre o nó `aos`, que
// abre o seu `model-audit.wal` sem a pedir: um host que corra os dois tem de lhes dar caminhos
// DISTINTOS. Um lock de SO sobre um volume partilhado por rede depende do sistema de ficheiros.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	audit "github.com/aos-ref/platform/audit"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

// ErrBadModelAudit — AOS_MODEL_AUDIT_PATH está definida mas o WORM durável não abre (caminho
// inacessível, posse de outro processo, hash-chain adulterada ou bifurcada que o replay
// recusa). Fail-closed de CONFIG: distingue "não configurado" (vazio ⇒ MemStore declarado) de
// "configurado e inválido", e aborta em vez de degradar em silêncio para governação volátil.
var ErrBadModelAudit = errors.New("aos-orq: audit duravel do model gateway mal configurado — AOS_MODEL_AUDIT_PATH aponta um WORM que nao abre (directorio inexistente, caminho inacessivel, detido por outro processo ou hash-chain que o replay recusa); fail-closed em vez de degradar para audit in-memory silencioso")

// parseModelAuditFromEnv resolve o [audit.Store] de governação do gateway do planeador a partir
// do ambiente. Vazio ⇒ (nil, "", fechar no-op, nil): o call site usa o MemStore de referência.
// Presente ⇒ toma a posse exclusiva do caminho, abre o WORM durável e devolve-o com o caminho e o
// `fechar`, que fecha o store e larga a posse (o SO larga-a na mesma se o processo morrer).
// Posse tomada por outro processo ⇒ o erro carrega [eventstore.ErrWALHeld] (código 5).
//
// Env:
//   - AOS_MODEL_AUDIT_PATH — caminho do WAL do WORM de governação. DISTINTO do path do nó.
func parseModelAuditFromEnv() (audit.Store, string, func() error, error) {
	nada := func() error { return nil }
	path := strings.TrimSpace(os.Getenv("AOS_MODEL_AUDIT_PATH"))
	if path == "" {
		return nil, "", nada, nil
	}
	// O directório tem de EXISTIR, como no nó: o `LockWAL` criá-lo-ia, e um erro de escrita no
	// caminho selaria em silêncio num sítio que o operador não escolheu.
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return nil, "", nada, fmt.Errorf("%w: directorio de %q inexistente ou nao e directorio", ErrBadModelAudit, path)
	}
	largar, err := eventstore.LockWAL(path)
	if err != nil {
		return nil, "", nada, fmt.Errorf("%w: %w", ErrBadModelAudit, err)
	}
	fs, err := audit.OpenFileStore(path)
	if err != nil {
		_ = largar()
		return nil, "", nada, fmt.Errorf("%w: %v", ErrBadModelAudit, err)
	}
	fechar := func() error {
		return errors.Join(fs.Close(), largar())
	}
	return fs, path, fechar, nil
}

// modelAuditPostureBanner declara o MODO do audit de governação, amarrado ao ESTADO composto (o
// store realmente durável) e não à intenção da config. Só sai quando há gateway composto: sem
// gateway não há chamadas de decomposição a selar. Sem acentos, no idioma das linhas do binário.
func modelAuditPostureBanner(gatewayComposto bool, durablePath string) string {
	if !gatewayComposto {
		return ""
	}
	if durablePath == "" {
		return "audit de governacao do model gateway (AOS-395): IN-MEMORY (VOLATIL) — os selos modelgw-gov das chamadas de decomposicao PERDEM-SE no fim deste processo. Defina AOS_MODEL_AUDIT_PATH (caminho PROPRIO, distinto do no) para os selar num WORM duravel tamper-evident"
	}
	return fmt.Sprintf("audit de governacao do model gateway (AOS-395): DURAVEL — selos modelgw-gov num WORM tamper-evident em disco (%s), com replay crash-safe; sobrevivem ao fim do processo. O caminho tem de ser PROPRIO deste binario: dois escritores na mesma particao bifurcam a hash-chain", durablePath)
}
