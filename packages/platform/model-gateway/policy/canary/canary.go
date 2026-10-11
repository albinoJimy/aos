// Package canary é a FUNÇÃO DE ESCOLHA do canary de modelos (AOS-520): dado o identificador de um
// plano e a fatia assinada, diz se o plano é servido pelo titular ou pelo candidato.
//
// É uma função PURA. Não lê o relógio, não guarda estado, não usa aleatoriedade, não lê o ambiente
// e não depende de mais nada do nó: a mesma entrada dá a mesma escolha em qualquer arranque e em
// qualquer réplica. O pacote só importa a biblioteca-padrão.
//
// # A função
//
//	balde = uint64_be( SHA-256( "aos-canary/v1" || 0x00 || chave )[0:8] ) mod 10000
//	candidato  sse  balde < fatia × 100
//
// `chave` é o identificador do plano, byte a byte, sem normalização nenhuma (nem caixa, nem
// espaços, nem corte por separador). `fatia` é a percentagem inteira do registo assinado, de 0 a
// 100. O byte 0x00 separa o domínio da chave; o prefixo de domínio leva a versão, pelo que uma
// função futura que reparta de outra maneira é OUTRA versão e não confunde os seus baldes com os
// desta.
//
// # Propriedades
//
//   - 0% ⇒ nenhum plano é do candidato; 100% ⇒ todos.
//   - MONÓTONA: o balde de um plano não depende da fatia, pelo que subir a fatia nunca tira ao
//     candidato um plano que ele já tinha — só lhe acrescenta.
//   - Fail-closed nas entradas que não deviam existir: uma fatia fora de 0..100 e uma chave vazia
//     dão sempre o titular.
//
// # O que a função NÃO garante
//
// Não tem segredo. Quem escolhe o identificador de um plano pode calcular o balde antes de o
// submeter e escolher um identificador que caia de um lado ou do outro. É um limite aceite e
// escrito no ADR do AOS-520: o canary mede um modelo, não defende de quem dá o nome aos planos.
//
// O que a função decide é só a REPARTIÇÃO. Quantos planos o candidato já serviu, se o canary
// ainda está aberto e se um plano já tinha sido atribuído são do livro durável do nó, que chama
// esta função uma vez por plano e grava o resultado.
package canary

import (
	"crypto/sha256"
	"encoding/binary"
)

// Version identifica esta função de escolha. É o que fica gravado, ao lado de cada escolha, como
// «quem fixou o modelo deste run»: um run fixado por outra versão não foi repartido por esta.
const Version = "aos-canary/v1"

// dominio é o prefixo de separação de domínio do resumo: a versão e um byte nulo. Mudá-lo muda o
// balde de todos os planos — os vectores de teste do ADR avermelham.
const dominio = Version + "\x00"

// Buckets é o número de baldes: a fatia tem a resolução de um centésimo de ponto percentual, e
// cada ponto percentual são cem baldes.
const Buckets = 10000

// bucketsPerPercent é quantos baldes vale um ponto percentual da fatia.
const bucketsPerPercent = Buckets / 100

// Role é o papel do modelo que serve um plano.
type Role string

const (
	// RoleIncumbent é o titular: o modelo que o nó pede por omissão.
	RoleIncumbent Role = "titular"
	// RoleCandidate é o candidato: o modelo do registo de entrada assinado, na sua fatia.
	RoleCandidate Role = "candidato"
)

// Bucket devolve o balde de um plano, de 0 a [Buckets]-1. Só depende da chave.
func Bucket(planKey string) uint64 {
	h := sha256.Sum256([]byte(dominio + planKey))
	return binary.BigEndian.Uint64(h[:8]) % Buckets
}

// Choose diz quem serve o plano de chave `planKey` com a fatia `sharePercent` (0 a 100).
//
// Uma fatia fora de 0..100, ou uma chave vazia, devolvem o titular: a função não inventa uma
// repartição para uma entrada que quem a chama devia ter recusado.
func Choose(planKey string, sharePercent int) Role {
	if planKey == "" || sharePercent <= 0 || sharePercent > 100 {
		return RoleIncumbent
	}
	if Bucket(planKey) < uint64(sharePercent)*bucketsPerPercent {
		return RoleCandidate
	}
	return RoleIncumbent
}
