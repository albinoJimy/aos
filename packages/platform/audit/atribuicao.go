package audit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sort"
)

// atribuicao.go — atribuição DETERMINÍSTICA de partições a réplicas (AC5 do AOS-284).
//
// # A lacuna que isto fecha
//
// A posse por partição ([PosseDeParticao]) responde «detenho ESTA partição?». Mas quem
// DECIDE que réplica deve deter que partição? Até aqui, ninguém: era responsabilidade de
// quem implementava a porta, e por isso uma CONVENÇÃO sem guarda. O `GenesisHash` é
// determinístico e `FileStore.Partitions()` enumera as partições que existem — logo quem
// verifica a cadeia consegue saber que partições existiam —, mas não conseguia saber QUEM
// as detinha, porque a atribuição não era uma função reconstruível.
//
// Este ficheiro torna a atribuição uma função PURA de `(partição, conjunto-de-réplicas)`.
// Deixa de ser convenção: a mesma pergunta feita por um verificador, sem acesso a nenhum
// lease store, dá a mesma resposta que o escritor obteve. É o que o AC5 pede — a atribuição
// é determinística e reconstruível.
//
// # Porque rendezvous hashing (HRW) e não módulo
//
// `hash(particao) % N` exige um índice estável por réplica e RESHUFFLE quase total quando N
// muda — uma réplica a entrar ou a sair remapearia praticamente todas as partições, e cada
// remapeamento é um handoff de posse. O rendezvous hashing (Highest-Random-Weight) atribui
// cada partição à réplica de maior peso `H(particao || replica)`: é determinístico, não
// depende da ORDEM da lista, e quando o conjunto muda só remapeia as partições cujo dono
// saiu. É o esquema certo para posse de recursos onde cada handoff custa.
//
// # O que isto NÃO é
//
// Isto é a ATRIBUIÇÃO (quem DEVIA deter). A EXCLUSÃO em tempo real — garantir que só um
// escreve mesmo sob partição de rede — continua a ser o lease durável do ADR-023 por trás
// da [PosseDeParticao]. A atribuição diz a cada réplica que partições reclamar; o lease
// arbitra a reclamação. Compõem-se: [AtribuicaoDeterministica] usa a função pura para
// responder à porta, e uma implementação de produção encadeia-a com o lease.

// atribuicaoPrefix é o domínio determinístico do peso de rendezvous, separado do
// [genesisPrefix] para que os dois hashes nunca se confundam.
const atribuicaoPrefix = "aos.audit.atribuicao:"

// ErrAtribuicaoSemReplicas — não há conjunto de réplicas sobre o qual atribuir. Sem
// candidatos não há dono, e sem dono ninguém escreve (fail-closed): é preferível a uma
// atribuição silenciosa a ninguém.
var ErrAtribuicaoSemReplicas = errors.New("audit: atribuicao de particao sem conjunto de replicas (E_PARTITION_NO_REPLICAS)")

// pesoRendezvous computa o peso HRW de uma partição para uma réplica:
// os primeiros 8 bytes de SHA-256(prefixo || len(particao) || particao || replica).
//
// A partição é PREFIXADA PELO COMPRIMENTO para que ("ab","c") e ("a","bc") não colidam —
// sem isso, a concatenação seria ambígua e duas atribuições distintas partilhariam peso.
func pesoRendezvous(particao, replica string) uint64 {
	h := sha256.New()
	h.Write([]byte(atribuicaoPrefix))
	var lp [8]byte
	binary.BigEndian.PutUint64(lp[:], uint64(len(particao)))
	h.Write(lp[:])
	h.Write([]byte(particao))
	h.Write([]byte(replica))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

// AtribuirParticao devolve a réplica DONA de uma partição, escolhida deterministicamente de
// entre `replicas` por rendezvous hashing. O segundo valor é falso se não houver candidato
// (lista vazia, ou só com nomes vazios) — nesse caso não há dono.
//
// É uma função PURA: não depende da ordem de `replicas` nem de estado nenhum. Réplicas com
// nome vazio são ignoradas (um nome vazio não é uma réplica). Um empate de peso — improvável
// a 64 bits, mas possível — resolve-se pelo nome menor, para que o desempate também seja
// reconstruível e não dependa da ordem da lista.
func AtribuirParticao(particao string, replicas []string) (string, bool) {
	melhorReplica := ""
	var melhorPeso uint64
	encontrou := false
	for _, r := range replicas {
		if r == "" {
			continue
		}
		peso := pesoRendezvous(particao, r)
		switch {
		case !encontrou, peso > melhorPeso, peso == melhorPeso && r < melhorReplica:
			melhorPeso, melhorReplica, encontrou = peso, r, true
		}
	}
	return melhorReplica, encontrou
}

// MapaDeAtribuicao reconstrói a posse de um conjunto de partições sobre um conjunto de
// réplicas: partição → réplica dona. É o que torna o AC5 VERIFICÁVEL de fora — quem verifica
// a cadeia enumera as partições (`FileStore.Partitions()`) e, com o conjunto de réplicas,
// deriva quem detinha cada uma sem consultar lease store nenhum.
//
// Partições sem dono (conjunto de réplicas vazio) não entram no mapa — a ausência é a
// resposta honesta, não uma entrada para "".
func MapaDeAtribuicao(particoes, replicas []string) map[string]string {
	mapa := make(map[string]string, len(particoes))
	for _, p := range particoes {
		if dono, ok := AtribuirParticao(p, replicas); ok {
			mapa[p] = dono
		}
	}
	return mapa
}

// AtribuicaoDeterministica é uma [PosseDeParticao] cuja posse DERIVA da função pura
// [AtribuirParticao]: esta réplica detém uma partição se, e só se, o rendezvous hashing
// sobre `Replicas` a atribui a `EstaReplica`.
//
// Fecha o AC5 ligando a atribuição à porta de posse: a decisão de quem detém deixa de ser
// convenção do implementador e passa a ser uma função reconstruível. Em produção,
// encadeia-se com o lease durável (ADR-023), que arbitra a reclamação em tempo real; aqui a
// atribuição diz o que reclamar.
type AtribuicaoDeterministica struct {
	// EstaReplica é a identidade desta réplica (a mesma string que aparece em Replicas).
	EstaReplica string
	// Replicas é o conjunto de réplicas sobre o qual se atribui. A ordem é irrelevante.
	Replicas []string
}

// Detem implementa [PosseDeParticao]. Devolve erro (fail-closed) quando não há conjunto de
// réplicas sobre o qual decidir — a incerteza recusa, tal como na porta base. Uma réplica
// que não conste do próprio conjunto simplesmente não é dona de nada (false, nil): é
// desfecho determinístico, não incerteza.
func (a AtribuicaoDeterministica) Detem(_ context.Context, particao string) (bool, error) {
	dono, ok := AtribuirParticao(particao, a.Replicas)
	if !ok {
		return false, ErrAtribuicaoSemReplicas
	}
	return dono == a.EstaReplica, nil
}

// ReplicasCanonicas devolve uma cópia ordenada e sem duplicados nem vazios de um conjunto de
// réplicas. A atribuição não depende da ordem, mas canonizar o conjunto ANTES de o registar
// (num evento, num runbook, num teste) garante que dois observadores comparam a mesma lista.
func ReplicasCanonicas(replicas []string) []string {
	vistas := make(map[string]struct{}, len(replicas))
	canon := make([]string, 0, len(replicas))
	for _, r := range replicas {
		if r == "" {
			continue
		}
		if _, ja := vistas[r]; ja {
			continue
		}
		vistas[r] = struct{}{}
		canon = append(canon, r)
	}
	sort.Strings(canon)
	return canon
}
