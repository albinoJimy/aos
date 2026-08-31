package main

import (
	"errors"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// wal_posse.go — O GUARD DE ARRANQUE (AOS-285): o nó recusa arrancar sobre um Event
// Store já detido por outro processo.
//
// # O que este guard impede, e o que NÃO impede
//
// IMPEDE: duas réplicas do nó sobre o MESMO WAL. Isso não é seguro — o Event Store de
// referência não arbitra entre processos (DEF-282, medido: dois `Open` sobre o mesmo
// ficheiro e dois `Claim` do mesmo run passam AMBOS, com o mesmo token). Sem este
// guard, o que impedia a configuração era um parágrafo em `tecnica/10` §3-bis; e a
// consequência de a ignorar não era um erro visível, era a maquinaria de posse a DAR A
// IMPRESSÃO de arbitrar enquanto duas réplicas escreviam o mesmo run.
//
// NÃO IMPEDE, e é honesto dizê-lo: o `DEF-282` continua ABERTO. O guard não dá ao
// substrato a arbitragem que lhe falta — fecha a via pela qual alguém lá chegaria sem
// saber. Um segundo escritor que não seja o nó (outro binário a abrir o mesmo WAL sem
// pedir a posse) continua a não ser impedido; ver a nota de escopo no fim.
//
// # Porque a tranca é do SISTEMA OPERATIVO
//
// A defesa óbvia seria um lease singleton sobre o próprio Event Store. É VACUOSA, e
// pela razão exacta que o guard existe para cobrir: dois processos reclamam, ambos
// ganham, ambos concluem que são o único. O árbitro tem de estar FORA do log — ver
// [eventstore.LockWAL].

// posseDoWAL é a posse que este nó detém sobre os seus ficheiros duráveis, ou nil se o
// guard não se aplica (ver [guardDePosseAplicavel]).
//
// São DOIS ficheiros distintos e ambos precisam de posse — ver [tomarPosseDoWAL].
type posseDoWAL struct {
	largar []eventstore.WALUnlocker
	paths  []string
}

// Largar devolve as posses, pela ordem INVERSA da aquisição. Idempotente e nil-safe:
// chamar num nó sem guard é no-op.
func (p *posseDoWAL) Largar() error {
	if p == nil {
		return nil
	}
	var primeiro error
	for i := len(p.largar) - 1; i >= 0; i-- {
		if p.largar[i] == nil {
			continue
		}
		if err := p.largar[i](); err != nil && primeiro == nil {
			primeiro = err
		}
		p.largar[i] = nil
	}
	return primeiro
}

// guardDePosseAplicavel decide se o guard de arranque se aplica a esta configuração —
// e existe como função NOMEADA, em vez de um `if` no meio do bootstrap, para que a
// condição seja legível e revisitável.
//
// # A CONDIÇÃO, e quando ela tem de ser revista
//
// O guard aplica-se sse o Event Store é **respaldado por ficheiro** — isto é, quando o
// nó o abre a partir de `AOS_EVENTSTORE_PATH`. É essa a topologia de ESCRITOR ÚNICO:
// um WAL local, sem arbitragem entre processos.
//
// NÃO se aplica quando:
//
//   - o store é IN-MEMORY (sem path): não há ficheiro a partilhar, logo não há
//     configuração insegura possível;
//   - o store é FORNECIDO por config: o seu ciclo de vida é do chamador, e trancar um
//     recurso que não abrimos seria tomar uma decisão que não é nossa.
//
// **QUANDO O AOS-100 LANDAR** um Event Store genuinamente partilhado (NATS JetStream),
// correr N réplicas passa a ser o OBJECTIVO e recusá-las seria recusar exactamente o
// que se quer. Nessa altura esta condição TEM de ser revista — e é por isso que ela
// está aqui, nomeada e comentada, em vez de implícita num `if path != ""` que alguém
// teria de descobrir a remover. O `DEF-282` é o eixo que traz a revisão junto.
func guardDePosseAplicavel(cfg Config) (path string, aplica bool) {
	if cfg.EventStore != nil {
		return "", false // fornecido pelo chamador — não é nosso para trancar
	}
	if cfg.EventStorePath == "" {
		return "", false // in-memory — nada partilhável
	}
	return cfg.EventStorePath, true
}

// guardDoWORMAplicavel é o MESMO teste para o WORM durável, que é um ficheiro
// SEPARADO (`WORMPath`) e não estava coberto pelo guard original de AOS-285.
//
// # Porque precisa de posse própria, e não por simetria
//
// MEDIDO a 2026-08-31 (AOS-284, AC1): dois `audit.OpenFileStore` sobre o mesmo ficheiro
// e um `Append` de cada na MESMA partição produzem **FORK** — ambos escrevem
// `audit_seq=1`. A consequência não é degradação: ao reabrir, a verificação de
// integridade do arranque (AOS-221) **RECUSA** a cadeia e o nó NÃO ARRANCA — e
// classifica-a como `adulteracao insertion`, indistinguível de um ataque.
//
// O caso comum já estava coberto **por consequência**: com os dois caminhos
// partilhados, o guard do Event Store recusa o arranque ANTES de o WORM ser aberto
// (a posse é tomada em `New` antes de ambos os `Open`). O que ficava a descoberto era
// a configuração ASSIMÉTRICA — mesmo WORM, Event Stores diferentes —, que nenhum dos
// dois guardas via.
func guardDoWORMAplicavel(cfg Config) (path string, aplica bool) {
	if cfg.WORM != nil {
		return "", false // fornecido pelo chamador — o ciclo de vida é dele
	}
	if cfg.WORMPath == "" {
		return "", false // in-memory — nada partilhável
	}
	return cfg.WORMPath, true
}

// ErrEventStoreJaDetido — outro processo já detém este Event Store. É a recusa do guard
// de AOS-285, e é DISTINTA de um erro de I/O: a acção do operador é parar a outra
// réplica, não arranjar o disco.
var ErrEventStoreJaDetido = errors.New("aos: Event Store já detido por outro processo — duas réplicas do nó sobre o mesmo Event Store NÃO é uma configuração suportada")

// tomarPosseDoWAL adquire a posse exclusiva de escrita do Event Store, se o guard se
// aplicar. Devolve (nil, nil) quando não se aplica — o chamador não precisa de saber
// distinguir os casos, só de largar no fim (a largada é nil-safe).
//
// FAIL-CLOSED: uma posse já tomada ABORTA o arranque. Um nó que arranca à mesma, «só
// para ler», escreveria na mesma no primeiro run — e é a escrita concorrente que
// corrompe.
func tomarPosseDoWAL(cfg Config) (*posseDoWAL, error) {
	p := &posseDoWAL{}
	// Cada alvo com a sua razão: o Event Store porque não arbitra entre processos
	// (DEF-282); o WORM porque dois escritores forkam a hash-chain e o nó deixa de
	// arrancar (medido — ver [guardDoWORMAplicavel]).
	alvos := []struct {
		path   string
		aplica bool
		nome   string
		porque string
	}{
		{mustPath(guardDePosseAplicavel(cfg)), mustOK(guardDePosseAplicavel(cfg)), "Event Store",
			"o Event Store de referência não arbitra entre processos — ver DEF-282 e ADR-023 §4"},
		{mustPath(guardDoWORMAplicavel(cfg)), mustOK(guardDoWORMAplicavel(cfg)), "WORM",
			"dois escritores FORKAM a hash-chain (medido: ambos escrevem audit_seq=1) e o arranque seguinte RECUSA a cadeia como adulterada — ver AOS-284"},
	}
	for _, a := range alvos {
		if !a.aplica {
			continue
		}
		largar, err := eventstore.LockWAL(a.path)
		if err != nil {
			// Largar o que já foi tomado: um arranque recusado não pode deixar
			// ficheiros detidos por um processo que não vai existir.
			_ = p.Largar()
			if errors.Is(err, eventstore.ErrWALHeld) {
				return nil, fmt.Errorf("%w (%s): %q. Pare a outra réplica, ou aponte esta a ficheiros próprios. Razão: %s",
					ErrEventStoreJaDetido, a.nome, a.path, a.porque)
			}
			return nil, fmt.Errorf("aos: posse do %s %q: %w", a.nome, a.path, err)
		}
		p.largar = append(p.largar, largar)
		p.paths = append(p.paths, a.path)
	}
	if len(p.largar) == 0 {
		return nil, nil
	}
	return p, nil
}

// mustPath/mustOK desempacotam o par (path, aplica) para a tabela acima — existem só
// porque Go não deixa desestruturar duas devoluções dentro de um literal de slice.
func mustPath(path string, _ bool) string { return path }
func mustOK(_ string, ok bool) bool       { return ok }
