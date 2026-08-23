package main

import (
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------------------------
// UM WORM QUE RECUSA ESCRITAS DEIXA DE SER INVISÍVEL.
//
// Achado da verificação de completude de 2026-08-23. TRÊS caminhos fail-closed do nó dependem de
// um `Append` à hash-chain e recusam com o MESMO 503 uniforme, sem log e sem contador:
//
//	POST /runs         → sealResidency falhou      ⇒ submissão RECUSADA
//	GET  /runs/{id}    → selo de leitura sensível  ⇒ leitura RECUSADA
//	POST /dsar/hold    → sealLegalHold falhou      ⇒ legal hold RECUSADO
//
// E NADA MAIS SE MOVIA. O `/readyz` não conhecia o WORM; o `aos_worm_partitions` lê ao scrape e
// continuava em 126, porque as partições nascem por run e nenhum run novo entrava; e o SLI
// `audit_worm_integrity` também não apanhava — o [sloEvaluator.probeWORM] chama `VerifyWORM`, que
// LÊ, e classifica um erro de I/O como SEM AMOSTRA de propósito, para não chamar «adulteração» a
// um disco lento. Essa decisão está certa para o fim dela: um disco cheio ou um mount remontado
// `:ro` parte o `Append` e deixa o `Verify` a passar.
//
// Resultado: o nó recusava 100% das submissões e dos legal holds com as duas sondas a 200 e as 38
// séries planas.
//
// ---------------------------------------------------------------------------------------------
// PORQUE NÃO SE SONDA A ESCRITA, e é a primeira coisa que ocorre a quem lê isto.
//
// Escrever no WORM a cada `/readyz` poluiria a cadeia tamper-evident com registos de sonda — um
// log de auditoria cujo conteúdo maioritário é «estou vivo» deixa de servir para auditar. O que
// se observa são as escritas que REALMENTE acontecem.
//
// PORQUE NÃO UM DECORADOR SOBRE O `audit.Store`, que seria o ponto único óbvio: o `VerifyWORM`
// faz `store.(audit.PartitionLister)` para saber que partições re-encadear. Um decorador que
// expusesse SEMPRE `Partitions()` transformaria um store injectado OPACO — que hoje devolve
// [audit.ErrPartitionsUnavailable] e é honestamente declarado como não-verificável — num store
// «verificável com zero partições», e o `Verify` passaria trivialmente. Seria enfraquecer a
// verificação de integridade para acrescentar observabilidade.
//
// O SINAL APARECE EXACTAMENTE QUANDO O DANO APARECE: se nenhuma destas rotas for exercida, nada é
// recusado e não há nada a assinalar. Um nó parado não tem incidente.
//
// RESIDUAL DECLARADO: só se observam os `Append` DESTAS TRÊS VIAS. Os do fluxo DSAR e do varredor
// de retenção têm tratamento próprio (o erro sobe ao chamador e é registado lá), e contá-los aqui
// misturaria eixos com remédios diferentes.
// ---------------------------------------------------------------------------------------------

// saudeDeSelagem guarda o que se sabe sobre as escritas ao WORM feitas pelas vias de governação.
type saudeDeSelagem struct {
	falhas         atomic.Int64
	ultimaFalhaSeg atomic.Int64
	// recusando é o ÚLTIMO desfecho observado, guardado como ESTADO e não inferido de dois
	// relógios.
	//
	// A primeira versão comparava `ultimaFalhaSeg >= ultimoOKSeg`, ambos em SEGUNDOS — e uma
	// falha seguida de sucesso DENTRO DO MESMO SEGUNDO empatava. Com o `>=` o empate ficava do
	// lado de «a recusar», e o nó nunca recuperava a prontidão: um soluço de disco tirava-o de
	// rotação até alguém o reiniciar. Foi um teste que o apanhou, não uma leitura.
	//
	// Duas selagens concorrentes com desfechos opostos deixam o último escritor a ganhar. É uma
	// corrida inócua: a selagem seguinte corrige, e a alternativa — um lock no caminho de
	// auditoria — custaria mais do que a precisão que compra.
	recusando atomic.Bool
}

// registar anota o desfecho de UM `Append` e devolve o erro tal e qual, para poder envolver a
// chamada sem alterar o fluxo de quem chama.
func (s *saudeDeSelagem) registar(err error) error {
	if err != nil {
		s.falhas.Add(1)
		s.ultimaFalhaSeg.Store(time.Now().Unix())
		s.recusando.Store(true)
		return err
	}
	s.recusando.Store(false)
	return nil
}

// aRecusarEscritas diz se a ÚLTIMA selagem observada falhou.
//
// Não é «alguma vez falhou»: um contador cumulativo não distingue «três falhas há uma hora, agora
// bem» de «a falhar agora», e é a segunda que importa para a prontidão. Uma selagem bem-sucedida
// posterior limpa o estado — auto-curativo, sem intervenção.
func (s *saudeDeSelagem) aRecusarEscritas() bool {
	return s.recusando.Load()
}
