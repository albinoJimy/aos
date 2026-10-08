// Package wiretest é a PORTA PÚBLICA dos providers falsos de wire do Model Gateway
// (`internal/wirefake`, AOS-508) para módulos-folha de teste e de ensaio que vivem FORA do
// módulo do gateway — o banco de ensaio (`packages/qa/banco-ensaio`, AOS-512).
//
// Existe porque `internal/` só é importável de dentro do módulo, e o banco tem de servir os
// MESMOS corpos e o MESMO falso de dois turnos que os testes do gateway usam, sem os copiar.
// Não acrescenta nada: reexporta.
//
// SÓ PARA TESTES E ENSAIO. Nenhum ficheiro de produção do gateway o importa
// (TestAOS508_NenhumCodigoDeProducaoImportaOsFalsos), e nenhum binário do nó o contém
// (TestAOS512_ONoNaoContemOBanco, no banco).
package wiretest

import "github.com/aos-ref/platform/model-gateway/internal/wirefake"

// Nomes devolve os nomes de todos os casos de wire, por ordem alfabética.
func Nomes() []string { return wirefake.Nomes() }

// Corpo devolve o corpo de resposta de um caso. Um nome que não existe entra em pânico.
func Corpo(nome string) []byte { return wirefake.Corpo(nome) }

// Validador é o provider falso de dois turnos que exige, ao segundo, o que emitiu no primeiro.
type Validador = wirefake.Validador

// Exigente é o provider falso que exige de volta, byte a byte, o estado opaco que emitiu em cada
// turno com tools — ou que, com Proibe, recusa qualquer estado no pedido (AOS-515). O banco de
// ensaio usa-o para qualificar a devolução do estado (AOS-516).
type Exigente = wirefake.Exigente

// IDEmitidoNoTurno devolve o id que o [Exigente] dá à tool call do turno n (de 0).
func IDEmitidoNoTurno(n int) string { return wirefake.IDEmitidoNoTurno(n) }

// Exigencia é o que um [Validador] exige do segundo pedido.
type Exigencia = wirefake.Exigencia

// As exigências de [Validador].
const (
	ExigeIDEmitido  = wirefake.ExigeIDEmitido
	ExigeRaciocinio = wirefake.ExigeRaciocinio
	ExigeAssinatura = wirefake.ExigeAssinatura
)
