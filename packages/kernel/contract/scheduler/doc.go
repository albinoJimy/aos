// Package scheduler contém os contratos canónicos das portas do Escalonador
// (admission control e degradação graciosa) partilháveis com o Model Gateway e
// outros componentes do data-plane.
//
// Vive no kernel para ser importável pelo control-plane (implementação) e pelos
// adaptadores finos do data-plane, sem inversões platform → control-plane.
package scheduler
